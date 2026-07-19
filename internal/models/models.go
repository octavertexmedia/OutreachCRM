package models

import "time"

const (
	RoleAdmin  = "admin"
	RoleSender = "sender"

	ProviderSMTP      = "smtp"
	ProviderGoogle    = "google"
	ProviderMicrosoft = "microsoft"
	ProviderPostmark  = "postmark"
	ProviderSES       = "ses"

	HITLAuto        = "auto"
	HITLNeedsReview = "needs_review"
	HITLDone        = "done"
)

type User struct {
	ID            int64
	Email         string
	PasswordHash  string
	Role          string
	Active        bool
	TOTPSecretEnc string
	TOTPEnabled   bool
	WorkspaceID   int64
	CreatedAt     time.Time
}

type SessionUser struct {
	ID          int64
	Email       string
	Role        string
	WorkspaceID int64
}

func (u SessionUser) IsAdmin() bool { return u.Role == RoleAdmin }

type Workspace struct {
	ID        int64
	Name      string
	CreatedAt time.Time
}

type AuditEntry struct {
	ID          int64
	WorkspaceID int64
	UserID      int64
	UserEmail   string
	Action      string
	Entity      string
	EntityID    string
	Meta        string
	CreatedAt   time.Time
}

type Lead struct {
	ID               int64
	OwnerID          int64
	WorkspaceID      int64
	Name             string
	Website          string
	Phone            string
	Email            string
	GoogleRating     float64
	Category         string
	IssuesJSON       string
	PremiumScore     int
	Confidence       int
	EnrichmentCost   int // cents
	EnrichmentStatus string
	Notes            string
	Source           string // manual | csv | api | seed
	Company          string
	Title            string
	DraftSubject     string
	DraftBody        string
	EmailBounceProb  float64
	EmailValidation  string
	ConsentAt        *time.Time
	ConsentSource    string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Campaign struct {
	ID              int64
	OwnerID         int64
	WorkspaceID     int64
	Name            string
	Status          string
	DailySendLimit  int
	Timezone        string
	SendWindowStart int // hour 0-23
	SendWindowEnd   int // hour 0-23
	ABEnabled       bool
	DeliverabilityPaused bool
	CreatedAt       time.Time
}

type EmailAccount struct {
	ID               int64
	OwnerID          int64
	WorkspaceID      int64
	Email            string
	Provider         string
	SMTPHost         string
	SMTPPort         int
	Username         string
	PasswordEnc      string
	AccessTokenEnc   string
	RefreshTokenEnc  string
	TokenExpiry      *time.Time
	IMAPHost         string
	IMAPPort         int
	IMAPLastUID      uint32
	DailyQuota       int
	SentToday        int
	QuotaDate        string
	LastSentAt       *time.Time
	Domain           string
	DomainDailyLimit int
	WarmupDay        int
	WarmupEnabled    bool
	ESPAPIKeyEnc     string
	CreatedAt        time.Time
}

type SequenceStep struct {
	ID              int64
	CampaignID      int64
	StepOrder       int
	DelayDays       int
	SubjectTemplate string
	BodySpintax     string
	VariantBSubject string
	VariantBBody    string
}

type CampaignLead struct {
	ID          int64
	CampaignID  int64
	LeadID      int64
	CurrentStep int
	Status      string
	EnrolledAt  time.Time
	NextSendAt  *time.Time
	Variant     string // a|b
}

type OutboundMessage struct {
	ID             int64
	CampaignID     int64
	LeadID         int64
	CampaignLeadID int64
	StepOrder      int
	AccountID      *int64
	ToEmail        string
	Subject        string
	Body           string
	Status         string
	ScheduledAt    time.Time
	NextAttemptAt  time.Time
	Attempts       int
	SentAt         *time.Time
	Error          string
	LastError      string
	LockedUntil    *time.Time
	LockOwner      string
	Variant        string
	MessageID      string
}

type InboundReply struct {
	ID         int64
	OwnerID    *int64
	WorkspaceID *int64
	LeadID     *int64
	LeadName   string
	FromEmail  string
	Subject    string
	Body       string
	Intent     string
	MessageID  string
	ThreadID   string
	HITLStatus string
	CreatedAt  time.Time
}

type Suppression struct {
	ID          int64
	WorkspaceID int64
	Email       string
	Reason      string
	CreatedAt   time.Time
}

type EmailTemplate struct {
	ID          int64
	WorkspaceID int64
	Name        string
	Subject     string
	Body        string
	CreatedAt   time.Time
}

type DomainCheck struct {
	Domain    string
	SPF       bool
	DKIM      bool
	DMARC     bool
	Detail    string
	CheckedAt time.Time
}

type DashboardStats struct {
	Leads      int `json:"leads"`
	Premium    int `json:"premium"`
	Campaigns  int `json:"campaigns"`
	Accounts   int `json:"accounts"`
	Scheduled  int `json:"scheduled"`
	Positive   int `json:"positive"`
	Dead       int `json:"dead"`
	HITLOpen   int `json:"hitlOpen"`
	SentToday  int `json:"sentToday"`
	Bounces    int `json:"bounces"`
}

type Analytics struct {
	Sent       int     `json:"sent"`
	Failed     int     `json:"failed"`
	Dead       int     `json:"dead"`
	Positive   int     `json:"positive"`
	Unsub      int     `json:"unsub"`
	OpenHITL   int     `json:"openHitl"`
	ByVariantA int     `json:"byVariantA"`
	ByVariantB int     `json:"byVariantB"`
	Enriched   int     `json:"enriched"`
	WithDraft  int     `json:"withDraft"`
	Enrolled   int     `json:"enrolled"`
	ReplyRate  float64 `json:"replyRate"` // positive / sent * 100
	UnsubRate  float64 `json:"unsubRate"`
	Queued     int     `json:"queued"`
}

type PipelineFunnel struct {
	Sourced   int `json:"sourced"`
	Enriched  int `json:"enriched"`
	Drafted   int `json:"drafted"`
	Sequenced int `json:"sequenced"`
	Replied   int `json:"replied"`
	Positive  int `json:"positive"`
}

// NamedCount is a labeled aggregate for charts.
type NamedCount struct {
	Name  string  `json:"name"`
	Count int     `json:"count"`
	Value float64 `json:"value,omitempty"`
}

// GeoPoint is a location proxy (from website/email TLD) for map bubbles.
type GeoPoint struct {
	Code    string  `json:"code"`
	Country string  `json:"country"`
	Count   int     `json:"count"`
	Lat     float64 `json:"lat"`
	Lng     float64 `json:"lng"`
}

// DayCount is a time-series point.
type DayCount struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
	Prev  int    `json:"prev,omitempty"` // prior-period parallel day
}

// CampaignNode powers the campaign treemap / schedule bars.
type CampaignNode struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Enrolled int    `json:"enrolled"`
	Sent     int    `json:"sent"`
	Failed   int    `json:"failed"`
	Queued   int    `json:"queued"`
}

// WordWeight is a token for the word cloud.
type WordWeight struct {
	Text  string `json:"text"`
	Value int    `json:"value"`
}

// BusinessSnapshot is the full dashboard payload for D3 infographics.
type BusinessSnapshot struct {
	GeneratedAt string         `json:"generatedAt"`
	KPIs        DashboardStats `json:"kpis"`
	Funnel      PipelineFunnel `json:"funnel"`
	Analytics   Analytics      `json:"analytics"`
	Locations   []GeoPoint     `json:"locations"`
	Campaigns   []CampaignNode `json:"campaigns"`
	Categories  []NamedCount   `json:"categories"`
	Sources     []NamedCount   `json:"sources"`
	LeadDays    []DayCount     `json:"leadDays"`
	SendDays    []DayCount     `json:"sendDays"`
	Words       []WordWeight   `json:"words"`
	Intents     []NamedCount   `json:"intents"`
}

type DeliverabilityDecisionRow struct {
	ID             int64
	WorkspaceID    int64
	CampaignID     int64
	Email          string
	Action         string
	BounceProb     float64
	SpamTrapRisk   float64
	EngagementProb float64
	ContentRisk    float64
	ISP            string
	Reasons        string
	CreatedAt      time.Time
}

type BlacklistCheck struct {
	Key       string
	Listed    bool
	Zones     string
	CheckedAt time.Time
}

