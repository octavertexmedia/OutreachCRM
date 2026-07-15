# OutReachCRM Handbook

## 1. What this is

Production-lean outreach CRM on one Go binary (≤30 MB): multi-user workspaces, TOTP 2FA, encrypted secrets, OAuth/ESP send, IMAP + HITL inbox, deliverability DNS checks, durable multi-instance-aware queue, GDPR export/delete, backups, and ops endpoints.

## 2. Who uses it

| Role | Access |
|------|--------|
| **admin** | All data, `/users`, `/workspaces`, `/audit` |
| **sender** | Own leads/campaigns/accounts; shared workspace tools |

Bootstrap admin from `BOOTSTRAP_ADMIN_*` when users table is empty. Users belong to a **workspace**.

## 3. Stack & deploy

| Piece | Choice |
|-------|--------|
| DB | SQLite WAL + versioned migrations + file backups (`data/backups/`) |
| Auth | bcrypt + TOTP 2FA + HMAC cookies; API rate limit |
| Secrets | AES-GCM (`ENCRYPTION_KEY` — treat as your vault secret) |
| Email | SMTP / Gmail+Outlook OAuth XOAUTH2 / Postmark HTTP / SES SMTP |
| Size | `make build-size` ≤ 30 MB |

```bash
export ENCRYPTION_KEY="$(openssl rand -base64 32)"
export BOOTSTRAP_ADMIN_EMAIL=you@co.com BOOTSTRAP_ADMIN_PASSWORD='...'
export SESSION_SECRET='...' PUBLIC_BASE_URL=https://crm.example.com
# optional TLS
# export TLS_CERT_FILE=/path/fullchain.pem TLS_KEY_FILE=/path/privkey.pem
./outreachcrm
```

Reverse proxy (Caddy/nginx) for TLS is fine if `TLS_*` unset.

## 4. The six-step product loop

| Step | Where in app |
|------|----------------|
| **1 Sourcing** | `/leads` — manual, CSV (deduped), seed demos |
| **2 Enrichment** | AI Enrichment + bulk enrich; crawl signals + confidence |
| **3 AI Writing** | Draft email → saved on lead → push into campaign step 1 |
| **4 Sequencing** | Campaigns + `/queue` + timezone/A/B/round-robin worker |
| **5 Reply mgmt** | Inbox classify + IMAP + `/hitl` + suggest reply |
| **6 Analytics** | `/analytics` — rates, funnel counts, A/B |

Dashboard shows the live funnel for steps 1–6.

## 4b. Feature map vs “full production”

| Area | Implemented |
|------|-------------|
| Users / workspaces / audit | Yes |
| Auth / encrypted secrets / TOTP | Yes (not SAML SSO / cloud KMS) |
| OAuth + Postmark/SES + warmup + domain limits + bounce webhooks | Yes |
| IMAP + threading fields + HITL queue + unsubscribe | Yes |
| SPF/DKIM/DMARC DNS checks + timezone windows | Yes |
| Crawl signals + confidence + LLM budget | Yes |
| SQLite backups + PII retention purge | Yes (not full Postgres) |
| Message leases for multi-instance | Yes |
| healthz/readyz/metrics + slog | Yes |
| GDPR export/delete + consent fields | Yes |
| CSV import, analytics, templates | Yes |
| **Email Deliverability Engine** | Yes — `/deliverability` + pre-send gate |

## 5. Key routes

- `/deliverability` — reputation dashboard, validate, DNSBL, decision log
- `/security` — TOTP; `/privacy/export`
- `/hitl` — positive reply queue
- `/domains` — DNS checks
- `/analytics`, `/templates`, `/audit`, `/workspaces`
- `POST /webhooks/postmark`, `POST /webhooks/ses` — bounce/complaint → suppression
- `/leads/import` — CSV bulk

## 6. Gotchas

- **Postgres:** intentionally not dual-driver (size/dialect). Production data path is SQLite + WAL + scheduled snapshots. Mirror to object storage via your VPS cron if needed.
- **SSO:** TOTP 2FA yes; enterprise SAML/OIDC IdP login not bundled (OAuth is for *mail*, not user login).
- **KMS:** use env/`ENCRYPTION_KEY` from your secret manager; app decrypts locally.
- Enrichment crawl is lightweight HTTP GET — not PageSpeed API.

## 7. Runbook (short)

1. Health: `curl /healthz` and `/readyz`
2. Metrics: `curl /metrics`
3. Backups land in `DATA_DIR/backups/`; also on shutdown
4. Bounce spike: check suppressions + dead letters on dashboard/analytics
5. Rotate `ENCRYPTION_KEY` requires re-entering SMTP/OAuth credentials

## 8. Changelog

- 2026-07-16 — Company playbooks seed: OctaVertex Media + RevNext (PMS/POS/booking/B2B/CMS/revenue) templates, sequences, ICP leads via `POST /leads/seed-playbooks`.
- 2026-07-16 — Email Deliverability Engine: validation, bounce/trap/engagement scoring, content/ISP/warmup, auto-suppress, complaint pause, `/deliverability` UI, pre-send gate.
- 2026-07-16 — Pipeline v4: lead source/company/title/drafts, funnel + queue UI, save/apply AI drafts into sequences, reply suggest + outbound replied tracking, analytics funnel stats.
- 2026-07-16 — Production v2: workspaces, audit, TOTP, ESP/webhooks, warmup/domain limits, DNS checks, timezone windows, crawl enrichment, HITL, GDPR, CSV import, analytics/templates, backups/PII purge, leases, TLS option.
- 2026-07-16 — Production pass 1: RBAC, OAuth IMAP, AES-GCM, retries.
- 2026-07-16 — MVP.
