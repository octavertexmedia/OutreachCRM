package store

import (
	"database/sql"
	"time"

	"github.com/manishkumar/outreachcrm/internal/classify"
	"github.com/manishkumar/outreachcrm/internal/models"
	"github.com/manishkumar/outreachcrm/internal/scoring"
)

// ClassifyReplies labels every reply whose stored classification is older than
// the current classifier.
//
// It runs as its own pass rather than inside the import: an import is
// rate-limited and slow to repeat, whereas re-classifying stored replies is
// cheap. Selecting on classifier_version is what turns a rule change into a
// small sweep instead of a full re-import.
func (s *Store) ClassifyReplies(workspaceID int64, limit int) (int, error) {
	if limit <= 0 {
		limit = 5000
	}
	rows, err := s.db.Query(`
SELECT r.id, COALESCE(r.category_raw,''), COALESCE(r.subject,''), COALESCE(r.body,''),
       r.created_at, COALESCE(r.auto_reply,0), COALESCE(r.prompt_sent_at,'')
FROM inbound_replies r
WHERE COALESCE(r.classifier_version,0) < ?
  AND (r.workspace_id = ? OR ? = 0)
ORDER BY r.id
LIMIT ?`, classify.Version, workspaceID, workspaceID, limit)
	if err != nil {
		return 0, err
	}

	type pending struct {
		id         int64
		raw        string
		subject    string
		body       string
		createdAt  string
		promptSent string
		ignore     bool
	}
	var batch []pending
	for rows.Next() {
		var p pending
		var ignore int
		if err := rows.Scan(&p.id, &p.raw, &p.subject, &p.body, &p.createdAt, &ignore, &p.promptSent); err != nil {
			rows.Close()
			return 0, err
		}
		p.ignore = ignore == 1
		batch = append(batch, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	n := 0
	for _, p := range batch {
		res := classify.Classify(classify.Input{
			SmartleadCategory: p.raw,
			IgnoreReply:       p.ignore,
			Subject:           p.subject,
			Body:              p.body,
			RepliedAt:         parseTime(p.createdAt),
			// The prompting send, when the import recorded it. A reply that
			// lands seconds after it was not typed by a person.
			SentAt: parseTime(p.promptSent),
		})
		points, _ := classify.IntentPoints(res)
		if _, err := s.db.Exec(`
UPDATE inbound_replies
SET category = ?, ask = ?, intent_points = ?, auto_reply = ?,
    classify_reason = ?, classifier_version = ?, intent = ?
WHERE id = ?`,
			string(res.Category), string(res.Ask), points, boolInt(res.AutoReply),
			res.Reason, res.Version, string(res.Category), p.id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// SetReplyImportMeta records what the import knows about a reply beyond its
// text: Smartlead's own category, and when the send that prompted it went out.
// Both come from the statistics row rather than the message thread, so they
// have to be attached after the reply is created.
//
// classifier_version is reset to 0 so the next sweep reclassifies with this new
// evidence rather than trusting an earlier, blinder verdict.
func (s *Store) SetReplyImportMeta(replyID int64, categoryRaw string, promptSentAt string, ignoreReply bool) error {
	if replyID == 0 {
		return nil
	}
	var sent any
	if promptSentAt != "" {
		sent = promptSentAt
	}
	_, err := s.db.Exec(`
UPDATE inbound_replies
SET category_raw = ?, prompt_sent_at = ?, auto_reply = ?, classifier_version = 0
WHERE id = ?`, categoryRaw, sent, boolInt(ignoreReply), replyID)
	return err
}

// signalInput is the per-person aggregate the scorer needs.
type signalInput struct {
	personID    int64
	workspaceID int64
	intentMax   int
	intentCat   string
	lastContact sql.NullString
	lastReply   sql.NullString
	revisitAt   sql.NullString
	jobChange   sql.NullString
	suppressed  int
	deliverable int
	tracked     int
}

// RecomputeSignals rebuilds person_signal for a workspace.
//
// The inputs are gathered in one aggregate query rather than a query per
// person, then scored in Go and written back in batches. Components are stored
// alongside the total so a recommendation can explain itself later.
func (s *Store) RecomputeSignals(workspaceID int64) (int, error) {
	rows, err := s.db.Query(`
SELECT p.id, p.workspace_id,
       COALESCE(MAX(r.intent_points), 0)                          AS intent_max,
       COALESCE(MAX(r.category), '')                              AS intent_cat,
       MAX(om.sent_at)                                            AS last_contact,
       MAX(r.created_at)                                          AS last_reply,
       MAX(r.revisit_at)                                          AS revisit_at,
       MAX(CASE WHEN ev.kind = ? THEN ev.occurred_at END)         AS job_change,
       MAX(CASE WHEN sup.email IS NOT NULL THEN 1 ELSE 0 END)     AS suppressed,
       MAX(CASE WHEN pe.status = ? THEN 1 ELSE 0 END)             AS has_live_email,
       MAX(CASE WHEN ct.track_opens = 1 THEN 1 ELSE 0 END)        AS tracked
FROM person p
LEFT JOIN inbound_replies r    ON r.person_id = p.id AND COALESCE(r.auto_reply,0) = 0
LEFT JOIN outbound_messages om ON om.person_id = p.id AND om.status = 'sent'
LEFT JOIN prospect_event ev    ON ev.person_id = p.id
LEFT JOIN person_email pe      ON pe.person_id = p.id
LEFT JOIN suppressions sup     ON lower(sup.email) = p.primary_email
LEFT JOIN campaign_tracking ct ON ct.campaign_id = om.campaign_id
WHERE p.merged_into_id IS NULL AND (p.workspace_id = ? OR ? = 0)
GROUP BY p.id, p.workspace_id`,
		models.EventJobChange, models.EmailStatusActive, workspaceID, workspaceID)
	if err != nil {
		return 0, err
	}

	var inputs []signalInput
	for rows.Next() {
		var in signalInput
		if err := rows.Scan(&in.personID, &in.workspaceID, &in.intentMax, &in.intentCat,
			&in.lastContact, &in.lastReply, &in.revisitAt, &in.jobChange,
			&in.suppressed, &in.deliverable, &in.tracked); err != nil {
			rows.Close()
			return 0, err
		}
		inputs = append(inputs, in)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	now := now()
	n := 0
	for _, in := range inputs {
		res := scoring.Score(scoring.Input{
			IntentMax:         in.intentMax,
			IntentReason:      intentReason(in.intentCat, in.intentMax),
			LastContactedAt:   parseTimePtr(in.lastContact),
			LastReplyAt:       parseTimePtr(in.lastReply),
			RevisitAt:         parseTimePtr(in.revisitAt),
			JobChangedAt:      parseTimePtr(in.jobChange),
			Suppressed:        in.suppressed == 1,
			Deliverable:       in.deliverable == 1,
			EngagementTracked: in.tracked == 1,
			Now:               now,
		})
		if err := s.upsertSignal(in, res, now); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (s *Store) upsertSignal(in signalInput, res scoring.Result, at time.Time) error {
	t := fmtTime(at)
	if _, err := s.db.Exec(`
INSERT OR IGNORE INTO person_signal (person_id, workspace_id, computed_at)
VALUES (?,?,?)`, in.personID, in.workspaceID, t); err != nil {
		return err
	}
	_, err := s.db.Exec(`
UPDATE person_signal
SET workspace_id = ?, intent_max = ?, intent_reason = ?, decay = ?, triggers = ?,
    priority = ?, reason = ?, suggested_action = ?, suppressed = ?,
    engagement_tracked = ?, last_contacted_at = ?, last_reply_at = ?,
    revisit_at = ?, computed_at = ?
WHERE person_id = ?`,
		in.workspaceID, res.IntentMax, res.Reason, res.Decay, res.Triggers,
		res.Priority, res.Reason, res.SuggestedAction, boolInt(res.Suppressed),
		in.tracked, nullStr(in.lastContact), nullStr(in.lastReply),
		nullStr(in.revisitAt), t, in.personID)
	return err
}

func nullStr(s sql.NullString) any {
	if !s.Valid || s.String == "" {
		return nil
	}
	return s.String
}

func intentReason(category string, points int) string {
	switch classify.Category(category) {
	case classify.Interested:
		if points >= 55 {
			return "asked for a meeting"
		}
		if points >= 40 {
			return "asked for information"
		}
		return "replied positively"
	case classify.FollowUpLater:
		return "timing objection — invited a later approach"
	case classify.NotInterested:
		return "said not interested"
	case classify.WrongContact:
		return "not their remit"
	}
	if points > 0 {
		return "engaged previously"
	}
	return ""
}

// Prospect is one row of the "worth contacting again" list.
type Prospect struct {
	PersonID            int64
	Name                string
	Company             string
	Title               string
	Email               string
	PreviousInteraction string
	WhyRecommended      string
	SuggestedAction     string
	LastContactedAt     *time.Time
	Priority            float64
}

// ListProspectsToContact answers the question the whole system exists for:
// which old prospects are worth approaching again, and why.
//
// Suppressed people and absorbed duplicates never appear, and the cooldown is
// already baked into priority, so this is a straight ranked read.
func (s *Store) ListProspectsToContact(workspaceID int64, minPriority float64, limit int) ([]Prospect, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
SELECT p.id, p.display_name, COALESCE(emp.company,''), COALESCE(emp.title,''),
       p.primary_email, s.reason, s.suggested_action, s.last_contacted_at, s.priority,
       COALESCE((
         SELECT substr(r.body, 1, 180) FROM inbound_replies r
         WHERE r.person_id = p.id AND COALESCE(r.auto_reply,0) = 0
         ORDER BY r.created_at DESC LIMIT 1
       ), '') AS last_reply_snippet
FROM person_signal s
JOIN person p ON p.id = s.person_id
LEFT JOIN person_employment emp ON emp.person_id = p.id AND emp.valid_to IS NULL
WHERE p.merged_into_id IS NULL
  AND s.suppressed = 0
  AND s.priority >= ?
  AND (s.workspace_id = ? OR ? = 0)
ORDER BY s.priority DESC, p.id ASC
LIMIT ?`, minPriority, workspaceID, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Prospect
	for rows.Next() {
		var p Prospect
		var last sql.NullString
		if err := rows.Scan(&p.PersonID, &p.Name, &p.Company, &p.Title, &p.Email,
			&p.WhyRecommended, &p.SuggestedAction, &last, &p.Priority,
			&p.PreviousInteraction); err != nil {
			return nil, err
		}
		p.LastContactedAt = parseTimePtr(last)
		out = append(out, p)
	}
	return out, rows.Err()
}
