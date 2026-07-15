# OutReachCRM

Production-lean Go + HTMX outreach CRM (≤30 MB): workspaces, TOTP, OAuth/ESP, IMAP+HITL, DNS checks, durable leased queue, GDPR, backups.

## Standing rules

- Keep `make build-size` ≤30 MB; no Google/Azure full SDKs, no Chromium.
- Update `HANDBOOK.md` changelog after behavior changes.
- Secrets via env / secret manager → `ENCRYPTION_KEY`; never commit `.env` or DB files.
