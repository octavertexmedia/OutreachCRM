package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/manishkumar/outreachcrm/internal/models"
)

func (s *Store) Stats(admin bool, ownerID int64) (models.DashboardStats, error) {
	var st models.DashboardStats
	count := func(table, extra string, args ...any) int {
		var n int
		q := `SELECT COUNT(*) FROM ` + table + ` WHERE 1=1` + extra
		_ = s.db.QueryRow(q, args...).Scan(&n)
		return n
	}
	if admin {
		st.Leads = count("leads", "")
		st.Premium = count("leads", " AND premium_score>=70")
		st.Campaigns = count("campaigns", "")
		st.Accounts = count("email_accounts", "")
		st.Scheduled = count("outbound_messages", " AND status='scheduled'")
		st.Positive = count("inbound_replies", " AND intent='positive'")
		st.Dead = count("outbound_messages", " AND status='dead'")
		st.HITLOpen = count("inbound_replies", " AND hitl_status='needs_review'")
		st.Bounces = count("suppressions", " AND reason IN ('bounce','complaint')")
		return st, nil
	}
	st.Leads = count("leads", " AND owner_id=?", ownerID)
	st.Premium = count("leads", " AND premium_score>=70 AND owner_id=?", ownerID)
	st.Campaigns = count("campaigns", " AND owner_id=?", ownerID)
	st.Accounts = count("email_accounts", " AND owner_id=?", ownerID)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM outbound_messages om JOIN campaigns c ON c.id=om.campaign_id WHERE om.status='scheduled' AND c.owner_id=?`, ownerID).Scan(&st.Scheduled)
	st.Positive = count("inbound_replies", " AND intent='positive' AND (owner_id=? OR owner_id IS NULL)", ownerID)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM outbound_messages om JOIN campaigns c ON c.id=om.campaign_id WHERE om.status='dead' AND c.owner_id=?`, ownerID).Scan(&st.Dead)
	return st, nil
}

func (s *Store) ListAccounts(admin bool, ownerID int64) ([]models.EmailAccount, error) {
	q := accountSelectSQL + ` FROM email_accounts WHERE 1=1`
	var args []any
	if !admin {
		q += ` AND owner_id=?`
		args = append(args, ownerID)
	}
	q += ` ORDER BY id`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.EmailAccount
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

const accountSelectSQL = `SELECT id, COALESCE(owner_id,0), COALESCE(workspace_id,1), email, COALESCE(provider,'smtp'), smtp_host, smtp_port, username,
		COALESCE(password_enc,''), COALESCE(access_token_enc,''), COALESCE(refresh_token_enc,''), token_expiry,
		COALESCE(imap_host,''), COALESCE(imap_port,993), COALESCE(imap_last_uid,0),
		daily_quota, sent_today, quota_date, last_sent_at,
		COALESCE(domain,''), COALESCE(domain_daily_limit,0), COALESCE(warmup_day,0), COALESCE(warmup_enabled,0), COALESCE(esp_api_key_enc,''), created_at`

func scanAccount(row scannable) (models.EmailAccount, error) {
	var a models.EmailAccount
	var tokenExp, last, created sql.NullString
	var imapUID int64
	var warm int
	err := row.Scan(&a.ID, &a.OwnerID, &a.WorkspaceID, &a.Email, &a.Provider, &a.SMTPHost, &a.SMTPPort, &a.Username,
		&a.PasswordEnc, &a.AccessTokenEnc, &a.RefreshTokenEnc, &tokenExp,
		&a.IMAPHost, &a.IMAPPort, &imapUID,
		&a.DailyQuota, &a.SentToday, &a.QuotaDate, &last,
		&a.Domain, &a.DomainDailyLimit, &a.WarmupDay, &warm, &a.ESPAPIKeyEnc, &created)
	if err != nil {
		return a, err
	}
	a.WarmupEnabled = warm == 1
	a.IMAPLastUID = uint32(imapUID)
	a.TokenExpiry = parseTimePtr(tokenExp)
	a.LastSentAt = parseTimePtr(last)
	a.CreatedAt = parseTime(created.String)
	return a, nil
}

func (s *Store) GetAccount(id int64) (models.EmailAccount, error) {
	row := s.db.QueryRow(accountSelectSQL+` FROM email_accounts WHERE id=?`, id)
	return scanAccount(row)
}

func (s *Store) CreateAccount(a models.EmailAccount) (int64, error) {
	if a.Provider == "" {
		a.Provider = models.ProviderSMTP
	}
	if a.IMAPPort == 0 {
		a.IMAPPort = 993
	}
	if a.WorkspaceID == 0 {
		a.WorkspaceID = 1
	}
	warm := 0
	if a.WarmupEnabled {
		warm = 1
	}
	res, err := s.db.Exec(`INSERT INTO email_accounts(owner_id, workspace_id, email, provider, smtp_host, smtp_port, username, password_enc,
		access_token_enc, refresh_token_enc, token_expiry, imap_host, imap_port, imap_last_uid, daily_quota, sent_today, quota_date,
		domain, domain_daily_limit, warmup_day, warmup_enabled, esp_api_key_enc, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,0,?,0,'',?,?,0,?,?,?)`,
		a.OwnerID, a.WorkspaceID, a.Email, a.Provider, a.SMTPHost, a.SMTPPort, a.Username, a.PasswordEnc,
		a.AccessTokenEnc, a.RefreshTokenEnc, nilStr(a.TokenExpiry), a.IMAPHost, a.IMAPPort, a.DailyQuota,
		a.Domain, a.DomainDailyLimit, warm, a.ESPAPIKeyEnc, fmtTime(now()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func nilStr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}

func (s *Store) UpdateAccountTokens(id int64, accessEnc, refreshEnc string, expiry *time.Time) error {
	_, err := s.db.Exec(`UPDATE email_accounts SET access_token_enc=?, refresh_token_enc=?, token_expiry=? WHERE id=?`,
		accessEnc, refreshEnc, nilStr(expiry), id)
	return err
}

func (s *Store) SetIMAPLastUID(id int64, uid uint32) error {
	_, err := s.db.Exec(`UPDATE email_accounts SET imap_last_uid=? WHERE id=?`, uid, id)
	return err
}

func (s *Store) ListOAuthAccounts() ([]models.EmailAccount, error) {
	rows, err := s.db.Query(accountSelectSQL + ` FROM email_accounts
		WHERE provider IN ('google','microsoft') OR (imap_host != '' AND password_enc != '')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.EmailAccount
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) PickAccount(ownerID int64, admin bool) (models.EmailAccount, error) {
	today := now().Format("2006-01-02")
	_, _ = s.db.Exec(`UPDATE email_accounts SET sent_today=0, quota_date=? WHERE quota_date != ? OR quota_date=''`, today, today)

	q := accountSelectSQL + ` FROM email_accounts WHERE sent_today < daily_quota`
	var args []any
	if !admin && ownerID > 0 {
		q += ` AND owner_id=?`
		args = append(args, ownerID)
	}
	q += ` ORDER BY CASE WHEN last_sent_at IS NULL OR last_sent_at='' THEN 0 ELSE 1 END, last_sent_at ASC, id ASC LIMIT 1`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return models.EmailAccount{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return models.EmailAccount{}, sql.ErrNoRows
	}
	return scanAccount(rows)
}

func (s *Store) MarkAccountSent(id int64) error {
	t := fmtTime(now())
	today := now().Format("2006-01-02")
	_, err := s.db.Exec(`UPDATE email_accounts SET sent_today=sent_today+1, quota_date=?, last_sent_at=? WHERE id=?`,
		today, t, id)
	return err
}

func (s *Store) ListDueMessages(limit int) ([]models.OutboundMessage, error) {
	rows, err := s.db.Query(`SELECT id, campaign_id, lead_id, campaign_lead_id, step_order, account_id, to_email, subject, body,
		status, scheduled_at, COALESCE(next_attempt_at, scheduled_at), attempts, sent_at, error, COALESCE(last_error,'')
		FROM outbound_messages
		WHERE status='scheduled' AND COALESCE(next_attempt_at, scheduled_at) <= ?
		ORDER BY COALESCE(next_attempt_at, scheduled_at) ASC LIMIT ?`, fmtTime(now()), limit)
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

func scanMessage(row scannable) (models.OutboundMessage, error) {
	var m models.OutboundMessage
	var accountID sql.NullInt64
	var scheduled, nextAttempt, sent sql.NullString
	err := row.Scan(&m.ID, &m.CampaignID, &m.LeadID, &m.CampaignLeadID, &m.StepOrder, &accountID, &m.ToEmail,
		&m.Subject, &m.Body, &m.Status, &scheduled, &nextAttempt, &m.Attempts, &sent, &m.Error, &m.LastError)
	if err != nil {
		return m, err
	}
	if accountID.Valid {
		id := accountID.Int64
		m.AccountID = &id
	}
	m.ScheduledAt = parseTime(scheduled.String)
	m.NextAttemptAt = parseTime(nextAttempt.String)
	m.SentAt = parseTimePtr(sent)
	return m, nil
}

func (s *Store) GetOutboundMessage(id int64) (models.OutboundMessage, error) {
	row := s.db.QueryRow(`SELECT id, campaign_id, lead_id, campaign_lead_id, step_order, account_id, to_email, subject, body,
		status, scheduled_at, COALESCE(next_attempt_at, scheduled_at), attempts, sent_at, error, COALESCE(last_error,'')
		FROM outbound_messages WHERE id=?`, id)
	return scanMessage(row)
}

func (s *Store) ClaimMessage(id int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE outbound_messages SET status='sending', attempts=attempts+1 WHERE id=? AND status='scheduled'`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) MarkMessageSent(id, accountID int64, subject, body string) error {
	t := fmtTime(now())
	_, err := s.db.Exec(`UPDATE outbound_messages SET status='sent', account_id=?, subject=?, body=?, sent_at=?, error='', last_error='' WHERE id=?`,
		accountID, subject, body, t, id)
	return err
}

func (s *Store) SetMessageMeta(id int64, messageID, variant string) error {
	_, err := s.db.Exec(`UPDATE outbound_messages SET message_id=?, variant=? WHERE id=?`, messageID, variant, id)
	return err
}

func (s *Store) FailMessageRetry(id int64, msg string, maxAttempts int, backoff time.Duration) error {
	var attempts int
	_ = s.db.QueryRow(`SELECT attempts FROM outbound_messages WHERE id=?`, id).Scan(&attempts)
	if attempts >= maxAttempts {
		_, err := s.db.Exec(`UPDATE outbound_messages SET status='dead', error=?, last_error=? WHERE id=?`, msg, msg, id)
		return err
	}
	next := now().Add(backoff)
	_, err := s.db.Exec(`UPDATE outbound_messages SET status='scheduled', next_attempt_at=?, error=?, last_error=? WHERE id=?`,
		fmtTime(next), msg, msg, id)
	return err
}

func (s *Store) ScheduleNextStep(m models.OutboundMessage) error {
	steps, err := s.ListSteps(m.CampaignID)
	if err != nil {
		return err
	}
	var next *models.SequenceStep
	for i := range steps {
		if steps[i].StepOrder > m.StepOrder {
			next = &steps[i]
			break
		}
	}
	if next == nil {
		_, err = s.db.Exec(`UPDATE campaign_leads SET status='completed', current_step=?, next_send_at=NULL WHERE id=?`,
			m.StepOrder, m.CampaignLeadID)
		return err
	}
	when := now().Add(time.Duration(next.DelayDays) * 24 * time.Hour)
	lead, err := s.GetLead(m.LeadID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE campaign_leads SET current_step=?, next_send_at=?, status='active' WHERE id=?`,
		next.StepOrder, fmtTime(when), m.CampaignLeadID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO outbound_messages(campaign_id, lead_id, campaign_lead_id, step_order, to_email, subject, body, status, scheduled_at, next_attempt_at, attempts)
		VALUES(?,?,?,?,?,?,?,'scheduled',?,?,0)`,
		m.CampaignID, m.LeadID, m.CampaignLeadID, next.StepOrder, lead.Email, next.SubjectTemplate, next.BodySpintax, fmtTime(when), fmtTime(when))
	return err
}

func (s *Store) CountCampaignSentToday(campaignID int64) (int, error) {
	today := now().Format("2006-01-02")
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM outbound_messages WHERE campaign_id=? AND status='sent' AND sent_at LIKE ?`,
		campaignID, today+"%").Scan(&n)
	return n, err
}

func (s *Store) ListReplies(admin bool, ownerID int64) ([]models.InboundReply, error) {
	q := `SELECT id, owner_id, workspace_id, lead_id, lead_name, from_email, subject, body, intent, COALESCE(message_id,''), COALESCE(thread_id,''), COALESCE(hitl_status,'auto'), created_at
		FROM inbound_replies WHERE 1=1`
	var args []any
	if !admin {
		q += ` AND (owner_id=? OR owner_id IS NULL)`
		args = append(args, ownerID)
	}
	q += ` ORDER BY id DESC LIMIT 100`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	return scanReplies(rows)
}

func (s *Store) CreateReply(r models.InboundReply) (int64, error) {
	if r.MessageID != "" {
		var exists int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM inbound_replies WHERE message_id=? AND message_id!=''`, r.MessageID).Scan(&exists)
		if exists > 0 {
			return 0, fmt.Errorf("duplicate message")
		}
	}
	if r.HITLStatus == "" {
		r.HITLStatus = models.HITLAuto
		if r.Intent == "positive" {
			r.HITLStatus = models.HITLNeedsReview
		}
	}
	if r.ThreadID == "" && r.MessageID != "" {
		r.ThreadID = r.MessageID
	}
	var ownerID, leadID, wsID any
	if r.OwnerID != nil {
		ownerID = *r.OwnerID
	}
	if r.LeadID != nil {
		leadID = *r.LeadID
	}
	if r.WorkspaceID != nil {
		wsID = *r.WorkspaceID
	}
	res, err := s.db.Exec(`INSERT INTO inbound_replies(owner_id, workspace_id, lead_id, lead_name, from_email, subject, body, intent, message_id, thread_id, hitl_status, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, ownerID, wsID, leadID, r.LeadName, r.FromEmail, r.Subject, r.Body, r.Intent, r.MessageID, r.ThreadID, r.HITLStatus, fmtTime(now()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) IsSuppressed(email string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM suppressions WHERE lower(email)=lower(?)`, email).Scan(&n)
	return n > 0, err
}

func (s *Store) AddSuppression(email, reason string) error {
	_, err := s.db.Exec(`INSERT INTO suppressions(email, reason, created_at) VALUES(?,?,?)
		ON CONFLICT(email) DO UPDATE SET reason=excluded.reason`, email, reason, fmtTime(now()))
	return err
}

func (s *Store) ListSuppressions() ([]models.Suppression, error) {
	rows, err := s.db.Query(`SELECT id, email, reason, created_at FROM suppressions ORDER BY id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Suppression
	for rows.Next() {
		var sp models.Suppression
		var created string
		if err := rows.Scan(&sp.ID, &sp.Email, &sp.Reason, &created); err != nil {
			return nil, err
		}
		sp.CreatedAt = parseTime(created)
		out = append(out, sp)
	}
	return out, rows.Err()
}

// Fix TakeOAuthState properly in users.go - redefine here to overwrite bug
func (s *Store) ConsumeOAuthState(state string) (userID int64, provider string, err error) {
	var exp string
	err = s.db.QueryRow(`SELECT user_id, provider, expires_at FROM oauth_states WHERE state=?`, state).
		Scan(&userID, &provider, &exp)
	if err != nil {
		return 0, "", err
	}
	_, _ = s.db.Exec(`DELETE FROM oauth_states WHERE state=?`, state)
	if parseTime(exp).Before(now()) {
		return 0, "", fmt.Errorf("oauth state expired")
	}
	return userID, provider, nil
}
