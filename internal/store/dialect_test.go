package store

import (
	"os"
	"strings"
	"testing"
)

func TestTranslateSQLiteIsPassthrough(t *testing.T) {
	q := `SELECT id FROM leads WHERE owner_id=? AND email=?`
	if got := translate(q, dialectSQLite); got != q {
		t.Fatalf("sqlite should not rewrite:\n got %q\nwant %q", got, q)
	}
}

func TestRewritePlaceholders(t *testing.T) {
	cases := []struct{ in, want string }{
		{`SELECT * FROM leads WHERE id=?`, `SELECT * FROM leads WHERE id=$1`},
		{`INSERT INTO t(a,b,c) VALUES(?,?,?)`, `INSERT INTO t(a,b,c) VALUES($1,$2,$3)`},
		// A literal ? inside a string must survive untouched.
		{`UPDATE t SET note='huh?' WHERE id=?`, `UPDATE t SET note='huh?' WHERE id=$1`},
		// Mixed literals and placeholders, as in the real UPDATE statements.
		{
			`UPDATE outbound_messages SET status='dead', last_error='unsubscribed' WHERE campaign_id=? AND lead_id=?`,
			`UPDATE outbound_messages SET status='dead', last_error='unsubscribed' WHERE campaign_id=$1 AND lead_id=$2`,
		},
		// Placeholder numbering must keep counting past 9.
		{
			`INSERT INTO t VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			`INSERT INTO t VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		},
		// Comments are not placeholder territory.
		{"-- why? because\nSELECT ?", "-- why? because\nSELECT $1"},
		{"/* what? */ SELECT ?", "/* what? */ SELECT $1"},
		// Quoted identifiers behave like literals.
		{`SELECT "od?d" FROM t WHERE a=?`, `SELECT "od?d" FROM t WHERE a=$1`},
	}
	for _, c := range cases {
		if got := rewritePlaceholders(c.in); got != c.want {
			t.Errorf("rewritePlaceholders(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestRewriteInsertOrIgnore(t *testing.T) {
	got := toPostgres(`INSERT OR IGNORE INTO audience_members(audience_id, lead_id, added_at) VALUES(?,?,?)`)
	want := `INSERT INTO audience_members(audience_id, lead_id, added_at) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`
	if got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
	// An existing ON CONFLICT clause must not be doubled up.
	q := `INSERT INTO t(a) VALUES(?) ON CONFLICT(a) DO UPDATE SET a=excluded.a`
	if strings.Count(toPostgres(q), "ON CONFLICT") != 1 {
		t.Fatalf("duplicated ON CONFLICT: %s", toPostgres(q))
	}
}

func TestRewriteDDL(t *testing.T) {
	got := toPostgres("CREATE TABLE IF NOT EXISTS leads (\n  id INTEGER PRIMARY KEY AUTOINCREMENT,\n  google_rating REAL DEFAULT 0\n);")
	if !strings.Contains(got, "id BIGSERIAL PRIMARY KEY") {
		t.Errorf("surrogate id should become BIGSERIAL: %s", got)
	}
	if !strings.Contains(got, "google_rating DOUBLE PRECISION") {
		t.Errorf("REAL should become DOUBLE PRECISION: %s", got)
	}

	// schema_migrations.version carries a real value, not a sequence.
	got = toPostgres(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)`)
	if strings.Contains(got, "BIGSERIAL") {
		t.Errorf("schema_migrations.version must stay INTEGER, got: %s", got)
	}

	// Duplicate ADD COLUMN must be a no-op rather than an error, because an
	// error would abort the surrounding migration transaction.
	got = toPostgres(`ALTER TABLE leads ADD COLUMN owner_id INTEGER`)
	if !strings.Contains(got, "ADD COLUMN IF NOT EXISTS owner_id") {
		t.Errorf("want IF NOT EXISTS, got: %s", got)
	}
	// Applying it twice must not stack the guard.
	if strings.Count(toPostgres(got), "IF NOT EXISTS") != 1 {
		t.Errorf("guard was doubled: %s", toPostgres(got))
	}
}

func TestWithReturningID(t *testing.T) {
	if got := withReturningID(`INSERT INTO t(a) VALUES($1)`); got != `INSERT INTO t(a) VALUES($1) RETURNING id` {
		t.Errorf("got %q", got)
	}
	if got := withReturningID(`INSERT INTO t(a) VALUES($1);`); got != `INSERT INTO t(a) VALUES($1) RETURNING id` {
		t.Errorf("trailing semicolon: got %q", got)
	}
	q := `INSERT INTO t(a) VALUES($1) RETURNING id`
	if got := withReturningID(q); got != q {
		t.Errorf("existing RETURNING must be left alone: got %q", got)
	}
}

func TestIsPostgresURL(t *testing.T) {
	for _, u := range []string{"postgres://a/b", "postgresql://a/b", "  POSTGRES://a/b "} {
		if !IsPostgresURL(u) {
			t.Errorf("%q should be postgres", u)
		}
	}
	for _, u := range []string{"", "data", "sqlite://x", "mysql://a/b"} {
		if IsPostgresURL(u) {
			t.Errorf("%q should not be postgres", u)
		}
	}
}

// TestPostgresRoundTrip exercises the paths that only differ under Postgres:
// migrations, InsertID's RETURNING clause, and INSERT OR IGNORE. Set
// TEST_PG_URL to a scratch database to run it.
func TestPostgresRoundTrip(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set")
	}
	s, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if s.Backend() != "postgres" {
		t.Fatalf("backend = %q", s.Backend())
	}

	// InsertID must return a real id where SQLite would use LastInsertId.
	id, err := s.db.InsertID(`INSERT INTO workspaces(name, created_at) VALUES(?,?)`, "rt-test", fmtTime(now()))
	if err != nil {
		t.Fatalf("InsertID: %v", err)
	}
	if id <= 0 {
		t.Fatalf("InsertID returned %d, want a positive id", id)
	}

	// INSERT OR IGNORE must not error on a duplicate key.
	for i := 0; i < 2; i++ {
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO app_settings(key, value) VALUES(?,?)`, "rt_probe", "1"); err != nil {
			t.Fatalf("INSERT OR IGNORE attempt %d: %v", i+1, err)
		}
	}

	// A soft transaction must survive a failing statement, the way the
	// migrator relies on.
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	tx.soft = true
	if _, err := tx.Exec(`INSERT INTO no_such_table(a) VALUES(1)`); err == nil {
		t.Fatal("expected the bad statement to fail")
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM workspaces`).Scan(&n); err != nil {
		t.Fatalf("transaction was poisoned by an ignored error: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	_, _ = s.db.Exec(`DELETE FROM workspaces WHERE name=?`, "rt-test")
	_, _ = s.db.Exec(`DELETE FROM app_settings WHERE key=?`, "rt_probe")
}
