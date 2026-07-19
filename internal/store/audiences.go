package store

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/manishkumar/outreachcrm/internal/models"
)

const leadSelectCols = `id, COALESCE(owner_id,0), COALESCE(workspace_id,1), name, website, phone, email, google_rating, category, issues_json,
		premium_score, COALESCE(confidence,0), COALESCE(enrichment_cost,0), enrichment_status, notes, consent_at, COALESCE(consent_source,''),
		COALESCE(source,'manual'), COALESCE(company,''), COALESCE(title,''), COALESCE(draft_subject,''), COALESCE(draft_body,''),
		COALESCE(email_bounce_prob,-1), COALESCE(email_validation,''),
		created_at, updated_at`

func (s *Store) ListLeadsFiltered(admin bool, ownerID, workspaceID int64, f models.LeadFilter) ([]models.Lead, error) {
	q := `SELECT ` + leadSelectCols + ` FROM leads l WHERE 1=1`
	q, args := s.applyLeadFilter(q, nil, admin, ownerID, workspaceID, f)
	q += ` ORDER BY l.id DESC LIMIT 5000`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Lead
	for rows.Next() {
		l, err := scanLead(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) CountLeadsFiltered(admin bool, ownerID, workspaceID int64, f models.LeadFilter) (int, error) {
	q := `SELECT COUNT(*) FROM leads l WHERE 1=1`
	q, args := s.applyLeadFilter(q, nil, admin, ownerID, workspaceID, f)
	var n int
	err := s.db.QueryRow(q, args...).Scan(&n)
	return n, err
}

func (s *Store) applyLeadFilter(q string, args []any, admin bool, ownerID, workspaceID int64, f models.LeadFilter) (string, []any) {
	if !admin {
		q += ` AND l.owner_id=?`
		args = append(args, ownerID)
	} else if workspaceID > 0 {
		q += ` AND l.workspace_id=?`
		args = append(args, workspaceID)
	}
	if len(f.Categories) > 0 {
		q += ` AND lower(trim(l.category)) IN (` + placeholders(len(f.Categories)) + `)`
		for _, c := range f.Categories {
			args = append(args, strings.ToLower(strings.TrimSpace(c)))
		}
	}
	if len(f.Sources) > 0 {
		q += ` AND lower(trim(l.source)) IN (` + placeholders(len(f.Sources)) + `)`
		for _, src := range f.Sources {
			args = append(args, strings.ToLower(strings.TrimSpace(src)))
		}
	}
	if len(f.EnrichmentStatuses) > 0 {
		q += ` AND lower(trim(l.enrichment_status)) IN (` + placeholders(len(f.EnrichmentStatuses)) + `)`
		for _, st := range f.EnrichmentStatuses {
			args = append(args, strings.ToLower(strings.TrimSpace(st)))
		}
	}
	if f.MinPremium > 0 {
		q += ` AND l.premium_score >= ?`
		args = append(args, f.MinPremium)
	}
	if f.HasEmail {
		q += ` AND trim(l.email) != ''`
	}
	if f.ExcludeSuppressed {
		q += ` AND (trim(l.email)='' OR lower(l.email) NOT IN (SELECT lower(email) FROM suppressions))`
	}
	if c := strings.TrimSpace(f.CompanyContains); c != "" {
		q += ` AND lower(l.company) LIKE ?`
		args = append(args, "%"+strings.ToLower(c)+"%")
	}
	if qq := strings.TrimSpace(f.Q); qq != "" {
		like := "%" + strings.ToLower(qq) + "%"
		q += ` AND (lower(l.name) LIKE ? OR lower(l.email) LIKE ? OR lower(l.company) LIKE ?)`
		args = append(args, like, like, like)
	}
	if f.ExcludeEnrolledIn > 0 {
		q += ` AND l.id NOT IN (SELECT lead_id FROM campaign_leads WHERE campaign_id=? AND status IN ('active','completed','enrolled'))`
		args = append(args, f.ExcludeEnrolledIn)
	}
	return q, args
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

func (s *Store) DistinctLeadValues(admin bool, ownerID, workspaceID int64, column string) []string {
	col := map[string]string{
		"category":          "category",
		"source":            "source",
		"enrichment_status": "enrichment_status",
	}[column]
	if col == "" {
		return nil
	}
	q := `SELECT DISTINCT trim(` + col + `) FROM leads l WHERE trim(` + col + `) != ''`
	var args []any
	if !admin {
		q += ` AND l.owner_id=?`
		args = append(args, ownerID)
	} else if workspaceID > 0 {
		q += ` AND l.workspace_id=?`
		args = append(args, workspaceID)
	}
	q += ` ORDER BY 1 ASC LIMIT 80`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			continue
		}
		out = append(out, v)
	}
	return out
}

func encodeLeadFilter(f models.LeadFilter) string {
	b, err := json.Marshal(f)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func decodeLeadFilter(raw string) models.LeadFilter {
	var f models.LeadFilter
	if strings.TrimSpace(raw) == "" {
		return f
	}
	_ = json.Unmarshal([]byte(raw), &f)
	return f
}

func (s *Store) CreateAudience(a models.Audience) (int64, error) {
	t := fmtTime(now())
	if a.WorkspaceID == 0 {
		a.WorkspaceID = 1
	}
	fj := a.FilterJSON
	if fj == "" {
		fj = encodeLeadFilter(a.Filter)
	}
	res, err := s.db.Exec(`INSERT INTO audiences(workspace_id, owner_id, name, description, filter_json, member_count, created_at, updated_at)
		VALUES(?,?,?,?,?,0,?,?)`,
		a.WorkspaceID, a.OwnerID, strings.TrimSpace(a.Name), strings.TrimSpace(a.Description), fj, t, t)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateAudience(a models.Audience) error {
	fj := a.FilterJSON
	if fj == "" {
		fj = encodeLeadFilter(a.Filter)
	}
	_, err := s.db.Exec(`UPDATE audiences SET name=?, description=?, filter_json=?, updated_at=? WHERE id=?`,
		strings.TrimSpace(a.Name), strings.TrimSpace(a.Description), fj, fmtTime(now()), a.ID)
	return err
}

func (s *Store) DeleteAudience(id int64) error {
	_, _ = s.db.Exec(`DELETE FROM audience_members WHERE audience_id=?`, id)
	_, err := s.db.Exec(`DELETE FROM audiences WHERE id=?`, id)
	return err
}

func (s *Store) GetAudience(id int64) (models.Audience, error) {
	row := s.db.QueryRow(`SELECT id, workspace_id, owner_id, name, description, filter_json, member_count, created_at, updated_at
		FROM audiences WHERE id=?`, id)
	return scanAudience(row)
}

func (s *Store) ListAudiences(admin bool, ownerID, workspaceID int64) ([]models.Audience, error) {
	q := `SELECT id, workspace_id, owner_id, name, description, filter_json, member_count, created_at, updated_at
		FROM audiences WHERE 1=1`
	var args []any
	if !admin {
		q += ` AND owner_id=?`
		args = append(args, ownerID)
	} else if workspaceID > 0 {
		q += ` AND workspace_id=?`
		args = append(args, workspaceID)
	}
	q += ` ORDER BY id DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Audience
	for rows.Next() {
		a, err := scanAudience(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanAudience(row scannable) (models.Audience, error) {
	var a models.Audience
	var created, updated string
	err := row.Scan(&a.ID, &a.WorkspaceID, &a.OwnerID, &a.Name, &a.Description, &a.FilterJSON, &a.MemberCount, &created, &updated)
	if err != nil {
		return a, err
	}
	a.Filter = decodeLeadFilter(a.FilterJSON)
	a.CreatedAt = parseTime(created)
	a.UpdatedAt = parseTime(updated)
	return a, nil
}

// RefreshAudienceMembers re-resolves the filter into audience_members and updates member_count.
func (s *Store) RefreshAudienceMembers(audienceID int64) (int, error) {
	a, err := s.GetAudience(audienceID)
	if err != nil {
		return 0, err
	}
	// Resolve with the audience owner's lead scope (workspace-aware for shared admin data).
	leads, err := s.ListLeadsFiltered(false, a.OwnerID, a.WorkspaceID, a.Filter)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM audience_members WHERE audience_id=?`, audienceID); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	t := fmtTime(now())
	for _, l := range leads {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO audience_members(audience_id, lead_id, added_at) VALUES(?,?,?)`,
			audienceID, l.ID, t); err != nil {
			_ = tx.Rollback()
			return 0, err
		}
	}
	n := len(leads)
	if _, err := tx.Exec(`UPDATE audiences SET member_count=?, updated_at=? WHERE id=?`, n, t, audienceID); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) ListAudienceMembers(audienceID int64, limit int) ([]models.Lead, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT ` + leadSelectCols + ` FROM leads l
		INNER JOIN audience_members am ON am.lead_id=l.id
		WHERE am.audience_id=? ORDER BY l.id DESC LIMIT ?`
	rows, err := s.db.Query(q, audienceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Lead
	for rows.Next() {
		l, err := scanLead(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) ListAudienceMemberIDs(audienceID int64) ([]int64, error) {
	rows, err := s.db.Query(`SELECT lead_id FROM audience_members WHERE audience_id=? ORDER BY lead_id`, audienceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) IsLeadEnrolled(campaignID, leadID int64) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM campaign_leads WHERE campaign_id=? AND lead_id=? AND status IN ('active','completed','enrolled')`,
		campaignID, leadID).Scan(&n)
	return n > 0
}

func (s *Store) CountCampaignEnrolled(campaignID int64) int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM campaign_leads WHERE campaign_id=? AND status IN ('active','completed','enrolled')`, campaignID).Scan(&n)
	return n
}

// EnrollAudience enrolls current audience_members into a campaign.
func (s *Store) EnrollAudience(campaignID, audienceID int64) (models.AudienceEnrollResult, error) {
	var res models.AudienceEnrollResult
	ids, err := s.ListAudienceMemberIDs(audienceID)
	if err != nil {
		return res, err
	}
	if len(ids) == 0 {
		// Auto-refresh once if empty
		if _, err := s.RefreshAudienceMembers(audienceID); err != nil {
			return res, err
		}
		ids, err = s.ListAudienceMemberIDs(audienceID)
		if err != nil {
			return res, err
		}
	}
	for _, leadID := range ids {
		if s.IsLeadEnrolled(campaignID, leadID) {
			res.Skipped++
			continue
		}
		lead, err := s.GetLead(leadID)
		if err != nil {
			res.Skipped++
			res.Errors = append(res.Errors, fmt.Sprintf("lead #%d: %v", leadID, err))
			continue
		}
		if strings.TrimSpace(lead.Email) == "" {
			res.Skipped++
			continue
		}
		if sup, _ := s.IsSuppressed(lead.Email); sup {
			res.Skipped++
			continue
		}
		if err := s.EnrollLead(campaignID, leadID); err != nil {
			res.Skipped++
			if len(res.Errors) < 20 {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", lead.Email, err))
			}
			continue
		}
		res.Enrolled++
	}
	return res, nil
}
