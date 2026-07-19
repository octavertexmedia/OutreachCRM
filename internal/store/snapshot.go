package store

import (
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/manishkumar/outreachcrm/internal/models"
)

// BusinessSnapshot builds chart-ready aggregates for the command-center dashboard.
func (s *Store) BusinessSnapshot(admin bool, ownerID, workspaceID int64) (models.BusinessSnapshot, error) {
	var snap models.BusinessSnapshot
	snap.GeneratedAt = time.Now().UTC().Format(time.RFC3339)

	st, err := s.Stats(admin, ownerID)
	if err != nil {
		return snap, err
	}
	// Sent today
	today := now().Format("2006-01-02")
	if admin {
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM outbound_messages WHERE status='sent' AND sent_at LIKE ?`, today+"%").Scan(&st.SentToday)
	} else {
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM outbound_messages om JOIN campaigns c ON c.id=om.campaign_id
			WHERE om.status='sent' AND om.sent_at LIKE ? AND c.owner_id=?`, today+"%", ownerID).Scan(&st.SentToday)
	}
	snap.KPIs = st

	funnel, _ := s.PipelineFunnel(admin, ownerID, workspaceID)
	snap.Funnel = funnel

	analytics, _ := s.Analytics(workspaceID)
	snap.Analytics = analytics

	snap.Locations = s.snapshotLocations(admin, ownerID, workspaceID)
	snap.Campaigns = s.snapshotCampaigns(admin, ownerID, workspaceID)
	snap.Categories = s.snapshotNamed(admin, ownerID, workspaceID,
		`SELECT COALESCE(NULLIF(trim(category),''),'(uncategorized)') AS n, COUNT(*) FROM leads WHERE 1=1`)
	snap.Sources = s.snapshotNamed(admin, ownerID, workspaceID,
		`SELECT COALESCE(NULLIF(trim(source),''),'manual') AS n, COUNT(*) FROM leads WHERE 1=1`)
	snap.Intents = s.snapshotIntents(admin, ownerID, workspaceID)
	snap.LeadDays = s.snapshotLeadDays(admin, ownerID, workspaceID, 90)
	snap.SendDays = s.snapshotSendDays(admin, ownerID, workspaceID, 90)
	snap.Words = s.snapshotWords(admin, ownerID, workspaceID)

	return snap, nil
}

func (s *Store) leadScope(admin bool, ownerID, workspaceID int64) (extra string, args []any) {
	if !admin {
		extra += ` AND owner_id=?`
		args = append(args, ownerID)
	} else if workspaceID > 0 {
		extra += ` AND workspace_id=?`
		args = append(args, workspaceID)
	}
	return extra, args
}

func (s *Store) snapshotNamed(admin bool, ownerID, workspaceID int64, baseSQL string) []models.NamedCount {
	extra, args := s.leadScope(admin, ownerID, workspaceID)
	q := baseSQL + extra + ` GROUP BY n ORDER BY COUNT(*) DESC LIMIT 12`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []models.NamedCount
	for rows.Next() {
		var n models.NamedCount
		if err := rows.Scan(&n.Name, &n.Count); err != nil {
			continue
		}
		n.Value = float64(n.Count)
		out = append(out, n)
	}
	return out
}

func (s *Store) snapshotIntents(admin bool, ownerID, workspaceID int64) []models.NamedCount {
	q := `SELECT COALESCE(NULLIF(trim(intent),''),'other'), COUNT(*) FROM inbound_replies WHERE 1=1`
	var args []any
	if !admin {
		q += ` AND (owner_id=? OR owner_id IS NULL)`
		args = append(args, ownerID)
	} else if workspaceID > 0 {
		q += ` AND (workspace_id=? OR workspace_id IS NULL)`
		args = append(args, workspaceID)
	}
	q += ` GROUP BY 1 ORDER BY COUNT(*) DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []models.NamedCount
	for rows.Next() {
		var n models.NamedCount
		if err := rows.Scan(&n.Name, &n.Count); err != nil {
			continue
		}
		n.Value = float64(n.Count)
		out = append(out, n)
	}
	return out
}

func (s *Store) snapshotCampaigns(admin bool, ownerID, workspaceID int64) []models.CampaignNode {
	q := `SELECT c.id, c.name, c.status,
		(SELECT COUNT(*) FROM campaign_leads cl WHERE cl.campaign_id=c.id),
		(SELECT COUNT(*) FROM outbound_messages om WHERE om.campaign_id=c.id AND om.status='sent'),
		(SELECT COUNT(*) FROM outbound_messages om WHERE om.campaign_id=c.id AND om.status IN ('failed','dead')),
		(SELECT COUNT(*) FROM outbound_messages om WHERE om.campaign_id=c.id AND om.status='scheduled')
		FROM campaigns c WHERE 1=1`
	var args []any
	if !admin {
		q += ` AND c.owner_id=?`
		args = append(args, ownerID)
	} else if workspaceID > 0 {
		q += ` AND c.workspace_id=?`
		args = append(args, workspaceID)
	}
	q += ` ORDER BY c.id DESC LIMIT 40`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []models.CampaignNode
	for rows.Next() {
		var n models.CampaignNode
		if err := rows.Scan(&n.ID, &n.Name, &n.Status, &n.Enrolled, &n.Sent, &n.Failed, &n.Queued); err != nil {
			continue
		}
		if n.Name == "" {
			n.Name = "Campaign"
		}
		out = append(out, n)
	}
	return out
}

func (s *Store) snapshotLeadDays(admin bool, ownerID, workspaceID int64, days int) []models.DayCount {
	extra, args := s.leadScope(admin, ownerID, workspaceID)
	q := `SELECT substr(created_at,1,10) AS d, COUNT(*) FROM leads WHERE 1=1` + extra +
		` AND created_at >= ? GROUP BY d ORDER BY d ASC`
	since := now().AddDate(0, 0, -days).Format("2006-01-02")
	args = append(args, since)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	byDay := map[string]int{}
	for rows.Next() {
		var d string
		var n int
		if err := rows.Scan(&d, &n); err != nil {
			continue
		}
		byDay[d] = n
	}
	// Prior window for "prev vs new" overlay
	prevSince := now().AddDate(0, 0, -days*2).Format("2006-01-02")
	prevEnd := since
	argsPrev := append([]any{}, args[:len(args)-1]...)
	argsPrev = append(argsPrev, prevSince, prevEnd)
	qPrev := `SELECT substr(created_at,1,10) AS d, COUNT(*) FROM leads WHERE 1=1` + extra +
		` AND created_at >= ? AND created_at < ? GROUP BY d`
	rows2, err := s.db.Query(qPrev, argsPrev...)
	prevByDay := map[string]int{}
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var d string
			var n int
			if err := rows2.Scan(&d, &n); err != nil {
				continue
			}
			prevByDay[d] = n
		}
	}

	out := make([]models.DayCount, 0, days)
	for i := days - 1; i >= 0; i-- {
		day := now().AddDate(0, 0, -i).Format("2006-01-02")
		prevDay := now().AddDate(0, 0, -i-days).Format("2006-01-02")
		out = append(out, models.DayCount{Day: day, Count: byDay[day], Prev: prevByDay[prevDay]})
	}
	return out
}

func (s *Store) snapshotSendDays(admin bool, ownerID, workspaceID int64, days int) []models.DayCount {
	since := now().AddDate(0, 0, -days).Format("2006-01-02")
	q := `SELECT substr(om.sent_at,1,10) AS d, COUNT(*) FROM outbound_messages om
		JOIN campaigns c ON c.id=om.campaign_id
		WHERE om.status='sent' AND om.sent_at >= ?`
	args := []any{since}
	if !admin {
		q += ` AND c.owner_id=?`
		args = append(args, ownerID)
	} else if workspaceID > 0 {
		q += ` AND c.workspace_id=?`
		args = append(args, workspaceID)
	}
	q += ` GROUP BY d ORDER BY d ASC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	byDay := map[string]int{}
	for rows.Next() {
		var d string
		var n int
		if err := rows.Scan(&d, &n); err != nil {
			continue
		}
		byDay[d] = n
	}
	out := make([]models.DayCount, 0, days)
	for i := days - 1; i >= 0; i-- {
		day := now().AddDate(0, 0, -i).Format("2006-01-02")
		out = append(out, models.DayCount{Day: day, Count: byDay[day]})
	}
	return out
}

func (s *Store) snapshotLocations(admin bool, ownerID, workspaceID int64) []models.GeoPoint {
	extra, args := s.leadScope(admin, ownerID, workspaceID)
	q := `SELECT website, email FROM leads WHERE 1=1` + extra + ` LIMIT 5000`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var website, email string
		if err := rows.Scan(&website, &email); err != nil {
			continue
		}
		code := geoCodeFromHost(hostFromURL(website))
		if code == "" {
			code = geoCodeFromHost(hostFromEmail(email))
		}
		if code == "" {
			code = "XX"
		}
		counts[code]++
	}
	out := make([]models.GeoPoint, 0, len(counts))
	for code, n := range counts {
		meta := geoMeta(code)
		out = append(out, models.GeoPoint{
			Code: code, Country: meta.name, Count: n, Lat: meta.lat, Lng: meta.lng,
		})
	}
	// sort by count desc
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Count > out[i].Count {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if len(out) > 24 {
		out = out[:24]
	}
	return out
}

var tokenRe = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+#./-]{2,}`)

func (s *Store) snapshotWords(admin bool, ownerID, workspaceID int64) []models.WordWeight {
	extra, args := s.leadScope(admin, ownerID, workspaceID)
	q := `SELECT COALESCE(category,''), COALESCE(company,''), COALESCE(notes,''), COALESCE(draft_subject,''), COALESCE(title,'')
		FROM leads WHERE 1=1` + extra + ` LIMIT 2000`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	freq := map[string]int{}
	stop := map[string]bool{
		"the": true, "and": true, "for": true, "with": true, "from": true, "that": true,
		"this": true, "your": true, "you": true, "are": true, "our": true, "www": true,
		"http": true, "https": true, "com": true, "inc": true, "ltd": true, "pvt": true,
		"null": true, "none": true, "n/a": true, "undefined": true,
	}
	for rows.Next() {
		var cat, company, notes, subject, title string
		if err := rows.Scan(&cat, &company, &notes, &subject, &title); err != nil {
			continue
		}
		blob := strings.ToLower(strings.Join([]string{cat, company, notes, subject, title}, " "))
		for _, tok := range tokenRe.FindAllString(blob, -1) {
			tok = strings.Trim(tok, ".-/+#")
			if len(tok) < 3 || stop[tok] {
				continue
			}
			// Prefer category/company tokens: weight later
			freq[tok]++
		}
		if c := strings.TrimSpace(strings.ToLower(cat)); c != "" && !stop[c] {
			freq[c] += 3
		}
		if c := strings.TrimSpace(strings.ToLower(company)); c != "" {
			for _, w := range strings.FieldsFunc(c, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
				if len(w) >= 3 && !stop[w] {
					freq[w] += 2
				}
			}
		}
	}
	type pair struct {
		t string
		n int
	}
	var list []pair
	for t, n := range freq {
		if n < 2 {
			continue
		}
		list = append(list, pair{t, n})
	}
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j].n > list[i].n {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	if len(list) > 60 {
		list = list[:60]
	}
	out := make([]models.WordWeight, 0, len(list))
	for _, p := range list {
		out = append(out, models.WordWeight{Text: p.t, Value: p.n})
	}
	return out
}

func hostFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func hostFromEmail(email string) string {
	email = strings.TrimSpace(strings.ToLower(email))
	i := strings.LastIndex(email, "@")
	if i < 0 || i+1 >= len(email) {
		return ""
	}
	return email[i+1:]
}

func geoCodeFromHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return ""
	}
	// strip common prefixes
	host = strings.TrimPrefix(host, "www.")
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return ""
	}
	tld := parts[len(parts)-1]
	// multi-part ccTLDs
	if len(parts) >= 3 {
		two := parts[len(parts)-2] + "." + tld
		if _, ok := geoTable[two]; ok {
			return two
		}
	}
	if _, ok := geoTable[tld]; ok {
		return tld
	}
	// generic TLDs → international bucket
	switch tld {
	case "com", "net", "org", "io", "ai", "co", "app", "dev", "xyz", "info", "biz":
		return "INTL"
	}
	return ""
}

type geoInfo struct {
	name       string
	lat, lng   float64
}

var geoTable = map[string]geoInfo{
	"in":  {"India", 20.59, 78.96},
	"us":  {"United States", 37.09, -95.71},
	"uk":  {"United Kingdom", 55.38, -3.44},
	"co.uk": {"United Kingdom", 55.38, -3.44},
	"de":  {"Germany", 51.17, 10.45},
	"fr":  {"France", 46.23, 2.21},
	"au":  {"Australia", -25.27, 133.78},
	"ca":  {"Canada", 56.13, -106.35},
	"sg":  {"Singapore", 1.35, 103.82},
	"ae":  {"UAE", 23.42, 53.85},
	"nl":  {"Netherlands", 52.13, 5.29},
	"ie":  {"Ireland", 53.14, -7.69},
	"jp":  {"Japan", 36.20, 138.25},
	"kr":  {"South Korea", 35.91, 127.7},
	"br":  {"Brazil", -14.24, -51.93},
	"mx":  {"Mexico", 23.63, -102.55},
	"za":  {"South Africa", -30.56, 22.94},
	"se":  {"Sweden", 60.13, 18.64},
	"ch":  {"Switzerland", 46.82, 8.22},
	"es":  {"Spain", 40.46, -3.75},
	"it":  {"Italy", 41.87, 12.57},
	"pl":  {"Poland", 51.92, 19.15},
	"id":  {"Indonesia", -0.79, 113.92},
	"ph":  {"Philippines", 12.88, 121.77},
	"my":  {"Malaysia", 4.21, 101.98},
	"nz":  {"New Zealand", -40.90, 174.89},
	"INTL": {"International (.com/.io)", 20, 0},
	"XX":  {"Unknown", 10, 20},
}

func geoMeta(code string) geoInfo {
	if g, ok := geoTable[code]; ok {
		return g
	}
	return geoTable["XX"]
}
