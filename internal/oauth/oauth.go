package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/manishkumar/outreachcrm/internal/models"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/microsoft"
)

type Managers struct {
	Google    *oauth2.Config
	Microsoft *oauth2.Config
}

func New(googleID, googleSecret, msID, msSecret, redirectBase string) *Managers {
	m := &Managers{}
	if googleID != "" && googleSecret != "" {
		m.Google = &oauth2.Config{
			ClientID:     googleID,
			ClientSecret: googleSecret,
			RedirectURL:  redirectBase + "/oauth/google/callback",
			Scopes: []string{
				"https://mail.google.com/",
				"https://www.googleapis.com/auth/userinfo.email",
			},
			Endpoint: google.Endpoint,
		}
	}
	if msID != "" && msSecret != "" {
		m.Microsoft = &oauth2.Config{
			ClientID:     msID,
			ClientSecret: msSecret,
			RedirectURL:  redirectBase + "/oauth/microsoft/callback",
			Scopes: []string{
				"offline_access",
				"https://outlook.office.com/SMTP.Send",
				"https://outlook.office.com/IMAP.AccessAsUser.All",
				"openid",
				"email",
				"profile",
			},
			Endpoint: microsoft.AzureADEndpoint("common"),
		}
	}
	return m
}

func (m *Managers) ConfigFor(provider string) (*oauth2.Config, error) {
	switch provider {
	case models.ProviderGoogle:
		if m.Google == nil {
			return nil, fmt.Errorf("google oauth not configured")
		}
		return m.Google, nil
	case models.ProviderMicrosoft:
		if m.Microsoft == nil {
			return nil, fmt.Errorf("microsoft oauth not configured")
		}
		return m.Microsoft, nil
	default:
		return nil, fmt.Errorf("unknown provider %s", provider)
	}
}

func RandomState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func Exchange(ctx context.Context, cfg *oauth2.Config, code string) (*oauth2.Token, error) {
	return cfg.Exchange(ctx, code)
}

func Refresh(ctx context.Context, cfg *oauth2.Config, refreshToken string) (*oauth2.Token, error) {
	ts := cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	return ts.Token()
}

func DefaultsForProvider(provider string) (smtpHost string, smtpPort int, imapHost string, imapPort int) {
	switch provider {
	case models.ProviderGoogle:
		return "smtp.gmail.com", 587, "imap.gmail.com", 993
	case models.ProviderMicrosoft:
		return "smtp.office365.com", 587, "outlook.office365.com", 993
	default:
		return "", 587, "", 993
	}
}

func TokenExpiry(t *oauth2.Token) *time.Time {
	if t == nil || t.Expiry.IsZero() {
		return nil
	}
	e := t.Expiry.UTC()
	return &e
}
