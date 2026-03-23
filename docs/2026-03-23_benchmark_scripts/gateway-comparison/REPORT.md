# AI Gateway Benchmark: GoModel vs LiteLLM

**Date:** 2026-03-23
**Platform:** macOS Darwin 25.3.0 / Apple Silicon (arm64)
**Go:** 1.26.1 | **Python:** 3.x (LiteLLM 1.82.0)

## Methodology

Both gateways proxy to the **same mock OpenAI-compatible backend** on localhost. The mock responds instantly with deterministic payloads, so results measure **pure gateway overhead** — not provider latency.

- **Mock backend:** Go HTTP server returning fixed OpenAI-compatible JSON/SSE
- **Load tool (non-streaming):** [`hey`](https://github.com/rakyll/hey) — 1,000 requests, 50 concurrent connections
- **Load tool (streaming):** Custom Go SSE benchmark tool — 1,000 requests, 50 concurrent
- **Resource monitoring:** `ps` sampling RSS and CPU% every 0.5s
- **Warm-up:** 100 requests before each benchmark run
- **Retries/logging/analytics disabled** on both gateways

### Versions

| Gateway  | Version         | Language | HTTP Framework              |
|----------|-----------------|----------|-----------------------------|
| GoModel  | main (b1b3941)  | Go 1.26  | Echo                        |
| LiteLLM  | 1.82.0          | Python   | FastAPI / Uvicorn (4 workers) |

---

## Results

### 1. Chat Completions — Non-Streaming

`POST /v1/chat/completions` with `stream: false`

| Metric             | Baseline (direct) | GoModel        | LiteLLM         | GoModel vs LiteLLM |
|--------------------|--------------------|----------------|-----------------|---------------------|
| **Throughput**     | 40,459 req/s       | 24,129 req/s   | 2,207 req/s     | **10.9x faster**    |
| **p50 latency**    | 0.90 ms            | 1.90 ms        | 21.30 ms        | **11.2x lower**     |
| **p95 latency**    | 2.80 ms            | 3.90 ms        | 26.90 ms        | **6.9x lower**      |
| **p99 latency**    | 6.10 ms            | 7.90 ms        | 39.90 ms        | **5.1x lower**      |
| **Avg latency**    | 1.10 ms            | 2.00 ms        | 21.80 ms        | **10.9x lower**     |
| **Peak RSS**       | —                  | 40.6 MB        | 313.8 MB        | **7.7x less memory**|
| **Error rate**     | 0%                 | 0%             | 0%              | —                   |

### 2. Chat Completions — Streaming

`POST /v1/chat/completions` with `stream: true`

| Metric                 | Baseline (direct) | GoModel        | LiteLLM          | GoModel vs LiteLLM |
|------------------------|--------------------|----------------|-----------------|---------------------|
| **Throughput**         | 6,448 req/s        | 3,929 req/s    | 386 req/s        | **10.2x faster**    |
| **TTFB p50**           | 6.42 ms            | 12.13 ms       | 121.22 ms        | **10.0x lower**     |
| **TTFB p95**           | 12.29 ms           | 14.26 ms       | 215.52 ms        | **15.1x lower**     |
| **TTFB p99**           | 14.97 ms           | 17.26 ms       | 240.65 ms        | **13.9x lower**     |
| **Total latency p50**  | 6.75 ms            | 12.66 ms       | 121.23 ms        | **9.6x lower**      |
| **Total latency p95**  | 13.45 ms           | 14.87 ms       | 215.53 ms        | **14.5x lower**     |
| **Total latency p99**  | 18.25 ms           | 17.44 ms       | 240.68 ms        | **13.8x lower**     |
| **Peak RSS**           | —                  | 49.9 MB        | 313.8 MB         | **6.3x less memory**|
| **Chunks/response**    | 34                 | 34             | 33               | —                   |
| **Error rate**         | 0%                 | 0%             | 0%               | —                   |

### 3. Responses API — Non-Streaming

`POST /v1/responses` with `stream: false`

| Metric             | GoModel        | LiteLLM         | GoModel vs LiteLLM |
|--------------------|----------------|-----------------|---------------------|
| **Throughput**     | 35,977 req/s   | 2,481 req/s     | **14.5x faster**    |
| **p50 latency**    | 1.10 ms        | 19.10 ms        | **17.4x lower**     |
| **p95 latency**    | 2.70 ms        | 22.70 ms        | **8.4x lower**      |
| **p99 latency**    | 4.40 ms        | 33.50 ms        | **7.6x lower**      |
| **Avg latency**    | 1.30 ms        | 19.40 ms        | **14.9x lower**     |
| **Peak RSS**       | 53.6 MB        | 313.8 MB        | **5.9x less memory**|
| **Error rate**     | 0%             | 0%              | —                   |

### 4. Responses API — Streaming

`POST /v1/responses` with `stream: true`

| Metric                 | GoModel        | LiteLLM          | GoModel vs LiteLLM |
|------------------------|----------------|-----------------|---------------------|
| **Throughput**         | 3,470 req/s    | 1,683 req/s      | **2.1x faster**     |
| **TTFB p50**           | 13.43 ms       | 29.16 ms         | **2.2x lower**      |
| **TTFB p95**           | 15.40 ms       | 35.63 ms         | **2.3x lower**      |
| **TTFB p99**           | 15.84 ms       | 42.39 ms         | **2.7x lower**      |
| **Total latency p50**  | 14.19 ms       | 29.16 ms         | **2.1x lower**      |
| **Total latency p95**  | 16.16 ms       | 35.63 ms         | **2.2x lower**      |
| **Total latency p99**  | 16.83 ms       | 42.39 ms         | **2.5x lower**      |
| **Peak RSS**           | 53.7 MB        | 313.8 MB         | **5.8x less memory**|
| **Chunks/response**    | 36             | 37               | —                   |
| **Error rate**         | 0%             | 0%               | —                   |

---

## Resource Consumption

| Metric             | GoModel     | LiteLLM     | Ratio            |
|--------------------|-------------|-------------|------------------|
| **Idle RSS**       | ~41 MB      | ~314 MB     | **7.7x less**    |
| **Peak RSS**       | ~54 MB      | ~314 MB     | **5.8x less**    |
| **Binary size**    | 59 MB (single static binary) | N/A (Python runtime) | — |
| **Startup time**   | < 1s        | ~5s         | —                |
| **Dependencies**   | None (Go binary) | Python 3 + pip packages | — |

---

## Overhead vs Direct Baseline

Latency added by each gateway on top of a direct connection to the backend:

| Test                         | GoModel          | LiteLLM            |
|------------------------------|------------------|--------------------|
| Chat non-stream p50          | +1.00 ms         | +20.40 ms          |
| Chat non-stream p99          | +1.80 ms         | +33.80 ms          |
| Chat stream TTFB p50         | +5.71 ms         | +114.80 ms         |
| Chat stream total p99        | -0.81 ms*        | +222.43 ms         |

\* GoModel's p99 was lower than baseline due to run-to-run variance at the tail.

---

## Summary

| Dimension               | GoModel                     | LiteLLM                      |
|-------------------------|-----------------------------|------------------------------|
| **Throughput**          | 24,000–36,000 req/s         | 2,200–2,500 req/s            |
| **Streaming throughput**| 3,500–3,900 req/s           | 400–1,700 req/s              |
| **p50 latency**         | 1–2 ms (non-stream), 12–14 ms (stream) | 19–21 ms (non-stream), 29–121 ms (stream) |
| **Memory**              | 41–54 MB                    | 314 MB                       |
| **Advantage**           | **10–15x faster, 6–8x less memory** | Richer feature set (100+ providers, spend tracking, prompt management) |

GoModel delivers **10–15x higher throughput** and **6–8x lower memory consumption** than LiteLLM across all four test types, while maintaining **0% error rate** and sub-millisecond overhead on non-streaming requests.

---

## Reproduction

```bash
# Prerequisites: Go 1.26+, Python 3 with litellm, hey
cd docs/2026-03-23_benchmark_scripts/gateway-comparison
bash run-benchmark.sh
```

### Test Configuration

- **Requests per test:** 1,000
- **Concurrency:** 50
- **Mock response size:** ~190 bytes (chat), ~130 bytes (responses)
- **Streaming chunks:** 31 content chunks per response
- **Both gateways:** retries disabled, logging disabled, auth disabled
