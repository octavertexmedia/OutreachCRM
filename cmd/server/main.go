package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/manishkumar/outreachcrm/internal/auth"
	"github.com/manishkumar/outreachcrm/internal/backup"
	"github.com/manishkumar/outreachcrm/internal/config"
	"github.com/manishkumar/outreachcrm/internal/crypto"
	"github.com/manishkumar/outreachcrm/internal/deliverability"
	"github.com/manishkumar/outreachcrm/internal/enrichment"
	"github.com/manishkumar/outreachcrm/internal/handlers"
	"github.com/manishkumar/outreachcrm/internal/imapsync"
	"github.com/manishkumar/outreachcrm/internal/inbox"
	"github.com/manishkumar/outreachcrm/internal/llm"
	"github.com/manishkumar/outreachcrm/internal/mail"
	"github.com/manishkumar/outreachcrm/internal/oauth"
	"github.com/manishkumar/outreachcrm/internal/sequencing"
	"github.com/manishkumar/outreachcrm/internal/store"
	"github.com/manishkumar/outreachcrm/internal/writing"
	"github.com/manishkumar/outreachcrm/web"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	cfg := config.Load()

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		slog.Error("store open", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	if err := st.BootstrapAdmin(cfg.BootstrapAdminEmail, cfg.BootstrapAdminPassword); err != nil {
		slog.Error("bootstrap admin", "err", err)
		os.Exit(1)
	}
	_ = st.SetSetting("pii_retention_days", strconv.Itoa(cfg.PIIRetentionDays))

	box, err := crypto.New(cfg.EncryptionKey)
	if err != nil {
		slog.Error("crypto", "err", err)
		os.Exit(1)
	}

	llmClient := llm.New(cfg.OpenAIAPIKey, cfg.OpenAIBaseURL, cfg.OpenAIModel)
	authMgr := auth.New(cfg.SessionSecret, cfg.CookieSecure)
	oauthMgr := oauth.New(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.MicrosoftClientID, cfg.MicrosoftClientSecret, cfg.OAuthRedirectBase)
	enrichSvc := &enrichment.Service{LLM: llmClient}
	writeSvc := &writing.Service{LLM: llmClient}
	inboxSvc := &inbox.Service{LLM: llmClient}
	dc := deliverability.DefaultConfig()
	dc.SMTPVerify = cfg.SMTPVerify
	dc.BlacklistCheck = cfg.BlacklistCheck
	dc.RequireAuth = cfg.RequireSendAuth
	dc.OptimizeSendTime = cfg.OptimizeSendTime
	eng := deliverability.New(dc)

	srv, err := handlers.New(st, authMgr, box, oauthMgr, cfg, enrichSvc, writeSvc, inboxSvc, eng, web.FS)
	if err != nil {
		slog.Error("handlers", "err", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	sendWorker := &sequencing.Worker{
		Store: st, Sender: &mail.Sender{DryRun: cfg.DryRunSMTP}, Box: box, OAuth: oauthMgr,
		Auth: authMgr, Deliverability: eng, PublicBaseURL: cfg.PublicBaseURL,
		Interval: cfg.WorkerInterval, Batch: 10, MaxAttempts: cfg.MaxSendAttempts,
	}
	go sendWorker.Run(ctx)

	imapWorker := &imapsync.Worker{Store: st, Box: box, OAuth: oauthMgr, Classify: inboxSvc, Interval: cfg.IMAPInterval}
	go imapWorker.Run(ctx)

	go backup.RunPeriodically(ctx.Done(), cfg.DataDir, cfg.BackupInterval)
	go func() {
		t := time.NewTicker(12 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				days, _ := strconv.Atoi(st.GetSetting("pii_retention_days", strconv.Itoa(cfg.PIIRetentionDays)))
				n, err := st.PurgeOldPII(days)
				if err != nil {
					slog.Error("pii purge", "err", err)
				} else if n > 0 {
					slog.Info("pii purge", "rows", n)
				}
			}
		}
	}()

	httpSrv := &http.Server{Addr: cfg.Addr, Handler: srv.Routes(), ReadHeaderTimeout: 10 * time.Second}

	go func() {
		slog.Info("listening", "addr", cfg.Addr, "version", cfg.AppVersion, "tls", cfg.TLSCertFile != "", "dry_run_smtp", cfg.DryRunSMTP)
		var err error
		if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
			err = httpSrv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			err = httpSrv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			slog.Error("http", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer shutdownCancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = backup.Snapshot(cfg.DataDir)
	slog.Info("shutdown complete")
}
