package enrichment

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/manishkumar/outreachcrm/internal/llm"
	"github.com/manishkumar/outreachcrm/internal/models"
)

type Result struct {
	Category     string   `json:"category"`
	Issues       []string `json:"issues"`
	PremiumScore int      `json:"premium_score"`
	Confidence   int      `json:"confidence"`
	CostCents    int      `json:"cost_cents"`
	Notes        string   `json:"notes"`
	Signals      []string `json:"signals"`
}

type Service struct {
	LLM        *llm.Client
	HTTPClient *http.Client
}

func (s *Service) client() *http.Client {
	if s.HTTPClient == nil {
		s.HTTPClient = &http.Client{Timeout: 12 * time.Second}
	}
	return s.HTTPClient
}

func (s *Service) crawlSignals(website string) []string {
	var sigs []string
	if website == "" {
		sigs = append(sigs, "no_website")
		return sigs
	}
	url := website
	if !strings.HasPrefix(url, "http") {
		url = "https://" + url
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return append(sigs, "fetch_error")
	}
	req.Header.Set("User-Agent", "OutReachCRM-Enrich/1.0")
	res, err := s.client().Do(req)
	if err != nil {
		return append(sigs, "unreachable")
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64*1024))
	lower := strings.ToLower(string(body))
	if res.StatusCode >= 400 {
		sigs = append(sigs, fmt.Sprintf("http_%d", res.StatusCode))
	}
	if strings.HasPrefix(url, "http://") {
		sigs = append(sigs, "insecure_http")
	}
	if !strings.Contains(lower, "viewport") {
		sigs = append(sigs, "missing_viewport_mobile")
	}
	if strings.Contains(lower, "googlesyndication") || strings.Contains(lower, "gtag/js") || strings.Contains(lower, "facebook.net/en_us/fbevents") {
		sigs = append(sigs, "runs_ads_or_pixels")
	}
	if len(body) < 1500 {
		sigs = append(sigs, "thin_html")
	}
	if strings.Contains(lower, "wix.com") || strings.Contains(lower, "blogspot") || strings.Contains(lower, "squarespace") {
		sigs = append(sigs, "template_site_builder")
	}
	return sigs
}

func (s *Service) Enrich(ctx context.Context, lead models.Lead) (Result, error) {
	signals := s.crawlSignals(lead.Website)
	if s.LLM == nil || !s.LLM.Enabled() {
		r := heuristic(lead, signals)
		r.CostCents = 0
		return r, nil
	}
	system := `You are a B2B lead enrichment engine. Score businesses for outreach premium packages.
Return JSON: {"category":"string","issues":["..."],"premium_score":0-100,"confidence":0-100,"notes":"string"}
Use crawl signals heavily. Score higher when ads spend likely + outdated site + weak mobile + poor reviews.`
	user := fmt.Sprintf(`Lead:
Name: %s
Website: %s
Phone: %s
Email: %s
Google rating: %.1f
Notes: %s
Crawl signals: %s`, lead.Name, lead.Website, lead.Phone, lead.Email, lead.GoogleRating, lead.Notes, strings.Join(signals, ", "))

	raw, err := s.LLM.Chat(ctx, system, user, true)
	if err != nil {
		return Result{}, err
	}
	var r Result
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return Result{}, fmt.Errorf("parse enrichment json: %w", err)
	}
	r.Signals = signals
	if r.PremiumScore < 0 {
		r.PremiumScore = 0
	}
	if r.PremiumScore > 100 {
		r.PremiumScore = 100
	}
	if r.Confidence == 0 {
		r.Confidence = 55 + len(signals)*5
		if r.Confidence > 95 {
			r.Confidence = 95
		}
	}
	if r.Category == "" {
		r.Category = "uncategorized"
	}
	r.CostCents = 2 // approximate per enrich call for budget tracking
	return r, nil
}

func heuristic(lead models.Lead, signals []string) Result {
	issues := []string{}
	score := 40
	conf := 40
	for _, sig := range signals {
		switch sig {
		case "no_website", "unreachable":
			issues = append(issues, "website missing or unreachable")
			score += 15
			conf += 10
		case "insecure_http", "template_site_builder", "thin_html":
			issues = append(issues, "outdated or fragile website")
			score += 12
			conf += 8
		case "missing_viewport_mobile":
			issues = append(issues, "weak mobile experience")
			score += 10
			conf += 8
		case "runs_ads_or_pixels":
			issues = append(issues, "spending on ads with likely site issues")
			score += 18
			conf += 12
		}
	}
	if lead.GoogleRating > 0 && lead.GoogleRating < 3.8 {
		issues = append(issues, "weak Google reviews")
		score += 15
		conf += 10
	}
	if len(issues) == 0 {
		issues = append(issues, "generic local SEO opportunity")
		score += 8
	}
	if score > 100 {
		score = 100
	}
	if conf > 95 {
		conf = 95
	}
	cat := "local business"
	if lead.Category != "" {
		cat = lead.Category
	}
	return Result{
		Category:     cat,
		Issues:       issues,
		PremiumScore: score,
		Confidence:   conf,
		Notes:        "Heuristic+crawl enrichment",
		Signals:      signals,
	}
}

func IssuesJSON(issues []string) string {
	b, _ := json.Marshal(issues)
	return string(b)
}
