// Command sqlite-to-postgres copies an existing OutReachCRM SQLite database
// into a Postgres one, preserving row ids.
//
// The Postgres schema is created by the store's own migrator, so the target
// must be reachable and will be migrated automatically. Copy is additive per
// table: use --truncate to replace existing rows instead of appending.
//
//	sqlite-to-postgres \
//	  --data-dir /data \
//	  --database-url "$DATABASE_URL" \
//	  --dry-run
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/manishkumar/outreachcrm/internal/store"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// copyOrder lists tables parents-first. Foreign keys are disabled during the
// copy anyway, but a stable order keeps the log readable and the run
// reproducible.
var copyOrder = []string{
	"workspaces",
	"users",
	"leads",
	"campaigns",
	"sequence_steps",
	"campaign_leads",
	"email_accounts",
	"email_templates",
	"outbound_messages",
	"inbound_replies",
	"audiences",
	"audience_members",
	"campaign_audience_runs",
	"suppressions",
	"app_settings",
	"audit_logs",
	"domain_checks",
	"blacklist_checks",
	"deliverability_decisions",
	"recipient_stats",
	"isp_send_log",
	"llm_usage",
	"marketing_smtp",
	"workspace_ai",
	"telephony_accounts",
	"call_logs",
	"oauth_states",
	"smartlead_map",
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	dataDir := flag.String("data-dir", env("DATA_DIR", "data"), "directory holding app.db")
	databaseURL := flag.String("database-url", os.Getenv("DATABASE_URL"), "postgres:// target URL")
	batch := flag.Int("batch", 500, "rows per INSERT")
	truncate := flag.Bool("truncate", false, "delete existing rows in each target table first")
	dryRun := flag.Bool("dry-run", false, "count source rows and exit without writing")
	flag.Parse()

	if !store.IsPostgresURL(*databaseURL) {
		slog.Error("--database-url must be a postgres:// URL")
		os.Exit(1)
	}

	srcPath := filepath.Join(*dataDir, "app.db")
	if _, err := os.Stat(srcPath); err != nil {
		slog.Error("source database", "path", srcPath, "err", err)
		os.Exit(1)
	}
	src, err := sql.Open("sqlite", srcPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		slog.Error("open sqlite", "err", err)
		os.Exit(1)
	}
	defer src.Close()

	// Let the store migrator build the Postgres schema before we copy into it.
	st, err := store.OpenPostgres(*databaseURL)
	if err != nil {
		slog.Error("open postgres", "err", err)
		os.Exit(1)
	}
	_ = st.Close()

	dst, err := sql.Open("postgres", *databaseURL)
	if err != nil {
		slog.Error("open postgres", "err", err)
		os.Exit(1)
	}
	defer dst.Close()
	if err := dst.Ping(); err != nil {
		slog.Error("postgres ping", "err", err)
		os.Exit(1)
	}

	if *dryRun {
		total := 0
		for _, table := range copyOrder {
			n, err := countRows(src, table)
			if err != nil {
				continue
			}
			if n > 0 {
				slog.Info("source rows", "table", table, "rows", n)
				total += n
			}
		}
		slog.Info("dry run complete", "tables", len(copyOrder), "rows", total)
		return
	}

	// Foreign keys would force a strict ordering and reject rows whose parents
	// arrive later. The source database already satisfies them.
	if _, err := dst.Exec(`SET session_replication_role = replica`); err != nil {
		slog.Warn("could not disable FK triggers; relying on table order", "err", err)
	}

	start := time.Now()
	grand := 0
	for _, table := range copyOrder {
		n, err := copyTable(src, dst, table, *batch, *truncate)
		if err != nil {
			slog.Error("copy failed", "table", table, "err", err)
			os.Exit(1)
		}
		if n > 0 {
			slog.Info("copied", "table", table, "rows", n)
		}
		grand += n
	}

	// Row ids were preserved, so every sequence has to be advanced past them or
	// the next insert collides.
	for _, table := range copyOrder {
		if err := resyncSequence(dst, table); err != nil {
			slog.Warn("sequence resync", "table", table, "err", err)
		}
	}

	_, _ = dst.Exec(`SET session_replication_role = DEFAULT`)
	slog.Info("migration complete", "rows", grand, "took", time.Since(start).String())
}

func countRows(db *sql.DB, table string) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&n)
	return n, err
}

// copyTable streams one table across. Columns are intersected so a column that
// exists in only one of the two schemas cannot break the run.
func copyTable(src, dst *sql.DB, table string, batch int, truncate bool) (int, error) {
	srcCols, err := columns(src, `SELECT * FROM "`+table+`" LIMIT 0`)
	if err != nil {
		// Table absent from this SQLite database; nothing to copy.
		return 0, nil
	}
	dstCols, err := columns(dst, `SELECT * FROM "`+table+`" LIMIT 0`)
	if err != nil {
		return 0, fmt.Errorf("target table missing: %w", err)
	}
	shared := intersect(srcCols, dstCols)
	if len(shared) == 0 {
		return 0, nil
	}
	if skipped := difference(srcCols, dstCols); len(skipped) > 0 {
		slog.Warn("columns not present in target, skipped", "table", table, "columns", strings.Join(skipped, ","))
	}

	if truncate {
		if _, err := dst.Exec(`DELETE FROM "` + table + `"`); err != nil {
			return 0, err
		}
	}

	quoted := make([]string, len(shared))
	for i, c := range shared {
		quoted[i] = `"` + c + `"`
	}
	selectList := strings.Join(quoted, ", ")

	rows, err := src.Query(`SELECT ` + selectList + ` FROM "` + table + `"`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	total := 0
	pending := make([]any, 0, batch*len(shared))
	nRows := 0

	flush := func() error {
		if nRows == 0 {
			return nil
		}
		stmt := buildInsert(table, quoted, nRows)
		if _, err := dst.Exec(stmt, pending...); err != nil {
			return err
		}
		total += nRows
		pending = pending[:0]
		nRows = 0
		return nil
	}

	for rows.Next() {
		vals := make([]any, len(shared))
		ptrs := make([]any, len(shared))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return total, err
		}
		for _, v := range vals {
			// SQLite hands back TEXT as []byte; Postgres would store that as
			// bytea, so normalize to string.
			if b, ok := v.([]byte); ok {
				pending = append(pending, string(b))
				continue
			}
			pending = append(pending, v)
		}
		nRows++
		if nRows >= batch {
			if err := flush(); err != nil {
				return total, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return total, err
	}
	if err := flush(); err != nil {
		return total, err
	}
	return total, nil
}

// buildInsert produces a multi-row INSERT with $N placeholders. Conflicting ids
// are skipped so a re-run tops up rather than failing.
func buildInsert(table string, quotedCols []string, nRows int) string {
	var b strings.Builder
	b.WriteString(`INSERT INTO "`)
	b.WriteString(table)
	b.WriteString(`" (`)
	b.WriteString(strings.Join(quotedCols, ", "))
	b.WriteString(") VALUES ")
	n := 1
	for r := 0; r < nRows; r++ {
		if r > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('(')
		for c := range quotedCols {
			if c > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "$%d", n)
			n++
		}
		b.WriteByte(')')
	}
	b.WriteString(" ON CONFLICT DO NOTHING")
	return b.String()
}

func columns(db *sql.DB, probe string) ([]string, error) {
	rows, err := db.Query(probe)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return rows.Columns()
}

func intersect(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, v := range b {
		set[strings.ToLower(v)] = true
	}
	var out []string
	for _, v := range a {
		if set[strings.ToLower(v)] {
			out = append(out, v)
		}
	}
	return out
}

func difference(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, v := range b {
		set[strings.ToLower(v)] = true
	}
	var out []string
	for _, v := range a {
		if !set[strings.ToLower(v)] {
			out = append(out, v)
		}
	}
	return out
}

// resyncSequence advances a table's id sequence past the copied rows. Tables
// keyed naturally (app_settings, smartlead_map, ...) have no id column and are
// skipped; pg_get_serial_sequence errors rather than returning NULL for those,
// so the column has to be checked first.
func resyncSequence(db *sql.DB, table string) error {
	var hasID bool
	if err := db.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		   WHERE table_schema='public' AND table_name=$1 AND column_name='id')`,
		table,
	).Scan(&hasID); err != nil {
		return err
	}
	if !hasID {
		return nil
	}
	var seq sql.NullString
	if err := db.QueryRow(`SELECT pg_get_serial_sequence($1, 'id')`, table).Scan(&seq); err != nil {
		return err
	}
	if !seq.Valid || seq.String == "" {
		return nil
	}
	_, err := db.Exec(
		`SELECT setval($1, COALESCE((SELECT MAX(id) FROM "`+table+`"), 0) + 1, false)`,
		seq.String,
	)
	return err
}

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
