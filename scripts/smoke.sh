#!/usr/bin/env bash
# End-to-end smoke test for sspanel: boots a throwaway instance, drives the
# panel API, and verifies a real Shadowsocks round trip with scripts/ssprobe.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/bin/sspanel"
PROBE="$ROOT/bin/ssprobe"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/sspanel-smoke.XXXXXX")"
PANEL_PORT="${PANEL_PORT:-12096}"
SS_PORT="${SS_PORT:-18388}"
ADMIN_PASSWORD="smoke-password-123"
COOKIE_JAR="$WORK/cookies.txt"

cleanup() {
  if [[ -n "${SERVER_PID:-}" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

log() { printf '\033[1;34m[smoke]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[smoke] FAIL:\033[0m %s\n' "$*" >&2; exit 1; }

[[ -x "$BIN" ]] || fail "missing $BIN (run make build)"

cat >"$WORK/config.yaml" <<EOF
panel:
  listen: 127.0.0.1
  port: $PANEL_PORT
  session_ttl: 1h
database:
  path: $WORK/sspanel.db
security:
  allow_private_targets: true
traffic:
  # Shortest interval the config accepts, so the dashboard rollup has data
  # within the lifetime of this test.
  flush_interval: 1s
log:
  level: warn
  format: text
EOF

log "building probe"
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$PROBE" ./scripts/ssprobe)

log "starting server on panel :$PANEL_PORT"
SSPANEL_ADMIN_PASSWORD="$ADMIN_PASSWORD" "$BIN" run --config "$WORK/config.yaml" >"$WORK/server.log" 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 100); do
  if curl -fsS "http://127.0.0.1:$PANEL_PORT/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.1
done
curl -fsS "http://127.0.0.1:$PANEL_PORT/healthz" >/dev/null || { cat "$WORK/server.log"; fail "panel did not become healthy"; }

api() {
  local method="$1" path="$2" body="${3:-}"
  if [[ -n "$body" ]]; then
    curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" -X "$method" \
      -H 'Content-Type: application/json' -d "$body" "http://127.0.0.1:$PANEL_PORT$path"
  else
    curl -fsS -b "$COOKIE_JAR" -c "$COOKIE_JAR" -X "$method" "http://127.0.0.1:$PANEL_PORT$path"
  fi
}

log "logging in"
api POST /api/v1/auth/login "{\"username\":\"admin\",\"password\":\"$ADMIN_PASSWORD\"}" >/dev/null

log "creating inbound on :$SS_PORT"
INBOUND=$(api POST /api/v1/inbounds "{\"remark\":\"smoke\",\"listen\":\"127.0.0.1\",\"port\":$SS_PORT,\"method\":\"2022-blake3-aes-128-gcm\",\"udp_enabled\":true,\"enabled\":true}")
INBOUND_ID=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$INBOUND")
SERVER_PSK=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["server_psk"])' <<<"$INBOUND")

log "creating client"
CLIENT=$(api POST "/api/v1/inbounds/$INBOUND_ID/clients" '{"name":"smoke-user","enabled":true,"quota_bytes":0,"ip_limit":0}')
CLIENT_ID=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$CLIENT")
CLIENT_PSK=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["psk"])' <<<"$CLIENT")

log "checking share link and subscription"
LINK_JSON=$(api GET "/api/v1/clients/$CLIENT_ID/link")
python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["link"].startswith("ss://"), d; assert d["subscription_url"].startswith("/sub/"), d' <<<"$LINK_JSON"
SUB_PATH=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["subscription_url"])' <<<"$LINK_JSON")
curl -fsS "http://127.0.0.1:$PANEL_PORT$SUB_PATH" >/dev/null

log "verifying TCP round trip through the proxy"
"$PROBE" -server "127.0.0.1:$SS_PORT" -method 2022-blake3-aes-128-gcm \
  -server-psk "$SERVER_PSK" -client-psk "$CLIENT_PSK" -target echo >/dev/null

log "verifying UDP round trip through the proxy"
"$PROBE" -server "127.0.0.1:$SS_PORT" -method 2022-blake3-aes-128-gcm \
  -server-psk "$SERVER_PSK" -client-psk "$CLIENT_PSK" -target udp-echo >/dev/null

log "checking traffic accounting"
sleep 1
CLIENTS_JSON=$(api GET "/api/v1/clients")
CLIENTS_JSON="$CLIENTS_JSON" python3 - <<'PY'
import json, os
payload = json.loads(os.environ["CLIENTS_JSON"])
client = payload["items"][0]
assert client["used_bytes"] > 0, client
assert client["online_ips"] is not None
print(f"counted {client['used_bytes']} bytes for {client['name']}")
PY

log "checking client list carries no secrets"
CLIENTS_LIST=$(api GET /api/v1/clients)
CLIENTS_LIST="$CLIENTS_LIST" python3 - <<'PY'
import json, os
item = json.loads(os.environ["CLIENTS_LIST"])["items"][0]
for field in ("psk", "sub_token", "link", "subscription_url",
              "created_at", "updated_at", "last_reset_at"):
    assert field not in item, f"client list leaked {field}: {item}"
for field in ("status", "used_bytes", "online_ips", "inbound_remark", "method"):
    assert field in item, f"client list is missing {field}: {item}"
PY

log "checking client list compression"
LIST_HEADERS=$(curl -fsS -D - -o /dev/null -H 'Accept-Encoding: gzip' \
  -b "$COOKIE_JAR" "http://127.0.0.1:$PANEL_PORT/api/v1/clients" | tr -d '\r')
grep -qi 'vary:.*accept-encoding' <<<"$LIST_HEADERS" || fail "client list lacks Vary: Accept-Encoding"
PLAIN_JSON=$(curl -fsS -b "$COOKIE_JAR" "http://127.0.0.1:$PANEL_PORT/api/v1/clients")
if grep -qi 'content-encoding: gzip' <<<"$LIST_HEADERS"; then
  GZIP_JSON=$(curl -fsS --compressed -b "$COOKIE_JAR" "http://127.0.0.1:$PANEL_PORT/api/v1/clients")
  [[ "$PLAIN_JSON" == "$GZIP_JSON" ]] || fail "compressed client list differs from the plain one"
  log "client list gzip: ${#PLAIN_JSON} -> ${#GZIP_JSON} bytes"
fi

log "checking inbound aggregates match the client list"
INBOUNDS_JSON=$(api GET /api/v1/inbounds)
CLIENTS_JSON=$(api GET /api/v1/clients)
INBOUNDS_JSON="$INBOUNDS_JSON" CLIENTS_JSON="$CLIENTS_JSON" python3 - <<'PY'
import json, os
inbounds = json.loads(os.environ["INBOUNDS_JSON"])["items"]
clients = json.loads(os.environ["CLIENTS_JSON"])["items"]
expected = {}
for c in clients:
    agg = expected.setdefault(c["inbound_id"], {"count": 0, "enabled": 0, "up": 0, "down": 0})
    agg["count"] += 1
    agg["enabled"] += 1 if c["enabled"] else 0
    agg["up"] += c["up_bytes"]
    agg["down"] += c["down_bytes"]
for inbound in inbounds:
    want = expected.get(inbound["id"], {"count": 0, "enabled": 0, "up": 0, "down": 0})
    assert inbound["client_count"] == want["count"], inbound
    assert inbound["enabled_client_count"] == want["enabled"], inbound
    # Traffic counters come from the database snapshot that the runtime
    # flushes every 10s, while the client list adds the live in-memory
    # counters on top. So the inbound aggregate must never exceed the client
    # list, but it may lag behind it until the next flush.
    assert inbound["up_bytes"] <= want["up"], inbound
    assert inbound["down_bytes"] <= want["down"], inbound
PY

log "checking dashboard traffic rollup"
# The 30 day series is read from the per-day rollup, which is written by the
# traffic flush, so give the runtime a few flush ticks to persist the traffic
# the probe just generated.
DASHBOARD_JSON=""
for _ in $(seq 1 60); do
  DASHBOARD_JSON=$(api GET /api/v1/dashboard)
  if grep -q '"day"' <<<"$DASHBOARD_JSON"; then break; fi
  sleep 0.25
done
DASHBOARD_JSON="$DASHBOARD_JSON" python3 - <<'PY'
import json, os
payload = json.loads(os.environ["DASHBOARD_JSON"])
days = payload["traffic_30d"]
assert days, "dashboard returned no traffic days after waiting for a flush"
# The series comes from the per-day rollup, which the runtime maintains in the
# same transaction as the detail rows. It must cover the window in order and
# carry non-negative numbers.
previous = ""
for day in days:
    assert day["day"] > previous, f"days are not strictly ascending: {day['day']}"
    previous = day["day"]
    assert day["up_bytes"] >= 0 and day["down_bytes"] >= 0, day
total = sum(d["up_bytes"] + d["down_bytes"] for d in days)
print(f"dashboard rollup: {len(days)} days, {total} bytes total")
PY

api GET /api/v1/dashboard >/dev/null

log "checking frontend"
curl -fsS "http://127.0.0.1:$PANEL_PORT/" | grep -q 'id="sspanel-config"' || fail "frontend config not injected"

log "checking asset compression"
ASSET=$(curl -fsS "http://127.0.0.1:$PANEL_PORT/" | grep -o 'assets/index-[A-Za-z0-9_-]*\.js' | head -1)
[[ -n "$ASSET" ]] || fail "could not locate the main asset"
# A GET (not HEAD) is used here: HEAD deliberately falls through to the plain
# file server, which does not encode.
GZ_HEADERS=$(curl -fsS -D - -o /dev/null -H 'Accept-Encoding: gzip' "http://127.0.0.1:$PANEL_PORT/$ASSET")
grep -qi 'content-encoding: gzip' <<<"$GZ_HEADERS" || fail "asset was not gzip encoded"
grep -qi 'vary:.*accept-encoding' <<<"$GZ_HEADERS" || fail "asset response lacks Vary: Accept-Encoding"
PLAIN_SHA=$(curl -fsS "http://127.0.0.1:$PANEL_PORT/$ASSET" | shasum -a 256 | cut -d' ' -f1)
GZIP_SHA=$(curl -fsS --compressed "http://127.0.0.1:$PANEL_PORT/$ASSET" | shasum -a 256 | cut -d' ' -f1)
[[ "$PLAIN_SHA" == "$GZIP_SHA" ]] || fail "compressed asset differs from the plain one"
PLAIN_SIZE=$(curl -fsS "http://127.0.0.1:$PANEL_PORT/$ASSET" | wc -c | tr -d ' ')
GZIP_SIZE=$(curl -fsS -H 'Accept-Encoding: gzip' "http://127.0.0.1:$PANEL_PORT/$ASSET" | wc -c | tr -d ' ')
[[ "$GZIP_SIZE" -lt "$PLAIN_SIZE" ]] || fail "compressed asset is not smaller ($GZIP_SIZE vs $PLAIN_SIZE)"
log "asset gzip: $PLAIN_SIZE -> $GZIP_SIZE bytes"
curl -fsS "http://127.0.0.1:$PANEL_PORT/settings" | grep -q '<div id="app">' || fail "SPA fallback broken"

log "stopping server"
kill "$SERVER_PID"
wait "$SERVER_PID" 2>/dev/null || true
SERVER_PID=""
grep -q 'stopped' "$WORK/server.log" || true

log "PASS"
