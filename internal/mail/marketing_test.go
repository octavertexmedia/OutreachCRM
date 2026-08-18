package mail

import (
	"testing"

	"github.com/manishkumar/outreachcrm/internal/models"
)

func TestPreferCampaignSender_UsesMarketingWhenConfigured(t *testing.T) {
	m := &models.MarketingSMTP{
		Enabled:   true,
		Provider:  models.ProviderBrevo,
		FromEmail: "bulk@esp.example",
		FromName:  "Outreach",
		APIKeyEnc: "enc-key",
		Host:      "smtp-relay.brevo.com",
		Port:      587,
	}
	personal := models.EmailAccount{
		Email:    "me@gmail.com",
		Provider: models.ProviderGoogle,
		IMAPHost: "imap.gmail.com",
	}
	got, used := PreferCampaignSender(m, personal)
	if !used {
		t.Fatal("expected marketing SMTP")
	}
	if got.Email != "bulk@esp.example" {
		t.Fatalf("from: %q", got.Email)
	}
	if got.Provider != models.ProviderBrevo {
		t.Fatalf("provider: %q", got.Provider)
	}
	if IsPersonalMailbox(got) {
		t.Fatal("marketing account must not be treated as personal mailbox")
	}
}

func TestPreferCampaignSender_FallsBackWhenMarketingOff(t *testing.T) {
	personal := models.EmailAccount{Email: "me@gmail.com", Provider: models.ProviderGoogle}
	got, used := PreferCampaignSender(nil, personal)
	if used || got.Email != personal.Email {
		t.Fatalf("used=%v email=%q", used, got.Email)
	}
	off := &models.MarketingSMTP{Enabled: false, FromEmail: "bulk@esp.example", APIKeyEnc: "x"}
	got, used = PreferCampaignSender(off, personal)
	if used || got.Email != personal.Email {
		t.Fatalf("disabled marketing should fall back: used=%v email=%q", used, got.Email)
	}
}

func TestPreferCampaignSender_IncompleteMarketingFallsBack(t *testing.T) {
	personal := models.EmailAccount{Email: "smtp@co.com", Provider: models.ProviderSMTP}
	incomplete := &models.MarketingSMTP{Enabled: true, Provider: models.ProviderSMTP, FromEmail: ""}
	got, used := PreferCampaignSender(incomplete, personal)
	if used || got.Email != personal.Email {
		t.Fatalf("incomplete marketing should fall back: used=%v", used)
	}
}

func TestAccountRole(t *testing.T) {
	if AccountRole(models.EmailAccount{Provider: models.ProviderGoogle}) != models.AccountRoleMailbox {
		t.Fatal("gmail is mailbox")
	}
	if AccountRole(models.EmailAccount{Provider: models.ProviderSMTP, IMAPHost: "imap.titan.email"}) != models.AccountRoleMailbox {
		t.Fatal("imap smtp is mailbox")
	}
	if AccountRole(models.EmailAccount{Provider: models.ProviderSMTP}) != "send" {
		t.Fatal("smtp-only is send")
	}
}
