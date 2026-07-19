package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/manishkumar/outreachcrm/internal/models"
)

func (s *Store) Audit(workspaceID, userID int64, action, entity, entityID, meta string) {
	_, _ = s.db.Exec(`INSERT INTO audit_logs(workspace_id, user_id, action, entity, entity_id, meta, created_at) VALUES(?,?,?,?,?,?,?)`,
		workspaceID, userID, action, entity, entityID, meta, fmtTime(now()))
}

func (s *Store) ListAudit(workspaceID int64, limit int) ([]models.AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT a.id, a.workspace_id, a.user_id, COALESCE(u.email,''), a.action, a.entity, a.entity_id, a.meta, a.created_at
		FROM audit_logs a LEFT JOIN users u ON u.id=a.user_id
		WHERE a.workspace_id=? ORDER BY a.id DESC LIMIT ?`, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.AuditEntry
	for rows.Next() {
		var e models.AuditEntry
		var created string
		if err := rows.Scan(&e.ID, &e.WorkspaceID, &e.UserID, &e.UserEmail, &e.Action, &e.Entity, &e.EntityID, &e.Meta, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = parseTime(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) EnsureWorkspace(name string) (int64, error) {
	return s.EnsureNamedWorkspace(name)
}

func (s *Store) ListWorkspaces() ([]models.Workspace, error) {
	rows, err := s.db.Query(`SELECT id, name, created_at FROM workspaces ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Workspace
	for rows.Next() {
		var w models.Workspace
		var created string
		if err := rows.Scan(&w.ID, &w.Name, &created); err != nil {
			return nil, err
		}
		w.CreatedAt = parseTime(created)
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) CreateWorkspace(name string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO workspaces(name, created_at) VALUES(?,?)`, name, fmtTime(now()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) GetSetting(key, fallback string) string {
	var v string
	err := s.db.QueryRow(`SELECT value FROM app_settings WHERE key=?`, key).Scan(&v)
	if err != nil || v == "" {
		return fallback
	}
	return v
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO app_settings(key, value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) SetUserTOTP(id int64, secretEnc string, enabled bool) error {
	en := 0
	if enabled {
		en = 1
	}
	_, err := s.db.Exec(`UPDATE users SET totp_secret_enc=?, totp_enabled=? WHERE id=?`, secretEnc, en, id)
	return err
}

func (s *Store) SaveDomainCheck(dc models.DomainCheck) error {
	spf, dkim, dmarc := 0, 0, 0
	if dc.SPF {
		spf = 1
	}
	if dc.DKIM {
		dkim = 1
	}
	if dc.DMARC {
		dmarc = 1
	}
	_, err := s.db.Exec(`INSERT INTO domain_checks(domain, spf, dkim, dmarc, detail, checked_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(domain) DO UPDATE SET spf=excluded.spf, dkim=excluded.dkim, dmarc=excluded.dmarc, detail=excluded.detail, checked_at=excluded.checked_at`,
		dc.Domain, spf, dkim, dmarc, dc.Detail, fmtTime(dc.CheckedAt))
	return err
}

func (s *Store) ListDomainChecks() ([]models.DomainCheck, error) {
	rows, err := s.db.Query(`SELECT domain, spf, dkim, dmarc, detail, checked_at FROM domain_checks ORDER BY checked_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.DomainCheck
	for rows.Next() {
		var dc models.DomainCheck
		var spf, dkim, dmarc int
		var checked string
		if err := rows.Scan(&dc.Domain, &spf, &dkim, &dmarc, &dc.Detail, &checked); err != nil {
			return nil, err
		}
		dc.SPF, dc.DKIM, dc.DMARC = spf == 1, dkim == 1, dmarc == 1
		dc.CheckedAt = parseTime(checked)
		out = append(out, dc)
	}
	return out, rows.Err()
}

func (s *Store) ListTemplates(workspaceID int64) ([]models.EmailTemplate, error) {
	rows, err := s.db.Query(`SELECT id, workspace_id, name, subject, body, created_at FROM email_templates WHERE workspace_id=? ORDER BY id DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.EmailTemplate
	for rows.Next() {
		var t models.EmailTemplate
		var created string
		if err := rows.Scan(&t.ID, &t.WorkspaceID, &t.Name, &t.Subject, &t.Body, &created); err != nil {
			return nil, err
		}
		t.CreatedAt = parseTime(created)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) CreateTemplate(t models.EmailTemplate) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO email_templates(workspace_id, name, subject, body, created_at) VALUES(?,?,?,?,?)`,
		t.WorkspaceID, t.Name, t.Subject, t.Body, fmtTime(now()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) RecordLLMUsage(workspaceID, userID int64, feature string, tokens, costCents int) error {
	_, err := s.db.Exec(`INSERT INTO llm_usage(workspace_id, user_id, feature, tokens, cost_cents, created_at) VALUES(?,?,?,?,?,?)`,
		workspaceID, userID, feature, tokens, costCents, fmtTime(now()))
	return err
}

func (s *Store) LLMSpendTodayCents(workspaceID int64) (int, error) {
	today := now().Format("2006-01-02")
	var n int
	err := s.db.QueryRow(`SELECT COALESCE(SUM(cost_cents),0) FROM llm_usage WHERE workspace_id=? AND created_at LIKE ?`, workspaceID, today+"%").Scan(&n)
	return n, err
}

func (s *Store) ClaimDueMessages(limit int, owner string, lease time.Duration) ([]models.OutboundMessage, error) {
	until := fmtTime(now().Add(lease))
	msgs, err := s.ListDueMessages(limit * 2)
	if err != nil {
		return nil, err
	}
	var claimed []models.OutboundMessage
	for _, m := range msgs {
		if len(claimed) >= limit {
			break
		}
		res, err := s.db.Exec(`UPDATE outbound_messages SET locked_until=?, lock_owner=?
			WHERE id=? AND status='scheduled' AND (locked_until IS NULL OR locked_until='' OR locked_until < ? OR lock_owner=?)`,
			until, owner, m.ID, fmtTime(now()), owner)
		if err != nil {
			continue
		}
		n, _ := res.RowsAffected()
		if n == 1 {
			claimed = append(claimed, m)
		}
	}
	return claimed, nil
}

func (s *Store) CountDomainSentToday(domain string) (int, error) {
	today := now().Format("2006-01-02")
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM outbound_messages om
		JOIN email_accounts ea ON ea.id=om.account_id
		WHERE om.status='sent' AND om.sent_at LIKE ? AND lower(ea.domain)=lower(?)`, today+"%", domain).Scan(&n)
	return n, err
}

func (s *Store) InWindow(camp models.Campaign, t time.Time) bool {
	loc, err := time.LoadLocation(camp.Timezone)
	if err != nil || camp.Timezone == "" {
		loc = time.UTC
	}
	local := t.In(loc)
	h := local.Hour()
	start, end := camp.SendWindowStart, camp.SendWindowEnd
	if start == end {
		return true
	}
	if start < end {
		return h >= start && h < end
	}
	return h >= start || h < end
}

func (s *Store) Analytics(workspaceID int64) (models.Analytics, error) {
	var a models.Analytics
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM outbound_messages om JOIN campaigns c ON c.id=om.campaign_id WHERE om.status='sent' AND c.workspace_id=?`, workspaceID).Scan(&a.Sent)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM outbound_messages om JOIN campaigns c ON c.id=om.campaign_id WHERE om.status='failed' AND c.workspace_id=?`, workspaceID).Scan(&a.Failed)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM outbound_messages om JOIN campaigns c ON c.id=om.campaign_id WHERE om.status='dead' AND c.workspace_id=?`, workspaceID).Scan(&a.Dead)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM inbound_replies WHERE intent='positive' AND (workspace_id=? OR workspace_id IS NULL)`, workspaceID).Scan(&a.Positive)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM suppressions WHERE reason='unsubscribe' AND workspace_id=?`, workspaceID).Scan(&a.Unsub)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM inbound_replies WHERE hitl_status='needs_review' AND (workspace_id=? OR workspace_id IS NULL)`, workspaceID).Scan(&a.OpenHITL)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM outbound_messages om JOIN campaigns c ON c.id=om.campaign_id WHERE om.variant='a' AND om.status='sent' AND c.workspace_id=?`, workspaceID).Scan(&a.ByVariantA)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM outbound_messages om JOIN campaigns c ON c.id=om.campaign_id WHERE om.variant='b' AND om.status='sent' AND c.workspace_id=?`, workspaceID).Scan(&a.ByVariantB)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM leads WHERE workspace_id=? AND enrichment_status='done'`, workspaceID).Scan(&a.Enriched)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM leads WHERE workspace_id=? AND draft_subject!=''`, workspaceID).Scan(&a.WithDraft)
	_ = s.db.QueryRow(`SELECT COUNT(DISTINCT cl.lead_id) FROM campaign_leads cl JOIN campaigns c ON c.id=cl.campaign_id WHERE c.workspace_id=?`, workspaceID).Scan(&a.Enrolled)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM outbound_messages om JOIN campaigns c ON c.id=om.campaign_id WHERE om.status='scheduled' AND c.workspace_id=?`, workspaceID).Scan(&a.Queued)
	if a.Sent > 0 {
		a.ReplyRate = float64(a.Positive) * 100 / float64(a.Sent)
		a.UnsubRate = float64(a.Unsub) * 100 / float64(a.Sent)
	}
	return a, nil
}

func (s *Store) ExportLeadJSON(workspaceID int64) (string, error) {
	leads, err := s.ListLeads(true, 0)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("[")
	first := true
	for _, l := range leads {
		if l.WorkspaceID != 0 && l.WorkspaceID != workspaceID && workspaceID > 0 {
			continue
		}
		if !first {
			b.WriteString(",")
		}
		first = false
		fmt.Fprintf(&b, `{"id":%d,"name":%q,"email":%q,"website":%q,"phone":%q,"category":%q,"consent_source":%q}`,
			l.ID, l.Name, l.Email, l.Website, l.Phone, l.Category, l.ConsentSource)
	}
	b.WriteString("]")
	return b.String(), nil
}

func (s *Store) DeleteLeadPII(id int64) error {
	_, err := s.db.Exec(`UPDATE leads SET name='[redacted]', email='', phone='', website='', notes='', issues_json='[]' WHERE id=?`, id)
	return err
}

func (s *Store) PurgeOldPII(days int) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := now().AddDate(0, 0, -days)
	res, err := s.db.Exec(`UPDATE leads SET name='[purged]', email='', phone='', website='', notes='' WHERE updated_at < ? AND email != ''`, fmtTime(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) SetReplyHITL(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE inbound_replies SET hitl_status=? WHERE id=?`, status, id)
	return err
}

func (s *Store) ListHITL(workspaceID int64) ([]models.InboundReply, error) {
	rows, err := s.db.Query(`SELECT id, owner_id, workspace_id, lead_id, lead_name, from_email, subject, body, intent, COALESCE(message_id,''), COALESCE(thread_id,''), COALESCE(hitl_status,'auto'), created_at
		FROM inbound_replies WHERE hitl_status='needs_review' AND (workspace_id=? OR workspace_id IS NULL) ORDER BY id DESC LIMIT 100`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReplies(rows)
}

func scanReplies(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}) ([]models.InboundReply, error) {
	defer rows.Close()
	var out []models.InboundReply
	for rows.Next() {
		var r models.InboundReply
		var ownerIDN, wsID, leadID sql.NullInt64
		var created string
		if err := rows.Scan(&r.ID, &ownerIDN, &wsID, &leadID, &r.LeadName, &r.FromEmail, &r.Subject, &r.Body, &r.Intent, &r.MessageID, &r.ThreadID, &r.HITLStatus, &created); err != nil {
			return nil, err
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
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) BumpWarmupDays() error {
	_, err := s.db.Exec(`UPDATE email_accounts SET warmup_day = warmup_day + 1 WHERE warmup_enabled=1`)
	return err
}

func (s *Store) AddSuppressionWS(workspaceID int64, email, reason string) error {
	_, err := s.db.Exec(`INSERT INTO suppressions(email, reason, created_at, workspace_id) VALUES(?,?,?,?)
		ON CONFLICT(email) DO UPDATE SET reason=excluded.reason, workspace_id=excluded.workspace_id`,
		email, reason, fmtTime(now()), workspaceID)
	return err
}
