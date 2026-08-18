package imapsync

import "strings"

type Preset struct {
	ID           string
	Label        string
	IMAPHost     string
	IMAPPort     int
	SMTPHost     string
	SMTPPort     int
	Hint         string
	NeedsAppPass bool
}

func Presets() []Preset {
	return []Preset{
		{ID: "titan", Label: "Titan Email", IMAPHost: "imap.titan.email", IMAPPort: 993, SMTPHost: "smtp.titan.email", SMTPPort: 465,
			Hint: "Use your full email address and mailbox password."},
		{ID: "hostinger", Label: "Hostinger Mail", IMAPHost: "imap.hostinger.com", IMAPPort: 993, SMTPHost: "smtp.hostinger.com", SMTPPort: 465,
			Hint: "Use your full email address and mailbox password from hPanel."},
		{ID: "zoho", Label: "Zoho Mail (US / .com)", IMAPHost: "imap.zoho.com", IMAPPort: 993, SMTPHost: "smtp.zoho.com", SMTPPort: 465,
			Hint: "Enable IMAP in Zoho Mail. Prefer an app password if 2FA is on.", NeedsAppPass: true},
		{ID: "zoho_in", Label: "Zoho Mail India (.in)", IMAPHost: "imap.zoho.in", IMAPPort: 993, SMTPHost: "smtp.zoho.in", SMTPPort: 465,
			Hint: "India data center. Wrong region = auth failures.", NeedsAppPass: true},
		{ID: "zoho_eu", Label: "Zoho Mail Europe (.eu)", IMAPHost: "imap.zoho.eu", IMAPPort: 993, SMTPHost: "smtp.zoho.eu", SMTPPort: 465,
			Hint: "EU data center. Enable IMAP + app password when 2FA is on.", NeedsAppPass: true},
		{ID: "gmail", Label: "Gmail (app password)", IMAPHost: "imap.gmail.com", IMAPPort: 993, SMTPHost: "smtp.gmail.com", SMTPPort: 587,
			Hint: "Prefer OAuth Connect Gmail. App password only if OAuth is unavailable.", NeedsAppPass: true},
		{ID: "office365", Label: "Microsoft 365 (app password)", IMAPHost: "outlook.office365.com", IMAPPort: 993, SMTPHost: "smtp.office365.com", SMTPPort: 587,
			Hint: "Prefer OAuth Connect Outlook. App password if IMAP password auth is required.", NeedsAppPass: true},
		{ID: "custom", Label: "Custom IMAP / SMTP", IMAPPort: 993, SMTPPort: 587,
			Hint: "Enter IMAP and SMTP hosts from your provider’s docs."},
	}
}

func PresetByID(id string) Preset {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, p := range Presets() {
		if p.ID == id {
			return p
		}
	}
	return Preset{ID: "custom", Label: "Custom IMAP / SMTP", IMAPPort: 993, SMTPPort: 587}
}

func ApplyPreset(id, imapHost, smtpHost string, imapPort, smtpPort int) (ih string, ip int, sh string, sp int) {
	p := PresetByID(id)
	ih = strings.TrimSpace(imapHost)
	sh = strings.TrimSpace(smtpHost)
	ip, sp = imapPort, smtpPort
	if ih == "" {
		ih = p.IMAPHost
	}
	if sh == "" {
		sh = p.SMTPHost
	}
	if ip == 0 {
		ip = p.IMAPPort
		if ip == 0 {
			ip = 993
		}
	}
	if sp == 0 {
		sp = p.SMTPPort
		if sp == 0 {
			sp = 587
		}
	}
	return ih, ip, sh, sp
}
