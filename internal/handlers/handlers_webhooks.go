package handlers

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/manishkumar/outreachcrm/internal/store"
)

func (s *Server) applyESPEvent(email, reason, event string) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return
	}
	ws := s.Store.WorkspaceIDForEmail(email)
	_ = s.Store.AddSuppressionWS(ws, email, reason)
	_ = s.Store.RecordRecipientEvent(ws, email, event)
	_ = s.Store.PauseHotCampaigns(ws, 2.0, 0.1)
}

func (s *Server) webhookPostmark(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload)
	email, _ := payload["Email"].(string)
	recordType, _ := payload["RecordType"].(string)
	rt := strings.ToLower(recordType)
	bounceType, _ := payload["Type"].(string)
	bt := strings.ToLower(bounceType)

	switch {
	case strings.Contains(rt, "spamcomplaint") || strings.Contains(rt, "spam"):
		s.applyESPEvent(email, "complaint", "complaint")
	case rt == "open":
		ws := s.Store.WorkspaceIDForEmail(email)
		_ = s.Store.RecordRecipientEvent(ws, email, "opened")
	case rt == "click":
		ws := s.Store.WorkspaceIDForEmail(email)
		_ = s.Store.RecordRecipientEvent(ws, email, "opened")
		_ = s.Store.RecordRecipientEvent(ws, email, "clicked")
	case rt == "subscriptionchange":
		s.applyESPEvent(email, "unsubscribe", "unsubscribe")
	case rt == "bounce" && (bt == "transient" || bt == "softbounce"):
		ws := s.Store.WorkspaceIDForEmail(email)
		_ = s.Store.RecordRecipientEvent(ws, email, "soft_bounce")
	case rt == "bounce":
		s.applyESPEvent(email, "bounce", "hard_bounce")
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) webhookSES(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var envelope map[string]any
	_ = json.Unmarshal(body, &envelope)
	msgStr, _ := envelope["Message"].(string)
	var msg map[string]any
	if msgStr != "" {
		_ = json.Unmarshal([]byte(msgStr), &msg)
	} else {
		msg = envelope
	}
	notif, _ := msg["notificationType"].(string)
	switch strings.ToLower(notif) {
	case "bounce":
		bounce, _ := msg["bounce"].(map[string]any)
		bounceType, _ := bounce["bounceType"].(string)
		soft := strings.EqualFold(bounceType, "Transient")
		if recipients, ok := bounce["bouncedRecipients"].([]any); ok {
			for _, r0 := range recipients {
				m, _ := r0.(map[string]any)
				email, _ := m["emailAddress"].(string)
				if email == "" {
					continue
				}
				if soft {
					ws := s.Store.WorkspaceIDForEmail(email)
					_ = s.Store.RecordRecipientEvent(ws, email, "soft_bounce")
				} else {
					s.applyESPEvent(email, "bounce", "hard_bounce")
				}
			}
		}
	case "complaint":
		if complaint, ok := msg["complaint"].(map[string]any); ok {
			if recipients, ok := complaint["complainedRecipients"].([]any); ok {
				for _, r0 := range recipients {
					m, _ := r0.(map[string]any)
					email, _ := m["emailAddress"].(string)
					s.applyESPEvent(email, "complaint", "complaint")
				}
			}
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// webhookSmartlead receives Smartlead events. This is inbound only: the
// subscription itself is created by hand in Smartlead's UI, because registering
// one is a POST and the integration is read-only.
//
// The path carries a shared secret so an unauthenticated caller cannot inject
// events. Delivery is acknowledged with 200 even when the event is ignored —
// Smartlead retries on failure, and retrying an event about someone we have not
// imported would never succeed.
func (s *Server) webhookSmartlead(w http.ResponseWriter, r *http.Request) {
	secret := s.Store.GetSetting("smartlead_webhook_secret", "")
	if secret == "" || r.PathValue("secret") != secret {
		http.NotFound(w, r)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}
	ev, err := store.ParseSmartleadWebhook(raw)
	if err != nil {
		// Malformed bodies are not worth retrying.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ignored"}`))
		return
	}

	ws := s.Store.WorkspaceIDForEmail(ev.Email)
	applied, err := s.Store.IngestSmartleadEvent(ws, ev)
	if err != nil {
		slog.Warn("smartlead webhook", "type", ev.Type, "err", err)
		http.Error(w, "ingest failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	if applied {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return
	}
	_, _ = w.Write([]byte(`{"status":"ignored"}`))
}
