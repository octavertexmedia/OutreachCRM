package backup

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// RunPeriodically copies data/app.db to data/backups/app-YYYYMMDD.db
func RunPeriodically(ctxDone <-chan struct{}, dataDir string, every time.Duration) {
	if every <= 0 {
		every = 24 * time.Hour
	}
	t := time.NewTicker(every)
	defer t.Stop()
	_ = Snapshot(dataDir)
	for {
		select {
		case <-ctxDone:
			return
		case <-t.C:
			if err := Snapshot(dataDir); err != nil {
				slog.Error("backup", "err", err)
			}
		}
	}
}

func Snapshot(dataDir string) error {
	src := filepath.Join(dataDir, "app.db")
	if _, err := os.Stat(src); err != nil {
		return err
	}
	dir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, fmt.Sprintf("app-%s.db", time.Now().UTC().Format("20060102-1504")))
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
	_, err = io.Copy(out, in)
	if err == nil {
		slog.Info("backup complete", "path", dst)
	}
	return err
}
