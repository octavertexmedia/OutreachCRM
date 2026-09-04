package smartlead

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListCampaignsAndLeadsPagination(t *testing.T) {
	nCamp, nLead := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != "k" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/v1/campaigns/":
			nCamp++
			w.Write([]byte(`{"data":[{"id":11,"name":"Q1","status":"ACTIVE","max_leads_per_day":20,"scheduler_cron_value":{"tz":"Asia/Kolkata","startHour":"09:00","endHour":"18:00"}}]}`))
		case "/api/v1/campaigns/11/leads":
			nLead++
			off := r.URL.Query().Get("offset")
			if off == "0" {
				w.Write([]byte(`{"total_leads":3,"data":[` + leadJSON("a@x.com", 1) + `,` + leadJSON("b@x.com", 2) + `]}`))
				return
			}
			if off == "2" {
				w.Write([]byte(`{"total_leads":3,"data":[` + leadJSON("c@x.com", 3) + `]}`))
				return
			}
			w.Write([]byte(`{"data":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New("k")
	c.BaseURL = srv.URL + "/api/v1"
	c.RatePerMin = -1 // no pacing against a local test server
	c.HTTP = srv.Client()
	c.LeadsPageSize = 2

	camps, err := c.ListCampaigns()
	if err != nil {
		t.Fatal(err)
	}
	if len(camps) != 1 || camps[0].ID != 11 || camps[0].Name != "Q1" {
		t.Fatalf("campaigns=%+v", camps)
	}
	if camps[0].Timezone != "Asia/Kolkata" || camps[0].SendWindowStart != 9 {
		t.Fatalf("schedule=%+v", camps[0])
	}
	leads, err := c.ListCampaignLeads(11, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(leads) != 3 {
		t.Fatalf("leads=%d", len(leads))
	}
	if nCamp != 1 || nLead != 2 {
		t.Fatalf("calls camp=%d lead=%d", nCamp, nLead)
	}
	var pages int
	maxPage := 0
	if err := c.EachCampaignLeads(11, "", func(page []Lead, meta PageMeta) error {
		pages++
		if len(page) > maxPage {
			maxPage = len(page)
		}
		if meta.Total != 3 {
			t.Fatalf("total_leads=%d", meta.Total)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if pages != 2 || maxPage > 2 {
		t.Fatalf("lead stream pages=%d maxPage=%d", pages, maxPage)
	}
}

func leadJSON(email string, id int) string {
	return `{"status":"IN_PROGRESS","lead":{"id":` + itoa(id) + `,"email":"` + email + `","first_name":"A","company_name":"Co"}}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestMapEnrollment(t *testing.T) {
	en, lead, sup, _ := MapEnrollment("INTERESTED", false, 1)
	if en != "completed" || lead != "interested" {
		t.Fatalf("%s %s", en, lead)
	}
	en, lead, sup, _ = MapEnrollment("BOUNCED", false, 0)
	if en != "dead" || sup != "bounce" {
		t.Fatalf("%s %s", en, sup)
	}
	en, _, sup, _ = MapEnrollment("STARTED", true, 0)
	if en != "unsubscribed" || sup != "unsubscribed" {
		t.Fatalf("%s %s", en, sup)
	}
}

func TestNormalizeAPIKey(t *testing.T) {
	got := NormalizeAPIKey("Smartlead api key - abc-def_xyz")
	if got != "abc-def_xyz" {
		t.Fatalf("got %q", got)
	}
}
