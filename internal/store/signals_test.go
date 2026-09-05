package store

import (
	"testing"
	"time"

	"github.com/manishkumar/outreachcrm/internal/classify"
	"github.com/manishkumar/outreachcrm/internal/models"
)

// seedProspect creates a lead with one sent message and optionally one reply,
// then builds the identity layer — the shape a real import leaves behind.
func seedProspect(t *testing.T, st *Store, name, email string, sentMonthsAgo float64, category, body string) int64 {
	t.Helper()
	leadID, err := st.CreateLead(models.Lead{OwnerID: 1, WorkspaceID: 1, Name: name, Email: email, Company: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	campID, err := st.CreateCampaign(models.Campaign{OwnerID: 1, WorkspaceID: 1, Name: "C " + email, Status: "paused"})
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
	sent := time.Now().UTC().Add(-time.Duration(sentMonthsAgo * 30.44 * 24 * float64(time.Hour)))
	if _, _, err := st.InsertHistoricalOutbound(models.OutboundMessage{
		CampaignID: campID, LeadID: leadID, CampaignLeadID: clID, StepOrder: 1,
		ToEmail: email, Subject: "Hi", Body: "Hello", Status: "sent", SentAt: &sent,
	}); err != nil {
		t.Fatal(err)
	}
	if category != "" {
		ws, lid := int64(1), leadID
		if _, err := st.CreateReply(models.InboundReply{
			WorkspaceID: &ws, LeadID: &lid, FromEmail: email, Subject: "Re: Hi",
			Body: body, CreatedAt: sent.Add(48 * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		// Stand in for what the importer stores from lead_category.
		if _, err := st.db.Exec(`UPDATE inbound_replies SET category_raw = ? WHERE from_email = ?`,
			category, email); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.BackfillProspects(); err != nil {
		t.Fatal(err)
	}
	return leadID
}

func TestClassifyRepliesIsVersionedAndRerunnable(t *testing.T) {
	st := newTestStore(t)
	seedProspect(t, st, "Ada Lovelace", "ada@ex.com", 20, "Meeting Request", "Can we meet next week?")

	n, err := st.ClassifyReplies(1, 0)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if n != 1 {
		t.Fatalf("classified %d, want 1", n)
	}

	var cat string
	var points, version int
	if err := st.db.QueryRow(`SELECT category, intent_points, classifier_version FROM inbound_replies LIMIT 1`).
		Scan(&cat, &points, &version); err != nil {
		t.Fatal(err)
	}
	if cat != string(classify.Interested) || points != 55 {
		t.Errorf("category/points = %s/%d, want interested/55", cat, points)
	}
	if version != classify.Version {
		t.Errorf("version = %d, want %d", version, classify.Version)
	}

	// Already at the current version: nothing to redo.
	again, err := st.ClassifyReplies(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Errorf("re-ran %d replies, want 0 — selection is by classifier_version", again)
	}
}

func TestAutoReplyIsClassifiedAndNotScored(t *testing.T) {
	st := newTestStore(t)
	seedProspect(t, st, "Bob Away", "bob@ex.com", 20, "Interested",
		"Sounds interesting! I am currently out of the office until Monday.")

	if _, err := st.ClassifyReplies(1, 0); err != nil {
		t.Fatal(err)
	}
	var auto, points int
	if err := st.db.QueryRow(`SELECT auto_reply, intent_points FROM inbound_replies LIMIT 1`).
		Scan(&auto, &points); err != nil {
		t.Fatal(err)
	}
	if auto != 1 {
		t.Error("an out-of-office must be flagged automated even when Smartlead called it Interested")
	}
	if points != 0 {
		t.Errorf("intent_points = %d, want 0 for an auto-reply", points)
	}
}

func TestRecomputeSignalsRanksByPriority(t *testing.T) {
	st := newTestStore(t)
	// Asked for a meeting, long ago.
	seedProspect(t, st, "Meeting Asker", "meet@ex.com", 20, "Meeting Request", "Can we meet?")
	// Only mildly warm, also long ago.
	seedProspect(t, st, "Mild Warm", "warm@ex.com", 20, "Interested", "Interesting, thanks.")
	// Contacted last week — must be held back by the cooldown.
	seedProspect(t, st, "Recent Contact", "recent@ex.com", 0.2, "Meeting Request", "Let us meet!")

	if _, err := st.ClassifyReplies(1, 0); err != nil {
		t.Fatal(err)
	}
	n, err := st.RecomputeSignals(1)
	if err != nil {
		t.Fatalf("recompute: %v", err)
	}
	if n < 3 {
		t.Fatalf("scored %d people, want at least 3", n)
	}

	list, err := st.ListProspectsToContact(1, 0.01, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < 2 {
		t.Fatalf("got %d prospects, want at least 2", len(list))
	}
	if list[0].Name != "Meeting Asker" {
		t.Errorf("top prospect = %q, want Meeting Asker (a meeting outranks a mild reply)", list[0].Name)
	}
	for _, p := range list {
		if p.Name == "Recent Contact" {
			t.Error("someone contacted last week must not be recommended")
		}
		if p.WhyRecommended == "" {
			t.Errorf("%s has no reason — an unexplained recommendation goes unused", p.Name)
		}
		if p.SuggestedAction == "" {
			t.Errorf("%s has no suggested action", p.Name)
		}
	}
}

func TestSuppressedProspectNeverListed(t *testing.T) {
	st := newTestStore(t)
	seedProspect(t, st, "Opted Out", "out@ex.com", 24, "Meeting Request", "Let us meet")
	if err := st.AddSuppressionWS(1, "out@ex.com", "unsubscribed"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClassifyReplies(1, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecomputeSignals(1); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListProspectsToContact(1, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.Email == "out@ex.com" {
			t.Fatal("a suppressed person must never appear in the contact list")
		}
	}
}

func TestJobChangeLiftsPriority(t *testing.T) {
	st := newTestStore(t)
	seedProspect(t, st, "Mover Person", "mover@ex.com", 26, "Not Interested Now", "Revisit next year please")
	if _, err := st.ClassifyReplies(1, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecomputeSignals(1); err != nil {
		t.Fatal(err)
	}
	before, err := st.ListProspectsToContact(1, 0, 10)
	if err != nil || len(before) == 0 {
		t.Fatalf("expected a prospect, got %d (%v)", len(before), err)
	}
	basePriority := before[0].Priority

	p, err := st.FindPersonByEmail(1, "mover@ex.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordJobChange(p.ID, "New Co", "newco.com", "Director",
		models.EmploymentSourceSignature, time.Now().UTC().Add(-60*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecomputeSignals(1); err != nil {
		t.Fatal(err)
	}
	after, err := st.ListProspectsToContact(1, 0, 10)
	if err != nil || len(after) == 0 {
		t.Fatal("expected the prospect after the job change")
	}
	if after[0].Priority <= basePriority {
		t.Errorf("priority %.2f did not rise from %.2f after a job change — that is the strongest trigger available",
			after[0].Priority, basePriority)
	}
	if after[0].Company != "New Co" {
		t.Errorf("company = %q, want the current role New Co", after[0].Company)
	}
}

func TestRecomputeIsIdempotent(t *testing.T) {
	st := newTestStore(t)
	seedProspect(t, st, "Ada Lovelace", "ada@ex.com", 20, "Interested", "Yes please")
	if _, err := st.ClassifyReplies(1, 0); err != nil {
		t.Fatal(err)
	}
	first, err := st.RecomputeSignals(1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.RecomputeSignals(1)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("recompute scored %d then %d people — it must be stable", first, second)
	}
	var rowCount int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM person_signal`).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != first {
		t.Errorf("person_signal has %d rows for %d people — recompute is duplicating", rowCount, first)
	}
}
