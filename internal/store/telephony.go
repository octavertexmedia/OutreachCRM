package store

import (
	"database/sql"
	"strings"
	"time"

	"github.com/manishkumar/outreachcrm/internal/models"
	"github.com/manishkumar/outreachcrm/internal/telephony"
)

// GetTelephonyAccount returns the workspace's Smartflo connection. A missing
// row is not an error — it means "not connected yet".
func (s *Store) GetTelephonyAccount(workspaceID int64) (models.TelephonyAccount, error) {
	if workspaceID == 0 {
		workspaceID = 1
	}
	var a models.TelephonyAccount
	var enabled int
	var verified, created, updated sql.NullString
	err := s.db.QueryRow(`
SELECT workspace_id, provider, base_url, login_email, password_enc, token_enc, auth_scheme,
       agent_number, caller_id, call_timeout, webhook_secret, enabled, last_error,
       last_verified_at, created_at, updated_at
FROM telephony_accounts WHERE workspace_id = ?`, workspaceID).Scan(
		&a.WorkspaceID, &a.Provider, &a.BaseURL, &a.LoginEmail, &a.PasswordEnc, &a.TokenEnc, &a.AuthScheme,
		&a.AgentNumber, &a.CallerID, &a.CallTimeout, &a.WebhookSecret, &enabled, &a.LastError,
		&verified, &created, &updated)
	if err == sql.ErrNoRows {
		return models.TelephonyAccount{WorkspaceID: workspaceID, Provider: models.ProviderSmartflo}, nil
	}
	if err != nil {
		return a, err
	}
	a.Enabled = enabled == 1
	a.LastVerified = parseTimePtr(verified)
	if created.Valid {
		a.CreatedAt = parseTime(created.String)
	}
	if updated.Valid {
		a.UpdatedAt = parseTime(updated.String)
	}
	return a, nil
}

// SaveTelephonyAccount upserts the connection for a workspace.
func (s *Store) SaveTelephonyAccount(a models.TelephonyAccount) error {
	if a.WorkspaceID == 0 {
		a.WorkspaceID = 1
	}
	if a.Provider == "" {
		a.Provider = models.ProviderSmartflo
	}
	ts := fmtTime(now())
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	_, err := s.db.Exec(`
INSERT INTO telephony_accounts
  (workspace_id, provider, base_url, login_email, password_enc, token_enc, auth_scheme,
   agent_number, caller_id, call_timeout, webhook_secret, enabled, last_error, last_verified_at,
   created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(workspace_id) DO UPDATE SET
  provider=excluded.provider, base_url=excluded.base_url, login_email=excluded.login_email,
  password_enc=excluded.password_enc, token_enc=excluded.token_enc, auth_scheme=excluded.auth_scheme,
  agent_number=excluded.agent_number, caller_id=excluded.caller_id, call_timeout=excluded.call_timeout,
  webhook_secret=excluded.webhook_secret, enabled=excluded.enabled, updated_at=excluded.updated_at`,
		a.WorkspaceID, a.Provider, a.BaseURL, a.LoginEmail, a.PasswordEnc, a.TokenEnc, a.AuthScheme,
		a.AgentNumber, a.CallerID, a.CallTimeout, a.WebhookSecret, enabled, a.LastError,
		nullTimePtr(a.LastVerified), ts, ts)
	return err
}

// MarkTelephonyVerified records the outcome of a credential test so operators
// can see whether the last token handshake worked.
func (s *Store) MarkTelephonyVerified(workspaceID int64, errMsg string) {
	ts := fmtTime(now())
	if errMsg == "" {
		_, _ = s.db.Exec(`UPDATE telephony_accounts SET last_error='', last_verified_at=?, updated_at=? WHERE workspace_id=?`, ts, ts, workspaceID)
		return
	}
	_, _ = s.db.Exec(`UPDATE telephony_accounts SET last_error=?, updated_at=? WHERE workspace_id=?`, errMsg, ts, workspaceID)
}

// TelephonyAccountBySecret resolves an inbound webhook to its workspace.
func (s *Store) TelephonyAccountBySecret(secret string) (models.TelephonyAccount, bool) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return models.TelephonyAccount{}, false
	}
	var ws int64
	if err := s.db.QueryRow(`SELECT workspace_id FROM telephony_accounts WHERE webhook_secret = ?`, secret).Scan(&ws); err != nil {
		return models.TelephonyAccount{}, false
	}
	a, err := s.GetTelephonyAccount(ws)
	if err != nil {
		return models.TelephonyAccount{}, false
	}
	return a, true
}

// FindLeadByPhone matches a caller/destination number to a lead in the
// workspace on the last 10 digits, which survives +91 / 0 / spacing variants.
func (s *Store) FindLeadByPhone(workspaceID int64, phone string) (int64, string, bool) {
	tail := telephony.Last10(phone)
	if len(tail) < 7 {
		return 0, "", false
	}
	var id int64
	var name string
	err := s.db.QueryRow(`
SELECT id, name FROM leads
WHERE workspace_id = ? AND phone != ''
  AND replace(replace(replace(replace(replace(replace(phone,' ',''),'-',''),'(',''),')',''),'+',''),'.','') LIKE ?
ORDER BY updated_at DESC LIMIT 1`, workspaceID, "%"+tail).Scan(&id, &name)
	if err != nil {
		return 0, "", false
	}
	return id, name, true
}

// CreateCallLog records a call the CRM initiated.
func (s *Store) CreateCallLog(c models.CallLog) (int64, error) {
	if c.WorkspaceID == 0 {
		c.WorkspaceID = 1
	}
	if c.Provider == "" {
		c.Provider = models.ProviderSmartflo
	}
	ts := fmtTime(now())
	return s.db.InsertID(`
INSERT INTO call_logs
  (workspace_id, lead_id, user_id, provider, call_id, uuid, ref_id, direction, status,
   agent_number, agent_name, client_number, caller_id, duration, billsec, recording_url,
   hangup_cause, notes, started_at, answered_at, ended_at, created_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.WorkspaceID, nullInt64Ptr(c.LeadID), c.UserID, c.Provider, c.CallID, c.UUID, c.RefID,
		c.Direction, c.Status, c.AgentNumber, c.AgentName, c.ClientNumber, c.CallerID,
		c.Duration, c.BillSec, c.RecordingURL, c.HangupCause, c.Notes,
		nullTimePtr(c.StartedAt), nullTimePtr(c.AnsweredAt), nullTimePtr(c.EndedAt), ts, ts)
}

// UpsertCallEvent merges a webhook event or CDR row onto an existing call.
// Matching walks ref_id (the identifier we sent on click-to-call) → uuid →
// call_id so both event sources land on one row instead of duplicating it.
func (s *Store) UpsertCallEvent(c models.CallLog) (int64, error) {
	if c.WorkspaceID == 0 {
		c.WorkspaceID = 1
	}
	if c.Provider == "" {
		c.Provider = models.ProviderSmartflo
	}
	var id int64
	find := func(col, val string) bool {
		if strings.TrimSpace(val) == "" {
			return false
		}
		err := s.db.QueryRow(
			`SELECT id FROM call_logs WHERE workspace_id=? AND provider=? AND `+col+`=? ORDER BY id DESC LIMIT 1`,
			c.WorkspaceID, c.Provider, val).Scan(&id)
		return err == nil && id > 0
	}
	if !find("ref_id", c.RefID) && !find("uuid", c.UUID) && !find("call_id", c.CallID) {
		return s.CreateCallLog(c)
	}

	// COALESCE(NULLIF(?,''), col) keeps whatever the earlier event already
	// knew when a later payload omits the field.
	_, err := s.db.Exec(`
UPDATE call_logs SET
  lead_id       = COALESCE(?, lead_id),
  call_id       = COALESCE(NULLIF(?,''), call_id),
  uuid          = COALESCE(NULLIF(?,''), uuid),
  ref_id        = COALESCE(NULLIF(?,''), ref_id),
  direction     = COALESCE(NULLIF(?,''), direction),
  status        = COALESCE(NULLIF(?,''), status),
  agent_number  = COALESCE(NULLIF(?,''), agent_number),
  agent_name    = COALESCE(NULLIF(?,''), agent_name),
  client_number = COALESCE(NULLIF(?,''), client_number),
  caller_id     = COALESCE(NULLIF(?,''), caller_id),
  duration      = CASE WHEN ? > 0 THEN ? ELSE duration END,
  billsec       = CASE WHEN ? > 0 THEN ? ELSE billsec END,
  recording_url = COALESCE(NULLIF(?,''), recording_url),
  hangup_cause  = COALESCE(NULLIF(?,''), hangup_cause),
  started_at    = COALESCE(started_at, ?),
  answered_at   = COALESCE(?, answered_at),
  ended_at      = COALESCE(?, ended_at),
  updated_at    = ?
WHERE id = ?`,
		nullInt64Ptr(c.LeadID), c.CallID, c.UUID, c.RefID, c.Direction, c.Status,
		c.AgentNumber, c.AgentName, c.ClientNumber, c.CallerID,
		c.Duration, c.Duration, c.BillSec, c.BillSec,
		c.RecordingURL, c.HangupCause,
		nullTimePtr(c.StartedAt), nullTimePtr(c.AnsweredAt), nullTimePtr(c.EndedAt),
		fmtTime(now()), id)
	return id, err
}

const callLogCols = `c.id, c.workspace_id, c.lead_id, c.user_id, c.provider, c.call_id, c.uuid, c.ref_id,
 c.direction, c.status, c.agent_number, c.agent_name, c.client_number, c.caller_id, c.duration,
 c.billsec, c.recording_url, c.hangup_cause, c.notes, c.started_at, c.answered_at, c.ended_at,
 c.created_at, c.updated_at, COALESCE(l.name,'')`

func scanCallLog(sc scannable) (models.CallLog, error) {
	var c models.CallLog
	var leadID sql.NullInt64
	var started, answered, ended sql.NullString
	var created, updated string
	err := sc.Scan(&c.ID, &c.WorkspaceID, &leadID, &c.UserID, &c.Provider, &c.CallID, &c.UUID, &c.RefID,
		&c.Direction, &c.Status, &c.AgentNumber, &c.AgentName, &c.ClientNumber, &c.CallerID, &c.Duration,
		&c.BillSec, &c.RecordingURL, &c.HangupCause, &c.Notes, &started, &answered, &ended,
		&created, &updated, &c.LeadName)
	if err != nil {
		return c, err
	}
	if leadID.Valid {
		v := leadID.Int64
		c.LeadID = &v
	}
	c.StartedAt = parseTimePtr(started)
	c.AnsweredAt = parseTimePtr(answered)
	c.EndedAt = parseTimePtr(ended)
	c.CreatedAt = parseTime(created)
	c.UpdatedAt = parseTime(updated)
	return c, nil
}

// ListCallLogs returns the workspace's most recent calls.
func (s *Store) ListCallLogs(workspaceID int64, limit int) ([]models.CallLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`
SELECT `+callLogCols+`
FROM call_logs c LEFT JOIN leads l ON l.id = c.lead_id
WHERE c.workspace_id = ?
ORDER BY COALESCE(c.started_at, c.created_at) DESC, c.id DESC
LIMIT ?`, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.CallLog
	for rows.Next() {
		c, err := scanCallLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListCallLogsForLead powers the call history in the lead inspector.
func (s *Store) ListCallLogsForLead(leadID int64, limit int) []models.CallLog {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.Query(`
SELECT `+callLogCols+`
FROM call_logs c LEFT JOIN leads l ON l.id = c.lead_id
WHERE c.lead_id = ?
ORDER BY COALESCE(c.started_at, c.created_at) DESC, c.id DESC
LIMIT ?`, leadID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []models.CallLog
	for rows.Next() {
		c, err := scanCallLog(rows)
		if err != nil {
			return out
		}
		out = append(out, c)
	}
	return out
}

// CallStatsFor aggregates the workspace's call activity.
func (s *Store) CallStatsFor(workspaceID int64) models.CallStats {
	var st models.CallStats
	today := now().Format("2006-01-02")
	_ = s.db.QueryRow(`
SELECT
  COUNT(*),
  COALESCE(SUM(CASE WHEN status = 'answered' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN status = 'missed' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN substr(COALESCE(started_at, created_at),1,10) = ? THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(billsec), 0)
FROM call_logs WHERE workspace_id = ?`, today, workspaceID).Scan(
		&st.Total, &st.Answered, &st.Missed, &st.Today, &st.TalkTime)
	st.Connected = st.Answered
	return st
}

// PruneCallLogs drops call rows older than the PII retention window.
func (s *Store) PruneCallLogs(days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := fmtTime(now().AddDate(0, 0, -days))
	res, err := s.db.Exec(`DELETE FROM call_logs WHERE COALESCE(started_at, created_at) < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func nullTimePtr(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return fmtTime(*t)
}

func nullInt64Ptr(v *int64) any {
	if v == nil || *v == 0 {
		return nil
	}
	return *v
}
