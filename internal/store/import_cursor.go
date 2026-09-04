package store

import (
	"database/sql"
	"time"
)

// Import resources tracked by a cursor.
const (
	ImportSourceSmartlead = "smartlead"

	ResourceLeads      = "leads"
	ResourceStatistics = "statistics"
	ResourceHistory    = "history"
	ResourceCampaigns  = "campaigns"
)

// ImportCursor is the resume point for one (source, resource, campaign) stream.
type ImportCursor struct {
	Source      string
	Resource    string
	CampaignID  int64
	NextOffset  int
	RowsDone    int
	TotalHint   int
	Completed   bool
	CompletedAt *time.Time
	LastError   string
	Attempts    int
}

// GetCursor returns the resume point for a stream. A stream that has never run
// comes back zeroed, which is the correct place to start.
func (s *Store) GetCursor(source, resource string, campaignID int64) (ImportCursor, error) {
	c := ImportCursor{Source: source, Resource: resource, CampaignID: campaignID}
	var completed sql.NullString
	err := s.db.QueryRow(`
SELECT next_offset, rows_done, total_hint, completed_at, last_error, attempts
FROM import_cursor
WHERE source = ? AND resource = ? AND campaign_id = ?`,
		source, resource, campaignID).Scan(
		&c.NextOffset, &c.RowsDone, &c.TotalHint, &completed, &c.LastError, &c.Attempts)
	if err == sql.ErrNoRows {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	c.CompletedAt = parseTimePtr(completed)
	c.Completed = c.CompletedAt != nil
	return c, nil
}

// SaveCursorProgress records that a page has been committed. Call this after
// the page's rows are durably written, never before: the offset is a promise
// that everything below it is already stored.
func (s *Store) SaveCursorProgress(source, resource string, campaignID int64, nextOffset, rowsDone, totalHint int) error {
	t := fmtTime(now())
	if _, err := s.db.Exec(`
INSERT OR IGNORE INTO import_cursor
  (source, resource, campaign_id, next_offset, rows_done, total_hint, updated_at)
VALUES (?,?,?,?,?,?,?)`,
		source, resource, campaignID, nextOffset, rowsDone, totalHint, t); err != nil {
		return err
	}
	_, err := s.db.Exec(`
UPDATE import_cursor
SET next_offset = ?, rows_done = ?, total_hint = ?, last_error = '', updated_at = ?
WHERE source = ? AND resource = ? AND campaign_id = ?`,
		nextOffset, rowsDone, totalHint, t, source, resource, campaignID)
	return err
}

// CompleteCursor marks a stream finished so a re-run skips it entirely.
func (s *Store) CompleteCursor(source, resource string, campaignID int64) error {
	t := fmtTime(now())
	if _, err := s.db.Exec(`
INSERT OR IGNORE INTO import_cursor
  (source, resource, campaign_id, updated_at) VALUES (?,?,?,?)`,
		source, resource, campaignID, t); err != nil {
		return err
	}
	_, err := s.db.Exec(`
UPDATE import_cursor SET completed_at = ?, last_error = '', updated_at = ?
WHERE source = ? AND resource = ? AND campaign_id = ?`,
		t, t, source, resource, campaignID)
	return err
}

// FailCursor records why a stream stopped, leaving next_offset intact so the
// retry resumes rather than restarts.
func (s *Store) FailCursor(source, resource string, campaignID int64, cause string) error {
	t := fmtTime(now())
	if _, err := s.db.Exec(`
INSERT OR IGNORE INTO import_cursor
  (source, resource, campaign_id, updated_at) VALUES (?,?,?,?)`,
		source, resource, campaignID, t); err != nil {
		return err
	}
	_, err := s.db.Exec(`
UPDATE import_cursor
SET last_error = ?, attempts = attempts + 1, updated_at = ?
WHERE source = ? AND resource = ? AND campaign_id = ?`,
		truncateText(cause, 500), t, source, resource, campaignID)
	return err
}

// ResetCursor clears a stream's progress so the next run starts from scratch.
// Row-level deduplication makes that safe; it is only ever wasted work.
func (s *Store) ResetCursor(source, resource string, campaignID int64) error {
	_, err := s.db.Exec(`
DELETE FROM import_cursor WHERE source = ? AND resource = ? AND campaign_id = ?`,
		source, resource, campaignID)
	return err
}

// ListOpenCursors returns streams that started but never finished — the work an
// interrupted import still owes.
func (s *Store) ListOpenCursors(source string) ([]ImportCursor, error) {
	rows, err := s.db.Query(`
SELECT resource, campaign_id, next_offset, rows_done, total_hint, last_error, attempts
FROM import_cursor
WHERE source = ? AND completed_at IS NULL
ORDER BY resource, campaign_id`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImportCursor
	for rows.Next() {
		c := ImportCursor{Source: source}
		if err := rows.Scan(&c.Resource, &c.CampaignID, &c.NextOffset,
			&c.RowsDone, &c.TotalHint, &c.LastError, &c.Attempts); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
