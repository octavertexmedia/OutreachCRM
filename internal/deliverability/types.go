package deliverability

import "time"

// Action is the send decision from the engine.
type Action string

const (
	ActionSend     Action = "send"
	ActionDelay    Action = "delay"
	ActionSuppress Action = "suppress"
)

// Decision is the layered result passed to the sequencer.
type Decision struct {
	Action           Action
	Email            string
	Domain           string
	ISP              string
	BounceProb       float64 // 0–100
	SpamTrapRisk     float64 // 0–100
	EngagementProb   float64 // 0–100 likely to engage
	RecipientScore   float64 // 0–100 reputation
	DomainScore      float64 // 0–100
	ContentRisk      float64 // 0–100
	AuthOK           bool
	DelayUntil       *time.Time
	Reasons          []string
	SuggestedSubject string // typo fix etc — unused for send
	SuggestedDomain  string
}

// Input gathers everything needed for a pre-send evaluation.
type Input struct {
	Email           string
	Subject         string
	Body            string
	SendingDomain   string
	AccountWarmup   bool
	CampaignID      int64
	WorkspaceID     int64
	Now             time.Time
	Recipient       RecipientHistory
	WorkspaceHealth HealthSnapshot
	SenderAuth      AuthStatus
	SkipSMTPVerify  bool
	SkipBlacklist   bool
}

// RecipientHistory is layer-4 signal from prior sends.
type RecipientHistory struct {
	Sent          int
	Opened        int
	Clicked       int
	Replied       int
	HardBounces   int
	SoftBounces   int
	Complaints    int
	Unsubscribes  int
	LastEventAt   time.Time
	FirstSeenAt   time.Time
	NeverEngaged  bool
	PurchasedList bool // heuristic flag from lead source
}

// HealthSnapshot is workspace / campaign complaint monitoring.
type HealthSnapshot struct {
	Sent7d         int
	HardBounce7d   int
	SoftBounce7d   int
	Complaint7d    int
	Unsub7d        int
	BounceRatePct  float64
	ComplaintPct   float64
	CampaignPaused bool
}

// AuthStatus mirrors SPF/DKIM/DMARC (+ optional DNSBL).
type AuthStatus struct {
	SPF       bool
	DKIM      bool
	DMARC     bool
	Blacklisted bool
	Zones     []string
	Detail    string
}

// Dashboard aggregates layer-15 metrics.
type Dashboard struct {
	DomainReputation float64
	IPReputation     float64 // heuristic from blacklists + complaint rate
	BounceRate       float64
	SpamRate         float64
	DeliveryRate     float64
	InboxRate        float64 // proxy: 100 - bounce - spam
	Sent7d           int
	Suppressed       int
	DecisionsToday   int
	DelayedToday     int
	SuppressedToday  int
	PausedCampaigns  int
}
