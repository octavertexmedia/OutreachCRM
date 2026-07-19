package config

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/manishkumar/outreachcrm/internal/crypto"
	"github.com/manishkumar/outreachcrm/internal/openbao"
)

type Config struct {
	Addr                   string
	DataDir                string
	SessionSecret          string
	EncryptionKey          string
	BootstrapAdminEmail    string
	BootstrapAdminPassword string
	CookieSecure           bool
	OpenAIAPIKey           string
	OpenAIBaseURL          string
	OpenAIModel            string
	OpenAIEmbedModel       string
	WorkerInterval         time.Duration
	IMAPInterval           time.Duration
	DryRunSMTP             bool
	MaxSendAttempts        int
	GoogleClientID         string
	GoogleClientSecret     string
	MicrosoftClientID      string
	MicrosoftClientSecret  string
	OAuthRedirectBase      string
	PublicBaseURL          string
	AppVersion             string
	TLSCertFile            string
	TLSKeyFile             string
	BackupInterval         time.Duration
	PIIRetentionDays       int
	SMTPVerify             bool
	BlacklistCheck         bool
	RequireSendAuth        bool
	OptimizeSendTime       bool
}

func Load() Config {
	if _, err := openbao.ApplySecrets(); err != nil {
		slog.Error("openbao", "err", err)
		os.Exit(1)
	}
	enc := env("ENCRYPTION_KEY", "")
	if enc == "" {
		enc = crypto.DevKey()
	}
	return Config{
		Addr:                   env("ADDR", ":8080"),
		DataDir:                env("DATA_DIR", "data"),
		SessionSecret:          env("SESSION_SECRET", "dev-session-secret-change-me"),
		EncryptionKey:          enc,
		BootstrapAdminEmail:    env("BOOTSTRAP_ADMIN_EMAIL", "admin@localhost"),
		BootstrapAdminPassword: env("BOOTSTRAP_ADMIN_PASSWORD", "changeme"),
		CookieSecure:           envBool("COOKIE_SECURE", false),
		OpenAIAPIKey:           env("OPENAI_API_KEY", ""),
		OpenAIBaseURL:          strings.TrimRight(env("OPENAI_BASE_URL", "https://api.openai.com/v1"), "/"),
		OpenAIModel:            env("OPENAI_MODEL", "gpt-4o-mini"),
		OpenAIEmbedModel:       env("OPENAI_EMBED_MODEL", "text-embedding-3-small"),
		WorkerInterval:         envDuration("WORKER_INTERVAL", 30*time.Second),
		IMAPInterval:           envDuration("IMAP_INTERVAL", 2*time.Minute),
		DryRunSMTP:             envBool("DRY_RUN_SMTP", false),
		MaxSendAttempts:        envInt("MAX_SEND_ATTEMPTS", 5),
		GoogleClientID:         env("GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret:     env("GOOGLE_CLIENT_SECRET", ""),
		MicrosoftClientID:      env("MICROSOFT_CLIENT_ID", ""),
		MicrosoftClientSecret:  env("MICROSOFT_CLIENT_SECRET", ""),
		OAuthRedirectBase:      strings.TrimRight(env("OAUTH_REDIRECT_BASE", env("PUBLIC_BASE_URL", "http://localhost:8080")), "/"),
		PublicBaseURL:          strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		AppVersion:             env("APP_VERSION", "dev"),
		TLSCertFile:            env("TLS_CERT_FILE", ""),
		TLSKeyFile:             env("TLS_KEY_FILE", ""),
		BackupInterval:         envDuration("BACKUP_INTERVAL", 24*time.Hour),
		PIIRetentionDays:       envInt("PII_RETENTION_DAYS", 365),
		SMTPVerify:             envBool("DELIVERABILITY_SMTP_VERIFY", false),
		BlacklistCheck:         envBool("DELIVERABILITY_BLACKLIST_CHECK", true),
		RequireSendAuth:        envBool("DELIVERABILITY_REQUIRE_AUTH", false),
		OptimizeSendTime:       envBool("DELIVERABILITY_OPTIMIZE_SEND_TIME", true),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
