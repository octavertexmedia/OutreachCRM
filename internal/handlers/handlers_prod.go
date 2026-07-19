package handlers

import (
	"context"
	"encoding/csv"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/manishkumar/outreachcrm/internal/auth"
	"github.com/manishkumar/outreachcrm/internal/dnscheck"
	"github.com/manishkumar/outreachcrm/internal/models"
	"github.com/manishkumar/outreachcrm/internal/search"
	"github.com/manishkumar/outreachcrm/internal/store"
	"github.com/manishkumar/outreachcrm/internal/totp"
)

func (s *Server) registerProdRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /login/totp", s.totpGet)
	mux.HandleFunc("POST /login/totp", s.totpPost)
	mux.HandleFunc("GET /security", s.securityGet)
	mux.HandleFunc("POST /security/totp/enable", s.totpEnable)
	mux.HandleFunc("POST /security/totp/disable", s.totpDisable)

	mux.HandleFunc("GET /analytics", s.analyticsGet)
	mux.HandleFunc("GET /audit", auth.RequireAdmin(s.auditGet))
	mux.HandleFunc("GET /workspaces", auth.RequireAdmin(s.workspacesGet))
	mux.HandleFunc("POST /workspaces", auth.RequireAdmin(s.workspacesPost))
	mux.HandleFunc("POST /workspaces/switch", auth.RequireAdmin(s.workspaceSwitch))
	mux.HandleFunc("POST /workspaces/seed-brands", auth.RequireAdmin(s.workspacesSeedBrands))
	mux.HandleFunc("POST /workspaces/{id}/seed", auth.RequireAdmin(s.workspaceSeedPack))
	mux.HandleFunc("GET /templates", s.templatesGet)
	mux.HandleFunc("POST /templates", s.templatesPost)
	mux.HandleFunc("GET /domains", s.domainsGet)
	mux.HandleFunc("POST /domains/check", s.domainsCheck)
	mux.HandleFunc("GET /deliverability", s.deliverabilityGet)
	mux.HandleFunc("POST /deliverability/validate", s.deliverabilityValidate)
	mux.HandleFunc("POST /deliverability/blacklist", s.deliverabilityBlacklist)
	mux.HandleFunc("POST /campaigns/{id}/deliverability", s.campaignDeliverability)
	mux.HandleFunc("POST /leads/{id}/validate-email", s.leadValidateEmail)
	mux.HandleFunc("POST /leads/import", s.leadsImport)
	mux.HandleFunc("GET /privacy/export", s.privacyExport)
	mux.HandleFunc("POST /privacy/delete/{id}", s.privacyDeleteLead)
	mux.HandleFunc("GET /hitl", s.hitlGet)
	mux.HandleFunc("POST /hitl/{id}", s.hitlPost)
	mux.HandleFunc("POST /hitl/{id}/suggest", s.hitlSuggest)

	mux.HandleFunc("POST /webhooks/postmark", s.webhookPostmark)
	mux.HandleFunc("POST /webhooks/ses", s.webhookSES)
}

func (s *Server) totpGet(w http.ResponseWriter, r *http.Request) {
	s.render(w, "totp.html", map[string]any{"Error": ""})
}

func (s *Server) totpPost(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("orc_pending")
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	uid, _ := strconv.ParseInt(c.Value, 10, 64)
	u, err := s.Store.GetUser(uid)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	_ = r.ParseForm()
	secret, err := s.Box.Decrypt(u.TOTPSecretEnc)
	if err != nil || !totp.Verify(secret, r.FormValue("code"), 1) {
		s.render(w, "totp.html", map[string]any{"Error": "Invalid code"})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "orc_pending", Value: "", Path: "/", MaxAge: -1})
	s.Auth.SetSession(w, models.SessionUser{ID: u.ID, Email: u.Email, Role: u.Role, WorkspaceID: u.WorkspaceID})
	s.Store.Audit(u.WorkspaceID, u.ID, "login.totp", "user", strconv.FormatInt(u.ID, 10), "")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) securityGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	full, _ := s.Store.GetUser(u.ID)
	s.render(w, "security.html", map[string]any{"Nav": "security", "User": u, "TOTPEnabled": full.TOTPEnabled, "URI": "", "Secret": ""})
}

func (s *Server) totpEnable(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_ = r.ParseForm()
	if secret := r.FormValue("confirm_secret"); secret != "" {
		if !totp.Verify(secret, r.FormValue("code"), 1) {
			http.Error(w, "invalid code", 400)
			return
		}
		enc, err := s.Box.Encrypt(secret)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_ = s.Store.SetUserTOTP(u.ID, enc, true)
		s.Store.Audit(u.WorkspaceID, u.ID, "totp.enable", "user", strconv.FormatInt(u.ID, 10), "")
		http.Redirect(w, r, "/security", http.StatusSeeOther)
		return
	}
	secret, err := totp.GenerateSecret()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	uri := totp.ProvisioningURI(secret, u.Email, "OutReachCRM")
	s.render(w, "security.html", map[string]any{
		"Nav": "security", "User": u, "TOTPEnabled": false, "URI": uri, "Secret": secret,
	})
}

func (s *Server) totpDisable(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_ = s.Store.SetUserTOTP(u.ID, "", false)
	s.Store.Audit(u.WorkspaceID, u.ID, "totp.disable", "user", strconv.FormatInt(u.ID, 10), "")
	http.Redirect(w, r, "/security", http.StatusSeeOther)
}

func (s *Server) analyticsGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	a, err := s.Store.Analytics(u.WorkspaceID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "analytics.html", map[string]any{"Nav": "analytics", "User": u, "A": a})
}

func (s *Server) auditGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	entries, err := s.Store.ListAudit(u.WorkspaceID, 200)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "audit.html", map[string]any{"Nav": "audit", "User": u, "Entries": entries})
}

func (s *Server) workspacesGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_, _, _ = s.Store.EnsureBrandWorkspaces()
	list, _ := s.Store.ListWorkspaces()
	type wsView struct {
		models.Workspace
		Leads      int
		Campaigns  int
		Users      int
		Templates  int
		Audiences  int
		Active     bool
	}
	var views []wsView
	for _, ws := range list {
		l, c, users, t, a := s.Store.WorkspaceStats(ws.ID)
		views = append(views, wsView{
			Workspace: ws, Leads: l, Campaigns: c, Users: users, Templates: t, Audiences: a,
			Active: ws.ID == u.WorkspaceID,
		})
	}
	s.render(w, "workspaces.html", map[string]any{
		"Nav": "workspaces", "User": u, "Workspaces": views,
		"Created":   r.URL.Query().Get("created") == "1",
		"Switched":  r.URL.Query().Get("switched") == "1",
		"Seeded":    r.URL.Query().Get("seeded") == "1",
		"SeedTpl":   r.URL.Query().Get("templates"),
		"SeedCamp":  r.URL.Query().Get("campaigns"),
		"SeedLeads": r.URL.Query().Get("leads"),
	})
}

func (s *Server) workspacesPost(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_ = r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	pack := strings.TrimSpace(r.FormValue("playbook"))
	if pack == "" {
		pack = store.PlaybookNone
	}
	id, templates, campaigns, err := s.Store.OnboardWorkspace(name, pack, u.ID)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.Store.Audit(u.WorkspaceID, u.ID, "workspace.onboard", "workspace", strconv.FormatInt(id, 10),
		fmt.Sprintf("pack=%s templates=%d campaigns=%d", pack, templates, campaigns))
	// Switch admin session into the new tenant so they can start working immediately.
	u.WorkspaceID = id
	s.Auth.SetSession(w, u)
	http.Redirect(w, r, fmt.Sprintf("/workspaces?created=1&templates=%d&campaigns=%d", templates, campaigns), http.StatusSeeOther)
}

func (s *Server) workspaceSwitch(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_ = r.ParseForm()
	id, _ := strconv.ParseInt(r.FormValue("workspace_id"), 10, 64)
	if _, err := s.Store.GetWorkspace(id); err != nil {
		http.Error(w, "workspace not found", 404)
		return
	}
	u.WorkspaceID = id
	s.Auth.SetSession(w, u)
	s.Store.Audit(id, u.ID, "workspace.switch", "workspace", strconv.FormatInt(id, 10), "")
	next := strings.TrimSpace(r.FormValue("next"))
	if next == "" || !strings.HasPrefix(next, "/") {
		next = "/workspaces?switched=1"
	} else if !strings.Contains(next, "?") {
		next += "?switched=1"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) workspacesSeedBrands(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	ovmT, ovmC, revT, revC, purged, err := s.Store.SeedBrandPlaybooks(u.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_ = s.Store.SetSetting("dummy_leads_purged", "1")
	s.Store.Audit(u.WorkspaceID, u.ID, "playbooks.seed_brands", "workspace", "brand",
		fmt.Sprintf("ovm_t=%d ovm_c=%d rev_t=%d rev_c=%d purged=%d", ovmT, ovmC, revT, revC, purged))
	http.Redirect(w, r, fmt.Sprintf("/workspaces?seeded=1&templates=%d&campaigns=%d&leads=%d",
		ovmT+revT, ovmC+revC, purged), http.StatusSeeOther)
}

func (s *Server) workspaceSeedPack(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if _, err := s.Store.GetWorkspace(id); err != nil {
		http.Error(w, "not found", 404)
		return
	}
	_ = r.ParseForm()
	pack := strings.TrimSpace(r.FormValue("playbook"))
	if pack == "" {
		pack = store.PlaybookAll
	}
	purged, templates, campaigns, err := s.Store.SeedCompanyPlaybooksFor(u.ID, id, pack)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.Store.Audit(id, u.ID, "playbooks.seed", "workspace", strconv.FormatInt(id, 10),
		fmt.Sprintf("pack=%s %s", pack, store.SeedSummary(purged, templates, campaigns)))
	http.Redirect(w, r, fmt.Sprintf("/workspaces?seeded=1&templates=%d&campaigns=%d&leads=%d", templates, campaigns, purged), http.StatusSeeOther)
}

func (s *Server) templatesGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	list, _ := s.Store.ListTemplates(u.WorkspaceID)
	s.render(w, "templates.html", map[string]any{"Nav": "templates", "User": u, "Templates": list})
}

func (s *Server) templatesPost(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_ = r.ParseForm()
	tpl := models.EmailTemplate{
		WorkspaceID: u.WorkspaceID,
		Name:        r.FormValue("name"),
		Subject:     r.FormValue("subject"),
		Body:        r.FormValue("body"),
	}
	id, err := s.Store.CreateTemplate(tpl)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	tpl.ID = id
	s.indexDocs(search.DocFromTemplate(tpl))
	http.Redirect(w, r, "/templates", http.StatusSeeOther)
}

func (s *Server) domainsGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	list, _ := s.Store.ListDomainChecks()
	s.render(w, "domains.html", map[string]any{"Nav": "domains", "User": u, "Checks": list})
}

func (s *Server) domainsCheck(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_ = r.ParseForm()
	dc := dnscheck.Check(r.FormValue("domain"))
	_ = s.Store.SaveDomainCheck(dc)
	s.Store.Audit(u.WorkspaceID, u.ID, "domain.check", "domain", dc.Domain, dc.Detail)
	http.Redirect(w, r, "/domains", http.StatusSeeOther)
}

func (s *Server) leadsImport(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "csv file required", 400)
		return
	}
	defer file.Close()
	rd := csv.NewReader(file)
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true

	// Header-aware: name,email,company,title,website,phone,source,notes
	// Legacy positional: name,email,website,phone
	var headers []string
	n := 0
	for {
		rec, err := rd.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) < 2 {
			continue
		}
		if headers == nil {
			if looksLikeCSVHeader(rec) {
				headers = normalizeCSVHeaders(rec)
				continue
			}
			headers = []string{"name", "email", "website", "phone"}
		}
		row := mapCSVRow(headers, rec)
		name := strings.TrimSpace(row["name"])
		email := strings.TrimSpace(row["email"])
		if name == "" || email == "" {
			continue
		}
		source := strings.TrimSpace(row["source"])
		if source == "" {
			source = "csv"
		}
		_, createErr := s.Store.CreateLead(models.Lead{
			OwnerID: u.ID, WorkspaceID: u.WorkspaceID,
			Name: name, Email: email,
			Company: strings.TrimSpace(row["company"]),
			Title:   strings.TrimSpace(row["title"]),
			Website: strings.TrimSpace(row["website"]),
			Phone:   strings.TrimSpace(row["phone"]),
			Source:  source,
			Notes:   strings.TrimSpace(row["notes"]),
			EnrichmentStatus: "pending",
		})
		if createErr == nil {
			src := strings.ToLower(source)
			if strings.Contains(src, "purchas") || strings.Contains(src, "bought") || strings.Contains(src, "scrape") {
				s.Store.MarkPurchasedList(u.WorkspaceID, email)
			}
			n++
		}
	}
	s.Store.Audit(u.WorkspaceID, u.ID, "lead.import", "lead", "", fmt.Sprintf("count=%d", n))
	http.Redirect(w, r, fmt.Sprintf("/leads?imported=1&n=%d", n), http.StatusSeeOther)
}

func looksLikeCSVHeader(rec []string) bool {
	if len(rec) == 0 {
		return false
	}
	first := strings.ToLower(strings.TrimSpace(rec[0]))
	if first == "name" || first == "full_name" || first == "contact" {
		return true
	}
	for _, c := range rec {
		switch strings.ToLower(strings.TrimSpace(c)) {
		case "email", "company", "title", "website", "source", "notes":
			return true
		}
	}
	return false
}

func normalizeCSVHeaders(rec []string) []string {
	out := make([]string, len(rec))
	for i, h := range rec {
		h = strings.ToLower(strings.TrimSpace(h))
		switch h {
		case "full_name", "contact", "contact_name":
			h = "name"
		case "e-mail", "email_address":
			h = "email"
		case "web", "url", "site":
			h = "website"
		case "job_title", "role":
			h = "title"
		case "organisation", "organization", "org":
			h = "company"
		case "mobile", "tel":
			h = "phone"
		case "note", "trigger", "comments":
			h = "notes"
		case "tag", "list", "segment":
			h = "source"
		}
		out[i] = h
	}
	return out
}

func mapCSVRow(headers, rec []string) map[string]string {
	m := make(map[string]string, len(headers))
	for i, h := range headers {
		if i >= len(rec) || h == "" {
			continue
		}
		m[h] = rec[i]
	}
	// Positional fallback when headerless legacy file used default headers
	if _, ok := m["name"]; !ok && len(rec) > 0 {
		m["name"] = rec[0]
	}
	if _, ok := m["email"]; !ok && len(rec) > 1 {
		m["email"] = rec[1]
	}
	return m
}

func (s *Server) privacyExport(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	raw, err := s.Store.ExportLeadJSON(u.WorkspaceID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.Store.Audit(u.WorkspaceID, u.ID, "privacy.export", "workspace", strconv.FormatInt(u.WorkspaceID, 10), "")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=leads-export.json")
	_, _ = w.Write([]byte(raw))
}

func (s *Server) privacyDeleteLead(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	lead, err := s.Store.GetLead(id)
	if err != nil || !s.canAccessOwner(u, lead.OwnerID) {
		http.Error(w, "not found", 404)
		return
	}
	_ = s.Store.DeleteLeadPII(id)
	s.Store.Audit(u.WorkspaceID, u.ID, "privacy.delete", "lead", strconv.FormatInt(id, 10), "")
	http.Redirect(w, r, "/leads", http.StatusSeeOther)
}

func (s *Server) hitlGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	list, _ := s.Store.ListHITL(u.WorkspaceID)
	s.render(w, "hitl.html", map[string]any{"Nav": "hitl", "User": u, "Replies": list})
}

func (s *Server) hitlPost(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	_ = r.ParseForm()
	status := r.FormValue("status")
	if status == "" {
		status = models.HITLDone
	}
	_ = s.Store.SetReplyHITL(id, status)
	s.Store.Audit(u.WorkspaceID, u.ID, "hitl.update", "reply", strconv.FormatInt(id, 10), status)
	http.Redirect(w, r, "/hitl", http.StatusSeeOther)
}

func (s *Server) hitlSuggest(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	replies, _ := s.Store.ListHITL(u.WorkspaceID)
	var found models.InboundReply
	for _, rp := range replies {
		if rp.ID == id {
			found = rp
			break
		}
	}
	if found.ID == 0 {
		http.Error(w, "not found", 404)
		return
	}
	var lead models.Lead
	if found.LeadID != nil {
		lead, _ = s.Store.GetLead(*found.LeadID)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	text, err := s.Writing.SuggestReply(ctx, lead, found.Body)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div class="panel"><strong>Suggested reply</strong><pre style="white-space:pre-wrap;font:inherit">%s</pre></div>`,
		template.HTMLEscapeString(text))
}

