# OpenBao secrets for OutReachCRM

Source of truth: **https://secrets.revnext.in/** (OpenBao on the shared Contabo VPS, stack `~/revnext-secrets`).

Product deploy never starts, stops, or wipes OpenBao volumes.

## KV layout (KV v2, mount `secret`)

```
secret/data/vertexcrm/outreach/production
# optional nested (merged if present):
secret/data/vertexcrm/outreach/production/app
secret/data/vertexcrm/outreach/production/openai
secret/data/vertexcrm/outreach/production/oauth
secret/data/vertexcrm/outreach/production/email
```

Keys are **env var names**. Recommended set:

| Key | Notes |
|-----|--------|
| `SESSION_SECRET` | Long random string |
| `ENCRYPTION_KEY` | `openssl rand -base64 32` |
| `BOOTSTRAP_ADMIN_EMAIL` | First admin (only when users table empty) |
| `BOOTSTRAP_ADMIN_PASSWORD` | Strong password |
| `OPENAI_API_KEY` | Optional |
| `OPENAI_BASE_URL` | Optional |
| `OPENAI_MODEL` | Optional |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` | Mail OAuth |
| `MICROSOFT_CLIENT_ID` / `MICROSOFT_CLIENT_SECRET` | Mail OAuth |
| `PUBLIC_BASE_URL` | `https://outreach.vertexcrm.in` |
| `OAUTH_REDIRECT_BASE` | Same as public URL |
| `COOKIE_SECURE` | `true` |
| `DRY_RUN_SMTP` | `false` when ready to send |

## One-time AppRole + seed (on VPS)

```bash
# With root/unseal token authenticated to bao/vault CLI against https://secrets.revnext.in
# or via docker: cd ~/revnext-secrets && docker compose exec -e BAO_TOKEN=... openbao bao ...

bao policy write outreachcrm-read - <<'EOF'
path "secret/data/vertexcrm/outreach/*" {
  capabilities = ["read"]
}
path "secret/metadata/vertexcrm/outreach/*" {
  capabilities = ["read", "list"]
}
EOF

bao auth enable approle 2>/dev/null || true
bao write auth/approle/role/outreachcrm \
  token_policies="outreachcrm-read" \
  token_ttl=1h \
  token_max_ttl=4h \
  secret_id_ttl=0

bao read -field=role_id auth/approle/role/outreachcrm/role-id
bao write -f -field=secret_id auth/approle/role/outreachcrm/secret-id
# Put role_id + secret_id into /var/www/outreachcrm/.env as OPENBAO_ROLE_ID / OPENBAO_SECRET_ID

ENC="$(openssl rand -base64 32)"
SESS="$(openssl rand -base64 48)"
bao kv put secret/vertexcrm/outreach/production \
  SESSION_SECRET="$SESS" \
  ENCRYPTION_KEY="$ENC" \
  BOOTSTRAP_ADMIN_EMAIL="admin@vertexcrm.in" \
  BOOTSTRAP_ADMIN_PASSWORD='CHANGE_ME' \
  PUBLIC_BASE_URL="https://outreach.vertexcrm.in" \
  OAUTH_REDIRECT_BASE="https://outreach.vertexcrm.in" \
  COOKIE_SECURE="true" \
  DRY_RUN_SMTP="true"
```

Then recreate the app container:

```bash
cd /var/www/outreachcrm
docker compose up -d --force-recreate
docker compose logs -f --tail=80 app
```

## App bootstrap env (not secrets)

`/var/www/outreachcrm/.env` should only need:

```bash
OPENBAO_ENABLED=true
OPENBAO_REQUIRED=true
OPENBAO_ADDR=http://openbao:8200   # or http://172.17.0.1:8200
OPENBAO_ROLE_ID=...
OPENBAO_SECRET_ID=...
OPENBAO_SECRET_PATH=vertexcrm/outreach
OPENBAO_ENVIRONMENT=production
```

`deploy/contabo-remote.sh` links `revnext_secrets_openbao` onto the compose network with alias `openbao` when that container exists.
