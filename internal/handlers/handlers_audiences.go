package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/manishkumar/outreachcrm/internal/models"
)

func (s *Server) registerAudienceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /audiences", s.audiencesGet)
	mux.HandleFunc("POST /audiences", s.audiencesPost)
	mux.HandleFunc("GET /audiences/{id}", s.audienceDetailGet)
	mux.HandleFunc("POST /audiences/{id}", s.audienceUpdate)
	mux.HandleFunc("POST /audiences/{id}/refresh", s.audienceRefresh)
	mux.HandleFunc("POST /audiences/{id}/delete", s.audienceDelete)
	mux.HandleFunc("POST /audiences/{id}/enroll", s.audienceEnroll)
	mux.HandleFunc("GET /api/audiences/preview", s.audiencePreview)
	mux.HandleFunc("GET /funnels", s.funnelsGet)
	mux.HandleFunc("GET /funnels/{id}", s.funnelDetailGet)
}

func splitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseLeadFilter(r *http.Request) models.LeadFilter {
	_ = r.ParseForm()
	f := models.LeadFilter{
		Categories:         splitCSV(r.FormValue("categories")),
		Sources:            splitCSV(r.FormValue("sources")),
		EnrichmentStatuses: splitCSV(r.FormValue("enrichment_statuses")),
		CompanyContains:    strings.TrimSpace(r.FormValue("company_contains")),
		Q:                  strings.TrimSpace(r.FormValue("q")),
		HasEmail:           r.FormValue("has_email") == "1" || r.FormValue("has_email") == "on",
		ExcludeSuppressed:  r.FormValue("exclude_suppressed") != "0",
	}
	if v := r.FormValue("min_premium"); v != "" {
		f.MinPremium, _ = strconv.Atoi(v)
	}
	if v := r.FormValue("exclude_enrolled_in"); v != "" {
		f.ExcludeEnrolledIn, _ = strconv.ParseInt(v, 10, 64)
	}
	// Multi-select from query: category=a&category=b
	if cats := r.Form["category"]; len(cats) > 0 {
		f.Categories = append(f.Categories, cats...)
	}
	if srcs := r.Form["source"]; len(srcs) > 0 {
		f.Sources = append(f.Sources, srcs...)
	}
	if sts := r.Form["enrichment_status"]; len(sts) > 0 {
		f.EnrichmentStatuses = append(f.EnrichmentStatuses, sts...)
	}
	return f
}

func parseLeadFilterQuery(q url.Values) models.LeadFilter {
	f := models.LeadFilter{
		Categories:         splitCSV(q.Get("categories")),
		Sources:            splitCSV(q.Get("sources")),
		EnrichmentStatuses: splitCSV(q.Get("enrichment_statuses")),
		CompanyContains:    strings.TrimSpace(q.Get("company_contains")),
		Q:                  strings.TrimSpace(q.Get("q")),
		HasEmail:           q.Get("has_email") == "1",
		ExcludeSuppressed:  q.Get("exclude_suppressed") != "0",
	}
	if v := q.Get("min_premium"); v != "" {
		f.MinPremium, _ = strconv.Atoi(v)
	}
	if v := q.Get("exclude_enrolled_in"); v != "" {
		f.ExcludeEnrolledIn, _ = strconv.ParseInt(v, 10, 64)
	}
	if cats := q["category"]; len(cats) > 0 {
		f.Categories = append(f.Categories, cats...)
	}
	if srcs := q["source"]; len(srcs) > 0 {
		f.Sources = append(f.Sources, srcs...)
	}
	if sts := q["enrichment_status"]; len(sts) > 0 {
		f.EnrichmentStatuses = append(f.EnrichmentStatuses, sts...)
	}
	return f
}

func filterActive(f models.LeadFilter) bool {
	return len(f.Categories) > 0 || len(f.Sources) > 0 || len(f.EnrichmentStatuses) > 0 ||
		f.MinPremium > 0 || f.HasEmail || f.CompanyContains != "" || f.Q != "" || f.ExcludeEnrolledIn > 0
}

func (s *Server) audiencesGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	list, err := s.Store.ListAudiences(u.IsAdmin(), u.ID, u.WorkspaceID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	cats := s.Store.DistinctLeadValues(u.IsAdmin(), u.ID, u.WorkspaceID, "category")
	srcs := s.Store.DistinctLeadValues(u.IsAdmin(), u.ID, u.WorkspaceID, "source")
	sts := s.Store.DistinctLeadValues(u.IsAdmin(), u.ID, u.WorkspaceID, "enrichment_status")
	camps, _ := s.Store.ListCampaigns(u.IsAdmin(), u.ID, u.WorkspaceID)
	s.render(w, "audiences.html", map[string]any{
		"Audiences": list, "Nav": "audiences", "User": u,
		"AudienceCount": len(list),
		"Categories":    cats, "Sources": srcs, "EnrichStatuses": sts,
		"Campaigns": camps,
		"Created":   r.URL.Query().Get("created") == "1",
		"Deleted":   r.URL.Query().Get("deleted") == "1",
		"Enrolled":  r.URL.Query().Get("enrolled") != "",
		"EnrollN":   r.URL.Query().Get("enrolled"),
		"SkipN":     r.URL.Query().Get("skipped"),
	})
}

func (s *Server) audiencesPost(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_ = r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name required", 400)
		return
	}
	f := parseLeadFilter(r)
	id, err := s.Store.CreateAudience(models.Audience{
		WorkspaceID: u.WorkspaceID,
		OwnerID:     u.ID,
		Name:        name,
		Description: strings.TrimSpace(r.FormValue("description")),
		Filter:      f,
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	n, _ := s.Store.RefreshAudienceMembers(id)
	s.Store.Audit(u.WorkspaceID, u.ID, "audience.create", "audience", fmt.Sprintf("%d", id), fmt.Sprintf("members=%d", n))
	http.Redirect(w, r, fmt.Sprintf("/audiences/%d?refreshed=%d", id, n), http.StatusSeeOther)
}

func (s *Server) audienceDetailGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a, err := s.Store.GetAudience(id)
	if err != nil || !s.canAccessOwner(u, a.OwnerID) {
		http.Error(w, "not found", 404)
		return
	}
	members, _ := s.Store.ListAudienceMembers(id, 100)
	liveCount, _ := s.Store.CountLeadsFiltered(u.IsAdmin(), u.ID, u.WorkspaceID, a.Filter)
	cats := s.Store.DistinctLeadValues(u.IsAdmin(), u.ID, u.WorkspaceID, "category")
	srcs := s.Store.DistinctLeadValues(u.IsAdmin(), u.ID, u.WorkspaceID, "source")
	sts := s.Store.DistinctLeadValues(u.IsAdmin(), u.ID, u.WorkspaceID, "enrichment_status")
	camps, _ := s.Store.ListCampaigns(u.IsAdmin(), u.ID, u.WorkspaceID)
	funnelRuns, _ := s.Store.ListAudienceFunnelRuns(id)
	s.render(w, "audience_detail.html", map[string]any{
		"Audience": a, "Members": members, "LiveCount": liveCount,
		"Nav": "audiences", "User": u,
		"Categories": cats, "Sources": srcs, "EnrichStatuses": sts,
		"Campaigns": camps, "FunnelRuns": funnelRuns,
		"Refreshed": r.URL.Query().Get("refreshed"),
		"Updated":   r.URL.Query().Get("updated") == "1",
		"Enrolled":  r.URL.Query().Get("enrolled") != "",
		"EnrollN":   r.URL.Query().Get("enrolled"),
		"SkipN":     r.URL.Query().Get("skipped"),
		"CatCSV":    strings.Join(a.Filter.Categories, ", "),
		"SrcCSV":    strings.Join(a.Filter.Sources, ", "),
		"StCSV":     strings.Join(a.Filter.EnrichmentStatuses, ", "),
	})
}

func (s *Server) audienceUpdate(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a, err := s.Store.GetAudience(id)
	if err != nil || !s.canAccessOwner(u, a.OwnerID) {
		http.Error(w, "not found", 404)
		return
	}
	_ = r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name required", 400)
		return
	}
	f := parseLeadFilter(r)
	a.Name = name
	a.Description = strings.TrimSpace(r.FormValue("description"))
	a.Filter = f
	if err := s.Store.UpdateAudience(a); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	n, _ := s.Store.RefreshAudienceMembers(id)
	s.Store.Audit(u.WorkspaceID, u.ID, "audience.update", "audience", fmt.Sprintf("%d", id), fmt.Sprintf("members=%d", n))
	http.Redirect(w, r, fmt.Sprintf("/audiences/%d?updated=1&refreshed=%d", id, n), http.StatusSeeOther)
}

func (s *Server) audienceRefresh(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a, err := s.Store.GetAudience(id)
	if err != nil || !s.canAccessOwner(u, a.OwnerID) {
		http.Error(w, "not found", 404)
		return
	}
	n, err := s.Store.RefreshAudienceMembers(id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.Store.Audit(u.WorkspaceID, u.ID, "audience.refresh", "audience", fmt.Sprintf("%d", id), fmt.Sprintf("members=%d", n))
	http.Redirect(w, r, fmt.Sprintf("/audiences/%d?refreshed=%d", id, n), http.StatusSeeOther)
}

func (s *Server) audienceDelete(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a, err := s.Store.GetAudience(id)
	if err != nil || !s.canAccessOwner(u, a.OwnerID) {
		http.Error(w, "not found", 404)
		return
	}
	if err := s.Store.DeleteAudience(id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.Store.Audit(u.WorkspaceID, u.ID, "audience.delete", "audience", fmt.Sprintf("%d", id), a.Name)
	http.Redirect(w, r, "/audiences?deleted=1", http.StatusSeeOther)
}

func (s *Server) audienceEnroll(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a, err := s.Store.GetAudience(id)
	if err != nil || !s.canAccessOwner(u, a.OwnerID) {
		http.Error(w, "not found", 404)
		return
	}
	_ = r.ParseForm()
	campID, _ := strconv.ParseInt(r.FormValue("campaign_id"), 10, 64)
	camp, err := s.Store.GetCampaign(campID)
	if err != nil || !s.canAccessOwner(u, camp.OwnerID) {
		http.Error(w, "campaign not found", 400)
		return
	}
	res, err := s.Store.EnrollAudience(campID, id)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.Store.Audit(u.WorkspaceID, u.ID, "audience.enroll", "audience", fmt.Sprintf("%d", id),
		fmt.Sprintf("campaign=%d enrolled=%d skipped=%d run=%d", campID, res.Enrolled, res.Skipped, res.RunID))
	if res.RunID > 0 {
		http.Redirect(w, r, fmt.Sprintf("/funnels/%d?enrolled=%d&skipped=%d", res.RunID, res.Enrolled, res.Skipped), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/audiences/%d?enrolled=%d&skipped=%d", id, res.Enrolled, res.Skipped), http.StatusSeeOther)
}

func (s *Server) campaignEnrollAudience(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	campID, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	camp, err := s.Store.GetCampaign(campID)
	if err != nil || !s.canAccessOwner(u, camp.OwnerID) {
		http.Error(w, "not found", 404)
		return
	}
	_ = r.ParseForm()
	audID, _ := strconv.ParseInt(r.FormValue("audience_id"), 10, 64)
	a, err := s.Store.GetAudience(audID)
	if err != nil || !s.canAccessOwner(u, a.OwnerID) {
		http.Error(w, "audience not found", 400)
		return
	}
	res, err := s.Store.EnrollAudience(campID, audID)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.Store.Audit(u.WorkspaceID, u.ID, "campaign.enroll_audience", "campaign", fmt.Sprintf("%d", campID),
		fmt.Sprintf("audience=%d enrolled=%d skipped=%d run=%d", audID, res.Enrolled, res.Skipped, res.RunID))
	if res.RunID > 0 {
		http.Redirect(w, r, fmt.Sprintf("/funnels/%d?enrolled=%d&skipped=%d", res.RunID, res.Enrolled, res.Skipped), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/campaigns?enrolled=%d&skipped=%d&audience=%d", res.Enrolled, res.Skipped, audID), http.StatusSeeOther)
}

func (s *Server) funnelsGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	runs, err := s.Store.ListCampaignAudienceRuns(u.WorkspaceID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "funnels.html", map[string]any{
		"Nav": "funnels", "User": u, "Runs": runs, "RunCount": len(runs),
	})
}

func (s *Server) funnelDetailGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	run, err := s.Store.GetCampaignAudienceRun(id)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	if !u.IsAdmin() && run.WorkspaceID != u.WorkspaceID && run.WorkspaceID != 0 {
		http.Error(w, "forbidden", 403)
		return
	}
	steps, _ := s.Store.ListSteps(run.CampaignID)
	s.render(w, "funnel_detail.html", map[string]any{
		"Nav": "funnels", "User": u, "Run": run, "Steps": steps,
		"Enrolled": r.URL.Query().Get("enrolled") != "",
		"EnrollN":  r.URL.Query().Get("enrolled"),
		"SkipN":    r.URL.Query().Get("skipped"),
	})
}

func (s *Server) audiencePreview(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	f := parseLeadFilterQuery(r.URL.Query())
	if r.URL.Query().Get("has_email") == "" {
		// live preview from filter bar: honor explicit flags only
	}
	count, err := s.Store.CountLeadsFiltered(u.IsAdmin(), u.ID, u.WorkspaceID, f)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	sample, _ := s.Store.ListLeadsFiltered(u.IsAdmin(), u.ID, u.WorkspaceID, f)
	if len(sample) > 8 {
		sample = sample[:8]
	}
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") || r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		type row struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Email   string `json:"email"`
			Company string `json:"company"`
			Category string `json:"category"`
			Source  string `json:"source"`
		}
		rows := make([]row, 0, len(sample))
		for _, l := range sample {
			rows = append(rows, row{l.ID, l.Name, l.Email, l.Company, l.Category, l.Source})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"count": count, "sample": rows})
		return
	}
	s.render(w, "audience_preview.html", map[string]any{
		"Count": count, "Sample": sample, "Filter": f,
	})
}
