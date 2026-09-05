package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/manishkumar/outreachcrm/internal/identity"
	"github.com/manishkumar/outreachcrm/internal/models"
)

// maxBlockSize caps how many records may share a blocking key before the block
// is skipped. A pathological bucket — a thousand people whose name normalises
// to the same thing — would otherwise cost a million comparisons and produce
// nothing but noise.
const maxBlockSize = 100

// ResolveResult reports what an identity-resolution pass did.
type ResolveResult struct {
	Compared   int
	Merged     int
	Queued     int
	BlocksSeen int
}

// ResolveIdentities compares people that share a blocking key, merging the
// conclusive matches and queueing the probable ones for review.
//
// Merging is deliberately conservative: only evidence that cannot reasonably
// coincide (a shared address, a shared LinkedIn URL, or someone telling us in a
// reply where they moved to) merges without a human. Everything else lands in
// identity_candidate.
func (s *Store) ResolveIdentities(workspaceID int64) (ResolveResult, error) {
	var res ResolveResult

	records, err := s.loadIdentityRecords(workspaceID)
	if err != nil {
		return res, err
	}

	// Block first: comparing all pairs across 65,000 people is two billion
	// comparisons, and every real match shares a name, a local part, a phone
	// number or a LinkedIn URL anyway.
	blocks := map[string][]int{}
	for i, r := range records {
		for _, k := range identity.BlockKeys(r) {
			blocks[k] = append(blocks[k], i)
		}
	}

	seen := map[[2]int64]bool{}
	for _, idx := range blocks {
		if len(idx) < 2 || len(idx) > maxBlockSize {
			continue
		}
		res.BlocksSeen++
		for i := 0; i < len(idx); i++ {
			for j := i + 1; j < len(idx); j++ {
				a, b := records[idx[i]], records[idx[j]]
				if a.PersonID == b.PersonID {
					continue
				}
				// A pair can share several block keys; score it once.
				key := pairKey(a.PersonID, b.PersonID)
				if seen[key] {
					continue
				}
				seen[key] = true
				res.Compared++

				m := identity.Score(a, b)
				switch m.Decision {
				case identity.DecisionMerge:
					// Keep the older record as the survivor: it carries the
					// longest history and the most inbound links.
					winner, loser := a.PersonID, b.PersonID
					if loser < winner {
						winner, loser = loser, winner
					}
					if err := s.MergePeople(winner, loser, m.Score, m.Signals); err != nil {
						return res, err
					}
					res.Merged++
				case identity.DecisionReview:
					if err := s.QueueIdentityCandidate(workspaceID, a.PersonID, b.PersonID, m.Score, m.Signals); err != nil {
						return res, err
					}
					res.Queued++
				}
			}
		}
	}
	return res, nil
}

func pairKey(a, b int64) [2]int64 {
	if a > b {
		a, b = b, a
	}
	return [2]int64{a, b}
}

// loadIdentityRecords reads the facts the matcher needs, one row per live
// person, in a single pass rather than a query per person.
func (s *Store) loadIdentityRecords(workspaceID int64) ([]identity.Record, error) {
	rows, err := s.db.Query(`
SELECT p.id, p.display_name, p.primary_email, p.phone, p.linkedin_url,
       COALESCE(e.company, ''), COALESCE(e.title, ''), e.valid_from, e.valid_to,
       CASE WHEN EXISTS (
         SELECT 1 FROM person_email pe
         WHERE pe.person_id = p.id AND pe.status = ?
       ) THEN 1 ELSE 0 END AS bounced
FROM person p
LEFT JOIN person_employment e
       ON e.person_id = p.id AND e.valid_to IS NULL
WHERE p.merged_into_id IS NULL AND (p.workspace_id = ? OR ? = 0)`,
		models.EmailStatusBounced, workspaceID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []identity.Record
	for rows.Next() {
		var r identity.Record
		var from, to sql.NullString
		var bounced int
		if err := rows.Scan(&r.PersonID, &r.Name, &r.Email, &r.Phone, &r.LinkedIn,
			&r.Company, &r.Title, &from, &to, &bounced); err != nil {
			return nil, err
		}
		r.ValidFrom = parseTimePtr(from)
		r.ValidTo = parseTimePtr(to)
		r.Bounced = bounced == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// MergePeople folds loser into winner.
//
// Nothing is moved or deleted: the merge is recorded as loser.merged_into_id,
// and every lookup resolves through that chain. That is what makes the merge
// exactly reversible — a merge that relocated addresses and events could not be
// undone afterwards, because the evidence of which rows had moved would be gone.
func (s *Store) MergePeople(winnerID, loserID int64, score int, signals []string) error {
	if winnerID == loserID || winnerID == 0 || loserID == 0 {
		return fmt.Errorf("merge needs two distinct people")
	}
	// Resolve both sides first: merging into an already-absorbed record would
	// build a chain that outlives the hop limit in GetPerson.
	winner, err := s.GetPerson(winnerID)
	if err != nil {
		return err
	}
	loser, err := s.GetPerson(loserID)
	if err != nil {
		return err
	}
	if winner.ID == loser.ID {
		return nil // already the same person
	}

	payload, _ := json.Marshal(map[string]any{
		"winner":  winner.ID,
		"loser":   loser.ID,
		"score":   score,
		"signals": signals,
		"loser_snapshot": map[string]any{
			"display_name":  loser.DisplayName,
			"primary_email": loser.PrimaryEmail,
			"phone":         loser.Phone,
			"lead_id":       loser.LeadID,
		},
	})

	t := fmtTime(now())
	if _, err := s.db.Exec(`
UPDATE person SET merged_into_id = ?, updated_at = ? WHERE id = ?`,
		winner.ID, t, loser.ID); err != nil {
		return err
	}

	return s.AppendEvent(models.ProspectEvent{
		PersonID:    winner.ID,
		WorkspaceID: winner.WorkspaceID,
		Kind:        models.EventMerge,
		OccurredAt:  now(),
		Payload:     string(payload),
		Source:      "local",
		DedupeKey:   fmt.Sprintf("merge:%d:%d", winner.ID, loser.ID),
	})
}

// UnmergePerson reverses a merge, restoring the absorbed record as its own
// person. Because merging moved nothing, this is exact.
func (s *Store) UnmergePerson(loserID int64) error {
	t := fmtTime(now())
	_, err := s.db.Exec(`
UPDATE person SET merged_into_id = NULL, updated_at = ? WHERE id = ?`, t, loserID)
	return err
}

// PersonCluster returns the surviving person's id together with every record
// absorbed into it, so history can be gathered across a merge.
func (s *Store) PersonCluster(personID int64) ([]int64, error) {
	root, err := s.GetPerson(personID)
	if err != nil {
		return nil, err
	}
	ids := []int64{root.ID}
	frontier := []int64{root.ID}
	// Chains are short in practice; the hop cap stops a cycle from spinning.
	for hop := 0; hop < 8 && len(frontier) > 0; hop++ {
		var next []int64
		for _, id := range frontier {
			rows, err := s.db.Query(`SELECT id FROM person WHERE merged_into_id = ?`, id)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var child int64
				if err := rows.Scan(&child); err != nil {
					rows.Close()
					return nil, err
				}
				ids = append(ids, child)
				next = append(next, child)
			}
			rows.Close()
		}
		frontier = next
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// QueueIdentityCandidate records a probable match for human review. The pair is
// stored in a stable order so the same two people cannot queue twice.
func (s *Store) QueueIdentityCandidate(workspaceID, a, b int64, score int, signals []string) error {
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	sig, _ := json.Marshal(signals)
	_, err := s.db.Exec(`
INSERT OR IGNORE INTO identity_candidate
  (workspace_id, person_id, other_person_id, score, signals, state, created_at)
VALUES (?,?,?,?,?,?,?)`,
		workspaceID, lo, hi, score, string(sig), models.IdentityPending, fmtTime(now()))
	return err
}

// ListIdentityCandidates returns pairs awaiting a decision, strongest first —
// the reviewer's queue.
func (s *Store) ListIdentityCandidates(workspaceID int64, state string, limit int) ([]models.IdentityCandidate, error) {
	if limit <= 0 {
		limit = 50
	}
	if state == "" {
		state = models.IdentityPending
	}
	rows, err := s.db.Query(`
SELECT id, workspace_id, person_id, other_person_id, score, signals, state, decided_at
FROM identity_candidate
WHERE state = ? AND (workspace_id = ? OR ? = 0)
ORDER BY score DESC, id ASC
LIMIT ?`, state, workspaceID, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.IdentityCandidate
	for rows.Next() {
		var c models.IdentityCandidate
		var decided sql.NullString
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.PersonID, &c.OtherPersonID,
			&c.Score, &c.Signals, &c.State, &decided); err != nil {
			return nil, err
		}
		c.DecidedAt = parseTimePtr(decided)
		out = append(out, c)
	}
	return out, rows.Err()
}

// DecideIdentityCandidate applies a reviewer's decision. Accepting merges the
// pair; rejecting records that they are different people so the pair is not
// offered again.
func (s *Store) DecideIdentityCandidate(id int64, accept bool, decidedBy int64) error {
	var wsID, a, b int64
	var score int
	var sig string
	if err := s.db.QueryRow(`
SELECT workspace_id, person_id, other_person_id, score, signals
FROM identity_candidate WHERE id = ?`, id).Scan(&wsID, &a, &b, &score, &sig); err != nil {
		return err
	}

	state := models.IdentityRejected
	if accept {
		var signals []string
		_ = json.Unmarshal([]byte(sig), &signals)
		winner, loser := a, b
		if loser < winner {
			winner, loser = loser, winner
		}
		if err := s.MergePeople(winner, loser, score, signals); err != nil {
			return err
		}
		state = models.IdentityMerged
	}

	t := fmtTime(now())
	_, err := s.db.Exec(`
UPDATE identity_candidate SET state = ?, decided_by = ?, decided_at = ? WHERE id = ?`,
		state, decidedBy, t, id)
	return err
}

// CountIdentityCandidates reports how many pairs sit in a given state.
func (s *Store) CountIdentityCandidates(workspaceID int64, state string) (int, error) {
	var n int
	err := s.db.QueryRow(`
SELECT COUNT(*) FROM identity_candidate
WHERE state = ? AND (workspace_id = ? OR ? = 0)`, state, workspaceID, workspaceID).Scan(&n)
	return n, err
}

// RecordJobChange closes the current employment row and opens a new one, then
// logs the change. The event is what makes the person resurface in scoring: a
// new role is the strongest reason to make contact again.
func (s *Store) RecordJobChange(personID int64, company, domain, title, source string, at time.Time) error {
	t := fmtTime(at)
	if _, err := s.db.Exec(`
UPDATE person_employment SET valid_to = ?
WHERE person_id = ? AND valid_to IS NULL`, t, personID); err != nil {
		return err
	}
	if _, err := s.db.Exec(`
INSERT INTO person_employment (person_id, company, domain, title, valid_from, valid_to, source, created_at)
VALUES (?,?,?,?,?,NULL,?,?)`,
		personID, company, domain, title, t, source, fmtTime(now())); err != nil {
		return err
	}
	p, err := s.GetPerson(personID)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"company": company, "title": title, "source": source})
	return s.AppendEvent(models.ProspectEvent{
		PersonID:    personID,
		WorkspaceID: p.WorkspaceID,
		Kind:        models.EventJobChange,
		OccurredAt:  at,
		Payload:     string(payload),
		Source:      source,
		DedupeKey:   fmt.Sprintf("jobchange:%d:%s:%s", personID, company, t),
	})
}
