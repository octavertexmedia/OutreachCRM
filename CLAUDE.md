# OutReachCRM

Production-lean Go + HTMX outreach CRM: workspaces, TOTP, OAuth/ESP, IMAP+HITL, DNS checks, durable leased queue, GDPR, backups, global hybrid search via Alibaba Zvec.

## Standing rules

- Keep `make build-size` ≤80 MB (raised for Zvec CGO hybrid search). No Google/Azure full SDKs, no Chromium.
- Default build is Zvec (`-tags zvec`): dense HNSW + native FTS + MultiQuery RRF. Use `make build-lite` only when CGO is unavailable.
- `DATABASE_URL=postgres://...` moves **both** the CRM store and `/search` to Postgres (pgvector HNSW + FTS GIN + trigram). Unset keeps SQLite + Zvec/SQLite FTS.
- Store queries are written in the SQLite flavor only; `internal/store/dialect.go` translates them for Postgres. Do not hand-write `$N` placeholders in `internal/store`.
- Migrating an existing deploy: run `cmd/sqlite-to-postgres` before setting `DATABASE_URL`, else the CRM starts empty.
- Update `HANDBOOK.md` changelog after behavior changes.
- Secrets via env / secret manager → `ENCRYPTION_KEY`; never commit `.env` or DB files.
