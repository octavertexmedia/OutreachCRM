#!/usr/bin/env bash
# Runs on the VPS via: ssh ... 'export ...; bash -s' < deploy/contabo-remote.sh
# Same Contabo host as AgencyCRM (/var/www/agency-crm). Nginx vhost for SITE_DOMAIN.
# Never touches OpenBao / ~/revnext-secrets.
set -euo pipefail

: "${DEPLOY_DIR:?}"
: "${IMAGE_DIR:?}"
: "${SITE_DOMAIN:?}"
: "${VPS_HOST:?}"

CERT_EMAIL="${CERT_EMAIL:-admin@${SITE_DOMAIN}}"
SERVER_NAMES="${SITE_DOMAIN}"
if [ -n "${SITE_DOMAIN_WWW:-}" ]; then
  SERVER_NAMES="${SERVER_NAMES} ${SITE_DOMAIN_WWW}"
fi

NGX_ENABLED_NAME="outreachcrm"
UPSTREAM_PORT="8003"

reload_outreach_nginx() {
  local enabled target
  for enabled in /etc/nginx/sites-enabled/*; do
    [ -L "$enabled" ] || continue
    target="$(readlink -f "$enabled" 2>/dev/null || true)"
    if [ -z "$target" ] || [ ! -f "$target" ]; then
      echo "[WARN] removing broken nginx symlink: $enabled"
      rm -f "$enabled"
    fi
  done
  rm -f /etc/nginx/sites-enabled/outreachcrm
  ln -sf "$NGX_SITE" "/etc/nginx/sites-enabled/${NGX_ENABLED_NAME}"
  if [ -x /usr/local/bin/nginx-safe-reload.sh ]; then
    /usr/local/bin/nginx-safe-reload.sh
  else
    nginx -t && systemctl reload nginx
  fi
}

link_openbao_network() {
  local net cid
  cid="$(docker compose -f docker-compose.yml ps -q app 2>/dev/null | head -1 || true)"
  if [ -n "$cid" ]; then
    net="$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{println $k}}{{end}}' "$cid" 2>/dev/null | head -1 || true)"
  fi
  if [ -z "$net" ]; then
    net="$(docker network ls --format '{{.Name}}' | grep -E 'outreachcrm_default' | head -1 || true)"
  fi
  if [ -n "$net" ] && docker inspect revnext_secrets_openbao >/dev/null 2>&1; then
    docker network connect --alias openbao "$net" revnext_secrets_openbao 2>/dev/null \
      || echo "[INFO] OpenBao already on $net (or connect skipped)"
    if grep -qE '^OPENBAO_ADDR=http://172\.17\.0\.1:8200' "$DEPLOY_DIR/.env" 2>/dev/null; then
      sed -i 's|^OPENBAO_ADDR=http://172\.17\.0\.1:8200|OPENBAO_ADDR=http://openbao:8200|' "$DEPLOY_DIR/.env" || true
      echo "[INFO] OPENBAO_ADDR → http://openbao:8200"
    fi
  else
    echo "[WARN] OpenBao container revnext_secrets_openbao not linked — ensure ~/revnext-secrets is up"
  fi
}

mkdir -p "$DEPLOY_DIR"
cd "$DEPLOY_DIR"

if ! command -v docker &>/dev/null || ! docker compose version &>/dev/null; then
  apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ca-certificates curl
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  . /etc/os-release
  [ "$ID" = "debian" ] && REPO="debian" || REPO="ubuntu"
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/$REPO $VERSION_CODENAME stable" > /etc/apt/sources.list.d/docker.list
  apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable docker && systemctl start docker
fi

ensure_docker_registry_dns() {
  if timeout 15 sh -c 'getent hosts registry-1.docker.io' >/dev/null 2>&1; then
    return 0
  fi
  echo "[WARN] Cannot resolve registry-1.docker.io — Docker pulls will fail."
  if [ "${VPS_AUTO_FIX_DNS:-0}" = "1" ] && systemctl is-active --quiet systemd-resolved 2>/dev/null; then
    echo "[INFO] VPS_AUTO_FIX_DNS=1 — writing DNS resolvers for deploy"
    mkdir -p /etc/systemd/resolved.conf.d
    cat >/etc/systemd/resolved.conf.d/99-outreachcrm-deploy.conf <<'RESOLVED'
[Resolve]
DNS=8.8.8.8 1.1.1.1
FallbackDNS=8.8.4.4
RESOLVED
    systemctl restart systemd-resolved
    sleep 2
    systemctl restart docker 2>/dev/null || true
    sleep 2
  fi
  if timeout 15 sh -c 'getent hosts registry-1.docker.io' >/dev/null 2>&1; then
    echo "[INFO] registry-1.docker.io resolves OK."
    return 0
  fi
  echo "[ERROR] Fix DNS on this host, then redeploy."
  exit 1
}
ensure_docker_registry_dns

echo "[1/7] Copying compose..."
cp "$IMAGE_DIR/docker-compose.yml" "$DEPLOY_DIR/docker-compose.yml"

if [ ! -f "$DEPLOY_DIR/.env" ]; then
  echo "[INFO] Creating initial bootstrap .env (OPENBAO_* + non-secret defaults)."
  {
    echo "ADDR=:8080"
    echo "DATA_DIR=/data"
    echo "COOKIE_SECURE=true"
    echo "DRY_RUN_SMTP=true"
    echo "PUBLIC_BASE_URL=https://${SITE_DOMAIN}"
    echo "OAUTH_REDIRECT_BASE=https://${SITE_DOMAIN}"
    echo "APP_VERSION=2.0.0"
    echo ""
    echo "# OpenBao — secrets.revnext.in / ~/revnext-secrets (set AppRole after bao policy create)"
    echo "OPENBAO_ENABLED=true"
    echo "OPENBAO_REQUIRED=true"
    echo "OPENBAO_ADDR=http://172.17.0.1:8200"
    echo "OPENBAO_MOUNT_POINT=secret"
    echo "OPENBAO_SECRET_PATH=vertexcrm/outreach"
    echo "OPENBAO_ENVIRONMENT=production"
    echo "OPENBAO_ROLE_ID="
    echo "OPENBAO_SECRET_ID="
  } > "$DEPLOY_DIR/.env"
  echo "[WARN] Fill OPENBAO_ROLE_ID / OPENBAO_SECRET_ID and seed KV at secret/data/vertexcrm/outreach/production"
fi

echo "[2/7] Stopping previous stack (OpenBao left alone)..."
docker compose -f docker-compose.yml down --remove-orphans 2>/dev/null || true

docker rmi outreachcrm-app:latest 2>/dev/null || true
if [ -f "$IMAGE_DIR/outreachcrm-src.tar.gz" ]; then
  echo "[3/7] Building image on VPS..."
  BUILD_CTX="${IMAGE_DIR}/build-context"
  rm -rf "$BUILD_CTX"
  mkdir -p "$BUILD_CTX"
  tar xzf "$IMAGE_DIR/outreachcrm-src.tar.gz" -C "$BUILD_CTX"
  export DOCKER_BUILDKIT=1
  docker build -t outreachcrm-app:latest "$BUILD_CTX"
  echo "[3b/7] Keeping app-src under $DEPLOY_DIR for docker compose build..."
  rm -rf "$DEPLOY_DIR/app-src"
  cp -a "$BUILD_CTX" "$DEPLOY_DIR/app-src"
else
  echo "[3/7] Loading pre-built image from tarball..."
  gzip -dc "$IMAGE_DIR/outreachcrm-app.tar.gz" | docker load
fi

echo "[4/7] Starting containers..."
docker compose -f docker-compose.yml up -d --force-recreate

echo "[5/7] Link OpenBao network alias (if secrets stack running)..."
link_openbao_network
# Restart once so updated OPENBAO_ADDR (openbao hostname) takes effect
docker compose -f docker-compose.yml up -d --force-recreate 2>/dev/null || true

echo "[6/7] Nginx (site: ${SITE_DOMAIN})..."
DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nginx certbot python3-certbot-nginx 2>/dev/null || true

NGX_SITE="/etc/nginx/sites-available/outreachcrm"
CERT_PATH="/etc/letsencrypt/live/${SITE_DOMAIN}/fullchain.pem"

write_nginx_ssl() {
  cat >"$NGX_SITE" <<NGX
server {
    listen 80;
    listen [::]:80;
    server_name ${SERVER_NAMES};
    location /.well-known/acme-challenge/ {
      alias /var/www/certbot/.well-known/acme-challenge/;
    }
    location / {
      return 301 https://\$host\$request_uri;
    }
}
server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name ${SERVER_NAMES};
    ssl_certificate /etc/letsencrypt/live/${SITE_DOMAIN}/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/${SITE_DOMAIN}/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    client_max_body_size 20M;
    location / {
      proxy_pass http://127.0.0.1:${UPSTREAM_PORT};
      proxy_set_header Host \$host;
      proxy_set_header X-Real-IP \$remote_addr;
      proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
      proxy_set_header X-Forwarded-Proto \$scheme;
    }
}
NGX
}

write_nginx_http() {
  cat >"$NGX_SITE" <<NGX
server {
    listen 80;
    listen [::]:80;
    server_name ${SERVER_NAMES};
    client_max_body_size 20M;
    location /.well-known/acme-challenge/ {
      alias /var/www/certbot/.well-known/acme-challenge/;
    }
    location / {
      proxy_pass http://127.0.0.1:${UPSTREAM_PORT};
      proxy_set_header Host \$host;
      proxy_set_header X-Real-IP \$remote_addr;
      proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
      proxy_set_header X-Forwarded-Proto \$scheme;
    }
}
NGX
}

if [ -f "$CERT_PATH" ]; then
  echo "[INFO] Existing certificate — enabling HTTPS config."
  write_nginx_ssl
else
  mkdir -p /var/www/certbot/.well-known/acme-challenge
  chmod -R 755 /var/www/certbot
  write_nginx_http
  reload_outreach_nginx
  echo "[INFO] Requesting certificate..."
  CB=(certbot certonly --webroot -w /var/www/certbot --non-interactive --agree-tos -m "$CERT_EMAIL" -d "$SITE_DOMAIN")
  [ -n "${SITE_DOMAIN_WWW:-}" ] && CB+=(-d "$SITE_DOMAIN_WWW")
  "${CB[@]}" || echo "[WARN] Certbot failed — check DNS A record for ${SITE_DOMAIN} → this VPS."
  if [ -f "$CERT_PATH" ]; then
    write_nginx_ssl
  fi
fi

reload_outreach_nginx
ufw allow 80/tcp 2>/dev/null || true
ufw allow 443/tcp 2>/dev/null || true
ufw allow 22/tcp 2>/dev/null || true
(ufw status | grep -q "Status: active") && ufw --force reload 2>/dev/null || true

echo "[7/7] Cleanup image staging..."
sleep 8
rm -rf "$IMAGE_DIR"
echo "[SUCCESS] outreachcrm deploy complete → https://${SITE_DOMAIN}/"
docker compose -f docker-compose.yml ps
