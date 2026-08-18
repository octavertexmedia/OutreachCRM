package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/manishkumar/outreachcrm/internal/models"
	"github.com/manishkumar/outreachcrm/internal/store"
)

// ReplySuggester drafts a HITL reply. writing.Service implements this without an import cycle.
type ReplySuggester interface {
	SuggestReply(ctx context.Context, lead models.Lead, inbound string) (string, error)
}

type ToolDef struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func ReadOnlyTools() []ToolDef {
	return []ToolDef{
		fn("workspace_pulse", "Counts for this workspace: leads, due queue, unreplied inbox, suppressions.",
			obj(nil)),
		fn("search_leads", "Search leads by name, email, or company.",
			obj(map[string]interface{}{
				"query": prop("string", "Search text"),
				"limit": prop("integer", "Max rows (default 8, max 20)"),
			}, "query")),
		fn("get_lead", "Fetch one lead by id.",
			obj(map[string]interface{}{
				"lead_id": prop("integer", "Lead id"),
			}, "lead_id")),
		fn("list_campaigns", "List campaigns in this workspace.",
			obj(map[string]interface{}{
				"limit": prop("integer", "Max rows (default 15, max 30)"),
			})),
		fn("list_queue", "List scheduled/sending queue messages.",
			obj(map[string]interface{}{
				"limit": prop("integer", "Max rows (default 15, max 40)"),
			})),
		fn("list_inbox_replies", "List recent inbound replies.",
			obj(map[string]interface{}{
				"limit": prop("integer", "Max rows (default 12, max 30)"),
			})),
	}
}

func WriteTools() []ToolDef {
	return []ToolDef{
		fn("update_lead_status", "Set a lead pipeline status. Requires confirm=true after the user agrees.",
			obj(map[string]interface{}{
				"lead_id": prop("integer", "Lead id"),
				"status":  prop("string", "new | contacted | interested | not_interested | unsubscribed | customer"),
				"confirm": prop("boolean", "Must be true to execute"),
			}, "lead_id", "status")),
		fn("enroll_in_campaign", "Enroll a lead in a campaign sequence. Requires confirm=true.",
			obj(map[string]interface{}{
				"lead_id":     prop("integer", "Lead id"),
				"campaign_id": prop("integer", "Campaign id"),
				"confirm":     prop("boolean", "Must be true to execute"),
			}, "lead_id", "campaign_id")),
		fn("draft_reply", "Save a suggested reply draft on the lead (HITL, not sent). Requires confirm=true.",
			obj(map[string]interface{}{
				"lead_id": prop("integer", "Lead id"),
				"subject": prop("string", "Reply subject"),
				"body":    prop("string", "Reply body (optional — generated if empty)"),
				"confirm": prop("boolean", "Must be true to execute"),
			}, "lead_id")),
		fn("create_followup_note", "Append a follow-up note on a lead. Requires confirm=true.",
			obj(map[string]interface{}{
				"lead_id": prop("integer", "Lead id"),
				"note":    prop("string", "Note text"),
				"confirm": prop("boolean", "Must be true to execute"),
			}, "lead_id", "note")),
	}
}

func AllAgentTools() []ToolDef {
	return append(ReadOnlyTools(), WriteTools()...)
}

func fn(name, desc string, params map[string]interface{}) ToolDef {
	return ToolDef{Type: "function", Function: ToolFunction{Name: name, Description: desc, Parameters: params}}
}

func prop(typ, desc string) map[string]interface{} {
	return map[string]interface{}{"type": typ, "description": desc}
}

func obj(properties map[string]interface{}, required ...string) map[string]interface{} {
	if properties == nil {
		properties = map[string]interface{}{}
	}
	out := map[string]interface{}{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

type ToolExecutor struct {
	Store       *store.Store
	WorkspaceID int64
	OwnerID     int64
	Admin       bool
	AutoConfirm bool
	Writing     ReplySuggester
	ReplyID     int64
	LeadID      int64
	Ctx         context.Context
}

func (e *ToolExecutor) confirmed(args map[string]interface{}) bool {
	return e != nil && (e.AutoConfirm || boolArg(args, "confirm"))
}

func (e *ToolExecutor) Execute(name, argsJSON string) (string, error) {
	if e == nil || e.Store == nil || e.WorkspaceID <= 0 {
		return "", fmt.Errorf("tool executor not configured")
	}
	var args map[string]interface{}
	if strings.TrimSpace(argsJSON) != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return toolErr("invalid arguments JSON"), nil
		}
	}
	if args == nil {
		args = map[string]interface{}{}
	}
	switch name {
	case "workspace_pulse":
		p, err := e.Store.WorkspacePulse(e.WorkspaceID)
		if err != nil {
			return toolErr(err.Error()), nil
		}
		return toolOK(p), nil
	case "search_leads":
		q := strings.TrimSpace(strArg(args, "query"))
		if q == "" {
			return toolErr("query is required"), nil
		}
		limit := intArg(args, "limit", 8, 1, 20)
		list, err := e.Store.ListLeadsFiltered(e.Admin, e.OwnerID, e.WorkspaceID, models.LeadFilter{Q: q})
		if err != nil {
			return toolErr(err.Error()), nil
		}
		if len(list) > limit {
			list = list[:limit]
		}
		rows := make([]map[string]interface{}, 0, len(list))
		for _, l := range list {
			rows = append(rows, leadRow(l))
		}
		return toolOK(map[string]interface{}{"query": q, "count": len(rows), "items": rows}), nil
	case "get_lead":
		id := int64(intArg(args, "lead_id", 0, 0, 1<<30))
		if id <= 0 && e.LeadID > 0 {
			id = e.LeadID
		}
		l, err := e.leadInScope(id)
		if err != nil {
			return toolErr(err.Error()), nil
		}
		return toolOK(leadRow(l)), nil
	case "list_campaigns":
		limit := intArg(args, "limit", 15, 1, 30)
		list, err := e.Store.ListCampaigns(e.Admin, e.OwnerID, e.WorkspaceID)
		if err != nil {
			return toolErr(err.Error()), nil
		}
		if len(list) > limit {
			list = list[:limit]
		}
		rows := make([]map[string]interface{}, 0, len(list))
		for _, c := range list {
			rows = append(rows, map[string]interface{}{
				"id": c.ID, "name": c.Name, "status": c.Status,
				"daily_limit": c.DailySendLimit, "href": fmt.Sprintf("/campaigns"),
			})
		}
		return toolOK(map[string]interface{}{"count": len(rows), "items": rows}), nil
	case "list_queue":
		limit := intArg(args, "limit", 15, 1, 40)
		list, err := e.Store.ListQueueWS(e.Admin, e.OwnerID, e.WorkspaceID, limit)
		if err != nil {
			return toolErr(err.Error()), nil
		}
		rows := make([]map[string]interface{}, 0, len(list))
		for _, m := range list {
			rows = append(rows, map[string]interface{}{
				"id": m.ID, "campaign_id": m.CampaignID, "lead_id": m.LeadID,
				"to": m.ToEmail, "subject": m.Subject, "status": m.Status,
				"scheduled_at": m.ScheduledAt.Format(time.RFC3339),
				"href":         "/queue",
			})
		}
		return toolOK(map[string]interface{}{"count": len(rows), "items": rows}), nil
	case "list_inbox_replies":
		limit := intArg(args, "limit", 12, 1, 30)
		list, err := e.Store.ListRepliesWS(e.WorkspaceID, limit)
		if err != nil {
			return toolErr(err.Error()), nil
		}
		rows := make([]map[string]interface{}, 0, len(list))
		for _, r := range list {
			leadID := int64(0)
			if r.LeadID != nil {
				leadID = *r.LeadID
			}
			body := r.Body
			if len([]rune(body)) > 280 {
				body = string([]rune(body)[:280]) + "…"
			}
			rows = append(rows, map[string]interface{}{
				"id": r.ID, "from": r.FromEmail, "lead": r.LeadName, "lead_id": leadID,
				"subject": r.Subject, "intent": r.Intent, "hitl": r.HITLStatus,
				"body": body, "href": "/inbox",
			})
		}
		return toolOK(map[string]interface{}{"count": len(rows), "items": rows}), nil
	case "update_lead_status":
		preview := map[string]interface{}{
			"lead_id": intArg(args, "lead_id", 0, 0, 1<<30),
			"status":  strArg(args, "status"),
		}
		if !e.confirmed(args) {
			return needsConfirm("Set confirm=true after the user agrees to change this lead status.", preview), nil
		}
		id := int64(intArg(args, "lead_id", 0, 0, 1<<30))
		if _, err := e.leadInScope(id); err != nil {
			return toolErr(err.Error()), nil
		}
		status := normalizeLeadStatus(strArg(args, "status"))
		if status == "" {
			return toolErr("invalid status"), nil
		}
		if err := e.Store.UpdateLeadStatus(id, status); err != nil {
			return toolErr(err.Error()), nil
		}
		return toolOK(map[string]interface{}{"status": "updated", "lead_id": id, "lead_status": status, "href": fmt.Sprintf("/leads")}), nil
	case "enroll_in_campaign":
		preview := map[string]interface{}{
			"lead_id":     intArg(args, "lead_id", 0, 0, 1<<30),
			"campaign_id": intArg(args, "campaign_id", 0, 0, 1<<30),
		}
		if !e.confirmed(args) {
			return needsConfirm("Set confirm=true after the user agrees to enroll this lead.", preview), nil
		}
		leadID := int64(intArg(args, "lead_id", 0, 0, 1<<30))
		campID := int64(intArg(args, "campaign_id", 0, 0, 1<<30))
		if _, err := e.leadInScope(leadID); err != nil {
			return toolErr(err.Error()), nil
		}
		camp, err := e.Store.GetCampaign(campID)
		if err != nil || camp.WorkspaceID != e.WorkspaceID {
			return toolErr("campaign not found"), nil
		}
		if err := e.Store.EnrollLead(campID, leadID); err != nil {
			return toolErr(err.Error()), nil
		}
		return toolOK(map[string]interface{}{"status": "enrolled", "lead_id": leadID, "campaign_id": campID, "href": "/queue"}), nil
	case "draft_reply":
		leadID := int64(intArg(args, "lead_id", 0, 0, 1<<30))
		if leadID <= 0 && e.LeadID > 0 {
			leadID = e.LeadID
		}
		subject := strings.TrimSpace(strArg(args, "subject"))
		body := strings.TrimSpace(strArg(args, "body"))
		preview := map[string]interface{}{"lead_id": leadID, "subject": subject, "body": body}
		if !e.confirmed(args) {
			return needsConfirm("Set confirm=true after the user agrees to save this draft (not sent).", preview), nil
		}
		lead, err := e.leadInScope(leadID)
		if err != nil {
			return toolErr(err.Error()), nil
		}
		if body == "" && e.Writing != nil {
			ctx := e.Ctx
			if ctx == nil {
				ctx = context.Background()
			}
			inbound := ""
			if e.ReplyID > 0 {
				if rp, rerr := e.Store.GetReply(e.ReplyID); rerr == nil {
					inbound = rp.Body
					if subject == "" && rp.Subject != "" {
						subject = "Re: " + rp.Subject
					}
				}
			}
			sug, serr := e.Writing.SuggestReply(ctx, lead, inbound)
			if serr != nil {
				return toolErr(serr.Error()), nil
			}
			body = sug
		}
		if subject == "" {
			subject = "Follow-up"
		}
		if body == "" {
			return toolErr("body is required"), nil
		}
		if err := e.Store.SaveLeadDraft(leadID, subject, body); err != nil {
			return toolErr(err.Error()), nil
		}
		return toolOK(map[string]interface{}{"status": "drafted", "lead_id": leadID, "subject": subject, "href": "/leads"}), nil
	case "create_followup_note":
		leadID := int64(intArg(args, "lead_id", 0, 0, 1<<30))
		note := strings.TrimSpace(strArg(args, "note"))
		preview := map[string]interface{}{"lead_id": leadID, "note": note}
		if !e.confirmed(args) {
			return needsConfirm("Set confirm=true after the user agrees to add this note.", preview), nil
		}
		if _, err := e.leadInScope(leadID); err != nil {
			return toolErr(err.Error()), nil
		}
		if note == "" {
			return toolErr("note is required"), nil
		}
		if err := e.Store.AppendLeadNote(leadID, note); err != nil {
			return toolErr(err.Error()), nil
		}
		return toolOK(map[string]interface{}{"status": "noted", "lead_id": leadID, "href": "/leads"}), nil
	default:
		return toolErr("unknown tool: " + name), nil
	}
}

func (e *ToolExecutor) leadInScope(id int64) (models.Lead, error) {
	if id <= 0 {
		return models.Lead{}, fmt.Errorf("lead_id is required")
	}
	l, err := e.Store.GetLead(id)
	if err != nil {
		return models.Lead{}, fmt.Errorf("lead not found")
	}
	if e.WorkspaceID > 0 && l.WorkspaceID != e.WorkspaceID {
		return models.Lead{}, fmt.Errorf("lead not found")
	}
	if !e.Admin && e.OwnerID > 0 && l.OwnerID != e.OwnerID && l.OwnerID != 0 {
		return models.Lead{}, fmt.Errorf("lead not found")
	}
	return l, nil
}

func leadRow(l models.Lead) map[string]interface{} {
	st := strings.TrimSpace(l.Status)
	if st == "" {
		st = models.LeadStatusNew
	}
	return map[string]interface{}{
		"id": l.ID, "name": l.Name, "email": l.Email, "company": l.Company,
		"title": l.Title, "status": st, "enrichment": l.EnrichmentStatus,
		"notes": l.Notes, "href": fmt.Sprintf("/leads"),
	}
}

func normalizeLeadStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case models.LeadStatusNew, models.LeadStatusContacted, models.LeadStatusInterested,
		models.LeadStatusNotInterested, models.LeadStatusUnsubscribed, models.LeadStatusCustomer:
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return ""
	}
}

func needsConfirm(msg string, preview map[string]interface{}) string {
	return toolOK(map[string]interface{}{
		"status":  "needs_confirmation",
		"message": msg,
		"preview": preview,
	})
}

func toolOK(v interface{}) string {
	b, _ := json.Marshal(map[string]interface{}{"ok": true, "data": v})
	return string(b)
}

func toolErr(msg string) string {
	b, _ := json.Marshal(map[string]interface{}{"ok": false, "error": msg})
	return string(b)
}

func strArg(args map[string]interface{}, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func boolArg(args map[string]interface{}, key string) bool {
	v, ok := args[key]
	if !ok || v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true") || t == "1"
	default:
		return false
	}
}

func intArg(args map[string]interface{}, key string, def, min, max int) int {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	n := def
	switch t := v.(type) {
	case float64:
		n = int(t)
	case int:
		n = t
	case int64:
		n = int(t)
	case json.Number:
		if i, err := t.Int64(); err == nil {
			n = int(i)
		}
	case string:
		var i int
		if _, err := fmt.Sscanf(t, "%d", &i); err == nil {
			n = i
		}
	}
	if n < min {
		n = min
	}
	if n > max {
		n = max
	}
	return n
}

func DefaultSystemPrompt() string {
	return `You are the OutReachCRM staff assistant. Help operators with leads, campaigns, the send queue, and inbox replies.
Use tools for live facts — do not invent ids, counts, or emails.
Write tools (status, enroll, draft, notes) must preview first, then execute only with confirm=true after the user clearly agrees.
Cite hrefs from tool results. After tools return, answer in plain language — do not dump raw JSON.`
}

func ToolUseRules() string {
	return `TOOL USE:
- Call workspace_pulse or list_* before guessing counts.
- search_leads / get_lead for a specific person.
- Write tools require confirm=true only after the user says yes.
- draft_reply saves a HITL draft — it does not send mail.`
}
