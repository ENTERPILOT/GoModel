#!/usr/bin/env bash
# Runs the gateway latency + capacity + resource benchmark on the local docker host.
#
# Designed to run ON the provisioned EC2 instance (invoked by ../run.sh over
# SSH), but it works on any docker host. Two passes:
#
#   Pass A — latency: REPEATS independent trials. Each trial brings up exactly one
#            gateway at a time (no contention), warms it, drives all six request
#            variants, tears it down. Gateway *order is randomized every trial* so
#            no gateway is pinned to the most-favorable slot; results land in
#            results/run<k>/. Aggregation (median + spread across trials) is left
#            to scripts/summarize.py.
#
#   Pass B — capacity + footprint (once): per gateway, measure cold-start latency,
#            image size, a throughput-vs-concurrency sweep (sustained req/s at each
#            concurrency level — true capacity, not latency-coupled), and CPU/mem
#            under sustained load.
#
# Results are written as JSON to ./results/ for the orchestrator to collect.
#
# NOTE: deliberately NOT `set -e`. This is a resilient benchmark harness — a
# single flaky docker/compose/curl on one variant must not abort the whole run;
# it should skip to the next variant and still reach the final meta.json sentinel
# the orchestrator polls for. Failures are visible in each variant's ok/failed.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"
RESULTS_DIR="$SCRIPT_DIR/results"
COMPOSE=(docker compose -p bench)

# Load knobs. Defaults target a non-burstable box (c7i.large); see ../run.sh.
N="${N:-20000}"          # requests per variant (large enough for a stable p99)
C="${C:-10}"             # reference concurrency for the latency pass
REPEATS="${REPEATS:-5}"  # independent latency trials (median + spread)
WARMUP="${WARMUP:-100}"  # global chat warmup after a gateway starts (process/connection init)
WARMUP_VARIANT="${WARMUP_VARIANT:-30}"  # per-variant warmup (per-dialect lazy-import cold start)
RESOURCE_SECONDS="${RESOURCE_SECONDS:-15}"  # sustained-load window for CPU/mem sampling
REST_SECONDS="${REST_SECONDS:-5}"           # settle gap between targets (cooldown)
# Per-variant wall cap: fast variants hit full N in seconds; this only bites the
# idle-bound streaming variants (e.g. Bifrost streams over a non-native backend
# fall back to the 1.5s idle timeout → ~7 req/s, which would take ~50 min for N).
MAX_VARIANT_SECONDS="${MAX_VARIANT_SECONDS:-60}"
SWEEP_CONCURRENCY="${SWEEP_CONCURRENCY:-1 2 4 8 16 32 64 128 256}"  # capacity-sweep points
SWEEP_DURATION="${SWEEP_DURATION:-8}"       # seconds of sustained load per sweep point
GATEWAYS="${GATEWAYS:-gomodel litellm portkey bifrost}"
# LiteLLM recommends one worker per CPU core; match the box so it isn't pinned to a
# single core while the Go gateways use all of them. Exported for docker-compose's
# ${LITELLM_NUM_WORKERS} substitution. (Per-variant warmup already warms each
# dialect; with >1 worker the warmup also spreads across workers.)
export LITELLM_NUM_WORKERS="${LITELLM_NUM_WORKERS:-$(nproc 2>/dev/null || echo 1)}"

AUTH="sk-bench-test-key"

log() { printf '\n\033[1;34m>>> %s\033[0m\n' "$*"; }

rm -rf "$RESULTS_DIR"; mkdir -p "$RESULTS_DIR"

# ── helpers ────────────────────────────────────────────────────────
# epoch as a float second (python3 is present on AL2023 + macOS; coarse fallback).
epoch() { python3 -c 'import time;print(time.time())' 2>/dev/null || date +%s; }

# shuffle a space-separated list; seed varies per call so trials differ in order.
shuffle() {
  printf '%s\n' $1 | awk -v seed="${2:-$RANDOM}" 'BEGIN{srand(seed)} {print rand()"\t"$0}' \
    | sort -k1,1n | cut -f2- | tr '\n' ' '
}

# svc:internal port  +  any extra loadgen headers (Portkey routing).
gw_port() { case "$1" in gomodel) echo 8080;; litellm) echo 4000;; portkey) echo 8787;; bifrost) echo 8089;; mock) echo 9999;; esac; }

# gw_headers fills the global HDRS array with loadgen -H args for the target.
HDRS=()
gw_headers() {
  HDRS=()
  case "$1" in
    portkey)
      HDRS=(-H 'x-portkey-provider: openai' -H 'x-portkey-custom-host: http://mock:9999/v1')
      ;;
  esac
}

# Model name per gateway. Bifrost routes by an explicit "provider/model" prefix.
gw_model() { case "$1" in bifrost) echo "openai/gpt-4o-mini";; *) echo "gpt-4o-mini";; esac; }

# Path per (gateway, dialect). Bifrost exposes Anthropic Messages under /anthropic/v1/messages.
gw_path() {  # target dialect default_path
  if [[ "$1" == "bifrost" && "$2" == "messages" ]]; then echo "/anthropic/v1/messages"; else echo "$3"; fi
}

# The six benchmark variants: dialect | mode | path
VARIANTS=(
  "chat|nonstream|/v1/chat/completions"
  "chat|stream|/v1/chat/completions"
  "responses|nonstream|/v1/responses"
  "responses|stream|/v1/responses"
  "messages|nonstream|/v1/messages"
  "messages|stream|/v1/messages"
)

BENCH_TOOLS_IMAGE="${BENCH_TOOLS_IMAGE:-bench-tools:local}"

# loadgen runs in a throwaway container on the shared benchnet network so it can
# reach gateways/mock by service name. JSON summary comes back on stdout.
run_variant() {
  local target="$1" svc="$2" spec="$3" outfile="$4"
  local dialect mode path
  IFS='|' read -r dialect mode path <<< "$spec"
  path="$(gw_path "$target" "$dialect" "$path")"
  local port; port="$(gw_port "$svc")"
  local url="http://${svc}:${port}${path}"

  local base=(-url "$url" -dialect "$dialect" -model "$(gw_model "$target")" -c "$C" -auth "$AUTH" -json -)
  [[ "$mode" == "stream" ]] && base+=(-stream)
  [[ "$MAX_VARIANT_SECONDS" -gt 0 ]] && base+=(-max-wall "${MAX_VARIANT_SECONDS}s")
  gw_headers "$target"
  if [[ ${#HDRS[@]} -gt 0 ]]; then base+=("${HDRS[@]}"); fi

  # Per-variant warmup: warm THIS exact dialect+mode before measuring. Python
  # gateways (LiteLLM) lazily import per-dialect translation modules on first use,
  # so a chat-only warmup leaves responses/messages cold and inflates their tails.
  if [[ "$WARMUP_VARIANT" -gt 0 ]]; then
    docker run --rm --network benchnet "$BENCH_TOOLS_IMAGE" /loadgen \
      "${base[@]}" -n "$WARMUP_VARIANT" >/dev/null 2>&1 || true
  fi

  docker run --rm --network benchnet "$BENCH_TOOLS_IMAGE" /loadgen \
    "${base[@]}" -n "$N" > "$outfile" 2>/dev/null || true

  # `|| true`: a single empty/missing summary must never abort the whole run.
  local ok fail
  ok="$(grep -o '"ok": *[0-9]*' "$outfile" 2>/dev/null | head -1 | grep -o '[0-9]*' || true)"
  fail="$(grep -o '"failed": *[0-9]*' "$outfile" 2>/dev/null | head -1 | grep -o '[0-9]*' || true)"
  printf '    %-8s %-10s %-9s ok=%-6s failed=%s\n' "$target" "$dialect" "$mode" "${ok:-?}" "${fail:-?}"
}

# run_sweep drives a throughput-vs-concurrency sweep (chat, non-stream) so we can
# read each gateway's saturation point — sustained req/s at each concurrency, via
# loadgen's time-boxed mode (not the latency pass's fixed-N, latency-coupled rps).
run_sweep() {
  local label="$1" svc="$2" port; port="$(gw_port "$svc")"
  local url="http://${svc}:${port}/v1/chat/completions"
  mkdir -p "$RESULTS_DIR/sweep"
  gw_headers "$label"; local hdr=(); [[ ${#HDRS[@]} -gt 0 ]] && hdr=("${HDRS[@]}")
  for cc in $SWEEP_CONCURRENCY; do
    local args=(-url "$url" -dialect chat -model "$(gw_model "$label")" -c "$cc" -duration "${SWEEP_DURATION}s" -auth "$AUTH" -json -)
    [[ ${#hdr[@]} -gt 0 ]] && args+=("${hdr[@]}")
    docker run --rm --network benchnet "$BENCH_TOOLS_IMAGE" /loadgen "${args[@]}" \
      > "$RESULTS_DIR/sweep/${label}_c${cc}.json" 2>/dev/null || true
    local rps; rps="$(grep -o '"rps": *[0-9.]*' "$RESULTS_DIR/sweep/${label}_c${cc}.json" 2>/dev/null | head -1 | grep -o '[0-9.]*' || true)"
    printf '    sweep %-8s c=%-4s rps=%s\n' "$label" "$cc" "${rps:-?}"
  done
}

# awk program that normalizes a docker-stats MemUsage field to MiB, then prints
# "mem_mb,cpu_pct".
STAT_AWK='
function tomib(s,  v){ v=s; gsub(/[^0-9.]/,"",v); v=v+0;
  if (s ~ /GiB|GB/) return v*1024;
  if (s ~ /MiB|MB/) return v;
  if (s ~ /KiB|kB/) return v/1024;
  if (s ~ /[0-9]B/) return v/1048576;
  return v }
{ split($0,a,";"); mem=a[1]; sub(/ ?\/.*/,"",mem);
  cpu=a[2]; gsub(/[^0-9.]/,"",cpu);
  m=tomib(mem); if (m>0) printf "%.2f,%s\n", m, cpu }'

SAMPLER_PID=""
start_sampler() {
  local cname="$1" csv="$2"
  echo "mem_mb,cpu_pct" > "$csv"
  (
    while docker ps --format '{{.Names}}' | grep -q "^${cname}$"; do
      docker stats --no-stream --format '{{.MemUsage}};{{.CPUPerc}}' "$cname" 2>/dev/null \
        | awk "$STAT_AWK" >> "$csv" || true
    done
  ) &
  SAMPLER_PID=$!
}

# Drive sustained chat load at a gateway for ~RESOURCE_SECONDS so the sampler
# captures the container under genuine pressure. Writes loadgen's summary to
# $3 so the achieved rps shares the exact window the CPU sample covers (lets
# summarize.py compute a self-consistent rps-per-CPU% efficiency).
sustained_load() {
  local gw="$1" hostport="$2" outfile="$3"
  local args=(-url "http://${gw}:${hostport}/v1/chat/completions" -dialect chat -model "$(gw_model "$gw")" -duration "${RESOURCE_SECONDS}s" -c "$C" -auth "$AUTH" -json -)
  gw_headers "$gw"; if [[ ${#HDRS[@]} -gt 0 ]]; then args+=("${HDRS[@]}"); fi
  docker run --rm --network benchnet "$BENCH_TOOLS_IMAGE" /loadgen "${args[@]}" > "$outfile" 2>/dev/null || true
}

stop_sampler() {
  [[ -n "$SAMPLER_PID" ]] && kill "$SAMPLER_PID" 2>/dev/null || true
  [[ -n "$SAMPLER_PID" ]] && wait "$SAMPLER_PID" 2>/dev/null || true
  SAMPLER_PID=""
}

summarize_resources() {  # csv -> json {peak_mem_mb, avg_mem_mb, avg_cpu_pct, samples}
  [[ -f "$1" ]] || { printf '{"peak_mem_mb":0,"avg_mem_mb":0,"avg_cpu_pct":0,"samples":0}'; return 0; }
  awk -F, 'NR>1 && $1>0 { n++; s_mem+=$1; s_cpu+=$2; if($1>peak)peak=$1 }
    END {
      if(n>0) printf "{\"peak_mem_mb\":%.1f,\"avg_mem_mb\":%.1f,\"avg_cpu_pct\":%.1f,\"samples\":%d}", peak, s_mem/n, s_cpu/n, n;
      else printf "{\"peak_mem_mb\":0,\"avg_mem_mb\":0,\"avg_cpu_pct\":0,\"samples\":0}"
    }' "$1"
}

record_image() {  # gateway image_ref -> results/<gw>_image.json
  local gw="$1" ref="$2"
  local size digest compressed
  size="$(docker image inspect "$ref" --format '{{.Size}}' 2>/dev/null || echo 0)"
  digest="$(docker image inspect "$ref" --format '{{if .RepoDigests}}{{index .RepoDigests 0}}{{else}}{{.Id}}{{end}}' 2>/dev/null || echo unknown)"
  # Compressed size = what you actually pull/store: gzip the saved image (uniform
  # across the locally-built gomodel image and the pulled competitor images).
  compressed="$(docker save "$ref" 2>/dev/null | gzip -c | wc -c | tr -d ' ' || echo 0)"
  printf '{"gateway":"%s","image":"%s","size_bytes":%s,"size_mb":%.1f,"compressed_bytes":%s,"compressed_mb":%.1f,"digest":"%s"}\n' \
    "$gw" "$ref" "${size:-0}" "$(awk "BEGIN{print ${size:-0}/1048576}")" \
    "${compressed:-0}" "$(awk "BEGIN{print ${compressed:-0}/1048576}")" "$digest" \
    > "$RESULTS_DIR/${gw}_image.json"
}

wait_ready() {  # gateway host_port -> poll a real chat request until HTTP 200
  local target="$1" hostport="$2" tries="${3:-60}"
  gw_headers "$target"
  local hdr=(); if [[ ${#HDRS[@]} -gt 0 ]]; then hdr=("${HDRS[@]}"); fi
  local code
  for ((i=0;i<tries;i++)); do
    code="$(curl -s -o /dev/null -w '%{http_code}' -m 5 -X POST \
      "http://localhost:${hostport}/v1/chat/completions" \
      -H 'Content-Type: application/json' -H "Authorization: Bearer $AUTH" ${hdr[@]+"${hdr[@]}"} \
      -d "{\"model\":\"$(gw_model "$target")\",\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}]}" 2>/dev/null || echo 000)"
    [[ "$code" == "200" ]] && return 0
    sleep 2
  done
  echo "  WARN: $target did not return 200 within $((tries*2))s (last code: ${code:-?})" >&2
  return 1
}

# bring a gateway up and time cold-start latency (compose up -> first HTTP 200).
# Leaves the gateway running. Writes results/<gw>_startup.json.
measure_startup() {
  local gw="$1" hostport; hostport="$(gw_port "$gw")"
  local t0 t1 code ready=0
  gw_headers "$gw"; local hdr=(); [[ ${#HDRS[@]} -gt 0 ]] && hdr=("${HDRS[@]}")
  t0="$(epoch)"
  GOMODEL_IMAGE="${GOMODEL_IMAGE:-gomodel-bench:local}" \
    "${COMPOSE[@]}" --profile "$gw" up -d "$gw" >/dev/null 2>&1 || true
  for ((i=0;i<600;i++)); do  # up to ~120s, 0.2s resolution
    code="$(curl -s -o /dev/null -w '%{http_code}' -m 5 -X POST \
      "http://localhost:${hostport}/v1/chat/completions" \
      -H 'Content-Type: application/json' -H "Authorization: Bearer $AUTH" ${hdr[@]+"${hdr[@]}"} \
      -d "{\"model\":\"$(gw_model "$gw")\",\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}]}" 2>/dev/null || echo 000)"
    [[ "$code" == "200" ]] && { ready=1; break; }
    sleep 0.2
  done
  t1="$(epoch)"
  local elapsed; elapsed="$(awk -v a="$t0" -v b="$t1" 'BEGIN{printf "%.3f", b-a}')"
  printf '{"gateway":"%s","startup_s":%s,"ready":%s}\n' "$gw" "$elapsed" "$ready" \
    > "$RESULTS_DIR/${gw}_startup.json"
  echo "    startup: ${gw} ${elapsed}s (ready=$ready)"
}

warmup_gateway() {
  local gw="$1" hostport; hostport="$(gw_port "$gw")"
  local warm_args=(-url "http://${gw}:${hostport}/v1/chat/completions" -dialect chat -model "$(gw_model "$gw")" -n "$WARMUP" -c "$C" -auth "$AUTH" -json -)
  gw_headers "$gw"; if [[ ${#HDRS[@]} -gt 0 ]]; then warm_args+=("${HDRS[@]}"); fi
  docker run --rm --network benchnet "$BENCH_TOOLS_IMAGE" /loadgen "${warm_args[@]}" >/dev/null 2>&1 || true
}

image_ref() { case "$1" in
  gomodel) echo "${GOMODEL_IMAGE:-gomodel-bench:local}";;
  litellm) echo "${LITELLM_IMAGE:-ghcr.io/berriai/litellm:main-stable}";;
  portkey) echo "${PORTKEY_IMAGE:-portkeyai/gateway:latest}";;
  bifrost) echo "${BIFROST_IMAGE:-maximhq/bifrost:latest}";;
esac; }

# ── Build the bench-tools image ────────────────────────────────────
log "Building bench-tools image"
docker build -q -t "$BENCH_TOOLS_IMAGE" ./bench-tools >/dev/null

# ── Pull latest competitor images up front (digests recorded per gateway) ──
for gw in $GATEWAYS; do
  [[ "$gw" == "gomodel" ]] && continue
  docker pull -q "$(image_ref "$gw")" 2>/dev/null || true
done

# ── Clean any leftover state, then bring up the shared mock ────────
"${COMPOSE[@]}" --profile gomodel --profile litellm --profile portkey --profile bifrost down -v >/dev/null 2>&1 || true
log "Starting mock backend"
"${COMPOSE[@]}" up -d mock
sleep 2

# ── PASS A: latency, REPEATS trials, randomized target order ───────
for r in $(seq 1 "$REPEATS"); do
  RUN_DIR="$RESULTS_DIR/run${r}"; mkdir -p "$RUN_DIR"
  "${COMPOSE[@]}" up -d mock >/dev/null 2>&1 || true  # ensure the shared mock is up
  ORDER="$(shuffle "baseline $GATEWAYS" "$((r * 7919 + RANDOM))")"
  log "Latency trial ${r}/${REPEATS}  (order: ${ORDER})"
  for t in $ORDER; do
    if [[ "$t" == "baseline" ]]; then
      for spec in "${VARIANTS[@]}"; do
        IFS='|' read -r dialect mode _ <<< "$spec"
        run_variant "baseline" "mock" "$spec" "$RUN_DIR/baseline_${dialect}_${mode}.json"
      done
    else
      GOMODEL_IMAGE="${GOMODEL_IMAGE:-gomodel-bench:local}" \
        "${COMPOSE[@]}" --profile "$t" up -d "$t" >/dev/null 2>&1 || true
      wait_ready "$t" "$(gw_port "$t")" || true
      warmup_gateway "$t"
      for spec in "${VARIANTS[@]}"; do
        IFS='|' read -r dialect mode _ <<< "$spec"
        run_variant "$t" "$t" "$spec" "$RUN_DIR/${t}_${dialect}_${mode}.json"
      done
      # Remove only this gateway's container — NOT `compose down`, which would
      # also tear down the profile-less mock and break the next baseline.
      "${COMPOSE[@]}" --profile "$t" rm -sf "$t" >/dev/null 2>&1 || true
    fi
    sleep "$REST_SECONDS"
  done
done

# ── PASS B: capacity sweep + startup + footprint, once, randomized ─
log "Capacity + footprint pass"
"${COMPOSE[@]}" up -d mock >/dev/null 2>&1 || true  # ensure the shared mock is up
# Baseline capacity ceiling first (mock is already up, no gateway lifecycle).
run_sweep "baseline" "mock"

for gw in $(shuffle "$GATEWAYS"); do
  ref="$(image_ref "$gw")"
  log "Capacity: $gw  (image: $ref)"
  measure_startup "$gw"          # brings the gateway up + times cold start
  record_image "$gw" "$ref"
  warmup_gateway "$gw"
  run_sweep "$gw" "$gw"

  cname="bench-${gw}-1"
  csv="$RESULTS_DIR/${gw}_resources.csv"
  load_json="$RESULTS_DIR/${gw}_sustained.json"
  idle_mem="$(docker stats --no-stream --format '{{.MemUsage}};0' "$cname" 2>/dev/null | awk "$STAT_AWK" | cut -d, -f1 || true)"
  start_sampler "$cname" "$csv"
  sustained_load "$gw" "$(gw_port "$gw")" "$load_json"
  stop_sampler

  res="$(summarize_resources "$csv")"
  load_rps="$(grep -o '"rps": *[0-9.]*' "$load_json" 2>/dev/null | head -1 | grep -o '[0-9.]*' || true)"
  printf '{"gateway":"%s","idle_mem_mb":%s,"load_rps":%s,"under_load":%s}\n' \
    "$gw" "${idle_mem:-0}" "${load_rps:-0}" "$res" > "$RESULTS_DIR/${gw}_resources.json"
  echo "    resources: idle=${idle_mem:-0}MiB load_rps=${load_rps:-0} $res"

  "${COMPOSE[@]}" --profile "$gw" rm -sf "$gw" >/dev/null 2>&1 || true
  sleep "$REST_SECONDS"
done

"${COMPOSE[@]}" down -v >/dev/null 2>&1 || true

# ── Run metadata ───────────────────────────────────────────────────
IMDS_TOKEN="$(curl -s -m 2 -X PUT 'http://169.254.169.254/latest/api/token' -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' 2>/dev/null || true)"
INSTANCE_TYPE_META="$(curl -s -m 2 -H "X-aws-ec2-metadata-token: $IMDS_TOKEN" http://169.254.169.254/latest/meta-data/instance-type 2>/dev/null || true)"
[[ "$INSTANCE_TYPE_META" == *"<"* || -z "$INSTANCE_TYPE_META" ]] && INSTANCE_TYPE_META="unknown"
cat > "$RESULTS_DIR/meta.json" <<JSON
{
  "n_requests": $N,
  "max_variant_seconds": $MAX_VARIANT_SECONDS,
  "concurrency": $C,
  "repeats": $REPEATS,
  "litellm_num_workers": $LITELLM_NUM_WORKERS,
  "warmup": $WARMUP,
  "resource_seconds": $RESOURCE_SECONDS,
  "rest_seconds": $REST_SECONDS,
  "sweep_concurrency": "$(echo "$SWEEP_CONCURRENCY" | tr ' ' ',')",
  "sweep_duration_s": $SWEEP_DURATION,
  "gateways": "$(echo "$GATEWAYS" | tr ' ' ',')",
  "instance_type": "$INSTANCE_TYPE_META",
  "cpus": $(nproc 2>/dev/null || echo 1),
  "kernel": "$(uname -r)"
}
JSON

log "Done. Results in $RESULTS_DIR"
ls -1 "$RESULTS_DIR"
