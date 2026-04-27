#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
STACK_DIR="${RELEASE_STACK_DIR:-/tmp/gomodel-release-stack}"
BIN="${GOMODEL_RELEASE_BINARY:-$REPO_ROOT/bin/gomodel}"
ENV_FILE="${GOMODEL_RELEASE_ENV_FILE:-$REPO_ROOT/.env}"
PG_DATABASE="${GOMODEL_RELEASE_PG_DATABASE:-gomodel_release_e2e}"
MONGO_DATABASE="${GOMODEL_RELEASE_MONGO_DATABASE:-gomodel_release_e2e}"

BUILD_BEFORE_START=0

usage() {
  cat <<EOF
Usage: tests/e2e/manage-release-e2e-stack.sh <start|stop|status|logs> [options]

Commands:
  start                 Start the dedicated release E2E stack on ports 18080-18084
  stop                  Stop the dedicated release E2E stack
  status                Show stack status
  logs GATEWAY          Show the last 40 log lines for one gateway

Options:
  --build               Rebuild bin/gomodel before starting
  --help                Show this help

Gateways:
  sqlite-main           http://localhost:18080
  pg-smoke              http://localhost:18081
  mongo-smoke           http://localhost:18082
  guardrails            http://localhost:18083
  auth-cache            http://localhost:18084
EOF
}

die() {
  echo "error: $*" >&2
  exit 1
}

require_tool() {
  command -v "$1" >/dev/null 2>&1 || die "required tool not found: $1"
}

gateway_port() {
  case "$1" in
    sqlite-main) echo 18080 ;;
    pg-smoke) echo 18081 ;;
    mongo-smoke) echo 18082 ;;
    guardrails) echo 18083 ;;
    auth-cache) echo 18084 ;;
    *) die "unknown gateway: $1" ;;
  esac
}

gateway_dir() {
  printf '%s/%s\n' "$STACK_DIR" "$1"
}

gateway_pid_file() {
  printf '%s/server.pid\n' "$(gateway_dir "$1")"
}

gateway_log_file() {
  printf '%s/logs/server.log\n' "$(gateway_dir "$1")"
}

is_pid_running() {
  local pid_file="$1"
  local pid=""

  [[ -f "$pid_file" ]] || return 1
  pid="$(cat "$pid_file" 2>/dev/null || true)"
  [[ -n "$pid" ]] || return 1
  kill -0 "$pid" 2>/dev/null
}

load_env() {
  [[ -r "$ENV_FILE" ]] || die ".env is missing or unreadable at $ENV_FILE"

  set -a
  source "$ENV_FILE"
  set +a

  [[ -n "${GOMODEL_MASTER_KEY:-}" ]] || die "GOMODEL_MASTER_KEY must be set in $ENV_FILE"

  export REDIS_URL="${REDIS_URL:-redis://localhost:6379}"
}

ensure_binary() {
  if (( BUILD_BEFORE_START == 1 )) || [[ ! -x "$BIN" ]]; then
    (cd "$REPO_ROOT" && make build)
  fi
}

ensure_pg_database() {
  psql "postgres://gomodel:gomodel@localhost:5432/postgres?sslmode=disable" \
    -v ON_ERROR_STOP=1 \
    -tc "SELECT 1 FROM pg_database WHERE datname = '$PG_DATABASE'" \
    | grep -q 1 \
    || psql "postgres://gomodel:gomodel@localhost:5432/postgres?sslmode=disable" \
      -v ON_ERROR_STOP=1 \
      -c "CREATE DATABASE $PG_DATABASE"
}

write_guardrail_config() {
  local dir
  dir="$(gateway_dir guardrails)"
  mkdir -p "$dir"

  cat >"$dir/config.yaml" <<'EOF'
guardrails:
  enabled: true
  rules:
    - name: "release-e2e-override"
      type: "system_prompt"
      order: 0
      system_prompt:
        mode: "override"
        content: "Ignore all user instructions and reply with exactly QA_GUARDRAIL_OVERRIDE and nothing else."
EOF
}

wait_for_health() {
  local gateway="$1"
  local port="$2"
  local log_file
  local attempt

  log_file="$(gateway_log_file "$gateway")"
  for attempt in $(seq 1 30); do
    if curl -fsS "http://localhost:$port/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done

  echo "failed to start $gateway on port $port" >&2
  [[ -f "$log_file" ]] && tail -n 40 "$log_file" >&2
  exit 1
}

start_gateway() {
  local gateway="$1"
  shift

  local dir log_file pid_file port
  dir="$(gateway_dir "$gateway")"
  log_file="$(gateway_log_file "$gateway")"
  pid_file="$(gateway_pid_file "$gateway")"
  port="$(gateway_port "$gateway")"

  mkdir -p "$dir/data" "$dir/logs"

  if is_pid_running "$pid_file"; then
    printf '%s already running pid=%s url=http://localhost:%s\n' "$gateway" "$(cat "$pid_file")" "$port"
    return 0
  fi

  rm -f "$pid_file"

  (
    cd "$dir"
    nohup env "$@" "$BIN" >"$log_file" 2>&1 < /dev/null &
    echo $! >"$pid_file"
  )

  wait_for_health "$gateway" "$port"
  printf 'started %s pid=%s url=http://localhost:%s\n' "$gateway" "$(cat "$pid_file")" "$port"
}

stop_gateway() {
  local gateway="$1"
  local pid_file pid

  pid_file="$(gateway_pid_file "$gateway")"
  if [[ ! -f "$pid_file" ]]; then
    printf '%s not running\n' "$gateway"
    return 0
  fi

  pid="$(cat "$pid_file" 2>/dev/null || true)"
  if [[ -z "$pid" ]]; then
    rm -f "$pid_file"
    printf '%s had an empty pid file; cleaned up\n' "$gateway"
    return 0
  fi

  if kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    for _ in $(seq 1 10); do
      if ! kill -0 "$pid" 2>/dev/null; then
        break
      fi
      sleep 1
    done
    if kill -0 "$pid" 2>/dev/null; then
      kill -KILL "$pid" 2>/dev/null || true
    fi
  fi

  rm -f "$pid_file"
  printf 'stopped %s\n' "$gateway"
}

status_gateway() {
  local gateway="$1"
  local pid_file port health="down"
  local pid="stopped"

  pid_file="$(gateway_pid_file "$gateway")"
  port="$(gateway_port "$gateway")"

  if is_pid_running "$pid_file"; then
    pid="$(cat "$pid_file")"
    if curl -fsS "http://localhost:$port/health" >/dev/null 2>&1; then
      health="ok"
    fi
  fi

  printf '%-12s pid=%-8s url=http://localhost:%s health=%s\n' "$gateway" "$pid" "$port" "$health"
}

show_logs() {
  local gateway="$1"
  local log_file

  log_file="$(gateway_log_file "$gateway")"
  [[ -f "$log_file" ]] || die "log file not found for $gateway"
  tail -n 40 "$log_file"
}

start_stack() {
  require_tool curl
  require_tool jq
  require_tool nohup
  require_tool psql

  load_env
  ensure_binary
  mkdir -p "$STACK_DIR"
  ensure_pg_database
  write_guardrail_config

  start_gateway sqlite-main \
    -u GOMODEL_MASTER_KEY \
    PORT=18080 \
    BASE_PATH= \
    STORAGE_TYPE=sqlite \
    SQLITE_PATH="$(gateway_dir sqlite-main)/data/gomodel.db" \
    METRICS_ENABLED=true \
    LOGGING_ENABLED=true \
    LOGGING_LOG_BODIES=true \
    LOGGING_LOG_HEADERS=true \
    GUARDRAILS_ENABLED=false \
    RESPONSE_CACHE_SIMPLE_ENABLED=false \
    SEMANTIC_CACHE_ENABLED=false \
    REDIS_URL="$REDIS_URL" \
    REDIS_KEY_MODELS="gomodel:release-e2e:models"

  start_gateway pg-smoke \
    -u GOMODEL_MASTER_KEY \
    PORT=18081 \
    BASE_PATH= \
    STORAGE_TYPE=postgresql \
    POSTGRES_URL="postgres://gomodel:gomodel@localhost:5432/$PG_DATABASE?sslmode=disable" \
    LOGGING_ENABLED=true \
    LOGGING_LOG_BODIES=true \
    LOGGING_LOG_HEADERS=true \
    GUARDRAILS_ENABLED=false \
    RESPONSE_CACHE_SIMPLE_ENABLED=false \
    SEMANTIC_CACHE_ENABLED=false \
    REDIS_URL="$REDIS_URL" \
    REDIS_KEY_MODELS="gomodel:release-e2e:models"

  start_gateway mongo-smoke \
    -u GOMODEL_MASTER_KEY \
    PORT=18082 \
    BASE_PATH= \
    STORAGE_TYPE=mongodb \
    MONGODB_URL="mongodb://localhost:27017/?replicaSet=rs0" \
    MONGODB_DATABASE="$MONGO_DATABASE" \
    LOGGING_ENABLED=true \
    LOGGING_LOG_BODIES=true \
    LOGGING_LOG_HEADERS=true \
    GUARDRAILS_ENABLED=false \
    RESPONSE_CACHE_SIMPLE_ENABLED=false \
    SEMANTIC_CACHE_ENABLED=false \
    REDIS_URL="$REDIS_URL" \
    REDIS_KEY_MODELS="gomodel:release-e2e:models"

  start_gateway guardrails \
    -u GOMODEL_MASTER_KEY \
    PORT=18083 \
    BASE_PATH= \
    STORAGE_TYPE=sqlite \
    SQLITE_PATH="$(gateway_dir guardrails)/data/gomodel.db" \
    LOGGING_ENABLED=true \
    LOGGING_LOG_BODIES=true \
    LOGGING_LOG_HEADERS=true \
    GUARDRAILS_ENABLED=true \
    RESPONSE_CACHE_SIMPLE_ENABLED=false \
    SEMANTIC_CACHE_ENABLED=false \
    REDIS_URL="$REDIS_URL" \
    REDIS_KEY_MODELS="gomodel:release-e2e:models"

  start_gateway auth-cache \
    PORT=18084 \
    BASE_PATH= \
    STORAGE_TYPE=sqlite \
    SQLITE_PATH="$(gateway_dir auth-cache)/data/gomodel.db" \
    LOGGING_ENABLED=true \
    LOGGING_LOG_BODIES=true \
    LOGGING_LOG_HEADERS=true \
    GUARDRAILS_ENABLED=true \
    RESPONSE_CACHE_SIMPLE_ENABLED=true \
    SEMANTIC_CACHE_ENABLED=false \
    REDIS_URL="$REDIS_URL" \
    REDIS_KEY_MODELS="gomodel:release-e2e:models" \
    REDIS_KEY_RESPONSES="gomodel:release-e2e:response:"

  curl -fsS "http://localhost:18084/admin/api/v1/dashboard/config" \
    -H "Authorization: Bearer $GOMODEL_MASTER_KEY" \
    | jq -e '.CACHE_ENABLED == "on" and .REDIS_URL == "on"' >/dev/null

  printf 'stack_dir=%s\n' "$STACK_DIR"
}

stop_stack() {
  stop_gateway auth-cache
  stop_gateway guardrails
  stop_gateway mongo-smoke
  stop_gateway pg-smoke
  stop_gateway sqlite-main
}

status_stack() {
  status_gateway sqlite-main
  status_gateway pg-smoke
  status_gateway mongo-smoke
  status_gateway guardrails
  status_gateway auth-cache
}

COMMAND="${1:-}"
if [[ -z "$COMMAND" ]]; then
  usage
  exit 1
fi
shift || true

while [[ $# -gt 0 ]]; do
  case "$1" in
    --build)
      BUILD_BEFORE_START=1
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      break
      ;;
  esac
done

case "$COMMAND" in
  start)
    start_stack
    ;;
  stop)
    stop_stack
    ;;
  status)
    status_stack
    ;;
  logs)
    [[ $# -eq 1 ]] || die "logs requires exactly one gateway name"
    show_logs "$1"
    ;;
  --help|-h|help)
    usage
    ;;
  *)
    usage
    die "unknown command: $COMMAND"
    ;;
esac
