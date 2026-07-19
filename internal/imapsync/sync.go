package imapsync

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-sasl"
	"github.com/manishkumar/outreachcrm/internal/crypto"
	"github.com/manishkumar/outreachcrm/internal/inbox"
	"github.com/manishkumar/outreachcrm/internal/models"
	"github.com/manishkumar/outreachcrm/internal/oauth"
	"github.com/manishkumar/outreachcrm/internal/store"
)

type Worker struct {
	Store    *store.Store
	Box      *crypto.Box
	OAuth    *oauth.Managers
	Classify *inbox.Service
	Interval time.Duration
}

func (w *Worker) Run(ctx context.Context) {
	if w.Interval <= 0 {
		w.Interval = 2 * time.Minute
	}
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	w.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	accounts, err := w.Store.ListOAuthAccounts()
	if err != nil {
		slog.Error("imap list accounts", "err", err)
		return
	}
	for _, a := range accounts {
		if a.IMAPHost == "" {
			continue
		}
		if err := w.syncAccount(ctx, a); err != nil {
			slog.Error("imap sync", "account", a.Email, "err", err)
		}
	}
}

func (w *Worker) syncAccount(ctx context.Context, a models.EmailAccount) error {
	access, pass, err := w.creds(ctx, &a)
	if err != nil {
		return err
	}
	addr := fmt.Sprintf("%s:%d", a.IMAPHost, a.IMAPPort)
	var c *client.Client
	if a.IMAPPort == 993 {
		c, err = client.DialTLS(addr, &tls.Config{ServerName: a.IMAPHost, MinVersion: tls.VersionTLS12})
	} else {
		c, err = client.Dial(addr)
		if err == nil {
			if err = c.StartTLS(&tls.Config{ServerName: a.IMAPHost, MinVersion: tls.VersionTLS12}); err != nil {
				c.Logout()
				return err
			}
		}
	}
	if err != nil {
		return err
	}
	defer c.Logout()

	switch a.Provider {
	case models.ProviderGoogle, models.ProviderMicrosoft:
		var authErr error
		// Prefer OAUTHBEARER; fall back to XOAUTH2 for Gmail/legacy.
		authErr = c.Authenticate(sasl.NewOAuthBearerClient(&sasl.OAuthBearerOptions{
			Username: a.Email,
			Token:    access,
		}))
		if authErr != nil {
			authErr = c.Authenticate(newXOAUTH2Client(a.Email, access))
		}
		if authErr != nil {
			return fmt.Errorf("imap oauth: %w", authErr)
		}
	default:
		user := a.Username
		if user == "" {
			user = a.Email
		}
		if err := c.Login(user, pass); err != nil {
			return err
		}
	}

	mbox, err := c.Select("INBOX", false)
	if err != nil {
		return err
	}
	if mbox.Messages == 0 {
		return nil
	}

	from := a.IMAPLastUID + 1
	if from == 1 {
		// first sync: only last 20 messages
		if mbox.Messages > 20 {
			fromUID := mbox.UidNext
			if fromUID > 20 {
				from = fromUID - 20
			}
		}
	}

	seqset := new(imap.SeqSet)
	seqset.AddRange(from, 0)

	section := &imap.BodySectionName{}
	items := []imap.FetchItem{imap.FetchEnvelope, imap.FetchUid, section.FetchItem()}
	messages := make(chan *imap.Message, 20)
	done := make(chan error, 1)
	go func() {
		done <- c.UidFetch(seqset, items, messages)
	}()

	var maxUID uint32 = a.IMAPLastUID
	for msg := range messages {
		if msg == nil {
			continue
		}
		if msg.Uid > maxUID {
			maxUID = msg.Uid
		}
		if err := w.ingest(ctx, a, msg, section); err != nil {
			slog.Warn("imap ingest", "uid", msg.Uid, "err", err)
		}
	}
	if err := <-done; err != nil {
		return err
	}
	if maxUID > a.IMAPLastUID {
		return w.Store.SetIMAPLastUID(a.ID, maxUID)
	}
	return nil
}

func (w *Worker) ingest(ctx context.Context, account models.EmailAccount, msg *imap.Message, section *imap.BodySectionName) error {
	r := msg.GetBody(section)
	if r == nil {
		return nil
	}
	mr, err := mail.CreateReader(r)
	if err != nil {
		return err
	}
	header := mr.Header
	fromList, _ := header.AddressList("From")
	fromEmail := ""
	if len(fromList) > 0 {
		fromEmail = fromList[0].Address
	}
	subject, _ := header.Subject()
	messageID, _ := header.MessageID()
	var body strings.Builder
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch p.Header.(type) {
		case *mail.InlineHeader:
			b, _ := io.ReadAll(io.LimitReader(p.Body, 64*1024))
			body.Write(b)
		}
	}
	text := strings.TrimSpace(body.String())
	if text == "" {
		return nil
	}

	intent, err := w.Classify.Classify(ctx, text)
	if err != nil {
		intent = "other"
	}
	lead, lerr := w.Store.FindLeadByEmail(fromEmail)
	var leadID *int64
	leadName := ""
	ownerID := account.OwnerID
	if lerr == nil {
		leadID = &lead.ID
		leadName = lead.Name
		if lead.OwnerID > 0 {
			ownerID = lead.OwnerID
		}
	}
	oid := ownerID
	_, err = w.Store.CreateReply(models.InboundReply{
		OwnerID:   &oid,
		LeadID:    leadID,
		LeadName:  leadName,
		FromEmail: fromEmail,
		Subject:   subject,
		Body:      text,
		Intent:    intent,
		MessageID: messageID,
	})
	if err != nil && strings.Contains(err.Error(), "duplicate") {
		return nil
	}
	if err != nil {
		return err
	}
	if fromEmail != "" {
		ws := w.Store.WorkspaceIDForEmail(fromEmail)
		if intent == "unsubscribe" {
			_ = w.Store.AddSuppressionWS(ws, fromEmail, "unsubscribe")
			_ = w.Store.RecordRecipientEvent(ws, fromEmail, "unsubscribe")
		}
		if intent == "positive" || intent == "neutral" {
			_ = w.Store.RecordRecipientEvent(ws, fromEmail, "replied")
		}
		w.Store.MarkOutboundReplied(fromEmail)
	}
	return nil
}

func (w *Worker) creds(ctx context.Context, a *models.EmailAccount) (access, pass string, err error) {
	switch a.Provider {
	case models.ProviderGoogle, models.ProviderMicrosoft:
		access, err = w.Box.Decrypt(a.AccessTokenEnc)
		if err != nil {
			return "", "", err
		}
		refresh, _ := w.Box.Decrypt(a.RefreshTokenEnc)
		needRefresh := a.TokenExpiry == nil || a.TokenExpiry.Before(time.Now().UTC().Add(2*time.Minute))
		if needRefresh && refresh != "" && w.OAuth != nil {
			cfg, err := w.OAuth.ConfigFor(a.Provider)
			if err != nil {
				return "", "", err
			}
			tok, err := oauth.Refresh(ctx, cfg, refresh)
			if err != nil {
				return "", "", err
			}
			access = tok.AccessToken
			ae, _ := w.Box.Encrypt(tok.AccessToken)
			re := a.RefreshTokenEnc
			if tok.RefreshToken != "" {
				re, _ = w.Box.Encrypt(tok.RefreshToken)
			}
			_ = w.Store.UpdateAccountTokens(a.ID, ae, re, oauth.TokenExpiry(tok))
		}
		return access, "", nil
	default:
		pass, err = w.Box.Decrypt(a.PasswordEnc)
		if err != nil {
			return "", a.PasswordEnc, nil
		}
		return "", pass, nil
	}
}
