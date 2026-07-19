package mail

import (
	"fmt"
	"html"
	"net/smtp"
	"strings"

	"github.com/manishkumar/outreachcrm/internal/deliverability"
	"github.com/manishkumar/outreachcrm/internal/models"
)

func sendMailPlainOrXOAUTH(addr string, account models.EmailAccount, accessToken, smtpPassword, to string, msg []byte) error {
	var auth smtp.Auth
	switch account.Provider {
	case models.ProviderGoogle, models.ProviderMicrosoft:
		auth = xoauth2Auth{username: account.Email, token: accessToken}
	default:
		user := account.Username
		if user == "" {
			user = account.Email
		}
		auth = smtp.PlainAuth("", user, smtpPassword, account.SMTPHost)
	}
	return smtp.SendMail(addr, auth, account.Email, []string{to}, msg)
}

func (s *Sender) sendSMTP(account models.EmailAccount, accessToken, smtpPassword, to, subject, body, messageID, openPixelURL string) error {
	addr := fmt.Sprintf("%s:%d", account.SMTPHost, account.SMTPPort)
	boundary := "orc_" + strings.ReplaceAll(messageID, "@", "_")
	if boundary == "orc_" {
		boundary = "orc_boundary"
	}
	headers := []string{
		"From: " + account.Email,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: multipart/alternative; boundary=\"" + boundary + "\"",
	}
	if messageID != "" {
		headers = append(headers, "Message-ID: <"+messageID+">")
	}
	htmlBody := textToHTML(body, openPixelURL)
	var parts strings.Builder
	parts.WriteString("--" + boundary + "\r\n")
	parts.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	parts.WriteString(body)
	parts.WriteString("\r\n--" + boundary + "\r\n")
	parts.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
	parts.WriteString(htmlBody)
	parts.WriteString("\r\n--" + boundary + "--\r\n")
	msg := strings.Join(append(headers, "", parts.String()), "\r\n")
	return sendMailPlainOrXOAUTH(addr, account, accessToken, smtpPassword, to, []byte(msg))
}

func textToHTML(body, openPixelURL string) string {
	esc := html.EscapeString(body)
	esc = strings.ReplaceAll(esc, "\r\n", "\n")
	esc = strings.ReplaceAll(esc, "\n", "<br>\n")
	out := `<!DOCTYPE html><html><body style="font-family:sans-serif;font-size:14px;color:#12241f">` + esc
	if openPixelURL != "" {
		out += `<img src="` + html.EscapeString(openPixelURL) + `" width="1" height="1" alt="" style="display:none;border:0" />`
	}
	out += `</body></html>`
	return out
}

type xoauth2Auth struct {
	username string
	token    string
}

func (a xoauth2Auth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	resp := fmt.Sprintf("user=%s\x01auth=Bearer %s\x01\x01", a.username, a.token)
	return "XOAUTH2", []byte(resp), nil
}

func (a xoauth2Auth) Next(fromServer []byte, more bool) ([]byte, error) {
	if more {
		return nil, fmt.Errorf("xoauth2 rejected: %s", string(fromServer))
	}
	return nil, nil
}

// EffectiveDailyQuota applies deliverability warmup ramp when enabled.
func EffectiveDailyQuota(a models.EmailAccount) int {
	q := a.DailyQuota
	if a.WarmupEnabled {
		warm := deliverability.WarmupDailyLimit(a.WarmupDay, q)
		return warm
	}
	return q
}
