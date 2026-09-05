package store

import (
	"testing"
	"time"

	"github.com/manishkumar/outreachcrm/internal/models"
)

// seedPeople creates leads and builds the identity layer from them.
func seedPeople(t *testing.T, st *Store, leads ...models.Lead) {
	t.Helper()
	for _, l := range leads {
		l.OwnerID, l.WorkspaceID = 1, 1
		if _, err := st.CreateLead(l); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.BackfillProspects(); err != nil {
		t.Fatal(err)
	}
}

// The brief's case, end to end: John Smith must be queued for review, not
// merged and not silently left as two unrelated strangers.
func TestResolveQueuesJohnSmithForReview(t *testing.T) {
	st := newTestStore(t)
	seedPeople(t, st,
		models.Lead{Name: "John Smith", Email: "john@oldcompany.com", Company: "Company A", Title: "Marketing Manager"},
		models.Lead{Name: "John Smith", Email: "john@newcompany.com", Company: "Company B", Title: "Director Marketing"},
	)

	res, err := st.ResolveIdentities(1)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Merged != 0 {
		t.Errorf("merged %d pairs, want 0 — name plus a domain change is not conclusive", res.Merged)
	}
	if res.Queued != 1 {
		t.Fatalf("queued %d, want 1", res.Queued)
	}

	cands, err := st.ListIdentityCandidates(1, models.IdentityPending, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("got %d candidates", len(cands))
	}
	if cands[0].Score < ReviewLow || cands[0].Score >= AutoHigh {
		t.Errorf("score %d should sit in the review band", cands[0].Score)
	}
	// Both people are still separate until someone decides.
	if n := st.mustCountPeople(t, 1); n != 2 {
		t.Errorf("CountPeople = %d, want 2 while the pair is pending", n)
	}
}

// Thresholds mirrored from internal/identity so the test states the intent
// rather than importing the constant it is checking.
const (
	ReviewLow = 50
	AutoHigh  = 100
)

func TestResolveDoesNotMergeDifferentPeople(t *testing.T) {
	st := newTestStore(t)
	seedPeople(t, st,
		models.Lead{Name: "John Smith", Email: "info@acme.com", Company: "Acme"},
		models.Lead{Name: "Jane Doe", Email: "info@other.com", Company: "Other"},
	)
	res, err := st.ResolveIdentities(1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Merged != 0 || res.Queued != 0 {
		t.Errorf("merged=%d queued=%d, want both 0 for two different people", res.Merged, res.Queued)
	}
	if n := st.mustCountPeople(t, 1); n != 2 {
		t.Errorf("CountPeople = %d, want 2", n)
	}
}

// Merging must be exactly reversible, because it will sometimes be wrong.
func TestMergeIsReversible(t *testing.T) {
	st := newTestStore(t)
	seedPeople(t, st,
		models.Lead{Name: "Ada Lovelace", Email: "ada@old.com"},
		models.Lead{Name: "Ada Lovelace", Email: "ada@new.com"},
	)
	a, err := st.FindPersonByEmail(1, "ada@old.com")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.FindPersonByEmail(1, "ada@new.com")
	if err != nil {
		t.Fatal(err)
	}

	if err := st.MergePeople(a.ID, b.ID, 120, []string{"test"}); err != nil {
		t.Fatal(err)
	}
	if n := st.mustCountPeople(t, 1); n != 1 {
		t.Fatalf("CountPeople = %d after merge, want 1", n)
	}
	// Both addresses now resolve to the survivor without any row having moved.
	for _, probe := range []string{"ada@old.com", "ada@new.com"} {
		got, err := st.FindPersonByEmail(1, probe)
		if err != nil {
			t.Fatalf("%s: %v", probe, err)
		}
		if got.ID != a.ID {
			t.Errorf("%s resolved to %d, want survivor %d", probe, got.ID, a.ID)
		}
	}
	cluster, err := st.PersonCluster(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cluster) != 2 {
		t.Errorf("cluster = %v, want both records", cluster)
	}

	// Undo restores the original two people exactly.
	if err := st.UnmergePerson(b.ID); err != nil {
		t.Fatal(err)
	}
	if n := st.mustCountPeople(t, 1); n != 2 {
		t.Errorf("CountPeople = %d after unmerge, want 2", n)
	}
	back, err := st.FindPersonByEmail(1, "ada@new.com")
	if err != nil {
		t.Fatal(err)
	}
	if back.ID != b.ID {
		t.Errorf("after unmerge the address resolves to %d, want %d", back.ID, b.ID)
	}
}

func TestMergeLeavesAnAuditTrail(t *testing.T) {
	st := newTestStore(t)
	seedPeople(t, st,
		models.Lead{Name: "Ada", Email: "ada@a.com"},
		models.Lead{Name: "Ada", Email: "ada@b.com"},
	)
	a, _ := st.FindPersonByEmail(1, "ada@a.com")
	b, _ := st.FindPersonByEmail(1, "ada@b.com")
	if err := st.MergePeople(a.ID, b.ID, 110, []string{"email"}); err != nil {
		t.Fatal(err)
	}
	n, err := st.CountEvents(a.ID, models.EventMerge)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("merge events = %d, want 1 — a merge must be explicable later", n)
	}
}

func TestMergeIntoAlreadyMergedRecordDoesNotChain(t *testing.T) {
	st := newTestStore(t)
	seedPeople(t, st,
		models.Lead{Name: "P One", Email: "p@1.com"},
		models.Lead{Name: "P Two", Email: "p@2.com"},
		models.Lead{Name: "P Three", Email: "p@3.com"},
	)
	p1, _ := st.FindPersonByEmail(1, "p@1.com")
	p2, _ := st.FindPersonByEmail(1, "p@2.com")
	p3, _ := st.FindPersonByEmail(1, "p@3.com")

	if err := st.MergePeople(p1.ID, p2.ID, 100, nil); err != nil {
		t.Fatal(err)
	}
	// Merging p3 into the already-absorbed p2 must land on p1, the survivor.
	if err := st.MergePeople(p2.ID, p3.ID, 100, nil); err != nil {
		t.Fatal(err)
	}
	got, err := st.FindPersonByEmail(1, "p@3.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != p1.ID {
		t.Errorf("p3 resolved to %d, want the surviving %d", got.ID, p1.ID)
	}
	if n := st.mustCountPeople(t, 1); n != 1 {
		t.Errorf("CountPeople = %d, want 1", n)
	}
}

func TestDecideCandidateMergesOrRejects(t *testing.T) {
	st := newTestStore(t)
	seedPeople(t, st,
		models.Lead{Name: "John Smith", Email: "john@oldco.com", Company: "Old"},
		models.Lead{Name: "John Smith", Email: "john@newco.com", Company: "New"},
	)
	if _, err := st.ResolveIdentities(1); err != nil {
		t.Fatal(err)
	}
	cands, _ := st.ListIdentityCandidates(1, models.IdentityPending, 10)
	if len(cands) != 1 {
		t.Fatalf("expected one candidate, got %d", len(cands))
	}

	if err := st.DecideIdentityCandidate(cands[0].ID, true, 1); err != nil {
		t.Fatal(err)
	}
	if n := st.mustCountPeople(t, 1); n != 1 {
		t.Errorf("CountPeople = %d after accepting, want 1", n)
	}
	pending, _ := st.CountIdentityCandidates(1, models.IdentityPending)
	if pending != 0 {
		t.Errorf("pending = %d after a decision, want 0", pending)
	}
	merged, _ := st.CountIdentityCandidates(1, models.IdentityMerged)
	if merged != 1 {
		t.Errorf("merged candidates = %d, want 1", merged)
	}
}

func TestRejectedCandidateDoesNotMerge(t *testing.T) {
	st := newTestStore(t)
	seedPeople(t, st,
		models.Lead{Name: "John Smith", Email: "john@oldco.com", Company: "Old"},
		models.Lead{Name: "John Smith", Email: "john@newco.com", Company: "New"},
	)
	if _, err := st.ResolveIdentities(1); err != nil {
		t.Fatal(err)
	}
	cands, _ := st.ListIdentityCandidates(1, models.IdentityPending, 10)
	if err := st.DecideIdentityCandidate(cands[0].ID, false, 1); err != nil {
		t.Fatal(err)
	}
	if n := st.mustCountPeople(t, 1); n != 2 {
		t.Errorf("CountPeople = %d after rejecting, want 2", n)
	}
}

// Re-running resolution must not re-queue a pair a human already judged.
func TestResolveIsIdempotent(t *testing.T) {
	st := newTestStore(t)
	seedPeople(t, st,
		models.Lead{Name: "John Smith", Email: "john@oldco.com", Company: "Old"},
		models.Lead{Name: "John Smith", Email: "john@newco.com", Company: "New"},
	)
	if _, err := st.ResolveIdentities(1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ResolveIdentities(1); err != nil {
		t.Fatal(err)
	}
	pending, _ := st.CountIdentityCandidates(1, models.IdentityPending)
	if pending != 1 {
		t.Errorf("pending = %d after two passes, want 1", pending)
	}
}

func TestRecordJobChangeClosesPreviousRole(t *testing.T) {
	st := newTestStore(t)
	seedPeople(t, st,
		models.Lead{Name: "John Smith", Email: "john@oldco.com", Company: "Old Co", Title: "Manager"},
	)
	p, err := st.FindPersonByEmail(1, "john@oldco.com")
	if err != nil {
		t.Fatal(err)
	}

	if err := st.RecordJobChange(p.ID, "New Co", "newco.com", "Director",
		models.EmploymentSourceSignature, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	roles, err := st.ListEmployment(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 2 {
		t.Fatalf("roles = %d, want 2 — the promotion must stay visible", len(roles))
	}
	// Current role first, and only one role may be current.
	if !roles[0].Current() || roles[0].Company != "New Co" {
		t.Errorf("current role = %+v, want New Co open-ended", roles[0])
	}
	if roles[1].Current() {
		t.Error("the previous role should have been closed")
	}
	if n, _ := st.CountEvents(p.ID, models.EventJobChange); n != 1 {
		t.Errorf("job_change events = %d, want 1 — scoring keys off this", n)
	}
}
