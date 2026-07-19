package deliverability

import (
	"context"
	"strings"
	"time"

	"github.com/manishkumar/outreachcrm/internal/dnscheck"
)

// Config controls optional expensive probes.
type Config struct {
	SMTPVerify          bool
	BlacklistCheck      bool
	MaxBounceProb       float64 // suppress at or above (default 70)
	MaxSpamTrapRisk     float64 // delay/suppress (default 75)
	MinEngagement       float64 // delay below (default 12) when history exists
	MaxContentRisk      float64 // suppress above (default 80)
	MaxBounceRatePct    float64 // pause threshold (default 2)
	MaxComplaintRatePct float64 // pause threshold (default 0.1)
	RequireAuth         bool    // require SPF+DKIM+DMARC on sending domain
	OptimizeSendTime    bool
}

func DefaultConfig() Config {
	return Config{
		SMTPVerify:          false,
		BlacklistCheck:      true,
		MaxBounceProb:       70,
		MaxSpamTrapRisk:     75,
		MinEngagement:       12,
		MaxContentRisk:      80,
		MaxBounceRatePct:    2.0,
		MaxComplaintRatePct: 0.1,
		RequireAuth:         false, // soft fail by default — many SMTP users aren't fully set up yet
		OptimizeSendTime:    true,
	}
}

// Engine is the deliverability orchestrator between CRM and SMTP.
type Engine struct {
	Cfg Config
}

func New(cfg Config) *Engine {
	if cfg.MaxBounceProb == 0 {
		cfg = DefaultConfig()
	}
	return &Engine{Cfg: cfg}
}

// Evaluate runs layers 1–14 and returns Send / Delay / Suppress.
func (e *Engine) Evaluate(ctx context.Context, in Input) Decision {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	d := Decision{Action: ActionSend, Email: strings.TrimSpace(strings.ToLower(in.Email))}

	if in.WorkspaceHealth.CampaignPaused {
		d.Action = ActionDelay
		until := now.Add(1 * time.Hour)
		d.DelayUntil = &until
		d.Reasons = append(d.Reasons, "campaign paused for deliverability")
		return d
	}

	// Layer 11 — complaint / bounce monitoring
	if in.WorkspaceHealth.Sent7d >= 20 {
		if in.WorkspaceHealth.BounceRatePct >= e.Cfg.MaxBounceRatePct {
			d.Action = ActionDelay
			until := now.Add(6 * time.Hour)
			d.DelayUntil = &until
			d.Reasons = append(d.Reasons, "workspace bounce rate above threshold")
			return d
		}
		if in.WorkspaceHealth.ComplaintPct >= e.Cfg.MaxComplaintRatePct {
			d.Action = ActionSuppress
			d.Reasons = append(d.Reasons, "workspace complaint rate above threshold")
			return d
		}
	}

	// Layer 1 — validation
	if !ValidateSyntax(d.Email) {
		d.Action = ActionSuppress
		d.BounceProb = 100
		d.Reasons = append(d.Reasons, "invalid email syntax")
		return d
	}
	local, domain, ok := SplitEmail(d.Email)
	if !ok {
		d.Action = ActionSuppress
		d.BounceProb = 100
		d.Reasons = append(d.Reasons, "cannot parse email")
		return d
	}
	d.Domain = domain
	if fix := SuggestTypo(domain); fix != "" {
		d.SuggestedDomain = fix
		d.Reasons = append(d.Reasons, "possible typo; suggested "+fix)
	}
	disposable := IsDisposable(domain)
	role := IsRoleBased(local)
	if disposable {
		d.Action = ActionSuppress
		d.BounceProb = 98
		d.Reasons = append(d.Reasons, "disposable email domain")
		return d
	}
	if role {
		d.Reasons = append(d.Reasons, "role-based address")
	}
	hasMX, mxNote := DomainHasMailExchanger(domain)
	if !hasMX {
		d.Action = ActionSuppress
		d.BounceProb = 100
		d.Reasons = append(d.Reasons, "domain has no MX/A/AAAA ("+mxNote+")")
		return d
	}

	mxHost := MXHost(domain)
	d.ISP = ClassifyISP(domain, mxHost)

	// Layer 13 — auth on *sending* domain
	auth := in.SenderAuth
	if in.SendingDomain != "" && (auth.Detail == "" && !auth.SPF && !auth.DKIM && !auth.DMARC) {
		dc := dnscheck.Check(in.SendingDomain)
		auth.SPF, auth.DKIM, auth.DMARC = dc.SPF, dc.DKIM, dc.DMARC
		auth.Detail = dc.Detail
	}
	d.AuthOK = auth.SPF && auth.DKIM && auth.DMARC
	if auth.Blacklisted && len(auth.Zones) > 0 {
		d.Reasons = append(d.Reasons, "sending IP on DNSBL: "+strings.Join(auth.Zones, ","))
	}
	if e.Cfg.RequireAuth && in.SendingDomain != "" && !d.AuthOK {
		d.Action = ActionDelay
		until := now.Add(2 * time.Hour)
		d.DelayUntil = &until
		d.Reasons = append(d.Reasons, "sending domain auth incomplete (SPF/DKIM/DMARC)")
		return d
	}

	// Layer 12 — blacklist (optional)
	if e.Cfg.BlacklistCheck && !in.SkipBlacklist && in.SendingDomain != "" {
		// check MX of sending domain IPs lightly — use domain name resolution
		ips := ResolveSendingIPs(in.SendingDomain)
		for _, ip := range ips {
			listed, zones := CheckBlacklists(ctx, ip)
			if listed {
				auth.Blacklisted = true
				auth.Zones = zones
				d.Reasons = append(d.Reasons, "sending IP on DNSBL: "+strings.Join(zones, ","))
				break
			}
		}
	}

	// Recipient domain score must not include sending-domain SPF/DKIM/DMARC.
	d.DomainScore = ScoreDomain(domain, AuthStatus{}, mxHost)
	d.RecipientScore = ScoreRecipient(in.Recipient)

	// Layer 2 — SMTP verify (optional)
	smtpReject, smtpUnknown := false, true
	if e.Cfg.SMTPVerify && !in.SkipSMTPVerify && mxHost != "" {
		smtpUnknown = false
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		vr := VerifySMTP(cctx, mxHost, "verify@"+in.SendingDomain, d.Email)
		cancel()
		if vr.Reject {
			smtpReject = true
			d.Reasons = append(d.Reasons, "SMTP RCPT rejected: "+vr.Detail)
		} else if vr.Accept {
			d.Reasons = append(d.Reasons, "SMTP RCPT accepted")
		} else {
			smtpUnknown = true
			d.Reasons = append(d.Reasons, "SMTP verify inconclusive")
		}
	}

	// Layers 5–7
	d.BounceProb = PredictBounce(true, hasMX, disposable, role, d.SuggestedDomain, in.Recipient, d.DomainScore, smtpReject, smtpUnknown)
	d.SpamTrapRisk = SpamTrapRisk(in.Recipient, "", d.DomainScore)
	if in.Recipient.PurchasedList {
		d.SpamTrapRisk = SpamTrapRisk(in.Recipient, "purchased", d.DomainScore)
	}

	// Layer 14 — content
	contentRisk, contentReasons := AnalyzeContent(in.Subject, in.Body)
	d.ContentRisk = contentRisk
	d.Reasons = append(d.Reasons, contentReasons...)
	d.EngagementProb = PredictEngagement(in.Recipient, role, contentRisk)

	// Hard suppress rules (layer 16 precursors)
	if disposable || smtpReject || in.Recipient.HardBounces > 0 || in.Recipient.Complaints > 0 {
		d.Action = ActionSuppress
		if disposable {
			d.Reasons = append(d.Reasons, "auto-suppress disposable")
		}
		if in.Recipient.HardBounces > 0 {
			d.Reasons = append(d.Reasons, "prior hard bounce")
		}
		if in.Recipient.Complaints > 0 {
			d.Reasons = append(d.Reasons, "prior spam complaint")
		}
		return d
	}
	if in.Recipient.Unsubscribes > 0 {
		d.Action = ActionSuppress
		d.Reasons = append(d.Reasons, "prior unsubscribe")
		return d
	}
	if in.Recipient.SoftBounces >= 3 {
		d.Action = ActionSuppress
		d.Reasons = append(d.Reasons, "multiple soft bounces")
		return d
	}
	if in.AccountWarmup && d.ContentRisk >= 45 {
		d.Action = ActionDelay
		until := now.Add(4 * time.Hour)
		d.DelayUntil = &until
		d.Reasons = append(d.Reasons, "warmup account — cooler content recommended")
		return d
	}
	if d.BounceProb >= e.Cfg.MaxBounceProb {
		d.Action = ActionSuppress
		d.Reasons = append(d.Reasons, "bounce probability too high")
		return d
	}
	if d.ContentRisk >= e.Cfg.MaxContentRisk {
		d.Action = ActionSuppress
		d.Reasons = append(d.Reasons, "content risk too high")
		return d
	}
	if auth.Blacklisted {
		d.Action = ActionDelay
		until := now.Add(12 * time.Hour)
		d.DelayUntil = &until
		d.Reasons = append(d.Reasons, "delay: blacklist remediation needed")
		return d
	}
	if d.SpamTrapRisk >= e.Cfg.MaxSpamTrapRisk {
		d.Action = ActionDelay
		until := now.Add(24 * time.Hour)
		d.DelayUntil = &until
		d.Reasons = append(d.Reasons, "spam-trap risk; delay and review list hygiene")
		return d
	}
	if in.Recipient.Sent >= 2 && d.EngagementProb < e.Cfg.MinEngagement {
		d.Action = ActionDelay
		until := now.Add(48 * time.Hour)
		d.DelayUntil = &until
		d.Reasons = append(d.Reasons, "low engagement prediction; cool-down")
		return d
	}
	if role && d.EngagementProb < 30 {
		d.Action = ActionDelay
		until := now.Add(6 * time.Hour)
		d.DelayUntil = &until
		d.Reasons = append(d.Reasons, "role mailbox — defer and personalize")
		return d
	}

	// Layer 8 — send-time optimization (only when outside daytime hours)
	if e.Cfg.OptimizeSendTime {
		h := now.Hour()
		if h < 8 || h > 18 {
			slot := NextSendSlot(d.Email, now)
			if slot.After(now.Add(2 * time.Minute)) {
				d.Action = ActionDelay
				d.DelayUntil = &slot
				d.Reasons = append(d.Reasons, "send-time optimization")
				return d
			}
		}
	}

	d.Reasons = append(d.Reasons, "cleared for send")
	return d
}

// QuickValidate is for UI / lead import without full send context.
func (e *Engine) QuickValidate(ctx context.Context, email string) Decision {
	return e.Evaluate(ctx, Input{
		Email:          email,
		SkipSMTPVerify: !e.Cfg.SMTPVerify,
		SkipBlacklist:  true,
		Now:            time.Now().UTC(),
	})
}
