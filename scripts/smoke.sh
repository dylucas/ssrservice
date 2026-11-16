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
# The subscription token is only returned by the detail endpoint, and the
# subscription cache-header check below needs the real URL.
CLIENT_SUB_TOKEN=$(api GET "/api/v1/clients/$CLIENT_ID" | python3 -c 'import json,sys; print(json.load(sys.stdin)["sub_token"])')

log "checking inbound domain in share links"
# The inbound created above now carries a domain, so every link the panel renders
# must use that host and port instead of the listen address.
DOMAIN_INBOUND=$(api POST /api/v1/inbounds "{\"remark\":\"smoke-domain\",\"listen\":\"127.0.0.1\",\"port\":$((SS_PORT + 2)),\"method\":\"2022-blake3-aes-128-gcm\",\"domain\":\"Smoke.Example.COM.\",\"domain_port\":8443,\"udp_enabled\":false,\"enabled\":true}")
DOMAIN_INBOUND_ID=$(python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["domain"]=="smoke.example.com", d; print(d["id"])' <<<"$DOMAIN_INBOUND")
DOMAIN_CLIENT=$(api POST "/api/v1/inbounds/$DOMAIN_INBOUND_ID/clients" '{"name":"domain-user","enabled":true}')
DOMAIN_CLIENT_ID=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$DOMAIN_CLIENT")
DOMAIN_LINK=$(api GET "/api/v1/clients/$DOMAIN_CLIENT_ID/link")
DOMAIN_LINK="$DOMAIN_LINK" python3 - <<'PY'
import json, os
payload = json.loads(os.environ["DOMAIN_LINK"])
assert "smoke.example.com:8443" in payload["link"], payload["link"]
assert payload["subscription_url"].startswith("/sub/"), payload
PY
DOMAIN_SUB_PATH=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["subscription_url"])' <<<"$DOMAIN_LINK")
DOMAIN_SUB=$(curl -fsS "http://127.0.0.1:$PANEL_PORT$DOMAIN_SUB_PATH" | base64 -d)
[[ "$DOMAIN_SUB" == *"smoke.example.com:8443"* ]] || fail "subscription does not use the inbound domain"
DOMAIN_CLASH=$(curl -fsS "http://127.0.0.1:$PANEL_PORT$DOMAIN_SUB_PATH?format=clash")
[[ "$DOMAIN_CLASH" == *"smoke.example.com"* ]] || fail "clash subscription does not use the inbound domain"

# Remove the domain fixtures again: later checks assert on the exact client set.
api DELETE "/api/v1/inbounds/$DOMAIN_INBOUND_ID" >/dev/null

log "checking invalid domains are rejected"
BAD_STATUS=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" -X POST \
  -H 'Content-Type: application/json' \
  -d "{\"remark\":\"bad\",\"listen\":\"127.0.0.1\",\"port\":$((SS_PORT + 3)),\"method\":\"2022-blake3-aes-128-gcm\",\"domain\":\"http://bad.example.com\",\"enabled\":true}" \
  "http://127.0.0.1:$PANEL_PORT/api/v1/inbounds")
[[ "$BAD_STATUS" == "400" ]] || fail "invalid domain accepted (status $BAD_STATUS)"

log "checking share link and subscription"
LINK_JSON=$(api GET "/api/v1/clients/$CLIENT_ID/link")
python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["link"].startswith("ss://"), d; assert d["subscription_url"].startswith("/sub/"), d' <<<"$LINK_JSON"
SUB_PATH=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["subscription_url"])' <<<"$LINK_JSON")
curl -fsS "http://127.0.0.1:$PANEL_PORT$SUB_PATH" >/dev/null

# The subscription carries the account figures clients display themselves.
SUB_HEADERS=$(curl -s -D - -o /dev/null "http://127.0.0.1:$PANEL_PORT$SUB_PATH" | tr -d '\r')
grep -qi '^Subscription-Userinfo: upload=.*; download=.*; total=.*; expire=' <<<"$SUB_HEADERS" \
  || fail "subscription has no Subscription-Userinfo header"
grep -qi '^Profile-Title: base64:' <<<"$SUB_HEADERS" || fail "subscription has no Profile-Title header"
# sing-box is a third format the server has to serve as valid JSON.
SINGBOX=$(curl -fsS "http://127.0.0.1:$PANEL_PORT$SUB_PATH?format=singbox")
SINGBOX="$SINGBOX" python3 - <<'PY'
import json, os
profile = json.loads(os.environ["SINGBOX"])
out = profile.get("outbounds")
assert isinstance(out, list) and len(out) == 1, profile
o = out[0]
for key in ("type", "tag", "server", "server_port", "method", "password"):
    assert key in o, (key, o)
assert o["type"] == "shadowsocks", o
assert o["tag"], "sing-box outbound tag must not be empty"
PY

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
# The client list is paged, so ask for the maximum page: the aggregate check has
# to see every user, and silently comparing against one default page would make
# the assertions pass for the wrong reason.
CLIENTS_JSON=$(api GET "/api/v1/clients?limit=500")
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

log "checking access analytics"
# Recording is off by default; the summary still answers and says so.
SUMMARY_DISABLED=$(api GET /api/v1/access/summary)
SUMMARY_DISABLED="$SUMMARY_DISABLED" python3 - <<'PY'
import json, os
payload = json.loads(os.environ["SUMMARY_DISABLED"])
assert payload["enabled"] is False, payload
assert payload["raw_retention_days"] == 7, payload
assert payload["aggregate_retention_days"] == 90, payload
PY

# Turn it on through the API exactly as the settings page does.
SETTINGS_JSON=$(api GET /api/v1/settings)
SETTINGS_ON=$(SETTINGS_JSON="$SETTINGS_JSON" python3 - <<'PY'
import json, os
payload = json.loads(os.environ["SETTINGS_JSON"])
payload["access_log_enabled"] = True
print(json.dumps(payload))
PY
)
api PUT /api/v1/settings "$SETTINGS_ON" >/dev/null

summary_enabled() {
  api GET /api/v1/access/summary | python3 -c 'import json,sys; print(json.load(sys.stdin)["enabled"])'
}
[[ "$(summary_enabled)" == "True" ]] || fail "enabling access analytics was not applied"

# Generate one recorded TCP and one recorded UDP access.
"$PROBE" -server "127.0.0.1:$SS_PORT" -method 2022-blake3-aes-128-gcm \
  -server-psk "$SERVER_PSK" -client-psk "$CLIENT_PSK" -target echo >/dev/null
"$PROBE" -server "127.0.0.1:$SS_PORT" -method 2022-blake3-aes-128-gcm \
  -server-psk "$SERVER_PSK" -client-psk "$CLIENT_PSK" -target udp-echo >/dev/null

ACCESS_JSON=""
for _ in $(seq 1 80); do
  ACCESS_JSON=$(api GET /api/v1/access/summary)
  if grep -q '"total_hits": *[1-9]' <<<"$ACCESS_JSON"; then break; fi
  sleep 0.25
done
ACCESS_JSON="$ACCESS_JSON" python3 - <<'PY'
import json, os
payload = json.loads(os.environ["ACCESS_JSON"])
assert payload["enabled"] is True, payload
assert payload["total_hits"] > 0, "no access record after a recorded round trip"
assert payload["allowed_hits"] > 0, payload
assert payload["unique_hosts"] > 0, payload
assert payload["top_hosts"], payload
assert payload["top_clients"], payload
assert payload["top_clients"][0]["name"] == "smoke-user", payload
# The trend has one point per day of the window, so a quiet day cannot be
# compressed out of the chart.
assert len(payload["daily"]) == 7, payload["daily"]
PY

# The day form of the window is what the date picker sends, and it is resolved
# server-side in the configured zone.
TODAY=$(date +%Y-%m-%d)
DAY_TOTAL=$(api GET "/api/v1/access/events?since_day=$TODAY&until_day=$TODAY" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["total"])')
[[ "$DAY_TOTAL" -gt 0 ]] || fail "the picked day returned no access records"
for bad in "since_day=yesterday" "since_day=2026-13-01" \
  "since_day=$TODAY&until_day=2000-01-01"; do
  status=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" \
    "http://127.0.0.1:$PANEL_PORT/api/v1/access/events?$bad")
  [[ "$status" == "400" ]] || fail "access events $bad was accepted (status $status)"
done

# A UDP destination is flushed once its burst is quiet, so wait for both
# transports rather than for the first record (which is the TCP one).
ACCESS_EVENTS=""
for _ in $(seq 1 80); do
  ACCESS_EVENTS=$(api GET "/api/v1/access/events?limit=20")
  if ACCESS_EVENTS="$ACCESS_EVENTS" python3 - <<'PY'
import json, os, sys
items = json.loads(os.environ["ACCESS_EVENTS"]).get("items", [])
sys.exit(0 if {"tcp", "udp"} <= {item["network"] for item in items} else 1)
PY
  then break; fi
  sleep 0.25
done
ACCESS_EVENTS="$ACCESS_EVENTS" CLIENT_PSK="$CLIENT_PSK" SERVER_PSK="$SERVER_PSK" python3 - <<'PY'
import json, os
payload = json.loads(os.environ["ACCESS_EVENTS"])
items = payload["items"]
assert items, "access event list is empty"
assert any(item["host"] == "127.0.0.1" for item in items), items
assert any(item["outcome"] == "allowed" for item in items), items
assert any(item["network"] == "tcp" for item in items), "TCP access was not recorded"
assert any(item["network"] == "udp" for item in items), "UDP access was not recorded"
body = json.dumps(payload)
for name in ("CLIENT_PSK", "SERVER_PSK"):
    secret = os.environ.get(name, "")
    assert secret and secret not in body, f"access events leaked {name}"
PY

# The user filter narrows the event list.
ACCESS_BY_CLIENT=$(api GET "/api/v1/access/events?client_id=$CLIENT_ID")
ACCESS_BY_CLIENT="$ACCESS_BY_CLIENT" python3 - <<'PY'
import json, os
payload = json.loads(os.environ["ACCESS_BY_CLIENT"])
assert payload["total"] > 0, payload
assert all(item["client_id"] for item in payload["items"]), payload
PY

# Bad parameters are rejected instead of being silently clamped.
for bad in "summary?days=0" "summary?days=91" "events?outcome=weird" \
  "events?network=icmp" "events?limit=0" "events?offset=-1"; do
  status=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" \
    "http://127.0.0.1:$PANEL_PORT/api/v1/access/$bad")
  [[ "$status" == "400" ]] || fail "access $bad was accepted (status $status)"
done

# Turning it off stops collection but keeps the history readable.
SETTINGS_OFF=$(SETTINGS_ON="$SETTINGS_ON" python3 - <<'PY'
import json, os
payload = json.loads(os.environ["SETTINGS_ON"])
payload["access_log_enabled"] = False
print(json.dumps(payload))
PY
)
api PUT /api/v1/settings "$SETTINGS_OFF" >/dev/null
[[ "$(summary_enabled)" == "False" ]] || fail "disabling access analytics was not applied"
HISTORY_TOTAL=$(api GET /api/v1/access/events | python3 -c 'import json,sys; print(json.load(sys.stdin)["total"])')
[[ "$HISTORY_TOTAL" -gt 0 ]] || fail "history disappeared after disabling access analytics"

log "checking stricter request validation"
# PUT /settings replaces the whole object: a partial body must be rejected, not
# silently zeroed (an empty timezone would move accounting to UTC and switch
# subscriptions off).
status=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" \
  -X PUT -H 'Content-Type: application/json' \
  -d '{"access_log_enabled":true}' "http://127.0.0.1:$PANEL_PORT/api/v1/settings")
[[ "$status" == "400" ]] || fail "settings accepted a partial body (status $status)"

# A toggle that does not say what the new state is must not be read as "off".
status=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" \
  -X POST -H 'Content-Type: application/json' -d '{}' \
  "http://127.0.0.1:$PANEL_PORT/api/v1/clients/$CLIENT_ID/toggle")
[[ "$status" == "400" ]] || fail "toggle without enabled was accepted (status $status)"
CLIENT_STILL=$(api GET "/api/v1/clients/$CLIENT_ID" | python3 -c 'import json,sys; print(json.load(sys.stdin)["enabled"])')
[[ "$CLIENT_STILL" == "True" ]] || fail "a rejected toggle still changed the client"

# A malformed export filter must not degrade into "export every PSK".
for bad in "inbound_id=abc" "inbound_id=-1"; do
  status=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" \
    "http://127.0.0.1:$PANEL_PORT/api/v1/export/clients?$bad")
  [[ "$status" == "400" ]] || fail "export $bad was accepted (status $status)"
done

# The audit list rejects bad paging like every other list endpoint.
for bad in "limit=abc" "limit=0" "offset=-1" "since=abc"; do
  status=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" \
    "http://127.0.0.1:$PANEL_PORT/api/v1/audit?$bad")
  [[ "$status" == "400" ]] || fail "audit $bad was accepted (status $status)"
done

# The subscription body carries a PSK, so no cache may keep it.
SUB_HEADERS=$(curl -s -D - -o /dev/null "http://127.0.0.1:$PANEL_PORT/sub/$CLIENT_SUB_TOKEN" | tr -d '\r')
grep -qi '^Cache-Control: no-store' <<<"$SUB_HEADERS" || fail "subscription is cacheable"

log "checking audit log"
AUDIT_JSON=$(api GET /api/v1/audit?limit=50)
AUDIT_JSON="$AUDIT_JSON" CLIENT_PSK="$CLIENT_PSK" SERVER_PSK="$SERVER_PSK" python3 - <<'PY'
import json, os
payload = json.loads(os.environ["AUDIT_JSON"])
items = payload["items"]
assert items, "audit log is empty after mutations"
actions = {item["action"] for item in items}
for expected in ("auth.login", "inbound.create", "client.create"):
    assert expected in actions, f"missing audit action {expected}: {sorted(actions)}"
body = json.dumps(payload)
for name in ("CLIENT_PSK", "SERVER_PSK"):
    secret = os.environ.get(name, "")
    assert secret and secret not in body, f"audit log leaked {name}"
print(f"audit rows: {payload['total']}, actions: {len(actions)}")
PY

# TOTP code for a step offset, computed with the same RFC 6238 parameters the
# server uses (SHA-1, 6 digits, 30 seconds). Taking the offset as an argument
# lets the script use the next step right after enrollment without sleeping: the
# confirming code is consumed at enrollment, so a login must present a newer one.
totp_code() {
  python3 - "$1" "$2" <<'PY'
import base64, hashlib, hmac, struct, sys, time
secret, offset = sys.argv[1], int(sys.argv[2])
key = base64.b32decode(secret + "=" * (-len(secret) % 8), casefold=True)
counter = int(time.time()) // 30 + offset
digest = hmac.new(key, struct.pack(">Q", counter), hashlib.sha1).digest()
start = digest[-1] & 0x0F
value = struct.unpack(">I", digest[start:start + 4])[0] & 0x7FFFFFFF
print(f"{value % 1000000:06d}")
PY
}

log "checking two-step verification"
# Enable it through the API, exactly as the settings dialog does.
TOTP_SETUP=$(api POST /api/v1/auth/totp/setup "{\"password\":\"$ADMIN_PASSWORD\"}")
TOTP_SECRET=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["secret"])' <<<"$TOTP_SETUP")
TOTP_URI=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["otpauth_url"])' <<<"$TOTP_SETUP")
[[ "$TOTP_URI" == otpauth://totp/* ]] || fail "setup did not return an otpauth URI"
TOTP_ENABLE=$(api POST /api/v1/auth/totp/enable "{\"code\":\"$(totp_code "$TOTP_SECRET" 0)\"}")
TOTP_ENABLED=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["totp_enabled"])' <<<"$TOTP_ENABLE")
[[ "$TOTP_ENABLED" == "True" ]] || fail "enabling two-step verification did not report success"
TOTP_RECOVERY=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["recovery_codes"][0])' <<<"$TOTP_ENABLE")
TOTP_RECOVERY_2=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["recovery_codes"][1])' <<<"$TOTP_ENABLE")
[[ -n "$TOTP_RECOVERY" && -n "$TOTP_RECOVERY_2" ]] || fail "enable returned too few recovery codes"

# A fresh client: the password step must answer with a challenge and no cookie.
TOTP_JAR="$WORK/totp-cookies.txt"
rm -f "$TOTP_JAR"
TOTP_LOGIN=$(curl -fsS -c "$TOTP_JAR" -X POST -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASSWORD\"}" \
  "http://127.0.0.1:$PANEL_PORT/api/v1/auth/login")
TOTP_CHALLENGE=$(TOTP_LOGIN="$TOTP_LOGIN" python3 - <<'PY'
import json, os
payload = json.loads(os.environ["TOTP_LOGIN"])
assert payload.get("totp_required") is True, payload
assert "admin" not in payload, payload
assert payload.get("challenge"), payload
print(payload["challenge"])
PY
)
grep -q 'sspanel_session' "$TOTP_JAR" 2>/dev/null && fail "the password step issued a session cookie"
[[ -n "$TOTP_CHALLENGE" ]] || fail "the password step returned no challenge"

# The wrong code is rejected and the right one completes the login.
TOTP_BAD=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d "{\"challenge\":\"$TOTP_CHALLENGE\",\"code\":\"000000\"}" \
  "http://127.0.0.1:$PANEL_PORT/api/v1/auth/login/totp")
[[ "$TOTP_BAD" == "401" ]] || fail "a wrong verification code returned $TOTP_BAD, want 401"
# The code from the next step: the enrollment consumed the current one.
TOTP_STEP_CODE=$(totp_code "$TOTP_SECRET" 1)
curl -fsS -c "$TOTP_JAR" -X POST -H 'Content-Type: application/json' \
  -d "{\"challenge\":\"$TOTP_CHALLENGE\",\"code\":\"$TOTP_STEP_CODE\"}" \
  "http://127.0.0.1:$PANEL_PORT/api/v1/auth/login/totp" >/dev/null
curl -fsS -b "$TOTP_JAR" "http://127.0.0.1:$PANEL_PORT/api/v1/auth/me" \
  | grep -q '"totp_enabled":true' || fail "the session from the second step does not report totp_enabled"

# Replaying the same code with a fresh challenge must fail: it is single use.
TOTP_REPLAY_CHALLENGE=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASSWORD\"}" \
  "http://127.0.0.1:$PANEL_PORT/api/v1/auth/login" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["challenge"])')
TOTP_REPLAY=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d "{\"challenge\":\"$TOTP_REPLAY_CHALLENGE\",\"code\":\"$TOTP_STEP_CODE\"}" \
  "http://127.0.0.1:$PANEL_PORT/api/v1/auth/login/totp")
[[ "$TOTP_REPLAY" == "401" ]] || fail "a replayed code returned $TOTP_REPLAY, want 401"

# A recovery code logs in exactly once.
TOTP_RECOVERY_CHALLENGE=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASSWORD\"}" \
  "http://127.0.0.1:$PANEL_PORT/api/v1/auth/login" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["challenge"])')
curl -fsS -X POST -H 'Content-Type: application/json' \
  -d "{\"challenge\":\"$TOTP_RECOVERY_CHALLENGE\",\"code\":\"$TOTP_RECOVERY\"}" \
  "http://127.0.0.1:$PANEL_PORT/api/v1/auth/login/totp" >/dev/null || fail "a recovery code did not complete the login"
TOTP_RECOVERY_REPLAY_CHALLENGE=$(curl -fsS -X POST -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASSWORD\"}" \
  "http://127.0.0.1:$PANEL_PORT/api/v1/auth/login" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["challenge"])')
TOTP_RECOVERY_REPLAY=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d "{\"challenge\":\"$TOTP_RECOVERY_REPLAY_CHALLENGE\",\"code\":\"$TOTP_RECOVERY\"}" \
  "http://127.0.0.1:$PANEL_PORT/api/v1/auth/login/totp")
[[ "$TOTP_RECOVERY_REPLAY" == "401" ]] || fail "a replayed recovery code returned $TOTP_RECOVERY_REPLAY, want 401"

# The audit trail names the actions and carries none of the secrets.
TOTP_AUDIT=$(api GET "/api/v1/audit?limit=50")
TOTP_AUDIT="$TOTP_AUDIT" TOTP_SECRET="$TOTP_SECRET" TOTP_RECOVERY="$TOTP_RECOVERY" \
  TOTP_RECOVERY_2="$TOTP_RECOVERY_2" TOTP_STEP_CODE="$TOTP_STEP_CODE" python3 - <<'PY'
import json, os
payload = json.loads(os.environ["TOTP_AUDIT"])
actions = {item["action"] for item in payload["items"]}
assert "admin.totp.enable" in actions, sorted(actions)
body = json.dumps(payload)
for name in ("TOTP_SECRET", "TOTP_RECOVERY", "TOTP_RECOVERY_2", "TOTP_STEP_CODE"):
    value = os.environ.get(name, "")
    assert value and value not in body, f"the audit log leaked {name}"
PY

# Turn it back off through the API, then the login is single step again. The
# second factor here is a recovery code, which also proves the disable path
# accepts either form; a TOTP code would need waiting for the next time step.
TOTP_DISABLE=$(curl -fsS -b "$TOTP_JAR" -X POST -H 'Content-Type: application/json' \
  -d "{\"password\":\"$ADMIN_PASSWORD\",\"code\":\"$TOTP_RECOVERY_2\"}" \
  "http://127.0.0.1:$PANEL_PORT/api/v1/auth/totp/disable")
grep -q '"totp_enabled":false' <<<"$TOTP_DISABLE" || fail "disabling two-step verification did not report success"
api POST /api/v1/auth/login "{\"username\":\"admin\",\"password\":\"$ADMIN_PASSWORD\"}" >/dev/null
log "two-step verification: challenge, code, replay and recovery code all behave"

log "checking re-authentication throttling"
# The password confirmation on sensitive endpoints is a place to guess the
# password, so it has a failure budget instead of being unlimited.
for _ in 1 2 3 4 5; do
  curl -s -o /dev/null -b "$COOKIE_JAR" -X PUT \
    -H 'Content-Type: application/json' \
    -d '{"current_password":"wrong-password","new_password":"another-password-456"}' \
    "http://127.0.0.1:$PANEL_PORT/api/v1/auth/password"
done
status=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" -X PUT \
  -H 'Content-Type: application/json' \
  -d "{\"current_password\":\"$ADMIN_PASSWORD\",\"new_password\":\"another-password-456\"}" \
  "http://127.0.0.1:$PANEL_PORT/api/v1/auth/password")
[[ "$status" == "429" ]] || fail "password re-authentication was not throttled (status $status)"
REAUTH_AUDIT=$(api GET "/api/v1/audit?action=admin.reauth.failed&limit=50" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["total"])')
[[ "$REAUTH_AUDIT" -eq 5 ]] || fail "failed re-authentications were not audited (total $REAUTH_AUDIT)"


log "checking client export"
api GET /api/v1/export/clients >"$WORK/clients.csv"
head -1 "$WORK/clients.csv" | grep -q '^inbound_id,inbound_remark,name,psk' || fail "unexpected export header"
EXPORT_ROWS=$(($(wc -l <"$WORK/clients.csv") - 1))
[[ "$EXPORT_ROWS" -eq 1 ]] || fail "exported $EXPORT_ROWS rows, want 1"

log "checking client import round trip"
IMPORT_PORT=$((SS_PORT + 1))
IMPORT_RESPONSE=$(api POST /api/v1/inbounds "{\"remark\":\"smoke-import\",\"listen\":\"127.0.0.1\",\"port\":$IMPORT_PORT,\"method\":\"2022-blake3-aes-128-gcm\",\"udp_enabled\":false,\"enabled\":true}")
IMPORT_INBOUND_ID=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$IMPORT_RESPONSE")
CSV_JSON=$(python3 -c 'import json,sys; print(json.dumps({"format":"csv","content":open(sys.argv[1]).read()}))' "$WORK/clients.csv")
IMPORT_RESULT=$(api POST "/api/v1/inbounds/$IMPORT_INBOUND_ID/clients/import" "$CSV_JSON")
IMPORT_RESULT="$IMPORT_RESULT" python3 - <<'PY'
import json, os
result = json.loads(os.environ["IMPORT_RESULT"])
assert result["created"] == 1, result
assert not result["failed"], result
PY
IMPORTED=$(api GET "/api/v1/inbounds/$IMPORT_INBOUND_ID/clients")
IMPORTED="$IMPORTED" python3 - <<'PY'
import json, os
items = json.loads(os.environ["IMPORTED"])["items"]
assert len(items) == 1, items
assert items[0]["name"] == "smoke-user", items
PY

log "checking client list paging and filters"
# Five more users on the import inbound, so paging has more rows than one page.
PAGING_JSON=$(python3 -c 'import json; print(json.dumps({"format":"csv","content":"name,psk\npage-a,\npage-b,\npage-c,\npage-d,\n"}))')
api POST "/api/v1/inbounds/$IMPORT_INBOUND_ID/clients/import" "$PAGING_JSON" >/dev/null
PAGING_BASE="/api/v1/inbounds/$IMPORT_INBOUND_ID/clients"
PAGE1=$(api GET "$PAGING_BASE?limit=2")
PAGE2=$(api GET "$PAGING_BASE?limit=2&offset=2")
PAGE1="$PAGE1" PAGE2="$PAGE2" python3 - <<'PY'
import json, os
first = json.loads(os.environ["PAGE1"])
second = json.loads(os.environ["PAGE2"])
assert first["total"] == 5, first
assert first["limit"] == 2 and first["offset"] == 0, first
assert len(first["items"]) == 2, first
assert second["total"] == 5 and len(second["items"]) == 2, second
ids = [item["id"] for item in first["items"] + second["items"]]
assert len(set(ids)) == 4, f"pages overlap: {ids}"
assert ids == sorted(ids), f"pages are not in id order: {ids}"
PY
# The default page is bounded, so a caller that forgets the parameters cannot
# pull the whole table.
DEFAULT_LIMIT=$(api GET "$PAGING_BASE" | python3 -c 'import json,sys; print(json.load(sys.stdin)["limit"])')
[[ "$DEFAULT_LIMIT" == "100" ]] || fail "default page limit is $DEFAULT_LIMIT, want 100"
# A filtered page reports the filtered total, and its rows really match.
FILTERED=$(api GET "$PAGING_BASE?status=disabled")
FILTERED="$FILTERED" python3 - <<'PY'
import json, os
payload = json.loads(os.environ["FILTERED"])
assert payload["total"] == 0 and not payload["items"], payload
PY
SEARCHED=$(api GET "$PAGING_BASE?q=page-c")
SEARCHED="$SEARCHED" python3 - <<'PY'
import json, os
payload = json.loads(os.environ["SEARCHED"])
assert payload["total"] == 1, payload
assert payload["items"][0]["name"] == "page-c", payload
PY
for bad in "limit=0" "limit=501" "limit=abc" "offset=-1" "status=bogus"; do
  STATUS=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" \
    "http://127.0.0.1:$PANEL_PORT$PAGING_BASE?$bad")
  [[ "$STATUS" == "400" ]] || fail "invalid paging parameter $bad returned $STATUS, want 400"
done
# inbound_id is only a query parameter on the top-level endpoint: the
# per-inbound route takes it from the path and ignores the query parameter.
STATUS=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" \
  "http://127.0.0.1:$PANEL_PORT/api/v1/clients?inbound_id=abc")
[[ "$STATUS" == "400" ]] || fail "invalid inbound_id returned $STATUS, want 400"
STATUS=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" \
  "http://127.0.0.1:$PANEL_PORT/api/v1/clients?offset=999999999")
[[ "$STATUS" == "400" ]] || fail "out-of-range offset returned $STATUS, want 400"
log "client list paging: 5 users, 2 per page, filters and 400s verified"

log "checking batch operations and attention"
BATCH_IDS=$(python3 -c 'import json,sys; print(json.dumps([i["id"] for i in json.load(sys.stdin)["items"][:2]]))' <<<"$PAGE1")
BATCH_RESULT=$(api POST /api/v1/batch/clients "{\"action\":\"disable\",\"ids\":$BATCH_IDS}")
BATCH_RESULT="$BATCH_RESULT" python3 - <<'PY'
import json, os
r = json.loads(os.environ["BATCH_RESULT"])
assert r["action"] == "disable", r
assert r["applied"] == 2, r
assert not r["failed"], r
PY
# Bad requests are rejected before anything is applied.
for bad in '{"action":"explode","ids":[1]}' '{"action":"enable","ids":[]}' \
  '{"action":"extend","ids":[1],"days":0}'; do
  status=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" -X POST \
    -H 'Content-Type: application/json' -d "$bad" \
    "http://127.0.0.1:$PANEL_PORT/api/v1/batch/clients")
  [[ "$status" == "400" ]] || fail "bad batch $bad accepted (status $status)"
done
# An unknown id is reported per row instead of failing the whole batch.
PARTIAL=$(api POST /api/v1/batch/clients "{\"action\":\"disable\",\"ids\":[999999]}")
PARTIAL="$PARTIAL" python3 - <<'PY'
import json, os
r = json.loads(os.environ["PARTIAL"])
assert r["applied"] == 0 and len(r["failed"]) == 1, r
assert r["failed"][0]["id"] == 999999, r
PY

DASHBOARD=$(api GET /api/v1/dashboard)
DASHBOARD="$DASHBOARD" python3 - <<'PY'
import json, os
d = json.loads(os.environ["DASHBOARD"])
a = d["attention"]
for key in ("expiring_soon", "expired", "quota_exceeded", "idle", "expiring_window_days"):
    assert key in a, (key, a)
assert a["expiring_window_days"] == 7, a
PY
# Each card links to a list with the same number of rows.
for status in expiring expired quota_exceeded idle; do
  ATTENTION=$(DASHBOARD="$DASHBOARD" STATUS="$status" python3 - <<'PY'
import json, os
d = json.loads(os.environ["DASHBOARD"])
key = {"expiring":"expiring_soon", "quota_exceeded":"quota_exceeded"}.get(
    os.environ["STATUS"], os.environ["STATUS"])
print(d["attention"][key])
PY
)
  LIST_TOTAL=$(api GET "/api/v1/clients?status=$status" | python3 -c 'import json,sys; print(json.load(sys.stdin)["total"])')
  [[ "$ATTENTION" == "$LIST_TOTAL" ]] || fail "attention $status = $ATTENTION but the list has $LIST_TOTAL rows"
done

log "checking audit and access CSV exports"
api GET /api/v1/export/audit >"$WORK/audit.csv"
head -1 "$WORK/audit.csv" | grep -q '^created_at,created_at_local,username,action' || fail "unexpected audit export header"
AUDIT_LINES=$(($(wc -l <"$WORK/audit.csv") - 1))
[[ "$AUDIT_LINES" -gt 0 ]] || fail "audit export has no rows"
api GET /api/v1/export/access >"$WORK/access.csv"
head -1 "$WORK/access.csv" | grep -q '^started_at,started_at_local,client_id' || fail "unexpected access export header"
ACCESS_LINES=$(($(wc -l <"$WORK/access.csv") - 1))
[[ "$ACCESS_LINES" -gt 0 ]] || fail "access export has no rows"
# Paging is rejected on an export rather than silently exporting one page.
for path in "/api/v1/export/audit?limit=10" "/api/v1/export/access?offset=1"; do
  status=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" "http://127.0.0.1:$PANEL_PORT$path")
  [[ "$status" == "400" ]] || fail "$path was accepted (status $status)"
done

log "checking inbound detail"
DETAIL=$(api GET "/api/v1/inbounds/$INBOUND_ID/detail?days=7")
DETAIL="$DETAIL" python3 - <<'PY'
import json, os
d = json.loads(os.environ["DETAIL"])
assert d["inbound"]["id"] == 1, d["inbound"]
assert d["days"] == 7, d
assert len(d["daily"]) == 7, "the trend must have one point per day"
assert isinstance(d["top_clients"], list) and d["top_clients"], d
for key in ("active", "disabled", "expired", "quota_exceeded"):
    assert key in d["status_counts"], d["status_counts"]
assert isinstance(d["recent_failures"], list), d
PY
status=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" \
  "http://127.0.0.1:$PANEL_PORT/api/v1/inbounds/$INBOUND_ID/detail?days=0")
[[ "$status" == "400" ]] || fail "inbound detail accepted days=0 (status $status)"

log "checking online backup"
# The backup must be a real SQLite file, and taking it must not stop the API: a
# write issued right after it still has to succeed. BackupTo used to hold the
# store write lock for the whole copy.
BACKUP_HEADERS=$(curl -fsS -D - -o "$WORK/backup.db" -b "$COOKIE_JAR" \
  "http://127.0.0.1:$PANEL_PORT/api/v1/backup" | tr -d '\r')
grep -qi 'content-disposition: attachment' <<<"$BACKUP_HEADERS" || fail "backup is not an attachment"
grep -qi 'cache-control: no-store' <<<"$BACKUP_HEADERS" || fail "backup is cacheable"
[[ "$(head -c 15 "$WORK/backup.db")" == "SQLite format 3" ]] || fail "backup is not a SQLite database"
BACKUP_CLIENTS=$(python3 - "$WORK/backup.db" <<'PYEOF'
import sqlite3, sys
db = sqlite3.connect(sys.argv[1])
row = db.execute("SELECT COUNT(*) FROM clients").fetchone()
db.close()
print(row[0])
PYEOF
)
[[ "$BACKUP_CLIENTS" -ge 1 ]] || fail "backup has no clients"
# A write immediately after the backup proves the server was not stalled.
api POST "/api/v1/inbounds/$IMPORT_INBOUND_ID/clients" '{"name":"post-backup","enabled":true}' >/dev/null
POST_BACKUP=$(api GET "/api/v1/inbounds/$IMPORT_INBOUND_ID/clients?q=post-backup")
POST_BACKUP="$POST_BACKUP" python3 - <<'PYEOF'
import json, os
payload = json.loads(os.environ["POST_BACKUP"])
assert payload["total"] == 1, payload
PYEOF
log "backup: $(stat -c%s "$WORK/backup.db" 2>/dev/null || wc -c <"$WORK/backup.db") bytes, $BACKUP_CLIENTS clients, writes continue afterwards"

log "checking frontend"
curl -fsS "http://127.0.0.1:$PANEL_PORT/" | grep -q 'id="sspanel-config"' || fail "frontend config not injected"

log "checking deep links resolve assets from the panel root"
# Vite emits relative asset URLs. Without a <base href>, "/clients/" resolves
# "./assets/index-xxx.js" against "/clients/", the SPA fallback answers with HTML
# and the browser refuses the module: a blank page.
DEEP_LINK=$(curl -fsS "http://127.0.0.1:$PANEL_PORT/clients/")
grep -q '<base href="/">' <<<"$DEEP_LINK" || fail "deep link lacks a base href"
DEEP_ASSET=$(grep -o 'src="\./assets/[^"]*"' <<<"$DEEP_LINK" | head -1 | sed 's/^src="//; s/"$//')
[[ -n "$DEEP_ASSET" ]] || fail "deep link has no relative asset to resolve"
# The path the base resolves to must really be served, not the HTML fallback.
curl -fsS -o /dev/null "http://127.0.0.1:$PANEL_PORT/${DEEP_ASSET#./}" || fail "resolved asset is not served"

log "checking a single-user inbound cannot enable a second client"
# 2022-blake3-chacha20-poly1305 has no identity header, so a second enabled user
# would be accounted against the first. Enabling it must be rejected, not
# accepted and then only partly served.
SOLO=$(api POST /api/v1/inbounds "{\"remark\":\"smoke-solo\",\"listen\":\"127.0.0.1\",\"port\":$((SS_PORT + 4)),\"method\":\"2022-blake3-chacha20-poly1305\",\"udp_enabled\":false,\"enabled\":true}")
SOLO_ID=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$SOLO")
api POST "/api/v1/inbounds/$SOLO_ID/clients" '{"name":"solo-a","enabled":true}' >/dev/null
SOLO_SECOND=$(api POST "/api/v1/inbounds/$SOLO_ID/clients" '{"name":"solo-b","enabled":false}')
SOLO_SECOND_ID=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$SOLO_SECOND")
SOLO_STATUS=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" -X POST \
  -H 'Content-Type: application/json' -d '{"enabled":true}' \
  "http://127.0.0.1:$PANEL_PORT/api/v1/clients/$SOLO_SECOND_ID/toggle")
[[ "$SOLO_STATUS" == "400" ]] || fail "enabling a second single-user client returned $SOLO_STATUS, want 400"
SOLO_ENABLED=$(api GET "/api/v1/inbounds/$SOLO_ID/clients" | python3 -c 'import json,sys; print(sum(1 for c in json.load(sys.stdin)["items"] if c["enabled"]))')
[[ "$SOLO_ENABLED" == "1" ]] || fail "rejected toggle still left $SOLO_ENABLED enabled clients"
api DELETE "/api/v1/inbounds/$SOLO_ID" >/dev/null
log "single-user rule: second enable rejected, one enabled client remains"

log "checking import failure line numbers survive blank lines"
INVALID_LINKS=$(python3 -c 'import json; print(json.dumps({"format":"ss","content":"ss://bogus-one\n\nss://bogus-two\n"}))')
LINE_RESULT=$(api POST "/api/v1/inbounds/$IMPORT_INBOUND_ID/clients/import" "$INVALID_LINKS")
LINE_RESULT="$LINE_RESULT" python3 - <<'PYSMOKE'
import json, os
result = json.loads(os.environ["LINE_RESULT"])
assert result["created"] == 0, result
lines = sorted(item["line"] for item in result["failed"])
# The blank separator must not shift the reported line: these are the first and
# third lines of the paste, not the first and second of a filtered slice.
assert lines == [1, 3], lines
PYSMOKE
log "import line numbers: 1 and 3 (blank line preserved)"

log "checking an inbound that fails to start is reported"
BLOCK_PORT=$((SS_PORT + 5))
BLOCKED=$(api POST /api/v1/inbounds "{\"remark\":\"smoke-blocked\",\"listen\":\"127.0.0.1\",\"port\":$BLOCK_PORT,\"method\":\"2022-blake3-aes-128-gcm\",\"udp_enabled\":false,\"enabled\":false}")
BLOCKED_ID=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$BLOCKED")
python3 - "$BLOCK_PORT" <<'PYSMOKE' &
import socket, sys, time
sock = socket.socket()
sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
sock.bind(("127.0.0.1", int(sys.argv[1])))
sock.listen(1)
time.sleep(30)
PYSMOKE
BLOCKER_PID=$!
sleep 1
BLOCK_STATUS=$(curl -s -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" -X POST \
  -H 'Content-Type: application/json' -d '{"enabled":true}' \
  "http://127.0.0.1:$PANEL_PORT/api/v1/inbounds/$BLOCKED_ID/toggle")
kill "$BLOCKER_PID" 2>/dev/null || true
wait "$BLOCKER_PID" 2>/dev/null || true
[[ "$BLOCK_STATUS" != "200" ]] || fail "enabling an inbound on an occupied port returned 200"
# The row must stay disabled: the panel has to describe what is actually
# listening, not what the operator asked for.
BLOCKED_ENABLED=$(api GET "/api/v1/inbounds/$BLOCKED_ID" | python3 -c 'import json,sys; print(str(json.load(sys.stdin)["enabled"]).lower())')
[[ "$BLOCKED_ENABLED" == "false" ]] || fail "a failed enable left the inbound marked enabled"
api DELETE "/api/v1/inbounds/$BLOCKED_ID" >/dev/null
log "failed start: toggle rejected and the row stayed disabled"

log "checking the repair command"
"$BIN" repair --db "$WORK/sspanel.db" >"$WORK/repair.log" 2>&1 || { cat "$WORK/repair.log"; fail "repair failed"; }
grep -q 'integrity: ok' "$WORK/repair.log" || fail "repair did not report a clean integrity check"
grep -q 'daily totals rebuilt' "$WORK/repair.log" || fail "repair did not rebuild the rollup"
log "repair: $(tr '\n' ' ' <"$WORK/repair.log")"

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

log "checking subscription tokens are redacted under a base path"
# The redaction prefix used to be built as basePath + "sub/", which produced
# "/panelsub/" and never matched a sub-path deployment, so the token (which
# unlocks the client's share link, both PSKs included) was written to the log.
SUBPATH_DIR="$WORK/subpath"
mkdir -p "$SUBPATH_DIR"
cat >"$SUBPATH_DIR/config.yaml" <<EOF
panel:
  listen: 127.0.0.1
  port: $((PANEL_PORT + 1))
  base_path: /panel
  session_ttl: 1h
database:
  path: $SUBPATH_DIR/sspanel.db
log:
  level: info
  format: text
EOF
SSPANEL_ADMIN_PASSWORD="$ADMIN_PASSWORD" "$BIN" run --config "$SUBPATH_DIR/config.yaml" \
  >"$SUBPATH_DIR/server.log" 2>&1 &
SUBPATH_PID=$!
for _ in $(seq 1 100); do
  if curl -fsS "http://127.0.0.1:$((PANEL_PORT + 1))/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.1
done
SUBPATH_BASE="http://127.0.0.1:$((PANEL_PORT + 1))"
curl -fsS -c "$SUBPATH_DIR/cookies.txt" -b "$SUBPATH_DIR/cookies.txt" -X POST \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASSWORD\"}" \
  "$SUBPATH_BASE/panel/api/v1/auth/login" >/dev/null
SUBPATH_INBOUND=$(curl -fsS -c "$SUBPATH_DIR/cookies.txt" -b "$SUBPATH_DIR/cookies.txt" -X POST \
  -H 'Content-Type: application/json' \
  -d "{\"remark\":\"sub\",\"listen\":\"127.0.0.1\",\"port\":$((SS_PORT + 6)),\"method\":\"2022-blake3-aes-128-gcm\",\"udp_enabled\":false,\"enabled\":true}" \
  "$SUBPATH_BASE/panel/api/v1/inbounds")
SUBPATH_INBOUND_ID=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])' <<<"$SUBPATH_INBOUND")
SUBPATH_CLIENT=$(curl -fsS -c "$SUBPATH_DIR/cookies.txt" -b "$SUBPATH_DIR/cookies.txt" -X POST \
  -H 'Content-Type: application/json' -d '{"name":"sub-user","enabled":true}' \
  "$SUBPATH_BASE/panel/api/v1/inbounds/$SUBPATH_INBOUND_ID/clients")
SUBPATH_TOKEN=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["sub_token"])' <<<"$SUBPATH_CLIENT")
curl -fsS "$SUBPATH_BASE/panel/sub/$SUBPATH_TOKEN" >/dev/null || fail "sub-path subscription is not served"
kill "$SUBPATH_PID" 2>/dev/null || true
wait "$SUBPATH_PID" 2>/dev/null || true
if grep -Fq "$SUBPATH_TOKEN" "$SUBPATH_DIR/server.log"; then
  cat "$SUBPATH_DIR/server.log" >&2
  fail "the subscription token was written to the access log"
fi
grep -q '/panel/sub/<redacted>' "$SUBPATH_DIR/server.log" || fail "the sub-path request was not logged as redacted"
log "sub-path log: /panel/sub/<redacted>"

log "PASS"
