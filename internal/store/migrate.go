package store

import (
	"database/sql"
	"fmt"
	"time"
)

type migration struct {
	version int
	sql     string
}

var migrations = []migration{
	{1, `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY
);
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  email TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'sender',
  active INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS leads (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  owner_id INTEGER,
  name TEXT NOT NULL,
  website TEXT DEFAULT '',
  phone TEXT DEFAULT '',
  email TEXT DEFAULT '',
  google_rating REAL DEFAULT 0,
  category TEXT DEFAULT '',
  issues_json TEXT DEFAULT '[]',
  premium_score INTEGER DEFAULT 0,
  enrichment_status TEXT DEFAULT 'pending',
  notes TEXT DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS campaigns (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  owner_id INTEGER,
  name TEXT NOT NULL,
  status TEXT DEFAULT 'draft',
  daily_send_limit INTEGER DEFAULT 50,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS email_accounts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  owner_id INTEGER,
  email TEXT NOT NULL,
  provider TEXT NOT NULL DEFAULT 'smtp',
  smtp_host TEXT NOT NULL DEFAULT '',
  smtp_port INTEGER NOT NULL DEFAULT 587,
  username TEXT NOT NULL DEFAULT '',
  password_enc TEXT NOT NULL DEFAULT '',
  access_token_enc TEXT NOT NULL DEFAULT '',
  refresh_token_enc TEXT NOT NULL DEFAULT '',
  token_expiry TEXT,
  imap_host TEXT NOT NULL DEFAULT '',
  imap_port INTEGER NOT NULL DEFAULT 993,
  imap_last_uid INTEGER NOT NULL DEFAULT 0,
  daily_quota INTEGER DEFAULT 40,
  sent_today INTEGER DEFAULT 0,
  quota_date TEXT DEFAULT '',
  last_sent_at TEXT,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sequence_steps (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  campaign_id INTEGER NOT NULL,
  step_order INTEGER NOT NULL,
  delay_days INTEGER NOT NULL DEFAULT 0,
  subject_template TEXT NOT NULL,
  body_spintax TEXT NOT NULL,
  FOREIGN KEY(campaign_id) REFERENCES campaigns(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS campaign_leads (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  campaign_id INTEGER NOT NULL,
  lead_id INTEGER NOT NULL,
  current_step INTEGER DEFAULT 0,
  status TEXT DEFAULT 'enrolled',
  enrolled_at TEXT NOT NULL,
  next_send_at TEXT,
  UNIQUE(campaign_id, lead_id),
  FOREIGN KEY(campaign_id) REFERENCES campaigns(id) ON DELETE CASCADE,
  FOREIGN KEY(lead_id) REFERENCES leads(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS outbound_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  campaign_id INTEGER NOT NULL,
  lead_id INTEGER NOT NULL,
  campaign_lead_id INTEGER NOT NULL,
  step_order INTEGER NOT NULL,
  account_id INTEGER,
  to_email TEXT NOT NULL,
  subject TEXT NOT NULL,
  body TEXT NOT NULL,
  status TEXT DEFAULT 'scheduled',
  scheduled_at TEXT NOT NULL,
  next_attempt_at TEXT,
  attempts INTEGER NOT NULL DEFAULT 0,
  sent_at TEXT,
  error TEXT DEFAULT '',
  last_error TEXT DEFAULT ''
);
CREATE TABLE IF NOT EXISTS inbound_replies (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  owner_id INTEGER,
  lead_id INTEGER,
  lead_name TEXT DEFAULT '',
  from_email TEXT DEFAULT '',
  subject TEXT DEFAULT '',
  body TEXT NOT NULL,
  intent TEXT DEFAULT 'other',
  message_id TEXT DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS suppressions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  email TEXT NOT NULL UNIQUE,
  reason TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS oauth_states (
  state TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL,
  provider TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
`},
	{2, `
-- MVP upgrade: add missing columns if upgrading from older schema
-- (no-op safe via try/ignore pattern handled in Go for ALTER)
`},
	{3, `
-- production v2 tables (columns via Go ALTER)
`},
	{4, `
-- pipeline quality: sourcing + drafts
`},
	{5, `
-- email deliverability engine
`},
	{6, `
-- audiences: saved lead filters + member snapshots
`},
	{7, `
-- campaign funnel tracker: audience × campaign runs
`},
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		return err
	}
	var current int
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&current)

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if m.sql != "" && m.version == 1 {
			if _, err := tx.Exec(m.sql); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", m.version, err)
			}
		}
		if m.version == 2 {
			if err := upgradeMVPColumns(tx); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", m.version, err)
			}
		}
		if m.version == 3 {
			if err := upgradeProdV2(tx); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", m.version, err)
			}
		}
		if m.version == 4 {
			if err := upgradePipeline(tx); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", m.version, err)
			}
		}
		if m.version == 5 {
			if err := upgradeDeliverability(tx); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", m.version, err)
			}
		}
		if m.version == 6 {
			if err := upgradeAudiences(tx); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", m.version, err)
			}
		}
		if m.version == 7 {
			if err := upgradeCampaignFunnels(tx); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", m.version, err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version) VALUES(?)`, m.version); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	// Ensure WAL
	_, _ = s.db.Exec(`PRAGMA journal_mode=WAL`)
	return nil
}

func upgradeMVPColumns(tx *sql.Tx) error {
	alters := []string{
		`ALTER TABLE leads ADD COLUMN owner_id INTEGER`,
		`ALTER TABLE campaigns ADD COLUMN owner_id INTEGER`,
		`ALTER TABLE email_accounts ADD COLUMN owner_id INTEGER`,
		`ALTER TABLE email_accounts ADD COLUMN provider TEXT NOT NULL DEFAULT 'smtp'`,
		`ALTER TABLE email_accounts ADD COLUMN password_enc TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE email_accounts ADD COLUMN access_token_enc TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE email_accounts ADD COLUMN refresh_token_enc TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE email_accounts ADD COLUMN token_expiry TEXT`,
		`ALTER TABLE email_accounts ADD COLUMN imap_host TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE email_accounts ADD COLUMN imap_port INTEGER NOT NULL DEFAULT 993`,
		`ALTER TABLE email_accounts ADD COLUMN imap_last_uid INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE outbound_messages ADD COLUMN next_attempt_at TEXT`,
		`ALTER TABLE outbound_messages ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE outbound_messages ADD COLUMN last_error TEXT DEFAULT ''`,
		`ALTER TABLE inbound_replies ADD COLUMN owner_id INTEGER`,
		`ALTER TABLE inbound_replies ADD COLUMN message_id TEXT DEFAULT ''`,
	}
	for _, q := range alters {
		if _, err := tx.Exec(q); err != nil {
			// ignore duplicate column errors from fresh v1 schema
			continue
		}
	}
	// Migrate plaintext password -> password_enc if old column exists
	_, _ = tx.Exec(`UPDATE email_accounts SET password_enc = password WHERE password_enc = '' AND password IS NOT NULL AND password != ''`)
	return nil
}

func upgradeProdV2(tx *sql.Tx) error {
	_, _ = tx.Exec(`
CREATE TABLE IF NOT EXISTS workspaces (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  workspace_id INTEGER NOT NULL DEFAULT 1,
  user_id INTEGER NOT NULL DEFAULT 0,
  action TEXT NOT NULL,
  entity TEXT NOT NULL DEFAULT '',
  entity_id TEXT NOT NULL DEFAULT '',
  meta TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS email_templates (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  workspace_id INTEGER NOT NULL DEFAULT 1,
  name TEXT NOT NULL,
  subject TEXT NOT NULL,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS domain_checks (
  domain TEXT PRIMARY KEY,
  spf INTEGER NOT NULL DEFAULT 0,
  dkim INTEGER NOT NULL DEFAULT 0,
  dmarc INTEGER NOT NULL DEFAULT 0,
  detail TEXT NOT NULL DEFAULT '',
  checked_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS llm_usage (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  workspace_id INTEGER NOT NULL DEFAULT 1,
  user_id INTEGER NOT NULL DEFAULT 0,
  feature TEXT NOT NULL DEFAULT '',
  tokens INTEGER NOT NULL DEFAULT 0,
  cost_cents INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS app_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`)
	alters := []string{
		`ALTER TABLE users ADD COLUMN totp_secret_enc TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN totp_enabled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN workspace_id INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE leads ADD COLUMN workspace_id INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE leads ADD COLUMN confidence INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE leads ADD COLUMN enrichment_cost INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE leads ADD COLUMN consent_at TEXT`,
		`ALTER TABLE leads ADD COLUMN consent_source TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE campaigns ADD COLUMN workspace_id INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE campaigns ADD COLUMN timezone TEXT NOT NULL DEFAULT 'UTC'`,
		`ALTER TABLE campaigns ADD COLUMN send_window_start INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE campaigns ADD COLUMN send_window_end INTEGER NOT NULL DEFAULT 23`,
		`ALTER TABLE campaigns ADD COLUMN ab_enabled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE email_accounts ADD COLUMN workspace_id INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE email_accounts ADD COLUMN domain TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE email_accounts ADD COLUMN domain_daily_limit INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE email_accounts ADD COLUMN warmup_day INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE email_accounts ADD COLUMN warmup_enabled INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE email_accounts ADD COLUMN esp_api_key_enc TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sequence_steps ADD COLUMN variant_b_subject TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sequence_steps ADD COLUMN variant_b_body TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE campaign_leads ADD COLUMN variant TEXT NOT NULL DEFAULT 'a'`,
		`ALTER TABLE outbound_messages ADD COLUMN locked_until TEXT`,
		`ALTER TABLE outbound_messages ADD COLUMN lock_owner TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE outbound_messages ADD COLUMN variant TEXT NOT NULL DEFAULT 'a'`,
		`ALTER TABLE outbound_messages ADD COLUMN message_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE inbound_replies ADD COLUMN workspace_id INTEGER`,
		`ALTER TABLE inbound_replies ADD COLUMN thread_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE inbound_replies ADD COLUMN hitl_status TEXT NOT NULL DEFAULT 'auto'`,
		`ALTER TABLE suppressions ADD COLUMN workspace_id INTEGER NOT NULL DEFAULT 1`,
	}
	for _, q := range alters {
		_, _ = tx.Exec(q)
	}
	var n int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM workspaces`).Scan(&n)
	if n == 0 {
		_, _ = tx.Exec(`INSERT INTO workspaces(name, created_at) VALUES('Default', ?)`, time.Now().UTC().Format(time.RFC3339))
	}
	_, _ = tx.Exec(`INSERT OR IGNORE INTO app_settings(key, value) VALUES('pii_retention_days', '365')`)
	_, _ = tx.Exec(`INSERT OR IGNORE INTO app_settings(key, value) VALUES('llm_daily_budget_cents', '500')`)
	return nil
}

func upgradePipeline(tx *sql.Tx) error {
	for _, q := range []string{
		`ALTER TABLE leads ADD COLUMN source TEXT NOT NULL DEFAULT 'manual'`,
		`ALTER TABLE leads ADD COLUMN company TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE leads ADD COLUMN title TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE leads ADD COLUMN draft_subject TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE leads ADD COLUMN draft_body TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE outbound_messages ADD COLUMN opened INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE outbound_messages ADD COLUMN replied INTEGER NOT NULL DEFAULT 0`,
	} {
		_, _ = tx.Exec(q)
	}
	return nil
}

func upgradeDeliverability(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS recipient_stats (
  email TEXT PRIMARY KEY,
  workspace_id INTEGER NOT NULL DEFAULT 1,
  sent INTEGER NOT NULL DEFAULT 0,
  opened INTEGER NOT NULL DEFAULT 0,
  clicked INTEGER NOT NULL DEFAULT 0,
  replied INTEGER NOT NULL DEFAULT 0,
  hard_bounces INTEGER NOT NULL DEFAULT 0,
  soft_bounces INTEGER NOT NULL DEFAULT 0,
  complaints INTEGER NOT NULL DEFAULT 0,
  unsubscribes INTEGER NOT NULL DEFAULT 0,
  purchased_list INTEGER NOT NULL DEFAULT 0,
  first_seen_at TEXT,
  last_event_at TEXT
);
CREATE TABLE IF NOT EXISTS deliverability_decisions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  workspace_id INTEGER NOT NULL DEFAULT 1,
  campaign_id INTEGER NOT NULL DEFAULT 0,
  email TEXT NOT NULL,
  action TEXT NOT NULL,
  bounce_prob REAL NOT NULL DEFAULT 0,
  spam_trap_risk REAL NOT NULL DEFAULT 0,
  engagement_prob REAL NOT NULL DEFAULT 0,
  content_risk REAL NOT NULL DEFAULT 0,
  isp TEXT NOT NULL DEFAULT '',
  reasons TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS isp_send_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  workspace_id INTEGER NOT NULL DEFAULT 1,
  isp TEXT NOT NULL,
  sent_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS blacklist_checks (
  key TEXT PRIMARY KEY,
  listed INTEGER NOT NULL DEFAULT 0,
  zones TEXT NOT NULL DEFAULT '',
  checked_at TEXT NOT NULL
);
`)
	if err != nil {
		return err
	}
	for _, q := range []string{
		`ALTER TABLE campaigns ADD COLUMN deliverability_paused INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE leads ADD COLUMN email_bounce_prob REAL NOT NULL DEFAULT -1`,
		`ALTER TABLE leads ADD COLUMN email_validated_at TEXT`,
		`ALTER TABLE leads ADD COLUMN email_validation TEXT NOT NULL DEFAULT ''`,
	} {
		_, _ = tx.Exec(q)
	}
	_, _ = tx.Exec(`INSERT OR IGNORE INTO app_settings(key, value) VALUES('deliverability_require_auth', '0')`)
	_, _ = tx.Exec(`INSERT OR IGNORE INTO app_settings(key, value) VALUES('deliverability_smtp_verify', '0')`)
	_, _ = tx.Exec(`INSERT OR IGNORE INTO app_settings(key, value) VALUES('deliverability_max_bounce_rate', '2')`)
	_, _ = tx.Exec(`INSERT OR IGNORE INTO app_settings(key, value) VALUES('deliverability_max_complaint_rate', '0.1')`)
	return nil
}

func upgradeAudiences(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS audiences (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  workspace_id INTEGER NOT NULL DEFAULT 1,
  owner_id INTEGER NOT NULL DEFAULT 0,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  filter_json TEXT NOT NULL DEFAULT '{}',
  member_count INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audience_members (
  audience_id INTEGER NOT NULL,
  lead_id INTEGER NOT NULL,
  added_at TEXT NOT NULL,
  PRIMARY KEY (audience_id, lead_id)
);
CREATE INDEX IF NOT EXISTS idx_audiences_workspace ON audiences(workspace_id);
CREATE INDEX IF NOT EXISTS idx_audience_members_lead ON audience_members(lead_id);
`)
	return err
}

func upgradeCampaignFunnels(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS campaign_audience_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  workspace_id INTEGER NOT NULL DEFAULT 1,
  campaign_id INTEGER NOT NULL,
  audience_id INTEGER NOT NULL,
  enrolled INTEGER NOT NULL DEFAULT 0,
  skipped INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(campaign_id, audience_id)
);
CREATE INDEX IF NOT EXISTS idx_car_workspace ON campaign_audience_runs(workspace_id);
CREATE INDEX IF NOT EXISTS idx_car_audience ON campaign_audience_runs(audience_id);
CREATE INDEX IF NOT EXISTS idx_car_campaign ON campaign_audience_runs(campaign_id);
`)
	if err != nil {
		return err
	}
	_, _ = tx.Exec(`ALTER TABLE campaign_leads ADD COLUMN audience_id INTEGER NOT NULL DEFAULT 0`)
	_, _ = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_cl_audience ON campaign_leads(audience_id)`)
	return nil
}

