package store

import (
	"testing"

	"github.com/manishkumar/outreachcrm/internal/models"
)

func TestImportCampaignLeadDoesNotQueue(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	leadID, err := st.CreateLead(models.Lead{OwnerID: 1, WorkspaceID: 1, Name: "Ada", Email: "ada@ex.com"})
	if err != nil {
		t.Fatal(err)
	}
	campID, err := st.CreateCampaign(models.Campaign{OwnerID: 1, WorkspaceID: 1, Name: "C", Status: "paused"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddStep(models.SequenceStep{CampaignID: campID, StepOrder: 1, SubjectTemplate: "Hi", BodySpintax: "Hello"}); err != nil {
		t.Fatal(err)
	}
	clID, err := st.ImportCampaignLead(campID, leadID, 1, "completed", "")
	if err != nil {
		t.Fatal(err)
	}
	if clID == 0 {
		t.Fatal("expected campaign_lead id")
	}
	n, err := st.CountScheduledOutbound(clID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("scheduled=%d want 0", n)
	}

	id, inserted, err := st.InsertHistoricalOutbound(models.OutboundMessage{
		CampaignID: campID, LeadID: leadID, CampaignLeadID: clID, StepOrder: 1,
		ToEmail: "ada@ex.com", Subject: "", Body: "", Status: "sent", MessageID: "sl-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 || !inserted {
		t.Fatal("expected new outbound id")
	}
	id2, inserted2, err := st.InsertHistoricalOutbound(models.OutboundMessage{
		CampaignID: campID, LeadID: leadID, CampaignLeadID: clID, StepOrder: 1,
		ToEmail: "ada@ex.com", Subject: "Hi from stats", Body: "Hello body", Status: "sent", MessageID: "sl-1",
		Opened: true, Replied: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id || inserted2 {
		t.Fatalf("idempotent want %d got %d inserted=%v", id, id2, inserted2)
	}
	msg, err := st.GetOutboundMessage(id)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Subject != "Hi from stats" || msg.Body != "Hello body" {
		t.Fatalf("enrich subject/body got %q %q", msg.Subject, msg.Body)
	}
	hist, err := st.ListSentHistory(true, 1, 1, 10)
	if err != nil || len(hist) != 1 {
		t.Fatalf("sent history %v n=%d", err, len(hist))
	}
	thread, err := st.ListLeadMail(leadID, 10)
	if err != nil || len(thread) != 1 || thread[0].Kind != "out" {
		t.Fatalf("lead mail %v %+v", err, thread)
	}
	n, _ = st.CountScheduledOutbound(clID)
	if n != 0 {
		t.Fatalf("still queued %d", n)
	}
	if _, _, err := st.InsertHistoricalOutbound(models.OutboundMessage{Status: "scheduled", CampaignLeadID: clID, StepOrder: 2}); err == nil {
		t.Fatal("scheduled historical insert should fail")
	}

	a, err := st.Analytics(1)
	if err != nil || a.Sent != 1 {
		t.Fatalf("analytics sent=%d err=%v", a.Sent, err)
	}

	if err := st.SmartleadRemember("campaign", "99", campID); err != nil {
		t.Fatal(err)
	}
	got, ok := st.SmartleadLookup("campaign", "99")
	if !ok || got != campID {
		t.Fatalf("map %v %d", ok, got)
	}
}
