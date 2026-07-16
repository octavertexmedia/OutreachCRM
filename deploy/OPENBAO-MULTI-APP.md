# OpenBao multi-app inventory — secrets.revnext.in

One OpenBao stack (`~/revnext-secrets` → [https://secrets.revnext.in](https://secrets.revnext.in)) holds secrets for **all** OctaVertex / RevNext / Vertex products. Apps only keep `OPENBAO_*` bootstrap credentials in their server `.env`.

Seed script: [`openbao-seed-all.sh`](./openbao-seed-all.sh).

## Path convention

```text
secret/data/{org}/{app}/{environment}[/{group}]
```

| Segment | Meaning |
|---------|---------|
| `org` | Product family: `revnext`, `vertexcrm`, `octavertex`, `shared` |
| `app` | Deployable unit (one AppRole each) |
| `environment` | `production` / `staging` |
| `group` | Optional nested KV (`django`, `database`, `oidc`, `oauth`, …) |

Domains are **values** inside secrets (`PUBLIC_BASE_URL`, `ALLOWED_HOSTS`), not path segments.

## App catalog (as many as practical)

| Org | AppRole / KV app | Domains / purpose | KV path | Notes |
|-----|------------------|-------------------|---------|-------|
| `revnext` | `channel-manager` | channel-manager, pms, pos, booking, hotels, networks, tours | `revnext/channel-manager/production` | Single Django process; already documented |
| `revnext` | `cms` | cms.revnext.in, app.revnext.in, \*.sites.revnext.in | `revnext/cms/production` | CMS VPS `84.247.183.69` → use `OPENBAO_ADDR=https://secrets.revnext.in` |
| `revnext` | `keycloak` | auth.revnext.in | `revnext/keycloak/production` | IdP admin / DB / client secrets |
| `vertexcrm` | `agency` | vertexcrm.in | `vertexcrm/agency/production` | AgencyCRM |
| `vertexcrm` | `outreach` | outreach.vertexcrm.in | `vertexcrm/outreach/production` | OutReachCRM |
| `octavertex` | `happynails` | HappyNails host | `octavertex/happynails/production` | Shared Contabo |
| `octavertex` | `packmold` | Packmold host | `octavertex/packmold/production` | Shared Contabo |
| `octavertex` | `suratbazaar` | SuratBazaar host | `octavertex/suratbazaar/production` | Shared Contabo |
| `octavertex` | `project100` | cms/editor/analytics.octavertexmedia.com + Hugo→S3 sites | `octavertex/project100/production` | Nested `aws` / `pocketbase` / `database` / `app` |
| `shared` | _(admin only)_ | cross-app SMTP etc. | `shared/production` | No AppRole to all apps; copy values into each app path when needed |

Optional later (same pattern): `octavertex/readymadesteel`, `octavertex/mashotels`, tenant paths under `tenants/{id}/…`.

## Isolation rules

1. **One AppRole per app** — policy allows only that app’s path.
2. **No global read** of `secret/*` for product roles.
3. **Bootstrap `.env` on each VPS** holds only:
   - `OPENBAO_ENABLED=true`
   - `OPENBAO_REQUIRED=true` (prod)
   - `OPENBAO_ADDR` — `http://openbao:8200` same-host, else `https://secrets.revnext.in`
   - `OPENBAO_ROLE_ID` / `OPENBAO_SECRET_ID`
   - `OPENBAO_SECRET_PATH` / `OPENBAO_ENVIRONMENT`
4. Product CI must **never** wipe `~/revnext-secrets` volumes.

## Seed

```bash
export BAO_ADDR=https://secrets.revnext.in
export BAO_TOKEN='…admin…'          # short-lived; rotate after
export SEED_ADMIN_EMAIL='you@octavertexmedia.com'

# Creates policies, AppRoles, and production KV placeholders for all catalog apps.
# Skips overwriting existing KV keys unless FORCE_KV=1.
./deploy/openbao-seed-all.sh

# Print credentials into a local gitignored file:
./deploy/openbao-seed-all.sh --print-creds | tee /tmp/openbao-approles.txt
chmod 600 /tmp/openbao-approles.txt
```

Then wire each server’s `.env` with that app’s `role_id` / `secret_id`.

## UI browsing

[https://secrets.revnext.in/ui](https://secrets.revnext.in/ui) → Secrets → `secret` → browse `{org}/{app}/production`.
