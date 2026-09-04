package store

import (
	"os"
	"testing"
	"time"

	"github.com/manishkumar/outreachcrm/internal/models"
)

func TestNormalizeEmail(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  John.Smith@Example.COM ", "john.smith@example.com"},
		{"john+crm@example.com", "john@example.com"},
		{"john+a+b@example.com", "john@example.com"},
		{"", ""},
		{"not-an-email", "not-an-email"},
		// A leading '+' is the whole local part, not plus-addressing.
		{"+weird@example.com", "+weird@example.com"},
		// Dots are NOT folded: correct for gmail, wrong for corporate domains,
		// and a false merge costs more than a duplicate.
		{"j.o.h.n@corp.com", "j.o.h.n@corp.com"},
	}
	for _, c := range cases {
		if got := NormalizeEmail(c.in); got != c.want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEmailDomain(t *testing.T) {
	for in, want := range map[string]string{
		"a@b.com":     "b.com",
		"a@sub.b.com": "sub.b.com",
		"nodomain":    "",
		"trailing@":   "",
	} {
		if got := EmailDomain(in); got != want {
			t.Errorf("EmailDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestBackfillProspectsIsIdempotent(t *testing.T) {
	st := newTestStore(t)

	for _, l := range []models.Lead{
		{OwnerID: 1, WorkspaceID: 1, Name: "Ada Lovelace", Email: "Ada+crm@Example.com", Company: "Analytical", Title: "Engineer"},
		{OwnerID: 1, WorkspaceID: 1, Name: "Alan Turing", Email: "alan@bletchley.uk", Company: "GCCS"},
		{OwnerID: 1, WorkspaceID: 1, Name: "No Email", Email: ""},
	} {
		if _, err := st.CreateLead(l); err != nil {
			t.Fatal(err)
		}
	}

	first, err := st.BackfillProspects()
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if first.PeopleCreated != 3 {
		t.Errorf("PeopleCreated = %d, want 3", first.PeopleCreated)
	}
	if first.EmailsCreated != 2 {
		t.Errorf("EmailsCreated = %d, want 2 (the third lead has no address)", first.EmailsCreated)
	}
	if first.EmploymentCreated != 2 {
		t.Errorf("EmploymentCreated = %d, want 2", first.EmploymentCreated)
	}

	// Running it again must create nothing. The importer calls this after every
	// run, so a non-idempotent backfill would duplicate the entire base.
	second, err := st.BackfillProspects()
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if second.PeopleCreated != 0 || second.EmailsCreated != 0 || second.EmploymentCreated != 0 {
		t.Errorf("second run created %+v, want all zero", second)
	}

	n, err := st.CountPeople(1)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("CountPeople = %d, want 3", n)
	}
}

func TestBackfillNormalizesAndLooksUpByEmail(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.CreateLead(models.Lead{
		OwnerID: 1, WorkspaceID: 1, Name: "Ada", Email: "Ada+crm@Example.com",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BackfillProspects(); err != nil {
		t.Fatal(err)
	}

	// The stored address is normalised, and any equivalent spelling finds it.
	for _, probe := range []string{"ada@example.com", "ADA@EXAMPLE.COM", "ada+anything@example.com"} {
		p, err := st.FindPersonByEmail(1, probe)
		if err != nil {
			t.Fatalf("FindPersonByEmail(%q): %v", probe, err)
		}
		if p.DisplayName != "Ada" {
			t.Errorf("probe %q found %q", probe, p.DisplayName)
		}
	}
}

// The whole point of the design: one human, two addresses, history intact.
func TestSecondEmailKeepsOnePerson(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.CreateLead(models.Lead{
		OwnerID: 1, WorkspaceID: 1, Name: "John Smith", Email: "john@oldcompany.com",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BackfillProspects(); err != nil {
		t.Fatal(err)
	}
	p, err := st.FindPersonByEmail(1, "john@oldcompany.com")
	if err != nil {
		t.Fatal(err)
	}

	if err := st.AddPersonEmail(p.ID, 1, "john@newcompany.com"); err != nil {
		t.Fatal(err)
	}

	// Both addresses must resolve to the same person.
	old, err := st.FindPersonByEmail(1, "john@oldcompany.com")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := st.FindPersonByEmail(1, "john@newcompany.com")
	if err != nil {
		t.Fatal(err)
	}
	if old.ID != fresh.ID {
		t.Fatalf("addresses resolved to different people: %d vs %d", old.ID, fresh.ID)
	}

	emails, err := st.ListPersonEmails(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) != 2 {
		t.Fatalf("ListPersonEmails = %d addresses, want 2", len(emails))
	}
	if got := st.mustCountPeople(t, 1); got != 1 {
		t.Errorf("CountPeople = %d, want 1 — a new address must not create a person", got)
	}
}

func (s *Store) mustCountPeople(t *testing.T, ws int64) int {
	t.Helper()
	n, err := s.CountPeople(ws)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAppendEventDedupes(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.CreateLead(models.Lead{OwnerID: 1, WorkspaceID: 1, Name: "Ada", Email: "ada@ex.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BackfillProspects(); err != nil {
		t.Fatal(err)
	}
	p, err := st.FindPersonByEmail(1, "ada@ex.com")
	if err != nil {
		t.Fatal(err)
	}

	ev := models.ProspectEvent{
		PersonID:   p.ID,
		Kind:       models.EventReply,
		OccurredAt: time.Now().UTC(),
		DedupeKey:  "smartlead:1:2:reply",
	}
	// Re-importing the same page must not double-count history.
	for i := 0; i < 3; i++ {
		if err := st.AppendEvent(ev); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	n, err := st.CountEvents(p.ID, models.EventReply)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("CountEvents = %d, want 1 after three identical appends", n)
	}
}

func TestCampaignTrackingDefaultsToTracked(t *testing.T) {
	st := newTestStore(t)
	// An unknown campaign must be assumed to track, so scoring never invents a
	// penalty for a prospect whose campaign settings we have not imported.
	if !st.CampaignTracksOpens(999) {
		t.Error("unknown campaign should default to tracked")
	}
	if err := st.SetCampaignTracking(999, false, false); err != nil {
		t.Fatal(err)
	}
	if st.CampaignTracksOpens(999) {
		t.Error("tracking was disabled and should now report false")
	}
	// Re-importing the campaign must update, not duplicate.
	if err := st.SetCampaignTracking(999, true, true); err != nil {
		t.Fatal(err)
	}
	if !st.CampaignTracksOpens(999) {
		t.Error("tracking should have been re-enabled")
	}
}

func TestGetPersonFollowsMergeChain(t *testing.T) {
	st := newTestStore(t)
	for _, e := range []string{"a@ex.com", "b@ex.com"} {
		if _, err := st.CreateLead(models.Lead{OwnerID: 1, WorkspaceID: 1, Name: "Dup", Email: e}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.BackfillProspects(); err != nil {
		t.Fatal(err)
	}
	a, _ := st.FindPersonByEmail(1, "a@ex.com")
	b, _ := st.FindPersonByEmail(1, "b@ex.com")

	if _, err := st.db.Exec(`UPDATE person SET merged_into_id = ? WHERE id = ?`, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	// Looking up the absorbed record must return the survivor.
	got, err := st.GetPerson(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != b.ID {
		t.Errorf("GetPerson(%d) = %d, want the survivor %d", a.ID, got.ID, b.ID)
	}
	if n := st.mustCountPeople(t, 1); n != 1 {
		t.Errorf("CountPeople = %d, want 1 — merged records must not be counted", n)
	}
}

// TestBackfillOnPostgres runs the backfill against a real Postgres database,
// because the statements it uses (INSERT ... SELECT, correlated UPDATE ... SET
// x = (SELECT ...), INSERT OR IGNORE) are exactly the shapes the dialect
// translator has to get right, and production runs Postgres. Set TEST_PG_URL
// to a scratch database to run it.
func TestBackfillOnPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set")
	}
	st, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	for _, l := range []models.Lead{
		{OwnerID: 1, WorkspaceID: 1, Name: "Ada Lovelace", Email: "Ada+crm@Example.com", Company: "Analytical", Title: "Engineer"},
		{OwnerID: 1, WorkspaceID: 1, Name: "Alan Turing", Email: "alan@bletchley.uk", Company: "GCCS"},
	} {
		if _, err := st.CreateLead(l); err != nil {
			t.Fatal(err)
		}
	}

	res, err := st.BackfillProspects()
	if err != nil {
		t.Fatalf("backfill on postgres: %v", err)
	}
	if res.PeopleCreated < 2 {
		t.Errorf("PeopleCreated = %d, want at least 2", res.PeopleCreated)
	}

	p, err := st.FindPersonByEmail(1, "ada@example.com")
	if err != nil {
		t.Fatalf("lookup after normalisation: %v", err)
	}
	if p.DisplayName != "Ada Lovelace" {
		t.Errorf("found %q", p.DisplayName)
	}

	if err := st.AddPersonEmail(p.ID, 1, "ada@newlab.org"); err != nil {
		t.Fatal(err)
	}
	again, err := st.FindPersonByEmail(1, "ada@newlab.org")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != p.ID {
		t.Errorf("second address made a new person: %d vs %d", again.ID, p.ID)
	}

	// Re-running must be a no-op on Postgres too.
	second, err := st.BackfillProspects()
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if second.PeopleCreated != 0 {
		t.Errorf("second run created %d people, want 0", second.PeopleCreated)
	}
}

func TestBackfillEventsFromMessagesDedupes(t *testing.T) {
	st := newTestStore(t)
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
	sent := time.Now().UTC().Add(-72 * time.Hour)
	if _, _, err := st.InsertHistoricalOutbound(models.OutboundMessage{
		CampaignID: campID, LeadID: leadID, CampaignLeadID: clID, StepOrder: 1,
		ToEmail: "ada@ex.com", Subject: "Hi", Body: "Hello", Status: "sent",
		SentAt: &sent,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BackfillProspects(); err != nil {
		t.Fatal(err)
	}

	first, err := st.BackfillEventsFromMessages(1)
	if err != nil {
		t.Fatalf("event backfill: %v", err)
	}
	if first == 0 {
		t.Fatal("expected at least one sent event")
	}

	// Re-running an import must not double-count history.
	second, err := st.BackfillEventsFromMessages(1)
	if err != nil {
		t.Fatal(err)
	}
	if second != 0 {
		t.Errorf("second run created %d events, want 0", second)
	}

	p, err := st.FindPersonByEmail(1, "ada@ex.com")
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.CountEvents(p.ID, models.EventSent)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("sent events = %d, want exactly 1", n)
	}
}
