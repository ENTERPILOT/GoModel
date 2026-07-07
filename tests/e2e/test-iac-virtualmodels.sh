#!/usr/bin/env bash
# IaC virtual models (#433) — declarative VIRTUAL_MODELS env + config.yaml.
#
# These cases launch standalone gomodel gateways with custom configuration, so
# they live outside the running-stack matrix in release-e2e-scenarios.md. They
# cover: a valid declaration boots even on a COLD model catalog (the catalog
# loads asynchronously after startup), managed entries are read-only to the admin
# API, declarative entries override store rows of the same source (non-
# destructively), the VIRTUAL_MODELS env merges over config.yaml per source, a
# STRUCTURALLY invalid declaration aborts startup, and an unknown-but-structurally
# -valid target does NOT abort startup (it is simply skipped at resolve time).
# Target PROVIDER names are validated at startup too (#464): a name matching no
# configured provider aborts, while a provider declared in YAML whose credentials
# did not resolve only warns and leaves the target unavailable.
#
# Requires: a built binary (make build), provider creds in .env (OpenAI + Groq),
# and curl + jq + lsof. Runs entirely on localhost with in-memory caches and
# throwaway sqlite — no Redis or warm cache needed (that requirement was the F3
# bug; startup validation now checks structure only).
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$SCRIPT_DIR/../.." && pwd)"
BIN="${GOMODEL_RELEASE_BINARY:-$REPO/bin/gomodel}"
WORK="${IAC_WORK_DIR:-/tmp/gomodel-iac-vm-$$}"

PASS=0; FAIL=0
ok(){ PASS=$((PASS+1)); printf 'PASS  %s\n' "$1"; }
bad(){ FAIL=$((FAIL+1)); printf 'FAIL  %s\n' "$1"; }
note(){ printf '      .. %s\n' "$1"; }

[ -x "$BIN" ] || { echo "error: binary not found at $BIN (run: make build)" >&2; exit 1; }
[ -r "$REPO/.env" ] || { echo "error: .env not found at $REPO/.env" >&2; exit 1; }

rm -rf "$WORK"; mkdir -p "$WORK/data"
# Load provider creds, then pin our own PORT AFTER sourcing so the .env's own
# PORT=8080 cannot clobber the test gateway's port.
set -a; source "$REPO/.env"; set +a
PORT="${IAC_PORT:-18091}"
NEG_PORT="${IAC_NEG_PORT:-18092}"
B="http://localhost:$PORT"

PIDF="$WORK/gw.pid"
# start_gw boots a gateway in unsafe mode (empty master key) with an in-memory
# model cache, so EVERY boot validates managed config against a cold catalog. It
# waits for /health (startup validation passed) and then for the async model load
# to finish ("model registry initialized") so resolve-time chats have a catalog.
start_gw(){ # uses $VM_ENV (may be empty) and $WORK/config.yaml if present
  for sp in $(lsof -nP -t -iTCP:$PORT -sTCP:LISTEN 2>/dev/null); do kill "$sp" 2>/dev/null; done
  sleep 1
  ( cd "$WORK"
    nohup env GOMODEL_MASTER_KEY= PORT=$PORT BASE_PATH= STORAGE_TYPE=sqlite \
      SQLITE_PATH="$WORK/data/gomodel.db" CONFIGURED_PROVIDER_MODELS_MODE=fallback \
      RESPONSE_CACHE_SIMPLE_ENABLED=false SEMANTIC_CACHE_ENABLED=false REDIS_URL= \
      VIRTUAL_MODELS="${VM_ENV:-}" "$BIN" >"$WORK/server.log" 2>&1 < /dev/null &
    echo $! >"$PIDF" )
  local healthy=1
  for _ in $(seq 1 30); do curl -fsS "$B/health" >/dev/null 2>&1 && { healthy=0; break; }; kill -0 "$(cat "$PIDF")" 2>/dev/null || break; sleep 1; done
  [ "$healthy" = 0 ] || return 1
  for _ in $(seq 1 30); do grep -q 'model registry initialized' "$WORK/server.log" && break; sleep 1; done
  sleep 1
  return 0
}
stop_gw(){ [ -f "$PIDF" ] && kill "$(cat "$PIDF")" 2>/dev/null; for _ in $(seq 1 10); do kill -0 "$(cat "$PIDF" 2>/dev/null)" 2>/dev/null || break; sleep 1; done; rm -f "$PIDF"; }
chat_provider(){ curl -sS "$B/v1/chat/completions" -H 'Content-Type: application/json' \
  -d "{\"model\":\"$1\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"max_tokens\":5,\"temperature\":0}" | jq -r '.provider // "ERR"'; }
trap 'stop_gw; rm -rf "$WORK"' EXIT

echo "############ IaC VIRTUAL MODELS (#433) ############"

############ valid declarative config boots on a COLD catalog (F3 regression) ############
export VM_ENV='[{"source":"qa-iac-alias","target":"openai/gpt-4.1-nano"},{"source":"qa-iac-lb","strategy":"round_robin","targets":[{"model":"openai/gpt-4.1-nano"},{"model":"groq/llama-3.1-8b-instant"}]}]'
start_gw && ok "I0 gateway starts with valid VIRTUAL_MODELS" || { bad "I0 startup"; tail -20 "$WORK/server.log"; }
# The in-memory cache is empty at startup, so config validation ran against a cold
# catalog ("cached_models":0) yet the gateway still came up — this is the fix.
if grep -q '"cached_models":0' "$WORK/server.log"; then ok "I0b booted with a COLD catalog at config-validation time (F3 fixed)"; else note "could not confirm cold catalog from log"; fi

curl -sS "$B/admin/virtual-models" > "$WORK/list.json"
jq -e 'any(.[];.source=="qa-iac-alias" and .managed==true and .kind=="redirect") and any(.[];.source=="qa-iac-lb" and .managed==true and (.targets|length)==2)' "$WORK/list.json" >/dev/null \
  && ok "I1 managed entries present with managed=true" || bad "I1 managed entries shape"

[ "$(chat_provider qa-iac-alias)" = openai ] && ok "I2 managed alias resolves (openai)" || bad "I2 managed alias resolve"
lb="$(for i in 1 2 3 4; do chat_provider qa-iac-lb; done)"
{ grep -q openai <<<"$lb" && grep -q groq <<<"$lb"; } && ok "I3 managed round-robin LB spreads across targets" || bad "I3 managed LB spread"

managed_rejected(){ # method, name, json
  local code; code=$(curl -sS -o "$WORK/w.json" -w '%{http_code}' -X "$1" "$B/admin/virtual-models" -H 'Content-Type: application/json' -d "$3")
  if [ "$code" = 400 ] && jq -e '(.error.message|test("managed by config"))' "$WORK/w.json" >/dev/null; then ok "$2"; else bad "$2 (code=$code)"; fi
}
managed_rejected PUT    "I4 admin PUT on managed source rejected"    '{"source":"qa-iac-alias","target_model":"groq/llama-3.1-8b-instant"}'
managed_rejected DELETE "I5 admin DELETE on managed source rejected" '{"source":"qa-iac-alias"}'
managed_rejected PUT    "I6 rename of managed source rejected"       '{"source":"qa-iac-renamed","old_source":"qa-iac-alias","target_model":"openai/gpt-4.1-nano"}'
stop_gw

############ managed overrides store row of same source (restart) ############
export VM_ENV=""; rm -f "$WORK/data/gomodel.db"*
start_gw >/dev/null || bad "I7 setup gateway"
curl -sS -o /dev/null -X PUT "$B/admin/virtual-models" -H 'Content-Type: application/json' -d '{"source":"qa-iac-ovr","target_model":"groq/llama-3.1-8b-instant"}'
note "store baseline qa-iac-ovr -> $(chat_provider qa-iac-ovr)"
stop_gw
export VM_ENV='[{"source":"qa-iac-ovr","target":"openai/gpt-4.1-nano"}]'
start_gw >/dev/null || bad "I7 override gateway"
rows=$(curl -sS "$B/admin/virtual-models" | jq '[.[]|select(.source=="qa-iac-ovr")]|length')
{ [ "$(chat_provider qa-iac-ovr)" = openai ] && [ "$rows" = 1 ]; } && ok "I7 managed config overrides store row (resolves openai, single row)" || bad "I7 override (rows=$rows)"
stop_gw
export VM_ENV=""
start_gw >/dev/null || bad "I7b resurface gateway"
{ [ "$(chat_provider qa-iac-ovr)" = groq ] && curl -sS "$B/admin/virtual-models" | jq -e 'any(.[];.source=="qa-iac-ovr" and .managed!=true)' >/dev/null; } \
  && ok "I7b store row resurfaces after IaC removed (overlay non-destructive)" || bad "I7b resurface"
stop_gw

############ env VIRTUAL_MODELS merges over config.yaml per source ############
rm -f "$WORK/data/gomodel.db"*
cat > "$WORK/config.yaml" <<'YAML'
virtual_models:
  - source: qa-iac-merge
    target: groq/llama-3.1-8b-instant
  - source: qa-iac-yamlonly
    target: groq/llama-3.1-8b-instant
YAML
export VM_ENV='[{"source":"qa-iac-merge","target":"openai/gpt-4.1-nano"}]'
start_gw >/dev/null || { bad "I8 merge gateway"; tail -20 "$WORK/server.log"; }
[ "$(chat_provider qa-iac-merge)" = openai ] && ok "I8 env overrides YAML for matching source" || bad "I8 env-over-yaml"
[ "$(chat_provider qa-iac-yamlonly)" = groq ] && ok "I8b YAML-only entry still loads" || bad "I8b yaml-only"
stop_gw; rm -f "$WORK/config.yaml"

############ STRUCTURALLY invalid declaration aborts startup (negative) ############
fail_start(){ # name, VM_ENV  -> expect the process to EXIT with a clear error
  for sp in $(lsof -nP -t -iTCP:$NEG_PORT -sTCP:LISTEN 2>/dev/null); do kill "$sp" 2>/dev/null; done
  ( cd "$WORK"; nohup env GOMODEL_MASTER_KEY= PORT=$NEG_PORT BASE_PATH= STORAGE_TYPE=sqlite SQLITE_PATH="$WORK/data/neg.db" REDIS_URL= RESPONSE_CACHE_SIMPLE_ENABLED=false VIRTUAL_MODELS="$2" "$BIN" >"$WORK/neg.log" 2>&1 < /dev/null & echo $! >"$WORK/neg.pid" )
  sleep 4
  if kill -0 "$(cat "$WORK/neg.pid")" 2>/dev/null; then bad "$1 (process still alive)";
  elif grep -qiE 'failed to initialize virtual models|strateg|cannot target itself|unknown target provider' "$WORK/neg.log"; then ok "$1"; note "$(grep -iE 'error' "$WORK/neg.log" | tail -1 | sed 's/.*"error"://')";
  else bad "$1 (no clear error)"; tail -3 "$WORK/neg.log"; fi
  kill "$(cat "$WORK/neg.pid")" 2>/dev/null; rm -f "$WORK/data/neg.db"* "$WORK/neg.pid"
}
fail_start "I9 unknown strategy aborts startup"         '[{"source":"qa-iac-bad","strategy":"weighted","targets":[{"model":"openai/gpt-4.1-nano"},{"model":"groq/llama-3.1-8b-instant"}]}]'
fail_start "I10 self-referential target aborts startup" '[{"source":"openai/gpt-4.1-nano","target":"openai/gpt-4.1-nano"}]'
# Explicit target provider names are static config, so a typo is caught before
# any API call (#464) — unlike model availability, which stays a resolve-time
# concern (I11). Only the explicit "provider" field is checked; the "target"
# shorthand and slash-shaped model IDs are never treated as provider names.
fail_start "I12 misspelled target provider aborts startup" '[{"source":"qa-iac-badprov","targets":[{"provider":"opnai","model":"gpt-4.1-nano"}]}]'

############ availability is NOT a startup gate (F3 fix): unknown target boots, stays unavailable ############
rm -f "$WORK/data/gomodel.db"*
export VM_ENV='[{"source":"qa-iac-unknown","target":"openai/this-model-xyz-404"}]'
if start_gw >/dev/null; then
  code=$(curl -sS -o /dev/null -w '%{http_code}' "$B/v1/chat/completions" -H 'Content-Type: application/json' \
    -d '{"model":"qa-iac-unknown","messages":[{"role":"user","content":"hi"}],"max_tokens":5}')
  { [ "$code" = 404 ] || [ "$code" = 400 ]; } \
    && ok "I11 unknown IaC target no longer aborts startup; redirect unavailable (resolve http=$code)" \
    || bad "I11 unknown target resolved unexpectedly (http=$code)"
else
  bad "I11 gateway aborted on an unknown target (F3 regression)"; tail -5 "$WORK/server.log"
fi
stop_gw

############ a DECLARED provider with unresolved credentials warns, never aborts ############
# A shared config.yaml may declare providers whose credentials are only set in
# some environments. Two same-type entries keep the by-type env overlay from
# credentialing them, so both stay declared-but-unregistered on every machine.
rm -f "$WORK/data/gomodel.db"*
cat > "$WORK/config.yaml" <<'YAML'
providers:
  qa-keyless-a:
    type: anthropic
    api_key: "${QA_UNSET_KEY_A}"
  qa-keyless-b:
    type: anthropic
    api_key: "${QA_UNSET_KEY_B}"
YAML
export VM_ENV='[{"source":"qa-iac-keyless","targets":[{"provider":"qa-keyless-a","model":"claude-sonnet-x"}]}]'
if start_gw >/dev/null; then
  grep -q 'configured but not registered' "$WORK/server.log" \
    && ok "I13 declared-but-unregistered target provider boots with a warning" \
    || bad "I13 missing not-registered warning"
  code=$(curl -sS -o /dev/null -w '%{http_code}' "$B/v1/chat/completions" -H 'Content-Type: application/json' \
    -d '{"model":"qa-iac-keyless","messages":[{"role":"user","content":"hi"}],"max_tokens":5}')
  { [ "$code" = 404 ] || [ "$code" = 400 ]; } \
    && ok "I13b keyless-provider redirect stays unavailable (resolve http=$code)" \
    || bad "I13b keyless-provider redirect resolved unexpectedly (http=$code)"
  grep -q 'configured providers skipped' "$WORK/server.log" \
    && ok "I13c startup logs the skipped keyless providers" \
    || bad "I13c missing skipped-providers log line"
else
  bad "I13 gateway aborted on a declared-but-unregistered target provider"; tail -5 "$WORK/server.log"
fi
stop_gw; rm -f "$WORK/config.yaml"

echo "############ IaC RESULT: PASS=$PASS FAIL=$FAIL ############"
[ "$FAIL" -eq 0 ]
