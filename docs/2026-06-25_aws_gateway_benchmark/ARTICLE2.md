---
title: "Benchmarking AI Gateways: GoModel vs LiteLLM vs Portkey vs Bifrost"
description: "A reproducible AI gateway benchmark comparing GoModel, LiteLLM, Portkey, and Bifrost on latency, throughput, memory, CPU, cold start, and image size."
coverImage: "/blog/charts/gomodel-gateway-benchmark-june-2026-cover.png"
coverImageWidth: 2400
coverImageHeight: 1260
pubDate: 2026-06-26
author: "Jakub A. Wasek"
keywords:
  - AI gateway benchmark
  - AI control plane
  - OpenAI-compatible API
  - LiteLLM alternative
  - GoModel
  - LiteLLM
  - Portkey
  - Bifrost
tags:
  - benchmarking
  - ai-gateway
  - ai-control-plane
  - litellm
  - portkey
  - bifrost
  - gomodel
---

![GoModel vs LiteLLM, Portkey and Bifrost benchmark - four gateways, one backend, GoModel wins](./cover.png)

In October 2025 I tried to build my startup on top of LiteLLM.

At first it looked like the obvious choice. It supported many providers, it had
an OpenAI-compatible API, and it was already used by a lot of people. I did not
want to write an AI gateway. I wanted to build the product behind it.

Then I started running it on the hot path.

My opinion changed there.

A gateway is not a dashboard or integration glue you call once in a while. It
sits on every request, every retry, every stream, every tool call, every
fallback, every timeout.

A heavy gateway charges rent forever.

Most AI gateway comparisons miss that part. They talk about provider count,
dashboards, tracing, and "support for 1000+ models". Those things matter, but
they are not free. Before the gateway calls OpenAI, Anthropic, Gemini, vLLM, or
anything else, it has already spent your CPU, memory, cold-start time, and
operational budget.

I am not comparing full product maturity here. I am comparing how these gateways
behave on the hot path.

So I started writing [GoModel](https://github.com/ENTERPILOT/GoModel): a small
open-source AI gateway and AI control plane in Go, with an OpenAI-compatible API
and explicit provider adapters.

When I <a href="https://news.ycombinator.com/item?id=47861333" rel="nofollow noopener noreferrer">launched GoModel on Hacker News</a>,
I promised a real, reproducible benchmark. This article is that follow-up.

The benchmark question is simple:

**How lean is each AI gateway when it sits on the request path?**

That question runs through the whole benchmark: GoModel vs LiteLLM vs Portkey vs
Bifrost, measured by latency, throughput, memory, CPU, cold start, and image
size rather than landing pages or feature matrices.

## The runtime footprint matters

Latency gets the easiest arguments. It rarely tells the whole story.

Most real LLM calls are dominated by inference time. If a model takes `2000 ms`
to answer, the difference between `5 ms` and `15 ms` of proxy overhead is not
the main story.

The main story is the deployment envelope:

- How much RAM does the gateway need under load?
- How much CPU does it burn per request?
- How many requests can it serve per core?
- How fast does it cold-start?
- How large is the Docker image?
- Can you run it as a sidecar, on a small VM, in serverless, or near local
  models?
- Is the core gateway actually open-source?

Those numbers decide whether the gateway can run where you want it to run.

A `372 MB` compressed image (`1.2 GB` unpacked) that idles around gigabytes of
RAM and takes `25 s` to cold-start is a different operational thing than a
`16 MB` image that peaks at `37 MB` of RAM and is serving traffic `0.56 s` after
launch.

So I care about the runtime footprint.

## What this benchmark does not prove

This benchmark does **not** prove that one gateway is best for every company.

I am not measuring:

- bug counts or overall correctness
- semantic cache quality
- tracing UI quality
- guardrail quality
- admin dashboards
- long-term provider maintenance
- every possible provider-specific feature
- total provider count

Those things matter. Some of them matter a lot.

LiteLLM in particular has more integrated providers and more gateway features
than GoModel today. If your first requirement is maximum provider coverage right
now, LiteLLM has a real advantage. This benchmark does not erase that. It
measures the runtime footprint of putting each gateway on the request path. In
practice, many smaller or newer providers already expose an OpenAI-compatible
API, so provider count is not always the same as practical routing coverage.

The benchmark measures one narrower thing: **runtime and deployment overhead on
the request path**.

That still matters, because the gateway is on the hot path. If you run high
request volume, local models, serverless workloads, edge workloads, or many small
model calls, the overhead stops being theoretical.

## AI gateway benchmark setup

I tested four AI gateways people actually compare:

- GoModel
- LiteLLM
- Portkey
- Bifrost

Every gateway talked to the **same instant mock backend**, on purpose. I did not
want to benchmark OpenAI, Anthropic, AWS networking, or random internet jitter.
I wanted to isolate the gateway itself.

Each gateway ran one at a time, in Docker, on an **AWS `c7i.large`** with
2 vCPU and 4 GiB RAM, running the latest **Amazon Linux 2023** AMI. The whole
thing is Terraform'd, runs with one command, and tears itself down afterwards.

I first ran this on a free-tier `t2.micro`. That was cheap and easy to
reproduce, but unfair to the heavier gateways. A 1 GiB machine cannot hold a
gateway that wants gigabytes of memory, so it starts swapping. At that point you
are benchmarking the host being too small.

So I moved to `c7i.large`: still small, but non-burstable and large enough that
nothing swaps. It also makes the LiteLLM setup more honest. LiteLLM recommends
one worker per vCPU, and this machine has 2 vCPUs, so LiteLLM gets 2
workers. That gives it the multi-core access it is supposed to have instead of
pinning it to a single worker on a tiny box.

The test covered six workloads:

- chat completions, non-streaming
- chat completions, streaming
- Responses API, non-streaming
- Responses API, streaming
- Anthropic messages, non-streaming
- Anthropic messages, streaming

Each workload used `8,000` requests at concurrency `10`, across **two trials
with randomized gateway order**. Latency is the **median across trials**, and I
report p99 with its min-max range so one noisy window cannot tell the whole
story.

I would not call this a statistically exhaustive study. It is a reproducible
engineering benchmark, and the harness is public so people can rerun it, change
the machine, or add their own workloads.

A few details matter if you want to reproduce or criticize the numbers:

- **Throughput is measured, not inferred.** The latency runs report
  completed-req/s at fixed concurrency, but real capacity comes from a separate
  concurrency sweep that drives each gateway to saturation.
- **Every dialect is warmed up before measurement.** LiteLLM lazily imports some
  per-dialect translation code on first use. A chat-only warmup made its
  Responses and Messages paths look worse than they should. I warmed up all
  dialects to avoid that.
- **Retries are disabled for all gateways.** I also disabled GoModel's circuit
  breaker for this benchmark. In production, rejecting traffic after upstream
  trouble is the right behavior. In a saturation benchmark, it would make the
  throughput number unfairly low.
- **LiteLLM runs with its recommended worker count.** A LiteLLM worker is
  effectively single-threaded, and its production guidance is one worker per
  vCPU. On this box that means `2` workers.
- **Streaming uses terminal-marker or idle-gap detection.** If a gateway streams
  content but never sends a terminal event, the harness measures to last byte
  instead of hanging forever.

## GoModel vs LiteLLM vs Portkey vs Bifrost

Representative latency is chat completions, non-streaming. All resource figures
are measured under load on the same box.

| Metric | GoModel | Bifrost | Portkey | LiteLLM |
|---|--:|--:|--:|--:|
| Runtime | Go | Go | Node.js | Python |
| Latency overhead `p50` | **`1.8 ms`** | `2.5 ms` | `9.7 ms` | `30.6 ms` |
| Latency `p99` | **`6.9 ms`** | `18.3 ms` | `30.5 ms` | `39.3 ms` |
| Throughput (sustained) | **`4900 req/s`** | `3100 req/s` | `950 req/s` | `324 req/s` |
| Peak RAM under load | **`37 MB`** | `143 MB` | `112 MB` | `2.3 GB` |
| Efficiency (req/s per CPU %) | **`52`** | `25` | `8.2` | `2.6` |
| Cold start to first request | **`0.56 s`** | `7.1 s` | `1.1 s` | `25.5 s` |
| Docker image (compressed pull) | **`16 MB`** | `77 MB` | `59 MB` | `372 MB` |
| Workload coverage | `6/6` | `6/6` | `4/6` | `6/6` |
| Vendor-neutral core | Yes | Partial † | Yes | Yes |
| Core source available | Yes ‡ | Partial ‡ | Partial ‡ | Yes |

Same numbers, at a glance:

![Latency tail p99: GoModel 6.9 ms, Bifrost 18.3 ms, Portkey 30.5 ms, LiteLLM 39.3 ms](./charts/june-2026-latency-p99.svg)

![Sustained throughput: GoModel 4,900 req/s, Bifrost 3,100, Portkey 950, LiteLLM 324](./charts/june-2026-throughput.svg)

![Peak memory under load: GoModel 37 MB, Bifrost 143 MB, Portkey 112 MB, LiteLLM 2.3 GB](./charts/june-2026-memory.svg)

![Docker image, compressed: GoModel 16 MB, Bifrost 77 MB, Portkey 59 MB, LiteLLM 372 MB](./charts/june-2026-image.svg)

## What stood out

GoModel had the lowest median latency and the tightest tail: `1.8 ms` p50 and
`6.9 ms` p99.

Bifrost was close on median latency at `2.5 ms`, which is a good result. The
gap opened at the tail and in memory: `18.3 ms` p99 and `143 MB` peak RAM under
load.

Portkey was heavier than I expected for this narrow proxy benchmark. It served
`950 req/s` sustained and used `112 MB` peak RAM under load. In this setup it did
not serve the Anthropic `/v1/messages` dialect, so it gets `4/6` workload
coverage. Treat that as a setup limitation, not a claim that Portkey cannot
support Anthropic in a fuller virtual-key configuration.

LiteLLM was the outlier. At its recommended worker count, it used about
`2.3 GB` of RAM, cold-started in `25.5 s`, and sustained `324 req/s`.

Not because Python is morally bad. The language matters only when it changes the
deployment envelope. Here it does: memory floor, image size, cold-start time,
dependency graph, and throughput per core.

The later <a href="https://futuresearch.ai/blog/litellm-pypi-supply-chain-attack/" rel="nofollow noopener noreferrer">supply-chain incident around LiteLLM</a>
also made me more confident in GoModel's design direction. A small Go binary
with a standard-library-heavy dependency tree is structurally less exposed to
that class of problem than a large Python dependency graph.

## What AI gateway benchmarks do not capture

Forwarding JSON is not the hard part.

The hard part is provider drift.

OpenAI, Anthropic, Gemini, AWS Bedrock, Azure OpenAI, Groq, xAI, Cerebras, vLLM,
and local servers all disagree in small ways. Then they change those ways. Tool
calling changes. Streaming changes. Reasoning parameters change. Image inputs
change. Error formats change. Rate-limit semantics change.

An AI gateway or AI control plane has to absorb that without becoming magic.

GoModel's bet is not "support every model name on the internet".

The bet is:

- support the providers people actually deploy
- keep provider adapters explicit
- accept OpenAI-compatible requests generously
- translate only what needs translation
- pass through what should stay provider-specific
- return conservative OpenAI-compatible responses

For the same reason, GoModel starts as a small OpenAI-compatible gateway, not as
a dashboard with a proxy attached.

## Why this matters for local models and vLLM

If all your traffic goes to a cloud model that takes several seconds to answer,
gateway overhead can look academic.

Local models change the math.

If you are routing through an AI gateway to vLLM, Ollama, LM Studio, llama.cpp,
or small specialized models on your own network, the model call can be much
faster. Then gateway overhead, cold starts, memory, and sidecar size matter more.

One reason I want GoModel to stay small: a gateway should be cheap enough to put
near the workload.

## Notes on neutrality and open source

Bifrost is built by Maxim AI, an LLM
evaluation and observability platform. It routes to many model providers, but
the gateway also sits close to Maxim's eval and observability ecosystem. If you
want to choose your own eval platform, or stay independent from any eval
platform, ask whether Bifrost is the right match for you. Good software can
still have incentives attached. "Vendor-neutral" needs an asterisk here.

"Open-source" also needs care.

Portkey keeps observability storage, dashboard, multi-team RBAC, and at-scale
semantic caching in a closed managed tier. Bifrost's core gateway is Apache-2.0,
but its Enterprise edition adds closed or managed features. LiteLLM's proxy core
is MIT, but enterprise features like SSO, audit logs, and fine-grained access
control sit behind a proprietary commercial license.

GoModel is open-source today. Some enterprise-grade AI control plane features may
stay private. The core gateway is intended to remain useful without those private
features.

## Reproduce it yourself

The benchmark is built to be self-verifiable. It provisions the AWS instance,
runs every gateway against the same backend, prints the tables, and destroys the
infrastructure.

**[Reproduce it yourself](https://github.com/ENTERPILOT/GoModel/tree/main/docs/2026-06-25_aws_gateway_benchmark)**:

```bash
./run.sh
```

One caveat: it runs on **paid** AWS infrastructure, not the free tier. A
`c7i.large` is about `$0.09`/hour and the run self-destructs within an hour or
two, so budget **under `$1`** per run to be safe.

If you pass `KEEP=1` or teardown fails, you keep paying until you destroy the
box, so double-check the teardown.

## Conclusion

I did not start GoModel because I wanted another AI gateway in the world.

I started it because the gateway I wanted to use became part of the problem. It
sat on the hot path, but did not feel like hot-path software: too heavy, too
slow to start, too expensive to keep around, too large for the job.

This benchmark is the result of turning that frustration into numbers.

The numbers say GoModel is small in the places I care about: `16 MB` image,
`37 MB` peak RAM, `0.56 s` cold start, `1.8 ms` p50, `6.9 ms` p99, and
`4900 req/s` sustained throughput on a small AWS box.

LiteLLM still has more providers and more features today. Portkey and Bifrost
have their own strengths. But if the gateway is going to sit between your users
and every model call, I think it should first be cheap, predictable, and boring
to run.

GoModel is my attempt to build that kind of gateway.
