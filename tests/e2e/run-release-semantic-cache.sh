#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

QA_SUFFIX="${QA_SUFFIX:-$(date +%s)-$$}"
OUTPUT_DIR="${OUTPUT_DIR:-/tmp/gomodel-release-semantic-cache-$QA_SUFFIX}"
AUTH_BASE_URL="${AUTH_BASE_URL:-http://127.0.0.1:18084}"
MOCK_PORT="${MOCK_PORT:-19090}"
QDRANT_PORT="${QDRANT_PORT:-16333}"
QDRANT_IMAGE="${QDRANT_IMAGE:-qdrant/qdrant:latest}"
SCENARIOS="${SCENARIOS:-S80,S81,S82,S83}"

mkdir -p "$OUTPUT_DIR"

for tool in docker go curl jq grep sed; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "error: required tool not found: $tool" >&2
    exit 1
  }
done

wait_for_url() {
  local url="$1"
  local label="$2"
  local header="${3:-}"
  local attempt

  for attempt in $(seq 1 60); do
    if [[ -n "$header" ]]; then
      if curl -fsS -H "$header" "$url" >/dev/null 2>&1; then
        return 0
      fi
    else
      if curl -fsS "$url" >/dev/null 2>&1; then
        return 0
      fi
    fi
    sleep 1
  done

  echo "error: timed out waiting for $label at $url" >&2
  return 1
}

clear_gomodel_env() {
  unset \
    PORT \
    GOMODEL_MASTER_KEY \
    GOMODEL_CACHE_DIR \
    MODEL_LIST_URL \
    STORAGE_TYPE \
    SQLITE_PATH \
    POSTGRES_URL \
    POSTGRES_MAX_CONNS \
    MONGODB_URL \
    MONGODB_DATABASE \
    LOGGING_ENABLED \
    LOGGING_FLUSH_INTERVAL \
    LOGGING_ONLY_MODEL_INTERACTIONS \
    USAGE_ENABLED \
    USAGE_FLUSH_INTERVAL \
    ADMIN_ENDPOINTS_ENABLED \
    ADMIN_UI_ENABLED \
    REDIS_URL \
    REDIS_KEY_MODELS \
    REDIS_TTL_MODELS \
    REDIS_KEY_RESPONSES \
    REDIS_TTL_RESPONSES \
    RESPONSE_CACHE_SIMPLE_ENABLED \
    SEMANTIC_CACHE_ENABLED \
    SEMANTIC_CACHE_THRESHOLD \
    SEMANTIC_CACHE_TTL \
    SEMANTIC_CACHE_MAX_CONV_MESSAGES \
    SEMANTIC_CACHE_EXCLUDE_SYSTEM_PROMPT \
    SEMANTIC_CACHE_EMBEDDER_PROVIDER \
    SEMANTIC_CACHE_EMBEDDER_MODEL \
    SEMANTIC_CACHE_VECTOR_STORE_TYPE \
    SEMANTIC_CACHE_QDRANT_URL \
    SEMANTIC_CACHE_QDRANT_COLLECTION \
    SEMANTIC_CACHE_QDRANT_API_KEY \
    SEMANTIC_CACHE_PGVECTOR_URL \
    SEMANTIC_CACHE_PGVECTOR_TABLE \
    SEMANTIC_CACHE_PGVECTOR_DIMENSION \
    SEMANTIC_CACHE_PINECONE_HOST \
    SEMANTIC_CACHE_PINECONE_API_KEY \
    SEMANTIC_CACHE_PINECONE_NAMESPACE \
    SEMANTIC_CACHE_PINECONE_DIMENSION \
    SEMANTIC_CACHE_WEAVIATE_URL \
    SEMANTIC_CACHE_WEAVIATE_CLASS \
    SEMANTIC_CACHE_WEAVIATE_API_KEY \
    OPENAI_API_KEY \
    OPENAI_BASE_URL
}

cleanup() {
  local status=$?

  if [[ -n "${gateway_pid:-}" ]]; then
    kill "$gateway_pid" 2>/dev/null || true
    wait "$gateway_pid" 2>/dev/null || true
  fi

  if [[ -n "${mock_pid:-}" ]]; then
    kill "$mock_pid" 2>/dev/null || true
    wait "$mock_pid" 2>/dev/null || true
  fi

  if [[ -n "${qdrant_container:-}" ]]; then
    docker rm -f "$qdrant_container" >/dev/null 2>&1 || true
  fi

  return "$status"
}
trap cleanup EXIT

AUTH_HOST="${AUTH_BASE_URL#http://}"
AUTH_HOST="${AUTH_HOST#https://}"
GATEWAY_PORT="${AUTH_HOST##*:}"
if [[ "$GATEWAY_PORT" == "$AUTH_HOST" ]]; then
  echo "error: AUTH_BASE_URL must include an explicit port, got $AUTH_BASE_URL" >&2
  exit 1
fi

safe_suffix="${QA_SUFFIX//[^A-Za-z0-9]/_}"
env_file="$OUTPUT_DIR/release-semantic.env"
runner_output_dir="$OUTPUT_DIR/release-e2e"
gateway_bin="$OUTPUT_DIR/gomodel"

echo "Starting Qdrant on port $QDRANT_PORT"
qdrant_container="$(docker run -d --rm -p "$QDRANT_PORT:6333" "$QDRANT_IMAGE")"
wait_for_url "http://127.0.0.1:$QDRANT_PORT/collections" "qdrant"

echo "Starting mock OpenAI backend on port $MOCK_PORT"
(
  cd "$REPO_ROOT"
  MOCK_PORT="$MOCK_PORT" go run ./docs/2026-03-23_benchmark_scripts/gateway-comparison/mock-backend
) >"$OUTPUT_DIR/mock-backend.log" 2>&1 &
mock_pid=$!
wait_for_url "http://127.0.0.1:$MOCK_PORT/health" "mock backend"

cat >"$env_file" <<EOF
GOMODEL_MASTER_KEY=release-semantic-master-key
PORT=$GATEWAY_PORT
ADMIN_ENDPOINTS_ENABLED=true
ADMIN_UI_ENABLED=false
STORAGE_TYPE=sqlite
SQLITE_PATH=$OUTPUT_DIR/release-semantic.sqlite
GOMODEL_CACHE_DIR=$OUTPUT_DIR/model-cache
LOGGING_ENABLED=true
LOGGING_FLUSH_INTERVAL=1
LOGGING_ONLY_MODEL_INTERACTIONS=true
USAGE_ENABLED=true
USAGE_FLUSH_INTERVAL=1
OPENAI_API_KEY=sk-release-semantic
OPENAI_BASE_URL=http://127.0.0.1:$MOCK_PORT/v1
SEMANTIC_CACHE_ENABLED=true
SEMANTIC_CACHE_THRESHOLD=0.99
SEMANTIC_CACHE_TTL=3600
SEMANTIC_CACHE_MAX_CONV_MESSAGES=5
SEMANTIC_CACHE_EMBEDDER_PROVIDER=openai
SEMANTIC_CACHE_EMBEDDER_MODEL=text-embedding-3-small
SEMANTIC_CACHE_VECTOR_STORE_TYPE=qdrant
SEMANTIC_CACHE_QDRANT_URL=http://127.0.0.1:$QDRANT_PORT
SEMANTIC_CACHE_QDRANT_COLLECTION=gomodel_release_semantic_$safe_suffix
EOF

echo "Starting gomodel on $AUTH_BASE_URL"
(
  cd "$REPO_ROOT"
  go build -o "$gateway_bin" ./cmd/gomodel
)
(
  clear_gomodel_env
  set -a
  source "$env_file"
  set +a
  cd "$OUTPUT_DIR"
  "$gateway_bin"
) >"$OUTPUT_DIR/gateway.log" 2>&1 &
gateway_pid=$!

wait_for_url "$AUTH_BASE_URL/health" "gomodel health"
wait_for_url "$AUTH_BASE_URL/v1/models" "gomodel models" "Authorization: Bearer release-semantic-master-key"

echo "Running release scenarios: $SCENARIOS"
(
  cd "$REPO_ROOT"
  RELEASE_E2E_ENV_FILE="$env_file" \
  AUTH_BASE_URL="$AUTH_BASE_URL" \
  tests/e2e/run-release-e2e.sh \
    --scenario "$SCENARIOS" \
    --qa-suffix "$QA_SUFFIX" \
    --output-dir "$runner_output_dir"
)

echo "Scenario logs: $runner_output_dir"
