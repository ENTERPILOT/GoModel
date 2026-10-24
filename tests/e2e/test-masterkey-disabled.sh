#!/usr/bin/env bash
# Disabled master key authentication (#1067) — MASTER_KEY_DISABLED.
#
# These cases launch standalone gomodel gateways with custom configuration, so
# they live outside the running-stack matrix in release-e2e-scenarios.md.
#
# internal/server/master_key_disabled_test.go covers the middleware in
# isolation. What only a real boot shows is the posture an operator actually
# gets: the configured master key stops working everywhere, the admin API is
# NOT opened for bootstrap the way an absent master key opens it, managed keys
# keep working on both surfaces, and a gateway left with no credential at all
# rejects every request instead of falling back to unauthenticated access.
# Getting that last one backwards would silently open a gateway the operator
# believes they just locked down.
#
# The probe is GET /v1/models, so no scenario calls an upstream provider or
# spends anything.
#
# Requires: a built binary (make build), .env (sourced for provider creds so the
# gateway has a catalog), and curl + jq + lsof.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$SCRIPT_DIR/../.." && pwd)"
BIN="${GOMODEL_RELEASE_BINARY:-$REPO/bin/gomodel}"
WORK="${MKD_WORK_DIR:-/tmp/gomodel-mkd-$$}"

PASS=0; FAIL=0
ok(){ PASS=$((PASS+1)); printf 'PASS  %s\n' "$1"; }
bad(){ FAIL=$((FAIL+1)); printf 'FAIL  %s\n' "$1"; }
note(){ printf '      .. %s\n' "$1"; }

[ -x "$BIN" ] || { echo "error: binary not found at $BIN (run: make build)" >&2; exit 1; }
[ -r "$REPO/.env" ] || { echo "error: .env not found at $REPO/.env" >&2; exit 1; }

rm -rf "$WORK"; mkdir -p "$WORK/data"
set -a; source "$REPO/.env"; set +a
PORT="${MKD_PORT:-18094}"
B="http://127.0.0.1:$PORT"
MASTER="qa-master-key-$RANDOM$RANDOM"

PIDF="$WORK/gw.pid"

# start_gw boots against one persistent sqlite file so a managed key issued
# while the master key still worked survives into the disabled boot — that
# restart is the operator flow this feature exists for.
start_gw(){ # env overrides as name=value arguments
  for sp in $(lsof -nP -t -iTCP:$PORT -sTCP:LISTEN 2>/dev/null); do kill "$sp" 2>/dev/null; done
  sleep 1
  ( cd "$WORK"
    nohup env PORT=$PORT BASE_PATH= STORAGE_TYPE=sqlite \
      SQLITE_PATH="$WORK/data/gomodel.db" LOGGING_ENABLED=true \
      RESPONSE_CACHE_SIMPLE_ENABLED=false SEMANTIC_CACHE_ENABLED=false REDIS_URL= \
      "$@" "$BIN" >"$WORK/server.log" 2>&1 < /dev/null &
    echo $! >"$PIDF" )
  local healthy=1
  for _ in $(seq 1 30); do curl -fsS "$B/health" >/dev/null 2>&1 && { healthy=0; break; }; kill -0 "$(cat "$PIDF")" 2>/dev/null || break; sleep 1; done
  [ "$healthy" = 0 ] || return 1
  for _ in $(seq 1 30); do grep -q 'model registry initialized' "$WORK/server.log" && break; sleep 1; done
  return 0
}
stop_gw(){ [ -f "$PIDF" ] && kill "$(cat "$PIDF")" 2>/dev/null; for _ in $(seq 1 10); do kill -0 "$(cat "$PIDF" 2>/dev/null)" 2>/dev/null || break; sleep 1; done; rm -f "$PIDF"; }
trap 'stop_gw; rm -rf "$WORK"' EXIT

code(){ # path, [curl args...] -> HTTP status
  local path="$1"; shift
  curl -sS -o "$WORK/body.json" -w '%{http_code}' "$B$path" "$@"
}
expect(){ [ "$2" = "$3" ] && ok "$1" || bad "$1 (expected $2, got $3)"; }

echo "############ MASTER KEY DISABLED (#1067) ############"

############ issue a managed key while the master key still authenticates ############
start_gw GOMODEL_MASTER_KEY="$MASTER" || { bad "M0 setup gateway"; tail -20 "$WORK/server.log"; }
expect "M0 the master key authenticates before it is disabled" 200 "$(code /v1/models -H "Authorization: Bearer $MASTER")"
curl -fsS -X POST "$B/admin/auth-keys" -H "Authorization: Bearer $MASTER" -H 'Content-Type: application/json' \
  -d '{"name":"qa-mkd-admin","description":"managed key with admin access","dashboard_access":true}' > "$WORK/admin-key.json"
curl -fsS -X POST "$B/admin/auth-keys" -H "Authorization: Bearer $MASTER" -H 'Content-Type: application/json' \
  -d '{"name":"qa-mkd-plain","description":"managed key without admin access"}' > "$WORK/plain-key.json"
ADMIN_KEY="$(jq -er '.value' "$WORK/admin-key.json")" || { bad "M0 admin managed key"; ADMIN_KEY=""; }
PLAIN_KEY="$(jq -er '.value' "$WORK/plain-key.json")" || { bad "M0 plain managed key"; PLAIN_KEY=""; }
[ -n "$ADMIN_KEY" ] && [ -n "$PLAIN_KEY" ] && ok "M0b issued two managed keys" || bad "M0b issued two managed keys"
stop_gw

############ same storage, master key authentication turned off ############
start_gw GOMODEL_MASTER_KEY="$MASTER" MASTER_KEY_DISABLED=true || { bad "M1 setup gateway"; tail -20 "$WORK/server.log"; }
expect "M1 the disabled master key is rejected on /v1" 401 "$(code /v1/models -H "Authorization: Bearer $MASTER")"
expect "M2 the disabled master key is rejected on /admin" 401 "$(code /admin/auth-keys -H "Authorization: Bearer $MASTER")"
expect "M3 a request with no credential is rejected on /v1" 401 "$(code /v1/models)"
# An absent master key opens /admin/* for dashboard bootstrap; a disabled one
# must not, or turning the key off would hand the admin API to anyone.
expect "M4 the admin API is NOT opened for bootstrap" 401 "$(code /admin/auth-keys)"
expect "M5 a managed key still authenticates on /v1" 200 "$(code /v1/models -H "Authorization: Bearer $PLAIN_KEY")"
expect "M6 a managed key with dashboard access still reaches /admin" 200 "$(code /admin/auth-keys -H "Authorization: Bearer $ADMIN_KEY")"
expect "M7 a managed key without dashboard access is refused on /admin" 403 "$(code /admin/auth-keys -H "Authorization: Bearer $PLAIN_KEY")"
expect "M8 /health stays open without a credential" 200 "$(code /health)"
grep -q 'master key authentication disabled' "$WORK/server.log" && ok "M9 startup reports the disabled master key" || bad "M9 startup posture log"
# The key is forgotten at load, so nothing downstream can derive it back.
if grep -q "$MASTER" "$WORK/server.log"; then bad "M9b the master key value leaked into the log"; else ok "M9b the master key value never reaches the log"; fi
stop_gw

############ disabled with no other credential left: closed, not open ############
rm -f "$WORK/data/gomodel.db"*
start_gw GOMODEL_MASTER_KEY="$MASTER" MASTER_KEY_DISABLED=true || { bad "M10 setup gateway"; tail -20 "$WORK/server.log"; }
expect "M10 no credential configured still rejects /v1 (not unsafe mode)" 401 "$(code /v1/models)"
expect "M10b no credential configured still rejects /admin" 401 "$(code /admin/auth-keys)"
grep -q 'every request will be rejected' "$WORK/server.log" && ok "M11 startup warns that nothing can authenticate" || bad "M11 startup warning"
stop_gw

############ the unset-key baseline is unchanged ############
rm -f "$WORK/data/gomodel.db"*
start_gw GOMODEL_MASTER_KEY= || { bad "M12 setup gateway"; tail -20 "$WORK/server.log"; }
expect "M12 an absent master key still allows unauthenticated /v1 (unsafe mode)" 200 "$(code /v1/models)"
expect "M12b an absent master key still opens /admin for bootstrap" 200 "$(code /admin/auth-keys)"
stop_gw

printf '\n############ RESULT: %d passed, %d failed ############\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
