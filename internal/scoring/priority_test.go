package scoring

import (
	"math"
	"testing"
	"time"
)

var now = time.Date(2026, time.September, 5, 0, 0, 0, 0, time.UTC)

func ago(months float64) *time.Time {
	t := now.Add(-time.Duration(months * 30.44 * 24 * float64(time.Hour)))
	return &t
}

func base(in Input) Input {
	in.Now = now
	in.Deliverable = true
	return in
}

// The worked example from the design: a timing objection 26 months ago plus a
// recent job change must outrank a stronger but stale, unchanged prospect.
func TestJobChangeOutranksHigherStaleIntent(t *testing.T) {
	mover := Score(base(Input{
		IntentMax: 25, IntentReason: "timing objection",
		LastContactedAt: ago(26),
		JobChangedAt:    ago(2),
		RevisitAt:       ago(14),
	}))
	stale := Score(base(Input{
		IntentMax: 30, IntentReason: "replied positively",
		LastContactedAt: ago(48),
	}))

	if mover.Priority <= stale.Priority {
		t.Fatalf("mover %.1f should outrank stale %.1f — a reason to act now beats raw intent",
			mover.Priority, stale.Priority)
	}
	if mover.Tier != "A" {
		t.Errorf("mover tier = %s, want A (priority %.1f)", mover.Tier, mover.Priority)
	}
	if stale.Tier == "A" {
		t.Errorf("a 4-year-old unchanged prospect should not be tier A (%.1f)", stale.Priority)
	}
}

func TestDecayHalvesOverHalfLife(t *testing.T) {
	if got := decay(HalfLifeMonths); math.Abs(got-0.5) > 0.001 {
		t.Errorf("decay at one half-life = %.3f, want 0.5", got)
	}
	if got := decay(0); got != 1 {
		t.Errorf("decay at zero = %.3f, want 1", got)
	}
	// Two years should retain roughly 57%.
	if got := decay(24); got < 0.55 || got > 0.60 {
		t.Errorf("decay at 24 months = %.3f, want ~0.57", got)
	}
}

// Someone emailed last week must not resurface, however warm they once were.
func TestCooldownSuppressesRecentContact(t *testing.T) {
	r := Score(base(Input{
		IntentMax: 55, IntentReason: "asked for a meeting",
		LastContactedAt: ago(0.3), // ~9 days
	}))
	if r.Priority != 0 {
		t.Errorf("priority = %.1f, want 0 inside the cooldown", r.Priority)
	}
	if r.Tier != "cooldown" {
		t.Errorf("tier = %s, want cooldown", r.Tier)
	}
	if r.SuggestedAction != "wait" {
		t.Errorf("action = %q, want wait", r.SuggestedAction)
	}
}

func TestOptOutIsExcludedNotRanked(t *testing.T) {
	r := Score(base(Input{IntentMax: 55, Suppressed: true, LastContactedAt: ago(40)}))
	if !r.Suppressed || r.Tier != "excluded" {
		t.Errorf("opt-out must be excluded, got tier %s suppressed=%v", r.Tier, r.Suppressed)
	}
	if r.Priority != 0 {
		t.Errorf("priority = %.1f, want 0 — an opt-out is a boundary, not a low score", r.Priority)
	}
}

func TestUndeliverableIsExcluded(t *testing.T) {
	in := base(Input{IntentMax: 55, LastContactedAt: ago(40)})
	in.Deliverable = false
	r := Score(in)
	if !r.Suppressed {
		t.Error("a person with no working address cannot be contacted")
	}
	if r.SuggestedAction == "" {
		t.Error("should still suggest finding a current address")
	}
}

// A negative reply must lower someone, never invert them into a top result.
func TestNotInterestedDoesNotGoNegative(t *testing.T) {
	r := Score(base(Input{IntentMax: -30, IntentReason: "said not interested", LastContactedAt: ago(40)}))
	if r.Priority < 0 {
		t.Errorf("priority = %.1f, want no negative ranking", r.Priority)
	}
}

func TestRecommendationsExplainThemselves(t *testing.T) {
	r := Score(base(Input{
		IntentMax: 40, IntentReason: "asked for information",
		LastContactedAt: ago(14),
	}))
	if r.Reason == "" {
		t.Error("every recommendation must carry its reason")
	}
	if r.SuggestedAction == "" {
		t.Error("a ranked list without a next step just moves the decision")
	}
	// Components must be inspectable, not just the total.
	if r.Decay <= 0 || r.Decay > 1 {
		t.Errorf("decay = %.3f, out of range", r.Decay)
	}
	want := float64(r.IntentMax) * r.Decay * r.Triggers
	if math.Abs(r.Priority-want) > 0.001 {
		t.Errorf("priority %.3f does not equal its components %.3f", r.Priority, want)
	}
}

func TestFutureRevisitDateDoesNotTriggerYet(t *testing.T) {
	future := now.Add(90 * 24 * time.Hour)
	r := Score(base(Input{
		IntentMax: 25, LastContactedAt: ago(20), RevisitAt: &future,
	}))
	if r.Triggers != 1 {
		t.Errorf("triggers = %.2f, want 1 — the date has not arrived", r.Triggers)
	}
}

func TestNeverContactedGetsNoDecay(t *testing.T) {
	r := Score(base(Input{IntentMax: 30, IntentReason: "positive"}))
	if r.Decay != 1 {
		t.Errorf("decay = %.2f, want 1 when there is no contact to have faded", r.Decay)
	}
}

func TestSuggestedActionMatchesEvidence(t *testing.T) {
	cases := []struct {
		in       Input
		contains string
	}{
		{Input{IntentMax: 25, JobChangedAt: ago(2), LastContactedAt: ago(30)}, "new role"},
		{Input{IntentMax: 55, LastContactedAt: ago(30)}, "meeting"},
		{Input{IntentMax: 40, LastContactedAt: ago(30)}, "information"},
	}
	for _, c := range cases {
		got := Score(base(c.in)).SuggestedAction
		if got == "" {
			t.Fatalf("no action for %+v", c.in)
		}
		if !contains(got, c.contains) {
			t.Errorf("action %q should mention %q", got, c.contains)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
