package mail

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/manishkumar/outreachcrm/internal/models"
)

type Sender struct {
	DryRun     bool
	HTTPClient *http.Client
}

func (s *Sender) client() *http.Client {
	if s.HTTPClient == nil {
		s.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	return s.HTTPClient
}

func (s *Sender) Send(account models.EmailAccount, accessToken, smtpPassword, espKey, to, subject, body, messageID, openPixelURL string) error {
	if s.DryRun {
		return nil
	}
	switch account.Provider {
	case models.ProviderPostmark:
		return s.sendPostmark(espKey, account.Email, to, subject, body, messageID, openPixelURL)
	case models.ProviderSES:
		if espKey != "" {
			smtpPassword = espKey
		}
		return s.sendSMTP(account, "", smtpPassword, to, subject, body, messageID, openPixelURL)
	case models.ProviderGoogle, models.ProviderMicrosoft:
		return s.sendSMTP(account, accessToken, "", to, subject, body, messageID, openPixelURL)
	default:
		return s.sendSMTP(account, "", smtpPassword, to, subject, body, messageID, openPixelURL)
	}
}

func (s *Sender) sendPostmark(token, from, to, subject, body, messageID, openPixelURL string) error {
	payload := map[string]any{
		"From":     from,
		"To":       to,
		"Subject":  subject,
		"TextBody": body,
		"HtmlBody": textToHTML(body, openPixelURL),
	}
	if messageID != "" {
		payload["Headers"] = []map[string]string{{"Name": "Message-ID", "Value": "<" + messageID + ">"}}
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, "https://api.postmarkapp.com/email", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Postmark-Server-Token", token)
	res, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return fmt.Errorf("postmark %d: %s", res.StatusCode, string(raw))
	}
	return nil
}
