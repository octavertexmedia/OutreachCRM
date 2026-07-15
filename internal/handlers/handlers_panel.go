package handlers

import (
	"net/http"
	"strconv"

	"github.com/manishkumar/outreachcrm/internal/models"
)

func (s *Server) registerPanelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /panel/leads/{id}", s.panelLead)
	mux.HandleFunc("GET /panel/campaigns/{id}", s.panelCampaign)
	mux.HandleFunc("GET /panel/queue/{id}", s.panelQueue)
}

func (s *Server) panelLead(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	lead, err := s.Store.GetLead(id)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	if !s.canAccessOwner(u, lead.OwnerID) {
		http.Error(w, "forbidden", 403)
		return
	}
	hist := s.Store.GetRecipientHistory(lead.Email)
	s.render(w, "panel_lead.html", map[string]any{"Lead": lead, "History": hist, "User": u})
}

func (s *Server) panelCampaign(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	camp, err := s.Store.GetCampaign(id)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	if !s.canAccessOwner(u, camp.OwnerID) {
		http.Error(w, "forbidden", 403)
		return
	}
	steps, _ := s.Store.ListSteps(id)
	s.render(w, "panel_campaign.html", map[string]any{
		"Campaign": camp, "Steps": steps, "User": u,
	})
}

func (s *Server) panelQueue(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	msg, err := s.Store.GetOutboundMessage(id)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	camp, err := s.Store.GetCampaign(msg.CampaignID)
	if err != nil || !s.canAccessOwner(u, camp.OwnerID) {
		http.Error(w, "forbidden", 403)
		return
	}
	var lead models.Lead
	if msg.LeadID > 0 {
		lead, _ = s.Store.GetLead(msg.LeadID)
	}
	s.render(w, "panel_queue.html", map[string]any{"Msg": msg, "Lead": lead, "User": u})
}
