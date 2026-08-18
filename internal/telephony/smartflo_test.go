package telephony

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type stub struct {
	logins   atomic.Int32
	refresh  atomic.Int32
	calls    atomic.Int32
	ttl      int
	wantAuth string // Authorization value the API accepts
	lastAuth atomic.Value
	lastBody atomic.Value
}

func (s *stub) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	token := func(w http.ResponseWriter, n int32) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "access_token": "tok-" + string(rune('0'+n)),
			"token_type": "bearer", "expires_in": s.ttl, "number_of_days_left": 30,
		})
	}
	mux.HandleFunc("POST /v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["email"] != "agent@example.com" || body["password"] != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "message": "The username or password is incorrect."})
			return
		}
		token(w, s.logins.Add(1))
	})
	mux.HandleFunc("POST /v1/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		token(w, s.refresh.Add(1)+100)
	})
	mux.HandleFunc("POST /v1/click_to_call", func(w http.ResponseWriter, r *http.Request) {
		s.lastAuth.Store(r.Header.Get("Authorization"))
		if s.wantAuth != "" && r.Header.Get("Authorization") != s.wantAuth {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "message": "Unauthenticated."})
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.lastBody.Store(body)
		s.calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "message": "Originate Success"})
	})
	mux.HandleFunc("GET /v1/call/records", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"count":"1","limit":"10","page":1,"results":[
		  {"id":"7","call_id":"c-1","uuid":"u-1","direction":"outbound","status":"answered",
		   "call_duration":"75","answered_seconds":42,"agent_name":"Asha","client_number":"+919812345678",
		   "date":"2026-08-01","time":"11:30:00","recording_url":"https://rec/1.mp3"}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestClient(t *testing.T, s *stub, scheme string) *Client {
	t.Helper()
	if s.ttl == 0 {
		s.ttl = 3600
	}
	srv := s.server(t)
	return New(Credentials{
		BaseURL:    srv.URL + "/v1",
		Email:      "agent@example.com",
		Password:   "s3cret",
		AuthScheme: scheme,
	})
}

func TestTokenIsCachedAcrossCalls(t *testing.T) {
	s := &stub{}
	c := newTestClient(t, s, "")
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := c.ClickToCall(ctx, CallRequest{AgentNumber: "1001", DestinationNumber: "9812345678"}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := s.logins.Load(); got != 1 {
		t.Fatalf("logins = %d, want 1 (token should be cached)", got)
	}
	if got := s.calls.Load(); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
}

func TestExpiringTokenIsRefreshedNotReloggedIn(t *testing.T) {
	// ttl inside the refresh skew: the next call must renew via /auth/refresh.
	s := &stub{ttl: 60}
	c := newTestClient(t, s, "")
	ctx := context.Background()
	if _, err := c.Token(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Token(ctx); err != nil {
		t.Fatal(err)
	}
	if s.logins.Load() != 1 {
		t.Fatalf("logins = %d, want 1", s.logins.Load())
	}
	if s.refresh.Load() == 0 {
		t.Fatal("expected a refresh call for a token inside the skew window")
	}
}

func TestAuthSchemeFallsBackToBearer(t *testing.T) {
	s := &stub{wantAuth: "Bearer tok-1"}
	c := newTestClient(t, s, "")
	if _, err := c.ClickToCall(context.Background(), CallRequest{AgentNumber: "1001", DestinationNumber: "9812345678"}); err != nil {
		t.Fatalf("click to call: %v", err)
	}
	if got, _ := s.lastAuth.Load().(string); got != "Bearer tok-1" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer tok-1")
	}
}

func TestRawTokenSchemeIsTriedFirst(t *testing.T) {
	s := &stub{wantAuth: "tok-1"}
	c := newTestClient(t, s, "")
	if _, err := c.ClickToCall(context.Background(), CallRequest{AgentNumber: "1001", DestinationNumber: "9812345678"}); err != nil {
		t.Fatalf("click to call: %v", err)
	}
	if got, _ := s.lastAuth.Load().(string); got != "tok-1" {
		t.Fatalf("Authorization = %q, want raw token", got)
	}
}

// A token the server dropped early must trigger one re-login, and the
// Authorization form probed along the way must be reverted, not kept.
func TestServerSideExpiryTriggersRelogin(t *testing.T) {
	s := &stub{wantAuth: "tok-2"}
	c := newTestClient(t, s, "")
	if _, err := c.ClickToCall(context.Background(), CallRequest{AgentNumber: "1001", DestinationNumber: "9812345678"}); err != nil {
		t.Fatalf("click to call: %v", err)
	}
	if got := s.logins.Load(); got != 2 {
		t.Fatalf("logins = %d, want 2 (initial + one recovery)", got)
	}
	if got, _ := s.lastAuth.Load().(string); got != "tok-2" {
		t.Fatalf("Authorization = %q, want the raw form to be restored", got)
	}
}

func TestBadCredentialsSurfaceServerMessage(t *testing.T) {
	s := &stub{}
	srv := s.server(t)
	c := New(Credentials{BaseURL: srv.URL + "/v1", Email: "agent@example.com", Password: "wrong"})
	_, err := c.Login(context.Background())
	if err == nil {
		t.Fatal("expected an error for bad credentials")
	}
	if !strings.Contains(err.Error(), "username or password is incorrect") {
		t.Fatalf("error = %v, want the Smartflo message", err)
	}
}

func TestClickToCallSendsRequiredFields(t *testing.T) {
	s := &stub{}
	c := newTestClient(t, s, "")
	_, err := c.ClickToCall(context.Background(), CallRequest{
		AgentNumber: "1001", DestinationNumber: "+91 98123-45678",
		CallerID: "08047123456", CustomIdentifier: "orc-abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := s.lastBody.Load().(map[string]any)
	if body["agent_number"] != "1001" || body["caller_id"] != "08047123456" || body["custom_identifier"] != "orc-abc" {
		t.Fatalf("body = %#v", body)
	}
	if body["async"] != float64(1) {
		t.Fatalf("async = %v, want 1 (default)", body["async"])
	}
}

func TestClickToCallRequiresNumbers(t *testing.T) {
	s := &stub{}
	c := newTestClient(t, s, "")
	if _, err := c.ClickToCall(context.Background(), CallRequest{DestinationNumber: "981"}); err == nil {
		t.Fatal("expected an error when the agent number is missing")
	}
	if _, err := c.ClickToCall(context.Background(), CallRequest{AgentNumber: "1001"}); err == nil {
		t.Fatal("expected an error when the destination is missing")
	}
}

func TestCallRecordsTolerateStringNumbers(t *testing.T) {
	s := &stub{}
	c := newTestClient(t, s, "")
	page, err := c.CallRecords(context.Background(), RecordsQuery{
		From: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Limit: 10, Page: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(page.Results))
	}
	r := page.Results[0]
	if r.CallDuration != 75 || r.AnsweredSeconds != 42 {
		t.Fatalf("duration=%d answered=%d, want 75/42", r.CallDuration, r.AnsweredSeconds)
	}
	if got := r.StartedAt(time.UTC); got.Format(time.RFC3339) != "2026-08-01T11:30:00Z" {
		t.Fatalf("StartedAt = %v", got)
	}
}

func TestUnconfiguredCredentialsFailFast(t *testing.T) {
	c := New(Credentials{BaseURL: "https://example.invalid/v1"})
	if _, err := c.Token(context.Background()); err == nil {
		t.Fatal("expected an error with no credentials")
	}
}

func TestRegistryReusesClientPerCredentialSet(t *testing.T) {
	r := NewRegistry()
	a := Credentials{Email: "x@y.z", Password: "p"}
	if r.Client(a) != r.Client(a) {
		t.Fatal("same credentials should reuse the client (and its token)")
	}
	b := Credentials{Email: "x@y.z", Password: "changed"}
	if r.Client(a) == r.Client(b) {
		t.Fatal("changed credentials must yield a new client")
	}
}

func TestNumberHelpers(t *testing.T) {
	cases := []struct{ in, norm, last10 string }{
		{"+91 98123-45678", "+919812345678", "9812345678"},
		{"098123 45678", "09812345678", "9812345678"},
		{"(080) 4712.3456", "08047123456", "8047123456"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := NormalizeNumber(c.in); got != c.norm {
			t.Errorf("NormalizeNumber(%q) = %q, want %q", c.in, got, c.norm)
		}
		if got := Last10(c.in); got != c.last10 {
			t.Errorf("Last10(%q) = %q, want %q", c.in, got, c.last10)
		}
	}
}
