package store

import (
	"database/sql"
	"strings"
	"sync"
)

// dialect identifies the SQL flavor behind a handle.
type dialect int

const (
	dialectSQLite dialect = iota
	dialectPostgres
)

// Every query in this package is written in the SQLite flavor: `?` placeholders,
// `INSERT OR IGNORE`, `INTEGER PRIMARY KEY AUTOINCREMENT`. db and tx translate
// that flavor to the active dialect on the way to the driver, so the ~250 call
// sites in this package stay dialect-free.
//
// Column types are deliberately left alone. Times are already stored as RFC3339
// TEXT (see fmtTime/parseTime), which sorts and compares identically in both
// engines, and booleans stay INTEGER — database/sql converts INTEGER 0/1 into a
// Go bool through driver.Bool, so model fields scan unchanged.

// isPostgresScheme reports whether a URL selects the Postgres backend. It
// matches internal/search's PostgresSearchURL so one DATABASE_URL drives both.
func isPostgresScheme(databaseURL string) bool {
	u := strings.ToLower(strings.TrimSpace(databaseURL))
	return strings.HasPrefix(u, "postgres://") || strings.HasPrefix(u, "postgresql://")
}

// db wraps *sql.DB with dialect translation. It is named to shadow the embedded
// handle's methods, so `s.db.Exec(...)` routes through the rewriter.
type db struct {
	sdb *sql.DB
	d   dialect
}

func (d *db) postgres() bool { return d.d == dialectPostgres }

func (d *db) Exec(q string, args ...any) (sql.Result, error) {
	return d.sdb.Exec(translate(q, d.d), args...)
}

func (d *db) Query(q string, args ...any) (*sql.Rows, error) {
	return d.sdb.Query(translate(q, d.d), args...)
}

func (d *db) QueryRow(q string, args ...any) *sql.Row {
	return d.sdb.QueryRow(translate(q, d.d), args...)
}

func (d *db) Begin() (*tx, error) {
	t, err := d.sdb.Begin()
	if err != nil {
		return nil, err
	}
	return &tx{stx: t, d: d.d}, nil
}

func (d *db) Ping() error  { return d.sdb.Ping() }
func (d *db) Close() error { return d.sdb.Close() }

// InsertID runs an INSERT and returns the new row id. SQLite uses
// LastInsertId; Postgres has no such concept, so the statement gets a
// RETURNING clause instead.
func (d *db) InsertID(q string, args ...any) (int64, error) {
	if d.postgres() {
		var id int64
		err := d.sdb.QueryRow(withReturningID(translate(q, d.d)), args...).Scan(&id)
		return id, err
	}
	res, err := d.sdb.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// tx is the transaction counterpart of db.
type tx struct {
	stx *sql.Tx
	d   dialect
	// soft makes each Exec savepoint-protected on Postgres. The migrator
	// deliberately ignores errors from statements that may already have been
	// applied (duplicate ADD COLUMN, re-seeded settings). SQLite shrugs those
	// off, but in Postgres one error aborts the entire transaction and every
	// later statement fails with "current transaction is aborted". A savepoint
	// per statement restores the SQLite-like behavior: the failure is returned
	// to the caller but the transaction stays usable.
	soft bool
	sp   int
}

func (t *tx) postgres() bool { return t.d == dialectPostgres }

func (t *tx) Exec(q string, args ...any) (sql.Result, error) {
	q = translate(q, t.d)
	if !t.soft || !t.postgres() {
		return t.stx.Exec(q, args...)
	}
	t.sp++
	name := "sp_" + itoa(t.sp)
	if _, err := t.stx.Exec("SAVEPOINT " + name); err != nil {
		return nil, err
	}
	res, err := t.stx.Exec(q, args...)
	if err != nil {
		_, _ = t.stx.Exec("ROLLBACK TO SAVEPOINT " + name)
		return nil, err
	}
	_, _ = t.stx.Exec("RELEASE SAVEPOINT " + name)
	return res, nil
}

func (t *tx) Query(q string, args ...any) (*sql.Rows, error) {
	return t.stx.Query(translate(q, t.d), args...)
}

func (t *tx) QueryRow(q string, args ...any) *sql.Row {
	return t.stx.QueryRow(translate(q, t.d), args...)
}

func (t *tx) Commit() error   { return t.stx.Commit() }
func (t *tx) Rollback() error { return t.stx.Rollback() }

func (t *tx) InsertID(q string, args ...any) (int64, error) {
	if t.postgres() {
		var id int64
		err := t.stx.QueryRow(withReturningID(translate(q, t.d)), args...).Scan(&id)
		return id, err
	}
	res, err := t.stx.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// translate rewrites SQLite-flavored SQL for the target dialect. Results are
// cached because every query in this package is a compile-time constant.
var translateCache sync.Map // string -> string

func translate(q string, d dialect) string {
	if d != dialectPostgres {
		return q
	}
	if v, ok := translateCache.Load(q); ok {
		return v.(string)
	}
	out := toPostgres(q)
	translateCache.Store(q, out)
	return out
}

func toPostgres(q string) string {
	q = rewriteInsertOrIgnore(q)
	q = rewriteDDL(q)
	return rewritePlaceholders(q)
}

// rewritePlaceholders turns `?` into `$1`, `$2`, ... skipping anything inside
// string literals, quoted identifiers, or comments.
func rewritePlaceholders(q string) string {
	var b strings.Builder
	b.Grow(len(q) + 8)
	n := 0
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch c {
		case '\'', '"':
			quote := c
			b.WriteByte(c)
			i++
			for i < len(q) {
				b.WriteByte(q[i])
				// '' and "" are escaped quotes, not terminators.
				if q[i] == quote {
					if i+1 < len(q) && q[i+1] == quote {
						i += 2
						if i-1 < len(q) {
							b.WriteByte(q[i-1])
						}
						continue
					}
					break
				}
				i++
			}
		case '-':
			if i+1 < len(q) && q[i+1] == '-' {
				for i < len(q) && q[i] != '\n' {
					b.WriteByte(q[i])
					i++
				}
				if i < len(q) {
					b.WriteByte(q[i])
				}
			} else {
				b.WriteByte(c)
			}
		case '/':
			if i+1 < len(q) && q[i+1] == '*' {
				b.WriteString("/*")
				i += 2
				for i < len(q) && !(q[i] == '*' && i+1 < len(q) && q[i+1] == '/') {
					b.WriteByte(q[i])
					i++
				}
				if i+1 < len(q) {
					b.WriteString("*/")
					i++
				}
			} else {
				b.WriteByte(c)
			}
		case '?':
			n++
			b.WriteByte('$')
			b.WriteString(itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// rewriteInsertOrIgnore maps SQLite's INSERT OR IGNORE onto ON CONFLICT DO
// NOTHING. The untargeted form covers any unique or exclusion violation, which
// is what INSERT OR IGNORE does.
func rewriteInsertOrIgnore(q string) string {
	idx := indexFold(q, "INSERT OR IGNORE INTO")
	if idx < 0 {
		return q
	}
	q = q[:idx] + "INSERT INTO" + q[idx+len("INSERT OR IGNORE INTO"):]
	trimmed := strings.TrimRight(q, " \t\r\n")
	semi := strings.HasSuffix(trimmed, ";")
	trimmed = strings.TrimSuffix(trimmed, ";")
	if indexFold(trimmed, "ON CONFLICT") >= 0 {
		return q
	}
	trimmed += " ON CONFLICT DO NOTHING"
	if semi {
		trimmed += ";"
	}
	return trimmed
}

// rewriteDDL adapts SQLite schema syntax to Postgres. Only the constructs this
// package actually emits are handled.
func rewriteDDL(q string) string {
	if indexFold(q, "CREATE TABLE") < 0 && indexFold(q, "ALTER TABLE") < 0 {
		return q
	}
	// Only surrogate `id` keys become sequences. Other INTEGER PRIMARY KEY
	// columns (schema_migrations.version) carry real values and must stay.
	q = replaceFold(q, "id INTEGER PRIMARY KEY AUTOINCREMENT", "id BIGSERIAL PRIMARY KEY")
	q = replaceFold(q, "id INTEGER PRIMARY KEY", "id BIGSERIAL PRIMARY KEY")
	q = replaceFold(q, " REAL", " DOUBLE PRECISION")
	// SQLite tolerates a duplicate ADD COLUMN by erroring, which the migrator
	// ignores. In Postgres an error aborts the whole transaction, so the
	// statement has to be a no-op instead.
	if i := indexFold(q, "ADD COLUMN"); i >= 0 && indexFold(q, "ADD COLUMN IF NOT EXISTS") < 0 {
		q = q[:i] + "ADD COLUMN IF NOT EXISTS" + q[i+len("ADD COLUMN"):]
	}
	return q
}

func indexFold(s, sub string) int {
	return strings.Index(strings.ToUpper(s), strings.ToUpper(sub))
}

func replaceFold(s, old, new string) string {
	for {
		i := indexFold(s, old)
		if i < 0 {
			return s
		}
		s = s[:i] + new + s[i+len(old):]
	}
}

// withReturningID appends RETURNING id when the statement lacks one.
func withReturningID(q string) string {
	if indexFold(q, "RETURNING") >= 0 {
		return q
	}
	trimmed := strings.TrimRight(q, " \t\r\n")
	trimmed = strings.TrimSuffix(trimmed, ";")
	return trimmed + " RETURNING id"
}
