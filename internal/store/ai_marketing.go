package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/manishkumar/outreachcrm/internal/models"
)

type WorkspacePulse struct {
	Leads        int `json:"leads"`
	DueQueue     int `json:"due_queue"`
	Unreplied    int `json:"unreplied_inbox"`
	Suppressions int `json:"suppressions"`
}

func (s *Store) WorkspacePulse(workspaceID int64) (WorkspacePulse, error) {
	var p WorkspacePulse
	if workspaceID <= 0 {
		workspaceID = 1
	}
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM leads WHERE workspace_id=?`, workspaceID).Scan(&p.Leads)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM outbound_messages om JOIN campaigns c ON c.id=om.campaign_id
		WHERE om.status='scheduled' AND c.workspace_id=?`, workspaceID).Scan(&p.DueQueue)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM inbound_replies
		WHERE (workspace_id=? OR workspace_id IS NULL) AND COALESCE(hitl_status,'') != 'done'`, workspaceID).Scan(&p.Unreplied)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM suppressions WHERE workspace_id=?`, workspaceID).Scan(&p.Suppressions)
	return p, nil
}

func (s *Store) UpdateLeadStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE leads SET status=?, updated_at=? WHERE id=?`, status, fmtTime(now()), id)
	return err
}

func (s *Store) AppendLeadNote(id int64, note string) error {
	note = strings.TrimSpace(note)
	if note == "" {
		return fmt.Errorf("note is required")
	}
	line := "[" + now().Format("2006-01-02 15:04") + "] " + note
	_, err := s.db.Exec(`UPDATE leads SET notes=TRIM(notes || char(10) || ?), updated_at=? WHERE id=?`, line, fmtTime(now()), id)
	return err
}

func (s *Store) GetReply(id int64) (models.InboundReply, error) {
	row := s.db.QueryRow(`SELECT id, owner_id, workspace_id, lead_id, lead_name, from_email, subject, body, intent, COALESCE(message_id,''), COALESCE(thread_id,''), COALESCE(hitl_status,'auto'), created_at
		FROM inbound_replies WHERE id=?`, id)
	var r models.InboundReply
	var ownerIDN, wsID, leadID sql.NullInt64
	var created string
	err := row.Scan(&r.ID, &ownerIDN, &wsID, &leadID, &r.LeadName, &r.FromEmail, &r.Subject, &r.Body, &r.Intent, &r.MessageID, &r.ThreadID, &r.HITLStatus, &created)
	if err != nil {
		return r, err
	}
	if ownerIDN.Valid {
		id := ownerIDN.Int64
		r.OwnerID = &id
	}
	if wsID.Valid {
		id := wsID.Int64
		r.WorkspaceID = &id
	}
	if leadID.Valid {
		id := leadID.Int64
		r.LeadID = &id
	}
	r.CreatedAt = parseTime(created)
	return r, nil
}

func (s *Store) ListRepliesWS(workspaceID int64, limit int) ([]models.InboundReply, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT id, owner_id, workspace_id, lead_id, lead_name, from_email, subject, body, intent, COALESCE(message_id,''), COALESCE(thread_id,''), COALESCE(hitl_status,'auto'), created_at
		FROM inbound_replies WHERE workspace_id=? OR workspace_id IS NULL ORDER BY id DESC LIMIT ?`, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	return scanReplies(rows)
}

func (s *Store) ListQueueWS(admin bool, ownerID, workspaceID int64, limit int) ([]models.OutboundMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT om.id, om.campaign_id, om.lead_id, om.campaign_lead_id, om.step_order, om.account_id, om.to_email, om.subject, om.body,
		om.status, om.scheduled_at, COALESCE(om.next_attempt_at, om.scheduled_at), om.attempts, om.sent_at, om.error, COALESCE(om.last_error,'')
		FROM outbound_messages om JOIN campaigns c ON c.id=om.campaign_id WHERE om.status IN ('scheduled','sending','failed')`
	var args []any
	if !admin {
		q += ` AND c.owner_id=?`
		args = append(args, ownerID)
	}
	if workspaceID > 0 {
		q += ` AND c.workspace_id=?`
		args = append(args, workspaceID)
	}
	q += ` ORDER BY COALESCE(om.next_attempt_at, om.scheduled_at) ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.OutboundMessage
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) GetWorkspaceAI(workspaceID int64) (models.WorkspaceAI, error) {
	var a models.WorkspaceAI
	var updated string
	err := s.db.QueryRow(`SELECT workspace_id, COALESCE(system_prompt,''), COALESCE(business_prompt,''), COALESCE(openai_key_enc,''),
		COALESCE(openai_base_url,''), COALESCE(openai_model,''), updated_at FROM workspace_ai WHERE workspace_id=?`, workspaceID).
		Scan(&a.WorkspaceID, &a.SystemPrompt, &a.BusinessPrompt, &a.OpenAIKeyEnc, &a.OpenAIBaseURL, &a.OpenAIModel, &updated)
	if err == sql.ErrNoRows {
		a.WorkspaceID = workspaceID
		return a, nil
	}
	if err != nil {
		return a, err
	}
	a.UpdatedAt = parseTime(updated)
	return a, nil
}

func (s *Store) UpsertWorkspaceAI(a models.WorkspaceAI) error {
	_, err := s.db.Exec(`INSERT INTO workspace_ai(workspace_id, system_prompt, business_prompt, openai_key_enc, openai_base_url, openai_model, updated_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(workspace_id) DO UPDATE SET
			system_prompt=excluded.system_prompt,
			business_prompt=excluded.business_prompt,
			openai_key_enc=CASE WHEN excluded.openai_key_enc!='' THEN excluded.openai_key_enc ELSE workspace_ai.openai_key_enc END,
			openai_base_url=excluded.openai_base_url,
			openai_model=excluded.openai_model,
			updated_at=excluded.updated_at`,
		a.WorkspaceID, a.SystemPrompt, a.BusinessPrompt, a.OpenAIKeyEnc, a.OpenAIBaseURL, a.OpenAIModel, fmtTime(now()))
	return err
}

func (s *Store) GetMarketingSMTP(workspaceID int64) (models.MarketingSMTP, error) {
	var m models.MarketingSMTP
	var created, updated string
	var enabled int
	err := s.db.QueryRow(`SELECT workspace_id, COALESCE(provider,'smtp'), COALESCE(host,''), COALESCE(port,587), COALESCE(username,''),
		COALESCE(password_enc,''), COALESCE(api_key_enc,''), COALESCE(from_email,''), COALESCE(from_name,''),
		COALESCE(daily_quota,200), COALESCE(sent_today,0), COALESCE(quota_date,''), COALESCE(enabled,1), created_at, updated_at
		FROM marketing_smtp WHERE workspace_id=?`, workspaceID).
		Scan(&m.WorkspaceID, &m.Provider, &m.Host, &m.Port, &m.Username, &m.PasswordEnc, &m.APIKeyEnc,
			&m.FromEmail, &m.FromName, &m.DailyQuota, &m.SentToday, &m.QuotaDate, &enabled, &created, &updated)
	if err != nil {
		return m, err
	}
	m.Enabled = enabled == 1
	m.CreatedAt = parseTime(created)
	m.UpdatedAt = parseTime(updated)
	today := now().Format("2006-01-02")
	if m.QuotaDate != today {
		m.SentToday = 0
		m.QuotaDate = today
		_, _ = s.db.Exec(`UPDATE marketing_smtp SET sent_today=0, quota_date=? WHERE workspace_id=?`, today, workspaceID)
	}
	return m, nil
}

func (s *Store) UpsertMarketingSMTP(m models.MarketingSMTP) error {
	if m.Port == 0 {
		m.Port = 587
	}
	if m.DailyQuota <= 0 {
		m.DailyQuota = 200
	}
	en := 0
	if m.Enabled {
		en = 1
	}
	t := fmtTime(now())
	_, err := s.db.Exec(`INSERT INTO marketing_smtp(workspace_id, provider, host, port, username, password_enc, api_key_enc,
		from_email, from_name, daily_quota, sent_today, quota_date, enabled, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,0,'',?,?,?)
		ON CONFLICT(workspace_id) DO UPDATE SET
			provider=excluded.provider,
			host=excluded.host,
			port=excluded.port,
			username=excluded.username,
			password_enc=CASE WHEN excluded.password_enc!='' THEN excluded.password_enc ELSE marketing_smtp.password_enc END,
			api_key_enc=CASE WHEN excluded.api_key_enc!='' THEN excluded.api_key_enc ELSE marketing_smtp.api_key_enc END,
			from_email=excluded.from_email,
			from_name=excluded.from_name,
			daily_quota=excluded.daily_quota,
			enabled=excluded.enabled,
			updated_at=excluded.updated_at`,
		m.WorkspaceID, m.Provider, m.Host, m.Port, m.Username, m.PasswordEnc, m.APIKeyEnc,
		m.FromEmail, m.FromName, m.DailyQuota, en, t, t)
	return err
}

func (s *Store) MarkMarketingSent(workspaceID int64) error {
	today := now().Format("2006-01-02")
	_, err := s.db.Exec(`UPDATE marketing_smtp SET sent_today=CASE WHEN quota_date=? THEN sent_today+1 ELSE 1 END, quota_date=?, updated_at=? WHERE workspace_id=?`,
		today, today, fmtTime(now()), workspaceID)
	return err
}

func (s *Store) ListMailboxAccounts() ([]models.EmailAccount, error) {
	return s.ListOAuthAccounts()
}
