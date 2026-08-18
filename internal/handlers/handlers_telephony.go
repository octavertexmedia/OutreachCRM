package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/manishkumar/outreachcrm/internal/auth"
	"github.com/manishkumar/outreachcrm/internal/models"
	"github.com/manishkumar/outreachcrm/internal/telephony"
)

func (s *Server) registerTelephonyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /calls", s.callsGet)
	mux.HandleFunc("POST /calls/settings", auth.RequireAdmin(s.callsSettings))
	mux.HandleFunc("POST /calls/test", auth.RequireAdmin(s.callsTest))
	mux.HandleFunc("POST /calls/disconnect", auth.RequireAdmin(s.callsDisconnect))
	mux.HandleFunc("POST /calls/sync", s.callsSync)
	mux.HandleFunc("POST /leads/{id}/call", s.leadCall)

	// Smartflo can be configured to deliver events as GET or POST, in JSON or
	// form encoding; the shared secret in the path is the only authentication
	// the platform offers (it documents no request signature).
	mux.HandleFunc("POST /webhooks/smartflo/{secret}", s.webhookSmartflo)
	mux.HandleFunc("GET /webhooks/smartflo/{secret}", s.webhookSmartflo)
}

// telephonyCreds resolves a workspace's Smartflo credentials, decrypting the
// stored secrets and falling back to the process-level env defaults.
func (s *Server) telephonyCreds(a models.TelephonyAccount) telephony.Credentials {
	c := telephony.Credentials{
		BaseURL:    a.BaseURL,
		Email:      a.LoginEmail,
		AuthScheme: a.AuthScheme,
	}
	if a.PasswordEnc != "" {
		if pw, err := s.Box.Decrypt(a.PasswordEnc); err == nil {
			c.Password = pw
		}
	}
	if a.TokenEnc != "" {
		if tok, err := s.Box.Decrypt(a.TokenEnc); err == nil {
			c.StaticToken = tok
		}
	}
	if c.BaseURL == "" {
		c.BaseURL = s.Cfg.SmartfloBaseURL
	}
	if c.AuthScheme == "" {
		c.AuthScheme = s.Cfg.SmartfloAuthScheme
	}
	if !c.Configured() {
		c.Email = s.Cfg.SmartfloEmail
		c.Password = s.Cfg.SmartfloPassword
		c.StaticToken = s.Cfg.SmartfloToken
	}
	return c
}

// telephonyFor returns the account, an authenticated client, and whether the
// workspace can place calls at all.
func (s *Server) telephonyFor(workspaceID int64) (models.TelephonyAccount, *telephony.Client, bool) {
	a, err := s.Store.GetTelephonyAccount(workspaceID)
	if err != nil {
		return a, nil, false
	}
	if a.AgentNumber == "" {
		a.AgentNumber = s.Cfg.SmartfloAgentNumber
	}
	if a.CallerID == "" {
		a.CallerID = s.Cfg.SmartfloCallerID
	}
	if a.CallTimeout == 0 {
		a.CallTimeout = s.Cfg.SmartfloCallTimeout
	}
	creds := s.telephonyCreds(a)
	if !creds.Configured() {
		return a, nil, false
	}
	return a, s.Telephony.Client(creds), true
}

func (s *Server) callsGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	acct, _, ready := s.telephonyFor(u.WorkspaceID)
	logs, err := s.Store.ListCallLogs(u.WorkspaceID, 100)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	webhookURL := ""
	if acct.WebhookSecret != "" {
		webhookURL = s.Cfg.PublicBaseURL + "/webhooks/smartflo/" + acct.WebhookSecret
	}
	s.render(w, "calls.html", map[string]any{
		"Nav": "calls", "User": u, "Q": "",
		"Account":    acct,
		"Ready":      ready,
		"EnvDefault": s.Cfg.SmartfloEmail != "" || s.Cfg.SmartfloToken != "",
		"BaseURL":    s.Cfg.SmartfloBaseURL,
		"WebhookURL": webhookURL,
		"Calls":      logs,
		"Stats":      s.Store.CallStatsFor(u.WorkspaceID),
	})
}

func (s *Server) callsSettings(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_ = r.ParseForm()
	acct, err := s.Store.GetTelephonyAccount(u.WorkspaceID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	acct.WorkspaceID = u.WorkspaceID
	acct.Provider = models.ProviderSmartflo
	acct.BaseURL = strings.TrimRight(strings.TrimSpace(r.FormValue("base_url")), "/")
	acct.LoginEmail = strings.TrimSpace(r.FormValue("login_email"))
	acct.AuthScheme = strings.TrimSpace(r.FormValue("auth_scheme"))
	acct.AgentNumber = strings.TrimSpace(r.FormValue("agent_number"))
	acct.CallerID = strings.TrimSpace(r.FormValue("caller_id"))
	acct.CallTimeout, _ = strconv.Atoi(r.FormValue("call_timeout"))
	acct.Enabled = r.FormValue("enabled") == "1" || r.FormValue("enabled") == "on"

	// Blank password/token fields mean "keep what is stored" so the form can
	// be re-saved without retyping secrets.
	if pw := r.FormValue("password"); pw != "" {
		enc, err := s.Box.Encrypt(pw)
		if err != nil {
			http.Error(w, "encrypt: "+err.Error(), 500)
			return
		}
		acct.PasswordEnc = enc
	}
	if tok := strings.TrimSpace(r.FormValue("access_token")); tok != "" {
		enc, err := s.Box.Encrypt(tok)
		if err != nil {
			http.Error(w, "encrypt: "+err.Error(), 500)
			return
		}
		acct.TokenEnc = enc
	}
	if r.FormValue("clear_token") == "1" {
		acct.TokenEnc = ""
	}
	if acct.WebhookSecret == "" || r.FormValue("rotate_secret") == "1" {
		acct.WebhookSecret = randomSecret()
	}
	if err := s.Store.SaveTelephonyAccount(acct); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.Store.Audit(u.WorkspaceID, u.ID, "telephony.save", "telephony", models.ProviderSmartflo, acct.LoginEmail)
	http.Redirect(w, r, "/calls", http.StatusSeeOther)
}

// callsTest performs a real token handshake so a bad password surfaces here
// rather than on the first click-to-call.
func (s *Server) callsTest(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_, client, ready := s.telephonyFor(u.WorkspaceID)
	if !ready {
		s.Store.MarkTelephonyVerified(u.WorkspaceID, "no credentials configured")
		http.Redirect(w, r, "/calls", http.StatusSeeOther)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	expiry, err := client.Login(ctx)
	if err != nil {
		s.Store.MarkTelephonyVerified(u.WorkspaceID, err.Error())
		s.Store.Audit(u.WorkspaceID, u.ID, "telephony.test.fail", "telephony", models.ProviderSmartflo, err.Error())
		http.Redirect(w, r, "/calls", http.StatusSeeOther)
		return
	}
	detail := "permanent token accepted"
	if !expiry.IsZero() {
		detail = "token valid until " + expiry.UTC().Format(time.RFC3339)
		if d := client.PasswordDaysLeft(); d > 0 {
			detail += fmt.Sprintf(" · password expires in %d days", d)
		}
	}
	s.Store.MarkTelephonyVerified(u.WorkspaceID, "")
	s.Store.Audit(u.WorkspaceID, u.ID, "telephony.test.ok", "telephony", models.ProviderSmartflo, detail)
	http.Redirect(w, r, "/calls", http.StatusSeeOther)
}

func (s *Server) callsDisconnect(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	acct, client, ready := s.telephonyFor(u.WorkspaceID)
	if ready {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		// Terminate the short-lived token server-side instead of leaving it live.
		if err := client.Logout(ctx); err != nil {
			slog.Warn("smartflo logout", "err", err)
		}
	}
	acct.WorkspaceID = u.WorkspaceID
	acct.LoginEmail, acct.PasswordEnc, acct.TokenEnc = "", "", ""
	acct.Enabled = false
	acct.LastError = ""
	if err := s.Store.SaveTelephonyAccount(acct); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.Store.Audit(u.WorkspaceID, u.ID, "telephony.disconnect", "telephony", models.ProviderSmartflo, "")
	http.Redirect(w, r, "/calls", http.StatusSeeOther)
}

// leadCall places a click-to-call: Smartflo rings the agent, then the lead.
func (s *Server) leadCall(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	lead, err := s.Store.GetLead(id)
	if err != nil || !s.canAccessOwner(u, lead.OwnerID) {
		http.Error(w, "not found", 404)
		return
	}
	_ = r.ParseForm()
	acct, client, ready := s.telephonyFor(u.WorkspaceID)
	if !ready {
		s.callFragment(w, id, false, "Smartflo is not connected — set it up on the Calls page.")
		return
	}
	if !acct.Enabled {
		s.callFragment(w, id, false, "Calling is switched off for this workspace — enable it on the Calls page.")
		return
	}
	destination := strings.TrimSpace(r.FormValue("number"))
	if destination == "" {
		destination = lead.Phone
	}
	if telephony.NormalizeNumber(destination) == "" {
		s.callFragment(w, id, false, "This lead has no phone number.")
		return
	}
	agent := strings.TrimSpace(r.FormValue("agent_number"))
	if agent == "" {
		agent = acct.AgentNumber
	}
	if agent == "" {
		s.callFragment(w, id, false, "No agent number configured — add one on the Calls page.")
		return
	}

	// The ref is generated before dialling so the webhook that arrives while
	// the API call is still in flight can find its row.
	ref := "orc-" + randomSecret()[:12]
	leadID := lead.ID
	if _, err := s.Store.CreateCallLog(models.CallLog{
		WorkspaceID:  u.WorkspaceID,
		LeadID:       &leadID,
		UserID:       u.ID,
		Provider:     models.ProviderSmartflo,
		RefID:        ref,
		Direction:    "click_to_call",
		Status:       models.CallStatusInitiated,
		AgentNumber:  agent,
		ClientNumber: telephony.NormalizeNumber(destination),
		CallerID:     acct.CallerID,
		StartedAt:    ptrTime(time.Now().UTC()),
	}); err != nil {
		s.callFragment(w, id, false, "Could not record the call: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	resp, err := client.ClickToCall(ctx, telephony.CallRequest{
		AgentNumber:       agent,
		DestinationNumber: telephony.NormalizeNumber(destination),
		CallerID:          acct.CallerID,
		Async:             1,
		CallTimeout:       acct.CallTimeout,
		CustomIdentifier:  ref,
	})
	if err != nil {
		_, _ = s.Store.UpsertCallEvent(models.CallLog{
			WorkspaceID: u.WorkspaceID, RefID: ref,
			Status: models.CallStatusFailed, HangupCause: truncate(err.Error(), 200),
		})
		s.Store.MarkTelephonyVerified(u.WorkspaceID, err.Error())
		s.callFragment(w, id, false, err.Error())
		return
	}
	if resp.CallID != "" {
		_, _ = s.Store.UpsertCallEvent(models.CallLog{
			WorkspaceID: u.WorkspaceID, RefID: ref, CallID: resp.CallID,
		})
	}
	s.Store.MarkTelephonyVerified(u.WorkspaceID, "")
	s.Store.Audit(u.WorkspaceID, u.ID, "call.start", "lead", strconv.FormatInt(id, 10), ref)
	msg := resp.Message
	if msg == "" {
		msg = "Calling — answer your phone, then " + lead.Name + " is dialled."
	}
	s.callFragment(w, id, true, msg)
}

func (s *Server) callFragment(w http.ResponseWriter, leadID int64, ok bool, msg string) {
	cls, label := "bad", "Call failed"
	if ok {
		cls, label = "done", "Calling"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div id="call-%d" class="call-note"><span class="badge %s">%s</span> <span class="muted">%s</span></div>`,
		leadID, cls, label, template.HTMLEscapeString(msg))
}

// callsSync pulls call detail records and reconciles them onto local rows —
// the backstop for events lost when the webhook endpoint was unreachable.
func (s *Server) callsSync(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	_, client, ready := s.telephonyFor(u.WorkspaceID)
	if !ready {
		http.Redirect(w, r, "/calls", http.StatusSeeOther)
		return
	}
	days := s.Cfg.SmartfloCDRDays
	if days <= 0 {
		days = 7
	}
	if v, err := strconv.Atoi(r.FormValue("days")); err == nil && v > 0 && v <= 90 {
		days = v
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	q := telephony.RecordsQuery{
		From:  time.Now().AddDate(0, 0, -days),
		To:    time.Now(),
		Page:  1,
		Limit: 100,
	}
	imported := 0
	for page := 1; page <= 10; page++ { // bounded: 1k records per sync
		q.Page = page
		res, err := client.CallRecords(ctx, q)
		if err != nil {
			s.Store.MarkTelephonyVerified(u.WorkspaceID, err.Error())
			slog.Error("smartflo cdr sync", "err", err)
			break
		}
		if len(res.Results) == 0 {
			break
		}
		for _, rec := range res.Results {
			s.ingestRecord(u.WorkspaceID, rec)
			imported++
		}
		if len(res.Results) < 100 {
			break
		}
	}
	s.Store.Audit(u.WorkspaceID, u.ID, "call.sync", "telephony", models.ProviderSmartflo, strconv.Itoa(imported))
	http.Redirect(w, r, "/calls", http.StatusSeeOther)
}

func (s *Server) ingestRecord(workspaceID int64, rec telephony.Record) {
	entry := models.CallLog{
		WorkspaceID:  workspaceID,
		Provider:     models.ProviderSmartflo,
		CallID:       rec.CallID,
		UUID:         rec.UUID,
		Direction:    strings.ToLower(rec.Direction),
		Status:       normalizeCallStatus(rec.Status),
		AgentName:    rec.AgentName,
		AgentNumber:  rec.AgentNumber,
		ClientNumber: telephony.NormalizeNumber(rec.ClientNumber),
		CallerID:     rec.DIDNumber,
		Duration:     int(rec.CallDuration),
		BillSec:      int(rec.AnsweredSeconds),
		RecordingURL: rec.RecordingURL,
		HangupCause:  rec.HangupCause,
	}
	if t := rec.StartedAt(time.UTC); !t.IsZero() {
		entry.StartedAt = &t
	}
	if id, _, ok := s.Store.FindLeadByPhone(workspaceID, rec.ClientNumber); ok {
		entry.LeadID = &id
	}
	if _, err := s.Store.UpsertCallEvent(entry); err != nil {
		slog.Error("call upsert", "err", err)
	}
}

// webhookSmartflo ingests call lifecycle events. Smartflo documents no request
// signature, so the shared secret embedded in the URL identifies the workspace
// and authorizes the delivery; rotate it from the Calls page.
func (s *Server) webhookSmartflo(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.Store.TelephonyAccountBySecret(r.PathValue("secret"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	payload := parseWebhookPayload(r)
	if len(payload) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}

	entry := models.CallLog{
		WorkspaceID:  acct.WorkspaceID,
		Provider:     models.ProviderSmartflo,
		CallID:       pick(payload, "call_id"),
		UUID:         pick(payload, "uuid"),
		RefID:        pick(payload, "custom_identifier", "ref_id"),
		Direction:    strings.ToLower(pick(payload, "direction")),
		Status:       normalizeCallStatus(pick(payload, "call_status", "status")),
		AgentNumber:  pick(payload, "answered_agent_number", "first_missed_agent_number", "agent_number"),
		AgentName:    pick(payload, "answered_agent_name", "agent_name"),
		ClientNumber: telephony.NormalizeNumber(pick(payload, "call_to_number", "customer_number_with_prefix", "client_number")),
		CallerID:     pick(payload, "caller_id_number", "did_number"),
		Duration:     atoiSafe(pick(payload, "duration")),
		BillSec:      atoiSafe(pick(payload, "billsec", "answered_seconds")),
		RecordingURL: pick(payload, "recording_url"),
		HangupCause:  pick(payload, "hangup_cause"),
	}
	entry.StartedAt = parseStamp(pick(payload, "start_stamp"))
	entry.AnsweredAt = parseStamp(pick(payload, "answer_stamp"))
	entry.EndedAt = parseStamp(pick(payload, "end_stamp"))
	if entry.ClientNumber != "" {
		if id, _, found := s.Store.FindLeadByPhone(acct.WorkspaceID, entry.ClientNumber); found {
			entry.LeadID = &id
		}
	}
	if _, err := s.Store.UpsertCallEvent(entry); err != nil {
		slog.Error("smartflo webhook", "err", err)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// parseWebhookPayload accepts every shape Smartflo can be configured to send:
// JSON body, form-encoded body, or GET query string. Keys are lower-cased and
// the "$" template prefix is stripped.
func parseWebhookPayload(r *http.Request) map[string]string {
	out := map[string]string{}
	add := func(k string, v any) {
		k = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(k), "$"))
		if k == "" {
			return
		}
		switch t := v.(type) {
		case string:
			out[k] = strings.TrimSpace(t)
		case float64:
			out[k] = strconv.FormatFloat(t, 'f', -1, 64)
		case bool:
			out[k] = strconv.FormatBool(t)
		case nil:
			// skip
		default:
			if b, err := json.Marshal(t); err == nil {
				out[k] = string(b)
			}
		}
	}
	for k, vs := range r.URL.Query() {
		if len(vs) > 0 {
			add(k, vs[0])
		}
	}
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	switch {
	case strings.Contains(ct, "json"):
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var m map[string]any
		if err := json.Unmarshal(body, &m); err == nil {
			for k, v := range m {
				add(k, v)
			}
		}
	default:
		if err := r.ParseForm(); err == nil {
			for k, vs := range r.PostForm {
				if len(vs) > 0 {
					add(k, vs[0])
				}
			}
		}
	}
	return out
}

func pick(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}

// normalizeCallStatus folds Smartflo's status vocabulary onto the CRM's.
func normalizeCallStatus(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch {
	case s == "":
		return ""
	case strings.Contains(s, "answer"), s == "c", s == "completed":
		return models.CallStatusAnswered
	case strings.Contains(s, "miss"), s == "m", strings.Contains(s, "noanswer"):
		return models.CallStatusMissed
	case strings.Contains(s, "fail"), strings.Contains(s, "busy"), strings.Contains(s, "reject"):
		return models.CallStatusFailed
	default:
		return s
	}
}

// parseStamp accepts the three timestamp formats Smartflo can be configured
// to emit: default "Y-m-d H:i:s", ISO 8601, and epoch seconds.
func parseStamp(v string) *time.Time {
	v = strings.TrimSpace(v)
	if v == "" || v == "0" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			t = t.UTC()
			return &t
		}
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 1_000_000_000 {
		if n > 1_000_000_000_000 { // milliseconds
			n /= 1000
		}
		t := time.Unix(n, 0).UTC()
		return &t
	}
	return nil
}

func atoiSafe(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		if f, ferr := strconv.ParseFloat(strings.TrimSpace(v), 64); ferr == nil {
			return int(f)
		}
		return 0
	}
	return n
}

func ptrTime(t time.Time) *time.Time { return &t }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func randomSecret() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
