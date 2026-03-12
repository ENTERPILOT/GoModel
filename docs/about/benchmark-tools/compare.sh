#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${ENV_FILE:-${ROOT_DIR}/../gomodel/.env}"
STAMP="$(date +%Y%m%d-%H%M%S)"
RESULTS_DIR="${RESULTS_DIR:-${ROOT_DIR}/benchmark-results/${STAMP}}"
LOG_DIR="${RESULTS_DIR}/logs"

REQUESTS="${REQUESTS:-60}"
CONCURRENCIES="${CONCURRENCIES:-1 4 8}"
read -r -a CONCURRENCY_ARRAY <<<"${CONCURRENCIES}"
WARMUP_REQUESTS="${WARMUP_REQUESTS:-3}"
MAX_TOKENS="${MAX_TOKENS:-8}"
PROMPT="${PROMPT:-Reply with exactly: OK}"
REQUEST_TIMEOUT="${REQUEST_TIMEOUT:-45s}"
SAMPLE_EVERY="${SAMPLE_EVERY:-500ms}"
COOLDOWN_SECONDS="${COOLDOWN_SECONDS:-0}"
RUN_DIRECT_BASELINE="${RUN_DIRECT_BASELINE:-1}"

GOMODEL_PORT="${GOMODEL_PORT:-38080}"
LITELLM_PORT="${LITELLM_PORT:-34000}"

mkdir -p "${LOG_DIR}"

GOMODEL_PID=""
LITELLM_PID=""

cleanup() {
  if [[ -n "${GOMODEL_PID}" ]] && kill -0 "${GOMODEL_PID}" >/dev/null 2>&1; then
    kill "${GOMODEL_PID}" >/dev/null 2>&1 || true
    wait "${GOMODEL_PID}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${LITELLM_PID}" ]] && kill -0 "${LITELLM_PID}" >/dev/null 2>&1; then
    kill "${LITELLM_PID}" >/dev/null 2>&1 || true
    wait "${LITELLM_PID}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

require_cmd go
require_cmd jq
require_cmd curl
require_cmd python3

if [[ ! -f "${ENV_FILE}" ]]; then
  echo "env file not found: ${ENV_FILE}" >&2
  exit 1
fi

set -a
source "${ENV_FILE}"
set +a

if [[ -z "${GROQ_API_KEY:-}" ]]; then
  echo "GROQ_API_KEY is required in ${ENV_FILE}" >&2
  exit 1
fi

# Ensure fairness: compare with a single provider enabled on both gateways.
unset OPENAI_API_KEY ANTHROPIC_API_KEY GEMINI_API_KEY XAI_API_KEY OLLAMA_BASE_URL

if ! python3 -m pip show litellm >/dev/null 2>&1; then
  echo "litellm is not installed. Installing litellm[proxy]..." >&2
  python3 -m pip install "litellm[proxy]==1.82.0"
fi
require_cmd litellm

PREFERRED_MODELS=(
  "llama-3.1-8b-instant"
  "openai/gpt-oss-20b"
  "qwen/qwen3-32b"
  "llama-3.3-70b-versatile"
)

discover_model() {
  local models
  models="$(curl -fsS https://api.groq.com/openai/v1/models \
    -H "Authorization: Bearer ${GROQ_API_KEY}" | jq -r '.data[].id')"

  if [[ -z "${models}" ]]; then
    echo "failed to discover Groq models" >&2
    exit 1
  fi

  if [[ -n "${MODEL:-}" ]] && grep -Fxq "${MODEL}" <<<"${models}"; then
    echo "${MODEL}"
    return
  fi

  local preferred
  for preferred in "${PREFERRED_MODELS[@]}"; do
    if grep -Fxq "${preferred}" <<<"${models}"; then
      echo "${preferred}"
      return
    fi
  done

  echo "${models}" | head -n 1
}

MODEL="$(discover_model)"

echo "Using model: ${MODEL}"
echo "Results dir: ${RESULTS_DIR}"

mkdir -p "${ROOT_DIR}/bin"
(cd "${ROOT_DIR}" && go build -o bin/gomodel ./cmd/gomodel)
(cd "${ROOT_DIR}" && go build -o bin/bench ./cmd/bench)

LITELLM_CONFIG="${RESULTS_DIR}/litellm_config.yaml"
cat >"${LITELLM_CONFIG}" <<EOF
model_list:
  - model_name: "${MODEL}"
    litellm_params:
      model: "groq/${MODEL}"
      api_key: os.environ/GROQ_API_KEY
EOF

wait_for_models() {
  local base_url="$1"
  local label="$2"
  local i
  for i in $(seq 1 90); do
    if curl -fsS "${base_url}/v1/models" >/dev/null 2>&1; then
      return
    fi
    sleep 1
  done
  echo "${label} did not become ready in time" >&2
  exit 1
}

start_gomodel() {
  local log_file="${LOG_DIR}/gomodel.log"
  (
    cd "${ROOT_DIR}"
    exec env \
      PORT="${GOMODEL_PORT}" \
      GOMODEL_MASTER_KEY="" \
      LOGGING_ENABLED=false \
      METRICS_ENABLED=false \
      ./bin/gomodel >"${log_file}" 2>&1
  ) &
  GOMODEL_PID=$!
  wait_for_models "http://127.0.0.1:${GOMODEL_PORT}" "GoModel"
  sleep 2
}

start_litellm() {
  local log_file="${LOG_DIR}/litellm.log"
  (
    cd "${ROOT_DIR}"
    exec env \
      LITELLM_LOG=ERROR \
      litellm --config "${LITELLM_CONFIG}" --host 127.0.0.1 --port "${LITELLM_PORT}" >"${log_file}" 2>&1
  ) &
  LITELLM_PID=$!
  wait_for_models "http://127.0.0.1:${LITELLM_PORT}" "LiteLLM"
  sleep 2
}

run_bench_matrix() {
  local gateway="$1"
  local base_url="$2"
  local pid="$3"

  echo "Warmup ${gateway}..."
  "${ROOT_DIR}/bin/bench" \
    -gateway "${gateway}" \
    -base-url "${base_url}" \
    -model "${MODEL}" \
    -requests "${WARMUP_REQUESTS}" \
    -concurrency 1 \
    -max-tokens "${MAX_TOKENS}" \
    -prompt "${PROMPT}" \
    -request-timeout "${REQUEST_TIMEOUT}" \
    -sample-every "${SAMPLE_EVERY}" \
    >/dev/null

  local c idx
  for idx in "${!CONCURRENCY_ARRAY[@]}"; do
    c="${CONCURRENCY_ARRAY[$idx]}"
    local out_file="${RESULTS_DIR}/${gateway}_c${c}.json"
    echo "Benchmark ${gateway} c=${c} req=${REQUESTS}"
    "${ROOT_DIR}/bin/bench" \
      -gateway "${gateway}" \
      -base-url "${base_url}" \
      -model "${MODEL}" \
      -requests "${REQUESTS}" \
      -concurrency "${c}" \
      -max-tokens "${MAX_TOKENS}" \
      -prompt "${PROMPT}" \
      -request-timeout "${REQUEST_TIMEOUT}" \
      -sample-every "${SAMPLE_EVERY}" \
      -pid "${pid}" \
      -output "${out_file}" \
      >/dev/null

    if [[ "${COOLDOWN_SECONDS}" -gt 0 ]] && [[ "${idx}" -lt "$(( ${#CONCURRENCY_ARRAY[@]} - 1 ))" ]]; then
      echo "Cooldown ${gateway}: ${COOLDOWN_SECONDS}s"
      sleep "${COOLDOWN_SECONDS}"
    fi
  done
}

echo "Starting GoModel..."
start_gomodel
run_bench_matrix "gomodel" "http://127.0.0.1:${GOMODEL_PORT}" "${GOMODEL_PID}"
kill "${GOMODEL_PID}" >/dev/null 2>&1 || true
wait "${GOMODEL_PID}" >/dev/null 2>&1 || true
GOMODEL_PID=""

echo "Starting LiteLLM..."
start_litellm
run_bench_matrix "litellm" "http://127.0.0.1:${LITELLM_PORT}" "${LITELLM_PID}"
kill "${LITELLM_PID}" >/dev/null 2>&1 || true
wait "${LITELLM_PID}" >/dev/null 2>&1 || true
LITELLM_PID=""

if [[ "${RUN_DIRECT_BASELINE}" == "1" ]]; then
  local_c=1
  for local_c in "${CONCURRENCY_ARRAY[@]}"; do
    out_file="${RESULTS_DIR}/direct_groq_c${local_c}.json"
    echo "Benchmark direct Groq c=${local_c} req=${REQUESTS}"
    "${ROOT_DIR}/bin/bench" \
      -gateway "direct-groq" \
      -base-url "https://api.groq.com/openai" \
      -model "${MODEL}" \
      -requests "${REQUESTS}" \
      -concurrency "${local_c}" \
      -max-tokens "${MAX_TOKENS}" \
      -prompt "${PROMPT}" \
      -request-timeout "${REQUEST_TIMEOUT}" \
      -sample-every "${SAMPLE_EVERY}" \
      -api-key "${GROQ_API_KEY}" \
      -output "${out_file}" \
      >/dev/null
  done
fi

{
  echo "# GoModel vs LiteLLM Benchmark"
  echo
  echo "- Timestamp: $(date -u +"%Y-%m-%dT%H:%M:%SZ")"
  echo "- Model: \`${MODEL}\`"
  echo "- Requests per run: ${REQUESTS}"
  echo "- Concurrency set: \`${CONCURRENCIES}\`"
  echo "- Prompt: \`${PROMPT}\`"
  echo "- Max tokens: ${MAX_TOKENS}"
  echo
  echo "## Results"
  echo
  echo "| Gateway | C | Success | Failure | Error % | Req/s | p50 ms | p95 ms | p99 ms | CPU avg % | CPU max % | RSS avg MB | RSS max MB |"
  echo "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|"

  for file in "${RESULTS_DIR}"/*.json; do
    jq -r '
      [
        .gateway,
        .concurrency,
        .success,
        .failures,
        (.error_rate * 100),
        .req_per_sec,
        .latency_ms.p50,
        .latency_ms.p95,
        .latency_ms.p99,
        .process.cpu_avg,
        .process.cpu_max,
        .process.rss_avg_mb,
        .process.rss_max_mb
      ] | @tsv
    ' "${file}"
  done | sort -k1,1 -k2,2n | while IFS=$'\t' read -r gateway c success failures error_rate reqs p50 p95 p99 cpu_avg cpu_max rss_avg rss_max; do
    printf '| %s | %s | %s | %s | %.2f | %.2f | %.1f | %.1f | %.1f | %.2f | %.2f | %.1f | %.1f |\n' \
      "${gateway}" "${c}" "${success}" "${failures}" \
      "${error_rate}" "${reqs}" "${p50}" "${p95}" "${p99}" \
      "${cpu_avg}" "${cpu_max}" "${rss_avg}" "${rss_max}"
  done
} >"${RESULTS_DIR}/REPORT.md"

echo
echo "Benchmark completed."
echo "Artifacts:"
echo "- JSON: ${RESULTS_DIR}/*.json"
echo "- Report: ${RESULTS_DIR}/REPORT.md"
echo "- Logs: ${LOG_DIR}"
