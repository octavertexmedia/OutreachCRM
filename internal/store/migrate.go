package store

import (
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
	{8, `
-- staff AI, marketing SMTP, lead status
`},
	{9, `
-- telephony: Tata Smartflo connection + call logs
`},
	{10, `
-- smartlead import id map
`},
	{11, `
-- prospect memory: durable identity, address history, employment, events
`},
	{12, `
-- resumable import cursors
`},
	{13, `
-- reply classification results + suppression reason on person
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
		// Migration steps intentionally ignore errors from statements that may
		// already have been applied; savepoints keep those from aborting the
		// whole transaction on Postgres.
		tx.soft = true
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
		if m.version == 8 {
			if err := upgradeAIAndMarketing(tx); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", m.version, err)
			}
		}
		if m.version == 9 {
			if err := upgradeTelephony(tx); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", m.version, err)
			}
		}
		if m.version == 10 {
			if err := upgradeSmartleadMap(tx); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", m.version, err)
			}
		}
		if m.version == 11 {
			if err := upgradeProspectMemory(tx); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", m.version, err)
			}
		}
		if m.version == 12 {
			if err := upgradeImportCursor(tx); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", m.version, err)
			}
		}
		if m.version == 13 {
			if err := upgradeReplyClassification(tx); err != nil {
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
	if !s.db.postgres() {
		// SQLite only: WAL keeps readers unblocked during writes.
		_, _ = s.db.Exec(`PRAGMA journal_mode=WAL`)
	}
	return nil
}

func upgradeMVPColumns(tx *tx) error {
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

func upgradeProdV2(tx *tx) error {
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

func upgradePipeline(tx *tx) error {
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

func upgradeDeliverability(tx *tx) error {
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

func upgradeAudiences(tx *tx) error {
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

func upgradeCampaignFunnels(tx *tx) error {
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

func upgradeAIAndMarketing(tx *tx) error {
	_, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS workspace_ai (
  workspace_id INTEGER PRIMARY KEY,
  system_prompt TEXT NOT NULL DEFAULT '',
  business_prompt TEXT NOT NULL DEFAULT '',
  openai_key_enc TEXT NOT NULL DEFAULT '',
  openai_base_url TEXT NOT NULL DEFAULT '',
  openai_model TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS marketing_smtp (
  workspace_id INTEGER PRIMARY KEY,
  provider TEXT NOT NULL DEFAULT 'smtp',
  host TEXT NOT NULL DEFAULT '',
  port INTEGER NOT NULL DEFAULT 587,
  username TEXT NOT NULL DEFAULT '',
  password_enc TEXT NOT NULL DEFAULT '',
  api_key_enc TEXT NOT NULL DEFAULT '',
  from_email TEXT NOT NULL DEFAULT '',
  from_name TEXT NOT NULL DEFAULT '',
  daily_quota INTEGER NOT NULL DEFAULT 200,
  sent_today INTEGER NOT NULL DEFAULT 0,
  quota_date TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
`)
	if err != nil {
		return err
	}
	_, _ = tx.Exec(`ALTER TABLE leads ADD COLUMN status TEXT NOT NULL DEFAULT 'new'`)
	_, _ = tx.Exec(`ALTER TABLE inbound_replies ADD COLUMN suggested_reply TEXT NOT NULL DEFAULT ''`)
	return nil
}

func upgradeTelephony(tx *tx) error {
	_, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS telephony_accounts (
  workspace_id INTEGER PRIMARY KEY,
  provider TEXT NOT NULL DEFAULT 'smartflo',
  base_url TEXT NOT NULL DEFAULT '',
  login_email TEXT NOT NULL DEFAULT '',
  password_enc TEXT NOT NULL DEFAULT '',
  token_enc TEXT NOT NULL DEFAULT '',
  auth_scheme TEXT NOT NULL DEFAULT '',
  agent_number TEXT NOT NULL DEFAULT '',
  caller_id TEXT NOT NULL DEFAULT '',
  call_timeout INTEGER NOT NULL DEFAULT 0,
  webhook_secret TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  last_error TEXT NOT NULL DEFAULT '',
  last_verified_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS call_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  workspace_id INTEGER NOT NULL DEFAULT 1,
  lead_id INTEGER,
  user_id INTEGER NOT NULL DEFAULT 0,
  provider TEXT NOT NULL DEFAULT 'smartflo',
  call_id TEXT NOT NULL DEFAULT '',
  uuid TEXT NOT NULL DEFAULT '',
  ref_id TEXT NOT NULL DEFAULT '',
  direction TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  agent_number TEXT NOT NULL DEFAULT '',
  agent_name TEXT NOT NULL DEFAULT '',
  client_number TEXT NOT NULL DEFAULT '',
  caller_id TEXT NOT NULL DEFAULT '',
  duration INTEGER NOT NULL DEFAULT 0,
  billsec INTEGER NOT NULL DEFAULT 0,
  recording_url TEXT NOT NULL DEFAULT '',
  hangup_cause TEXT NOT NULL DEFAULT '',
  notes TEXT NOT NULL DEFAULT '',
  started_at TEXT,
  answered_at TEXT,
  ended_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_call_logs_ws ON call_logs(workspace_id);
CREATE INDEX IF NOT EXISTS idx_call_logs_lead ON call_logs(lead_id);
CREATE INDEX IF NOT EXISTS idx_call_logs_ref ON call_logs(ref_id);
CREATE INDEX IF NOT EXISTS idx_call_logs_uuid ON call_logs(uuid);
CREATE INDEX IF NOT EXISTS idx_call_logs_callid ON call_logs(call_id);
`)
	return err
}

func upgradeSmartleadMap(tx *tx) error {
	_, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS smartlead_map (
  kind TEXT NOT NULL,
  remote_id TEXT NOT NULL,
  local_id INTEGER NOT NULL,
  PRIMARY KEY (kind, remote_id)
);
CREATE INDEX IF NOT EXISTS idx_smartlead_map_local ON smartlead_map(kind, local_id);
`)
	return err
}

// upgradeProspectMemory adds the prospect-memory layer: a durable person
// identity separated from the email addresses that reach them.
//
// It is deliberately additive. `leads` keeps working and `person.lead_id` links
// the two, so reads can be repointed one at a time instead of in a big-bang
// cutover on live outreach data. Identity resolution then merges people down
// over time via person.merged_into_id, which is reversible.
//
// Times are RFC3339 TEXT to match the rest of this package (see fmtTime), which
// sorts and compares identically on SQLite and Postgres.
func upgradeProspectMemory(tx *tx) error {
	_, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS person (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  workspace_id INTEGER NOT NULL DEFAULT 1,
  lead_id INTEGER,
  display_name TEXT NOT NULL DEFAULT '',
  primary_email TEXT NOT NULL DEFAULT '',
  phone TEXT NOT NULL DEFAULT '',
  linkedin_url TEXT NOT NULL DEFAULT '',
  merged_into_id INTEGER,
  first_seen_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_person_ws ON person(workspace_id);
CREATE INDEX IF NOT EXISTS idx_person_lead ON person(lead_id);
CREATE INDEX IF NOT EXISTS idx_person_merged ON person(merged_into_id);

-- Uniqueness lives here, not on person: several addresses may reach one human.
CREATE TABLE IF NOT EXISTS person_email (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  person_id INTEGER NOT NULL,
  workspace_id INTEGER NOT NULL DEFAULT 1,
  email TEXT NOT NULL,
  email_domain TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active',
  bounce_count INTEGER NOT NULL DEFAULT 0,
  first_seen_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_person_email_uniq ON person_email(workspace_id, email);
CREATE INDEX IF NOT EXISTS idx_person_email_person ON person_email(person_id);
CREATE INDEX IF NOT EXISTS idx_person_email_domain ON person_email(email_domain);

-- Employment as rows with validity windows, so a promotion stays visible
-- instead of overwriting the role it replaced.
CREATE TABLE IF NOT EXISTS person_employment (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  person_id INTEGER NOT NULL,
  company TEXT NOT NULL DEFAULT '',
  domain TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  valid_from TEXT,
  valid_to TEXT,
  source TEXT NOT NULL DEFAULT 'smartlead',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_person_employment_person ON person_employment(person_id, valid_to);

-- Append-only. Scores derive from this, so a scoring change is a recompute
-- rather than a migration. dedupe_key makes every import re-runnable.
CREATE TABLE IF NOT EXISTS prospect_event (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  person_id INTEGER NOT NULL,
  workspace_id INTEGER NOT NULL DEFAULT 1,
  campaign_id INTEGER,
  kind TEXT NOT NULL,
  occurred_at TEXT NOT NULL,
  payload TEXT NOT NULL DEFAULT '{}',
  source TEXT NOT NULL DEFAULT 'smartlead_import',
  dedupe_key TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_prospect_event_dedupe ON prospect_event(dedupe_key);
CREATE INDEX IF NOT EXISTS idx_prospect_event_person ON prospect_event(person_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_prospect_event_kind ON prospect_event(kind, occurred_at);

-- Materialised scoring snapshot. Never the source of truth; components are
-- stored alongside the total so a recommendation can explain itself.
CREATE TABLE IF NOT EXISTS person_signal (
  person_id INTEGER PRIMARY KEY,
  workspace_id INTEGER NOT NULL DEFAULT 1,
  intent_max INTEGER NOT NULL DEFAULT 0,
  intent_reason TEXT NOT NULL DEFAULT '',
  decay REAL NOT NULL DEFAULT 1,
  triggers REAL NOT NULL DEFAULT 1,
  priority REAL NOT NULL DEFAULT 0,
  reason TEXT NOT NULL DEFAULT '',
  suggested_action TEXT NOT NULL DEFAULT '',
  suppressed INTEGER NOT NULL DEFAULT 0,
  engagement_tracked INTEGER NOT NULL DEFAULT 0,
  last_contacted_at TEXT,
  last_reply_at TEXT,
  revisit_at TEXT,
  computed_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_person_signal_priority ON person_signal(workspace_id, suppressed, priority);

-- Review queue for merges that are probable but not certain (score 50-99).
CREATE TABLE IF NOT EXISTS identity_candidate (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  workspace_id INTEGER NOT NULL DEFAULT 1,
  person_id INTEGER NOT NULL,
  other_person_id INTEGER NOT NULL,
  score INTEGER NOT NULL DEFAULT 0,
  signals TEXT NOT NULL DEFAULT '[]',
  state TEXT NOT NULL DEFAULT 'pending',
  decided_by INTEGER,
  decided_at TEXT,
  created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_identity_candidate_pair ON identity_candidate(person_id, other_person_id);
CREATE INDEX IF NOT EXISTS idx_identity_candidate_state ON identity_candidate(workspace_id, state, score);

-- Per-campaign tracking settings, so scoring can tell "not engaged" apart from
-- "engagement was never recorded". Smartlead disables open/click tracking per
-- campaign, and absence of a signal is not absence of interest.
CREATE TABLE IF NOT EXISTS campaign_tracking (
  campaign_id INTEGER PRIMARY KEY,
  track_opens INTEGER NOT NULL DEFAULT 1,
  track_clicks INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL
);
`)
	if err != nil {
		return err
	}
	// Connect existing history to the identity layer.
	for _, q := range []string{
		`ALTER TABLE outbound_messages ADD COLUMN person_id INTEGER`,
		`ALTER TABLE inbound_replies ADD COLUMN person_id INTEGER`,
		`ALTER TABLE inbound_replies ADD COLUMN category_raw TEXT DEFAULT ''`,
		`ALTER TABLE inbound_replies ADD COLUMN classifier_version INTEGER DEFAULT 0`,
		`ALTER TABLE inbound_replies ADD COLUMN auto_reply INTEGER DEFAULT 0`,
	} {
		_, _ = tx.Exec(q)
	}
	_, _ = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_outbound_person ON outbound_messages(person_id)`)
	_, _ = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_replies_person ON inbound_replies(person_id)`)
	return nil
}

// upgradeImportCursor adds resumable import state. A five-year backfill is long
// enough that something will interrupt it — an SSH drop already corrupted one
// run — so progress is committed per page and a restart resumes from the last
// completed page rather than from the beginning.
func upgradeImportCursor(tx *tx) error {
	_, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS import_cursor (
  source       TEXT NOT NULL,
  resource     TEXT NOT NULL,
  campaign_id  INTEGER NOT NULL DEFAULT 0,
  next_offset  INTEGER NOT NULL DEFAULT 0,
  rows_done    INTEGER NOT NULL DEFAULT 0,
  total_hint   INTEGER NOT NULL DEFAULT 0,
  completed_at TEXT,
  last_error   TEXT NOT NULL DEFAULT '',
  attempts     INTEGER NOT NULL DEFAULT 0,
  updated_at   TEXT NOT NULL,
  PRIMARY KEY (source, resource, campaign_id)
);
CREATE INDEX IF NOT EXISTS idx_import_cursor_open ON import_cursor(source, completed_at);
`)
	return err
}

// upgradeReplyClassification stores each reply's classification alongside it.
// intent_points is denormalised so recomputing every person's score is one
// aggregate query rather than a category lookup per reply in Go.
func upgradeReplyClassification(tx *tx) error {
	for _, q := range []string{
		`ALTER TABLE inbound_replies ADD COLUMN category TEXT DEFAULT ''`,
		`ALTER TABLE inbound_replies ADD COLUMN ask TEXT DEFAULT ''`,
		`ALTER TABLE inbound_replies ADD COLUMN intent_points INTEGER DEFAULT 0`,
		`ALTER TABLE inbound_replies ADD COLUMN classify_reason TEXT DEFAULT ''`,
		`ALTER TABLE inbound_replies ADD COLUMN revisit_at TEXT`,
	} {
		if _, err := tx.Exec(q); err != nil {
			// Column may already exist from a partial earlier run; the
			// savepoint wrapper keeps the transaction usable either way.
			continue
		}
	}
	_, _ = tx.Exec(`CREATE INDEX IF NOT EXISTS idx_replies_classify ON inbound_replies(classifier_version)`)
	return nil
}
