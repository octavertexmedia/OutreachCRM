# Deploy OutReachCRM → https://outreach.vertexcrm.in

Same Contabo VPS as AgencyCRM (`/var/www/agency-crm`, host port **8002**).
OutReachCRM: `/var/www/outreachcrm`, host port **8003**, nginx site `outreachcrm`.

Secrets: OpenBao at **https://secrets.revnext.in/** — see [OPENBAO.md](OPENBAO.md).

## Prerequisites

1. DNS: `outreach.vertexcrm.in` **A** → Contabo VPS IP (same as `vertexcrm.in`).
2. OpenBao stack up (`~/revnext-secrets`); AppRole + KV seeded ([OPENBAO.md](OPENBAO.md)).
3. GitHub Actions secrets on this repo:
   - `SSH_PRIVATE_KEY`, `VPS_HOST`, `SITE_DOMAIN=outreach.vertexcrm.in`
   - Optional: `VPS_USER`, `LETSENCRYPT_EMAIL`

## Automatic (recommended)

Push to `main` or run **Actions → CI/CD — Test & Deploy**.

First deploy creates `/var/www/outreachcrm/.env` with OpenBao bootstrap placeholders — set `OPENBAO_ROLE_ID` / `OPENBAO_SECRET_ID` on the VPS before the app can start with `OPENBAO_REQUIRED=true`.

## Manual

```bash
git archive --format=tar.gz -o /tmp/outreachcrm-src.tar.gz HEAD
# Upload + run deploy/contabo-remote.sh with:
#   DEPLOY_DIR=/var/www/outreachcrm
#   IMAGE_DIR=/tmp/deploy-outreachcrm
#   SITE_DOMAIN=outreach.vertexcrm.in
#   VPS_HOST=<ip>
```

## Verify

```bash
curl -sS https://outreach.vertexcrm.in/healthz
curl -sS https://outreach.vertexcrm.in/readyz
# On VPS:
cd /var/www/outreachcrm && docker compose logs --tail=100 app
```

## Port map (shared VPS)

| App | Host bind | Notes |
|-----|-----------|--------|
| AgencyCRM | `127.0.0.1:8002` | vertexcrm.in |
| OutReachCRM | `127.0.0.1:8003` | outreach.vertexcrm.in |
| OpenBao | `127.0.0.1:8200` | secrets.revnext.in |
