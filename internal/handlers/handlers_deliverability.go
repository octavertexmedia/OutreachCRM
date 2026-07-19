package handlers

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/manishkumar/outreachcrm/internal/deliverability"
)

func (s *Server) deliverabilityGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	dash := s.Store.DeliverabilityDashboard(u.WorkspaceID)
	decisions, _ := s.Store.ListRecentDecisions(u.WorkspaceID, 40)
	bls, _ := s.Store.LatestBlacklistChecks(20)
	s.render(w, "deliverability.html", map[string]any{
		"Nav": "deliverability", "User": u, "Dash": dash, "Decisions": decisions, "Blacklists": bls,
		"SMTPVerify": s.Cfg.SMTPVerify, "RequireAuth": s.Cfg.RequireSendAuth, "BlacklistCheck": s.Cfg.BlacklistCheck,
	})
}

func (s *Server) deliverabilityValidate(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_ = r.ParseForm()
	email := strings.TrimSpace(r.FormValue("email"))
	eng := s.Deliverability
	if eng == nil {
		eng = deliverability.New(deliverability.DefaultConfig())
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	d := eng.QuickValidate(ctx, email)
	s.Store.LogDeliverabilityDecision(u.WorkspaceID, 0, d)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div class="panel">
  <p><strong>%s</strong> → <span class="badge %s">%s</span></p>
  <p class="muted">Bounce %.0f%% · Trap %.0f%% · Engage %.0f%% · Domain %.0f · ISP %s</p>
  <ul>%s</ul>
  %s
</div>`,
		template.HTMLEscapeString(d.Email),
		map[string]string{"send": "done", "delay": "warn", "suppress": "bad"}[string(d.Action)],
		d.Action, d.BounceProb, d.SpamTrapRisk, d.EngagementProb, d.DomainScore, template.HTMLEscapeString(d.ISP),
		func() string {
			var b strings.Builder
			for _, r := range d.Reasons {
				b.WriteString("<li>" + template.HTMLEscapeString(r) + "</li>")
			}
			return b.String()
		}(),
		func() string {
			if d.SuggestedDomain != "" {
				return "<p>Suggested domain: <code>" + template.HTMLEscapeString(d.SuggestedDomain) + "</code></p>"
			}
			return ""
		}(),
	)
}

func (s *Server) deliverabilityBlacklist(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_ = r.ParseForm()
	host := strings.TrimSpace(r.FormValue("host"))
	if host == "" {
		http.Redirect(w, r, "/deliverability", http.StatusSeeOther)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	ips := deliverability.ResolveSendingIPs(host)
	var listed bool
	var zones []string
	for _, ip := range ips {
		l, z := deliverability.CheckBlacklists(ctx, ip)
		if l {
			listed = true
			zones = append(zones, z...)
		}
		s.Store.SaveBlacklistCheck(ip, l, z)
	}
	if len(ips) == 0 {
		s.Store.SaveBlacklistCheck(host, false, []string{"unresolved"})
	}
	s.Store.Audit(u.WorkspaceID, u.ID, "blacklist.check", "host", host, strings.Join(zones, ","))
	_ = listed
	http.Redirect(w, r, "/deliverability", http.StatusSeeOther)
}

func (s *Server) leadValidateEmail(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	lead, err := s.Store.GetLead(id)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	if !u.IsAdmin() && lead.OwnerID != u.ID {
		http.Error(w, "forbidden", 403)
		return
	}
	eng := s.Deliverability
	if eng == nil {
		eng = deliverability.New(deliverability.DefaultConfig())
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	d := eng.QuickValidate(ctx, lead.Email)
	summary := string(d.Action) + ": " + strings.Join(d.Reasons, "; ")
	_ = s.Store.SaveLeadValidation(id, d.BounceProb, summary)
	s.Store.LogDeliverabilityDecision(u.WorkspaceID, 0, d)
	if d.Action == deliverability.ActionSuppress {
		_ = s.Store.AddSuppressionWS(u.WorkspaceID, lead.Email, "validation")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<span class="badge %s">%s · bounce %.0f%%</span>`,
		map[string]string{"send": "done", "delay": "warn", "suppress": "bad"}[string(d.Action)],
		d.Action, d.BounceProb)
}

func (s *Server) campaignDeliverability(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	camp, err := s.Store.GetCampaign(id)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	if !u.IsAdmin() && camp.OwnerID != u.ID {
		http.Error(w, "forbidden", 403)
		return
	}
	_ = r.ParseForm()
	paused := r.FormValue("paused") == "1"
	_ = s.Store.SetCampaignDeliverabilityPaused(id, paused)
	if !paused && camp.Status == "paused" {
		_ = s.Store.SetCampaignStatus(id, "active")
	}
	s.Store.Audit(u.WorkspaceID, u.ID, "campaign.deliverability", "campaign", strconv.FormatInt(id, 10), r.FormValue("paused"))
	http.Redirect(w, r, "/campaigns", http.StatusSeeOther)
}
