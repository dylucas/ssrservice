#!/usr/bin/env bash
# install.sh - install sspanel as a systemd service on Linux.
#
# Usage:
#   sudo ./install.sh                 # uses ./bin/sspanel
#   sudo ./install.sh /path/to/sspanel
#
# The script is idempotent: it keeps an existing /etc/sspanel/config.yaml and
# database when re-run.
set -euo pipefail

BINARY_SRC="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/bin/sspanel}"
BIN_DIR="/usr/local/bin"
CONF_DIR="/etc/sspanel"
DATA_DIR="/var/lib/sspanel"
SERVICE_FILE="/etc/systemd/system/sspanel.service"
RUN_USER="sspanel"

log() { printf '\033[1;32m[install]\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m[install] %s\033[0m\n' "$*" >&2; exit 1; }

[[ "${EUID}" -eq 0 ]] || die "please run as root (sudo ./install.sh)"
[[ -f "${BINARY_SRC}" ]] || die "binary not found: ${BINARY_SRC} (run make build first)"
command -v systemctl >/dev/null || die "systemd is required"

if ! id "${RUN_USER}" >/dev/null 2>&1; then
  log "creating system user ${RUN_USER}"
  useradd --system --home "${DATA_DIR}" --shell /usr/sbin/nologin "${RUN_USER}"
fi

log "installing binary to ${BIN_DIR}/sspanel"
install -m 0755 "${BINARY_SRC}" "${BIN_DIR}/sspanel"

log "preparing directories"
install -d -m 0750 -o "${RUN_USER}" -g "${RUN_USER}" "${CONF_DIR}" "${DATA_DIR}"

if [[ ! -f "${CONF_DIR}/config.yaml" ]]; then
  log "writing default configuration"
  cat >"${CONF_DIR}/config.yaml" <<EOF
panel:
  listen: 127.0.0.1
  port: 2096
  base_path: /
  session_ttl: 24h

database:
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
  chown "${RUN_USER}:${RUN_USER}" "${CONF_DIR}/config.yaml"
  chmod 0640 "${CONF_DIR}/config.yaml"
else
  log "keeping existing ${CONF_DIR}/config.yaml"
fi

log "installing systemd unit"
install -m 0644 "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/sspanel.service" "${SERVICE_FILE}"

log "reloading systemd"
systemctl daemon-reload
systemctl enable sspanel.service
systemctl restart sspanel.service

log "waiting for the panel to become healthy"
for _ in $(seq 1 50); do
  if curl -fsS "http://127.0.0.1:2096/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

if curl -fsS "http://127.0.0.1:2096/healthz" >/dev/null 2>&1; then
  log "sspanel is running"
else
  log "service started but health check failed; inspect: journalctl -u sspanel -n 50"
fi

if grep -q "password" /dev/null 2>&1; then :; fi
log "initial admin password is printed once in: journalctl -u sspanel | grep -A4 'administrator created'"
log "then open the panel through an SSH tunnel: ssh -L 2096:127.0.0.1:2096 user@this-host"
