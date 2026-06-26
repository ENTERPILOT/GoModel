# AWS gateway latency & resource benchmark — GoModel vs LiteLLM vs Portkey vs Bifrost

A reproducible, one-command benchmark that provisions a free-tier AWS instance,
runs four AI gateways through identical workloads against a deterministic mock
backend, measures latency and resource cost, and tears the infrastructure down.

Because every gateway talks to the **same local mock backend**, the numbers
reflect *gateway overhead*, not upstream model latency or network jitter.

## What it compares

Four OpenAI-compatible gateways, each pointed at the mock:

| Gateway  | Image                                | How it reaches the mock |
|----------|--------------------------------------|-------------------------|
| GoModel  | built from this repo (`Dockerfile`)  | `OPENAI_BASE_URL` env   |
| LiteLLM  | `ghcr.io/berriai/litellm:main-stable`| `configs/litellm-config.yaml` |
| Portkey  | `portkeyai/gateway:latest`           | `x-portkey-custom-host` header (+ `TRUSTED_CUSTOM_HOSTS=mock`) |
| Bifrost  | `maximhq/bifrost:latest`             | `configs/bifrost-config.json` (`network_config.base_url` + `allow_private_network`) |

Per-gateway quirks the harness handles automatically (see `gw_model`/`gw_path` in
`run-on-instance.sh`): Bifrost needs an explicit `openai/`-prefixed model, serves
the Anthropic dialect at `/anthropic/v1/messages` (not `/v1/messages`), and must
allow private-network egress to reach the mock.

### Workloads — 6 variants

The common denominator across OpenAI-compatible gateways, in both modes:

| Dialect   | Endpoint               | non-stream | stream |
|-----------|------------------------|:----------:|:------:|
| Chat      | `/v1/chat/completions` | ✓ | ✓ |
| Responses | `/v1/responses`        | ✓ | ✓ |
| Messages  | `/v1/messages` (Anthropic) | ✓ | ✓ |

A **baseline** (load sent straight to the mock, no gateway) runs first as the
latency floor. Variants a gateway does not implement are reported as failures
rather than silently skipped — e.g. Portkey's OSS gateway does not serve the
Anthropic Messages dialect here, so its messages variants fail; that asymmetry is
the finding. Streaming uses a terminal-marker **or idle-gap** end-of-stream
detection (`loadgen -idle`), so a gateway that streams content without sending a
terminal event (Bifrost) is still measured to last-byte rather than hanging.

### Metrics captured

- **Latency** — total-latency p50/p90/p95/p99, plus **TTFT** (time to first
  token) for streaming, and throughput (RPS). Driven by the `loadgen` tool.
- **Docker image size** — `docker image inspect` size + repo digest per gateway.
- **Memory** — idle RSS after warmup and peak RSS under load (`docker stats`).
- **CPU** — average CPU% under load (`docker stats`).

## Layout

```
terraform/            free-tier EC2 + SSH key + security group (apply/destroy)
remote/               everything shipped to and run on the instance
  bench-tools/        Go mock backend + loadgen (one small image)
  configs/            litellm config
  docker-compose.yml  mock + one gateway per profile (benchnet network)
  run-on-instance.sh  builds images, runs 6 variants x N gateways, samples stats
scripts/summarize.py  raw JSON -> latency + resource tables + summary.json
run.sh                orchestrator: build -> apply -> run -> collect -> destroy
```

## Prerequisites

- AWS credentials configured (`aws sts get-caller-identity` works)
- Terraform ≥ 1.6, Docker (with `buildx`), `rsync`, `ssh`, Python 3
- An AWS account with default VPC in the chosen region

## Run it

```bash
cd docs/2026-06-25_aws_gateway_benchmark
./run.sh                         # full run in us-east-1, then auto-destroy
N=1000 C=20 ./run.sh             # heavier load
REGION=eu-west-1 ./run.sh        # different region
GATEWAYS="gomodel litellm" ./run.sh   # subset
KEEP=1 ./run.sh                  # leave the instance up for debugging
```

`run.sh` always tears the instance down via an EXIT trap, even on failure. If a
run is interrupted, reconcile manually:

```bash
cd terraform && terraform destroy -auto-approve
```

Results land in `output/<timestamp>/` (raw per-variant JSON, `summary.json`,
and the printed `summary.txt` table).

## Local dry-run (no AWS)

The instance-side harness runs on any Docker host:

```bash
cd remote && N=30 C=5 GATEWAYS="gomodel litellm portkey bifrost" ./run-on-instance.sh
```

(Build the GoModel image first: `docker build -t gomodel-bench:local ../../..`)

## Reproducibility & caveats

- **Pinned**: gateway image refs (overridable via `*_IMAGE` env), the Compose
  plugin version, instance type, and the deterministic mock payload. Exact image
  **digests** are recorded in each `*_image.json` so a run is fully traceable.
- **AMI** resolves to the latest Amazon Linux 2023 via SSM (reproducible by
  policy). Pin `var.ami_id` for a byte-identical OS.
- **Free tier**: defaults to **t2.micro** — the 12-month-free-tier instance in
  us-east-1 — with `standard` CPU credits (no surprise burst charges), a 20 GiB
  gp3 root volume (free tier allows 30 GiB), the default VPC (no paid NAT/EIP),
  and an Amazon Linux 2023 AMI. Image pulls are inbound traffic (free). In
  regions where t2.micro is unavailable, set `INSTANCE_TYPE=t3.micro` (the
  free-tier instance there). Newer accounts on AWS's credit-based free plan stay
  within credit for a single short run.
- **t2.micro is burstable** (1 vCPU, CPU-credit throttled). Treat absolute
  latency as *indicative*; the value is the *relative* comparison on identical
  hardware. Gateways run **one at a time** so they never contend, and the load is
  kept modest (N=500, c=10) to stay within launch credits. For production-grade
  absolute numbers, set `INSTANCE_TYPE=c7i.large` (not free tier).
- **Cost**: a single free-tier instance for ~15–30 min — $0 within free-tier
  allowance, otherwise a few cents.
