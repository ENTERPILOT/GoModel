#!/usr/bin/env bash
# Checks that a database written by an older release still works after
# upgrading the binary.
#
# It boots the baseline binary against an empty database, writes a row through
# every store domain, restarts it so the snapshot is read back from storage
# rather than from the caches those writes just populated, then boots the
# working-tree binary on the same database and asserts three things: startup is
# free of storage errors, every domain reads back identically, and every domain
# still accepts writes.
#
# The store test suites cannot cover this: sqlxtest builds each case a fresh
# schema, so it never meets a table created by an earlier release. That blind
# spot is how the workflow_versions.created_at break reached a real database.
#
#   usage: tests/e2e/upgrade-compat.sh [sqlite|postgresql|mongodb|all]
#
#   UPGRADE_BASELINE_REF   git ref to compare against  (default: origin/main)
#   UPGRADE_PORT           gateway port                (default: 18190)
#
# Needs .env with provider credentials (two scenarios call a real model), plus
# the docker infra from `make infra` for the postgresql and mongodb backends.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

BACKEND="${1:-all}"
BASELINE_REF="${UPGRADE_BASELINE_REF:-origin/main}"
GW_PORT="${UPGRADE_PORT:-18190}"
BASE="http://localhost:$GW_PORT"

ROOT_WORK="${UPGRADE_WORK_DIR:-/tmp/gomodel-upgrade-compat}"
BASELINE_TREE="$ROOT_WORK/baseline-tree"
OLD_BIN="$ROOT_WORK/gomodel-baseline"
NEW_BIN="$REPO_ROOT/bin/gomodel"
PG_DB="${UPGRADE_PG_DATABASE:-gomodel_upgrade_compat}"
MONGO_DB="${UPGRADE_MONGO_DATABASE:-gomodel_upgrade_compat}"
MONGO_CONTAINER="${UPGRADE_MONGO_CONTAINER:-gomodel-mongodb-1}"

die() { echo "error: $*" >&2; exit 1; }

if [[ "$BACKEND" == "all" ]]; then
  status=0
  for backend in sqlite postgresql mongodb; do
    "$0" "$backend" || status=1
  done
  exit "$status"
fi

case "$BACKEND" in
  sqlite | postgresql | mongodb) ;;
  *) die "unknown backend: $BACKEND (want sqlite, postgresql, mongodb or all)" ;;
esac

WORK="$ROOT_WORK/$BACKEND"

# ---------------------------------------------------------------- binaries --

build_binaries() {
  mkdir -p "$ROOT_WORK"

  # Always rebuild: an existing bin/gomodel from an earlier session would make
  # this harness validate a binary that predates the change under test, and
  # report a pass for it.
  (cd "$REPO_ROOT" && make build)

  # A worktree, not a checkout: the working tree must stay on its own branch.
  if [[ ! -d "$BASELINE_TREE" ]]; then
    git -C "$REPO_ROOT" worktree add --detach "$BASELINE_TREE" "$BASELINE_REF" >/dev/null
  else
    git -C "$BASELINE_TREE" checkout --detach "$BASELINE_REF" >/dev/null 2>&1 \
      || die "cannot move the baseline worktree to $BASELINE_REF"
  fi
  (cd "$BASELINE_TREE" && go build -o "$OLD_BIN" ./cmd/gomodel)
}

# ------------------------------------------------------------------ gateway --

storage_env() {
  case "$BACKEND" in
    sqlite)
      printf 'STORAGE_TYPE=sqlite\nSQLITE_PATH=%s/data/gomodel.db\n' "$WORK"
      ;;
    postgresql)
      printf 'STORAGE_TYPE=postgresql\nPOSTGRES_URL=postgres://gomodel:gomodel@localhost:5432/%s?sslmode=disable\n' "$PG_DB"
      ;;
    mongodb)
      printf 'STORAGE_TYPE=mongodb\nMONGODB_URL=mongodb://localhost:27017/?replicaSet=rs0\nMONGODB_DATABASE=%s\n' "$MONGO_DB"
      ;;
  esac
}

reset_backend() {
  case "$BACKEND" in
    postgresql)
      psql "postgres://gomodel:gomodel@localhost:5432/postgres?sslmode=disable" -v ON_ERROR_STOP=1 \
        -c "DROP DATABASE IF EXISTS $PG_DB" -c "CREATE DATABASE $PG_DB" >/dev/null
      ;;
    mongodb)
      docker exec "$MONGO_CONTAINER" mongosh --quiet \
        --eval "db.getSiblingDB('$MONGO_DB').dropDatabase()" >/dev/null
      ;;
  esac
}

start_gw() {
  local bin="$1" tag="$2" env_args=()
  while IFS= read -r line; do [[ -n "$line" ]] && env_args+=("$line"); done < <(storage_env)

  (
    cd "$WORK"
    nohup env PORT="$GW_PORT" BASE_PATH= \
      "${env_args[@]}" \
      LOGGING_ENABLED=true LOGGING_LOG_BODIES=true LOGGING_LOG_HEADERS=true \
      GUARDRAILS_ENABLED=true \
      RESPONSE_CACHE_SIMPLE_ENABLED=false SEMANTIC_CACHE_ENABLED=false \
      REDIS_URL= \
      CONFIGURED_PROVIDER_MODELS_MODE=allowlist \
      "$bin" >"$WORK/logs/$tag.log" 2>&1 </dev/null &
    echo $! >"$WORK/server.pid"
  )

  local attempt
  for attempt in $(seq 1 40); do
    if curl -fsS "$BASE/health" >/dev/null 2>&1; then
      # A healthy probe proves something is listening, not that it is ours: a
      # gateway that failed to bind leaves the previous one answering.
      if kill -0 "$(cat "$WORK/server.pid")" 2>/dev/null; then return 0; fi
      echo "FATAL: $tag gateway died but port $GW_PORT still answers" >&2
      tail -40 "$WORK/logs/$tag.log" >&2
      exit 1
    fi
    sleep 1
  done

  echo "FATAL: $tag gateway did not become healthy" >&2
  tail -40 "$WORK/logs/$tag.log" >&2
  exit 1
}

stop_gw() {
  local pid
  pid="$(cat "$WORK/server.pid" 2>/dev/null || true)"
  [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
  for _ in $(seq 1 20); do
    curl -fsS "$BASE/health" >/dev/null 2>&1 || return 0
    sleep 0.5
  done
  kill -9 "$pid" 2>/dev/null || true
}

jput() { curl -fsS -X PUT "$BASE$1" -H 'Content-Type: application/json' -d "$2"; }
jpost() { curl -fsS -X POST "$BASE$1" -H 'Content-Type: application/json' -d "$2"; }

# Creating a managed key switches the gateway out of unsafe mode, so every /v1
# call from that point on carries it.
api_key() { jq -r .value "$WORK/authkey.json"; }
vpost() { curl -fsS -X POST "$BASE$1" -H "Authorization: Bearer $(api_key)" -H 'Content-Type: application/json' -d "$2"; }
vget() { curl -fsS "$BASE$1" -H "Authorization: Bearer $(api_key)"; }

# --------------------------------------------------------------------- seed --

seed() {
  echo "-- writing one row per store domain with the $BASELINE_REF binary"

  jput /admin/virtual-models \
    '{"source":"compat-alias","target_model":"openai/gpt-4.1-nano","description":"upgrade compat alias"}' >/dev/null
  jput /admin/virtual-models \
    '{"source":"compat-lb","targets":[{"provider":"openai","model":"gpt-4.1-nano","weight":2},{"provider":"groq","model":"groq/compound-mini"}],"strategy":"round_robin"}' >/dev/null

  jput /admin/failover \
    '{"primary_model":"compat-failover-src","fallback_models":["groq/groq/compound-mini","gemini/gemini-2.5-flash-lite"]}' >/dev/null

  jput /admin/guardrails \
    '{"name":"compat-guardrail","type":"system_prompt","description":"upgrade compat","config":{"mode":"inject","content":"compat guardrail content"}}' >/dev/null

  jput /admin/budgets \
    '{"user_path":"/compat/budget","budget_key":{"period":"daily"},"amount":12.5}' >/dev/null

  jput /admin/rate-limits \
    '{"scope":"provider","subject":"openai","limit_key":{"period":"hour"},"max_requests":99999}' >/dev/null
  jput /admin/rate-limits \
    '{"scope":"user_path","subject":"/compat/rl","limit_key":{"period":"minute"},"max_requests":42,"max_tokens":4242}' >/dev/null

  jput /admin/tagging/settings \
    '{"headers":[{"header":"X-Compat-Tag","prefix":"c-","do_not_pass":true,"delimiter":";"}]}' >/dev/null

  jput /admin/mcp-servers \
    '{"name":"compat-mcp","url":"http://localhost:18090/beta","transport":"http","description":"upgrade compat mcp"}' >/dev/null

  jput /admin/provider-credentials \
    '{"name":"compat-prov","type":"openai","api_keys":["sk-compat-secret"],"base_url":"https://example.invalid/v1","models":["compat-cred-model"]}' >/dev/null

  jput /admin/model-pricing-overrides \
    '{"selector":"openai/gpt-4.1-nano","pricing":{"input_per_mtok":1.25,"output_per_mtok":9.5}}' >/dev/null

  jpost /admin/auth-keys \
    '{"name":"compat-key","description":"upgrade compat key","user_path":"/compat","labels":["compat-label"]}' \
    > "$WORK/authkey.json"

  jpost /admin/workflows \
    '{"scope_provider":"openai","scope_model":"gpt-4.1-nano","scope_user_path":"/compat","name":"compat-workflow","description":"upgrade compat workflow","workflow_payload":{"schema_version":1,"features":{"cache":false,"audit":true,"usage":true,"guardrails":false,"failover":false},"guardrails":[]}}' \
    > "$WORK/workflow.json"

  # A real chat call fills the usage and audit-log stores.
  vpost /v1/chat/completions \
    '{"model":"gpt-4.1-nano","messages":[{"role":"user","content":"Reply with exactly COMPAT_OK"}],"max_tokens":16,"temperature":0}' \
    > "$WORK/chat.json"

  vpost /v1/conversations '{"metadata":{"purpose":"upgrade-compat"}}' > "$WORK/conversation.json"
  vpost /v1/responses \
    '{"model":"gpt-4.1-nano","input":"Reply with exactly RESP_OK","store":true,"max_output_tokens":16}' \
    > "$WORK/response.json"

  printf '%s\n' '{"custom_id":"compat-1","method":"POST","url":"/v1/chat/completions","body":{"model":"gpt-4.1-nano","messages":[{"role":"user","content":"hi"}],"max_tokens":8}}' \
    > "$WORK/batch.jsonl"
  curl -fsS -X POST "$BASE/v1/files?provider=openai" -H "Authorization: Bearer $(api_key)" \
    -F purpose=batch -F "file=@$WORK/batch.jsonl" > "$WORK/file.json"
}

# ----------------------------------------------------------------- snapshot --

# snapshot writes one normalized JSON file per domain into $1. Fields that
# legitimately differ between two runs of the same binary are dropped: rate
# limit counters live in memory and reset with the process, and budget ratios
# and settings timestamps move with the clock. Budget scope/subject are dropped
# only while the baseline predates budget scopes: user_path is still emitted for
# user-path budgets, so the migrated row is still compared by its identity.
snapshot() {
  local out="$1"
  mkdir -p "$out"

  curl -fsS "$BASE/admin/virtual-models" \
    | jq -S 'map(select(.source|startswith("compat")))' > "$out/virtual-models.json"
  curl -fsS "$BASE/admin/failover" | jq -S '.' > "$out/failover.json"
  curl -fsS "$BASE/admin/guardrails" \
    | jq -S 'map(select(.name|startswith("compat")))' > "$out/guardrails.json"
  curl -fsS "$BASE/admin/budgets" \
    | jq -S 'del(.server_time) | .budgets |= map(select(.user_path // "" |startswith("/compat")) | del(.period_ratio, .scope, .subject))' \
    > "$out/budgets.json"
  curl -fsS "$BASE/admin/budgets/settings" | jq -S 'del(.updated_at)' > "$out/budget-settings.json"
  curl -fsS "$BASE/admin/rate-limits" \
    | jq -S 'del(.server_time) | .rate_limits |= map(select(.subject == "openai" or (.subject|startswith("/compat"))) | del(.requests_used, .requests_remaining, .tokens_used, .tokens_remaining, .window_start, .window_reset))' \
    > "$out/rate-limits.json"
  curl -fsS "$BASE/admin/tagging/settings" | jq -S '.' > "$out/tagging.json"
  curl -fsS "$BASE/admin/mcp-servers" \
    | jq -S 'map(select(.name|startswith("compat")) | del(.status, .last_error, .connected_at, .last_listed_at, .tool_count, .prompt_count, .resource_count))' \
    > "$out/mcp-servers.json"
  curl -fsS "$BASE/admin/provider-credentials" \
    | jq -S 'map(select(.name|startswith("compat")))' > "$out/provider-credentials.json"
  curl -fsS "$BASE/admin/model-pricing-overrides" | jq -S '.' > "$out/pricing-overrides.json"
  curl -fsS "$BASE/admin/auth-keys" \
    | jq -S 'if type=="array" then map(select(.name|startswith("compat"))) else . end' > "$out/auth-keys.json"
  # created_at is dropped because it is the one field whose representation
  # deliberately changed: PostgreSQL stored it as TIMESTAMPTZ and now stores
  # unix seconds like SQLite always did, so the same instant renders without
  # sub-second precision and in UTC. That the instant itself survives is
  # asserted against a real database by
  # workflows.TestNewSQLStoreConvertsTimestamptzCreatedAt.
  curl -fsS "$BASE/admin/workflows" \
    | jq -S 'if type=="array" then map(select(.name|startswith("compat")) | del(.created_at)) else . end' \
    > "$out/workflows.json"

  vget "/v1/conversations/$(jq -r .id "$WORK/conversation.json")" \
    | jq -S 'del(.updated_at)' > "$out/conversation.json"
  vget "/v1/responses/$(jq -r .id "$WORK/response.json")" | jq -S '.' > "$out/response.json"
  vget "/v1/files/$(jq -r .id "$WORK/file.json")" | jq -S '.' > "$out/file.json"

  curl -fsS "$BASE/admin/usage/summary?days=7" \
    | jq -S 'del(.server_time, .generated_at)' > "$out/usage-summary.json"
  curl -fsS "$BASE/admin/audit/log?limit=20" \
    | jq -S '{count: ((.entries // .logs // []) | length), models: [((.entries // .logs // [])[] | .model)] | sort}' \
    > "$out/audit-log.json"
}

fail=0
report() {
  if [[ "$2" == 0 ]]; then
    printf 'PASS  %s\n' "$1"
  else
    printf 'FAIL  %s\n' "$1"
    fail=1
  fi
}

# --------------------------------------------------------------------- run --

echo "=============================================================="
echo " upgrade compatibility: $BACKEND ($BASELINE_REF -> working tree)"
echo "=============================================================="

command -v jq >/dev/null || die "jq is required"
[[ -r "$REPO_ROOT/.env" ]] || die ".env is missing at $REPO_ROOT/.env"

build_binaries

# A gateway left over from an aborted run would answer the health probe while
# the new process fails to bind, and would keep serving the database this run
# is about to delete.
if lsof -ti tcp:"$GW_PORT" >/dev/null 2>&1; then
  lsof -ti tcp:"$GW_PORT" | xargs kill 2>/dev/null || true
  sleep 2
fi

rm -rf "$WORK"
mkdir -p "$WORK/data" "$WORK/logs"

set -a
# shellcheck disable=SC1091
source "$REPO_ROOT/.env"
set +a
unset GOMODEL_MASTER_KEY PORT

reset_backend

start_gw "$OLD_BIN" baseline
echo "-- $BASELINE_REF gateway up"
seed
stop_gw

# The seed writes populated in-process caches; some admin reads serve those
# rather than the store. Restarting makes the baseline snapshot a storage read,
# so a difference later means storage, not caching.
start_gw "$OLD_BIN" baseline-reread
snapshot "$WORK/baseline"
stop_gw
echo "-- database now holds rows written by $BASELINE_REF"

start_gw "$NEW_BIN" upgraded
echo "-- working-tree gateway up on the same database"

if grep -iE '"level":"(ERROR|FATAL)"' "$WORK/logs/upgraded.log" \
  | grep -viE 'fetch models' > "$WORK/startup-errors.txt"; then
  report "startup is free of storage errors" 1
  cat "$WORK/startup-errors.txt"
else
  report "startup is free of storage errors" 0
fi

snapshot "$WORK/upgraded"
for baseline_file in "$WORK/baseline"/*.json; do
  name="$(basename "$baseline_file")"
  if diff -u "$baseline_file" "$WORK/upgraded/$name" > "$WORK/diff-$name.txt"; then
    report "reads identical: $name" 0
  else
    report "reads identical: $name" 1
    head -40 "$WORK/diff-$name.txt"
  fi
done

echo "-- exercising writes on the upgraded database"
write_check() {
  local label="$1" rc=0
  shift
  "$@" >/dev/null 2>"$WORK/write-err.txt" || rc=$?
  report "write works: $label" "$rc"
  [[ "$rc" == 0 ]] || cat "$WORK/write-err.txt"
}

write_check "virtual-models upsert" jput /admin/virtual-models '{"source":"compat-alias","target_model":"openai/gpt-4.1-mini","description":"updated after upgrade"}'
write_check "failover upsert" jput /admin/failover '{"primary_model":"compat-failover-src","fallback_models":["groq/groq/compound-mini"]}'
write_check "guardrail upsert" jput /admin/guardrails '{"name":"compat-guardrail","type":"system_prompt","description":"updated","config":{"mode":"inject","content":"updated content"}}'
write_check "budget upsert" jput /admin/budgets '{"user_path":"/compat/budget","budget_key":{"period":"daily"},"amount":22.5}'
write_check "label budget upsert" jput /admin/budgets '{"scope":"label","subject":"compat-label","budget_key":{"period":"daily"},"amount":7.5}'
write_check "rate-limit upsert" jput /admin/rate-limits '{"scope":"user_path","subject":"/compat/rl","limit_key":{"period":"minute"},"max_requests":43,"max_tokens":4343}'
write_check "tagging upsert" jput /admin/tagging/settings '{"headers":[{"header":"X-Compat-Tag-2"}]}'
write_check "mcp-server upsert" jput /admin/mcp-servers '{"name":"compat-mcp","url":"http://localhost:18090/beta","transport":"http","description":"updated"}'
write_check "provider-credential upsert" jput /admin/provider-credentials '{"name":"compat-prov","type":"openai","api_keys":["***********"],"base_url":"https://example.invalid/v2","models":["compat-cred-model"]}'
write_check "pricing override upsert" jput /admin/model-pricing-overrides '{"selector":"openai/gpt-4.1-nano","pricing":{"input_per_mtok":2.25,"output_per_mtok":9.75}}'
write_check "auth-key create" jpost /admin/auth-keys '{"name":"compat-key-2","user_path":"/compat"}'
write_check "workflow create" jpost /admin/workflows '{"scope_provider":"openai","scope_model":"gpt-4.1-mini","scope_user_path":"/compat","name":"compat-workflow-2","workflow_payload":{"schema_version":1,"features":{"cache":false,"audit":true,"usage":true,"guardrails":false,"failover":false},"guardrails":[]}}'
write_check "chat completion (usage + audit)" vpost /v1/chat/completions '{"model":"gpt-4.1-nano","messages":[{"role":"user","content":"Reply with exactly UPGRADE_OK"}],"max_tokens":16,"temperature":0}'
write_check "response snapshot" vpost /v1/responses '{"model":"gpt-4.1-nano","input":"Reply with exactly UPGRADE_RESP_OK","store":true,"max_output_tokens":16}'
write_check "conversation create" vpost /v1/conversations '{"metadata":{"purpose":"post-upgrade"}}'

upload_file() {
  curl -fsS -X POST "$BASE/v1/files?provider=openai" -H "Authorization: Bearer $(api_key)" \
    -F purpose=batch -F "file=@$WORK/batch.jsonl"
}
write_check "file upload" upload_file

stop_gw

echo
if [[ "$fail" == 0 ]]; then
  echo "RESULT $BACKEND: PASS"
else
  echo "RESULT $BACKEND: FAIL"
fi
exit "$fail"
