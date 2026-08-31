<p align="center">
  <img alt="GoModel logo" src="docs/logo.svg" width="96">
</p>

<h1 align="center">
  GoModel - The last AI gateway you will ever need
</h1>

<p align="center">
  <a href="https://github.com/ENTERPILOT/GoModel/actions/workflows/test.yml"><img alt="CI" src="https://github.com/ENTERPILOT/GoModel/actions/workflows/test.yml/badge.svg"></a>
  <a href="https://github.com/ENTERPILOT/GoModel/blob/main/go.mod"><img alt="GO Version" src="https://img.shields.io/github/go-mod/go-version/ENTERPILOT/GoModel?label=GO"></a>
  <a href="https://hub.docker.com/r/enterpilot/gomodel"><img alt="Docker Pulls" src="https://img.shields.io/docker/pulls/enterpilot/gomodel?label=Docker%20Pulls"></a>
  <a href="https://discord.gg/gaEB9BQSPH"><img alt="Discord" src="https://img.shields.io/badge/Discord-Join-5865F2?logo=discord&logoColor=white"></a>
</p>

<p align="center">
  <a href="https://news.ycombinator.com/item?id=47849097"><img alt="Hacker News" src="https://img.shields.io/badge/Hacker%20News-Apr%2021%20%2726%20%7C%20%234-brightgreen?logo=ycombinator&logoColor=white"></a>
  <a href="https://gomodel.enterpilot.io/docs?utm_source=readme"><img alt="docs GoModel" src="https://img.shields.io/badge/Docs-GoModel-blue"></a>
</p>

<p align="center">
  <a href="https://news.ycombinator.com/item?id=47849097"><img alt="GoModel on Hacker News" src="https://hackerbadge.vercel.app/api?id=47849097"></a>
</p>

<p align="center">
  GoModel is the fastest and the most resource-efficient AI Gateway (<a href="https://gomodel.enterpilot.io/docs/about/benchmarks?utm_source=readme">the self-reproducible benchmarks</a>). It's an alternative to LiteLLM (which was hacked recently) and Portkey (which is no longer maintained on GitHub).
</p>

<a href="https://demo.enterpilot.io/admin/dashboard?utm_source=readme">
  <img src="docs/2026-07-07_demo.gif" alt="GoModel AI gateway dashboard showing AI usage analytics, observability panel, token and costs tracking, and estimated cost monitoring" width="100%">
</a>
<p align="center">
  (click on the animation ↑ to see the live demo)
</p>

<p>
  GoModel saves you money and nerves.
</p>
<p>
  <strong>Money</strong> - because you can remember the responses on this layer (caching), track your spending and do tricks like prompt compression and intelligent routing.
</p>
<p>
  <strong>Nerves</strong> - because we strive to achieve good quality and reliability. Our ambition is to be the last AI gateway you will need - the most reliable, resource-optimal, feature-rich and fast.
</p>

## Quick Start

**Step 1:** Install and start GoModel

**macOS / Linux**

```bash
curl -fsSL https://gomodel.enterpilot.io/install.sh | sh
# OPENAI_API_KEY="your-openai-key" # (optional)
gomodel
```

**Windows (PowerShell)**

```powershell
irm https://gomodel.enterpilot.io/install.ps1 | iex
# $env:OPENAI_API_KEY = "your-openai-key" # (optional)
gomodel
```

**Docker**

```bash
docker run --rm -p 8080:8080 \
  -e OPENAI_API_KEY="your-openai-key" \
  enterpilot/gomodel
```

ℹ️ You can configure GoModel with `.env` variables, a `config.yaml` file, OR directly in the dashboard.

ℹ️ Full list of environment variables (including all available providers): [`.env.template`](./.env.template)

ℹ️ The most secure way in production is to use `.env` to load API keys.

**Step 2:** Open the dashboard

```
http://localhost:8080/admin/dashboard
```

**Step 3:** Make your first API call

```bash
curl http://localhost:8080/v1/responses \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5-chat-latest",
    "input": "Hello!"
  }'
```

## Using GoModel with official SDKs

GoModel exposes an OpenAI-compatible API at `/v1` and an Anthropic-compatible
API at `/v1/messages`, so the official SDKs work unchanged - just point the
base URL at your GoModel server and use your GoModel key
(set up with the `GOMODEL_MASTER_KEY` env variable or one generated in the dashboard).

### OpenAI SDK

**Python**

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8080/v1",  # your GoModel server
    api_key="your-gomodel-key",
)
```

**TypeScript / JavaScript**

```typescript
import OpenAI from "openai";

const client = new OpenAI({
  baseURL: "http://localhost:8080/v1", // your GoModel server
  apiKey: "your-gomodel-key",
});
```

### Anthropic SDK

The Anthropic SDK authenticates with `x-api-key`, which GoModel accepts
alongside `Authorization: Bearer`.

**Python**

```python
from anthropic import Anthropic

client = Anthropic(
    base_url="http://localhost:8080",  # your GoModel server (no /v1 suffix)
    api_key="your-gomodel-key",
)
```

**TypeScript / JavaScript**

```typescript
import Anthropic from "@anthropic-ai/sdk";

const client = new Anthropic({
  baseURL: "http://localhost:8080", // your GoModel server (no /v1 suffix)
  apiKey: "your-gomodel-key",
});
```

## Supported LLM Providers

GoModel supports OpenAI, Anthropic, Cohere, Google Gemini, Vertex AI, DeepSeek,
Groq, Fireworks AI, Meta (Muse Spark), OpenRouter, Z.ai, xAI (Grok), Alibaba
Cloud Model Studio (Bailian), Kilo AI, MiniMax, Xiaomi MiMo, OpenCode Go, Azure
OpenAI, Oracle, Ollama, SGLang, vLLM, llm-d, Amazon Bedrock Runtime, Amazon
Bedrock Mantle, and all OpenAI-compatible providers. Subscription-billed:
ChatGPT (the Codex backend) and Claude. Voice: ElevenLabs (text-to-speech and
speech-to-text).

See the [Providers Overview](https://gomodel.enterpilot.io/docs/providers/overview?utm_source=readme) for the full
per-provider feature matrix (chat, `/responses`, embeddings, files, batches,
passthrough), credentials, and configuration notes.

---

## Docker Compose

**Infrastructure only** (Redis, PostgreSQL, MongoDB, Adminer - no image build):

```bash
cp .env.template .env
# Add your API keys to .env
docker compose up -d
# or: make infra
```

**Full stack** (adds GoModel + Prometheus; builds the app image):

```bash
docker compose --profile app up -d
# or: make image
```

| Service         | URL                   |
| --------------- | --------------------- |
| GoModel API     | http://localhost:8080 |
| Adminer (DB UI) | http://localhost:8081 |
| Prometheus      | http://localhost:9090 |

### Building the Docker Image Locally

```bash
docker build -t gomodel .
docker run --rm -p 8080:8080 --env-file .env gomodel
```

---

## API Endpoints

GoModel exposes OpenAI-compatible and Anthropic-compatible APIs, provider-native
passthrough, and operations routes. See the
[API Endpoints reference](https://gomodel.enterpilot.io/docs/advanced/api-endpoints?utm_source=readme) for the full
endpoint tables, and [Admin Endpoints](https://gomodel.enterpilot.io/docs/advanced/admin-endpoints?utm_source=readme) for
the admin REST API and dashboard.

---

## Gateway Configuration

GoModel is configured through environment variables and an optional `config.yaml`. Environment variables override YAML values. See the [Configuration reference](https://gomodel.enterpilot.io/docs/advanced/configuration?utm_source=readme) for the full list of settings organized by category, along with [`.env.template`](./.env.template) and [`config/config.example.yaml`](./config/config.example.yaml).

---

## Features

- [Caching](https://gomodel.enterpilot.io/docs/features/cache?utm_source=readme) - exact and semantic response caching, so repeated prompts cost nothing
- [Cost tracking](https://gomodel.enterpilot.io/docs/features/cost-tracking?utm_source=readme) - per-request cost estimates, usage analytics, and spending breakdowns in the dashboard
- [Budgets](https://gomodel.enterpilot.io/docs/features/budgets?utm_source=readme) - hard spend limits per user, team, or key
- [Rate limits](https://gomodel.enterpilot.io/docs/features/rate-limits?utm_source=readme) - requests, tokens, and concurrency caps per user path, provider, or model
- [Virtual models](https://gomodel.enterpilot.io/docs/features/virtual-models?utm_source=readme) - aliases and load balancing (round-robin or cost-based) behind stable model names
- [Failover](https://gomodel.enterpilot.io/docs/features/failover?utm_source=readme) - automatic rerouting to backup providers, with [retries and circuit breakers](https://gomodel.enterpilot.io/docs/advanced/resilience?utm_source=readme)
- [Labelling](https://gomodel.enterpilot.io/docs/features/labelling?utm_source=readme) - tag requests from HTTP headers or API keys and break down usage by label
- [User paths](https://gomodel.enterpilot.io/docs/features/user-path?utm_source=readme) - hierarchical scoping of keys, model access, budgets, usage, and audit logs
- [MCP gateway](https://gomodel.enterpilot.io/docs/features/mcp-gateway?utm_source=readme) - aggregate your MCP servers behind one authenticated endpoint
- [Passthrough API](https://gomodel.enterpilot.io/docs/features/passthrough-api?utm_source=readme) - provider-native APIs under `/p/{provider}/...`, with GoModel auth and tracking
- [Guardrails](https://gomodel.enterpilot.io/docs/advanced/guardrails?utm_source=readme) - request and response policies enforced at the gateway
- [Provider key rotation](https://gomodel.enterpilot.io/docs/providers/key-rotation?utm_source=readme) - round-robin over multiple API keys to lift per-key rate limits
- [Observability](https://gomodel.enterpilot.io/docs/guides/prometheus-metrics?utm_source=readme) - Prometheus metrics, [OpenTelemetry](https://gomodel.enterpilot.io/docs/guides/opentelemetry?utm_source=readme) traces, audit logs, and live request streaming in the dashboard

## GoModel Pro

[GoModel Pro](https://gomodel.enterpilot.io/pro?utm_source=readme) is the commercial distribution of GoModel: the same gateway, one binary, with licensed features on top of the same MIT core -

- [Prompt compression](https://gomodel.enterpilot.io/docs/pro/compression?utm_source=readme) - 2-20% fewer input tokens on agentic traffic; nothing summarized, nothing dropped, and prompt caches keep hitting
- [OIDC single sign-on](https://gomodel.enterpilot.io/docs/pro/sso?utm_source=readme) - Okta, Entra ID, Google, Auth0, or Cognito in front of the dashboard, mapped onto your user-path hierarchy
- [Per-child quota templates](https://gomodel.enterpilot.io/docs/features/budgets?utm_source=readme) - one budget or rate-limit rule on a parent path, independent counters for every child
- Intelligent routing (beta) - easy/hard model tiering with adaptive selection

The open-source gateway is not a demo of the paid one: everything in this repository stays MIT-licensed. The Pro license is flat per company - no seat, request, or node counting - validated offline, so it works air-gapped. See [pricing and details](https://gomodel.enterpilot.io/pro?utm_source=readme).

## Roadmap

See the [Roadmap](https://gomodel.enterpilot.io/docs/about/roadmap?utm_source=readme) for commercial features and the public 0.2.0 milestone.

## Sponsors

<a href="https://github.com/Neiko2002"><img src="https://github.com/Neiko2002.png" alt="Neiko2002" width="64"></a>

## Community

Join our [Discord](https://discord.gg/gaEB9BQSPH) to connect with other GoModel users.
