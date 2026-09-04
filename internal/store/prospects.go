package store

import (
	"database/sql"
	"strings"

	"github.com/manishkumar/outreachcrm/internal/models"
)

// NormalizeEmail lowercases, trims, and strips plus-addressing so that
// "John.Smith+crm@Example.com " and "john.smith@example.com" resolve to one
// address. Gmail-style dot-folding is deliberately NOT applied: it is correct
// for gmail.com and wrong for most corporate domains, and a false identity
// merge is far more costly than a duplicate.
func NormalizeEmail(email string) string {
	e := strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndex(e, "@")
	if at <= 0 {
		return e
	}
	local, domain := e[:at], e[at+1:]
	if plus := strings.Index(local, "+"); plus > 0 {
		local = local[:plus]
	}
	return local + "@" + domain
}

// EmailDomain returns the domain part of an address, or "" if there isn't one.
// Kept in Go rather than SQL because SQLite and Postgres disagree on the string
// functions needed to do it in a query.
func EmailDomain(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 || at+1 >= len(email) {
		return ""
	}
	return email[at+1:]
}

// BackfillResult reports what a backfill run created.
type BackfillResult struct {
	PeopleCreated     int
	EmailsCreated     int
	EmploymentCreated int
	MessagesLinked    int
	RepliesLinked     int
}

// BackfillProspects builds the identity layer from the legacy leads table.
//
// It is idempotent: every step skips rows that already exist, so it can be run
// repeatedly and after each subsequent import. It creates exactly one person
// per lead — deliberately, because merging duplicates is identity resolution's
// job and it needs the full picture before deciding.
func (s *Store) BackfillProspects() (BackfillResult, error) {
	var res BackfillResult
	t := fmtTime(now())

	// One person per lead not already represented. Set-based: 65k rows in one
	// statement rather than 65k round trips.
	r, err := s.db.Exec(`
INSERT INTO person (workspace_id, lead_id, display_name, primary_email, phone,
                    first_seen_at, last_seen_at, created_at, updated_at)
SELECT COALESCE(l.workspace_id, 1), l.id, COALESCE(l.name, ''),
       lower(COALESCE(l.email, '')), COALESCE(l.phone, ''),
       COALESCE(l.created_at, ?), COALESCE(l.updated_at, ?), ?, ?
FROM leads l
WHERE NOT EXISTS (SELECT 1 FROM person p WHERE p.lead_id = l.id)`,
		t, t, t, t)
	if err != nil {
		return res, err
	}
	if n, err := r.RowsAffected(); err == nil {
		res.PeopleCreated = int(n)
	}

	if n, err := s.backfillPersonEmails(); err != nil {
		return res, err
	} else {
		res.EmailsCreated = n
	}

	if n, err := s.backfillEmployment(); err != nil {
		return res, err
	} else {
		res.EmploymentCreated = n
	}

	// Connect existing history to the identity layer.
	if r, err := s.db.Exec(`
UPDATE outbound_messages SET person_id = (
  SELECT p.id FROM person p WHERE p.lead_id = outbound_messages.lead_id
) WHERE person_id IS NULL`); err == nil {
		if n, err := r.RowsAffected(); err == nil {
			res.MessagesLinked = int(n)
		}
	} else {
		return res, err
	}

	if r, err := s.db.Exec(`
UPDATE inbound_replies SET person_id = (
  SELECT p.id FROM person p WHERE p.lead_id = inbound_replies.lead_id
) WHERE person_id IS NULL AND lead_id IS NOT NULL`); err == nil {
		if n, err := r.RowsAffected(); err == nil {
			res.RepliesLinked = int(n)
		}
	} else {
		return res, err
	}

	return res, nil
}

// backfillPersonEmails creates the address rows. Done in Go because the domain
// split needs string functions the two dialects spell differently, and because
// an address may already belong to a different person from an earlier run.
func (s *Store) backfillPersonEmails() (int, error) {
	rows, err := s.db.Query(`
SELECT p.id, p.workspace_id, p.primary_email, p.first_seen_at, p.last_seen_at
FROM person p
WHERE p.primary_email <> ''
  AND NOT EXISTS (SELECT 1 FROM person_email e WHERE e.person_id = p.id)`)
	if err != nil {
		return 0, err
	}
	type pending struct {
		personID    int64
		workspaceID int64
		email       string
		first, last string
	}
	var batch []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.personID, &p.workspaceID, &p.email, &p.first, &p.last); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Insert in multi-row batches. One statement per address turns a backfill
	// of the full base into ~65k round trips (~40s); batching makes it a few
	// seconds, and this runs after every import.
	const perBatch = 200
	n := 0
	var args []any
	rows_ := 0

	flush := func() error {
		if rows_ == 0 {
			return nil
		}
		// INSERT OR IGNORE: an address may already map to a different person
		// from an earlier run, in which case that mapping wins and we leave it.
		q := `INSERT OR IGNORE INTO person_email
  (person_id, workspace_id, email, email_domain, status, first_seen_at, last_seen_at) VALUES ` +
			placeholderRows(rows_, 7)
		r, err := s.db.Exec(q, args...)
		if err != nil {
			return err
		}
		if a, err := r.RowsAffected(); err == nil && a > 0 {
			n += int(a)
		}
		args = args[:0]
		rows_ = 0
		return nil
	}

	for _, p := range batch {
		email := NormalizeEmail(p.email)
		if email == "" {
			continue
		}
		args = append(args, p.personID, p.workspaceID, email, EmailDomain(email),
			models.EmailStatusActive, p.first, p.last)
		rows_++
		if rows_ >= perBatch {
			if err := flush(); err != nil {
				return n, err
			}
		}
	}
	if err := flush(); err != nil {
		return n, err
	}
	return n, nil
}

// placeholderRows builds "(?,?,?),(?,?,?)" for a multi-row INSERT. The dialect
// layer renumbers these to $1..$N for Postgres.
func placeholderRows(rows, cols int) string {
	var b strings.Builder
	b.Grow(rows * (cols*2 + 2))
	for r := 0; r < rows; r++ {
		if r > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('(')
		for c := 0; c < cols; c++ {
			if c > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('?')
		}
		b.WriteByte(')')
	}
	return b.String()
}

// backfillEmployment seeds one employment row per lead that has a company or a
// title, left open-ended because we do not know when the role started.
func (s *Store) backfillEmployment() (int, error) {
	t := fmtTime(now())
	r, err := s.db.Exec(`
INSERT INTO person_employment (person_id, company, domain, title, valid_from, valid_to, source, created_at)
SELECT p.id, COALESCE(l.company, ''), COALESCE(l.website, ''), COALESCE(l.title, ''),
       l.created_at, NULL, ?, ?
FROM person p
JOIN leads l ON l.id = p.lead_id
WHERE (COALESCE(l.company, '') <> '' OR COALESCE(l.title, '') <> '')
  AND NOT EXISTS (SELECT 1 FROM person_employment e WHERE e.person_id = p.id)`,
		models.EmploymentSourceSmartlead, t)
	if err != nil {
		return 0, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return int(n), nil
}

// FindPersonByEmail resolves an address to a person, following a merge if the
// record it lands on has since been absorbed into another.
func (s *Store) FindPersonByEmail(workspaceID int64, email string) (models.Person, error) {
	norm := NormalizeEmail(email)
	var id int64
	err := s.db.QueryRow(`
SELECT e.person_id FROM person_email e
WHERE e.email = ? AND (e.workspace_id = ? OR ? = 0) LIMIT 1`,
		norm, workspaceID, workspaceID).Scan(&id)
	if err != nil {
		return models.Person{}, err
	}
	return s.GetPerson(id)
}

// GetPerson loads one person, resolving merge chains so callers always get the
// surviving record.
func (s *Store) GetPerson(id int64) (models.Person, error) {
	for hops := 0; hops < 8; hops++ {
		var p models.Person
		var lead, merged sql.NullInt64
		var first, last string
		err := s.db.QueryRow(`
SELECT id, workspace_id, lead_id, display_name, primary_email, phone,
       linkedin_url, merged_into_id, first_seen_at, last_seen_at
FROM person WHERE id = ?`, id).Scan(
			&p.ID, &p.WorkspaceID, &lead, &p.DisplayName, &p.PrimaryEmail,
			&p.Phone, &p.LinkedInURL, &merged, &first, &last)
		if err != nil {
			return models.Person{}, err
		}
		p.LeadID = lead.Int64
		p.FirstSeenAt = parseTime(first)
		p.LastSeenAt = parseTime(last)
		if !merged.Valid || merged.Int64 == 0 {
			return p, nil
		}
		p.MergedIntoID = merged.Int64
		id = merged.Int64
	}
	return models.Person{}, sql.ErrNoRows
}

// CountPeople returns the number of live (unmerged) people in a workspace.
func (s *Store) CountPeople(workspaceID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`
SELECT COUNT(*) FROM person
WHERE merged_into_id IS NULL AND (workspace_id = ? OR ? = 0)`,
		workspaceID, workspaceID).Scan(&n)
	return n, err
}

// ListPersonEmails returns every address known for a person, newest activity
// first — the address history that survives a job change.
func (s *Store) ListPersonEmails(personID int64) ([]models.PersonEmail, error) {
	rows, err := s.db.Query(`
SELECT id, person_id, workspace_id, email, email_domain, status, bounce_count,
       first_seen_at, last_seen_at
FROM person_email WHERE person_id = ? ORDER BY last_seen_at DESC`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.PersonEmail
	for rows.Next() {
		var e models.PersonEmail
		var first, last string
		if err := rows.Scan(&e.ID, &e.PersonID, &e.WorkspaceID, &e.Email,
			&e.Domain, &e.Status, &e.BounceCount, &first, &last); err != nil {
			return nil, err
		}
		e.FirstSeenAt = parseTime(first)
		e.LastSeenAt = parseTime(last)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListEmployment returns a person's roles, current first.
func (s *Store) ListEmployment(personID int64) ([]models.PersonEmployment, error) {
	rows, err := s.db.Query(`
SELECT id, person_id, company, domain, title, valid_from, valid_to, source
FROM person_employment WHERE person_id = ?
ORDER BY CASE WHEN valid_to IS NULL THEN 0 ELSE 1 END, valid_from DESC`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.PersonEmployment
	for rows.Next() {
		var e models.PersonEmployment
		var from, to sql.NullString
		if err := rows.Scan(&e.ID, &e.PersonID, &e.Company, &e.Domain,
			&e.Title, &from, &to, &e.Source); err != nil {
			return nil, err
		}
		e.ValidFrom = parseTimePtr(from)
		e.ValidTo = parseTimePtr(to)
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddPersonEmail records an additional address for a person — the mechanism by
// which someone survives a job change with their history intact.
func (s *Store) AddPersonEmail(personID, workspaceID int64, email string) error {
	norm := NormalizeEmail(email)
	if norm == "" {
		return nil
	}
	t := fmtTime(now())
	_, err := s.db.Exec(`
INSERT OR IGNORE INTO person_email
  (person_id, workspace_id, email, email_domain, status, first_seen_at, last_seen_at)
VALUES (?,?,?,?,?,?,?)`,
		personID, workspaceID, norm, EmailDomain(norm), models.EmailStatusActive, t, t)
	return err
}

// AppendEvent records something that happened to a person. Events are
// append-only and deduped on DedupeKey, which is what makes re-running an
// import a no-op rather than a source of double-counted history.
func (s *Store) AppendEvent(e models.ProspectEvent) error {
	if e.DedupeKey == "" || e.PersonID == 0 {
		return nil
	}
	payload := e.Payload
	if payload == "" {
		payload = "{}"
	}
	source := e.Source
	if source == "" {
		source = "smartlead_import"
	}
	var campaign any
	if e.CampaignID > 0 {
		campaign = e.CampaignID
	}
	_, err := s.db.Exec(`
INSERT OR IGNORE INTO prospect_event
  (person_id, workspace_id, campaign_id, kind, occurred_at, payload, source, dedupe_key, created_at)
VALUES (?,?,?,?,?,?,?,?,?)`,
		e.PersonID, e.WorkspaceID, campaign, e.Kind, fmtTime(e.OccurredAt),
		payload, source, e.DedupeKey, fmtTime(now()))
	return err
}

// CountEvents returns how many events a person has of a given kind. Passing an
// empty kind counts them all.
func (s *Store) CountEvents(personID int64, kind string) (int, error) {
	var n int
	err := s.db.QueryRow(`
SELECT COUNT(*) FROM prospect_event
WHERE person_id = ? AND (kind = ? OR ? = '')`, personID, kind, kind).Scan(&n)
	return n, err
}

// SetCampaignTracking records whether a campaign collected opens and clicks, so
// scoring can tell "not engaged" apart from "never measured".
func (s *Store) SetCampaignTracking(campaignID int64, opens, clicks bool) error {
	_, err := s.db.Exec(`
INSERT OR IGNORE INTO campaign_tracking (campaign_id, track_opens, track_clicks, updated_at)
VALUES (?,?,?,?)`, campaignID, boolInt(opens), boolInt(clicks), fmtTime(now()))
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
UPDATE campaign_tracking SET track_opens = ?, track_clicks = ?, updated_at = ?
WHERE campaign_id = ?`, boolInt(opens), boolInt(clicks), fmtTime(now()), campaignID)
	return err
}

// CampaignTracksOpens reports whether a campaign recorded opens. Unknown
// campaigns are assumed to track, which is the safe default: it never
// manufactures a penalty for a prospect.
func (s *Store) CampaignTracksOpens(campaignID int64) bool {
	var v int
	if err := s.db.QueryRow(`SELECT track_opens FROM campaign_tracking WHERE campaign_id = ?`,
		campaignID).Scan(&v); err != nil {
		return true
	}
	return v == 1
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// BackfillEventsFromMessages derives the append-only event log from messages
// already imported. It runs after BackfillProspects, when person_id is set, so
// it can work set-based instead of resolving an identity per row — the same
// reason the design puts identity work after the raw import rather than inside
// its pagination loop.
//
// Every event carries a deterministic dedupe_key, so running this repeatedly
// tops up rather than double-counting history.
func (s *Store) BackfillEventsFromMessages(workspaceID int64) (int, error) {
	t := fmtTime(now())
	total := 0

	// Sent mail. Note `'sent:' || m.id` concatenates in both engines.
	n, err := s.execCount(`
INSERT OR IGNORE INTO prospect_event
  (person_id, workspace_id, campaign_id, kind, occurred_at, payload, source, dedupe_key, created_at)
SELECT m.person_id, ?, m.campaign_id, ?, m.sent_at, '{}', 'smartlead_import',
       'sent:' || m.id, ?
FROM outbound_messages m
WHERE m.person_id IS NOT NULL
  AND m.status = 'sent'
  AND m.sent_at IS NOT NULL AND m.sent_at <> ''`,
		workspaceID, models.EventSent, t)
	if err != nil {
		return total, err
	}
	total += n

	// Bounces. Smartlead exposes no bounce type, so these are recorded without
	// a hard/soft distinction and must not be read as a job change on their own.
	n, err = s.execCount(`
INSERT OR IGNORE INTO prospect_event
  (person_id, workspace_id, campaign_id, kind, occurred_at, payload, source, dedupe_key, created_at)
SELECT m.person_id, ?, m.campaign_id, ?, COALESCE(NULLIF(m.sent_at,''), ?), '{}', 'smartlead_import',
       'bounce:' || m.id, ?
FROM outbound_messages m
WHERE m.person_id IS NOT NULL AND m.status = 'dead'`,
		workspaceID, models.EventBounce, t, t)
	if err != nil {
		return total, err
	}
	total += n

	// Replies. Auto-replies are excluded here rather than filtered later: an
	// out-of-office is not evidence of interest, and once it is in the event
	// log every downstream score has to remember to ignore it.
	n, err = s.execCount(`
INSERT OR IGNORE INTO prospect_event
  (person_id, workspace_id, campaign_id, kind, occurred_at, payload, source, dedupe_key, created_at)
SELECT r.person_id, ?, 0, ?, r.created_at, '{}', 'smartlead_import',
       'reply:' || r.id, ?
FROM inbound_replies r
WHERE r.person_id IS NOT NULL
  AND COALESCE(r.auto_reply, 0) = 0`,
		workspaceID, models.EventReply, t)
	if err != nil {
		return total, err
	}
	total += n

	return total, nil
}

func (s *Store) execCount(q string, args ...any) (int, error) {
	r, err := s.db.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return int(n), nil
}
