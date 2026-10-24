#!/usr/bin/env bash
# Client address resolution (#1061) — SERVER_TRUSTED_PROXIES,
# SERVER_CLIENT_IP_HEADER, SERVER_TRUSTED_HOPS.
#
# These cases launch standalone gomodel gateways with custom configuration, so
# they live outside the running-stack matrix in release-e2e-scenarios.md. The
# matrix can only show the default posture (a forwarding header is ignored);
# every trusted-proxy policy needs its own boot.
#
# They cover the whole path from configuration to a recorded address: the
# policy compiles at startup, reaches echo's IP extractor, and lands in the
# audit entry's client_ip. config/clientip_test.go covers the parsing in
# isolation and internal/server/clientip_test.go the extractor; nothing else
# checks that a configured policy actually changes what a running gateway
# records.
#
# The probe is GET /v1/models with LOGGING_ONLY_MODEL_INTERACTIONS=false, so no
# scenario calls an upstream provider or spends anything.
#
# Requires: a built binary (make build), .env (sourced for provider creds so the
# gateway has a catalog), and curl + jq + lsof.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$SCRIPT_DIR/../.." && pwd)"
BIN="${GOMODEL_RELEASE_BINARY:-$REPO/bin/gomodel}"
WORK="${CLIENTIP_WORK_DIR:-/tmp/gomodel-clientip-$$}"

PASS=0; FAIL=0
ok(){ PASS=$((PASS+1)); printf 'PASS  %s\n' "$1"; }
bad(){ FAIL=$((FAIL+1)); printf 'FAIL  %s\n' "$1"; }
note(){ printf '      .. %s\n' "$1"; }

[ -x "$BIN" ] || { echo "error: binary not found at $BIN (run: make build)" >&2; exit 1; }
[ -r "$REPO/.env" ] || { echo "error: .env not found at $REPO/.env" >&2; exit 1; }

rm -rf "$WORK"; mkdir -p "$WORK/data"
set -a; source "$REPO/.env"; set +a
PORT="${CLIENTIP_PORT:-18093}"
# IPv4 explicitly: "localhost" resolves to ::1 on this host, and the socket peer
# is what every fallback in the policy reports.
B="http://127.0.0.1:$PORT"
DIRECT="127.0.0.1"

PIDF="$WORK/gw.pid"

# start_gw boots a gateway with the SERVER_* policy passed as name=value
# arguments. The master key is left empty so the admin audit reader is
# reachable without a credential, and audit logging covers every path so the
# probe can be a free GET /v1/models.
start_gw(){
  for sp in $(lsof -nP -t -iTCP:$PORT -sTCP:LISTEN 2>/dev/null); do kill "$sp" 2>/dev/null; done
  sleep 1
  ( cd "$WORK"
    nohup env GOMODEL_MASTER_KEY= PORT=$PORT BASE_PATH= STORAGE_TYPE=sqlite \
      SQLITE_PATH="$WORK/data/gomodel.db" LOGGING_ENABLED=true \
      LOGGING_ONLY_MODEL_INTERACTIONS=false \
      RESPONSE_CACHE_SIMPLE_ENABLED=false SEMANTIC_CACHE_ENABLED=false REDIS_URL= \
      "$@" "$BIN" >"$WORK/server.log" 2>&1 < /dev/null &
    echo $! >"$PIDF" )
  local healthy=1
  for _ in $(seq 1 30); do curl -fsS "$B/health" >/dev/null 2>&1 && { healthy=0; break; }; kill -0 "$(cat "$PIDF")" 2>/dev/null || break; sleep 1; done
  [ "$healthy" = 0 ] || return 1
  # The catalog loads asynchronously; /v1/models answers 503 until it is in.
  for _ in $(seq 1 30); do grep -q 'model registry initialized' "$WORK/server.log" && break; sleep 1; done
  return 0
}
stop_gw(){ [ -f "$PIDF" ] && kill "$(cat "$PIDF")" 2>/dev/null; for _ in $(seq 1 10); do kill -0 "$(cat "$PIDF" 2>/dev/null)" 2>/dev/null || break; sleep 1; done; rm -f "$PIDF"; }
trap 'stop_gw; rm -rf "$WORK"' EXIT

# probe sends one request carrying the given headers and echoes the client_ip
# the gateway recorded for it.
probe(){
  local rid="qa-clientip-$RANDOM-$RANDOM"
  curl -fsS -o /dev/null "$B/v1/models" -H "X-Request-ID: $rid" "$@" || { echo REQFAIL; return; }
  local ip=""
  for _ in $(seq 1 10); do
    ip="$(curl -fsS "$B/admin/audit/log?search=$rid&limit=3" | jq -r --arg r "$rid" 'first(.entries[]? | select(.request_id==$r) | .client_ip) // empty')"
    [ -n "$ip" ] && break
    sleep 1
  done
  echo "${ip:-NOENTRY}"
}
expect(){ # label, expected, actual
  [ "$2" = "$3" ] && ok "$1" || bad "$1 (expected $2, got $3)"
}

# start_fails boots a gateway expected to abort at startup and greps its log.
start_fails(){ # label, message-regex, env...
  local label="$1" want="$2"; shift 2
  if start_gw "$@"; then bad "$label (gateway started)"; stop_gw; return; fi
  if grep -Eq "$want" "$WORK/server.log"; then ok "$label"; else bad "$label (message not found)"; note "$(tail -3 "$WORK/server.log")"; fi
  stop_gw
}

echo "############ CLIENT IP RESOLUTION (#1061) ############"

############ default: nothing is trusted ############
start_gw || { bad "C1 setup"; tail -20 "$WORK/server.log"; }
expect "C1 default ignores a spoofed X-Forwarded-For" "$DIRECT" "$(probe -H 'X-Forwarded-For: 203.0.113.9')"
expect "C1b default ignores X-Real-IP too" "$DIRECT" "$(probe -H 'X-Real-IP: 203.0.113.9')"
stop_gw

############ X-Forwarded-For behind a trusted loopback proxy ############
start_gw SERVER_TRUSTED_PROXIES=loopback || { bad "C2 setup"; tail -20 "$WORK/server.log"; }
expect "C2 single forwarded address resolves" "203.0.113.9" "$(probe -H 'X-Forwarded-For: 203.0.113.9')"
# Only loopback is trusted, so the nearest hop the gateway cannot vouch for is
# the proxy address the client could not have written.
expect "C3 chain scan stops at the nearest untrusted hop" "198.51.100.7" "$(probe -H 'X-Forwarded-For: 203.0.113.9, 198.51.100.7')"
# Every hop trusted means no hop is evidence: fall back to the socket peer.
expect "C4 an all-trusted chain falls back to the socket peer" "$DIRECT" "$(probe -H 'X-Forwarded-For: 127.0.0.2')"
# A chain the gateway cannot fully parse says nothing about the client.
expect "C5 an unparseable chain falls back to the socket peer" "$DIRECT" "$(probe -H 'X-Forwarded-For: 203.0.113.9, not-an-ip')"
stop_gw

############ the intermediate proxy network is trusted too ############
start_gw SERVER_TRUSTED_PROXIES=loopback,198.51.100.0/24 || { bad "C6 setup"; tail -20 "$WORK/server.log"; }
expect "C6 trusting the intermediate network reaches the client" "203.0.113.9" "$(probe -H 'X-Forwarded-For: 203.0.113.9, 198.51.100.7')"
stop_gw

############ a single-address edge header ############
start_gw SERVER_TRUSTED_PROXIES=loopback SERVER_CLIENT_IP_HEADER=CF-Connecting-IP || { bad "C7 setup"; tail -20 "$WORK/server.log"; }
expect "C7 a single-address header is taken verbatim" "203.0.113.9" "$(probe -H 'CF-Connecting-IP: 203.0.113.9' -H 'X-Forwarded-For: 192.0.2.5')"
expect "C7b the configured header missing falls back to the socket peer" "$DIRECT" "$(probe -H 'X-Forwarded-For: 192.0.2.5')"
stop_gw

############ a fixed hop depth ############
start_gw SERVER_TRUSTED_PROXIES=loopback SERVER_TRUSTED_HOPS=1 || { bad "C8 setup"; tail -20 "$WORK/server.log"; }
expect "C8 the entry at the configured depth wins over a chain scan" "198.51.100.7" "$(probe -H 'X-Forwarded-For: 203.0.113.9, 198.51.100.7')"
expect "C8b no forwarding header at all falls back to the socket peer" "$DIRECT" "$(probe)"
stop_gw

start_gw SERVER_TRUSTED_PROXIES=loopback SERVER_TRUSTED_HOPS=2 || { bad "C9 setup"; tail -20 "$WORK/server.log"; }
# A one-entry chain did not come through the two proxies the depth declares, so
# it is not evidence about the client.
expect "C9 a chain shorter than the configured depth falls back to the socket peer" "$DIRECT" "$(probe -H 'X-Forwarded-For: 203.0.113.9')"
expect "C9b a chain at the configured depth resolves" "203.0.113.9" "$(probe -H 'X-Forwarded-For: 203.0.113.9, 198.51.100.7')"
stop_gw

############ configuration negatives abort startup ############
start_fails "N1 an unparseable trusted proxy aborts startup" 'is not a valid IP address, CIDR network, or preset' SERVER_TRUSTED_PROXIES=not-a-network
start_fails "N2 a client IP header without trusted proxies aborts startup" 'client_ip_header is set but server.trusted_proxies is empty' SERVER_CLIENT_IP_HEADER=CF-Connecting-IP
start_fails "N3 the RFC 7239 Forwarded header is rejected" 'is not supported' SERVER_TRUSTED_PROXIES=loopback SERVER_CLIENT_IP_HEADER=Forwarded
start_fails "N4 hop depth on a single-address header aborts startup" 'counts hops in a X-Forwarded-For chain' SERVER_TRUSTED_PROXIES=loopback SERVER_CLIENT_IP_HEADER=X-Real-IP SERVER_TRUSTED_HOPS=1
start_fails "N5 a negative hop depth aborts startup" 'trusted_hops must be 0 or a positive number' SERVER_TRUSTED_PROXIES=loopback SERVER_TRUSTED_HOPS=-1

printf '\n############ RESULT: %d passed, %d failed ############\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]
