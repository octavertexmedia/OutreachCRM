# OutReachCRM Handbook
> The one place that explains how this works. Plain English. Last updated: 2026-09-04

## 1. What this is

Production-lean outreach CRM on one Go binary (≤80 MB with Zvec): multi-user workspaces, TOTP 2FA, encrypted secrets, OAuth/ESP send, IMAP + HITL inbox, deliverability DNS checks, durable multi-instance-aware queue, GDPR export/delete, backups, hybrid global search, and ops endpoints.

## 2. Who uses it

| Role | Access |
|------|--------|
| **admin** | All data, `/users`, `/workspaces`, `/audit` |
| **sender** | Own leads/campaigns/accounts; shared workspace tools |

Bootstrap admin from `BOOTSTRAP_ADMIN_*` when users table is empty. Users belong to a **workspace**. Startup ensures **Default**, **OctaVertex Media**, and **RevNext**; admins onboard more on `/workspaces`, switch via the sidebar, and assign senders on `/users`. Brand playbook seed loads OVM packs into OctaVertex Media and RevNext packs into RevNext.

## 3. Stack & deploy

| Piece | Choice |
|-------|--------|
| DB | SQLite WAL + versioned migrations + file backups (`data/backups/`). **CRM rows stay SQLite.** |
| Auth | bcrypt + TOTP 2FA + HMAC cookies; API rate limit |
| Secrets | OpenBao at **https://secrets.revnext.in/** (AppRole → KV); AES-GCM in-app with `ENCRYPTION_KEY` |
| Email | Mailbox IMAP (OAuth or password) for sync + 1:1; paid ESP SMTP (Brevo/SendGrid/Mailgun/Postmark/SES) for campaign blasts |
| Search | Default: [Alibaba Zvec](https://github.com/alibaba/zvec) hybrid (HNSW + FTS + RRF). Opt-in pgvector when `DATABASE_URL=postgres://…`. Lite: SQLite FTS5 (`make build-lite`) |
| Size | `make build-size` ≤ 80 MB (Go binary; ship `libzvec_c_api` alongside) |
| Prod URL | **https://outreach.vertexcrm.in/** — Contabo VPS with AgencyCRM (`:8003`, `/var/www/outreachcrm`) |

**Production:** Docker Compose + Nginx + Let’s Encrypt. CI: `.github/workflows/deploy.yml`. Runbook: `deploy/DEPLOY.md`. OpenBao KV + AppRole: `deploy/OPENBAO.md`.

```bash
# Local (no OpenBao)
export ENCRYPTION_KEY="$(openssl rand -base64 32)"
export BOOTSTRAP_ADMIN_EMAIL=you@co.com BOOTSTRAP_ADMIN_PASSWORD='...'
export SESSION_SECRET='...' PUBLIC_BASE_URL=http://localhost:8080
./outreachcrm

# Optional local Postgres search index (does not move CRM data off SQLite):
#   make postgres-up
#   export DATABASE_URL='postgres://outreach:outreach@127.0.0.1:5432/outreachcrm?sslmode=disable'
#   make run
# Search backend in the UI becomes pgvector-hybrid(…). `make postgres-down` stops the container (volume kept).

# Production: OPENBAO_ENABLED=true + AppRole in /var/www/outreachcrm/.env
# Secrets loaded from secret/data/vertexcrm/outreach/production before config binds.
```

Reverse proxy (nginx on VPS) terminates TLS; leave `TLS_*` unset in the container.

## 4. The six-step product loop

| Step | Where in app |
|------|----------------|
| **1 Sourcing** | `/leads` — manual + CSV (header-aware); playbook seed adds campaigns/templates only (no dummy leads) |
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
| SQLite backups + PII retention purge | Yes — operational CRM is SQLite. Postgres (if `DATABASE_URL` is set) is **search index only** |
| Message leases for multi-instance | Yes |
| healthz/readyz/metrics + slog | Yes |
| GDPR export/delete + consent fields | Yes |
| CSV import, analytics, templates | Yes |
| **Email Deliverability Engine** | Yes — `/deliverability` + pre-send gate |
| **Global search** | Yes — `/search` + topbar; Zvec by default, or pgvector hybrid when `DATABASE_URL` is set |
| **Staff AI + marketing ESP** | Yes — dashboard/inbox copilot; mailbox IMAP vs paid campaign SMTP |

## 5. Key routes

- `/search`, `GET /search/results` — global search (leads, campaigns, inbox, queue, templates, accounts)
- `POST /search/reindex` — admin rebuild of the search index
- `/deliverability` — reputation dashboard, validate, DNSBL, decision log
- `/security` — TOTP; `/privacy/export`
- `/hitl` — positive reply queue
- `/domains` — DNS checks
- `/analytics`, `/templates`, `/audit`, `/workspaces`
- `POST /webhooks/postmark`, `POST /webhooks/ses` — bounce/complaint → suppression
- `/leads/import` — CSV bulk
- `go run ./cmd/smartlead-import` — one-shot Smartlead API → SQLite (paused campaigns, no sequencer queue)
- `POST /api/dashboard/ai/chat`, `POST /api/inbox/ai/chat` — staff AI (tool-calling, confirm-gated writes)
- `/settings/email` — workspace AI prompts + bulk marketing SMTP

## 6. Gotchas

- **Global search:** Default `make build` / `make run` (empty `DATABASE_URL`) use [Alibaba Zvec](https://github.com/alibaba/zvec) — dense HNSW (`embedding`, 1536-d cosine) + native FTS fused with MultiQuery RRF. Index under `DATA_DIR/search/zvec/`. `make build-lite` is SQLite FTS5-only (no CGO). **Opt-in Postgres:** set `DATABASE_URL` to `postgres://` or `postgresql://` (local: `make postgres-up`, compose file `docker-compose.postgres.yml`, image `pgvector/pgvector:pg16`). Search then uses a `search_docs` table: **HNSW cosine** on `vector(1536)`, **GIN** on generated `tsvector` (`plainto_tsquery` + `ts_rank_cd`), **GIN trigram** (`pg_trgm`) on email/name/company; Go **RRF** (k=60) fuses the three lists. Embeddings reuse `OPENAI_EMBED_MODEL` (default `text-embedding-3-small`) when `OPENAI_API_KEY` is set, else the hash embedder. Extensions: `vector`, `pg_trgm`, `unaccent`. Admins: `POST /search/reindex`. Size cap is 80 MB; ship `libzvec_c_api` when using Zvec.
- **Postgres vs SQLite:** Production CRM (leads, campaigns, queue, users, Smartlead import, etc.) stays **SQLite WAL**. We did not dual-drive the whole store (`?` vs `$n` across every query). Postgres is the optional **search index** only. Do not point production at Postgres expecting a full cutover.
- **SSO:** TOTP 2FA yes; enterprise SAML/OIDC IdP login not bundled (OAuth is for *mail*, not user login).
- **KMS / secrets:** production loads `ENCRYPTION_KEY` (and peers) from OpenBao KV via AppRole; app still decrypts locally with AES-GCM (not cloud KMS).
- Enrichment crawl is lightweight HTTP GET — not PageSpeed API.
- **Mailbox vs marketing SMTP:** `/accounts` is IMAP sync + HITL replies (OAuth or Titan/Zoho/Hostinger password). Campaign blasts use the workspace **Marketing SMTP** on `/settings/email` when set — personal Gmail/Outlook are not used for blasts in that case.
- **Smartlead import:** `cmd/smartlead-import` pulls campaigns, sequences, leads, sent stats, replies, and suppressions into a workspace named **Smartlead**. Campaigns stay **paused**; mailboxes `provider=smartlead` and `daily_quota=0` (no SMTP secrets — they cannot send). Historical mail is `sent`/`dead` only — never `EnrollLead` / never `scheduled`. Dashboard **Messages sent** = `outbound_messages.status='sent'` joined to that workspace’s campaigns; **Replies** = `inbound_replies` for the workspace. `/queue` shows due rows plus a **Sent history** table (click to read body); lead inspector shows the thread. Re-run is **resumable**: mapped campaigns/leads still backfill missing stats + replies (idempotent `smartlead_map` + message_id). Stats and leads stream **per API page** (insert then WAL `TRUNCATE`); the importer never holds a full campaign of HTML stats in RAM. Key: `SMARTLEAD_API_KEY` or `--api-key-file`. Prod: backup SQLite, then `docker compose exec app /app/smartlead-import --data-dir /data` (safe to re-run after a drop).
- **Staff AI:** `OUTREACH_AI_MODE=off|suggest|auto` (default off). Suggest saves inbound drafts; auto never sends free-form mail. Writes from chat require confirm. See `docs/AI-ASSIST.md`.
- Watch out: if Smartlead import stops after leads, dashboard **Messages sent / Replies stay 0** until you re-run the importer (it backfills `outbound_messages` + `inbound_replies` without re-queueing). Switch the sidebar to workspace **Smartlead** to see those KPIs.

## 7. Runbook (short)

1. Health: `curl /healthz` and `/readyz`
2. Metrics: `curl /metrics`
3. Backups land in `DATA_DIR/backups/`; also on shutdown
4. Bounce spike: check suppressions + dead letters on dashboard/analytics
5. Rotate `ENCRYPTION_KEY` requires re-entering SMTP/OAuth credentials

## 8. Changelog

- 2026-09-05 — Prospect memory, phases 4–6. **Classification** (`internal/classify`, migration 13): Smartlead's own `lead_category` is the primary label (Meeting Request → intent 55, Information Request → 40, Not Interested Now → 25); local rules catch the auto-replies it misses — a reply inside 2 minutes of the send is machine-generated, and detection runs *before* the category mapping so an enthusiastic out-of-office is not scored as interest. Results carry `classifier_version`, so a rule change is a cheap sweep, not a re-import. **Scoring** (`internal/scoring`): priority = intent (high-water mark, never summed) × decay (30-month half-life) × triggers (job change ×1.4, matured timing objection ×1.3, 90-day cooldown ×0). Opt-outs and undeliverable addresses are *excluded*, not ranked low. Components are stored on `person_signal` so `ListProspectsToContact` explains every recommendation. **Automation** (`internal/prospects`): a sweep every `PROSPECT_SWEEP_INTERVAL` (default 6h) reclassifies and rescores; `POST /webhooks/smartlead/{secret}` ingests inbound events idempotently — subscriptions are still created by hand in Smartlead's UI, since registering one would be a write. Identity resolution is deliberately excluded from the sweep: auto-merge alters live records and stays an explicit decision.
- 2026-09-05 — Smartlead integration is **one-way and enforced**: OutReachCRM reads, never writes. Every request in `internal/smartlead` passes through `send`, which refuses any method but GET (`ErrWriteAttempted`), and a source-level test fails if a write verb appears anywhere in the package — verified by introducing a POST helper and watching it fail. Consequence for automation: receiving a webhook is inbound and fine, but *registering* one is a POST, so subscriptions are created by hand once in the Smartlead UI; the nightly reconciliation sweep means a missed subscription delays data rather than losing it. Priority lists are exported for a human to upload, never pushed back as lead categories.
- 2026-09-05 — Prospect memory, phase 3 (identity resolution): `internal/identity` scores whether two records are the same person — email or LinkedIn are conclusive (100), phone 60, distinctive local-part 35, a domain the person cited in a reply 30, name 25, bounce-then-move 20, disjoint employment 15; a *different* name vetoes everything (-100). ≥100 auto-merges, 50–99 queues for review, below that stays separate. Generic mailboxes (`info@`, `sales@`) carry no weight and are not block keys. Candidate generation blocks on name / local-part / phone / LinkedIn: 60k people resolve in ~274ms with 3,928 comparisons instead of ~1.8 billion. **Merging moves no rows** — it sets `person.merged_into_id` and every lookup resolves through the chain, which is what makes `UnmergePerson` exact; each merge writes a `merge` event carrying the absorbed record's snapshot. `RecordJobChange` closes the open employment row and opens a new one, emitting the `job_change` event scoring keys off. Not wired into the importer: resolution is an explicit operation because auto-merge changes live records.
- 2026-09-05 — Prospect memory, phase 2 (import): Smartlead client now paces itself at 55 req/min (the documented limit is 60/60s) with `Retry-After` honoured, and page size is clamped to the verified maximum of 1000 — `limit=2000` returns an empty array with HTTP 200, which reads exactly like end-of-data, so an empty *first* page with a non-zero `total_stats` is now an error rather than a silent no-op. `StatRow` captures the fields the importer was discarding: `reply_time`, `open_time`/`click_time`, `open_count`/`click_count`, `lead_category` (Smartlead's own reply classification, free on every row) and `ignore_reply`. Campaign `track_settings` is stored per campaign (migration 11 `campaign_tracking`) because six of seven live campaigns disable open/click tracking, and scoring must read that as "not measured" rather than "not interested". Migration 12 adds `import_cursor` for resumable imports: progress commits per page, so an interrupted run resumes instead of restarting. Import now runs `BackfillProspects` + `BackfillEventsFromMessages` at the end, both idempotent.
- 2026-09-05 — Prospect memory, phase 1 (schema): `person` / `person_email` / `person_employment` / `prospect_event` / `person_signal` / `identity_candidate` / `campaign_tracking`, migration 11. Identity is separated from address — uniqueness lives on `person_email`, so a job change adds an address instead of splitting someone's history into two records. Employment is rows with validity windows, so a promotion stays visible. `prospect_event` is append-only and deduped on `dedupe_key`, making imports re-runnable. Additive: `leads` is untouched and `person.lead_id` links the two. `Store.BackfillProspects()` is idempotent and runs the full base in ~3s. Design: see the Prospect Memory System artifact.
- 2026-09-04 — CRM store ports to Postgres: `DATABASE_URL=postgres://…` now backs the **whole CRM**, not just `/search`. Queries stay written in the SQLite flavor and `internal/store/dialect.go` translates them (`?`→`$N`, `INSERT OR IGNORE`→`ON CONFLICT DO NOTHING`, `id INTEGER PRIMARY KEY AUTOINCREMENT`→`BIGSERIAL`, `ADD COLUMN IF NOT EXISTS`); migration transactions run savepoint-protected so an intentionally ignored error cannot abort them. Postgres gets a 25-conn pool instead of SQLite's single writer. Move existing data with `cmd/sqlite-to-postgres` (`--dry-run` first) **before** setting `DATABASE_URL` in production, or the CRM comes up empty. Unset `DATABASE_URL` keeps SQLite.
- 2026-09-04 — Opt-in Postgres search: `DATABASE_URL=postgres://…` indexes `/search` on pgvector HNSW + FTS GIN + trigram (RRF in Go). CRM data stays SQLite. Local: `make postgres-up` (`docker-compose.postgres.yml`).
- 2026-09-04 — Smartlead importer streams stats (and lead) pages into SQLite instead of holding a full campaign in memory; WAL TRUNCATE after each page.
- 2026-09-04 — Smartlead import backfill: resumable stats/replies (even if campaigns already mapped), persist `email_subject`/`email_message` on `outbound_messages`, show sent history on `/queue` + lead inspector, workspace-scoped sent/reply KPIs, WAL-batched writes.
- 2026-09-04 — Smartlead one-shot import (`cmd/smartlead-import`): campaigns/sequences/leads/stats/replies/accounts/suppressions into workspace **Smartlead**; campaigns stay paused; no mailbox secrets; idempotent `smartlead_map`.
- 2026-08-18 — Staff AI (dashboard + inbox tool chat), password IMAP presets (Titan/Zoho/Hostinger), workspace marketing SMTP for campaign blasts (personal mailboxes stay for sync + HITL). Env: `OUTREACH_AI_MODE`. Docs: `docs/AI-ASSIST.md`.
- 2026-07-20 — `/users` (and Admin nav: Users / Workspaces / Audit) is admin-only; sender role is redirected to `/` and never shown the Admin nav section.
- 2026-07-19 — Campaign funnel tracker: enroll audience → records which campaign funnel it runs; `/funnels` shows queued/sent/replied/positive/step distribution per audience×campaign (octavertex-growth wiring).
- 2026-07-19 — Multi-workspace: auto OctaVertex Media + RevNext tenants; onboard any new workspace with optional playbook pack; admin switcher; assign users to workspace; brand seed splits OVM/RevNext packs; lists scoped to active workspace.
- 2026-07-19 — Audiences: saved lead filters (category/source/enrichment/company/email) with member snapshot, live preview, bulk enroll into campaigns; filter bar + “Save as audience” on `/leads`; primary campaign enroll path.
- 2026-07-19 — Dashboard business snapshot: KPI strip + pipeline funnel, D3 world bubbles (TLD→country), campaign treemap & activity bars, prev-vs-new leads area, category donut, ICP word cloud, reply intents; JSON at `GET /api/dashboard/snapshot`.
- 2026-07-19 — Search categories: All / Leads / Campaigns / Email / Inbox / Queue / Templates / Accounts, plus field chips (name, email, phone, website, company, subject, notes).
- 2026-07-19 — Search page uses `app-body` appshell (sidebar/topbar/inspector) like other console pages.
- 2026-07-19 — Global search is full Zvec hybrid (HNSW + FTS + MultiQuery RRF); size cap raised to 80 MB; OpenAI embeddings with hash fallback; `make build-lite` for pure-Go FTS5 only.
- 2026-07-19 — Global search: topbar + `/search` over leads/campaigns/inbox/queue/templates/accounts.
- 2026-07-19 — Deliverability harden: DNSBL on send (24h cache), workspace-aware ESP webhooks + soft bounces, open/click tracking (`/t/…`), purchased-list flag, honest proxy labels on `/deliverability`.
- 2026-07-19 — Simplified console surfaces: landing, inbox, HITL, analytics, deliverability, domains, accounts, templates, workspaces — same page-hero / collapse pattern as campaigns & leads.
- 2026-07-19 — `/leads` simplified; stop seeding dummy/ICP example.* leads; one-time purge on first leads view; CSV template is header-only.
- 2026-07-19 — `/campaigns` simplified: compact list, collapsed sequence/enroll, seed only on empty or behind details; create defaults IST 9–18 · 20/day.
- 2026-07-19 — Octavertex growth wired into product: seed campaign **OVM · Manufacturing Lead Platform (₹1.25L+)** (Day 0/2/5/10/21), Mfg templates + ICP leads, header-aware CSV import (`company/title/source/notes`), `/static/ovm-manufacturing-icp.csv` + `octavertex-growth/playbook/CRM-WIRING.md`.
- 2026-07-16 — OpenBao multi-app catalog + seed: add `octavertex/project100` (AppRole `project100`, nested aws/pocketbase/database/app groups).
- 2026-07-16 — Public landing at `/` (guests) + VertexCRM favicon/logo branding shared with AgencyCRM; signed-in users still get the dashboard.
- 2026-07-16 — Contabo deploy for `outreach.vertexcrm.in` (Docker/nginx `:8003`) + OpenBao AppRole secret overlay (`secrets.revnext.in`, path `vertexcrm/outreach/production`).
- 2026-07-16 — Company playbooks seed: OctaVertex Media + RevNext (PMS/POS/booking/B2B/CMS/revenue) templates, sequences, ICP leads via `POST /leads/seed-playbooks`.
- 2026-07-16 — Email Deliverability Engine: validation, bounce/trap/engagement scoring, content/ISP/warmup, auto-suppress, complaint pause, `/deliverability` UI, pre-send gate.
- 2026-07-16 — Pipeline v4: lead source/company/title/drafts, funnel + queue UI, save/apply AI drafts into sequences, reply suggest + outbound replied tracking, analytics funnel stats.
- 2026-07-16 — Production v2: workspaces, audit, TOTP, ESP/webhooks, warmup/domain limits, DNS checks, timezone windows, crawl enrichment, HITL, GDPR, CSV import, analytics/templates, backups/PII purge, leases, TLS option.
- 2026-07-16 — Production pass 1: RBAC, OAuth IMAP, AES-GCM, retries.
- 2026-07-16 — MVP.
