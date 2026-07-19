# OutReachCRM

Production-lean Go + HTMX outreach CRM: workspaces, TOTP, OAuth/ESP, IMAP+HITL, DNS checks, durable leased queue, GDPR, backups, global hybrid search via Alibaba Zvec.

## Standing rules

- Keep `make build-size` ≤80 MB (raised for Zvec CGO hybrid search). No Google/Azure full SDKs, no Chromium.
- Default build is Zvec (`-tags zvec`): dense HNSW + native FTS + MultiQuery RRF. Use `make build-lite` only when CGO is unavailable.
- Update `HANDBOOK.md` changelog after behavior changes.
- Secrets via env / secret manager → `ENCRYPTION_KEY`; never commit `.env` or DB files.
