package sequencing

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/manishkumar/outreachcrm/internal/auth"
	"github.com/manishkumar/outreachcrm/internal/crypto"
	"github.com/manishkumar/outreachcrm/internal/deliverability"
	"github.com/manishkumar/outreachcrm/internal/mail"
	"github.com/manishkumar/outreachcrm/internal/models"
	"github.com/manishkumar/outreachcrm/internal/oauth"
	"github.com/manishkumar/outreachcrm/internal/store"
	"github.com/manishkumar/outreachcrm/internal/writing"
)

type Worker struct {
	Store           *store.Store
	Sender          *mail.Sender
	Box             *crypto.Box
	OAuth           *oauth.Managers
	Auth            *auth.Manager
	Deliverability  *deliverability.Engine
	PublicBaseURL   string
	Interval        time.Duration
	Batch           int
	MaxAttempts     int
	OwnerID         string
}

func (w *Worker) Run(ctx context.Context) {
	if w.Interval <= 0 {
		w.Interval = 30 * time.Second
	}
	if w.Batch <= 0 {
		w.Batch = 10
	}
	if w.MaxAttempts <= 0 {
		w.MaxAttempts = 5
	}
	if w.OwnerID == "" {
		host, _ := os.Hostname()
		w.OwnerID = fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	if w.Deliverability == nil {
		w.Deliverability = deliverability.New(deliverability.DefaultConfig())
	}
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	day := time.NewTicker(24 * time.Hour)
	defer day.Stop()
	w.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tick(ctx)
		case <-day.C:
			_ = w.Store.BumpWarmupDays()
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	msgs, err := w.Store.ClaimDueMessages(w.Batch, w.OwnerID, 2*time.Minute)
	if err != nil {
		slog.Error("sequencer claim", "err", err)
		return
	}
	for _, m := range msgs {
		if err := w.process(ctx, m); err != nil {
			slog.Error("sequencer process", "id", m.ID, "err", err)
		}
	}
}

func (w *Worker) process(ctx context.Context, msg models.OutboundMessage) error {
	camp, err := w.Store.GetCampaign(msg.CampaignID)
	if err != nil {
		return err
	}
	if camp.Status != "active" {
		return nil
	}
	if camp.DeliverabilityPaused {
		return w.Store.RescheduleMessageAt(msg.ID, time.Now().UTC().Add(time.Hour), "campaign deliverability paused")
	}
	if !w.Store.InWindow(camp, time.Now()) {
		return w.Store.FailMessageRetry(msg.ID, "outside send window", w.MaxAttempts+10, 15*time.Minute)
	}

	sup, _ := w.Store.IsSuppressed(msg.ToEmail)
	if sup {
		return w.Store.DeadLetterMessage(msg.ID, "suppressed")
	}

	sentToday, err := w.Store.CountCampaignSentToday(msg.CampaignID)
	if err != nil {
		return err
	}
	if camp.DailySendLimit > 0 && sentToday >= camp.DailySendLimit {
		return w.Store.FailMessageRetry(msg.ID, "campaign daily limit", w.MaxAttempts+10, 30*time.Minute)
	}

	account, err := w.Store.PickAccount(camp.OwnerID, false)
	if err != nil {
		backoff := time.Duration(1<<minInt(msg.Attempts, 4)) * time.Minute
		if err == sql.ErrNoRows {
			return w.Store.FailMessageRetry(msg.ID, "no account with quota", w.MaxAttempts, backoff)
		}
		return w.Store.FailMessageRetry(msg.ID, err.Error(), w.MaxAttempts, backoff)
	}

	eff := mail.EffectiveDailyQuota(account)
	if account.SentToday >= eff {
		return w.Store.FailMessageRetry(msg.ID, "account warmup/quota", w.MaxAttempts+10, 20*time.Minute)
	}
	if account.Domain != "" && account.DomainDailyLimit > 0 {
		n, _ := w.Store.CountDomainSentToday(account.Domain)
		if n >= account.DomainDailyLimit {
			return w.Store.FailMessageRetry(msg.ID, "domain daily limit", w.MaxAttempts+10, 30*time.Minute)
		}
	}

	lead, err := w.Store.GetLead(msg.LeadID)
	if err != nil {
		return w.Store.FailMessageRetry(msg.ID, err.Error(), w.MaxAttempts, time.Minute)
	}

	subject, body := msg.Subject, msg.Body
	if camp.ABEnabled && msg.Variant == "b" {
		steps, _ := w.Store.ListSteps(msg.CampaignID)
		for _, st := range steps {
			if st.StepOrder == msg.StepOrder && st.VariantBSubject != "" {
				subject, body = st.VariantBSubject, st.VariantBBody
				break
			}
		}
	}
	subject, body = writing.PersonalizeLead(subject, body, lead)
	if w.Auth != nil && w.PublicBaseURL != "" {
		tok := w.Auth.SignUnsubscribe(msg.LeadID, msg.CampaignID)
		body += "\n\n---\nUnsubscribe: " + w.PublicBaseURL + "/u/" + tok
	}

	// Email Deliverability Engine — gate before SMTP
	hist := w.Store.GetRecipientHistory(msg.ToEmail)
	src := strings.ToLower(lead.Source)
	if strings.Contains(src, "purchas") || strings.Contains(src, "bought") || strings.Contains(src, "scrape") {
		hist.PurchasedList = true
	}
	health := w.Store.WorkspaceHealth(camp.WorkspaceID)
	health.CampaignPaused = camp.DeliverabilityPaused
	sendDomain := account.Domain
	if sendDomain == "" {
		if _, d, ok := deliverability.SplitEmail(account.Email); ok {
			sendDomain = d
		}
	}
	decision := w.Deliverability.Evaluate(ctx, deliverability.Input{
		Email:          msg.ToEmail,
		Subject:        subject,
		Body:           body,
		SendingDomain:  sendDomain,
		CampaignID:     camp.ID,
		WorkspaceID:    camp.WorkspaceID,
		Recipient:      hist,
		WorkspaceHealth: health,
		Now:            time.Now().UTC(),
		SkipBlacklist:  true, // DNSBL is on-demand via /deliverability (too slow for send hot path)
		SkipSMTPVerify: !w.Deliverability.Cfg.SMTPVerify,
	})
	w.Store.LogDeliverabilityDecision(camp.WorkspaceID, camp.ID, decision)

	switch decision.Action {
	case deliverability.ActionSuppress:
		_ = w.Store.AddSuppressionWS(camp.WorkspaceID, msg.ToEmail, "deliverability")
		return w.Store.DeadLetterMessage(msg.ID, "deliverability: "+strings.Join(decision.Reasons, "; "))
	case deliverability.ActionDelay:
		when := time.Now().UTC().Add(30 * time.Minute)
		if decision.DelayUntil != nil {
			when = *decision.DelayUntil
		}
		return w.Store.RescheduleMessageAt(msg.ID, when, "deliverability: "+strings.Join(decision.Reasons, "; "))
	}

	// Layer 10 — ISP throttling
	isp := decision.ISP
	if isp == "" {
		isp = "other"
	}
	win := time.Duration(deliverability.ISPWindowMinutes(isp)) * time.Minute
	if n := w.Store.CountISPSentSince(camp.WorkspaceID, isp, time.Now().UTC().Add(-win)); n >= deliverability.ISPBurstLimit(isp) {
		return w.Store.RescheduleMessageAt(msg.ID, time.Now().UTC().Add(win), "isp throttle: "+isp)
	}

	// Auto-pause if health thresholds breached
	_ = w.Store.PauseHotCampaigns(camp.WorkspaceID, w.Deliverability.Cfg.MaxBounceRatePct, w.Deliverability.Cfg.MaxComplaintRatePct)

	claimed, err := w.Store.ClaimMessage(msg.ID)
	if err != nil || !claimed {
		return err
	}

	access, smtpPass, espKey, err := w.resolveCredentials(ctx, &account)
	if err != nil {
		backoff := time.Duration(1<<minInt(msg.Attempts, 4)) * time.Minute
		return w.Store.FailMessageRetry(msg.ID, err.Error(), w.MaxAttempts, backoff)
	}

	mid := msg.MessageID
	if mid == "" {
		mid = newMessageID()
	}

	if err := w.Sender.Send(account, access, smtpPass, espKey, msg.ToEmail, subject, body, mid); err != nil {
		backoff := time.Duration(1<<minInt(msg.Attempts, 4)) * time.Minute
		return w.Store.FailMessageRetry(msg.ID, err.Error(), w.MaxAttempts, backoff)
	}
	if err := w.Store.MarkMessageSent(msg.ID, account.ID, subject, body); err != nil {
		return err
	}
	_ = w.Store.SetMessageMeta(msg.ID, mid, msg.Variant)
	if err := w.Store.MarkAccountSent(account.ID); err != nil {
		return err
	}
	w.Store.RecordISPSend(camp.WorkspaceID, isp)
	_ = w.Store.RecordRecipientEvent(camp.WorkspaceID, msg.ToEmail, "sent")
	return w.Store.ScheduleNextStep(msg)
}

func (w *Worker) resolveCredentials(ctx context.Context, account *models.EmailAccount) (access, smtpPass, espKey string, err error) {
	switch account.Provider {
	case models.ProviderPostmark, models.ProviderSES:
		espKey, err = w.Box.Decrypt(account.ESPAPIKeyEnc)
		if err != nil {
			espKey = account.ESPAPIKeyEnc
			err = nil
		}
		pass, _ := w.Box.Decrypt(account.PasswordEnc)
		return "", pass, espKey, nil
	case models.ProviderGoogle, models.ProviderMicrosoft:
		refresh, err := w.Box.Decrypt(account.RefreshTokenEnc)
		if err != nil {
			return "", "", "", err
		}
		access, err = w.Box.Decrypt(account.AccessTokenEnc)
		if err != nil {
			return "", "", "", err
		}
		needRefresh := account.TokenExpiry == nil || account.TokenExpiry.Before(time.Now().UTC().Add(2*time.Minute))
		if needRefresh && refresh != "" && w.OAuth != nil {
			cfg, err := w.OAuth.ConfigFor(account.Provider)
			if err != nil {
				return "", "", "", err
			}
			tok, err := oauth.Refresh(ctx, cfg, refresh)
			if err != nil {
				return "", "", "", err
			}
			access = tok.AccessToken
			accessEnc, _ := w.Box.Encrypt(tok.AccessToken)
			refEnc := account.RefreshTokenEnc
			if tok.RefreshToken != "" {
				refEnc, _ = w.Box.Encrypt(tok.RefreshToken)
			}
			_ = w.Store.UpdateAccountTokens(account.ID, accessEnc, refEnc, oauth.TokenExpiry(tok))
		}
		return access, "", "", nil
	default:
		pass, err := w.Box.Decrypt(account.PasswordEnc)
		if err != nil {
			if _, berr := base64.StdEncoding.DecodeString(account.PasswordEnc); berr != nil || len(account.PasswordEnc) < 32 {
				return "", account.PasswordEnc, "", nil
			}
			return "", "", "", err
		}
		return "", pass, "", nil
	}
}

func newMessageID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b) + "@outreachcrm.local"
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
