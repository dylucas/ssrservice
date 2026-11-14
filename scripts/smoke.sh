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
