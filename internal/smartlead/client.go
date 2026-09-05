// Package smartlead reads data out of Smartlead. It never writes to it.
//
// The integration is deliberately one-way: OutReachCRM pulls campaigns, leads,
// statistics and threads, and Smartlead remains the system of record for its
// own state. Nothing here may create, update, pause, unsubscribe, block, reply
// or register a webhook, even though the API offers all of those.
//
// The rule is enforced rather than merely documented: every request passes
// through send, which refuses any method other than GET. A write added by
// accident fails loudly at the call site instead of silently changing a live
// outreach account.
package smartlead

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultBaseURL = "https://server.smartlead.ai/api/v1"

// Verified against the live API on 2026-09-05: limit=1000 returns 1000 rows,
// limit=2000 returns an empty array with HTTP 200. Never raise these above
// maxPageSize — an over-large limit looks exactly like end-of-data.
const (
	defaultLeadsPageSize = 1000
	defaultStatsPageSize = 1000
	maxPageSize          = 1000
)

// DefaultRatePerMin sits just under Smartlead's documented 60 requests per 60
// seconds, leaving headroom so live webhook traffic on the same key cannot
// starve a long import.
const DefaultRatePerMin = 55

type Client struct {
	BaseURL       string
	APIKey        string
	HTTP          *http.Client
	MaxRetries    int
	LeadsPageSize int
	StatsPageSize int

	// RatePerMin caps outbound requests. Zero means DefaultRatePerMin; a
	// negative value disables limiting, which is only useful in tests.
	RatePerMin int

	limiterOnce sync.Once
	limiter     *paceLimiter
}

// paceLimiter spaces requests evenly rather than allowing a burst that would
// immediately trip the API limit. Every caller on this client shares it, so
// concurrent workers cannot collectively exceed the budget.
type paceLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func (l *paceLimiter) wait() {
	if l == nil || l.interval <= 0 {
		return
	}
	l.mu.Lock()
	now := time.Now()
	if l.next.After(now) {
		d := l.next.Sub(now)
		l.next = l.next.Add(l.interval)
		l.mu.Unlock()
		time.Sleep(d)
		return
	}
	l.next = now.Add(l.interval)
	l.mu.Unlock()
}

func (c *Client) pace() *paceLimiter {
	c.limiterOnce.Do(func() {
		n := c.RatePerMin
		if n == 0 {
			n = DefaultRatePerMin
		}
		if n < 0 {
			c.limiter = &paceLimiter{}
			return
		}
		c.limiter = &paceLimiter{interval: time.Minute / time.Duration(n)}
	})
	return c.limiter
}

func New(apiKey string) *Client {
	return &Client{
		BaseURL:    DefaultBaseURL,
		APIKey:     NormalizeAPIKey(apiKey),
		HTTP:       &http.Client{Timeout: 60 * time.Second},
		MaxRetries: 5,
	}
}

// NormalizeAPIKey trims labels such as "Smartlead api key - " from a pasted secret.
func NormalizeAPIKey(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	lower := strings.ToLower(s)
	for _, p := range []string{"smartlead api key - ", "api key - ", "api_key=", "api-key:"} {
		if strings.HasPrefix(lower, p) {
			s = strings.TrimSpace(s[len(p):])
			break
		}
	}
	if i := strings.LastIndexAny(s, " \t"); i >= 0 {
		cand := strings.TrimSpace(s[i+1:])
		if strings.Contains(cand, "_") && len(cand) >= 20 {
			s = cand
		}
	}
	return strings.TrimSpace(s)
}

func (c *Client) get(path string, q url.Values) ([]byte, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("smartlead api key is empty")
	}
	base := strings.TrimRight(c.BaseURL, "/")
	path = "/" + strings.TrimLeft(path, "/")
	if q == nil {
		q = url.Values{}
	}
	q.Set("api_key", c.APIKey)
	u := base + path + "?" + q.Encode()

	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		c.pace().wait()
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "OutReachCRM-smartlead-import/1.0")
		resp, err := c.send(req)
		if err != nil {
			lastErr = err
			time.Sleep(backoff(attempt))
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("smartlead %s: HTTP %d", path, resp.StatusCode)
			wait := backoff(attempt)
			// The server knows better than our backoff curve when it says so.
			if ra := retryAfter(resp.Header.Get("Retry-After")); ra > 0 {
				wait = ra
			}
			time.Sleep(wait)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("smartlead %s: HTTP %d: %s", path, resp.StatusCode, truncate(string(body), 300))
		}
		return body, nil
	}
	return nil, lastErr
}

// retryAfter parses a Retry-After header given as delay-seconds. The HTTP-date
// form is not used by this API and is ignored rather than guessed at.
func retryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		return 0
	}
	if secs > 120 {
		secs = 120
	}
	return time.Duration(secs) * time.Second
}

// ErrWriteAttempted is returned when something tries to send a non-GET request
// to Smartlead. The integration is read-only by design; see the package doc.
var ErrWriteAttempted = errors.New("smartlead: integration is read-only, refusing non-GET request")

// send is the single point through which every Smartlead request passes. It
// enforces the one-way contract: reads only, no matter what a caller intends.
func (c *Client) send(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return nil, fmt.Errorf("%w (%s %s)", ErrWriteAttempted, req.Method, req.URL.Path)
	}
	return c.HTTP.Do(req)
}

func backoff(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt)) * 400 * time.Millisecond
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	return d
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

type Campaign struct {
	ID              int64
	Name            string
	Status          string
	Timezone        string
	SendWindowStart int
	SendWindowEnd   int
	DailySendLimit  int

	// TrackOpens / TrackClicks come from the campaign's track_settings. Six of
	// seven live campaigns disable both, so engagement was never recorded for
	// most of the base. Scoring has to tell that apart from "not interested".
	TrackOpens  bool
	TrackClicks bool
}

type SequenceStep struct {
	Order           int
	DelayDays       int
	Subject         string
	Body            string
	VariantBSubject string
	VariantBBody    string
}

type EmailAccount struct {
	ID         int64
	Email      string
	FromName   string
	Warmup     bool
	MessageDay int
}

type Lead struct {
	ID           int64
	Email        string
	FirstName    string
	LastName     string
	Phone        string
	Company      string
	Website      string
	Title        string
	Status       string
	CustomJSON   string
	Unsubscribed bool
	LastSeqSent  int
	ReplyCount   int
}

type StatRow struct {
	LeadEmail    string
	LeadName     string
	SeqNumber    int
	SentAt       string
	Opened       bool
	Clicked      bool
	Replied      bool
	Bounced      bool
	Unsubscribed bool
	StatsID      string
	EmailSubject string
	EmailMessage string

	// Timestamps, where the campaign recorded them. OpenAt and ClickAt are
	// empty on any campaign with tracking disabled, which is most of them.
	OpenAt  string
	ClickAt string
	ReplyAt string

	OpenCount  int
	ClickCount int

	// Category is Smartlead's own reply classification ("Meeting Request",
	// "Out Of Office", ...). It arrives free on every row, so no separate
	// classification call is needed for the common cases.
	Category string

	// IgnoreReply is Smartlead's auto-reply hint. Treat it as a positive
	// signal only: in a live sample it flagged 17 replies while 31 of 57
	// arrived within two minutes of the send. Its absence proves nothing.
	IgnoreReply bool
}

type HistoryMsg struct {
	ID        string
	Subject   string
	Body      string
	Direction string
	At        string
}

type PageMeta struct {
	Offset int
	Limit  int
	Total  int
}

func (c *Client) leadsLimit() int {
	if c != nil && c.LeadsPageSize > 0 {
		return clampPageSize(c.LeadsPageSize)
	}
	return defaultLeadsPageSize
}

func (c *Client) statsLimit() int {
	if c != nil && c.StatsPageSize > 0 {
		return clampPageSize(c.StatsPageSize)
	}
	return defaultStatsPageSize
}

// clampPageSize keeps a caller from asking for more than the API will serve.
// Above the cap it returns an empty page with HTTP 200, which is
// indistinguishable from end-of-data and would silently truncate an import.
func clampPageSize(n int) int {
	if n > maxPageSize {
		return maxPageSize
	}
	return n
}

func (c *Client) ListCampaigns() ([]Campaign, error) {
	raw, err := c.get("/campaigns/", nil)
	if err != nil {
		raw, err = c.get("/campaigns", nil)
	}
	if err != nil {
		return nil, err
	}
	items := extractObjects(raw, "data", "campaigns", "list", "results")
	out := make([]Campaign, 0, len(items))
	for _, it := range items {
		out = append(out, parseCampaign(it))
	}
	return out, nil
}

func (c *Client) GetCampaign(id int64) (Campaign, error) {
	raw, err := c.get(fmt.Sprintf("/campaigns/%d", id), nil)
	if err != nil {
		return Campaign{}, err
	}
	var wrap map[string]json.RawMessage
	if json.Unmarshal(raw, &wrap) == nil {
		if inner, ok := wrap["data"]; ok {
			raw = inner
		} else if inner, ok := wrap["campaign"]; ok {
			raw = inner
		}
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return Campaign{}, err
	}
	return parseCampaign(obj), nil
}

func (c *Client) ListSequences(campaignID int64) ([]SequenceStep, error) {
	raw, err := c.get(fmt.Sprintf("/campaigns/%d/sequences", campaignID), nil)
	if err != nil {
		return nil, err
	}
	items := extractObjects(raw, "data", "sequences", "list")
	// sometimes data is an object with sequences key
	if len(items) == 0 {
		var wrap map[string]json.RawMessage
		if json.Unmarshal(raw, &wrap) == nil {
			if inner, ok := wrap["data"]; ok {
				items = extractObjects(inner, "sequences", "data")
			}
		}
	}
	byOrder := map[int]*SequenceStep{}
	var orders []int
	for _, it := range items {
		order := intVal(it, "seq_number", "sequence_number", "step_number", "id")
		if order <= 0 {
			order = 1
		}
		subj := strVal(it, "subject", "email_subject")
		body := strVal(it, "email_body", "body", "html")
		delay := delayDays(it)
		variant := strings.ToUpper(strVal(it, "variant", "variant_label", "variant_categ"))
		st, ok := byOrder[order]
		if !ok {
			st = &SequenceStep{Order: order, DelayDays: delay}
			byOrder[order] = st
			orders = append(orders, order)
		}
		if variant == "B" || (st.Subject != "" && subj != "" && subj != st.Subject && st.VariantBSubject == "") {
			st.VariantBSubject = subj
			st.VariantBBody = body
			continue
		}
		if st.Subject == "" {
			st.Subject = subj
			st.Body = body
			if delay > st.DelayDays {
				st.DelayDays = delay
			}
		}
	}
	out := make([]SequenceStep, 0, len(orders))
	for _, o := range orders {
		out = append(out, *byOrder[o])
	}
	return out, nil
}

func delayDays(it map[string]any) int {
	if n := intVal(it, "seq_delay_in_days", "delay_in_days", "delay_days"); n > 0 {
		return n
	}
	if nested, ok := it["seq_delay_details"].(map[string]any); ok {
		return intVal(nested, "delay_in_days", "delayDays")
	}
	return 0
}

func (c *Client) ListEmailAccounts() ([]EmailAccount, error) {
	var out []EmailAccount
	var prevFP string
	for offset := 0; offset <= 100000; offset += 100 {
		q := url.Values{}
		q.Set("offset", strconv.Itoa(offset))
		q.Set("limit", "100")
		raw, err := c.get("/email-accounts/", q)
		if err != nil {
			return nil, err
		}
		items := extractObjects(raw, "data", "email_accounts", "list", "results")
		if len(items) == 0 {
			break
		}
		fp := pageFingerprint(items, "id", "from_email", "email")
		if fp != "" && fp == prevFP {
			break
		}
		prevFP = fp
		for _, it := range items {
			email := strVal(it, "from_email", "username", "email")
			if email == "" {
				continue
			}
			out = append(out, EmailAccount{
				ID:         int64Val(it, "id", "account_id"),
				Email:      email,
				FromName:   strVal(it, "from_name", "name"),
				Warmup:     boolVal(it, "warmup_enabled", "is_warmup_enabled"),
				MessageDay: intVal(it, "max_email_per_day", "message_per_day", "daily_limit"),
			})
		}
		if len(items) < 100 {
			break
		}
	}
	return out, nil
}

func (c *Client) EachCampaignLeads(campaignID int64, status string, fn func(page []Lead, meta PageMeta) error) error {
	if fn == nil {
		return fmt.Errorf("lead page callback is nil")
	}
	limit := c.leadsLimit()
	var prevFP string
	fetched := 0
	for offset := 0; offset <= 100000; offset += limit {
		q := url.Values{}
		q.Set("offset", strconv.Itoa(offset))
		q.Set("limit", strconv.Itoa(limit))
		if status != "" {
			q.Set("status", status)
		}
		raw, err := c.get(fmt.Sprintf("/campaigns/%d/leads", campaignID), q)
		if err != nil {
			return err
		}
		total := intFromWrapper(raw, "total_leads", "total")
		items := extractObjects(raw, "data", "leads", "list", "results")
		if len(items) == 0 {
			break
		}
		fp := leadPageFingerprint(items)
		if fp != "" && fp == prevFP {
			break
		}
		prevFP = fp
		page := make([]Lead, 0, len(items))
		for _, it := range items {
			ld := parseLead(it)
			if ld.Email == "" {
				continue
			}
			page = append(page, ld)
		}
		fetched += len(page)
		if err := fn(page, PageMeta{Offset: offset, Limit: limit, Total: total}); err != nil {
			return err
		}
		if len(items) < limit {
			break
		}
		if total > 0 && fetched >= total {
			break
		}
	}
	return nil
}

func (c *Client) ListCampaignLeads(campaignID int64, status string) ([]Lead, error) {
	var out []Lead
	err := c.EachCampaignLeads(campaignID, status, func(page []Lead, _ PageMeta) error {
		out = append(out, page...)
		return nil
	})
	return out, err
}

func parseStatRow(it map[string]any) (StatRow, bool) {
	email := strVal(it, "lead_email", "email", "to_email")
	if email == "" {
		return StatRow{}, false
	}
	return StatRow{
		LeadEmail:    email,
		LeadName:     strVal(it, "lead_name", "name"),
		SeqNumber:    intVal(it, "email_sequence_number", "sequence_number", "seq_number"),
		SentAt:       strVal(it, "sent_time", "sent_at", "email_sent_at"),
		Opened:       boolVal(it, "is_opened") || strVal(it, "open_time", "opened_at") != "",
		Clicked:      boolVal(it, "is_clicked") || strVal(it, "click_time", "clicked_at") != "",
		Replied:      boolVal(it, "is_replied") || strVal(it, "reply_time", "replied_at") != "",
		Bounced:      boolVal(it, "is_bounced") || strings.EqualFold(strVal(it, "email_status", "status"), "bounced"),
		Unsubscribed: boolVal(it, "is_unsubscribed") || strings.EqualFold(strVal(it, "email_status", "status"), "unsubscribed"),
		StatsID:      strVal(it, "id", "stats_id", "email_id"),
		EmailSubject: strVal(it, "email_subject", "subject"),
		EmailMessage: firstNonEmpty(strVal(it, "email_message", "email_body", "email_html", "body", "html")),
		OpenAt:       strVal(it, "open_time", "opened_at"),
		ClickAt:      strVal(it, "click_time", "clicked_at"),
		ReplyAt:      strVal(it, "reply_time", "replied_at"),
		OpenCount:    intVal(it, "open_count"),
		ClickCount:   intVal(it, "click_count"),
		Category:     strVal(it, "lead_category", "category"),
		IgnoreReply:  boolVal(it, "ignore_reply"),
	}, true
}

func (c *Client) EachStatistics(campaignID int64, fn func(page []StatRow, meta PageMeta) error) error {
	if fn == nil {
		return fmt.Errorf("statistics page callback is nil")
	}
	limit := c.statsLimit()
	var prevFP string
	fetched := 0
	for offset := 0; offset <= 200000; offset += limit {
		q := url.Values{}
		q.Set("offset", strconv.Itoa(offset))
		q.Set("limit", strconv.Itoa(limit))
		raw, err := c.get(fmt.Sprintf("/campaigns/%d/statistics", campaignID), q)
		if err != nil {
			return err
		}
		total := intFromWrapper(raw, "total_stats", "total", "count", "total_count")
		items := extractObjects(raw, "data", "stats", "list", "results")
		if len(items) == 0 {
			// An empty first page when the server reports rows means the
			// request was rejected in a way that still returned HTTP 200 —
			// an over-large limit does exactly this. Treating it as
			// end-of-data would silently import nothing.
			if offset == 0 && total > 0 {
				return fmt.Errorf("smartlead statistics campaign %d: empty first page but total_stats=%d (limit %d may exceed the server maximum of %d)", campaignID, total, limit, maxPageSize)
			}
			break
		}
		fp := pageFingerprint(items, "id", "lead_email", "email")
		if fp != "" && fp == prevFP {
			break
		}
		prevFP = fp
		page := make([]StatRow, 0, len(items))
		for _, it := range items {
			row, ok := parseStatRow(it)
			if !ok {
				continue
			}
			page = append(page, row)
		}
		fetched += len(page)
		if err := fn(page, PageMeta{Offset: offset, Limit: limit, Total: total}); err != nil {
			return err
		}
		if len(items) < limit {
			break
		}
		if total > 0 && fetched >= total {
			break
		}
	}
	return nil
}

func (c *Client) ListStatistics(campaignID int64) ([]StatRow, error) {
	var out []StatRow
	err := c.EachStatistics(campaignID, func(page []StatRow, _ PageMeta) error {
		out = append(out, page...)
		return nil
	})
	return out, err
}

func leadPageFingerprint(items []map[string]any) string {
	if len(items) == 0 {
		return ""
	}
	a := parseLead(items[0])
	b := parseLead(items[len(items)-1])
	return a.Email + "|" + b.Email + "|" + strconv.Itoa(len(items))
}

func intFromWrapper(raw []byte, keys ...string) int {
	var wrap map[string]any
	if json.Unmarshal(raw, &wrap) != nil {
		return 0
	}
	return intVal(wrap, keys...)
}

func pageFingerprint(items []map[string]any, keys ...string) string {
	if len(items) == 0 {
		return ""
	}
	a := items[0]
	b := items[len(items)-1]
	return strVal(a, keys...) + "|" + strVal(b, keys...) + "|" + strconv.Itoa(len(items))
}

func (c *Client) MessageHistory(campaignID, leadID int64) ([]HistoryMsg, error) {
	q := url.Values{}
	q.Set("show_plain_text_response", "true")
	raw, err := c.get(fmt.Sprintf("/campaigns/%d/leads/%d/message-history", campaignID, leadID), q)
	if err != nil {
		return nil, err
	}
	items := extractObjects(raw, "messages", "data", "history", "list")
	out := make([]HistoryMsg, 0, len(items))
	for _, it := range items {
		dir := strings.ToLower(strVal(it, "direction", "type", "email_type"))
		out = append(out, HistoryMsg{
			ID:        strVal(it, "id", "message_id", "email_id"),
			Subject:   strVal(it, "subject"),
			Body:      firstNonEmpty(strVal(it, "plain_text", "text", "body", "email_body", "html")),
			Direction: dir,
			At:        strVal(it, "sent_at", "received_at", "time", "created_at"),
		})
	}
	return out, nil
}

func (c *Client) ListBlockList() ([]string, error) {
	raw, err := c.get("/leads/block-list", nil)
	if err != nil {
		return nil, err
	}
	var emails []string
	items := extractObjects(raw, "data", "emails", "leads", "list", "block_list")
	for _, it := range items {
		e := strVal(it, "email", "lead_email")
		if e != "" {
			emails = append(emails, e)
		}
	}
	if len(emails) == 0 {
		var arr []string
		if json.Unmarshal(raw, &arr) == nil {
			emails = arr
		} else {
			var wrap map[string]any
			if json.Unmarshal(raw, &wrap) == nil {
				if list, ok := wrap["emails"].([]any); ok {
					for _, v := range list {
						if s, ok := v.(string); ok && s != "" {
							emails = append(emails, s)
						}
					}
				}
			}
		}
	}
	return emails, nil
}

func parseCampaign(it map[string]any) Campaign {
	tz := strVal(it, "timezone")
	if tz == "" {
		if sch, ok := it["scheduler_cron_value"].(map[string]any); ok {
			tz = strVal(sch, "tz", "timezone")
		}
	}
	start, end := 9, 18
	if sch, ok := nestedMap(it, "scheduler_cron_value"); ok {
		if h := parseHour(strVal(sch, "startHour", "start_hour")); h >= 0 {
			start = h
		}
		if h := parseHour(strVal(sch, "endHour", "end_hour")); h >= 0 {
			end = h
		}
		if tz == "" {
			tz = strVal(sch, "tz", "timezone")
		}
	}
	if tz == "" {
		tz = "UTC"
	}
	limit := intVal(it, "max_leads_per_day", "daily_send_limit", "send_limit")
	if limit <= 0 {
		limit = 50
	}
	openTrk, clickTrk := parseTrackSettings(it["track_settings"])
	return Campaign{
		ID:              int64Val(it, "id", "campaign_id"),
		Name:            strVal(it, "name", "campaign_name"),
		Status:          strVal(it, "status"),
		Timezone:        tz,
		SendWindowStart: start,
		SendWindowEnd:   end,
		DailySendLimit:  limit,
		TrackOpens:      openTrk,
		TrackClicks:     clickTrk,
	}
}

// parseTrackSettings reads Smartlead's track_settings, which lists what is
// DISABLED — an empty array means full tracking. Absent or unparseable settings
// default to tracked, so a missing field can never invent a penalty for a
// prospect who simply was not measured.
func parseTrackSettings(v any) (opens, clicks bool) {
	opens, clicks = true, true
	list, ok := v.([]any)
	if !ok {
		return
	}
	for _, item := range list {
		switch strings.ToUpper(strings.TrimSpace(fmt.Sprint(item))) {
		case "DONT_EMAIL_OPEN":
			opens = false
		case "DONT_LINK_CLICK":
			clicks = false
		}
	}
	return
}

func parseLead(it map[string]any) Lead {
	nested := it
	if inner, ok := nestedMap(it, "lead"); ok {
		nested = inner
	}
	custom := ""
	if cf, ok := nested["custom_fields"]; ok {
		b, _ := json.Marshal(cf)
		custom = string(b)
	} else if cf, ok := it["custom_fields"]; ok {
		b, _ := json.Marshal(cf)
		custom = string(b)
	}
	title := strVal(nested, "title", "job_title")
	if title == "" && custom != "" {
		var m map[string]any
		if json.Unmarshal([]byte(custom), &m) == nil {
			title = strVal(m, "job_title", "title")
		}
	}
	status := strVal(it, "status", "lead_status", "campaign_lead_status")
	if status == "" {
		status = strVal(nested, "status")
	}
	return Lead{
		ID:           int64Val(nested, "id", "lead_id"),
		Email:        strVal(nested, "email", "lead_email"),
		FirstName:    strVal(nested, "first_name", "firstName"),
		LastName:     strVal(nested, "last_name", "lastName"),
		Phone:        strVal(nested, "phone_number", "phone"),
		Company:      strVal(nested, "company_name", "company"),
		Website:      firstNonEmpty(strVal(nested, "website", "company_url"), strVal(nested, "linkedin_profile")),
		Title:        title,
		Status:       status,
		CustomJSON:   custom,
		Unsubscribed: boolVal(it, "is_unsubscribed") || boolVal(nested, "is_unsubscribed"),
		LastSeqSent:  intVal(it, "last_email_sequence_sent", "seq_number"),
		ReplyCount:   intVal(it, "reply_count"),
	}
}

func extractObjects(raw []byte, keys ...string) []map[string]any {
	raw = bytesTrim(raw)
	if len(raw) == 0 {
		return nil
	}
	var arr []map[string]any
	if json.Unmarshal(raw, &arr) == nil {
		return arr
	}
	var wrap map[string]json.RawMessage
	if json.Unmarshal(raw, &wrap) != nil {
		return nil
	}
	for _, k := range keys {
		inner, ok := wrap[k]
		if !ok {
			continue
		}
		if json.Unmarshal(inner, &arr) == nil {
			return arr
		}
		var innerObj map[string]json.RawMessage
		if json.Unmarshal(inner, &innerObj) == nil {
			for _, k2 := range keys {
				if v, ok := innerObj[k2]; ok {
					if json.Unmarshal(v, &arr) == nil {
						return arr
					}
				}
			}
		}
	}
	return nil
}

func bytesTrim(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func nestedMap(m map[string]any, key string) (map[string]any, bool) {
	v, ok := m[key]
	if !ok {
		return nil, false
	}
	switch t := v.(type) {
	case map[string]any:
		return t, true
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		var out map[string]any
		if json.Unmarshal(b, &out) != nil {
			return nil, false
		}
		return out, true
	}
}

func strVal(m map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case string:
			if strings.TrimSpace(t) != "" {
				return strings.TrimSpace(t)
			}
		case json.Number:
			return t.String()
		case float64:
			return strconv.FormatInt(int64(t), 10)
		case bool:
			if t {
				return "true"
			}
		}
	}
	return ""
}

func intVal(m map[string]any, keys ...string) int {
	return int(int64Val(m, keys...))
}

func int64Val(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case float64:
			return int64(t)
		case json.Number:
			n, _ := t.Int64()
			return n
		case string:
			n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
			return n
		case int:
			return int64(t)
		case int64:
			return t
		}
	}
	return 0
}

func boolVal(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case bool:
			return t
		case float64:
			return t != 0
		case string:
			s := strings.ToLower(strings.TrimSpace(t))
			return s == "true" || s == "1" || s == "yes"
		}
	}
	return false
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func parseHour(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return -1
	}
	if i := strings.IndexByte(s, ':'); i > 0 {
		s = s[:i]
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 23 {
		return -1
	}
	return n
}
