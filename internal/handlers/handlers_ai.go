package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/manishkumar/outreachcrm/internal/llm"
	"github.com/manishkumar/outreachcrm/internal/models"
)

type aiChatTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type aiChatRequest struct {
	Message string       `json:"message"`
	History []aiChatTurn `json:"history"`
	ReplyID int64        `json:"reply_id"`
	LeadID  int64        `json:"lead_id"`
}

func (s *Server) registerAIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/dashboard/ai/chat", s.dashboardAIChat)
	mux.HandleFunc("POST /api/inbox/ai/chat", s.inboxAIChat)
	mux.HandleFunc("GET /settings/email", s.settingsEmailGet)
	mux.HandleFunc("POST /settings/email/ai", s.settingsAIPost)
	mux.HandleFunc("POST /settings/email/marketing", s.settingsMarketingPost)
}

func (s *Server) dashboardAIChat(w http.ResponseWriter, r *http.Request) {
	s.runStaffAIChat(w, r, false)
}

func (s *Server) inboxAIChat(w http.ResponseWriter, r *http.Request) {
	s.runStaffAIChat(w, r, true)
}

func (s *Server) runStaffAIChat(w http.ResponseWriter, r *http.Request, inbox bool) {
	u := s.current(r)
	if u.ID == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.Cfg.AIMode == models.AIModeOff {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "AI assistant is off. Set OUTREACH_AI_MODE=suggest and an OPENAI_API_KEY."})
		return
	}

	var req aiChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = r.ParseForm()
		req.Message = strings.TrimSpace(r.FormValue("message"))
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Message is required"})
		return
	}
	if len(req.Message) > 4000 {
		req.Message = req.Message[:4000]
	}

	client, system, business, err := s.resolveWorkspaceLLM(u.WorkspaceID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	sys := strings.TrimSpace(system + "\n\n" + business + "\n\n" + llm.ToolUseRules())
	if inbox {
		sys += "\nYou are assisting from Inbox. Prefer list_inbox_replies, get_lead, and draft_reply. Do not send mail."
		if req.ReplyID > 0 {
			if rp, rerr := s.Store.GetReply(req.ReplyID); rerr == nil {
				sys += "\n\n## Open reply\n" + inboxReplyContext(rp)
				if rp.LeadID != nil && req.LeadID == 0 {
					req.LeadID = *rp.LeadID
				}
			}
		}
		if req.LeadID > 0 {
			if ld, lerr := s.Store.GetLead(req.LeadID); lerr == nil {
				sys += "\nLinked lead: " + ld.Name + " <" + ld.Email + "> status=" + ld.Status
			}
		}
	}

	messages := []llm.Message{{Role: "system", Content: sys}}
	nHist := 0
	for _, h := range req.History {
		role := strings.ToLower(strings.TrimSpace(h.Role))
		content := strings.TrimSpace(h.Content)
		if content == "" || (role != "user" && role != "assistant") {
			continue
		}
		if len([]rune(content)) > 2000 {
			content = string([]rune(content)[:2000]) + "…"
		}
		messages = append(messages, llm.Message{Role: role, Content: content})
		nHist++
		if nHist >= 10 {
			break
		}
	}
	messages = append(messages, llm.Message{Role: "user", Content: req.Message})

	ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
	defer cancel()
	exec := &llm.ToolExecutor{
		Store:       s.Store,
		WorkspaceID: u.WorkspaceID,
		OwnerID:     u.ID,
		Admin:       u.IsAdmin(),
		Writing:     s.Writing, // writing.Service implements llm.ReplySuggester
		ReplyID:     req.ReplyID,
		LeadID:      req.LeadID,
		Ctx:         ctx,
	}
	loop, err := llm.RunToolAgent(ctx, client, messages, exec)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}

	toolNames := make([]string, 0, len(loop.Tools))
	for _, tr := range loop.Tools {
		toolNames = append(toolNames, tr.Name)
		s.Store.Audit(u.WorkspaceID, u.ID, "ai.tool", tr.Name, "", truncateRunes(tr.Args, 80))
	}
	action := "ai.dashboard"
	if inbox {
		action = "ai.inbox"
	}
	s.Store.Audit(u.WorkspaceID, u.ID, action, "ask", "", truncateRunes(req.Message, 120))

	writeJSON(w, http.StatusOK, map[string]any{
		"answer":      loop.Answer,
		"model":       client.Model,
		"tools_used":  toolNames,
		"tool_rounds": loop.Rounds,
	})
}

func inboxReplyContext(rp models.InboundReply) string {
	leadID := int64(0)
	if rp.LeadID != nil {
		leadID = *rp.LeadID
	}
	body := rp.Body
	if len([]rune(body)) > 600 {
		body = string([]rune(body)[:600]) + "…"
	}
	return "reply_id=" + strconv.FormatInt(rp.ID, 10) + " from=" + rp.FromEmail + " lead=" + rp.LeadName +
		" lead_id=" + strconv.FormatInt(leadID, 10) + " intent=" + rp.Intent + "\nsubject=" + rp.Subject + "\n" + body
}

func (s *Server) resolveWorkspaceLLM(workspaceID int64) (*llm.Client, string, string, error) {
	settings, _ := s.Store.GetWorkspaceAI(workspaceID)
	key := s.Cfg.OpenAIAPIKey
	base := s.Cfg.OpenAIBaseURL
	model := s.Cfg.OpenAIModel
	if settings.OpenAIKeyEnc != "" && s.Box != nil {
		if dec, err := s.Box.Decrypt(settings.OpenAIKeyEnc); err == nil && strings.TrimSpace(dec) != "" {
			key = dec
		}
	}
	if strings.TrimSpace(settings.OpenAIBaseURL) != "" {
		base = strings.TrimRight(settings.OpenAIBaseURL, "/")
	}
	if strings.TrimSpace(settings.OpenAIModel) != "" {
		model = settings.OpenAIModel
	}
	if strings.TrimSpace(key) == "" {
		return nil, "", "", errAIKeyMissing
	}
	system := strings.TrimSpace(settings.SystemPrompt)
	if system == "" {
		system = llm.DefaultSystemPrompt()
	}
	return llm.New(key, base, model), system, settings.BusinessPrompt, nil
}

var errAIKeyMissing = errString("LLM API key is missing. Set OPENAI_API_KEY or a workspace override on /settings/email.")

type errString string

func (e errString) Error() string { return string(e) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (s *Server) aiPanelData(u models.SessionUser, endpoint string) map[string]any {
	keyOK := s.Cfg.OpenAIAPIKey != ""
	if !keyOK {
		if ws, err := s.Store.GetWorkspaceAI(u.WorkspaceID); err == nil && ws.OpenAIKeyEnc != "" {
			keyOK = true
		}
	}
	return map[string]any{
		"AIEnabled":  s.Cfg.AIMode != models.AIModeOff && keyOK,
		"AIMode":     s.Cfg.AIMode,
		"AIEndpoint": endpoint,
	}
}
