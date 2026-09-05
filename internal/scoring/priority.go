// Package scoring ranks prospects by whether they are worth contacting again.
//
// It deliberately keeps two numbers rather than one. Intent is the high-water
// mark of interest a person ever expressed; readiness is whether enough has
// changed, or enough time passed, to justify approaching them now. A single
// blended score cannot separate "very interested last week" from "mildly
// interested three years ago", and those need opposite actions.
package scoring

import (
	"fmt"
	"math"
	"time"
)

// Tuning constants. These are starting values to be revised once real outcomes
// exist, not truths.
const (
	// HalfLifeMonths is how long it takes warmth to halve. Interest fades but
	// does not vanish: a two-year-old positive reply keeps about 57% of it.
	HalfLifeMonths = 30.0

	// CooldownDays stops the list recommending someone contacted recently.
	CooldownDays = 90

	// TriggerJobChange is the most valuable signal available — a new role means
	// new budget, new problems, and a prior conversation that now reads as a
	// warm introduction rather than a cold approach.
	TriggerJobChange = 1.4

	// TriggerTimingMatured applies once a stated "ask me later" date passes.
	TriggerTimingMatured = 1.3

	// TierA and TierB are the priority cuts used for presentation.
	TierA = 20.0
	TierB = 12.0
)

// Input is everything scoring needs about one person.
type Input struct {
	// IntentMax is the strongest intent ever expressed, from classify.
	IntentMax    int
	IntentReason string

	LastContactedAt *time.Time
	LastReplyAt     *time.Time

	// RevisitAt is a date the person themselves asked to be approached after.
	RevisitAt *time.Time

	// JobChangedAt is when a role change was detected, if any.
	JobChangedAt *time.Time

	// Suppressed covers opt-outs and unsubscribes. It is a hard exclusion, not
	// a low score — a legal and courtesy boundary cannot be outweighed by
	// interest.
	Suppressed bool

	// Deliverable is false when every known address for this person has
	// bounced and no alternative exists.
	Deliverable bool

	// EngagementTracked records whether the campaigns this person was in
	// actually measured opens and clicks. When false, the absence of
	// engagement is not evidence of anything.
	EngagementTracked bool

	Now time.Time
}

// Result is a scored prospect, carrying its components so the ranking can
// explain itself. A recommendation nobody can interrogate does not get acted on.
type Result struct {
	IntentMax int
	Decay     float64
	Triggers  float64
	Priority  float64

	Reason          string
	SuggestedAction string
	Tier            string
	Suppressed      bool
}

// Score ranks one prospect.
func Score(in Input) Result {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	res := Result{IntentMax: in.IntentMax, Decay: 1, Triggers: 1}

	// Hard exclusions first. These are not low scores; they are absences from
	// the list entirely.
	if in.Suppressed {
		res.Suppressed = true
		res.Reason = "opted out — never contact"
		res.Tier = "excluded"
		return res
	}
	if !in.Deliverable {
		res.Suppressed = true
		res.Reason = "no deliverable address on file"
		res.SuggestedAction = "find a current address before any outreach"
		res.Tier = "excluded"
		return res
	}

	months := monthsSince(in.LastContactedAt, now)
	res.Decay = decay(months)

	var reasons []string
	if in.IntentReason != "" {
		reasons = append(reasons, in.IntentReason)
	}

	// Cooldown: someone emailed last week should not resurface, whatever they
	// once said.
	if in.LastContactedAt != nil && now.Sub(*in.LastContactedAt) < CooldownDays*24*time.Hour {
		res.Triggers = 0
		res.Priority = 0
		res.Reason = fmt.Sprintf("contacted %s ago — inside the %d-day cooldown", humanAge(months), CooldownDays)
		res.SuggestedAction = "wait"
		res.Tier = "cooldown"
		return res
	}

	if in.JobChangedAt != nil {
		res.Triggers *= TriggerJobChange
		reasons = append(reasons, "changed company "+humanAge(monthsSince(in.JobChangedAt, now))+" ago")
	}
	if in.RevisitAt != nil && !in.RevisitAt.After(now) {
		res.Triggers *= TriggerTimingMatured
		reasons = append(reasons, "the date they asked to be contacted after has passed")
	}

	base := float64(in.IntentMax)
	if base < 0 {
		base = 0 // a negative reply suppresses ranking, it does not invert it
	}
	res.Priority = base * res.Decay * res.Triggers

	res.Reason = joinReasons(reasons)
	res.SuggestedAction = suggest(in)
	res.Tier = tier(res.Priority)
	return res
}

// decay halves a score every HalfLifeMonths. Contact that never happened gets
// no decay: there is nothing to have faded.
func decay(months float64) float64 {
	if months <= 0 {
		return 1
	}
	return math.Pow(0.5, months/HalfLifeMonths)
}

func monthsSince(t *time.Time, now time.Time) float64 {
	if t == nil || t.IsZero() {
		return 0
	}
	d := now.Sub(*t)
	if d < 0 {
		return 0
	}
	return d.Hours() / 24 / 30.44
}

func tier(p float64) string {
	switch {
	case p >= TierA:
		return "A"
	case p >= TierB:
		return "B"
	case p > 0:
		return "C"
	default:
		return "none"
	}
}

func humanAge(months float64) string {
	switch {
	case months < 1:
		return "under a month"
	case months < 24:
		return fmt.Sprintf("%d months", int(math.Round(months)))
	default:
		return fmt.Sprintf("%.1f years", months/12)
	}
}

func joinReasons(rs []string) string {
	switch len(rs) {
	case 0:
		return "no recorded interest"
	case 1:
		return rs[0]
	default:
		out := rs[0]
		for _, r := range rs[1:] {
			out += "; " + r
		}
		return out
	}
}

// suggest turns the evidence into the next action, because a ranked list
// without a next step just moves the decision rather than making it.
func suggest(in Input) string {
	switch {
	case in.JobChangedAt != nil:
		return "congratulate on the new role and reference the earlier conversation"
	case in.RevisitAt != nil:
		return "follow up on the timing they asked for"
	case in.IntentMax >= 55:
		return "re-offer the meeting they asked for"
	case in.IntentMax >= 40:
		return "re-send the information they requested, updated"
	case in.IntentMax >= 25:
		return "check whether the timing has changed"
	case in.IntentMax > 0:
		return "light re-approach with a new angle"
	default:
		return "no strong reason to re-approach yet"
	}
}
