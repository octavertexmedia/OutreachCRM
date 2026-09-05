package smartlead

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("k")
	c.BaseURL = srv.URL + "/api/v1"
	c.RatePerMin = -1 // no pacing against a local test server
	c.HTTP = srv.Client()
	return c
}

// Smartlead's track_settings lists what is DISABLED, so an empty array means
// full tracking. Getting this backwards would mark every campaign untracked.
func TestParseTrackSettings(t *testing.T) {
	cases := []struct {
		name                  string
		json                  string
		wantOpens, wantClicks bool
	}{
		{"empty means fully tracked", `[]`, true, true},
		{"opens disabled", `["DONT_EMAIL_OPEN"]`, false, true},
		{"clicks disabled", `["DONT_LINK_CLICK"]`, true, false},
		{"both disabled — the live default", `["DONT_EMAIL_OPEN","DONT_LINK_CLICK"]`, false, false},
		{"absent field defaults to tracked", `null`, true, true},
		{"unknown entries ignored", `["SOMETHING_ELSE"]`, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := `{"data":[{"id":11,"name":"C","status":"PAUSED","track_settings":` + c.json + `}]}`
			cl := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(body))
			})
			camps, err := cl.ListCampaigns()
			if err != nil {
				t.Fatal(err)
			}
			if len(camps) != 1 {
				t.Fatalf("got %d campaigns", len(camps))
			}
			if camps[0].TrackOpens != c.wantOpens || camps[0].TrackClicks != c.wantClicks {
				t.Errorf("opens/clicks = %v/%v, want %v/%v",
					camps[0].TrackOpens, camps[0].TrackClicks, c.wantOpens, c.wantClicks)
			}
		})
	}
}

// These fields exist on every statistics row and were previously discarded.
// lead_category in particular is Smartlead's own reply classification.
func TestStatRowCapturesEngagementAndCategory(t *testing.T) {
	row := `{
	  "stats_id":"abc","lead_email":"a@x.com","lead_name":"A",
	  "sequence_number":2,"sent_time":"2025-12-04T20:39:07.287Z",
	  "open_time":"2025-12-04T21:00:00.000Z","click_time":"2025-12-04T21:05:00.000Z",
	  "reply_time":"2025-12-05T09:00:00.000Z",
	  "open_count":3,"click_count":1,
	  "lead_category":"Meeting Request","ignore_reply":true,
	  "is_bounced":false,"is_unsubscribed":false,
	  "email_subject":"Hi","email_message":"<div>Hello</div>"
	}`
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") == "0" {
			w.Write([]byte(`{"total_stats":1,"data":[` + row + `]}`))
			return
		}
		w.Write([]byte(`{"total_stats":1,"data":[]}`))
	})

	rows, err := c.ListStatistics(11)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows", len(rows))
	}
	got := rows[0]
	if got.Category != "Meeting Request" {
		t.Errorf("Category = %q", got.Category)
	}
	if !got.IgnoreReply {
		t.Error("IgnoreReply should be true")
	}
	if got.OpenCount != 3 || got.ClickCount != 1 {
		t.Errorf("open/click count = %d/%d, want 3/1", got.OpenCount, got.ClickCount)
	}
	if got.OpenAt == "" || got.ClickAt == "" || got.ReplyAt == "" {
		t.Errorf("timestamps missing: open=%q click=%q reply=%q", got.OpenAt, got.ClickAt, got.ReplyAt)
	}
	// The derived booleans must still agree with the timestamps.
	if !got.Opened || !got.Clicked || !got.Replied {
		t.Errorf("derived flags = %v/%v/%v, want all true", got.Opened, got.Clicked, got.Replied)
	}
}

// A limit above the server maximum returns an empty array with HTTP 200, which
// is indistinguishable from end-of-data. Clamping is what stops an import from
// silently succeeding with nothing.
func TestPageSizeIsClamped(t *testing.T) {
	var sawLimit string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if sawLimit == "" {
			sawLimit = r.URL.Query().Get("limit")
		}
		w.Write([]byte(`{"total_stats":0,"data":[]}`))
	})
	c.StatsPageSize = 5000
	if _, err := c.ListStatistics(11); err != nil {
		t.Fatal(err)
	}
	if sawLimit != fmt.Sprint(maxStatsPageSize) {
		t.Errorf("requested limit=%s, want it clamped to %d", sawLimit, maxStatsPageSize)
	}
}

// The leads endpoint caps at 100, not 1000. Raising it silently fetched zero
// leads in production, which meant no reply threads were fetched either.
func TestLeadsPageSizeIsClampedLower(t *testing.T) {
	var sawLimit string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if sawLimit == "" {
			sawLimit = r.URL.Query().Get("limit")
		}
		w.Write([]byte(`{"total_leads":0,"data":[]}`))
	})
	c.LeadsPageSize = 1000
	if _, err := c.ListCampaignLeads(11, ""); err != nil {
		t.Fatal(err)
	}
	if sawLimit != fmt.Sprint(maxLeadsPageSize) {
		t.Errorf("requested leads limit=%s, want it clamped to %d", sawLimit, maxLeadsPageSize)
	}
}

// And the same empty-first-page guard must protect leads, not just statistics.
func TestEmptyFirstLeadsPageIsAnError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"total_leads":19555,"data":[]}`))
	})
	if _, err := c.ListCampaignLeads(11, ""); err == nil {
		t.Fatal("an empty first leads page with a non-zero total must not read as end-of-data")
	}
}

// If the server reports rows but hands back an empty first page, something is
// wrong with the request. Reporting success would import nothing and look fine.
func TestEmptyFirstPageWithTotalIsAnError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"total_stats":45415,"data":[]}`))
	})
	_, err := c.ListStatistics(11)
	if err == nil {
		t.Fatal("expected an error, got nil — an empty first page must not read as end-of-data")
	}
	if !strings.Contains(err.Error(), "empty first page") {
		t.Errorf("error = %v, want it to explain the empty first page", err)
	}
}

// An empty page at a later offset is a legitimate end-of-data.
func TestEmptyLaterPageEndsCleanly(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") == "0" {
			w.Write([]byte(`{"total_stats":1,"data":[{"stats_id":"a","lead_email":"a@x.com"}]}`))
			return
		}
		w.Write([]byte(`{"total_stats":1,"data":[]}`))
	})
	c.StatsPageSize = 1
	rows, err := c.ListStatistics(11)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("got %d rows, want 1", len(rows))
	}
}

// The limiter is what keeps a long import inside 60 requests per minute.
func TestRateLimiterPacesRequests(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	})
	// 600/min is 100ms apart — fast enough for a test, slow enough to measure.
	c.RatePerMin = 600

	start := time.Now()
	for i := 0; i < 4; i++ {
		if _, err := c.ListBlockList(); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	// Four requests at 100ms spacing: the first is immediate, so expect ~300ms.
	if elapsed < 250*time.Millisecond {
		t.Errorf("4 requests took %s — the limiter is not pacing them", elapsed)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	cases := map[string]time.Duration{
		"":     0,
		"5":    5 * time.Second,
		"0":    0,
		"-1":   0,
		"abc":  0,
		"9999": 120 * time.Second, // capped so a bad header cannot stall an import
	}
	for in, want := range cases {
		if got := retryAfter(in); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}
