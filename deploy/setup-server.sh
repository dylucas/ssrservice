#!/usr/bin/env bash
# setup-server.sh - one-click sspanel deployment on a fresh Linux server.
#
# The script is self-contained: it works from a cloned repo (deploy/) or when
# downloaded on its own. It installs the binary as a hardened systemd service,
# writes a safe default configuration, and can optionally set up a TLS reverse
# proxy and firewall rules. It is idempotent: re-running keeps the existing
# config.yaml and database.
#
# Usage:
#   sudo bash setup-server.sh [options]
#
# Acquisition modes (default: auto):
#   auto        Use ./bin/sspanel if present, else build from source when Go and
#               npm are available, else download a GitHub release.
#   --binary P  Install a local binary (e.g. one you scp'd over).
#   --build     Force building from source on this server (Go >= 1.25, Node >= 22).
#   --download  Force downloading a GitHub release asset.
#
# Options:
#   --version TAG       Release tag to download (default: latest).
#   --repo OWNER/REPO   GitHub repo for releases (default: ydong/ssrservice).
#   --proxy PROXY       Reverse proxy: caddy | nginx | none (default: none).
#   --domain DOMAIN     Domain for the proxy (required when --proxy is set).
#   --email EMAIL       Contact email for TLS certs (Caddy, optional).
#   --tz TZ             Timezone for the service (optional).
#   --port PORT         Open this Shadowsocks inbound port in the firewall
#                       (TCP+UDP). May be repeated.
#   --skip-firewall     Do not touch ufw/firewalld.
#   -y, --yes           Non-interactive: accept defaults without prompting.
#   -h, --help          Show this help.
#
# Environment variables:
#   SSPANEL_ADMIN_PASSWORD  Initial admin password (else random, printed once
#                           in the journal).
#   TZ                      Same as --tz.
#
# Examples:
#   sudo bash setup-server.sh -y
#   sudo bash setup-server.sh --build --proxy caddy --domain panel.example.com
#   sudo bash setup-server.sh --binary /tmp/sspanel --port 8388 -y

set -euo pipefail

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------
BIN_DIR="/usr/local/bin"
CONF_DIR="/etc/sspanel"
DATA_DIR="/var/lib/sspanel"
SERVICE_FILE="/etc/systemd/system/sspanel.service"
DROPIN_DIR="/etc/systemd/system/sspanel.service.d"
RUN_USER="sspanel"
PANEL_PORT="2096"
DEFAULT_REPO="ydong/ssrservice"

RED=$'\033[1;31m'; GRN=$'\033[1;32m'; YLW=$'\033[1;33m'; BLU=$'\033[1;34m'; NC=$'\033[0m'
log()  { printf '%s[setup]%s %s\n' "$GRN" "$NC" "$*"; }
info() { printf '%s[setup]%s %s\n' "$BLU" "$NC" "$*"; }
warn() { printf '%s[setup]%s %s\n' "$YLW" "$NC" "$*" >&2; }
die()  { printf '%s[setup] %s%s\n' "$RED" "$*" "$NC" >&2; exit 1; }

usage() { sed -n '2,41p' "${BASH_SOURCE[0]}"; }

# ---------------------------------------------------------------------------
# Option parsing
# ---------------------------------------------------------------------------
ASSUME_YES=0
BUILD_FROM_SOURCE=0
FORCE_DOWNLOAD=0
BINARY_SRC=""
VERSION="latest"
REPO="$DEFAULT_REPO"
PROXY="none"
DOMAIN=""
EMAIL=""
TZ="${TZ:-}"
INBOUND_PORTS=()
SKIP_FIREWALL=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary)        [[ $# -ge 2 ]] || die "--binary requires a path"; BINARY_SRC="$2"; shift 2;;
    --build)         BUILD_FROM_SOURCE=1; shift;;
    --download)      FORCE_DOWNLOAD=1; shift;;
    --version)       [[ $# -ge 2 ]] || die "--version requires a tag"; VERSION="$2"; shift 2;;
    --repo)          [[ $# -ge 2 ]] || die "--repo requires OWNER/REPO"; REPO="$2"; shift 2;;
    --proxy)         [[ $# -ge 2 ]] || die "--proxy requires caddy|nginx|none"; PROXY="$2"; shift 2;;
    --domain)        [[ $# -ge 2 ]] || die "--domain requires a domain"; DOMAIN="$2"; shift 2;;
    --email)         [[ $# -ge 2 ]] || die "--email requires an address"; EMAIL="$2"; shift 2;;
    --tz)            [[ $# -ge 2 ]] || die "--tz requires a timezone"; TZ="$2"; shift 2;;
    --port)          [[ $# -ge 2 ]] || die "--port requires a port number"; INBOUND_PORTS+=("$2"); shift 2;;
    --skip-firewall) SKIP_FIREWALL=1; shift;;
    -y|--yes)        ASSUME_YES=1; shift;;
    -h|--help)       usage; exit 0;;
    *)               die "unknown option: $1 (try --help)";;
  esac
done

confirm() {
  [[ "$ASSUME_YES" == 1 ]] && return 0
  local reply
  read -r -p "$1 [Y/n] " reply || reply="Y"
  case "$reply" in [nN]*) return 1;; *) return 0;; esac
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# ---------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------
[[ "$EUID" -eq 0 ]] || die "please run as root (sudo bash $0)"
[[ "$(uname -s)" == "Linux" ]] || die "this script targets Linux (got $(uname -s))"

case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64";;
  aarch64|arm64) ARCH="arm64";;
  *) die "unsupported architecture: $(uname -m)";;
esac

for cmd in install systemctl mkdir chown chmod id mktemp; do
  command -v "$cmd" >/dev/null || die "required command not found: $cmd"
done

[[ "$PROXY" == "none" || "$PROXY" == "caddy" || "$PROXY" == "nginx" ]] \
  || die "invalid --proxy value: $PROXY (expected caddy|nginx|none)"
if [[ "$PROXY" != "none" && -z "$DOMAIN" ]]; then
  die "--domain is required when --proxy is set"
fi
if (( ${#INBOUND_PORTS[@]} > 0 )); then
  for p in "${INBOUND_PORTS[@]}"; do
    [[ "$p" =~ ^[0-9]+$ ]] && (( p >= 1 && p <= 65535 )) || die "invalid port: $p"
  done
fi

# Detect distro for informational purposes / package hints.
OS_ID=""
if [[ -f /etc/os-release ]]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  OS_ID="${ID:-}"
fi

TMP_CREATED=""
cleanup() { [[ -n "$TMP_CREATED" && -f "$TMP_CREATED" ]] && rm -f "$TMP_CREATED"; }
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Acquire the binary
# ---------------------------------------------------------------------------
determine_mode() {
  if   [[ -n "$BINARY_SRC" ]]; then MODE="local"
  elif [[ "$BUILD_FROM_SOURCE" == 1 ]]; then MODE="build"
  elif [[ "$FORCE_DOWNLOAD" == 1 ]]; then MODE="download"
  elif [[ -x "$REPO_ROOT/bin/sspanel" ]]; then
    BINARY_SRC="$REPO_ROOT/bin/sspanel"; MODE="local"
  elif command -v go >/dev/null && command -v npm >/dev/null && [[ -f "$REPO_ROOT/go.mod" ]]; then
    MODE="build"
  else
    MODE="download"
  fi
}

build_from_source() {
  command -v go >/dev/null  || die "go is required to build from source (try --binary or --download)"
  command -v npm >/dev/null || die "npm is required to build the web frontend"
  [[ -f "$REPO_ROOT/go.mod" ]] || die "go.mod not found in $REPO_ROOT (use --binary or --download)"
  log "building from source in $REPO_ROOT"
  ( cd "$REPO_ROOT" && make build ) || die "build failed"
  BINARY_SRC="$REPO_ROOT/bin/sspanel"
}

download_release() {
  local url
  command -v curl >/dev/null || command -v wget >/dev/null \
    || die "curl or wget is required to download a release (try --build or --binary)"
  if [[ "$VERSION" == "latest" || -z "$VERSION" ]]; then
    url="https://github.com/${REPO}/releases/latest/download/sspanel-linux-${ARCH}"
  else
    url="https://github.com/${REPO}/releases/download/${VERSION}/sspanel-linux-${ARCH}"
  fi
  BINARY_SRC="$(mktemp)"; TMP_CREATED="$BINARY_SRC"
  log "downloading $url"
  if command -v curl >/dev/null; then
    curl -fSL --retry 3 -o "$BINARY_SRC" "$url" \
      || die "download failed: $url (no release asset? use --build or --binary)"
  else
    wget -q --tries=3 -O "$BINARY_SRC" "$url" \
      || die "download failed: $url (no release asset? use --build or --binary)"
  fi
  chmod 0755 "$BINARY_SRC"
}

determine_mode
log "binary source mode: $MODE"
case "$MODE" in
  local)
    [[ -f "$BINARY_SRC" ]] || die "binary not found: $BINARY_SRC"
    if command -v file >/dev/null && ! file "$BINARY_SRC" | grep -qi 'ELF'; then
      warn "the binary does not look like a Linux ELF executable: $BINARY_SRC"
    fi
    log "using local binary: $BINARY_SRC"
    ;;
  build)  build_from_source;;
  download) download_release;;
esac

# ---------------------------------------------------------------------------
# System user and directories
# ---------------------------------------------------------------------------
ensure_user() {
  id "$RUN_USER" >/dev/null 2>&1 && return 0
  log "creating system user $RUN_USER"
  local shell
  shell="$(command -v nologin || echo /usr/sbin/nologin)"
  if command -v useradd >/dev/null; then
    useradd --system --home "$DATA_DIR" --shell "$shell" "$RUN_USER" \
      || useradd -r -d "$DATA_DIR" -s "$shell" "$RUN_USER" \
      || die "failed to create user $RUN_USER"
  elif command -v adduser >/dev/null; then
    adduser -S -D -H -h "$DATA_DIR" -s "$shell" "$RUN_USER" \
      || die "failed to create user $RUN_USER"
  else
    die "neither useradd nor adduser is available"
  fi
}

install_binary() {
  if [[ "$BINARY_SRC" == "$BIN_DIR/sspanel" ]]; then
    log "binary already installed at $BINARY_SRC"
    return
  fi
  log "installing binary to $BIN_DIR/sspanel"
  install -m 0755 "$BINARY_SRC" "$BIN_DIR/sspanel"
}

prepare_dirs() {
  log "preparing directories"
  # 0700/0600 on purpose: the DB holds every PSK and subscription token.
  install -d -m 0700 -o "$RUN_USER" -g "$RUN_USER" "$CONF_DIR" "$DATA_DIR"
}

write_config() {
  if [[ -f "$CONF_DIR/config.yaml" ]]; then
    log "keeping existing $CONF_DIR/config.yaml"
    return
  fi
  log "writing default configuration"
  local trusted_block=""
  if [[ "$PROXY" != "none" ]]; then
    trusted_block=$'  trusted_proxies:\n    - 127.0.0.1\n'
  fi
  cat >"$CONF_DIR/config.yaml" <<EOF
panel:
  listen: 127.0.0.1
  port: ${PANEL_PORT}
  base_path: /
  session_ttl: 24h
${trusted_block}database:
  path: ${DATA_DIR}/sspanel.db

security:
  allow_private_targets: false
  block_metadata: true
  login_rate_limit: 5

traffic:
  flush_interval: 10s
  enforce_interval: 30s
  reset_day: 1
  timezone: Local
  udp_timeout: 60s

log:
  level: info
  format: text
EOF
  chown "$RUN_USER:$RUN_USER" "$CONF_DIR/config.yaml"
  chmod 0600 "$CONF_DIR/config.yaml"
}

write_env_file() {
  local env_file="$CONF_DIR/env"
  local content="" need=0
  [[ -n "$TZ" ]] && { content+="TZ=${TZ}"$'\n'; need=1; }
  [[ -n "${SSPANEL_ADMIN_PASSWORD:-}" ]] && {
    content+="SSPANEL_ADMIN_PASSWORD=${SSPANEL_ADMIN_PASSWORD}"$'\n'; need=1;
  }
  if (( need == 0 )); then return; fi
  if [[ -f "$env_file" ]]; then
    log "keeping existing $env_file"
  else
    log "writing $env_file"
    printf '%s' "$content" >"$env_file"
    chown "$RUN_USER:$RUN_USER" "$env_file"
    chmod 0600 "$env_file"
  fi
  mkdir -p "$DROPIN_DIR"
  cat >"$DROPIN_DIR/10-env.conf" <<EOF
[Service]
EnvironmentFile=${env_file}
EOF
  chmod 0644 "$DROPIN_DIR/10-env.conf"
}

install_service() {
  log "installing systemd unit"
  if [[ -f "$SCRIPT_DIR/sspanel.service" ]]; then
    install -m 0644 "$SCRIPT_DIR/sspanel.service" "$SERVICE_FILE"
  else
    warn "sspanel.service not found next to script; writing built-in unit"
    cat >"$SERVICE_FILE" <<'EOF'
[Unit]
Description=sspanel - Shadowsocks server with web panel
Documentation=https://github.com/ydong/ssrservice
After=network-online.target nss-lookup.target
Wants=network-online.target

[Service]
Type=simple
User=sspanel
Group=sspanel
ExecStart=/usr/local/bin/sspanel run --config /etc/sspanel/config.yaml
Restart=on-failure
RestartSec=3s
LimitNOFILE=1048576
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictRealtime=true
RestrictNamespaces=true
LockPersonality=true
MemoryDenyWriteExecute=true
SystemCallArchitectures=native
ReadWritePaths=/var/lib/sspanel
StateDirectory=sspanel
RuntimeDirectory=sspanel

[Install]
WantedBy=multi-user.target
EOF
    chmod 0644 "$SERVICE_FILE"
  fi
}

# ---------------------------------------------------------------------------
# Optional reverse proxy / firewall
# ---------------------------------------------------------------------------
setup_caddy() {
  if ! command -v caddy >/dev/null; then
    warn "caddy is not installed; skipping automatic proxy setup"
    info "install Caddy, then use a Caddyfile like:"
    info "  ${DOMAIN} { reverse_proxy 127.0.0.1:${PANEL_PORT} }"
    return
  fi
  install -d -m 0755 /etc/caddy
  [[ -f /etc/caddy/Caddyfile ]] && cp -a /etc/caddy/Caddyfile "/etc/caddy/Caddyfile.bak.$(date +%s)"
  local email_block=""
  if [[ -n "$EMAIL" ]]; then
    email_block=$'{\n\temail '"${EMAIL}"$'\n}\n\n'
  fi
  cat >/etc/caddy/Caddyfile <<EOF
${email_block}${DOMAIN} {
	encode gzip

	header {
		Strict-Transport-Security "max-age=31536000; includeSubDomains"
		X-Content-Type-Options "nosniff"
		X-Frame-Options "DENY"
		Referrer-Policy "no-referrer"
		-Server
	}

	reverse_proxy 127.0.0.1:${PANEL_PORT} {
		header_up X-Real-IP {remote_host}
	}
}
EOF
  systemctl enable caddy >/dev/null 2>&1 || true
  systemctl reload caddy 2>/dev/null || systemctl restart caddy
  log "Caddy configured for https://${DOMAIN}"
}

setup_nginx() {
  if ! command -v nginx >/dev/null; then
    warn "nginx is not installed; skipping automatic proxy setup"
    info "install nginx + a TLS certificate, then adapt deploy/nginx.conf"
    return
  fi
  local conf="/etc/nginx/conf.d/sspanel.conf"
  install -d -m 0755 /etc/nginx/conf.d
  [[ -f "$conf" ]] && cp -a "$conf" "$conf.bak.$(date +%s)"
  cat >"$conf" <<EOF
upstream sspanel_panel {
    server 127.0.0.1:${PANEL_PORT};
    keepalive 16;
}

server {
    listen 80;
    server_name ${DOMAIN};
    return 301 https://\$host\$request_uri;
}

server {
    listen 443 ssl;
    http2 on;
    server_name ${DOMAIN};

    ssl_certificate     /etc/letsencrypt/live/${DOMAIN}/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/${DOMAIN}/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;

    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
    add_header X-Content-Type-Options nosniff always;
    add_header X-Frame-Options DENY always;

    client_max_body_size 2m;

    location / {
        proxy_pass http://sspanel_panel;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_read_timeout 60s;
    }
}
EOF
  nginx -t || die "nginx config test failed; fix $conf and reload manually"
  systemctl enable nginx >/dev/null 2>&1 || true
  systemctl reload nginx 2>/dev/null || systemctl restart nginx
  log "nginx configured for https://${DOMAIN} (certificates must exist)"
}

setup_proxy() {
  case "$PROXY" in
    caddy) setup_caddy;;
    nginx) setup_nginx;;
    none)  :;;
  esac
}

setup_firewall() {
  (( ${#INBOUND_PORTS[@]} > 0 )) || return 0
  if [[ "$SKIP_FIREWALL" == 1 ]]; then
    info "skipping firewall changes (--skip-firewall)"
    return 0
  fi
  if command -v ufw >/dev/null; then
    for p in "${INBOUND_PORTS[@]}"; do
      ufw allow "${p}/tcp" >/dev/null && ufw allow "${p}/udp" >/dev/null \
        && log "ufw: opened ${p}/tcp and ${p}/udp"
    done
  elif command -v firewall-cmd >/dev/null; then
    for p in "${INBOUND_PORTS[@]}"; do
      firewall-cmd --permanent --add-port="${p}/tcp" >/dev/null
      firewall-cmd --permanent --add-port="${p}/udp" >/dev/null
      log "firewalld: added ${p}/tcp and ${p}/udp"
    done
    firewall-cmd --reload >/dev/null
  else
    warn "no supported firewall found; open these ports manually (tcp+udp): ${INBOUND_PORTS[*]}"
  fi
}

# ---------------------------------------------------------------------------
# Start & verify
# ---------------------------------------------------------------------------
start_service() {
  log "reloading systemd and starting sspanel"
  systemctl daemon-reload
  systemctl enable sspanel.service >/dev/null
  systemctl restart sspanel.service
  if command -v curl >/dev/null; then
    local ok=0
    for _ in $(seq 1 50); do
      if curl -fsS "http://127.0.0.1:${PANEL_PORT}/healthz" >/dev/null 2>&1; then ok=1; break; fi
      sleep 0.2
    done
    if (( ok == 1 )); then
      log "health check passed"
    else
      warn "service started but health check failed; inspect: journalctl -u sspanel -n 50"
    fi
  else
    warn "curl not found; skipped health check"
  fi
}

print_summary() {
  printf '\n'
  log "deployment complete"
  info "status:   systemctl status sspanel"
  info "logs:     journalctl -u sspanel -f"
  info "health:   curl -fsS http://127.0.0.1:${PANEL_PORT}/healthz"
  info "tunnel:   ssh -L ${PANEL_PORT}:127.0.0.1:${PANEL_PORT} user@$(hostname)"
  if [[ "$PROXY" != "none" && -n "$DOMAIN" ]]; then
    info "panel:    https://${DOMAIN}/"
  fi
  info "admin:    journalctl -u sspanel | grep -A5 'administrator created'  (first login only)"
  if (( ${#INBOUND_PORTS[@]} > 0 )); then
    info "inbound:  opened ports ${INBOUND_PORTS[*]} (tcp+udp) in the firewall"
  fi
}

# ---------------------------------------------------------------------------
# Run
# ---------------------------------------------------------------------------
log "starting sspanel deployment (os=${OS_ID:-unknown}, arch=${ARCH})"
if [[ "$ASSUME_YES" == 0 ]]; then
  confirm "Install sspanel as a systemd service on this host?" || die "aborted by user"
fi
ensure_user
install_binary
prepare_dirs
write_config
write_env_file
install_service
setup_proxy
setup_firewall
start_service
print_summary
