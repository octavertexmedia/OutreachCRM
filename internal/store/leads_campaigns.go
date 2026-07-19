package store

import (
	"database/sql"
	"fmt"

	"github.com/manishkumar/outreachcrm/internal/models"
)

func (s *Store) ListLeads(admin bool, ownerID int64) ([]models.Lead, error) {
	return s.ListLeadsFiltered(admin, ownerID, 0, models.LeadFilter{})
}

func scanLead(row scannable) (models.Lead, error) {
	var l models.Lead
	var created, updated string
	var consent sql.NullString
	err := row.Scan(&l.ID, &l.OwnerID, &l.WorkspaceID, &l.Name, &l.Website, &l.Phone, &l.Email, &l.GoogleRating, &l.Category,
		&l.IssuesJSON, &l.PremiumScore, &l.Confidence, &l.EnrichmentCost, &l.EnrichmentStatus, &l.Notes, &consent, &l.ConsentSource,
		&l.Source, &l.Company, &l.Title, &l.DraftSubject, &l.DraftBody, &l.EmailBounceProb, &l.EmailValidation,
		&created, &updated)
	l.CreatedAt = parseTime(created)
	l.UpdatedAt = parseTime(updated)
	l.ConsentAt = parseTimePtr(consent)
	return l, err
}

func (s *Store) GetLead(id int64) (models.Lead, error) {
	row := s.db.QueryRow(`SELECT id, COALESCE(owner_id,0), COALESCE(workspace_id,1), name, website, phone, email, google_rating, category, issues_json,
		premium_score, COALESCE(confidence,0), COALESCE(enrichment_cost,0), enrichment_status, notes, consent_at, COALESCE(consent_source,''),
		COALESCE(source,'manual'), COALESCE(company,''), COALESCE(title,''), COALESCE(draft_subject,''), COALESCE(draft_body,''),
		COALESCE(email_bounce_prob,-1), COALESCE(email_validation,''),
		created_at, updated_at FROM leads WHERE id=?`, id)
	return scanLead(row)
}

func (s *Store) FindLeadByEmail(email string) (models.Lead, error) {
	row := s.db.QueryRow(`SELECT id, COALESCE(owner_id,0), COALESCE(workspace_id,1), name, website, phone, email, google_rating, category, issues_json,
		premium_score, COALESCE(confidence,0), COALESCE(enrichment_cost,0), enrichment_status, notes, consent_at, COALESCE(consent_source,''),
		COALESCE(source,'manual'), COALESCE(company,''), COALESCE(title,''), COALESCE(draft_subject,''), COALESCE(draft_body,''),
		COALESCE(email_bounce_prob,-1), COALESCE(email_validation,''),
		created_at, updated_at FROM leads WHERE lower(email)=lower(?) LIMIT 1`, email)
	return scanLead(row)
}

func (s *Store) CreateLead(l models.Lead) (int64, error) {
	if l.Email != "" {
		existing, err := s.FindLeadByEmail(l.Email)
		if err == nil && existing.ID > 0 {
			return existing.ID, fmt.Errorf("duplicate email: lead #%d already exists", existing.ID)
		}
	}
	t := fmtTime(now())
	if l.WorkspaceID == 0 {
		l.WorkspaceID = 1
	}
	if l.Source == "" {
		l.Source = "manual"
	}
	res, err := s.db.Exec(`INSERT INTO leads(owner_id, workspace_id, name, website, phone, email, google_rating, category, issues_json,
		premium_score, enrichment_status, notes, consent_at, consent_source, source, company, title, draft_subject, draft_body, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		l.OwnerID, l.WorkspaceID, l.Name, l.Website, l.Phone, l.Email, l.GoogleRating, l.Category, nullJSON(l.IssuesJSON),
		l.PremiumScore, defaultStatus(l.EnrichmentStatus), l.Notes, nilStr(l.ConsentAt), l.ConsentSource,
		l.Source, l.Company, l.Title, l.DraftSubject, l.DraftBody, t, t)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) SaveLeadDraft(id int64, subject, body string) error {
	_, err := s.db.Exec(`UPDATE leads SET draft_subject=?, draft_body=?, updated_at=? WHERE id=?`, subject, body, fmtTime(now()), id)
	return err
}

func (s *Store) ListPendingEnrichIDs(admin bool, ownerID int64, limit int) ([]int64, error) {
	q := `SELECT id FROM leads WHERE enrichment_status IN ('pending','error')`
	var args []any
	if !admin {
		q += ` AND owner_id=?`
		args = append(args, ownerID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) PipelineFunnel(admin bool, ownerID, workspaceID int64) (models.PipelineFunnel, error) {
	var f models.PipelineFunnel
	ow := ""
	var args []any
	if !admin {
		ow = " AND owner_id=?"
		args = append(args, ownerID)
	}
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM leads WHERE 1=1`+ow, args...).Scan(&f.Sourced)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM leads WHERE enrichment_status='done'`+ow, args...).Scan(&f.Enriched)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM leads WHERE draft_subject != ''`+ow, args...).Scan(&f.Drafted)
	if admin {
		_ = s.db.QueryRow(`SELECT COUNT(DISTINCT lead_id) FROM campaign_leads`).Scan(&f.Sequenced)
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM inbound_replies`).Scan(&f.Replied)
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM inbound_replies WHERE intent='positive'`).Scan(&f.Positive)
	} else {
		_ = s.db.QueryRow(`SELECT COUNT(DISTINCT cl.lead_id) FROM campaign_leads cl JOIN leads l ON l.id=cl.lead_id WHERE l.owner_id=?`, ownerID).Scan(&f.Sequenced)
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM inbound_replies WHERE owner_id=? OR owner_id IS NULL`, ownerID).Scan(&f.Replied)
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM inbound_replies WHERE intent='positive' AND (owner_id=? OR owner_id IS NULL)`, ownerID).Scan(&f.Positive)
	}
	_ = workspaceID
	return f, nil
}

func (s *Store) ListQueue(admin bool, ownerID int64, limit int) ([]models.OutboundMessage, error) {
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

func (s *Store) ApplyDraftToCampaignStep(campaignID int64, stepOrder int, subject, body string) error {
	_, err := s.db.Exec(`UPDATE sequence_steps SET subject_template=?, body_spintax=? WHERE campaign_id=? AND step_order=?`,
		subject, body, campaignID, stepOrder)
	return err
}

func (s *Store) MarkOutboundReplied(leadEmail string) {
	_, _ = s.db.Exec(`UPDATE outbound_messages SET replied=1 WHERE lower(to_email)=lower(?) AND status='sent'`, leadEmail)
}

// SeedDemoLeads is deprecated — dummy leads are no longer inserted.
func (s *Store) SeedDemoLeads(ownerID, workspaceID int64) (int, error) {
	return s.PurgeDummyLeads()
}

func (s *Store) UpdateLeadEnrichment(id int64, category, issuesJSON string, score, confidence, costCents int, status string) error {
	_, err := s.db.Exec(`UPDATE leads SET category=?, issues_json=?, premium_score=?, confidence=?, enrichment_cost=?, enrichment_status=?, updated_at=? WHERE id=?`,
		category, issuesJSON, score, confidence, costCents, status, fmtTime(now()), id)
	return err
}

func (s *Store) SetLeadEnrichmentStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE leads SET enrichment_status=?, updated_at=? WHERE id=?`, status, fmtTime(now()), id)
	return err
}

func (s *Store) ListCampaigns(admin bool, ownerID int64) ([]models.Campaign, error) {
	q := `SELECT id, COALESCE(owner_id,0), COALESCE(workspace_id,1), name, status, daily_send_limit,
		COALESCE(timezone,'UTC'), COALESCE(send_window_start,0), COALESCE(send_window_end,23), COALESCE(ab_enabled,0),
		COALESCE(deliverability_paused,0), created_at FROM campaigns WHERE 1=1`
	var args []any
	if !admin {
		q += ` AND owner_id=?`
		args = append(args, ownerID)
	}
	q += ` ORDER BY id DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Campaign
	for rows.Next() {
		var c models.Campaign
		var created string
		var ab, paused int
		if err := rows.Scan(&c.ID, &c.OwnerID, &c.WorkspaceID, &c.Name, &c.Status, &c.DailySendLimit,
			&c.Timezone, &c.SendWindowStart, &c.SendWindowEnd, &ab, &paused, &created); err != nil {
			return nil, err
		}
		c.ABEnabled = ab == 1
		c.DeliverabilityPaused = paused == 1
		c.CreatedAt = parseTime(created)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetCampaign(id int64) (models.Campaign, error) {
	var c models.Campaign
	var created string
	var ab, paused int
	err := s.db.QueryRow(`SELECT id, COALESCE(owner_id,0), COALESCE(workspace_id,1), name, status, daily_send_limit,
		COALESCE(timezone,'UTC'), COALESCE(send_window_start,0), COALESCE(send_window_end,23), COALESCE(ab_enabled,0),
		COALESCE(deliverability_paused,0), created_at FROM campaigns WHERE id=?`, id).
		Scan(&c.ID, &c.OwnerID, &c.WorkspaceID, &c.Name, &c.Status, &c.DailySendLimit, &c.Timezone, &c.SendWindowStart, &c.SendWindowEnd, &ab, &paused, &created)
	c.ABEnabled = ab == 1
	c.DeliverabilityPaused = paused == 1
	c.CreatedAt = parseTime(created)
	return c, err
}

func (s *Store) CreateCampaign(c models.Campaign) (int64, error) {
	status := c.Status
	if status == "" {
		status = "draft"
	}
	if c.WorkspaceID == 0 {
		c.WorkspaceID = 1
	}
	if c.Timezone == "" {
		c.Timezone = "UTC"
	}
	if c.SendWindowEnd == 0 && c.SendWindowStart == 0 {
		c.SendWindowEnd = 23
	}
	ab := 0
	if c.ABEnabled {
		ab = 1
	}
	res, err := s.db.Exec(`INSERT INTO campaigns(owner_id, workspace_id, name, status, daily_send_limit, timezone, send_window_start, send_window_end, ab_enabled, created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		c.OwnerID, c.WorkspaceID, c.Name, status, c.DailySendLimit, c.Timezone, c.SendWindowStart, c.SendWindowEnd, ab, fmtTime(now()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) SetCampaignStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE campaigns SET status=? WHERE id=?`, status, id)
	return err
}

func (s *Store) ListSteps(campaignID int64) ([]models.SequenceStep, error) {
	rows, err := s.db.Query(`SELECT id, campaign_id, step_order, delay_days, subject_template, body_spintax,
		COALESCE(variant_b_subject,''), COALESCE(variant_b_body,'')
		FROM sequence_steps WHERE campaign_id=? ORDER BY step_order`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.SequenceStep
	for rows.Next() {
		var st models.SequenceStep
		if err := rows.Scan(&st.ID, &st.CampaignID, &st.StepOrder, &st.DelayDays, &st.SubjectTemplate, &st.BodySpintax, &st.VariantBSubject, &st.VariantBBody); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) AddStep(st models.SequenceStep) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO sequence_steps(campaign_id, step_order, delay_days, subject_template, body_spintax, variant_b_subject, variant_b_body)
		VALUES(?,?,?,?,?,?,?)`, st.CampaignID, st.StepOrder, st.DelayDays, st.SubjectTemplate, st.BodySpintax, st.VariantBSubject, st.VariantBBody)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) NextStepOrder(campaignID int64) (int, error) {
	var n sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(step_order) FROM sequence_steps WHERE campaign_id=?`, campaignID).Scan(&n)
	if err != nil {
		return 1, err
	}
	if !n.Valid {
		return 1, nil
	}
	return int(n.Int64) + 1, nil
}

func (s *Store) EnrollLead(campaignID, leadID int64) error {
	lead, err := s.GetLead(leadID)
	if err != nil {
		return err
	}
	if lead.Email == "" {
		return fmt.Errorf("lead has no email")
	}
	sup, _ := s.IsSuppressed(lead.Email)
	if sup {
		return fmt.Errorf("lead email is suppressed")
	}
	steps, err := s.ListSteps(campaignID)
	if err != nil {
		return err
	}
	if len(steps) == 0 {
		return fmt.Errorf("campaign has no sequence steps")
	}
	t := now()
	res, err := s.db.Exec(`INSERT INTO campaign_leads(campaign_id, lead_id, current_step, status, enrolled_at, next_send_at)
		VALUES(?,?,0,'active',?,?) ON CONFLICT(campaign_id, lead_id) DO UPDATE SET status='active', next_send_at=excluded.next_send_at`,
		campaignID, leadID, fmtTime(t), fmtTime(t))
	if err != nil {
		return err
	}
	clID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if clID == 0 {
		err = s.db.QueryRow(`SELECT id FROM campaign_leads WHERE campaign_id=? AND lead_id=?`, campaignID, leadID).Scan(&clID)
		if err != nil {
			return err
		}
	}
	var pending int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM outbound_messages WHERE campaign_lead_id=? AND status IN ('scheduled','sending')`, clID).Scan(&pending)
	if pending > 0 {
		return nil
	}
	step := steps[0]
	variant := "a"
	camp, _ := s.GetCampaign(campaignID)
	if camp.ABEnabled {
		if now().UnixNano()%2 == 1 {
			variant = "b"
		}
	}
	_, err = s.db.Exec(`INSERT INTO outbound_messages(campaign_id, lead_id, campaign_lead_id, step_order, to_email, subject, body, status, scheduled_at, next_attempt_at, attempts, variant)
		VALUES(?,?,?,?,?,?,?,'scheduled',?,?,0,?)`,
		campaignID, leadID, clID, step.StepOrder, lead.Email, step.SubjectTemplate, step.BodySpintax, fmtTime(t), fmtTime(t), variant)
	return err
}

func (s *Store) UnsubscribeCampaignLead(campaignID, leadID int64) error {
	_, err := s.db.Exec(`UPDATE campaign_leads SET status='unsubscribed', next_send_at=NULL WHERE campaign_id=? AND lead_id=?`, campaignID, leadID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE outbound_messages SET status='dead', last_error='unsubscribed' WHERE campaign_id=? AND lead_id=? AND status IN ('scheduled','sending')`,
		campaignID, leadID)
	return err
}
