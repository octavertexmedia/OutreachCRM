package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/manishkumar/outreachcrm/internal/models"
)

// SmartleadEvent is one inbound webhook from Smartlead.
//
// Receiving events is inbound data and fits the one-way rule. Creating the
// subscription is a POST to Smartlead and does not, so subscriptions are made
// by hand in their UI; the nightly sweep reconciles from the API regardless, so
// a subscription nobody added delays data rather than losing it.
type SmartleadEvent struct {
	Type       string
	Email      string
	CampaignID int64
	OccurredAt time.Time
	Subject    string
	Body       string
	Category   string
	MessageID  string
	Raw        string
}

// Smartlead webhook event types.
const (
	SLEventSent            = "EMAIL_SENT"
	SLEventOpen            = "EMAIL_OPEN"
	SLEventClick           = "EMAIL_LINK_CLICK"
	SLEventReply           = "EMAIL_REPLY"
	SLEventBounce          = "EMAIL_BOUNCE"
	SLEventUnsubscribed    = "LEAD_UNSUBSCRIBED"
	SLEventCategoryUpdated = "LEAD_CATEGORY_UPDATED"
)

// IngestSmartleadEvent records one webhook against the person it concerns.
//
// Unknown addresses are ignored rather than creating a person: a webhook is not
// a reliable place to mint identities, and the import will introduce the person
// properly with their full history attached.
func (s *Store) IngestSmartleadEvent(workspaceID int64, ev SmartleadEvent) (bool, error) {
	email := NormalizeEmail(ev.Email)
	if email == "" {
		return false, fmt.Errorf("event has no lead email")
	}
	person, err := s.FindPersonByEmail(workspaceID, email)
	if err != nil || person.ID == 0 {
		// Not an error: the person simply is not imported yet.
		return false, nil
	}

	kind := mapSmartleadEventKind(ev.Type)
	if kind == "" {
		return false, nil
	}
	at := ev.OccurredAt
	if at.IsZero() {
		at = now()
	}

	payload, _ := json.Marshal(map[string]any{
		"type":     ev.Type,
		"category": ev.Category,
		"subject":  ev.Subject,
	})

	// The dedupe key makes a redelivered webhook a no-op. Smartlead retries,
	// and the same event arriving twice must not double-count history.
	key := ev.MessageID
	if key == "" {
		key = fmtTime(at)
	}
	if err := s.AppendEvent(models.ProspectEvent{
		PersonID:    person.ID,
		WorkspaceID: person.WorkspaceID,
		CampaignID:  ev.CampaignID,
		Kind:        kind,
		OccurredAt:  at,
		Payload:     string(payload),
		Source:      "webhook",
		DedupeKey:   fmt.Sprintf("wh:%s:%d:%s", ev.Type, person.ID, key),
	}); err != nil {
		return false, err
	}

	switch kind {
	case models.EventReply:
		if err := s.ingestWebhookReply(person, ev, at); err != nil {
			return false, err
		}
	case models.EventUnsubscribe:
		// An opt-out is a boundary, applied immediately rather than waiting
		// for the next sweep.
		if err := s.AddSuppressionWS(person.WorkspaceID, email, "unsubscribed"); err != nil {
			return false, err
		}
	case models.EventBounce:
		if _, err := s.db.Exec(`
UPDATE person_email SET status = ?, bounce_count = bounce_count + 1
WHERE person_id = ? AND email = ?`,
			models.EmailStatusBounced, person.ID, email); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (s *Store) ingestWebhookReply(person models.Person, ev SmartleadEvent, at time.Time) error {
	ws, lead := person.WorkspaceID, person.LeadID
	var leadPtr *int64
	if lead > 0 {
		leadPtr = &lead
	}
	id, err := s.CreateReply(models.InboundReply{
		WorkspaceID: &ws,
		LeadID:      leadPtr,
		LeadName:    person.DisplayName,
		FromEmail:   NormalizeEmail(ev.Email),
		Subject:     ev.Subject,
		Body:        ev.Body,
		MessageID:   ev.MessageID,
		CreatedAt:   at,
	})
	if err != nil {
		// CreateReply rejects a message_id it already holds. On a redelivered
		// webhook that is the correct outcome, not a failure: returning an
		// error here would make Smartlead retry an event we have already
		// stored, forever.
		if strings.Contains(err.Error(), "duplicate message") {
			return nil
		}
		return err
	}
	// Store Smartlead's own label and leave classification to the versioned
	// pass, so a rule change re-runs cheaply instead of needing new webhooks.
	_, err = s.db.Exec(`
UPDATE inbound_replies SET person_id = ?, category_raw = ?, classifier_version = 0
WHERE id = ?`, person.ID, ev.Category, id)
	return err
}

func mapSmartleadEventKind(t string) string {
	switch strings.ToUpper(strings.TrimSpace(t)) {
	case SLEventSent:
		return models.EventSent
	case SLEventOpen:
		return models.EventOpen
	case SLEventClick:
		return models.EventClick
	case SLEventReply:
		return models.EventReply
	case SLEventBounce:
		return models.EventBounce
	case SLEventUnsubscribed:
		return models.EventUnsubscribe
	case SLEventCategoryUpdated:
		return models.EventCategoryChange
	default:
		return ""
	}
}

// ParseSmartleadWebhook reads the fields we care about out of a webhook body,
// tolerating the several shapes Smartlead uses across event types.
func ParseSmartleadWebhook(raw []byte) (SmartleadEvent, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return SmartleadEvent{}, err
	}
	ev := SmartleadEvent{Raw: string(raw)}
	ev.Type = firstString(m, "event_type", "type", "event")
	ev.Email = firstString(m, "to_email", "lead_email", "email")
	if ev.Email == "" {
		if lead, ok := m["lead"].(map[string]any); ok {
			ev.Email = firstString(lead, "email")
		}
	}
	ev.Subject = firstString(m, "subject", "email_subject")
	ev.Body = firstString(m, "reply_body", "email_body", "body", "text", "html")
	ev.Category = firstString(m, "lead_category", "category")
	ev.MessageID = firstString(m, "message_id", "stats_id", "id")
	if v := firstString(m, "campaign_id"); v != "" {
		ev.CampaignID = parseInt64(v)
	} else if f, ok := m["campaign_id"].(float64); ok {
		ev.CampaignID = int64(f)
	}
	if ts := firstString(m, "event_timestamp", "time", "sent_time", "reply_time", "created_at"); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			ev.OccurredAt = t.UTC()
		}
	}
	if ev.Type == "" {
		return ev, fmt.Errorf("webhook has no event type")
	}
	return ev, nil
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func parseInt64(s string) int64 {
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int64(r-'0')
	}
	return n
}
