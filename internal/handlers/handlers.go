package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/manishkumar/outreachcrm/internal/auth"
	"github.com/manishkumar/outreachcrm/internal/config"
	"github.com/manishkumar/outreachcrm/internal/crypto"
	"github.com/manishkumar/outreachcrm/internal/deliverability"
	"github.com/manishkumar/outreachcrm/internal/enrichment"
	"github.com/manishkumar/outreachcrm/internal/inbox"
	"github.com/manishkumar/outreachcrm/internal/models"
	"github.com/manishkumar/outreachcrm/internal/oauth"
	"github.com/manishkumar/outreachcrm/internal/search"
	"github.com/manishkumar/outreachcrm/internal/store"
	"github.com/manishkumar/outreachcrm/internal/writing"
)

type Server struct {
	Store          *store.Store
	Auth           *auth.Manager
	Box            *crypto.Box
	OAuth          *oauth.Managers
	Cfg            config.Config
	Enrichment     *enrichment.Service
	Writing        *writing.Service
	Inbox          *inbox.Service
	Deliverability *deliverability.Engine
	Search         *search.Service
	Templates      *template.Template
	Static         fs.FS
	ready          atomic.Bool
	reqCount       atomic.Uint64
}

func New(st *store.Store, a *auth.Manager, box *crypto.Box, oa *oauth.Managers, cfg config.Config,
	en *enrichment.Service, wr *writing.Service, in *inbox.Service, deliv *deliverability.Engine,
	searchSvc *search.Service, webFS fs.FS) (*Server, error) {
	funcs := template.FuncMap{
		"issues": func(s string) []string {
			var out []string
			_ = json.Unmarshal([]byte(s), &out)
			return out
		},
		"kindLabel": search.KindLabel,
		"badgeClass": func(status string) string {
			switch status {
			case "done", "positive", "active", "sent", "admin":
				return "done"
			case "error", "failed", "unsubscribe", "dead":
				return "bad"
			case "neutral", "enriching", "pending", "sender", "paused", "draft":
				return "warn"
			default:
				return ""
			}
		},
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(webFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	staticFS, err := fs.Sub(webFS, "static")
	if err != nil {
		return nil, err
	}
	s := &Server{
		Store: st, Auth: a, Box: box, OAuth: oa, Cfg: cfg,
		Enrichment: en, Writing: wr, Inbox: in, Deliverability: deliv,
		Search: searchSvc, Templates: tmpl, Static: staticFS,
	}
	s.ready.Store(true)
	return s, nil
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(s.Static))))
	s.mountBrandAssets(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("GET /u/{token}", s.unsubscribeGet)
	mux.HandleFunc("POST /u/{token}", s.unsubscribePost)
	mux.HandleFunc("GET /t/{token}", s.trackGet)

	mux.HandleFunc("GET /login", s.loginGet)
	mux.HandleFunc("POST /login", s.loginPost)
	mux.HandleFunc("POST /logout", s.logout)

	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /leads", s.leadsGet)
	mux.HandleFunc("POST /leads", s.leadsPost)
	mux.HandleFunc("POST /leads/seed", s.leadsSeed)
	mux.HandleFunc("POST /leads/seed-playbooks", s.leadsSeedPlaybooks)
	mux.HandleFunc("POST /leads/enrich-all", s.leadsEnrichAll)
	mux.HandleFunc("POST /leads/{id}/enrich", s.leadEnrich)
	mux.HandleFunc("POST /leads/{id}/generate-email", s.leadGenerateEmail)
	mux.HandleFunc("POST /leads/{id}/apply-draft", s.leadApplyDraft)

	mux.HandleFunc("GET /campaigns", s.campaignsGet)
	mux.HandleFunc("POST /campaigns", s.campaignsPost)
	mux.HandleFunc("POST /campaigns/{id}/steps", s.campaignAddStep)
	mux.HandleFunc("POST /campaigns/{id}/status", s.campaignStatus)
	mux.HandleFunc("POST /campaigns/{id}/enroll", s.campaignEnroll)
	mux.HandleFunc("GET /queue", s.queueGet)

	mux.HandleFunc("GET /accounts", s.accountsGet)
	mux.HandleFunc("POST /accounts", s.accountsPost)
	mux.HandleFunc("GET /oauth/google/start", s.oauthStartGoogle)
	mux.HandleFunc("GET /oauth/google/callback", s.oauthCallbackGoogle)
	mux.HandleFunc("GET /oauth/microsoft/start", s.oauthStartMicrosoft)
	mux.HandleFunc("GET /oauth/microsoft/callback", s.oauthCallbackMicrosoft)

	mux.HandleFunc("GET /inbox", s.inboxGet)
	mux.HandleFunc("POST /inbox/classify", s.inboxClassify)

	mux.HandleFunc("GET /users", auth.RequireAdmin(s.usersGet))
	mux.HandleFunc("POST /users", auth.RequireAdmin(s.usersPost))
	mux.HandleFunc("POST /users/{id}/active", auth.RequireAdmin(s.usersActive))

	s.registerProdRoutes(mux)
	s.registerPanelRoutes(mux)
	s.registerSearchRoutes(mux)

	return s.wrap(s.Auth.Middleware(mux))
}

func (s *Server) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic", "err", rec, "path", r.URL.Path)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		s.reqCount.Add(1)
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.Ping(); err != nil || !s.ready.Load() {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# HELP outreachcrm_http_requests_total Total HTTP requests\n")
	fmt.Fprintf(w, "# TYPE outreachcrm_http_requests_total counter\n")
	fmt.Fprintf(w, "outreachcrm_http_requests_total %d\n", s.reqCount.Load())
	fmt.Fprintf(w, "# HELP outreachcrm_build_info Build version\n")
	fmt.Fprintf(w, "# TYPE outreachcrm_build_info gauge\n")
	fmt.Fprintf(w, "outreachcrm_build_info{version=%q} 1\n", s.Cfg.AppVersion)
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.Templates.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("template", "name", name, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (s *Server) current(r *http.Request) models.SessionUser {
	u, _ := auth.UserFromContext(r.Context())
	return u
}

func (s *Server) canAccessOwner(u models.SessionUser, ownerID int64) bool {
	return u.IsAdmin() || ownerID == u.ID || ownerID == 0
}

func (s *Server) loginGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.Auth.UserFromRequest(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, "login.html", map[string]any{"Error": ""})
}

func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	if !s.Auth.AllowLogin(auth.ClientIP(r)) {
		s.render(w, "login.html", map[string]any{"Error": "Too many login attempts. Try again shortly."})
		return
	}
	_ = r.ParseForm()
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	u, err := s.Store.Authenticate(email, password)
	if err != nil {
		s.render(w, "login.html", map[string]any{"Error": "Invalid email or password"})
		return
	}
	if u.TOTPEnabled {
		http.SetCookie(w, &http.Cookie{Name: "orc_pending", Value: fmt.Sprintf("%d", u.ID), Path: "/", HttpOnly: true, MaxAge: 300, SameSite: http.SameSiteLaxMode, Secure: s.Cfg.CookieSecure})
		http.Redirect(w, r, "/login/totp", http.StatusSeeOther)
		return
	}
	s.Auth.SetSession(w, models.SessionUser{ID: u.ID, Email: u.Email, Role: u.Role, WorkspaceID: u.WorkspaceID})
	s.Store.Audit(u.WorkspaceID, u.ID, "login", "user", fmt.Sprintf("%d", u.ID), "")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.Auth.ClearSession(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) mountBrandAssets(mux *http.ServeMux) {
	serve := func(name, contentType string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			b, err := fs.ReadFile(s.Static, name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			if contentType != "" {
				w.Header().Set("Content-Type", contentType)
			}
			w.Header().Set("Cache-Control", "public, max-age=86400")
			_, _ = w.Write(b)
		}
	}
	mux.HandleFunc("GET /favicon.ico", serve("favicon.ico", "image/x-icon"))
	mux.HandleFunc("GET /favicon.svg", serve("favicon.svg", "image/svg+xml"))
	mux.HandleFunc("GET /favicon-96x96.png", serve("favicon-96x96.png", "image/png"))
	mux.HandleFunc("GET /apple-touch-icon.png", serve("apple-touch-icon.png", "image/png"))
	mux.HandleFunc("GET /site.webmanifest", serve("site.webmanifest", "application/manifest+json"))
	mux.HandleFunc("GET /web-app-manifest-192x192.png", serve("web-app-manifest-192x192.png", "image/png"))
	mux.HandleFunc("GET /web-app-manifest-512x512.png", serve("web-app-manifest-512x512.png", "image/png"))
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if u, ok := s.Auth.UserFromRequest(r); ok {
		s.dashboardFor(w, r, u)
		return
	}
	s.render(w, "landing.html", map[string]any{})
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	s.dashboardFor(w, r, s.current(r))
}

func (s *Server) dashboardFor(w http.ResponseWriter, r *http.Request, u models.SessionUser) {
	st, err := s.Store.Stats(u.IsAdmin(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	funnel, _ := s.Store.PipelineFunnel(u.IsAdmin(), u.ID, u.WorkspaceID)
	s.render(w, "dashboard.html", map[string]any{"Stats": st, "Funnel": funnel, "Nav": "dashboard", "User": u})
}

func (s *Server) leadsGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	// One-time cleanup of seeded example.* / demo leads after deploy.
	if s.Store.GetSetting("dummy_leads_purged", "") != "1" {
		if n, err := s.Store.PurgeDummyLeads(); err == nil {
			_ = s.Store.SetSetting("dummy_leads_purged", "1")
			if n > 0 {
				s.Store.Audit(u.WorkspaceID, u.ID, "lead.purge_dummy", "lead", "", fmt.Sprintf("n=%d", n))
			}
		}
	}
	leads, err := s.Store.ListLeads(u.IsAdmin(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	purgedN, _ := strconv.Atoi(r.URL.Query().Get("purged"))
	s.render(w, "leads.html", map[string]any{
		"Leads": leads, "Nav": "leads", "User": u,
		"LeadCount": len(leads),
		"Purged":    r.URL.Query().Get("purged") != "",
		"PurgedN":   purgedN,
		"Imported":  r.URL.Query().Get("imported") == "1",
		"ImportedN": r.URL.Query().Get("n"),
	})
}

func (s *Server) leadsPost(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_ = r.ParseForm()
	rating, _ := strconv.ParseFloat(r.FormValue("google_rating"), 64)
	var consentAt *time.Time
	if r.FormValue("consent") == "1" {
		t := time.Now().UTC()
		consentAt = &t
	}
	email := strings.TrimSpace(r.FormValue("email"))
	source := "manual"
	id, err := s.Store.CreateLead(models.Lead{
		OwnerID:       u.ID,
		WorkspaceID:   u.WorkspaceID,
		Name:          strings.TrimSpace(r.FormValue("name")),
		Company:       strings.TrimSpace(r.FormValue("company")),
		Title:         strings.TrimSpace(r.FormValue("title")),
		Website:       strings.TrimSpace(r.FormValue("website")),
		Phone:         strings.TrimSpace(r.FormValue("phone")),
		Email:         email,
		GoogleRating:  rating,
		Notes:         strings.TrimSpace(r.FormValue("notes")),
		Source:        source,
		ConsentAt:     consentAt,
		ConsentSource: r.FormValue("consent_source"),
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.Store.Audit(u.WorkspaceID, u.ID, "lead.create", "lead", strconv.FormatInt(id, 10), "")
	if lead, err := s.Store.GetLead(id); err == nil {
		s.indexDocs(search.DocFromLead(lead))
	}
	http.Redirect(w, r, "/leads", http.StatusSeeOther)
}

func (s *Server) leadEnrich(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	lead, err := s.Store.GetLead(id)
	if err != nil || !s.canAccessOwner(u, lead.OwnerID) {
		http.Error(w, "not found", 404)
		return
	}
	_ = s.Store.SetLeadEnrichmentStatus(id, "enriching")
	budget, _ := strconv.Atoi(s.Store.GetSetting("llm_daily_budget_cents", "500"))
	spent, _ := s.Store.LLMSpendTodayCents(u.WorkspaceID)
	if budget > 0 && spent >= budget && s.Cfg.OpenAIAPIKey != "" {
		http.Error(w, "LLM daily budget exceeded", http.StatusTooManyRequests)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
	defer cancel()
	res, err := s.Enrichment.Enrich(ctx, lead)
	if err != nil {
		_ = s.Store.SetLeadEnrichmentStatus(id, "error")
		lead.EnrichmentStatus = "error"
		s.render(w, "lead_row.html", lead)
		return
	}
	_ = s.Store.UpdateLeadEnrichment(id, res.Category, enrichment.IssuesJSON(res.Issues), res.PremiumScore, res.Confidence, res.CostCents, "done")
	if u.WorkspaceID > 0 {
		_ = s.Store.RecordLLMUsage(u.WorkspaceID, u.ID, "enrich", 0, res.CostCents)
	}
	lead, _ = s.Store.GetLead(id)
	s.render(w, "lead_row.html", lead)
}

func (s *Server) leadGenerateEmail(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	lead, err := s.Store.GetLead(id)
	if err != nil || !s.canAccessOwner(u, lead.OwnerID) {
		http.Error(w, "not found", 404)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
	defer cancel()
	d, err := s.Writing.Generate(ctx, lead)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_ = s.Store.SaveLeadDraft(id, d.Subject, d.Body)
	_ = s.Store.RecordLLMUsage(u.WorkspaceID, u.ID, "write", 0, 2)
	camps, _ := s.Store.ListCampaigns(u.IsAdmin(), u.ID)
	var campOpts strings.Builder
	for _, c := range camps {
		fmt.Fprintf(&campOpts, `<option value="%d">%s</option>`, c.ID, template.HTMLEscapeString(c.Name))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div class="panel" id="draft-%d">
  <h3>AI draft saved for %s</h3>
  <p><strong>Subject:</strong> %s</p>
  <pre>%s</pre>
  <form method="post" action="/leads/%d/apply-draft" class="inline" style="margin-top:0.75rem">
    <label>Apply to campaign step 1
      <select name="campaign_id" required><option value="">Select…</option>%s</select>
    </label>
    <button type="submit">Push into sequence</button>
  </form>
</div>`,
		id,
		template.HTMLEscapeString(lead.Name),
		template.HTMLEscapeString(d.Subject),
		template.HTMLEscapeString(d.Body),
		id,
		campOpts.String(),
	)
}

func (s *Server) leadApplyDraft(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	lead, err := s.Store.GetLead(id)
	if err != nil || !s.canAccessOwner(u, lead.OwnerID) {
		http.Error(w, "not found", 404)
		return
	}
	if lead.DraftSubject == "" {
		http.Error(w, "no draft — generate first", 400)
		return
	}
	_ = r.ParseForm()
	campID, _ := strconv.ParseInt(r.FormValue("campaign_id"), 10, 64)
	camp, err := s.Store.GetCampaign(campID)
	if err != nil || !s.canAccessOwner(u, camp.OwnerID) {
		http.Error(w, "campaign not found", 404)
		return
	}
	if err := s.Store.ApplyDraftToCampaignStep(campID, 1, lead.DraftSubject, lead.DraftBody); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := s.Store.EnrollLead(campID, id); err != nil {
		http.Error(w, "draft applied but enroll failed: "+err.Error(), 400)
		return
	}
	s.Store.Audit(u.WorkspaceID, u.ID, "draft.apply", "lead", strconv.FormatInt(id, 10), fmt.Sprintf("campaign=%d", campID))
	http.Redirect(w, r, "/campaigns", http.StatusSeeOther)
}

func (s *Server) leadsSeedPlaybooks(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	purged, templates, campaigns, err := s.Store.SeedCompanyPlaybooks(u.ID, u.WorkspaceID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_ = s.Store.SetSetting("dummy_leads_purged", "1")
	s.Store.Audit(u.WorkspaceID, u.ID, "playbooks.seed", "workspace", strconv.FormatInt(u.WorkspaceID, 10),
		store.SeedSummary(purged, templates, campaigns))
	http.Redirect(w, r, fmt.Sprintf("/campaigns?seeded=1&leads=%d&templates=%d&campaigns=%d", purged, templates, campaigns), http.StatusSeeOther)
}

func (s *Server) leadsSeed(w http.ResponseWriter, r *http.Request) {
	// Legacy route: purge dummy leads instead of inserting demos.
	u := s.current(r)
	n, _ := s.Store.PurgeDummyLeads()
	_ = s.Store.SetSetting("dummy_leads_purged", "1")
	s.Store.Audit(u.WorkspaceID, u.ID, "lead.purge_dummy", "lead", "", fmt.Sprintf("n=%d", n))
	http.Redirect(w, r, fmt.Sprintf("/leads?purged=%d", n), http.StatusSeeOther)
}

func (s *Server) leadsEnrichAll(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	ids, err := s.Store.ListPendingEnrichIDs(u.IsAdmin(), u.ID, 25)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	ok := 0
	for _, id := range ids {
		lead, err := s.Store.GetLead(id)
		if err != nil || !s.canAccessOwner(u, lead.OwnerID) {
			continue
		}
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		res, err := s.Enrichment.Enrich(ctx, lead)
		cancel()
		if err != nil {
			_ = s.Store.SetLeadEnrichmentStatus(id, "error")
			continue
		}
		_ = s.Store.UpdateLeadEnrichment(id, res.Category, enrichment.IssuesJSON(res.Issues), res.PremiumScore, res.Confidence, res.CostCents, "done")
		_ = s.Store.RecordLLMUsage(u.WorkspaceID, u.ID, "enrich", 0, res.CostCents)
		ok++
	}
	s.Store.Audit(u.WorkspaceID, u.ID, "lead.enrich_bulk", "lead", "", fmt.Sprintf("ok=%d", ok))
	http.Redirect(w, r, "/leads", http.StatusSeeOther)
}

func (s *Server) queueGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	msgs, err := s.Store.ListQueue(u.IsAdmin(), u.ID, 100)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "queue.html", map[string]any{"Nav": "queue", "User": u, "Messages": msgs})
}

func (s *Server) campaignsGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	camps, err := s.Store.ListCampaigns(u.IsAdmin(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	leads, _ := s.Store.ListLeads(u.IsAdmin(), u.ID)
	type campView struct {
		models.Campaign
		Steps []models.SequenceStep
	}
	var views []campView
	for _, c := range camps {
		steps, _ := s.Store.ListSteps(c.ID)
		views = append(views, campView{Campaign: c, Steps: steps})
	}
	seeded := r.URL.Query().Get("seeded") == "1"
	seedLeads, _ := strconv.Atoi(r.URL.Query().Get("leads"))
	seedTemplates, _ := strconv.Atoi(r.URL.Query().Get("templates"))
	seedCampaigns, _ := strconv.Atoi(r.URL.Query().Get("campaigns"))
	s.render(w, "campaigns.html", map[string]any{
		"Campaigns": views, "Leads": leads, "Nav": "campaigns", "User": u,
		"Seeded": seeded, "SeedLeads": seedLeads, "SeedTemplates": seedTemplates, "SeedCampaigns": seedCampaigns,
		"CampaignCount": len(views),
	})
}

func (s *Server) campaignsPost(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_ = r.ParseForm()
	limit, _ := strconv.Atoi(r.FormValue("daily_send_limit"))
	if limit <= 0 {
		limit = 20
	}
	start, _ := strconv.Atoi(r.FormValue("send_window_start"))
	end, _ := strconv.Atoi(r.FormValue("send_window_end"))
	if end == 0 && start == 0 {
		start, end = 9, 18
	}
	tz := strings.TrimSpace(r.FormValue("timezone"))
	if tz == "" {
		tz = "Asia/Kolkata"
	}
	id, err := s.Store.CreateCampaign(models.Campaign{
		OwnerID:         u.ID,
		WorkspaceID:     u.WorkspaceID,
		Name:            strings.TrimSpace(r.FormValue("name")),
		Status:          "draft",
		DailySendLimit:  limit,
		Timezone:        tz,
		SendWindowStart: start,
		SendWindowEnd:   end,
		ABEnabled:       r.FormValue("ab_enabled") == "1",
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defaults := []models.SequenceStep{
		{CampaignID: id, StepOrder: 1, DelayDays: 0, SubjectTemplate: "{Quick thought|Idea} for {{name}}", BodySpintax: "Hi {{name}},\n\n{I noticed|Came across} an opportunity on your site.\n\n{Open to a quick audit?|Want a short teardown?}"},
		{CampaignID: id, StepOrder: 2, DelayDays: 2, SubjectTemplate: "Re: {following up|circling back}", BodySpintax: "Hi {{name}},\n\n{Just bumping this|Following up} in case it got buried.\n\n{Still open to a quick look?|Worth a 2-min audit?}"},
		{CampaignID: id, StepOrder: 3, DelayDays: 4, SubjectTemplate: "{Closing the loop|Last note}", BodySpintax: "Hi {{name}},\n\n{I'll close the loop here|Last note from me}.\n\n{Happy to share the audit if useful.|Reply anytime if timing improves.}"},
	}
	for _, st := range defaults {
		_, _ = s.Store.AddStep(st)
	}
	if camp, err := s.Store.GetCampaign(id); err == nil {
		s.indexDocs(search.DocFromCampaign(camp))
	}
	http.Redirect(w, r, "/campaigns", http.StatusSeeOther)
}

func (s *Server) campaignAddStep(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	camp, err := s.Store.GetCampaign(id)
	if err != nil || !s.canAccessOwner(u, camp.OwnerID) {
		http.Error(w, "not found", 404)
		return
	}
	_ = r.ParseForm()
	order, _ := s.Store.NextStepOrder(id)
	delay, _ := strconv.Atoi(r.FormValue("delay_days"))
	_, err = s.Store.AddStep(models.SequenceStep{
		CampaignID: id, StepOrder: order, DelayDays: delay,
		SubjectTemplate: r.FormValue("subject_template"),
		BodySpintax:     r.FormValue("body_spintax"),
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/campaigns", http.StatusSeeOther)
}

func (s *Server) campaignStatus(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	camp, err := s.Store.GetCampaign(id)
	if err != nil || !s.canAccessOwner(u, camp.OwnerID) {
		http.Error(w, "not found", 404)
		return
	}
	_ = r.ParseForm()
	status := r.FormValue("status")
	if status == "" {
		status = "active"
	}
	_ = s.Store.SetCampaignStatus(id, status)
	http.Redirect(w, r, "/campaigns", http.StatusSeeOther)
}

func (s *Server) campaignEnroll(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	camp, err := s.Store.GetCampaign(id)
	if err != nil || !s.canAccessOwner(u, camp.OwnerID) {
		http.Error(w, "not found", 404)
		return
	}
	_ = r.ParseForm()
	leadID, _ := strconv.ParseInt(r.FormValue("lead_id"), 10, 64)
	lead, err := s.Store.GetLead(leadID)
	if err != nil || !s.canAccessOwner(u, lead.OwnerID) {
		http.Error(w, "lead not found", 400)
		return
	}
	if err := s.Store.EnrollLead(id, leadID); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/campaigns", http.StatusSeeOther)
}

func (s *Server) accountsGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	accounts, err := s.Store.ListAccounts(u.IsAdmin(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "accounts.html", map[string]any{
		"Accounts": accounts, "Nav": "accounts", "User": u,
		"GoogleEnabled": s.OAuth != nil && s.OAuth.Google != nil,
		"MSEnabled":     s.OAuth != nil && s.OAuth.Microsoft != nil,
	})
}

func (s *Server) accountsPost(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_ = r.ParseForm()
	port, _ := strconv.Atoi(r.FormValue("smtp_port"))
	if port == 0 {
		port = 587
	}
	imapPort, _ := strconv.Atoi(r.FormValue("imap_port"))
	if imapPort == 0 {
		imapPort = 993
	}
	quota, _ := strconv.Atoi(r.FormValue("daily_quota"))
	if quota <= 0 {
		quota = 40
	}
	passEnc, err := s.Box.Encrypt(r.FormValue("password"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	espEnc, _ := s.Box.Encrypt(r.FormValue("esp_api_key"))
	provider := r.FormValue("provider")
	if provider == "" {
		provider = models.ProviderSMTP
	}
	domain := strings.TrimSpace(r.FormValue("domain"))
	if domain == "" {
		parts := strings.Split(strings.TrimSpace(r.FormValue("email")), "@")
		if len(parts) == 2 {
			domain = parts[1]
		}
	}
	domainLimit, _ := strconv.Atoi(r.FormValue("domain_daily_limit"))
	_, err = s.Store.CreateAccount(models.EmailAccount{
		OwnerID:          u.ID,
		WorkspaceID:      u.WorkspaceID,
		Email:            strings.TrimSpace(r.FormValue("email")),
		Provider:         provider,
		SMTPHost:         strings.TrimSpace(r.FormValue("smtp_host")),
		SMTPPort:         port,
		Username:         strings.TrimSpace(r.FormValue("username")),
		PasswordEnc:      passEnc,
		IMAPHost:         strings.TrimSpace(r.FormValue("imap_host")),
		IMAPPort:         imapPort,
		DailyQuota:       quota,
		Domain:           domain,
		DomainDailyLimit: domainLimit,
		WarmupEnabled:    r.FormValue("warmup") == "1",
		ESPAPIKeyEnc:     espEnc,
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.Store.Audit(u.WorkspaceID, u.ID, "account.create", "account", "", provider)
	http.Redirect(w, r, "/accounts", http.StatusSeeOther)
}

func (s *Server) oauthStartGoogle(w http.ResponseWriter, r *http.Request) {
	s.oauthStart(w, r, models.ProviderGoogle)
}
func (s *Server) oauthStartMicrosoft(w http.ResponseWriter, r *http.Request) {
	s.oauthStart(w, r, models.ProviderMicrosoft)
}

func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request, provider string) {
	u := s.current(r)
	cfg, err := s.OAuth.ConfigFor(provider)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	state, err := oauth.RandomState()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	exp := time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339)
	if err := s.Store.SaveOAuthState(state, u.ID, provider, exp); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	url := cfg.AuthCodeURL(state)
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func (s *Server) oauthCallbackGoogle(w http.ResponseWriter, r *http.Request) {
	s.oauthCallback(w, r, models.ProviderGoogle)
}
func (s *Server) oauthCallbackMicrosoft(w http.ResponseWriter, r *http.Request) {
	s.oauthCallback(w, r, models.ProviderMicrosoft)
}

func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request, provider string) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	userID, prov, err := s.Store.ConsumeOAuthState(state)
	if err != nil || prov != provider {
		http.Error(w, "invalid oauth state", 400)
		return
	}
	cfg, err := s.OAuth.ConfigFor(provider)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tok, err := oauth.Exchange(ctx, cfg, code)
	if err != nil {
		http.Error(w, "token exchange failed: "+err.Error(), 400)
		return
	}
	email := r.URL.Query().Get("email")
	if email == "" {
		email = "oauth-" + provider + "-" + strconv.FormatInt(userID, 10) + "@connected.local"
	}
	// Prefer email from userinfo if available via id token / extra — keep placeholder replaced by username field
	if tok.Extra("email") != nil {
		if e, ok := tok.Extra("email").(string); ok && e != "" {
			email = e
		}
	}
	smtpHost, smtpPort, imapHost, imapPort := oauth.DefaultsForProvider(provider)
	accessEnc, _ := s.Box.Encrypt(tok.AccessToken)
	refreshEnc, _ := s.Box.Encrypt(tok.RefreshToken)
	_, err = s.Store.CreateAccount(models.EmailAccount{
		OwnerID:         userID,
		Email:           email,
		Provider:        provider,
		SMTPHost:        smtpHost,
		SMTPPort:        smtpPort,
		Username:        email,
		AccessTokenEnc:  accessEnc,
		RefreshTokenEnc: refreshEnc,
		TokenExpiry:     oauth.TokenExpiry(tok),
		IMAPHost:        imapHost,
		IMAPPort:        imapPort,
		DailyQuota:      40,
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/accounts", http.StatusSeeOther)
}

func (s *Server) inboxGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	replies, err := s.Store.ListReplies(u.IsAdmin(), u.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "inbox.html", map[string]any{"Replies": replies, "Nav": "inbox", "User": u})
}

func (s *Server) inboxClassify(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_ = r.ParseForm()
	body := r.FormValue("body")
	ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
	defer cancel()
	intent, err := s.Inbox.Classify(ctx, body)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	from := r.FormValue("from_email")
	if from != "" {
		wsID := s.Store.WorkspaceIDForEmail(from)
		if intent == "unsubscribe" {
			_ = s.Store.AddSuppressionWS(wsID, from, "unsubscribe")
			_ = s.Store.RecordRecipientEvent(wsID, from, "unsubscribe")
		}
		if intent == "positive" || intent == "neutral" {
			_ = s.Store.RecordRecipientEvent(wsID, from, "replied")
		}
		s.Store.MarkOutboundReplied(from)
	}
	oid := u.ID
	ws := u.WorkspaceID
	id, err := s.Store.CreateReply(models.InboundReply{
		OwnerID:     &oid,
		WorkspaceID: &ws,
		LeadName:    r.FormValue("lead_name"),
		FromEmail:   from,
		Subject:     r.FormValue("subject"),
		Body:        body,
		Intent:      intent,
		ThreadID:    r.FormValue("thread_id"),
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	replies, _ := s.Store.ListReplies(u.IsAdmin(), u.ID)
	for _, rp := range replies {
		if rp.ID == id {
			s.render(w, "reply_row.html", rp)
			return
		}
	}
}

func (s *Server) usersGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	users, err := s.Store.ListUsers()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "users.html", map[string]any{"Users": users, "Nav": "users", "User": u})
}

func (s *Server) usersPost(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	role := r.FormValue("role")
	if role != models.RoleAdmin && role != models.RoleSender {
		role = models.RoleSender
	}
	_, err := s.Store.CreateUser(strings.TrimSpace(r.FormValue("email")), r.FormValue("password"), role)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}

func (s *Server) usersActive(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	_ = r.ParseForm()
	active := r.FormValue("active") == "1"
	_ = s.Store.SetUserActive(id, active)
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}

func (s *Server) unsubscribeGet(w http.ResponseWriter, r *http.Request) {
	s.render(w, "unsubscribe.html", map[string]any{"Token": r.PathValue("token"), "Done": false})
}

func (s *Server) unsubscribePost(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	leadID, campID, ok := s.Auth.VerifyUnsubscribe(token)
	if !ok {
		http.Error(w, "invalid token", 400)
		return
	}
	lead, err := s.Store.GetLead(leadID)
	if err == nil && lead.Email != "" {
		ws := lead.WorkspaceID
		if ws == 0 {
			ws = 1
		}
		_ = s.Store.AddSuppressionWS(ws, lead.Email, "unsubscribe")
		_ = s.Store.RecordRecipientEvent(ws, lead.Email, "unsubscribe")
	}
	_ = s.Store.UnsubscribeCampaignLead(campID, leadID)
	s.render(w, "unsubscribe.html", map[string]any{"Token": token, "Done": true})
}

// trackGet records open/click engagement for deliverability scoring.
func (s *Server) trackGet(w http.ResponseWriter, r *http.Request) {
	kind, leadID, _, dest, ok := s.Auth.VerifyTrack(r.PathValue("token"))
	if !ok {
		http.Error(w, "invalid token", 400)
		return
	}
	lead, err := s.Store.GetLead(leadID)
	email := ""
	ws := int64(1)
	if err == nil {
		email = lead.Email
		if lead.WorkspaceID > 0 {
			ws = lead.WorkspaceID
		}
	}
	if email != "" {
		switch kind {
		case "o":
			_ = s.Store.RecordRecipientEvent(ws, email, "opened")
		case "c":
			_ = s.Store.RecordRecipientEvent(ws, email, "opened")
			_ = s.Store.RecordRecipientEvent(ws, email, "clicked")
		}
	}
	if kind == "c" && dest != "" && (strings.HasPrefix(dest, "http://") || strings.HasPrefix(dest, "https://")) {
		http.Redirect(w, r, dest, http.StatusFound)
		return
	}
	// 1×1 transparent GIF
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte{
		0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00, 0x80, 0x00, 0x00, 0xff, 0xff, 0xff,
		0x00, 0x00, 0x00, 0x21, 0xf9, 0x04, 0x01, 0x00, 0x00, 0x00, 0x00, 0x2c, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x01, 0x00, 0x00, 0x02, 0x02, 0x44, 0x01, 0x00, 0x3b,
	})
}
