# March 23, 2026 benchmark scripts

This directory is the reproducible entry point for the March 23, 2026
GoModel vs LiteLLM benchmark refresh.

It is built around the benchmark workspace in
`docs/2026-03-23_benchmark_scripts/gateway-comparison/`, then adds:

- a tested normalization step for the raw `hey` and streaming outputs,
- chart generation for the blog assets,
- a stable wrapper command for rerunning the benchmark and rebuilding the
  article artifacts.

## What this benchmark measures

This run uses the same localhost mock backend for both gateways, so the numbers
measure gateway overhead rather than upstream model latency.

Workloads covered:

- `/v1/chat/completions` non-streaming
- `/v1/chat/completions` streaming
- `/v1/responses` non-streaming
- `/v1/responses` streaming

The raw benchmark runner also records a direct baseline for chat traffic with no
gateway in the middle.

## Prerequisites

- Go 1.26+
- Python 3.10+
- `hey`
- `litellm`
- Python packages: `matplotlib`, `numpy`

Install Python packages if needed:

```bash
python3 -m pip install matplotlib numpy
```

## Quick start

Run the raw benchmark and generate normalized artifacts:

```bash
RUN_BENCHMARK=1 bash docs/2026-03-23_benchmark_scripts/run.sh
```

If you already have a benchmark result directory, point the wrapper at it:

```bash
RESULTS_DIR=/path/to/results bash docs/2026-03-23_benchmark_scripts/run.sh
```

Copy the generated chart assets into the sibling Enterpilot blog repo:

```bash
BLOG_PUBLIC_DIR=../enterpilot.io/blog/public/charts \
  bash docs/2026-03-23_benchmark_scripts/run.sh
```

## Outputs

By default, generated artifacts land in `docs/2026-03-23_benchmark_scripts/output/`:

- `benchmark_summary.json`: normalized machine-readable metrics
- `charts/gomodel-vs-litellm-march-2026-dashboard.png`
- `charts/gomodel-vs-litellm-march-2026-throughput.png`
- `charts/gomodel-vs-litellm-march-2026-latency.png`
- `charts/gomodel-vs-litellm-march-2026-memory.png`
- `charts/gomodel-vs-litellm-march-2026-speedup.png`

## Notes

- The raw benchmark runner lives in `docs/2026-03-23_benchmark_scripts/gateway-comparison/run-benchmark.sh`.
- The normalization step exists because raw shell summaries are easy to drift or
  misparse; the parser in this directory is covered by unit tests with inline
  sample fixtures, so the repo does not need to carry benchmark result dumps.
- These results are a point-in-time localhost benchmark, not a universal claim
  about every deployment shape.
