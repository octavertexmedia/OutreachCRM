package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/manishkumar/outreachcrm/internal/smartlead"
	"github.com/manishkumar/outreachcrm/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	dataDir := flag.String("data-dir", env("DATA_DIR", "data"), "OutReachCRM DATA_DIR (SQLite lives here)")
	apiKey := flag.String("api-key", os.Getenv("SMARTLEAD_API_KEY"), "Smartlead API key (or SMARTLEAD_API_KEY)")
	keyFile := flag.String("api-key-file", "", "file containing Smartlead API key (used if --api-key empty)")
	workspace := flag.String("workspace", "Smartlead", "target workspace name (created if missing)")
	dryRun := flag.Bool("dry-run", false, "fetch only; do not write to SQLite")
	backup := flag.Bool("backup", true, "copy app.db next to DATA_DIR/backups before writing (SQLite only)")
	databaseURL := flag.String("database-url", os.Getenv("DATABASE_URL"), "postgres:// URL; when set the import writes to Postgres instead of SQLite")
	flag.Parse()

	key := strings.TrimSpace(*apiKey)
	if key == "" && *keyFile != "" {
		b, err := os.ReadFile(*keyFile)
		if err != nil {
			slog.Error("read api key file", "err", err)
			os.Exit(1)
		}
		key = strings.TrimSpace(string(b))
	}
	if key == "" {
		slog.Error("SMARTLEAD_API_KEY or --api-key / --api-key-file required")
		os.Exit(1)
	}

	st, err := store.OpenAuto(*dataDir, *databaseURL)
	if err != nil {
		slog.Error("store open", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	slog.Info("store ready", "backend", st.Backend())

	if !*dryRun && *backup && !store.IsPostgresURL(*databaseURL) {
		if err := backupDB(*dataDir); err != nil {
			slog.Error("backup", "err", err)
			os.Exit(1)
		}
	}

	client := smartlead.New(key)
	stats, err := smartlead.Run(st, client, smartlead.Options{
		WorkspaceName: *workspace,
		DryRun:        *dryRun,
	})
	if err != nil {
		slog.Error("import failed", "err", err)
		os.Exit(1)
	}
	slog.Info("smartlead import complete",
		"dry_run", *dryRun,
		"workspace_id", stats.WorkspaceID,
		"owner_id", stats.OwnerID,
		"campaigns", stats.Campaigns,
		"steps", stats.Steps,
		"accounts", stats.Accounts,
		"leads_created", stats.Leads,
		"enrollments", stats.Enrollments,
		"outbound_sent", stats.Outbound,
		"replies", stats.Replies,
		"suppressions", stats.Suppressions,
		"existing_leads", stats.SkippedDupes,
		"errors", len(stats.Errors),
	)
	for _, e := range stats.Errors {
		slog.Warn("import issue", "detail", e)
	}
	fmt.Printf("imported workspace=%d campaigns=%d leads=%d enroll=%d sent=%d replies=%d accounts=%d\n",
		stats.WorkspaceID, stats.Campaigns, stats.Leads, stats.Enrollments, stats.Outbound, stats.Replies, stats.Accounts)
}

func env(k, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return fallback
}

func backupDB(dataDir string) error {
	src := filepath.Join(dataDir, "app.db")
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			slog.Info("no existing app.db to backup")
			return nil
		}
		return err
	}
	dir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, "pre-smartlead-"+time.Now().UTC().Format("20060102T150405Z")+".db")
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	slog.Info("sqlite backup", "path", dst)
	return nil
}
