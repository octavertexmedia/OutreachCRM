package identity

import (
	"testing"
	"time"
)

func date(y int, m time.Month, d int) *time.Time {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return &t
}

func TestNormalizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"John Smith", "john smith"},
		{"Smith, John", "john smith"}, // word order must not matter
		{"  JOHN   SMITH ", "john smith"},
		{"Dr. John Smith Jr.", "john smith"}, // honorifics and suffixes dropped
		{"John O'Smith", "john osmith"},
		// Smartlead exports sometimes repeat the surname.
		{"Kayleen Easterday Easterday", "easterday kayleen"},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeName(c.in); got != c.want {
			t.Errorf("NormalizeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{
		"+91 98765 43210": "9876543210",
		"09876543210":     "9876543210",
		"(987) 654-3210":  "9876543210",
		"12345":           "", // too short to identify anyone
		"":                "",
	}
	for in, want := range cases {
		if got := NormalizePhone(in); got != want {
			t.Errorf("NormalizePhone(%q) = %q, want %q", in, got, want)
		}
	}
}

// The case from the design brief. It must land in review: name plus a changed
// domain is suggestive, not conclusive — there is more than one John Smith in
// marketing.
func TestJohnSmithGoesToReview(t *testing.T) {
	old := Record{
		PersonID: 1, Name: "John Smith", Email: "john@oldcompany.com",
		Company: "Company A", Title: "Marketing Manager",
		ValidFrom: date(2021, time.January, 1), ValidTo: date(2024, time.June, 1),
	}
	fresh := Record{
		PersonID: 2, Name: "John Smith", Email: "john@newcompany.com",
		Company: "Company B", Title: "Director Marketing",
		ValidFrom: date(2024, time.July, 1),
	}

	m := Score(old, fresh)
	if m.Decision != DecisionReview {
		t.Fatalf("decision = %s (score %d, signals %v), want review", m.Decision, m.Score, m.Signals)
	}
	// name 25 + local_part 35 + employment_disjoint 15 = 75
	if m.Score != 75 {
		t.Errorf("score = %d, want 75", m.Score)
	}
	if m.Score >= AutoMergeAt {
		t.Error("must not auto-merge on name plus domain change alone")
	}
}

// Adding a bounce on the old address strengthens the case but must still not
// reach auto-merge — that conservatism is deliberate for a first release.
func TestJohnSmithWithBounceStillReviews(t *testing.T) {
	old := Record{
		Name: "John Smith", Email: "john@oldcompany.com", Company: "Company A",
		ValidFrom: date(2021, time.January, 1), ValidTo: date(2024, time.June, 1),
		Bounced: true,
	}
	fresh := Record{
		Name: "John Smith", Email: "john@newcompany.com", Company: "Company B",
		ValidFrom: date(2024, time.July, 1),
	}
	m := Score(old, fresh)
	if m.Score != 95 {
		t.Errorf("score = %d, want 95 (75 + bounce 20)", m.Score)
	}
	if m.Decision != DecisionReview {
		t.Errorf("decision = %s, want review", m.Decision)
	}
}

// If they told us where they went, that is conclusive enough.
func TestDomainCitedInThreadReachesMerge(t *testing.T) {
	old := Record{
		Name: "John Smith", Email: "john@oldcompany.com", Company: "Company A",
		ValidFrom: date(2021, time.January, 1), ValidTo: date(2024, time.June, 1),
		DomainsSeenInThreads: []string{"newcompany.com"},
		Bounced:              true,
	}
	fresh := Record{
		Name: "John Smith", Email: "john@newcompany.com", Company: "Company B",
		ValidFrom: date(2024, time.July, 1),
	}
	m := Score(old, fresh)
	// 25 + 35 + 15 + 20 + 30 = 125
	if m.Decision != DecisionMerge {
		t.Errorf("decision = %s (score %d, signals %v), want merge", m.Decision, m.Score, m.Signals)
	}
}

func TestDifferentNameVetoesEverything(t *testing.T) {
	a := Record{Name: "John Smith", Email: "sales@acme.com", Phone: "+91 98765 43210"}
	b := Record{Name: "Jane Doe", Email: "sales@other.com", Phone: "+91 98765 43210"}
	m := Score(a, b)
	if m.Decision != DecisionSeparate {
		t.Errorf("decision = %s, want separate", m.Decision)
	}
	if m.Score > 0 {
		t.Errorf("score = %d, want the veto to dominate", m.Score)
	}
}

// Shared role mailboxes must never merge two companies' contacts together.
func TestGenericLocalPartCarriesNoWeight(t *testing.T) {
	a := Record{Name: "Ada Lovelace", Email: "info@acme.com"}
	b := Record{Name: "Ada Lovelace", Email: "info@other.com"}
	m := Score(a, b)
	// Name alone: 25. The shared "info" must contribute nothing.
	if m.Score != 25 {
		t.Errorf("score = %d, want 25 — a generic local part is not evidence", m.Score)
	}
	if m.Decision != DecisionSeparate {
		t.Errorf("decision = %s, want separate", m.Decision)
	}
}

func TestExactEmailIsConclusive(t *testing.T) {
	a := Record{Name: "Ada Lovelace", Email: "ada@ex.com"}
	b := Record{Name: "Ada Lovelace", Email: "ada@ex.com"}
	if m := Score(a, b); m.Decision != DecisionMerge {
		t.Errorf("decision = %s (score %d), want merge", m.Decision, m.Score)
	}
}

func TestLinkedInIsConclusive(t *testing.T) {
	a := Record{Name: "Ada L", Email: "ada@a.com", LinkedIn: "https://linkedin.com/in/ada"}
	b := Record{Name: "Ada L", Email: "ada@b.com", LinkedIn: "https://LinkedIn.com/in/ada"}
	if m := Score(a, b); m.Decision != DecisionMerge {
		t.Errorf("decision = %s (score %d), want merge", m.Decision, m.Score)
	}
}

// A missing name on one side must not be read as a conflict — plenty of
// imported rows have only an address.
func TestUnknownNameIsNotAConflict(t *testing.T) {
	a := Record{Name: "", Email: "jsmith@a.com"}
	b := Record{Name: "John Smith", Email: "jsmith@b.com"}
	m := Score(a, b)
	if m.Score < 0 {
		t.Errorf("score = %d, an absent name must not veto", m.Score)
	}
	if m.Decision == DecisionMerge {
		t.Error("an absent name must not reach auto-merge either")
	}
}

func TestEmploymentOverlapIsNotEvidenceOfAMove(t *testing.T) {
	a := Record{
		Name: "John Smith", Email: "john@a.com", Company: "A",
		ValidFrom: date(2021, time.January, 1), ValidTo: date(2025, time.January, 1),
	}
	b := Record{
		Name: "John Smith", Email: "john@b.com", Company: "B",
		ValidFrom: date(2023, time.January, 1), // overlaps A
	}
	m := Score(a, b)
	for _, s := range m.Signals {
		if s == "employment_disjoint" {
			t.Error("overlapping roles must not count as a move")
		}
	}
}

func TestBlockKeys(t *testing.T) {
	r := Record{Name: "John Smith", Email: "john.smith@acme.com", Phone: "+91 98765 43210"}
	keys := BlockKeys(r)
	want := map[string]bool{"name:john smith": true, "local:john.smith": true, "phone:9876543210": true}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %d entries", keys, len(want))
	}
	for _, k := range keys {
		if !want[k] {
			t.Errorf("unexpected block key %q", k)
		}
	}

	// A generic mailbox must not produce a local-part block, or every "info@"
	// in the base lands in one bucket and the comparison explodes.
	generic := BlockKeys(Record{Name: "A B", Email: "info@acme.com"})
	for _, k := range generic {
		if k == "local:info" {
			t.Error("generic local part must not be a block key")
		}
	}
}

func TestDecideThresholds(t *testing.T) {
	cases := map[int]Decision{
		0: DecisionSeparate, 49: DecisionSeparate,
		50: DecisionReview, 75: DecisionReview, 99: DecisionReview,
		100: DecisionMerge, 125: DecisionMerge,
	}
	for score, want := range cases {
		if got := decide(score); got != want {
			t.Errorf("decide(%d) = %s, want %s", score, got, want)
		}
	}
}
