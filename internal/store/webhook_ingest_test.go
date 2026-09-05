package store

import (
	"testing"
	"time"

	"github.com/manishkumar/outreachcrm/internal/models"
)

func TestParseSmartleadWebhook(t *testing.T) {
	raw := []byte(`{
	  "event_type":"EMAIL_REPLY","campaign_id":2669003,
	  "to_email":"ada@ex.com","subject":"Re: hello",
	  "reply_body":"Sounds good, let us talk",
	  "lead_category":"Meeting Request","message_id":"m-1",
	  "event_timestamp":"2026-09-01T10:00:00Z"
	}`)
	ev, err := ParseSmartleadWebhook(raw)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "EMAIL_REPLY" || ev.Email != "ada@ex.com" {
		t.Errorf("type/email = %s/%s", ev.Type, ev.Email)
	}
	if ev.CampaignID != 2669003 {
		t.Errorf("campaign = %d", ev.CampaignID)
	}
	if ev.Category != "Meeting Request" {
		t.Errorf("category = %q", ev.Category)
	}
	if ev.OccurredAt.IsZero() {
		t.Error("timestamp not parsed")
	}
}

func TestParseSmartleadWebhookNestedLead(t *testing.T) {
	ev, err := ParseSmartleadWebhook([]byte(`{"event_type":"EMAIL_OPEN","lead":{"email":"bob@ex.com"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Email != "bob@ex.com" {
		t.Errorf("email = %q, want the nested lead address", ev.Email)
	}
}

func TestParseSmartleadWebhookRejectsUntyped(t *testing.T) {
	if _, err := ParseSmartleadWebhook([]byte(`{"to_email":"a@b.com"}`)); err == nil {
		t.Error("a webhook with no event type must be rejected")
	}
}

func TestIngestWebhookIsIdempotent(t *testing.T) {
	st := newTestStore(t)
	seedProspect(t, st, "Ada Lovelace", "ada@ex.com", 20, "", "")

	ev := SmartleadEvent{
		Type:       SLEventReply,
		Email:      "ada@ex.com",
		Subject:    "Re: hello",
		Body:       "Let us talk next week",
		Category:   "Meeting Request",
		MessageID:  "m-1",
		OccurredAt: time.Now().UTC(),
	}

	// Smartlead retries; a redelivered event must not double-count history.
	for i := 0; i < 3; i++ {
		applied, err := st.IngestSmartleadEvent(1, ev)
		if err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
		if !applied {
			t.Fatalf("ingest %d was not applied", i)
		}
	}

	p, err := st.FindPersonByEmail(1, "ada@ex.com")
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.CountEvents(p.ID, models.EventReply)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("reply events = %d after three deliveries, want 1", n)
	}
}

func TestIngestUnknownAddressIsIgnoredNotAnError(t *testing.T) {
	st := newTestStore(t)
	applied, err := st.IngestSmartleadEvent(1, SmartleadEvent{
		Type: SLEventOpen, Email: "nobody@nowhere.com", OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("an unknown address must not error: %v", err)
	}
	if applied {
		t.Error("nothing should have been applied for an unimported person")
	}
}

func TestIngestUnsubscribeSuppressesImmediately(t *testing.T) {
	st := newTestStore(t)
	seedProspect(t, st, "Opting Out", "bye@ex.com", 10, "", "")

	if _, err := st.IngestSmartleadEvent(1, SmartleadEvent{
		Type: SLEventUnsubscribed, Email: "bye@ex.com", OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	suppressed, err := st.IsSuppressed("bye@ex.com")
	if err != nil {
		t.Fatal(err)
	}
	if !suppressed {
		t.Error("an opt-out must apply at once, not wait for the next sweep")
	}
}

func TestIngestBounceMarksAddressDead(t *testing.T) {
	st := newTestStore(t)
	seedProspect(t, st, "Gone Away", "gone@ex.com", 10, "", "")

	if _, err := st.IngestSmartleadEvent(1, SmartleadEvent{
		Type: SLEventBounce, Email: "gone@ex.com", OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	p, _ := st.FindPersonByEmail(1, "gone@ex.com")
	emails, err := st.ListPersonEmails(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) == 0 || emails[0].Status != models.EmailStatusBounced {
		t.Errorf("address status = %+v, want bounced", emails)
	}
	if emails[0].BounceCount != 1 {
		t.Errorf("bounce_count = %d, want 1", emails[0].BounceCount)
	}
}

// A reply arriving by webhook must be stored unclassified so the versioned pass
// picks it up, rather than being classified inline and frozen.
func TestWebhookReplyIsQueuedForClassification(t *testing.T) {
	st := newTestStore(t)
	seedProspect(t, st, "Ada Lovelace", "ada@ex.com", 20, "", "")

	if _, err := st.IngestSmartleadEvent(1, SmartleadEvent{
		Type: SLEventReply, Email: "ada@ex.com", Subject: "Re: hi",
		Body: "Can we meet?", Category: "Meeting Request", MessageID: "m-9",
		OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	var version int
	var raw string
	if err := st.db.QueryRow(`
SELECT COALESCE(classifier_version,0), COALESCE(category_raw,'')
FROM inbound_replies WHERE message_id = ?`, "m-9").Scan(&version, &raw); err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Errorf("classifier_version = %d, want 0 so the sweep classifies it", version)
	}
	if raw != "Meeting Request" {
		t.Errorf("category_raw = %q, want Smartlead's own label preserved", raw)
	}

	n, err := st.ClassifyReplies(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("sweep classified %d, want 1", n)
	}
}
