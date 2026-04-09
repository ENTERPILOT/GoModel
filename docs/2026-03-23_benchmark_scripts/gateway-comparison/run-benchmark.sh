#!/usr/bin/env bash
set -euo pipefail

# ── Configuration ──────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
RESULTS_DIR="$SCRIPT_DIR/results"
MOCK_PORT=9999
GOMODEL_PORT=8081
LITELLM_PORT=8082

N_REQUESTS=1000         # total requests per test
CONCURRENCY=50          # concurrent connections

rm -rf "$RESULTS_DIR"
mkdir -p "$RESULTS_DIR"

# ── Helpers ────────────────────────────────────────────────────────
log()  { printf "\n\033[1;34m>>> %s\033[0m\n" "$1"; }
err()  { printf "\033[1;31mERROR: %s\033[0m\n" "$1" >&2; }

timestamp_ms() {
    python3 - <<'PY'
import time
print(int(time.time() * 1000))
PY
}

parse_hey_percentile() {
    local hey_file=$1 percentile=$2
    awk -v pct="${percentile}%%" '$1 == pct && $2 == "in" { print $3; exit }' "$hey_file"
}

GW_PID=""
MON_PID=""

cleanup() {
    log "Cleaning up..."
    kill "$MOCK_PID"   2>/dev/null || true
    [[ -n "$GW_PID" ]]  && kill "$GW_PID"  2>/dev/null || true
    [[ -n "$MON_PID" ]] && kill "$MON_PID" 2>/dev/null || true
    pkill -f "litellm.*${LITELLM_PORT}" 2>/dev/null || true
    rm -f /tmp/gomodel-bench.db
    rm -rf /tmp/gomodel-bench-cache
}
trap cleanup EXIT

wait_for_server() {
    local url="$1" name="$2" timeout="${3:-15}"
    for i in $(seq 1 "$timeout"); do
        if curl -sf "$url" >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    err "$name did not start within ${timeout}s"
    return 1
}

# Resource monitor: samples RSS (KB) and CPU% every 0.5s
start_monitor() {
    local pid=$1 outfile=$2
    (
        echo "timestamp_ms,rss_kb,cpu_pct" > "$outfile"
        while kill -0 "$pid" 2>/dev/null; do
            read rss cpu <<< "$(ps -o rss=,pcpu= -p "$pid" 2>/dev/null || echo "0 0")"
            echo "$(timestamp_ms),${rss// /},${cpu// /}" >> "$outfile"
            sleep 0.5
        done
    ) &
    MON_PID=$!
}

stop_monitor() {
    [[ -n "$MON_PID" ]] && kill "$MON_PID" 2>/dev/null || true
    [[ -n "$MON_PID" ]] && wait "$MON_PID" 2>/dev/null || true
    MON_PID=""
}

summarize_resources() {
    local file=$1
    if [[ ! -f "$file" ]] || [[ $(wc -l < "$file") -le 1 ]]; then
        echo '{"peak_rss_mb":0,"avg_rss_mb":0,"avg_cpu_pct":0}'
        return
    fi
    awk -F, 'NR>1 && $2>0 {
        sum_rss+=$2; sum_cpu+=$3; count++;
        if($2>max_rss) max_rss=$2
    } END {
        if(count>0)
            printf "{\"peak_rss_mb\":%.1f,\"avg_rss_mb\":%.1f,\"avg_cpu_pct\":%.1f}\n",
                max_rss/1024, sum_rss/count/1024, sum_cpu/count
        else
            print "{\"peak_rss_mb\":0,\"avg_rss_mb\":0,\"avg_cpu_pct\":0}"
    }' "$file"
}

# ── Generic benchmark functions ────────────────────────────────────
run_nonstream_bench() {
    local name=$1 port=$2 endpoint=$3 gw_pid=$4 model=$5
    local url="http://localhost:${port}${endpoint}"

    start_monitor "$gw_pid" "$RESULTS_DIR/${name}_resources.csv"

    log "  Non-streaming: $url (model=$model)"
    local body
    if [[ "$endpoint" == *responses* ]]; then
        body="{\"model\":\"${model}\",\"stream\":false,\"input\":\"Say hello for a benchmark test.\"}"
    else
        body="{\"model\":\"${model}\",\"stream\":false,\"messages\":[{\"role\":\"user\",\"content\":\"Say hello for a benchmark test.\"}]}"
    fi

    hey -n "$N_REQUESTS" -c "$CONCURRENCY" \
        -m POST \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer sk-bench-test-key" \
        -d "$body" \
        "$url" > "$RESULTS_DIR/${name}_hey.txt" 2>&1

    stop_monitor

    local hey_file="$RESULTS_DIR/${name}_hey.txt"
    local rps avg p50 p95 p99
    rps=$(grep "Requests/sec:" "$hey_file" | awk '{print $2}' || echo "N/A")
    avg=$(grep "Average:" "$hey_file" | head -1 | awk '{print $2}' || echo "N/A")
    p50=$(parse_hey_percentile "$hey_file" "50" || echo "N/A")
    p95=$(parse_hey_percentile "$hey_file" "95" || echo "N/A")
    p99=$(parse_hey_percentile "$hey_file" "99" || echo "N/A")

    local resources
    resources=$(summarize_resources "$RESULTS_DIR/${name}_resources.csv")

    echo "{\"name\":\"$name\",\"rps\":\"$rps\",\"avg\":\"$avg\",\"p50\":\"$p50\",\"p95\":\"$p95\",\"p99\":\"$p99\",\"resources\":$resources}" > "$RESULTS_DIR/${name}_summary.json"

    echo "    RPS: $rps | Avg: $avg | p50: $p50 | p95: $p95 | p99: $p99"
    echo "    Resources: $resources"
}

run_stream_bench() {
    local name=$1 port=$2 endpoint_type=$3 gw_pid=$4 model=$5
    local url
    if [[ "$endpoint_type" == "responses" ]]; then
        url="http://localhost:${port}/v1/responses"
    else
        url="http://localhost:${port}/v1/chat/completions"
    fi

    start_monitor "$gw_pid" "$RESULTS_DIR/${name}_resources.csv"

    log "  Streaming: $url (model=$model)"
    "$SCRIPT_DIR/stream-bench/stream-bench" \
        -url "$url" \
        -n "$N_REQUESTS" \
        -c "$CONCURRENCY" \
        -endpoint "$endpoint_type" \
        -model "$model" \
        -json "$RESULTS_DIR/${name}_stream.json" \
        2>&1 | tee "$RESULTS_DIR/${name}_stream.txt"

    stop_monitor

    local resources
    resources=$(summarize_resources "$RESULTS_DIR/${name}_resources.csv")
    echo "    Resources: $resources"

    if [[ -f "$RESULTS_DIR/${name}_stream.json" ]]; then
        python3 -c "
import json
with open('$RESULTS_DIR/${name}_stream.json') as f:
    d = json.load(f)
d['resources'] = $resources
with open('$RESULTS_DIR/${name}_stream.json', 'w') as f:
    json.dump(d, f, indent=2)
" 2>/dev/null || true
    fi
}

# ── Skip build if binaries already exist ───────────────────────────
if [[ ! -f "$SCRIPT_DIR/mock-backend/mock-server" ]]; then
    log "Building mock backend..."
    (cd "$SCRIPT_DIR/mock-backend" && go build -o mock-server .)
fi

if [[ ! -f "$SCRIPT_DIR/stream-bench/stream-bench" ]]; then
    log "Building streaming benchmark tool..."
    (cd "$SCRIPT_DIR/stream-bench" && go build -o stream-bench .)
fi

if [[ ! -f "$SCRIPT_DIR/gomodel-bin" ]]; then
    log "Building GoModel..."
    (cd "$PROJECT_ROOT" && go build -o "$SCRIPT_DIR/gomodel-bin" ./cmd/gomodel)
fi

# ── Start mock backend ─────────────────────────────────────────────
log "Starting mock backend on :${MOCK_PORT}..."
MOCK_PORT=$MOCK_PORT "$SCRIPT_DIR/mock-backend/mock-server" &
MOCK_PID=$!
wait_for_server "http://localhost:${MOCK_PORT}/health" "Mock backend"

# ══════════════════════════════════════════════════════════════════
# BASELINE: Direct to mock (no gateway)
# ══════════════════════════════════════════════════════════════════
log "=== BASELINE (direct to mock, no gateway) ==="

hey -n "$N_REQUESTS" -c "$CONCURRENCY" \
    -m POST \
    -H "Content-Type: application/json" \
    -d '{"model":"gpt-4o-mini","stream":false,"messages":[{"role":"user","content":"Say hello for a benchmark test."}]}' \
    "http://localhost:${MOCK_PORT}/v1/chat/completions" > "$RESULTS_DIR/baseline_chat_nonstream_hey.txt" 2>&1

echo "  Baseline non-stream:"
grep "Requests/sec:" "$RESULTS_DIR/baseline_chat_nonstream_hey.txt"
awk '/Latency distribution/,0' "$RESULTS_DIR/baseline_chat_nonstream_hey.txt" | head -8

"$SCRIPT_DIR/stream-bench/stream-bench" \
    -url "http://localhost:${MOCK_PORT}/v1/chat/completions" \
    -n "$N_REQUESTS" -c "$CONCURRENCY" -endpoint chat -model "gpt-4o-mini" \
    -json "$RESULTS_DIR/baseline_chat_stream.json" \
    2>&1 | tee "$RESULTS_DIR/baseline_chat_stream.txt"

# ══════════════════════════════════════════════════════════════════
# GATEWAY 1: GoModel
# ══════════════════════════════════════════════════════════════════
log "=== BENCHMARKING GoModel ==="

OPENAI_API_KEY="sk-bench-test-key" \
OPENAI_BASE_URL="http://localhost:${MOCK_PORT}/v1" \
PORT=$GOMODEL_PORT \
GOMODEL_MASTER_KEY="" \
LOGGING_ENABLED=false \
USAGE_ENABLED=false \
STORAGE_TYPE=sqlite \
SQLITE_PATH="/tmp/gomodel-bench.db" \
GOMODEL_CACHE_DIR="/tmp/gomodel-bench-cache" \
ADMIN_ENDPOINTS_ENABLED=false \
ADMIN_UI_ENABLED=false \
SWAGGER_ENABLED=false \
"$SCRIPT_DIR/gomodel-bin" > "$RESULTS_DIR/gomodel_server.log" 2>&1 &
GW_PID=$!
wait_for_server "http://localhost:${GOMODEL_PORT}/health" "GoModel"
sleep 1

# Warm up
hey -n 100 -c 10 -m POST \
    -H "Content-Type: application/json" \
    -d '{"model":"gpt-4o-mini","stream":false,"messages":[{"role":"user","content":"warmup"}]}' \
    "http://localhost:${GOMODEL_PORT}/v1/chat/completions" >/dev/null 2>&1

run_nonstream_bench "gomodel_chat_nonstream" $GOMODEL_PORT "/v1/chat/completions" $GW_PID "gpt-4o-mini"
run_stream_bench    "gomodel_chat_stream"    $GOMODEL_PORT "chat"                  $GW_PID "gpt-4o-mini"
run_nonstream_bench "gomodel_resp_nonstream"  $GOMODEL_PORT "/v1/responses"         $GW_PID "gpt-4o-mini"
run_stream_bench    "gomodel_resp_stream"     $GOMODEL_PORT "responses"             $GW_PID "gpt-4o-mini"

kill $GW_PID 2>/dev/null || true; wait $GW_PID 2>/dev/null || true
GW_PID=""

# ══════════════════════════════════════════════════════════════════
# GATEWAY 2: LiteLLM
# ══════════════════════════════════════════════════════════════════
log "=== BENCHMARKING LiteLLM ==="

litellm --config "$SCRIPT_DIR/configs/litellm-config.yaml" \
    --port $LITELLM_PORT \
    --num_workers 4 \
    > "$RESULTS_DIR/litellm_server.log" 2>&1 &
GW_PID=$!
wait_for_server "http://localhost:${LITELLM_PORT}/health/liveliness" "LiteLLM" 30
sleep 3

# Warm up
hey -n 50 -c 5 -m POST \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer sk-bench-test-key" \
    -d '{"model":"gpt-4o-mini","stream":false,"messages":[{"role":"user","content":"warmup"}]}' \
    "http://localhost:${LITELLM_PORT}/v1/chat/completions" >/dev/null 2>&1

run_nonstream_bench "litellm_chat_nonstream" $LITELLM_PORT "/v1/chat/completions" $GW_PID "gpt-4o-mini"
run_stream_bench    "litellm_chat_stream"    $LITELLM_PORT "chat"                  $GW_PID "gpt-4o-mini"
run_nonstream_bench "litellm_resp_nonstream"  $LITELLM_PORT "/v1/responses"         $GW_PID "gpt-4o-mini"
run_stream_bench    "litellm_resp_stream"     $LITELLM_PORT "responses"             $GW_PID "gpt-4o-mini"

kill $GW_PID 2>/dev/null || true; wait $GW_PID 2>/dev/null || true
pkill -f "litellm.*${LITELLM_PORT}" 2>/dev/null || true
GW_PID=""

# ══════════════════════════════════════════════════════════════════
log "=== ALL BENCHMARKS COMPLETE ==="
log "Results in: $RESULTS_DIR/"
ls -la "$RESULTS_DIR/"
