//go:build !zvec

package search

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// sqliteEngine is the default pure-Go FTS5 backend (no CGO).
type sqliteEngine struct {
	db *sql.DB
}

// openEngine opens the lite SQLite FTS5 backend (no CGO / no Zvec).
func openEngine(dataDir string, _ Embedder) (Engine, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "search.db")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	e := &sqliteEngine{db: db}
	if err := e.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return e, nil
}

func (e *sqliteEngine) Backend() string { return "sqlite-fts5" }

func (e *sqliteEngine) migrate() error {
	_, err := e.db.Exec(`
CREATE TABLE IF NOT EXISTS search_docs (
  pk TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  entity_id INTEGER NOT NULL,
  workspace_id INTEGER NOT NULL,
  owner_id INTEGER NOT NULL DEFAULT 0,
  title TEXT NOT NULL DEFAULT '',
  snippet TEXT NOT NULL DEFAULT '',
  href TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL DEFAULT ''
);
CREATE VIRTUAL TABLE IF NOT EXISTS search_fts USING fts5(
  pk, title, content,
  content='search_docs',
  content_rowid='rowid',
  tokenize='porter unicode61'
);
CREATE TRIGGER IF NOT EXISTS search_docs_ai AFTER INSERT ON search_docs BEGIN
  INSERT INTO search_fts(rowid, pk, title, content) VALUES (new.rowid, new.pk, new.title, new.content);
END;
CREATE TRIGGER IF NOT EXISTS search_docs_ad AFTER DELETE ON search_docs BEGIN
  INSERT INTO search_fts(search_fts, rowid, pk, title, content) VALUES('delete', old.rowid, old.pk, old.title, old.content);
END;
CREATE TRIGGER IF NOT EXISTS search_docs_au AFTER UPDATE ON search_docs BEGIN
  INSERT INTO search_fts(search_fts, rowid, pk, title, content) VALUES('delete', old.rowid, old.pk, old.title, old.content);
  INSERT INTO search_fts(rowid, pk, title, content) VALUES (new.rowid, new.pk, new.title, new.content);
END;
`)
	return err
}

func (e *sqliteEngine) Upsert(docs []Document) error {
	if len(docs) == 0 {
		return nil
	}
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`INSERT INTO search_docs(pk, kind, entity_id, workspace_id, owner_id, title, snippet, href, content)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(pk) DO UPDATE SET
			kind=excluded.kind, entity_id=excluded.entity_id, workspace_id=excluded.workspace_id,
			owner_id=excluded.owner_id, title=excluded.title, snippet=excluded.snippet,
			href=excluded.href, content=excluded.content`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, d := range docs {
		pk := PK(d.Kind, d.EntityID)
		if _, err := stmt.Exec(pk, string(d.Kind), d.EntityID, d.WorkspaceID, d.OwnerID, d.Title, d.Snippet, d.Href, d.Content); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (e *sqliteEngine) Delete(kind Kind, entityID int64) error {
	_, err := e.db.Exec(`DELETE FROM search_docs WHERE pk=?`, PK(kind, entityID))
	return err
}

func (e *sqliteEngine) Clear() error {
	_, err := e.db.Exec(`DELETE FROM search_docs`)
	return err
}

func (e *sqliteEngine) Close() error {
	if e.db == nil {
		return nil
	}
	err := e.db.Close()
	e.db = nil
	return err
}

func (e *sqliteEngine) Search(q Query) ([]Result, error) {
	match := sanitizeMatch(q.Text)
	if match == "" {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}

	// Prefix each token for prefix matching: acme → acme*
	tokens := strings.Fields(match)
	for i, t := range tokens {
		tokens[i] = t + "*"
	}
	ftsQuery := strings.Join(tokens, " ")

	sqlQ := `SELECT d.kind, d.entity_id, d.workspace_id, d.owner_id, d.title, d.snippet, d.href, bm25(search_fts) AS score
		FROM search_fts
		JOIN search_docs d ON d.rowid = search_fts.rowid
		WHERE search_fts MATCH ? AND d.workspace_id = ?`
	args := []any{ftsQuery, q.WorkspaceID}
	if !q.Admin && q.OwnerID > 0 {
		sqlQ += ` AND (d.owner_id = 0 OR d.owner_id = ?)`
		args = append(args, q.OwnerID)
	}
	sqlQ += ` ORDER BY score LIMIT ?`
	args = append(args, limit)

	rows, err := e.db.Query(sqlQ, args...)
	if err != nil {
		// Fallback: plain LIKE if FTS query syntax fails
		return e.searchLike(q, match, limit)
	}
	defer rows.Close()
	var out []Result
	for rows.Next() {
		var r Result
		var kind string
		var score float64
		if err := rows.Scan(&kind, &r.EntityID, &r.WorkspaceID, &r.OwnerID, &r.Title, &r.Snippet, &r.Href, &score); err != nil {
			return nil, err
		}
		r.Kind = Kind(kind)
		r.Score = -score // bm25: lower is better
		out = append(out, r)
	}
	return out, rows.Err()
}

func (e *sqliteEngine) searchLike(q Query, match string, limit int) ([]Result, error) {
	like := "%" + strings.ToLower(match) + "%"
	sqlQ := `SELECT kind, entity_id, workspace_id, owner_id, title, snippet, href, 0
		FROM search_docs
		WHERE workspace_id = ? AND (lower(title) LIKE ? OR lower(content) LIKE ?)`
	args := []any{q.WorkspaceID, like, like}
	if !q.Admin && q.OwnerID > 0 {
		sqlQ += ` AND (owner_id = 0 OR owner_id = ?)`
		args = append(args, q.OwnerID)
	}
	sqlQ += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	// id column doesn't exist — use entity_id
	sqlQ = strings.Replace(sqlQ, `ORDER BY id DESC`, `ORDER BY entity_id DESC`, 1)

	rows, err := e.db.Query(sqlQ, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	var out []Result
	for rows.Next() {
		var r Result
		var kind string
		var score float64
		if err := rows.Scan(&kind, &r.EntityID, &r.WorkspaceID, &r.OwnerID, &r.Title, &r.Snippet, &r.Href, &score); err != nil {
			return nil, err
		}
		r.Kind = Kind(kind)
		r.Score = score
		out = append(out, r)
	}
	return out, rows.Err()
}
