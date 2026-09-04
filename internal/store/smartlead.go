package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/manishkumar/outreachcrm/internal/models"
)

func (s *Store) SmartleadLookup(kind, remoteID string) (int64, bool) {
	var id int64
	err := s.db.QueryRow(`SELECT local_id FROM smartlead_map WHERE kind=? AND remote_id=?`, kind, remoteID).Scan(&id)
	if err != nil {
		return 0, false
	}
	return id, true
}

func (s *Store) SmartleadRemember(kind, remoteID string, localID int64) error {
	_, err := s.db.Exec(`INSERT INTO smartlead_map(kind, remote_id, local_id) VALUES(?,?,?)
		ON CONFLICT(kind, remote_id) DO UPDATE SET local_id=excluded.local_id`, kind, remoteID, localID)
	return err
}

func (s *Store) FirstAdminID() (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM users WHERE role=? AND active=1 ORDER BY id LIMIT 1`, models.RoleAdmin).Scan(&id)
	if err == sql.ErrNoRows {
		err = s.db.QueryRow(`SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&id)
	}
	return id, err
}

func (s *Store) CountCampaignSteps(campaignID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sequence_steps WHERE campaign_id=?`, campaignID).Scan(&n)
	return n, err
}

func (s *Store) CountScheduledOutbound(campaignLeadID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM outbound_messages WHERE campaign_lead_id=? AND status IN ('scheduled','sending')`, campaignLeadID).Scan(&n)
	return n, err
}

// ImportCampaignLead enrolls a lead without queueing outbound mail.
func (s *Store) ImportCampaignLead(campaignID, leadID int64, currentStep int, status, enrolledAt string) (int64, error) {
	if status == "" {
		status = "active"
	}
	if currentStep < 0 {
		currentStep = 0
	}
	if enrolledAt == "" {
		enrolledAt = fmtTime(now())
	}
	id, err := s.db.InsertID(`INSERT INTO campaign_leads(campaign_id, lead_id, current_step, status, enrolled_at, next_send_at)
		VALUES(?,?,?,?,?,NULL)
		ON CONFLICT(campaign_id, lead_id) DO UPDATE SET
			current_step=excluded.current_step,
			status=excluded.status,
			next_send_at=NULL`,
		campaignID, leadID, currentStep, status, enrolledAt)
	if err != nil {
		return 0, err
	}
	if id == 0 {
		err = s.db.QueryRow(`SELECT id FROM campaign_leads WHERE campaign_id=? AND lead_id=?`, campaignID, leadID).Scan(&id)
	}
	return id, err
}

func (s *Store) InsertHistoricalOutbound(m models.OutboundMessage) (id int64, inserted bool, err error) {
	status := m.Status
	if status == "" {
		status = "sent"
	}
	if status == "scheduled" || status == "sending" {
		return 0, false, fmt.Errorf("historical outbound cannot be %s", status)
	}
	step := m.StepOrder
	if step <= 0 {
		step = 1
	}
	if m.MessageID != "" {
		var existing int64
		qerr := s.db.QueryRow(`SELECT id FROM outbound_messages WHERE message_id=? AND message_id!='' LIMIT 1`, m.MessageID).Scan(&existing)
		if qerr == nil && existing > 0 {
			s.enrichHistoricalOutbound(existing, m)
			return existing, false, nil
		}
	}
	var existing int64
	err = s.db.QueryRow(`SELECT id FROM outbound_messages WHERE campaign_lead_id=? AND step_order=? LIMIT 1`, m.CampaignLeadID, step).Scan(&existing)
	if err == nil && existing > 0 {
		s.enrichHistoricalOutbound(existing, m)
		return existing, false, nil
	}
	if m.CampaignID > 0 && m.LeadID > 0 {
		err = s.db.QueryRow(`SELECT id FROM outbound_messages WHERE campaign_id=? AND lead_id=? AND step_order=? LIMIT 1`, m.CampaignID, m.LeadID, step).Scan(&existing)
		if err == nil && existing > 0 {
			s.enrichHistoricalOutbound(existing, m)
			return existing, false, nil
		}
	}

	sched := m.ScheduledAt
	if sched.IsZero() {
		sched = now()
	}
	sent := m.SentAt
	if sent == nil {
		t := sched
		sent = &t
	}
	opened, replied := 0, 0
	if m.Opened {
		opened = 1
	}
	if m.Replied {
		replied = 1
	}
	id, err = s.db.InsertID(`INSERT INTO outbound_messages(
		campaign_id, lead_id, campaign_lead_id, step_order, to_email, subject, body, status,
		scheduled_at, next_attempt_at, attempts, sent_at, error, last_error, variant, message_id, opened, replied)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.CampaignID, m.LeadID, m.CampaignLeadID, step, m.ToEmail, m.Subject, m.Body, status,
		fmtTime(sched), fmtTime(sched), 1, fmtTime(*sent), m.Error, m.LastError, defaultVariant(m.Variant), m.MessageID, opened, replied)
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

func (s *Store) enrichHistoricalOutbound(id int64, m models.OutboundMessage) {
	if id <= 0 {
		return
	}
	opened, replied := 0, 0
	if m.Opened {
		opened = 1
	}
	if m.Replied {
		replied = 1
	}
	_, _ = s.db.Exec(`UPDATE outbound_messages SET
		subject=CASE WHEN length(trim(?))>0 AND (subject='' OR subject IS NULL) THEN ? ELSE subject END,
		body=CASE WHEN length(trim(?))>0 AND (body='' OR body IS NULL) THEN ? ELSE body END,
		opened=CASE WHEN ?=1 THEN 1 ELSE opened END,
		replied=CASE WHEN ?=1 THEN 1 ELSE replied END,
		message_id=CASE WHEN message_id='' AND length(trim(?))>0 THEN ? ELSE message_id END
		WHERE id=?`,
		m.Subject, m.Subject, m.Body, m.Body, opened, replied, m.MessageID, m.MessageID, id)
}

func (s *Store) FindLeadByEmailInWorkspace(email string, workspaceID int64) (models.Lead, error) {
	if workspaceID > 0 {
		row := s.db.QueryRow(`SELECT `+leadSelectCols+` FROM leads WHERE lower(email)=lower(?) AND workspace_id=? LIMIT 1`, email, workspaceID)
		l, err := scanLead(row)
		if err == nil && l.ID > 0 {
			return l, nil
		}
	}
	return s.FindLeadByEmail(email)
}

func (s *Store) ListSentHistory(admin bool, ownerID, workspaceID int64, limit int) ([]models.OutboundMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT om.id, om.campaign_id, om.lead_id, om.campaign_lead_id, om.step_order, om.account_id, om.to_email, om.subject, om.body,
		om.status, om.scheduled_at, COALESCE(om.next_attempt_at, om.scheduled_at), om.attempts, om.sent_at, om.error, COALESCE(om.last_error,'')
		FROM outbound_messages om JOIN campaigns c ON c.id=om.campaign_id
		WHERE om.status IN ('sent','dead')`
	var args []any
	if !admin {
		q += ` AND c.owner_id=?`
		args = append(args, ownerID)
	}
	if workspaceID > 0 {
		q += ` AND c.workspace_id=?`
		args = append(args, workspaceID)
	}
	q += ` ORDER BY COALESCE(om.sent_at, om.scheduled_at) DESC LIMIT ?`
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

func (s *Store) ListLeadMail(leadID int64, limit int) ([]models.LeadMailItem, error) {
	if limit <= 0 {
		limit = 40
	}
	var items []models.LeadMailItem
	rows, err := s.db.Query(`SELECT id, subject, body, status, COALESCE(sent_at, scheduled_at)
		FROM outbound_messages WHERE lead_id=? ORDER BY id DESC LIMIT ?`, leadID, limit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var it models.LeadMailItem
		var at string
		if err := rows.Scan(&it.ID, &it.Subject, &it.Body, &it.Status, &at); err != nil {
			rows.Close()
			return nil, err
		}
		it.Kind = "out"
		it.At = parseTime(at)
		if it.At.IsZero() {
			it.At = parseFlexibleTime(at)
		}
		items = append(items, it)
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT id, subject, body, intent, created_at
		FROM inbound_replies WHERE lead_id=? ORDER BY id DESC LIMIT ?`, leadID, limit)
	if err != nil {
		return items, err
	}
	defer rows.Close()
	for rows.Next() {
		var it models.LeadMailItem
		var at string
		if err := rows.Scan(&it.ID, &it.Subject, &it.Body, &it.Status, &at); err != nil {
			return nil, err
		}
		it.Kind = "in"
		it.At = parseTime(at)
		if it.At.IsZero() {
			it.At = parseFlexibleTime(at)
		}
		items = append(items, it)
	}
	// newest first
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].At.After(items[i].At) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, rows.Err()
}

func (s *Store) WALCheckpointPassive() {
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`)
}

func (s *Store) WALCheckpointTruncate() {
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_, _ = s.db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`)
	}
}

func (s *Store) SetBusyTimeout(ms int) {
	if ms < 1000 {
		ms = 1000
	}
	_, _ = s.db.Exec(fmt.Sprintf("PRAGMA busy_timeout=%d", ms))
}

func defaultVariant(v string) string {
	if v == "" {
		return "a"
	}
	return v
}

func parseFlexibleTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// ParseImportTime is used by the Smartlead importer for sent_at strings.
func ParseImportTime(s string) time.Time {
	return parseFlexibleTime(s)
}
