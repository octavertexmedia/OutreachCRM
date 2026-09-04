package smartlead

import (
	"strings"

	"github.com/manishkumar/outreachcrm/internal/models"
)

// MapEnrollment converts a Smartlead campaign-lead status into CRM campaign_leads.status
// and optional lead pipeline status / suppression reason.
func MapEnrollment(slStatus string, unsubscribed bool, replyCount int) (enrollStatus string, leadStatus string, suppressReason string, currentStep int) {
	st := strings.ToUpper(strings.TrimSpace(slStatus))
	st = strings.ReplaceAll(st, " ", "_")
	switch st {
	case "NOT_CONTACTED", "NOTCONTACTED", "PENDING":
		enrollStatus = "active"
		leadStatus = models.LeadStatusNew
	case "IN_PROGRESS", "INPROGRESS", "STARTED":
		enrollStatus = "active"
		leadStatus = models.LeadStatusContacted
	case "COMPLETED", "COMPLETE":
		enrollStatus = "completed"
		leadStatus = models.LeadStatusContacted
	case "INTERESTED":
		enrollStatus = "completed"
		leadStatus = models.LeadStatusInterested
	case "NOT_INTERESTED", "NOTINTERESTED":
		enrollStatus = "completed"
		leadStatus = models.LeadStatusNotInterested
	case "UNSUBSCRIBED":
		enrollStatus = "unsubscribed"
		leadStatus = models.LeadStatusUnsubscribed
		suppressReason = "unsubscribed"
	case "DO_NOT_CONTACT", "DONOTCONTACT", "DNC":
		enrollStatus = "unsubscribed"
		leadStatus = models.LeadStatusUnsubscribed
		suppressReason = "do_not_contact"
	case "BOUNCED", "BOUNCE":
		enrollStatus = "dead"
		leadStatus = models.LeadStatusContacted
		suppressReason = "bounce"
	default:
		if unsubscribed {
			enrollStatus = "unsubscribed"
			leadStatus = models.LeadStatusUnsubscribed
			suppressReason = "unsubscribed"
			return
		}
		if replyCount > 0 {
			enrollStatus = "completed"
			leadStatus = models.LeadStatusContacted
			return
		}
		enrollStatus = "active"
		leadStatus = models.LeadStatusNew
	}
	if unsubscribed && suppressReason == "" {
		enrollStatus = "unsubscribed"
		leadStatus = models.LeadStatusUnsubscribed
		suppressReason = "unsubscribed"
	}
	return
}

func WantsReplyHistory(slStatus string, replyCount int) bool {
	st := strings.ToUpper(strings.TrimSpace(slStatus))
	st = strings.ReplaceAll(st, " ", "_")
	switch st {
	case "INTERESTED", "NOT_INTERESTED", "NOTINTERESTED":
		return true
	}
	return replyCount > 0
}

func LeadDisplayName(first, last, email string) string {
	name := strings.TrimSpace(strings.TrimSpace(first) + " " + strings.TrimSpace(last))
	if name != "" {
		return name
	}
	return email
}
