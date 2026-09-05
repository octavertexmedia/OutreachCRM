package smartlead

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The integration is one-way. These tests exist so that stays true after
// someone reaches for a Smartlead endpoint that happens to write.

func TestSendRefusesNonGET(t *testing.T) {
	c := New("k")
	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
	} {
		req, err := http.NewRequest(method, "https://server.smartlead.ai/api/v1/campaigns/", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.send(req)
		if err == nil {
			if resp != nil {
				_ = resp.Body.Close()
			}
			t.Fatalf("%s was allowed through — writes to Smartlead must be refused", method)
		}
		if !errors.Is(err, ErrWriteAttempted) {
			t.Errorf("%s returned %v, want ErrWriteAttempted", method, err)
		}
	}
}

func TestSendAllowsGET(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	c := New("k")
	c.HTTP = srv.Client()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := c.send(req)
	if err != nil {
		t.Fatalf("GET should be allowed: %v", err)
	}
	_ = resp.Body.Close()
}

// A full import must only ever read. This drives the real code paths rather
// than trusting the chokepoint in isolation.
func TestImportOnlyIssuesReads(t *testing.T) {
	var mu sync.Mutex
	var methods []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()

		switch {
		case strings.HasSuffix(r.URL.Path, "/campaigns/"):
			w.Write([]byte(`{"data":[{"id":11,"name":"C","status":"PAUSED","track_settings":["DONT_EMAIL_OPEN"]}]}`))
		case strings.Contains(r.URL.Path, "/sequences"):
			w.Write([]byte(`{"data":[{"seq_number":1,"subject":"Hi","email_body":"Hello"}]}`))
		case strings.Contains(r.URL.Path, "/statistics"):
			if r.URL.Query().Get("offset") == "0" {
				w.Write([]byte(`{"total_stats":1,"data":[{"stats_id":"s1","lead_email":"a@x.com","lead_name":"A","sequence_number":1,"sent_time":"2025-01-01T00:00:00Z"}]}`))
				return
			}
			w.Write([]byte(`{"total_stats":1,"data":[]}`))
		case strings.Contains(r.URL.Path, "/leads"):
			if r.URL.Query().Get("offset") == "0" {
				w.Write([]byte(`{"total_leads":1,"data":[{"lead":{"id":1,"email":"a@x.com","first_name":"A"},"status":"COMPLETED"}]}`))
				return
			}
			w.Write([]byte(`{"data":[]}`))
		default:
			w.Write([]byte(`{"data":[]}`))
		}
	}))
	defer srv.Close()

	c := New("k")
	c.BaseURL = srv.URL + "/api/v1"
	c.RatePerMin = -1
	c.HTTP = srv.Client()

	// Exercise every read path the importer uses.
	if _, err := c.ListCampaigns(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListSequences(11); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListStatistics(11); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListCampaignLeads(11, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListEmailAccounts(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListBlockList(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MessageHistory(11, 1); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(methods) == 0 {
		t.Fatal("no requests were made — the test proves nothing")
	}
	for _, m := range methods {
		if m != http.MethodGet {
			t.Errorf("import issued a %s request; Smartlead access must be read-only", m)
		}
	}
}

// A source-level guard. The runtime check only fires on a request that is
// actually made; this catches a write helper the moment it is written, even if
// nothing calls it yet.
func TestNoWriteVerbsInPackageSource(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	banned := []string{
		"http.MethodPost", "http.MethodPut", "http.MethodPatch", "http.MethodDelete",
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, verb := range banned {
			if strings.Contains(src, verb) {
				t.Errorf("%s references %s — the Smartlead integration is read-only. "+
					"If a write is genuinely required, that is a product decision, not a refactor.",
					name, verb)
			}
		}
	}
}
