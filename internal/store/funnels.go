package store

import (
	"fmt"

	"github.com/manishkumar/outreachcrm/internal/models"
)

// RecordAudienceCampaignRun upserts the audience→campaign funnel run after enroll.
func (s *Store) RecordAudienceCampaignRun(workspaceID, campaignID, audienceID int64, enrolled, skipped int) (int64, error) {
	if campaignID <= 0 || audienceID <= 0 {
		return 0, fmt.Errorf("campaign and audience required")
	}
	if workspaceID <= 0 {
		workspaceID = 1
	}
	t := fmtTime(now())
	res, err := s.db.Exec(`INSERT INTO campaign_audience_runs(workspace_id, campaign_id, audience_id, enrolled, skipped, status, created_at, updated_at)
		VALUES(?,?,?,?,?,'active',?,?)
		ON CONFLICT(campaign_id, audience_id) DO UPDATE SET
			enrolled=campaign_audience_runs.enrolled + excluded.enrolled,
			skipped=campaign_audience_runs.skipped + excluded.skipped,
			status='active',
			updated_at=excluded.updated_at`,
		workspaceID, campaignID, audienceID, enrolled, skipped, t, t)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		_ = s.db.QueryRow(`SELECT id FROM campaign_audience_runs WHERE campaign_id=? AND audience_id=?`,
			campaignID, audienceID).Scan(&id)
	}
	return id, nil
}

func (s *Store) ListCampaignAudienceRuns(workspaceID int64) ([]models.CampaignAudienceRun, error) {
	q := `SELECT r.id, r.workspace_id, r.campaign_id, r.audience_id,
		COALESCE(c.name,''), COALESCE(a.name,''),
		r.enrolled, r.skipped, r.status, r.created_at, r.updated_at
		FROM campaign_audience_runs r
		LEFT JOIN campaigns c ON c.id=r.campaign_id
		LEFT JOIN audiences a ON a.id=r.audience_id
		WHERE 1=1`
	var args []any
	if workspaceID > 0 {
		q += ` AND r.workspace_id=?`
		args = append(args, workspaceID)
	}
	q += ` ORDER BY r.updated_at DESC, r.id DESC LIMIT 100`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.CampaignAudienceRun
	for rows.Next() {
		run, err := scanCampaignAudienceRun(rows)
		if err != nil {
			return nil, err
		}
		run.Funnel = s.CampaignAudienceFunnel(run.CampaignID, run.AudienceID)
		out = append(out, run)
	}
	return out, rows.Err()
}

func (s *Store) GetCampaignAudienceRun(id int64) (models.CampaignAudienceRun, error) {
	row := s.db.QueryRow(`SELECT r.id, r.workspace_id, r.campaign_id, r.audience_id,
		COALESCE(c.name,''), COALESCE(a.name,''),
		r.enrolled, r.skipped, r.status, r.created_at, r.updated_at
		FROM campaign_audience_runs r
		LEFT JOIN campaigns c ON c.id=r.campaign_id
		LEFT JOIN audiences a ON a.id=r.audience_id
		WHERE r.id=?`, id)
	run, err := scanCampaignAudienceRun(row)
	if err != nil {
		return run, err
	}
	run.Funnel = s.CampaignAudienceFunnel(run.CampaignID, run.AudienceID)
	return run, nil
}

func (s *Store) GetCampaignAudienceRunByPair(campaignID, audienceID int64) (models.CampaignAudienceRun, error) {
	row := s.db.QueryRow(`SELECT r.id, r.workspace_id, r.campaign_id, r.audience_id,
		COALESCE(c.name,''), COALESCE(a.name,''),
		r.enrolled, r.skipped, r.status, r.created_at, r.updated_at
		FROM campaign_audience_runs r
		LEFT JOIN campaigns c ON c.id=r.campaign_id
		LEFT JOIN audiences a ON a.id=r.audience_id
		WHERE r.campaign_id=? AND r.audience_id=?`, campaignID, audienceID)
	run, err := scanCampaignAudienceRun(row)
	if err != nil {
		return run, err
	}
	run.Funnel = s.CampaignAudienceFunnel(run.CampaignID, run.AudienceID)
	return run, nil
}

func scanCampaignAudienceRun(row scannable) (models.CampaignAudienceRun, error) {
	var r models.CampaignAudienceRun
	var created, updated string
	err := row.Scan(&r.ID, &r.WorkspaceID, &r.CampaignID, &r.AudienceID,
		&r.CampaignName, &r.AudienceName, &r.Enrolled, &r.Skipped, &r.Status, &created, &updated)
	r.CreatedAt = parseTime(created)
	r.UpdatedAt = parseTime(updated)
	return r, err
}

// CampaignAudienceFunnel computes live sequence funnel for audience members in a campaign.
func (s *Store) CampaignAudienceFunnel(campaignID, audienceID int64) models.CampaignFunnelStats {
	var f models.CampaignFunnelStats
	if campaignID <= 0 || audienceID <= 0 {
		return f
	}
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM campaign_leads
		WHERE campaign_id=? AND audience_id=? AND status IN ('active','completed','enrolled','unsubscribed')`,
		campaignID, audienceID).Scan(&f.Members)

	_ = s.db.QueryRow(`SELECT COUNT(DISTINCT cl.lead_id) FROM campaign_leads cl
		JOIN outbound_messages om ON om.campaign_lead_id=cl.id
		WHERE cl.campaign_id=? AND cl.audience_id=? AND om.status IN ('scheduled','sending')`,
		campaignID, audienceID).Scan(&f.Queued)

	_ = s.db.QueryRow(`SELECT COUNT(DISTINCT cl.lead_id) FROM campaign_leads cl
		JOIN outbound_messages om ON om.campaign_lead_id=cl.id
		WHERE cl.campaign_id=? AND cl.audience_id=? AND om.status='sent'`,
		campaignID, audienceID).Scan(&f.Sent)

	_ = s.db.QueryRow(`SELECT COUNT(DISTINCT cl.lead_id) FROM campaign_leads cl
		JOIN inbound_replies ir ON ir.lead_id=cl.lead_id
		WHERE cl.campaign_id=? AND cl.audience_id=?`,
		campaignID, audienceID).Scan(&f.Replied)

	_ = s.db.QueryRow(`SELECT COUNT(DISTINCT cl.lead_id) FROM campaign_leads cl
		JOIN inbound_replies ir ON ir.lead_id=cl.lead_id
		WHERE cl.campaign_id=? AND cl.audience_id=? AND ir.intent='positive'`,
		campaignID, audienceID).Scan(&f.Positive)

	_ = s.db.QueryRow(`SELECT COUNT(*) FROM campaign_leads
		WHERE campaign_id=? AND audience_id=? AND status='unsubscribed'`,
		campaignID, audienceID).Scan(&f.Unsubscribed)

	_ = s.db.QueryRow(`SELECT COUNT(*) FROM campaign_leads
		WHERE campaign_id=? AND audience_id=? AND status='completed'`,
		campaignID, audienceID).Scan(&f.Completed)

	rows, err := s.db.Query(`SELECT current_step, COUNT(*) FROM campaign_leads
		WHERE campaign_id=? AND audience_id=? AND status IN ('active','enrolled','completed')
		GROUP BY current_step ORDER BY current_step`, campaignID, audienceID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var step, n int
			if err := rows.Scan(&step, &n); err != nil {
				continue
			}
			label := fmt.Sprintf("Step %d", step)
			if step == 0 {
				label = "Step 1 (queued)"
			}
			f.ByStep = append(f.ByStep, models.NamedCount{Name: label, Count: n, Value: float64(n)})
		}
	}
	return f
}

// ListAudienceFunnelRuns returns runs for one audience (which funnels it is/was in).
func (s *Store) ListAudienceFunnelRuns(audienceID int64) ([]models.CampaignAudienceRun, error) {
	rows, err := s.db.Query(`SELECT r.id, r.workspace_id, r.campaign_id, r.audience_id,
		COALESCE(c.name,''), COALESCE(a.name,''),
		r.enrolled, r.skipped, r.status, r.created_at, r.updated_at
		FROM campaign_audience_runs r
		LEFT JOIN campaigns c ON c.id=r.campaign_id
		LEFT JOIN audiences a ON a.id=r.audience_id
		WHERE r.audience_id=? ORDER BY r.updated_at DESC`, audienceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.CampaignAudienceRun
	for rows.Next() {
		run, err := scanCampaignAudienceRun(rows)
		if err != nil {
			return nil, err
		}
		run.Funnel = s.CampaignAudienceFunnel(run.CampaignID, run.AudienceID)
		out = append(out, run)
	}
	return out, rows.Err()
}
