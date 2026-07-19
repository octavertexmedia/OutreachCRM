package store

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/manishkumar/outreachcrm/internal/deliverability"
	"github.com/manishkumar/outreachcrm/internal/models"
)

func (s *Store) RescheduleMessageAt(id int64, when time.Time, reason string) error {
	_, err := s.db.Exec(`UPDATE outbound_messages SET status='scheduled', next_attempt_at=?, error=?, last_error=?, locked_until=NULL, lock_owner='' WHERE id=?`,
		fmtTime(when), reason, reason, id)
	return err
}

func (s *Store) DeadLetterMessage(id int64, reason string) error {
	_, err := s.db.Exec(`UPDATE outbound_messages SET status='dead', error=?, last_error=?, locked_until=NULL, lock_owner='' WHERE id=?`,
		reason, reason, id)
	return err
}

func (s *Store) GetRecipientHistory(email string) deliverability.RecipientHistory {
	email = strings.ToLower(strings.TrimSpace(email))
	var h deliverability.RecipientHistory
	var purchased int
	var first, last string
	err := s.db.QueryRow(`SELECT sent, opened, clicked, replied, hard_bounces, soft_bounces, complaints, unsubscribes,
		COALESCE(purchased_list,0), COALESCE(first_seen_at,''), COALESCE(last_event_at,'')
		FROM recipient_stats WHERE lower(email)=lower(?)`, email).Scan(
		&h.Sent, &h.Opened, &h.Clicked, &h.Replied, &h.HardBounces, &h.SoftBounces, &h.Complaints, &h.Unsubscribes,
		&purchased, &first, &last)
	if err != nil {
		// derive soft stats from outbound if first contact
		var sent, opened, replied int
		_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(opened),0), COALESCE(SUM(replied),0) FROM outbound_messages WHERE lower(to_email)=lower(?) AND status='sent'`,
			email).Scan(&sent, &opened, &replied)
		h.Sent, h.Opened, h.Replied = sent, opened, replied
		h.NeverEngaged = sent >= 2 && opened+replied == 0
		return h
	}
	h.PurchasedList = purchased == 1
	h.FirstSeenAt = parseTime(first)
	h.LastEventAt = parseTime(last)
	h.NeverEngaged = h.Sent >= 2 && h.Opened+h.Clicked+h.Replied == 0
	return h
}

func (s *Store) RecordRecipientEvent(workspaceID int64, email, event string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil
	}
	t := fmtTime(now())
	_, _ = s.db.Exec(`INSERT INTO recipient_stats(email, workspace_id, first_seen_at, last_event_at) VALUES(?,?,?,?)
		ON CONFLICT(email) DO UPDATE SET last_event_at=excluded.last_event_at`, email, workspaceID, t, t)
	col := ""
	switch event {
	case "sent":
		col = "sent"
	case "opened":
		col = "opened"
	case "clicked":
		col = "clicked"
	case "replied":
		col = "replied"
	case "hard_bounce":
		col = "hard_bounces"
	case "soft_bounce":
		col = "soft_bounces"
	case "complaint":
		col = "complaints"
	case "unsubscribe":
		col = "unsubscribes"
	default:
		return nil
	}
	_, err := s.db.Exec(`UPDATE recipient_stats SET `+col+`=`+col+`+1, last_event_at=? WHERE lower(email)=lower(?)`, t, email)
	return err
}

func (s *Store) MarkPurchasedList(workspaceID int64, email string) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return
	}
	if workspaceID == 0 {
		workspaceID = 1
	}
	t := fmtTime(now())
	_, _ = s.db.Exec(`INSERT INTO recipient_stats(email, workspace_id, purchased_list, first_seen_at, last_event_at) VALUES(?,?,1,?,?)
		ON CONFLICT(email) DO UPDATE SET purchased_list=1, workspace_id=excluded.workspace_id`,
		email, workspaceID, t, t)
}

// WorkspaceIDForEmail resolves workspace from recipient_stats, leads, or recent outbound.
func (s *Store) WorkspaceIDForEmail(email string) int64 {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return 1
	}
	var ws int64
	_ = s.db.QueryRow(`SELECT COALESCE(workspace_id,1) FROM recipient_stats WHERE lower(email)=lower(?)`, email).Scan(&ws)
	if ws > 0 {
		return ws
	}
	_ = s.db.QueryRow(`SELECT COALESCE(workspace_id,1) FROM leads WHERE lower(email)=lower(?) ORDER BY id DESC LIMIT 1`, email).Scan(&ws)
	if ws > 0 {
		return ws
	}
	_ = s.db.QueryRow(`SELECT COALESCE(c.workspace_id,1) FROM outbound_messages om
		JOIN campaigns c ON c.id=om.campaign_id
		WHERE lower(om.to_email)=lower(?) ORDER BY om.id DESC LIMIT 1`, email).Scan(&ws)
	if ws > 0 {
		return ws
	}
	return 1
}

// GetCachedBlacklist returns a DNSBL result fresher than maxAge.
func (s *Store) GetCachedBlacklist(key string, maxAge time.Duration) (listed bool, zones []string, ok bool) {
	var listedI int
	var z, checked string
	err := s.db.QueryRow(`SELECT listed, zones, checked_at FROM blacklist_checks WHERE key=?`, key).Scan(&listedI, &z, &checked)
	if err != nil {
		return false, nil, false
	}
	t := parseTime(checked)
	if t.IsZero() || now().Sub(t) > maxAge {
		return false, nil, false
	}
	if z != "" {
		zones = strings.Split(z, ",")
	}
	return listedI == 1, zones, true
}

// CountRecentDNSBLListings counts listed IPs/hosts checked within maxAge.
func (s *Store) CountRecentDNSBLListings(maxAge time.Duration) int {
	since := fmtTime(now().Add(-maxAge))
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM blacklist_checks WHERE listed=1 AND checked_at >= ?`, since).Scan(&n)
	return n
}

func (s *Store) LogDeliverabilityDecision(workspaceID, campaignID int64, d deliverability.Decision) {
	reasons, _ := json.Marshal(d.Reasons)
	_, _ = s.db.Exec(`INSERT INTO deliverability_decisions(workspace_id, campaign_id, email, action, bounce_prob, spam_trap_risk, engagement_prob, content_risk, isp, reasons, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		workspaceID, campaignID, d.Email, string(d.Action), d.BounceProb, d.SpamTrapRisk, d.EngagementProb, d.ContentRisk, d.ISP, string(reasons), fmtTime(now()))
}

func (s *Store) ListRecentDecisions(workspaceID int64, limit int) ([]models.DeliverabilityDecisionRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id, workspace_id, campaign_id, email, action, bounce_prob, spam_trap_risk, engagement_prob, content_risk, isp, reasons, created_at
		FROM deliverability_decisions WHERE workspace_id=? ORDER BY id DESC LIMIT ?`, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.DeliverabilityDecisionRow
	for rows.Next() {
		var r models.DeliverabilityDecisionRow
		var created string
		if err := rows.Scan(&r.ID, &r.WorkspaceID, &r.CampaignID, &r.Email, &r.Action, &r.BounceProb, &r.SpamTrapRisk, &r.EngagementProb, &r.ContentRisk, &r.ISP, &r.Reasons, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = parseTime(created)
		out = append(out, r)
	}
	return out, nil
}

func (s *Store) RecordISPSend(workspaceID int64, isp string) {
	_, _ = s.db.Exec(`INSERT INTO isp_send_log(workspace_id, isp, sent_at) VALUES(?,?,?)`, workspaceID, isp, fmtTime(now()))
}

func (s *Store) CountISPSentSince(workspaceID int64, isp string, since time.Time) int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM isp_send_log WHERE workspace_id=? AND isp=? AND sent_at >= ?`,
		workspaceID, isp, fmtTime(since)).Scan(&n)
	return n
}

func (s *Store) WorkspaceHealth(workspaceID int64) deliverability.HealthSnapshot {
	var h deliverability.HealthSnapshot
	since := fmtTime(now().Add(-7 * 24 * time.Hour))
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM outbound_messages om
		JOIN campaigns c ON c.id=om.campaign_id
		WHERE c.workspace_id=? AND om.status='sent' AND om.sent_at >= ?`, workspaceID, since).Scan(&h.Sent7d)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM suppressions WHERE workspace_id=? AND reason='bounce' AND created_at >= ?`, workspaceID, since).Scan(&h.HardBounce7d)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM suppressions WHERE workspace_id=? AND reason='complaint' AND created_at >= ?`, workspaceID, since).Scan(&h.Complaint7d)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM suppressions WHERE workspace_id=? AND reason='unsubscribe' AND created_at >= ?`, workspaceID, since).Scan(&h.Unsub7d)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM recipient_stats WHERE workspace_id=? AND soft_bounces>0 AND last_event_at >= ?`, workspaceID, since).Scan(&h.SoftBounce7d)
	if h.Sent7d > 0 {
		h.BounceRatePct = float64(h.HardBounce7d) / float64(h.Sent7d) * 100
		h.ComplaintPct = float64(h.Complaint7d) / float64(h.Sent7d) * 100
	}
	return h
}

func (s *Store) SetCampaignDeliverabilityPaused(id int64, paused bool) error {
	v := 0
	if paused {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE campaigns SET deliverability_paused=? WHERE id=?`, v, id)
	return err
}

func (s *Store) CampaignDeliverabilityPaused(id int64) bool {
	var v int
	_ = s.db.QueryRow(`SELECT COALESCE(deliverability_paused,0) FROM campaigns WHERE id=?`, id).Scan(&v)
	return v == 1
}

func (s *Store) PauseHotCampaigns(workspaceID int64, bounceRate, complaintRate float64) int {
	h := s.WorkspaceHealth(workspaceID)
	if h.Sent7d < 20 {
		return 0
	}
	if h.BounceRatePct < bounceRate && h.ComplaintPct < complaintRate {
		return 0
	}
	res, err := s.db.Exec(`UPDATE campaigns SET deliverability_paused=1, status='paused' WHERE workspace_id=? AND status='active'`, workspaceID)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return int(n)
}

func (s *Store) SaveLeadValidation(leadID int64, bounceProb float64, summary string) error {
	_, err := s.db.Exec(`UPDATE leads SET email_bounce_prob=?, email_validated_at=?, email_validation=? WHERE id=?`,
		bounceProb, fmtTime(now()), summary, leadID)
	return err
}

func (s *Store) DeliverabilityDashboard(workspaceID int64) deliverability.Dashboard {
	var d deliverability.Dashboard
	h := s.WorkspaceHealth(workspaceID)
	d.Sent7d = h.Sent7d
	d.BounceRate = h.BounceRatePct
	d.SpamRate = h.ComplaintPct
	d.DeliveryRate = 100
	if h.Sent7d > 0 {
		d.DeliveryRate = 100 - h.BounceRatePct
	}
	d.InboxRate = d.DeliveryRate - h.ComplaintPct*10
	if d.InboxRate < 0 {
		d.InboxRate = 0
	}
	d.MetricsAreProxy = true
	d.DomainReputation = 90 - h.BounceRatePct*5 - h.ComplaintPct*50
	if d.DomainReputation < 0 {
		d.DomainReputation = 0
	}
	if d.DomainReputation > 100 {
		d.DomainReputation = 100
	}
	d.DNSBLListed = s.CountRecentDNSBLListings(7 * 24 * time.Hour)
	d.IPReputation = d.DomainReputation
	if d.DNSBLListed > 0 {
		d.IPReputation -= float64(d.DNSBLListed) * 15
		if d.IPReputation < 0 {
			d.IPReputation = 0
		}
	}
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM suppressions WHERE workspace_id=?`, workspaceID).Scan(&d.Suppressed)
	today := now().Format("2006-01-02")
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM deliverability_decisions WHERE workspace_id=? AND created_at LIKE ?`, workspaceID, today+"%").Scan(&d.DecisionsToday)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM deliverability_decisions WHERE workspace_id=? AND action='delay' AND created_at LIKE ?`, workspaceID, today+"%").Scan(&d.DelayedToday)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM deliverability_decisions WHERE workspace_id=? AND action='suppress' AND created_at LIKE ?`, workspaceID, today+"%").Scan(&d.SuppressedToday)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM campaigns WHERE workspace_id=? AND COALESCE(deliverability_paused,0)=1`, workspaceID).Scan(&d.PausedCampaigns)
	return d
}

func (s *Store) SaveBlacklistCheck(key string, listed bool, zones []string) {
	v := 0
	if listed {
		v = 1
	}
	_, _ = s.db.Exec(`INSERT INTO blacklist_checks(key, listed, zones, checked_at) VALUES(?,?,?,?)
		ON CONFLICT(key) DO UPDATE SET listed=excluded.listed, zones=excluded.zones, checked_at=excluded.checked_at`,
		key, v, strings.Join(zones, ","), fmtTime(now()))
}

func (s *Store) LatestBlacklistChecks(limit int) ([]models.BlacklistCheck, error) {
	rows, err := s.db.Query(`SELECT key, listed, zones, checked_at FROM blacklist_checks ORDER BY checked_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.BlacklistCheck
	for rows.Next() {
		var b models.BlacklistCheck
		var listed int
		var checked string
		if err := rows.Scan(&b.Key, &listed, &b.Zones, &checked); err != nil {
			return nil, err
		}
		b.Listed = listed == 1
		b.CheckedAt = parseTime(checked)
		out = append(out, b)
	}
	return out, nil
}
