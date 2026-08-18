package mail

import (
	"strings"

	"github.com/manishkumar/outreachcrm/internal/models"
)

// PreferCampaignSender returns the paid marketing ESP when configured.
// Personal Gmail/Microsoft mailboxes are never used for campaign blasts in that case.
func PreferCampaignSender(marketing *models.MarketingSMTP, personal models.EmailAccount) (account models.EmailAccount, usedMarketing bool) {
	if marketing != nil && marketing.Configured() {
		return marketing.AsAccount(), true
	}
	return personal, false
}

// IsPersonalMailbox is true for OAuth Gmail/Outlook (1:1 HITL / IMAP sync, not bulk).
func IsPersonalMailbox(a models.EmailAccount) bool {
	switch strings.ToLower(strings.TrimSpace(a.Provider)) {
	case models.ProviderGoogle, models.ProviderMicrosoft:
		return true
	default:
		return false
	}
}

// AccountRole labels an email_accounts row for the UI.
func AccountRole(a models.EmailAccount) string {
	if IsPersonalMailbox(a) || strings.TrimSpace(a.IMAPHost) != "" {
		return models.AccountRoleMailbox
	}
	return "send"
}
