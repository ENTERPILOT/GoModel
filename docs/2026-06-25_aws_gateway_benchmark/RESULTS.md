# Results — 2026-06-25 (AWS c7i.large run)

Reference run produced by `./run.sh` (raw data in `output/20260625-182538/`,
machine summary in that dir's `summary.md` / `summary.json`). Four gateways:
**GoModel, LiteLLM, Portkey, Bifrost**.

- **Host**: AWS EC2 **c7i.large** (2 vCPU, 4 GiB, **non-burstable** — no CPU-credit
  drift, so the tail is stable), Amazon Linux 2023, us-east-1.
- **Load**: N=8000 requests/variant, concurrency 10, **2 randomized-order trials**
  (latency = median across trials; p99 shown with its min–max), 200-request
  process warmup + 50-request per-variant warmup, per-variant wall cap 10 s, 8 s
  resource window, capacity sweep at c∈{1,16,128}. Shared in-process **mock**
  backend, so every number is **gateway overhead**, not model latency.
- **Parity**: retries disabled on every gateway, GoModel's circuit breaker disabled
  (so the sweep can't trip it), and **LiteLLM run at its recommended worker count —
  one worker per CPU core (`num_workers=2` on this 2-vCPU box)** so it isn't pinned
  to a single core while the Go gateways use both.
- Images (digests in `*_image.json`): GoModel (built from this repo), latest
  `litellm:main-stable`, `portkeyai/gateway:latest`, `maximhq/bifrost:latest`.

> Fast reference run (N=8000 × 2 trials) sized to finish end-to-end in well under
> 20 minutes; the p99 min–max spreads are tight, so the medians are stable. Raise
> `N`/`REPEATS` for a heavier run.

## Latency — non-streaming (ms, median of trials)

| Workload  | metric | baseline | GoModel | Bifrost | Portkey | LiteLLM |
|-----------|--------|---------:|--------:|--------:|--------:|--------:|
| chat      | p50 | 0.23 | **1.81** | 2.51 | 9.70 | 30.56 |
| chat      | p99 | 2.77 | **6.88** | 18.27 | 30.54 | 39.26 |
| responses | p50 | 0.26 | **2.01** | 2.73 | 9.07 | 39.12 |
| responses | p99 | 2.33 | **7.28** | 16.55 | 26.92 | 48.60 |
| messages  | p50 | 0.26 | **1.76** | 2.65 | ✗ | 61.06 |
| messages  | p99 | 2.23 | **6.59** | 19.08 | ✗ | 98.12 |

**GoModel has the lowest p50 and the tightest p99** (~7 ms vs Bifrost ~18 ms,
Portkey ~31 ms, LiteLLM ~39 ms). `overhead p50` (gateway p50 − baseline p50):
GoModel ≈ 1.6 ms, Bifrost ≈ 2.3 ms, Portkey ≈ 9.5 ms, LiteLLM ≈ 30 ms.

## Latency — streaming (ms, median of trials)

| Workload  | metric | GoModel | Bifrost | Portkey | LiteLLM |
|-----------|--------|--------:|--------:|--------:|--------:|
| chat      | TTFT p50 | **4.71** | 9.02 | 27.97 | 151.94 |
| chat      | total p50 | **4.95** | 11.89 | 27.98 | 151.95 |
| responses | TTFT p50 | **4.69** | 12.87 | 27.90 | 47.53 |
| responses | total p50 | **5.00** | 14.94 | 27.93 | 47.55 |
| messages  | TTFT p50 | **7.50** | †      | ✗ | 48.86 |
| messages  | total p50 | **8.38** | †      | ✗ | 48.89 |

† **Bifrost messages-stream is an idle-bound artifact, not a throughput number**
(no terminal event over a non-native backend → 0 completions within the 10 s cap).

## Throughput / capacity (chat non-stream, sustained req/s by concurrency)

| target | c=1 | c=16 | c=128 | peak | knee |
|--------|----:|-----:|------:|-----:|-----:|
| baseline | 15510 | 29701 | 30015 | **30015** | 16 |
| GoModel  | 2745 | 4928 | 4567 | **4928** | 16 |
| Bifrost  | 1885 | 3088 | 2904 | **3088** | 16 |
| Portkey  | 636 | 946 | 900 | **946** | 16 |
| LiteLLM  | 227 | 324 | 254 | **324** | 16 |

GoModel tops the gateways at **~4900 req/s**, ~1.6× Bifrost, ~5× Portkey, ~15×
LiteLLM. All saturate by c=16 on 2 vCPUs.

## Resources

| Metric | GoModel | Portkey | Bifrost | LiteLLM |
|--------|--------:|--------:|--------:|--------:|
| Docker image, compressed pull (MB) | **16** | 59 | 77 | 372 |
| Docker image, on-disk (MB)      | **47.2** | 177.4 | 230.7 | 1159.9 |
| Cold start to first 200 (s) | **0.56** | 1.05 | 7.07 | 25.49 |
| Peak RSS under load (MB)| **37.0** | 112.0 | 143.0 | 2272.3 |
| Avg CPU under load (%) | 92.6 | 116.9 | 117.6 | 101.1 |
| Sustained req/s (resource window) | **4824** | 960 | 2977 | 261 |
| Efficiency (req/s per CPU %) | **52.1** | 8.2 | 25.3 | 2.6 |

GoModel is the most CPU-efficient (**52 req/s per CPU-%**, ~2× Bifrost, ~6×
Portkey, ~20× LiteLLM), the smallest image (**47 MB**), the smallest footprint
(**37 MB** peak), and the fastest cold start (**0.56 s**).

> **LiteLLM at its recommended config.** With `num_workers=2` (one per core) LiteLLM
> is faster and higher-throughput than the earlier single-worker run (≈220 → 324
> req/s; chat p50 ≈ 44 → 31 ms — a single worker was queuing the 10 concurrent
> requests), but its **memory doubled to ~2.3 GB** (two ~1 GB worker processes) and
> its **cold start rose to ~25 s**. Running LiteLLM "properly" widens the resource
> gap, not narrows it.

## Feature coverage (6 variants)

| Gateway | chat | responses | messages | total |
|---------|:----:|:---------:|:--------:|:-----:|
| GoModel | ✓ | ✓ | ✓ | 6/6 |
| LiteLLM | ✓ | ✓ | ✓ | 6/6 |
| Bifrost | ✓ | ✓ | ✓† | 6/6 |
| Portkey | ✓ | ✓ | ✗ | 4/6 |

- **Portkey** errors on the Anthropic `/v1/messages` dialect in this single-provider
  (openai → mock) setup; setup limitation, not a hard capability gap.
- **Bifrost** serves Anthropic at `/anthropic/v1/messages`, needs an `openai/`-prefixed
  model and `allow_private_network:true`; messages-streaming has the caveat above (†).

## Takeaways

- **GoModel** — best all-rounder: lowest p50 and tightest p99 (~7 ms), highest
  gateway throughput (~4900 req/s), best CPU efficiency (52 req/s per %), smallest
  image (47 MB) and memory (37 MB), fastest cold start (0.56 s), full 6/6 coverage.
- **Bifrost** (Go) — second on throughput, low p50 but a heavier p99 tail; streaming
  terminal-event gaps over a non-native backend.
- **Portkey** (Node) — middle tier; no Anthropic Messages in this setup.
- **LiteLLM** (Python) — full coverage, but even at its recommended 2-worker config
  it is ~15× behind on throughput and carries a **1.16 GB image + ~2.3 GB RAM + ~25 s
  cold start**. The cost of Python on the hot path.

## Methodology notes

- **Repeats + spread** — 2 trials, randomized gateway order each trial; latency is
  the median across trials, p99 carries its min–max.
- **Config parity** — retries off on all; GoModel's circuit breaker disabled (a few
  transient errors under the c=128 sweep would otherwise trip it and blanket-503 its
  own capacity); **LiteLLM at one worker per core (`num_workers`=vCPUs)**, its own
  production recommendation, set automatically from `nproc`.
- **Warm-up** — 200 global + 50 per-variant requests; the per-variant warmup
  neutralizes LiteLLM's lazy per-dialect imports and, with >1 worker, warms each
  worker before measuring.
- **Throughput vs latency separated** — capacity comes from a time-boxed concurrency
  sweep, not the latency-coupled rps in the latency tables.
- **Per-variant wall cap (10 s)** — bounds idle-bound streaming variants; cap-aborted
  requests are reported as `capped`, not `failed`.
- **Resilient orchestration** — the remote benchmark runs detached (`setsid`) and the
  orchestrator polls for the `meta.json` sentinel, so an SSH drop can't kill or hang
  the run; `set -uo` so one flaky variant skips instead of aborting.
- Reproduce with `./run.sh`; pin `var.ami_id` and the `*_IMAGE` digests for a
  byte-identical rerun. Heavier run: `N=20000 REPEATS=5 ./run.sh`.
