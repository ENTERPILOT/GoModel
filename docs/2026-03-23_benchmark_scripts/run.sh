#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
RAW_BENCH_DIR="$SCRIPT_DIR/gateway-comparison"
RESULTS_DIR="${RESULTS_DIR:-$RAW_BENCH_DIR/results}"
OUTPUT_DIR="${OUTPUT_DIR:-$SCRIPT_DIR/output}"

if [[ "${RUN_BENCHMARK:-0}" == "1" ]]; then
    echo "Running raw benchmark suite..."
    bash "$RAW_BENCH_DIR/run-benchmark.sh"
fi

if [[ ! -d "$RESULTS_DIR" ]]; then
    echo "Results directory not found: $RESULTS_DIR" >&2
    echo "Run with RUN_BENCHMARK=1 or set RESULTS_DIR to an existing benchmark result directory." >&2
    exit 1
fi

echo "Generating normalized benchmark artifacts..."
CMD=(
    python3
    "$SCRIPT_DIR/generate_benchmark_artifacts.py"
    --results-dir "$RESULTS_DIR"
    --output-dir "$OUTPUT_DIR"
)

if [[ -n "${BLOG_PUBLIC_DIR:-}" ]]; then
    CMD+=(--blog-public-dir "$BLOG_PUBLIC_DIR")
fi

"${CMD[@]}"
