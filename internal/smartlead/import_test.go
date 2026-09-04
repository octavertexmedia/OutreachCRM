package smartlead

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/manishkumar/outreachcrm/internal/models"
	"github.com/manishkumar/outreachcrm/internal/store"
)

func TestImportResumeFillsStatsAndReplies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/email-accounts/":
			w.Write([]byte(`{"data":[]}`))
		case "/api/v1/campaigns/", "/api/v1/campaigns":
			w.Write([]byte(`{"data":[{"id":11,"name":"Imported","status":"PAUSED","max_leads_per_day":10}]}`))
		case "/api/v1/campaigns/11":
			w.Write([]byte(`{"id":11,"name":"Imported","status":"PAUSED"}`))
		case "/api/v1/campaigns/11/sequences":
			w.Write([]byte(`{"data":[{"seq_number":1,"subject":"Seq hi","email_body":"Seq body"}]}`))
		case "/api/v1/campaigns/11/leads":
			if r.URL.Query().Get("offset") == "0" {
				w.Write([]byte(`{"data":[{"status":"INTERESTED","reply_count":1,"last_email_sequence_sent":1,"lead":{"id":1,"email":"ada@ex.com","first_name":"Ada"}}]}`))
				return
			}
			w.Write([]byte(`{"data":[]}`))
		case "/api/v1/campaigns/11/statistics":
			if r.URL.Query().Get("offset") == "0" {
				w.Write([]byte(`{"data":[{"id":"st-1","lead_email":"ada@ex.com","email_sequence_number":1,"sent_time":"2026-01-02 10:00:00","is_replied":true,"email_subject":"Stats subject","email_message":"Stats body here"}]}`))
				return
			}
			w.Write([]byte(`{"data":[]}`))
		case "/api/v1/campaigns/11/leads/1/message-history":
			w.Write([]byte(`{"history":[{"id":"in-1","direction":"inbound","subject":"Re: hi","plain_text":"We are interested","sent_at":"2026-01-03T10:00:00Z"},{"id":"out-1","direction":"outbound","subject":"Hist subject","plain_text":"Hist body","sent_at":"2026-01-02T10:00:00Z"}]}`))
		case "/api/v1/leads/block-list":
			w.Write([]byte(`{"data":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.CreateUser("admin@ex.com", "secret-pass-1", models.RoleAdmin); err != nil {
		t.Fatal(err)
	}

	client := New("k")
	client.BaseURL = srv.URL + "/api/v1"
	client.MinSleep = 0
	client.HTTP = srv.Client()

	wsID, err := st.EnsureNamedWorkspace("Smartlead")
	if err != nil {
		t.Fatal(err)
	}
	campID, err := st.CreateCampaign(models.Campaign{OwnerID: 1, WorkspaceID: wsID, Name: "Imported", Status: "paused"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SmartleadRemember("campaign", "11", campID); err != nil {
		t.Fatal(err)
	}
	leadID, err := st.CreateLead(models.Lead{OwnerID: 1, WorkspaceID: wsID, Name: "Ada", Email: "ada@ex.com", Source: "smartlead"})
	if err != nil {
		t.Fatal(err)
	}
	clID, err := st.ImportCampaignLead(campID, leadID, 1, "completed", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SmartleadRemember("campaign_lead:11", "ada@ex.com", clID); err != nil {
		t.Fatal(err)
	}
	n, _ := st.CountScheduledOutbound(clID)
	if n != 0 {
		t.Fatalf("pre-import queued %d", n)
	}

	stats, err := Run(st, client, Options{WorkspaceName: "Smartlead"})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Outbound < 1 {
		t.Fatalf("expected historical outbound on resume, stats=%+v errors=%v", stats, stats.Errors)
	}
	if stats.Replies < 1 {
		t.Fatalf("expected inbound reply, stats=%+v errors=%v", stats, stats.Errors)
	}
	n, _ = st.CountScheduledOutbound(clID)
	if n != 0 {
		t.Fatalf("must not queue scheduled mail, got %d", n)
	}
	hist, err := st.ListSentHistory(true, 1, wsID, 20)
	if err != nil || len(hist) == 0 {
		t.Fatalf("sent history %v n=%d", err, len(hist))
	}
	foundBody := false
	for _, m := range hist {
		if m.Subject == "Stats subject" && m.Body == "Stats body here" {
			foundBody = true
		}
		if m.Status == "scheduled" || m.Status == "sending" {
			t.Fatalf("status %s", m.Status)
		}
	}
	if !foundBody {
		t.Fatalf("expected stats subject/body on outbound, got %+v", hist)
	}
	replies, err := st.ListRepliesScoped(true, 1, wsID, 20)
	if err != nil || len(replies) < 1 {
		t.Fatalf("replies %v n=%d", err, len(replies))
	}
	if replies[0].Body != "We are interested" {
		t.Fatalf("reply body %q", replies[0].Body)
	}
	a, err := st.Analytics(wsID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Sent < 1 || a.Replies < 1 {
		t.Fatalf("analytics sent=%d replies=%d", a.Sent, a.Replies)
	}
	sentAfterFirst := a.Sent
	repliesAfterFirst := a.Replies

	stats2, err := Run(st, client, Options{WorkspaceName: "Smartlead"})
	if err != nil {
		t.Fatal(err)
	}
	_ = stats2
	a2, err := st.Analytics(wsID)
	if err != nil {
		t.Fatal(err)
	}
	if a2.Sent != sentAfterFirst || a2.Replies != repliesAfterFirst {
		t.Fatalf("second run grew history sent %d→%d replies %d→%d (new outbound=%d replies=%d)", sentAfterFirst, a2.Sent, repliesAfterFirst, a2.Replies, stats2.Outbound, stats2.Replies)
	}
}

func TestEachStatisticsStreamsPages(t *testing.T) {
	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/campaigns/9/statistics" {
			http.NotFound(w, r)
			return
		}
		off := r.URL.Query().Get("offset")
		lim := r.URL.Query().Get("limit")
		if lim != "2" {
			t.Errorf("limit=%s want 2", lim)
		}
		offsets = append(offsets, off)
		switch off {
		case "0":
			w.Write([]byte(`{"total_stats":4,"data":[{"id":"1","lead_email":"a@x.com","email_sequence_number":1,"email_message":"<p>A</p>"},{"id":"2","lead_email":"b@x.com","email_sequence_number":1,"email_message":"<p>B</p>"}]}`))
		case "2":
			w.Write([]byte(`{"total_stats":4,"data":[{"id":"3","lead_email":"c@x.com","email_sequence_number":1,"email_message":"<p>C</p>"},{"id":"4","lead_email":"d@x.com","email_sequence_number":1,"email_message":"<p>D</p>"}]}`))
		default:
			w.Write([]byte(`{"total_stats":4,"data":[]}`))
		}
	}))
	defer srv.Close()
	c := New("k")
	c.BaseURL = srv.URL + "/api/v1"
	c.MinSleep = 0
	c.HTTP = srv.Client()
	c.StatsPageSize = 2

	var pages, rows, maxPage int
	var lastTotal int
	err := c.EachStatistics(9, func(page []StatRow, meta PageMeta) error {
		pages++
		rows += len(page)
		if len(page) > maxPage {
			maxPage = len(page)
		}
		lastTotal = meta.Total
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 || rows != 4 || maxPage != 2 || lastTotal != 4 {
		t.Fatalf("pages=%d rows=%d maxPage=%d total=%d offsets=%v", pages, rows, maxPage, lastTotal, offsets)
	}
	if len(offsets) != 2 || offsets[0] != "0" || offsets[1] != "2" {
		t.Fatalf("offsets=%v", offsets)
	}
}

func TestListStatisticsParsesBodies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/campaigns/3/statistics" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"9","lead_email":"b@x.com","email_sequence_number":2,"email_subject":"Sub","email_message":"<p>Hi</p>","sent_time":"2026-02-01"}]}`))
	}))
	defer srv.Close()
	c := New("k")
	c.BaseURL = srv.URL + "/api/v1"
	c.MinSleep = 0
	c.HTTP = srv.Client()
	rows, err := c.ListStatistics(3)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %+v", err, rows)
	}
	if rows[0].EmailSubject != "Sub" || rows[0].EmailMessage != "<p>Hi</p>" || rows[0].SeqNumber != 2 {
		t.Fatalf("%+v", rows[0])
	}
}

func TestImportStreamsStatisticsPages(t *testing.T) {
	var statOffsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/email-accounts/":
			w.Write([]byte(`{"data":[]}`))
		case "/api/v1/campaigns/", "/api/v1/campaigns":
			w.Write([]byte(`{"data":[{"id":22,"name":"Paged","status":"PAUSED"}]}`))
		case "/api/v1/campaigns/22":
			w.Write([]byte(`{"id":22,"name":"Paged","status":"PAUSED"}`))
		case "/api/v1/campaigns/22/sequences":
			w.Write([]byte(`{"data":[{"seq_number":1,"subject":"Hi","email_body":"Body"}]}`))
		case "/api/v1/campaigns/22/leads":
			if r.URL.Query().Get("offset") == "0" {
				w.Write([]byte(`{"total_leads":2,"data":[{"status":"COMPLETED","lead":{"id":1,"email":"ada@ex.com","first_name":"Ada"}},{"status":"COMPLETED","lead":{"id":2,"email":"bob@ex.com","first_name":"Bob"}}]}`))
				return
			}
			w.Write([]byte(`{"data":[]}`))
		case "/api/v1/campaigns/22/statistics":
			off := r.URL.Query().Get("offset")
			statOffsets = append(statOffsets, off)
			switch off {
			case "0":
				w.Write([]byte(`{"total_stats":2,"data":[{"id":"st-a","lead_email":"ada@ex.com","email_sequence_number":1,"sent_time":"2026-01-02 10:00:00","email_subject":"A","email_message":"<p>Ada html</p>"}]}`))
			case "1":
				w.Write([]byte(`{"total_stats":2,"data":[{"id":"st-b","lead_email":"bob@ex.com","email_sequence_number":1,"sent_time":"2026-01-02 11:00:00","email_subject":"B","email_message":"<p>Bob html</p>"}]}`))
			default:
				w.Write([]byte(`{"total_stats":2,"data":[]}`))
			}
		case "/api/v1/leads/block-list":
			w.Write([]byte(`{"data":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.CreateUser("admin@ex.com", "secret-pass-1", models.RoleAdmin); err != nil {
		t.Fatal(err)
	}

	client := New("k")
	client.BaseURL = srv.URL + "/api/v1"
	client.MinSleep = 0
	client.HTTP = srv.Client()
	client.StatsPageSize = 1

	stats, err := Run(st, client, Options{WorkspaceName: "Smartlead"})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Outbound != 2 {
		t.Fatalf("outbound=%d errors=%v offsets=%v", stats.Outbound, stats.Errors, statOffsets)
	}
	if len(statOffsets) < 2 || statOffsets[0] != "0" || statOffsets[1] != "1" {
		t.Fatalf("expected paged stats offsets, got %v", statOffsets)
	}
	hist, err := st.ListSentHistory(true, 1, stats.WorkspaceID, 20)
	if err != nil || len(hist) != 2 {
		t.Fatalf("history %v n=%d", err, len(hist))
	}

	statOffsets = nil
	stats2, err := Run(st, client, Options{WorkspaceName: "Smartlead"})
	if err != nil {
		t.Fatal(err)
	}
	if stats2.Outbound != 0 {
		t.Fatalf("re-run inserted %d more outbound", stats2.Outbound)
	}
	hist2, err := st.ListSentHistory(true, 1, stats.WorkspaceID, 20)
	if err != nil || len(hist2) != 2 {
		t.Fatalf("re-run history %v n=%d", err, len(hist2))
	}
}
