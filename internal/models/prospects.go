package models

import "time"

// Person is the durable identity in the prospect-memory layer. A person is not
// their email address: addresses expire, people do not. Everything that records
// contact history hangs off Person, so a job change no longer splits someone's
// history into two unrelated records.
type Person struct {
	ID           int64
	WorkspaceID  int64
	LeadID       int64 // transitional link to the legacy leads row
	DisplayName  string
	PrimaryEmail string
	Phone        string
	LinkedInURL  string
	MergedIntoID int64 // non-zero once this record was folded into another
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
}

// Merged reports whether this record has been absorbed by another person.
// Merged records are retained so a bad merge stays reversible.
func (p Person) Merged() bool { return p.MergedIntoID > 0 }

// Email address states. A bounced or unsubscribed address stays on the person;
// it is the address that is dead, not the relationship.
const (
	EmailStatusActive       = "active"
	EmailStatusBounced      = "bounced"
	EmailStatusUnsubscribed = "unsubscribed"
	EmailStatusStale        = "stale"
)

// PersonEmail is one address that reaches a person. Uniqueness lives here
// rather than on Person, which is what lets several addresses map to one human.
type PersonEmail struct {
	ID          int64
	PersonID    int64
	WorkspaceID int64
	Email       string
	Domain      string
	Status      string
	BounceCount int
	FirstSeenAt time.Time
	LastSeenAt  time.Time
}

// Employment sources, in descending order of trust.
const (
	EmploymentSourceManual    = "manual"
	EmploymentSourceSignature = "reply_signature"
	EmploymentSourceEnrich    = "enrichment"
	EmploymentSourceSmartlead = "smartlead"
)

// PersonEmployment is one role held by a person. Stored as rows with validity
// windows so a promotion is visible as history rather than overwriting the role
// it replaced — that change is itself a scoring signal.
type PersonEmployment struct {
	ID        int64
	PersonID  int64
	Company   string
	Domain    string
	Title     string
	ValidFrom *time.Time
	ValidTo   *time.Time // nil means this is the current role
	Source    string
}

// Current reports whether this is the person's present role.
func (e PersonEmployment) Current() bool { return e.ValidTo == nil }

// Prospect event kinds. The log is append-only: scores are derived from it, so
// changing how scoring works is a recompute rather than a migration.
const (
	EventSent           = "sent"
	EventOpen           = "open"
	EventClick          = "click"
	EventReply          = "reply"
	EventBounce         = "bounce"
	EventUnsubscribe    = "unsubscribe"
	EventCategoryChange = "category_change"
	EventJobChange      = "job_change"
	EventMerge          = "merge"
)

// ProspectEvent is one thing that happened to a person, ever.
type ProspectEvent struct {
	ID          int64
	PersonID    int64
	WorkspaceID int64
	CampaignID  int64
	Kind        string
	OccurredAt  time.Time
	Payload     string // JSON
	Source      string // smartlead_import | webhook | local
	DedupeKey   string
}

// PersonSignal is the materialised scoring snapshot for one person. Components
// are stored alongside the total because a recommendation that cannot explain
// itself does not get acted on.
type PersonSignal struct {
	PersonID    int64
	WorkspaceID int64

	IntentMax    int     // high-water mark of expressed interest, 0-100
	IntentReason string  // what produced IntentMax
	Decay        float64 // time decay on last contact
	Triggers     float64 // job change, matured timing objection, cooldown
	Priority     float64 // IntentMax * Decay * Triggers

	Reason          string
	SuggestedAction string
	Suppressed      bool

	// EngagementTracked records whether the campaigns this person was in
	// actually collected opens and clicks. Smartlead disables that per
	// campaign, and "not measured" must not be scored as "not interested".
	EngagementTracked bool

	LastContactedAt *time.Time
	LastReplyAt     *time.Time
	RevisitAt       *time.Time
	ComputedAt      time.Time
}

// Identity-candidate review states.
const (
	IdentityPending  = "pending"
	IdentityMerged   = "merged"
	IdentityRejected = "rejected"
)

// IdentityCandidate is a probable-but-unproven match held for human review.
// Auto-merging on a weak signal mixes two people's history irreversibly, so
// anything short of conclusive evidence lands here instead.
type IdentityCandidate struct {
	ID            int64
	WorkspaceID   int64
	PersonID      int64
	OtherPersonID int64
	Score         int
	Signals       string // JSON array of the matched signal names
	State         string
	DecidedAt     *time.Time
}
