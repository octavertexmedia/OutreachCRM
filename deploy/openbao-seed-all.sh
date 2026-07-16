#!/usr/bin/env bash
# Seed OpenBao at secrets.revnext.in for all OctaVertex / RevNext / Vertex apps.
#
# Usage:
#   export BAO_ADDR=https://secrets.revnext.in
#   export BAO_TOKEN='admin-token'
#   export SEED_ADMIN_EMAIL='you@example.com'   # optional
#   ./deploy/openbao-seed-all.sh
#   ./deploy/openbao-seed-all.sh --print-creds
#   FORCE_KV=1 ./deploy/openbao-seed-all.sh      # overwrite KV placeholders
#
# Requires: bao or vault CLI, openssl, jq (optional).
set -euo pipefail

PRINT_CREDS=0
if [ "${1:-}" = "--print-creds" ]; then
  PRINT_CREDS=1
fi

BAO_ADDR="${BAO_ADDR:-https://secrets.revnext.in}"
export BAO_ADDR
: "${BAO_TOKEN:?Set BAO_TOKEN to an OpenBao admin/root token}"

if command -v bao >/dev/null 2>&1; then
  CLI=bao
elif command -v vault >/dev/null 2>&1; then
  CLI=vault
  export VAULT_ADDR="$BAO_ADDR"
  export VAULT_TOKEN="$BAO_TOKEN"
else
  echo "[ERROR] Install OpenBao CLI (bao) or Hashicorp vault CLI." >&2
  exit 1
fi

# bao uses BAO_*; vault uses VAULT_*
export BAO_TOKEN
export VAULT_TOKEN="${VAULT_TOKEN:-$BAO_TOKEN}"
export VAULT_ADDR="${VAULT_ADDR:-$BAO_ADDR}"

SEED_ADMIN_EMAIL="${SEED_ADMIN_EMAIL:-admin@vertexcrm.in}"
FORCE_KV="${FORCE_KV:-0}"
CREDS_TMP="$(mktemp)"
trap 'rm -f "$CREDS_TMP"' EXIT

echo "[INFO] OpenBao addr=$BAO_ADDR cli=$CLI"

# --- helpers ---
run() { "$CLI" "$@"; }

ensure_kv() {
  if run secrets list -format=json 2>/dev/null | grep -q '"secret/"'; then
    echo "[OK] KV mount secret/ present"
  else
    echo "[INFO] Enabling kv-v2 at secret/"
    run secrets enable -path=secret kv-v2 || true
  fi
}

ensure_approle() {
  run auth enable approle 2>/dev/null || true
}

write_policy() {
  local name="$1" path_prefix="$2"
  run policy write "$name" - <<EOF
path "secret/data/${path_prefix}/*" {
  capabilities = ["read"]
}
path "secret/metadata/${path_prefix}/*" {
  capabilities = ["read", "list"]
}
EOF
  echo "[OK] policy $name → secret/data/${path_prefix}/*"
}

ensure_role() {
  local role="$1" policy="$2"
  run write "auth/approle/role/${role}" \
    token_policies="${policy}" \
    token_ttl=1h \
    token_max_ttl=4h \
    secret_id_ttl=0 >/dev/null
  local role_id secret_id
  role_id="$(run read -field=role_id "auth/approle/role/${role}/role-id")"
  secret_id="$(run write -f -field=secret_id "auth/approle/role/${role}/secret-id")"
  printf '%s\t%s\t%s\n' "$role" "$role_id" "$secret_id" >>"$CREDS_TMP"
  echo "[OK] AppRole $role"
}

kv_exists() {
  local path="$1"
  run kv get "secret/${path}" >/dev/null 2>&1
}

# Merge-safe put: if path exists and FORCE_KV!=1, skip.
kv_seed() {
  local path="$1"
  shift
  if kv_exists "$path" && [ "$FORCE_KV" != "1" ]; then
    echo "[SKIP] secret/${path} already exists (FORCE_KV=1 to overwrite)"
    return 0
  fi
  run kv put "secret/${path}" "$@" >/dev/null
  echo "[OK] kv put secret/${path}"
}

# --- bootstrap engines ---
ensure_kv
ensure_approle

# Catalog: role | policy | path_prefix (without /production)
# shellcheck disable=SC2034
APPS=(
  "channel-manager|revnext-channel-manager-read|revnext/channel-manager"
  "cms|revnext-cms-read|revnext/cms"
  "keycloak|revnext-keycloak-read|revnext/keycloak"
  "agency|vertexcrm-agency-read|vertexcrm/agency"
  "outreach|vertexcrm-outreach-read|vertexcrm/outreach"
  "happynails|octavertex-happynails-read|octavertex/happynails"
  "packmold|octavertex-packmold-read|octavertex/packmold"
  "suratbazaar|octavertex-suratbazaar-read|octavertex/suratbazaar"
  "project100|octavertex-project100-read|octavertex/project100"
)

for entry in "${APPS[@]}"; do
  IFS='|' read -r role policy prefix <<<"$entry"
  write_policy "$policy" "$prefix"
  ensure_role "$role" "$policy"
done

# --- KV placeholders (production) ---
# Shared cross-app reference (admin token only; no product AppRole)
kv_seed "shared/production/meta" \
  OWNER="octavertex" \
  NOTE="Copy values into per-app paths; do not grant apps access here"

# Channel Manager
kv_seed "revnext/channel-manager/production" \
  SITE_URL="https://channel-manager.revnext.in" \
  ENVIRONMENT="production" \
  DEBUG="false"

# CMS
kv_seed "revnext/cms/production" \
  SITE_URL="https://cms.revnext.in" \
  PUBLIC_APP_URL="https://app.revnext.in" \
  ENVIRONMENT="production"

# Keycloak / auth
kv_seed "revnext/keycloak/production" \
  KC_HOSTNAME="auth.revnext.in" \
  NOTE="Store KC admin + DB + client secrets here"

# AgencyCRM
SESS_A="$(openssl rand -base64 48)"
kv_seed "vertexcrm/agency/production" \
  SECRET_KEY="$SESS_A" \
  PUBLIC_SITE_URL="https://vertexcrm.in" \
  ENV="production"

# OutReachCRM
SESS_O="$(openssl rand -base64 48)"
ENC_O="$(openssl rand -base64 32)"
kv_seed "vertexcrm/outreach/production" \
  SESSION_SECRET="$SESS_O" \
  ENCRYPTION_KEY="$ENC_O" \
  BOOTSTRAP_ADMIN_EMAIL="$SEED_ADMIN_EMAIL" \
  BOOTSTRAP_ADMIN_PASSWORD="$(openssl rand -base64 18)" \
  PUBLIC_BASE_URL="https://outreach.vertexcrm.in" \
  OAUTH_REDIRECT_BASE="https://outreach.vertexcrm.in" \
  COOKIE_SECURE="true" \
  DRY_RUN_SMTP="true"

# HappyNails / Packmold / SuratBazaar placeholders
kv_seed "octavertex/happynails/production" \
  SECRET_KEY="$(openssl rand -base64 48)" \
  ENVIRONMENT="production" \
  NOTE="Replace with live HappyNails secrets"

kv_seed "octavertex/packmold/production" \
  SECRET_KEY="$(openssl rand -base64 48)" \
  ENVIRONMENT="production" \
  NOTE="Replace with live Packmold secrets"

kv_seed "octavertex/suratbazaar/production" \
  SECRET_KEY="$(openssl rand -base64 48)" \
  ENVIRONMENT="production" \
  NOTE="Replace with live SuratBazaar secrets"

# Project100 (Hugo sites + PocketBase CMS + analytics stack)
kv_seed "octavertex/project100/production" \
  ENVIRONMENT="production" \
  POCKETBASE_URL="https://cms.octavertexmedia.com" \
  UMAMI_PUBLIC_URL="https://analytics.octavertexmedia.com" \
  MEDIA_TRACKER_URL="https://track.octavertexmedia.com/click.js" \
  NOTE="Fill nested aws/pocketbase/database/app groups with live secrets"

kv_seed "octavertex/project100/production/aws" \
  AWS_REGION="us-east-1" \
  NOTE="Set AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY (or migrate CI to OIDC)"

kv_seed "octavertex/project100/production/cloudflare" \
  NOTE="Set CLOUDFLARE_ACCOUNT_ID + CLOUDFLARE_API_TOKEN (S3 static CDN)"

kv_seed "octavertex/project100/production/pocketbase" \
  POCKETBASE_URL="https://cms.octavertexmedia.com" \
  NOTE="Set POCKETBASE_TOKEN (+ admin credentials if needed)"

kv_seed "octavertex/project100/production/database" \
  DATABASE_USERNAME="project100" \
  DATABASE_NAME="project100" \
  UMAMI_DB_USER="umami" \
  UMAMI_DB_NAME="umami" \
  CLICKHOUSE_DB="project100_analytics" \
  CLICKHOUSE_USER="project100" \
  NOTE="Set DATABASE_PASSWORD REDIS_PASSWORD UMAMI_DB_PASSWORD CLICKHOUSE_PASSWORD UMAMI_APP_SECRET"

kv_seed "octavertex/project100/production/app" \
  ENFORCE_SEO_ON_SUPERUSER="1" \
  TRACKER_RPM_PER_IP="120" \
  NOTE="Set TRACKER_IP_SALT TRACKER_SITE_KEYS GRAFANA_ADMIN_PASSWORD"

echo ""
echo "========== AppRole credentials =========="
echo -e "ROLE\tROLE_ID\tSECRET_ID"
if [ "$PRINT_CREDS" = "1" ]; then
  cat "$CREDS_TMP"
else
  echo "(hidden — re-run with --print-creds to show role_id/secret_id)"
  echo "Saved temporarily during this run only."
  # still print for operator convenience when seeding interactively
  cat "$CREDS_TMP"
fi
echo "========================================="
echo ""
echo "[NEXT] For each app server .env:"
echo "  OPENBAO_ENABLED=true"
echo "  OPENBAO_REQUIRED=true"
echo "  OPENBAO_ADDR=https://secrets.revnext.in   # or http://openbao:8200 on shared Contabo"
echo "  OPENBAO_SECRET_PATH=<org>/<app>           # e.g. vertexcrm/outreach"
echo "  OPENBAO_ENVIRONMENT=production"
echo "  OPENBAO_ROLE_ID=..."
echo "  OPENBAO_SECRET_ID=..."
echo ""
echo "[DONE] Browse UI: ${BAO_ADDR%/}/ui"
