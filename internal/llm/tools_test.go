package llm

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/manishkumar/outreachcrm/internal/models"
	"github.com/manishkumar/outreachcrm/internal/store"
)

func TestWriteToolsRequireConfirm(t *testing.T) {
	st := openTestStore(t)
	leadID, campID := seedLeadAndCampaign(t, st)
	e := &ToolExecutor{Store: st, WorkspaceID: 1, OwnerID: 1, Admin: true}

	writes := []struct {
		name string
		args string
	}{
		{"update_lead_status", `{"lead_id":` + itoa64(leadID) + `,"status":"contacted"}`},
		{"enroll_in_campaign", `{"lead_id":` + itoa64(leadID) + `,"campaign_id":` + itoa64(campID) + `}`},
		{"draft_reply", `{"lead_id":` + itoa64(leadID) + `,"subject":"Re: hi","body":"Thanks"}`},
		{"create_followup_note", `{"lead_id":` + itoa64(leadID) + `,"note":"Call Friday"}`},
	}
	for _, tc := range writes {
		out, err := e.Execute(tc.name, tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !strings.Contains(out, "needs_confirmation") {
			t.Fatalf("%s: want needs_confirmation, got %s", tc.name, out)
		}
	}

	lead, _ := st.GetLead(leadID)
	if lead.Status != "" && lead.Status != models.LeadStatusNew {
		t.Fatalf("status changed without confirm: %q", lead.Status)
	}
	if strings.Contains(lead.Notes, "Call Friday") {
		t.Fatal("note written without confirm")
	}
	if lead.DraftBody == "Thanks" {
		t.Fatal("draft saved without confirm")
	}
}

func TestWriteToolsExecuteWithConfirm(t *testing.T) {
	st := openTestStore(t)
	leadID, _ := seedLeadAndCampaign(t, st)
	e := &ToolExecutor{Store: st, WorkspaceID: 1, OwnerID: 1, Admin: true}

	out, err := e.Execute("update_lead_status", `{"lead_id":`+itoa64(leadID)+`,"status":"contacted","confirm":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "needs_confirmation") {
		t.Fatalf("confirmed write should execute: %s", out)
	}
	lead, err := st.GetLead(leadID)
	if err != nil {
		t.Fatal(err)
	}
	if lead.Status != models.LeadStatusContacted {
		t.Fatalf("status=%q", lead.Status)
	}

	out, err = e.Execute("create_followup_note", `{"lead_id":`+itoa64(leadID)+`,"note":"Ping next week","confirm":true}`)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil || parsed["ok"] != true {
		t.Fatalf("note result: %s", out)
	}
	lead, _ = st.GetLead(leadID)
	if !strings.Contains(lead.Notes, "Ping next week") {
		t.Fatalf("notes=%q", lead.Notes)
	}
}

func TestAutoConfirmBypassesGate(t *testing.T) {
	st := openTestStore(t)
	leadID, _ := seedLeadAndCampaign(t, st)
	e := &ToolExecutor{Store: st, WorkspaceID: 1, OwnerID: 1, Admin: true, AutoConfirm: true}
	out, err := e.Execute("update_lead_status", `{"lead_id":`+itoa64(leadID)+`,"status":"interested"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "needs_confirmation") {
		t.Fatalf("AutoConfirm should write: %s", out)
	}
	lead, _ := st.GetLead(leadID)
	if lead.Status != models.LeadStatusInterested {
		t.Fatalf("status=%q", lead.Status)
	}
}

func itoa64(n int64) string {
	return strconv.FormatInt(n, 10)
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedLeadAndCampaign(t *testing.T, st *store.Store) (leadID, campID int64) {
	t.Helper()
	var err error
	leadID, err = st.CreateLead(models.Lead{
		OwnerID: 1, WorkspaceID: 1, Name: "Ada", Email: "ada@example.com", Company: "Ada Co",
	})
	if err != nil {
		t.Fatal(err)
	}
	campID, err = st.CreateCampaign(models.Campaign{
		OwnerID: 1, WorkspaceID: 1, Name: "Intro", Status: "active", DailySendLimit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddStep(models.SequenceStep{
		CampaignID: campID, StepOrder: 1, SubjectTemplate: "Hi", BodySpintax: "Hello",
	}); err != nil {
		t.Fatal(err)
	}
	return leadID, campID
}
