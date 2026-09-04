package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *db
}

// Open opens the SQLite store under dataDir.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, "app.db")
	sdb, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// SQLite serializes writers; a single connection avoids SQLITE_BUSY.
	sdb.SetMaxOpenConns(1)
	return finishOpen(&db{sdb: sdb, d: dialectSQLite})
}

// OpenPostgres opens the store against Postgres. Unlike SQLite it takes a real
// connection pool, which is what lets bulk work (the Smartlead import) commit
// concurrently instead of serializing behind one writer.
func OpenPostgres(databaseURL string) (*Store, error) {
	sdb, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	sdb.SetMaxOpenConns(25)
	sdb.SetMaxIdleConns(5)
	sdb.SetConnMaxLifetime(time.Hour)
	if err := sdb.Ping(); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("postgres connect: %w", err)
	}
	return finishOpen(&db{sdb: sdb, d: dialectPostgres})
}

// OpenAuto selects the backend: Postgres when databaseURL is a postgres:// URL,
// otherwise SQLite under dataDir.
func OpenAuto(dataDir, databaseURL string) (*Store, error) {
	if IsPostgresURL(databaseURL) {
		return OpenPostgres(databaseURL)
	}
	return Open(dataDir)
}

// IsPostgresURL reports whether the URL selects the Postgres backend.
func IsPostgresURL(databaseURL string) bool {
	return isPostgresScheme(databaseURL)
}

func finishOpen(d *db) (*Store, error) {
	s := &Store{db: d}
	if err := s.migrate(); err != nil {
		_ = d.Close()
		return nil, err
	}
	return s, nil
}

// Backend names the active engine, for logging and /readyz.
func (s *Store) Backend() string {
	if s.db.postgres() {
		return "postgres"
	}
	return "sqlite"
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping() error { return s.db.Ping() }

func now() time.Time             { return time.Now().UTC() }
func fmtTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }
func nullJSON(s string) string {
	if s == "" {
		return "[]"
	}
	return s
}
func defaultStatus(s string) string {
	if s == "" {
		return "pending"
	}
	return s
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func parseTimePtr(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseTime(s.String)
	return &t
}

type scannable interface {
	Scan(dest ...any) error
}
