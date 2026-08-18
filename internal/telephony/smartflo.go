// Package telephony integrates Tata Tele Smartflo cloud telephony.
//
// Auth follows https://docs.smartflo.tatatelebusiness.com/reference/authentication-using-tokens:
// POST /v1/auth/login returns a JWT that is valid for expires_in seconds (3600
// for short-lived tokens), POST /v1/auth/refresh trades a live token for a new
// one, POST /v1/auth/logout terminates it. Permanent tokens issued by Tata
// support skip login entirely. Tokens are cached in-process and renewed ahead
// of expiry so no request ever carries a token that dies mid-flight.
package telephony

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultBaseURL = "https://api-smartflo.tatateleservices.com/v1"

	// refreshSkew renews the token early so an in-flight API call never
	// carries a credential that expires between send and receive.
	refreshSkew = 2 * time.Minute
	// defaultTTL is used when the login response omits expires_in.
	defaultTTL = time.Hour
)

// Credentials describe one Smartflo tenant. Either StaticToken (permanent
// token from Tata support) or Email+Password (short-lived token) is required.
type Credentials struct {
	BaseURL     string
	Email       string
	Password    string
	StaticToken string
	// AuthScheme is the Authorization prefix. Smartflo's reference does not
	// pin the format down, so "" means send the raw token and let the client
	// fall back to "Bearer" if the raw form is rejected.
	AuthScheme string
}

func (c Credentials) Configured() bool {
	return strings.TrimSpace(c.StaticToken) != "" ||
		(strings.TrimSpace(c.Email) != "" && c.Password != "")
}

// Fingerprint keys a client in the Registry: change any credential field and
// the cached token is dropped with the old client.
func (c Credentials) Fingerprint() string {
	return strings.Join([]string{c.BaseURL, c.Email, c.Password, c.StaticToken, c.AuthScheme}, "\x00")
}

// APIError is a non-2xx response from Smartflo.
type APIError struct {
	Status  int
	Op      string
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("smartflo %s: http %d", e.Op, e.Status)
	}
	return fmt.Sprintf("smartflo %s: %s (http %d)", e.Op, e.Message, e.Status)
}

type Client struct {
	creds Credentials
	base  string
	hc    *http.Client

	mu         sync.Mutex
	token      string
	expires    time.Time
	scheme     string // learned Authorization prefix ("" or "Bearer")
	schemeLock bool   // true when the scheme was configured explicitly
	daysLeft   int
}

func New(c Credentials) *Client {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	scheme := strings.TrimSpace(c.AuthScheme)
	return &Client{
		creds:      c,
		base:       base,
		hc:         &http.Client{Timeout: 25 * time.Second},
		scheme:     scheme,
		schemeLock: scheme != "",
	}
}

func (c *Client) BaseURL() string { return c.base }

// PasswordDaysLeft is number_of_days_left from the last login (0 if unknown):
// Smartflo expires passwords, and a silent expiry breaks every call.
func (c *Client) PasswordDaysLeft() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.daysLeft
}

type tokenResponse struct {
	Success         bool     `json:"success"`
	AccessToken     string   `json:"access_token"`
	TokenType       string   `json:"token_type"`
	ExpiresIn       flexInt  `json:"expires_in"`
	NumberOfDays    flexInt  `json:"number_of_days_left"`
	Message         string   `json:"message"`
	ValidationError anyValue `json:"errors"`
}

// Token returns a valid access token, logging in or refreshing as needed.
func (c *Client) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokenLocked(ctx)
}

func (c *Client) tokenLocked(ctx context.Context) (string, error) {
	if t := strings.TrimSpace(c.creds.StaticToken); t != "" {
		return t, nil
	}
	if !c.creds.Configured() {
		return "", fmt.Errorf("smartflo: no credentials configured")
	}
	now := time.Now()
	if c.token != "" && now.Before(c.expires.Add(-refreshSkew)) {
		return c.token, nil
	}
	// Still inside the window: a refresh is cheaper than a full login and
	// keeps the password out of the hot path.
	if c.token != "" && now.Before(c.expires) {
		if err := c.refreshLocked(ctx); err == nil {
			return c.token, nil
		}
	}
	if err := c.loginLocked(ctx); err != nil {
		return "", err
	}
	return c.token, nil
}

// Login forces a fresh token and reports when it expires — used by the
// "test connection" action so operators see a real failure, not a silent one.
func (c *Client) Login(ctx context.Context) (time.Time, error) {
	if strings.TrimSpace(c.creds.StaticToken) != "" {
		// A permanent token has no login step; probe it with a cheap call.
		return time.Time{}, c.probe(ctx)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.loginLocked(ctx); err != nil {
		return time.Time{}, err
	}
	return c.expires, nil
}

func (c *Client) probe(ctx context.Context) error {
	var out RecordsPage
	q := url.Values{"page": {"1"}, "limit": {"1"}}
	return c.call(ctx, http.MethodGet, "/call/records", q, nil, &out)
}

func (c *Client) loginLocked(ctx context.Context) error {
	body := map[string]string{
		"email":    strings.TrimSpace(c.creds.Email),
		"password": c.creds.Password,
	}
	var out tokenResponse
	status, raw, err := c.send(ctx, http.MethodPost, "/auth/login", nil, body, "")
	if err != nil {
		return err
	}
	_ = json.Unmarshal(raw, &out)
	if status >= 400 || out.AccessToken == "" {
		msg := out.Message
		if msg == "" {
			msg = firstMessage(raw)
		}
		c.token, c.expires = "", time.Time{}
		return &APIError{Status: status, Op: "auth/login", Message: msg}
	}
	c.applyToken(out)
	return nil
}

func (c *Client) refreshLocked(ctx context.Context) error {
	var out tokenResponse
	status, raw, err := c.send(ctx, http.MethodPost, "/auth/refresh", nil, nil, c.headerLocked(c.token))
	if err != nil {
		return err
	}
	_ = json.Unmarshal(raw, &out)
	if status >= 400 || out.AccessToken == "" {
		return &APIError{Status: status, Op: "auth/refresh", Message: out.Message}
	}
	c.applyToken(out)
	return nil
}

func (c *Client) applyToken(out tokenResponse) {
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = defaultTTL
	}
	c.token = out.AccessToken
	c.expires = time.Now().Add(ttl)
	c.daysLeft = int(out.NumberOfDays)
	// token_type is deliberately not turned into an Authorization prefix:
	// Smartflo reports "bearer" while accepting the raw token, so the raw
	// form is tried first and call() flips to "Bearer" only on a 401.
}

// Logout terminates the cached short-lived token (POST /auth/logout).
func (c *Client) Logout(ctx context.Context) error {
	c.mu.Lock()
	tok := c.token
	c.token, c.expires = "", time.Time{}
	c.mu.Unlock()
	if tok == "" {
		return nil
	}
	status, raw, err := c.send(ctx, http.MethodPost, "/auth/logout", nil, nil, c.header(tok))
	if err != nil {
		return err
	}
	if status >= 400 {
		return &APIError{Status: status, Op: "auth/logout", Message: firstMessage(raw)}
	}
	return nil
}

func (c *Client) header(token string) string {
	c.mu.Lock()
	scheme := c.scheme
	c.mu.Unlock()
	return authValue(scheme, token)
}

// headerLocked is the variant for callers that already hold c.mu.
func (c *Client) headerLocked(token string) string { return authValue(c.scheme, token) }

func authValue(scheme, token string) string {
	if scheme == "" {
		return token
	}
	return scheme + " " + token
}

// flipScheme switches between the raw-token and "Bearer <token>" forms once,
// since the Smartflo reference does not state which one it expects.
func (c *Client) flipScheme() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.schemeLock {
		return false
	}
	if c.scheme == "" {
		c.scheme = "Bearer"
	} else {
		c.scheme = ""
	}
	return true
}

func (c *Client) invalidate() {
	c.mu.Lock()
	c.token, c.expires = "", time.Time{}
	c.mu.Unlock()
}

// call performs an authenticated request. A 401 has two plausible causes —
// the wrong Authorization form or a token the server already dropped — so it
// walks both: flip the form (free), and only then spend a fresh login. A flip
// that did not help is reverted so the learned form stays honest.
func (c *Client) call(ctx context.Context, method, path string, query url.Values, body, out any) error {
	flipped, relogged := false, false
	for {
		tok, err := c.Token(ctx)
		if err != nil {
			return err
		}
		status, raw, err := c.send(ctx, method, path, query, body, c.header(tok))
		if err != nil {
			return err
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			if !flipped && c.flipScheme() {
				flipped = true
				continue
			}
			if flipped {
				c.flipScheme() // revert: the Authorization form was not the problem
				flipped = false
			}
			if !relogged && strings.TrimSpace(c.creds.StaticToken) == "" {
				relogged = true
				c.invalidate()
				continue
			}
			return &APIError{Status: status, Op: path, Message: firstMessage(raw)}
		}
		if status >= 400 {
			return &APIError{Status: status, Op: path, Message: firstMessage(raw)}
		}
		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("smartflo %s: decode: %w", path, err)
			}
		}
		return nil
	}
}

func (c *Client) send(ctx context.Context, method, path string, query url.Values, body any, authz string) (int, []byte, error) {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("smartflo %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}

// ---- Click to call -------------------------------------------------------

type CallRequest struct {
	AgentNumber       string `json:"agent_number"`
	DestinationNumber string `json:"destination_number"`
	CallerID          string `json:"caller_id,omitempty"`
	Async             int    `json:"async"`
	CallTimeout       int    `json:"call_timeout,omitempty"`
	CustomIdentifier  string `json:"custom_identifier,omitempty"`
}

type CallResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	CallID  string `json:"call_id,omitempty"`
}

// ClickToCall rings the agent first, then dials the destination. It returns as
// soon as Smartflo accepts the request; call progress arrives over webhooks.
func (c *Client) ClickToCall(ctx context.Context, req CallRequest) (CallResponse, error) {
	if strings.TrimSpace(req.AgentNumber) == "" {
		return CallResponse{}, fmt.Errorf("smartflo: agent number is required")
	}
	if strings.TrimSpace(req.DestinationNumber) == "" {
		return CallResponse{}, fmt.Errorf("smartflo: destination number is required")
	}
	if req.Async == 0 {
		req.Async = 1
	}
	var out CallResponse
	if err := c.call(ctx, http.MethodPost, "/click_to_call", nil, req, &out); err != nil {
		return out, err
	}
	if !out.Success && out.Message != "" {
		return out, fmt.Errorf("smartflo click_to_call: %s", out.Message)
	}
	return out, nil
}

// ---- Call detail records -------------------------------------------------

type RecordsQuery struct {
	From      time.Time
	To        time.Time
	Page      int
	Limit     int
	Direction string // inbound | outbound
	CallType  string // c = answered, m = missed
	Agents    string
	CallID    string
}

func (q RecordsQuery) values() url.Values {
	v := url.Values{}
	const layout = "2006-01-02 15:04:05"
	if !q.From.IsZero() {
		v.Set("from_date", q.From.Format(layout))
	}
	if !q.To.IsZero() {
		v.Set("to_date", q.To.Format(layout))
	}
	if q.Page > 0 {
		v.Set("page", strconv.Itoa(q.Page))
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	for k, s := range map[string]string{
		"direction": q.Direction,
		"call_type": q.CallType,
		"agents":    q.Agents,
		"call_id":   q.CallID,
	} {
		if strings.TrimSpace(s) != "" {
			v.Set(k, s)
		}
	}
	return v
}

// Record is one CDR row. Smartflo returns several numeric fields as strings,
// hence flexInt on every count.
type Record struct {
	ID              string  `json:"id"`
	CallID          string  `json:"call_id"`
	UUID            string  `json:"uuid"`
	Direction       string  `json:"direction"`
	Description     string  `json:"description"`
	Status          string  `json:"status"`
	RecordingURL    string  `json:"recording_url"`
	Service         string  `json:"service"`
	Date            string  `json:"date"`
	Time            string  `json:"time"`
	EndStamp        string  `json:"end_stamp"`
	CallDuration    flexInt `json:"call_duration"`
	AnsweredSeconds flexInt `json:"answered_seconds"`
	AgentName       string  `json:"agent_name"`
	AgentNumber     string  `json:"agent_number"`
	ClientNumber    string  `json:"client_number"`
	DIDNumber       string  `json:"did_number"`
	HangupCause     string  `json:"hangup_cause"`
}

type RecordsPage struct {
	Count   flexInt  `json:"count"`
	Limit   flexInt  `json:"limit"`
	Size    flexInt  `json:"size"`
	Page    flexInt  `json:"page"`
	Results []Record `json:"results"`
}

// StartedAt parses the CDR "date"/"time" pair in the given location.
func (r Record) StartedAt(loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	ds := strings.TrimSpace(r.Date + " " + r.Time)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, ds, loc); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// CallRecords pulls one page of CDRs.
func (c *Client) CallRecords(ctx context.Context, q RecordsQuery) (RecordsPage, error) {
	var out RecordsPage
	err := c.call(ctx, http.MethodGet, "/call/records", q.values(), nil, &out)
	return out, err
}

// ---- Registry ------------------------------------------------------------

// Registry caches one Client (and therefore one token) per credential set, so
// every workspace reuses its token across requests instead of logging in each
// time. Changing any credential yields a new client and drops the old token.
type Registry struct {
	mu      sync.Mutex
	clients map[string]*Client
}

func NewRegistry() *Registry {
	return &Registry{clients: map[string]*Client{}}
}

func (r *Registry) Client(creds Credentials) *Client {
	key := creds.Fingerprint()
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.clients[key]; ok {
		return c
	}
	if len(r.clients) > 64 {
		r.clients = map[string]*Client{}
	}
	c := New(creds)
	r.clients[key] = c
	return c
}

// ---- helpers -------------------------------------------------------------

// flexInt accepts a JSON number, a quoted number, or an "HH:MM:SS" duration.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		*f = flexInt(n)
		return nil
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		*f = flexInt(v)
		return nil
	}
	if parts := strings.Split(s, ":"); len(parts) == 3 {
		h, _ := strconv.Atoi(parts[0])
		m, _ := strconv.Atoi(parts[1])
		sec, _ := strconv.Atoi(parts[2])
		*f = flexInt(h*3600 + m*60 + sec)
		return nil
	}
	*f = 0 // tolerate anything else rather than dropping the whole record
	return nil
}

// anyValue swallows fields whose shape varies between error responses.
type anyValue struct{}

func (anyValue) UnmarshalJSON([]byte) error { return nil }

// firstMessage digs a human-readable error out of a Smartflo error body.
func firstMessage(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		s := strings.TrimSpace(string(raw))
		if len(s) > 200 {
			s = s[:200]
		}
		return s
	}
	for _, k := range []string{"message", "error", "detail"} {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	if errs, ok := m["errors"].(map[string]any); ok {
		for _, v := range errs {
			switch t := v.(type) {
			case string:
				return t
			case []any:
				if len(t) > 0 {
					if s, ok := t[0].(string); ok {
						return s
					}
				}
			}
		}
	}
	return ""
}

// NormalizeNumber strips formatting so CRM phone fields and Smartflo numbers
// compare cleanly. It keeps a leading + and digits only.
func NormalizeNumber(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for i, r := range s {
		if r == '+' && i == 0 {
			b.WriteRune(r)
			continue
		}
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Last10 returns the subscriber part of a number for lead matching, which is
// the only portion that survives every prefix/country-code variation.
func Last10(s string) string {
	d := strings.TrimPrefix(NormalizeNumber(s), "+")
	if len(d) <= 10 {
		return d
	}
	return d[len(d)-10:]
}
