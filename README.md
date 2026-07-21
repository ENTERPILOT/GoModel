<p align="center">
  <img alt="GoModel logo" src="docs/logo.svg" width="96">
</p>

<h1 align="center">
  GoModel - AI Gateway in Go
</h1>

<p align="center">
  <a href="https://github.com/ENTERPILOT/GoModel/actions/workflows/test.yml"><img alt="CI" src="https://github.com/ENTERPILOT/GoModel/actions/workflows/test.yml/badge.svg"></a>
  <a href="https://github.com/ENTERPILOT/GoModel/blob/main/go.mod"><img alt="GO Version" src="https://img.shields.io/github/go-mod/go-version/ENTERPILOT/GoModel?label=GO"></a>
  <a href="https://hub.docker.com/r/enterpilot/gomodel"><img alt="Docker Pulls" src="https://img.shields.io/docker/pulls/enterpilot/gomodel?label=Docker%20Pulls"></a>
  <a href="https://discord.gg/gaEB9BQSPH"><img alt="Discord" src="https://img.shields.io/badge/Discord-Join-5865F2?logo=discord&logoColor=white"></a>
</p>

<p align="center">
  <a href="https://news.ycombinator.com/item?id=47849097"><img alt="Hacker News" src="https://img.shields.io/badge/Hacker%20News-Apr%2021%20%2726%20%7C%20%234-brightgreen?logo=ycombinator&logoColor=white"></a>
  <a href="https://gomodel.enterpilot.io/docs"><img alt="docs GoModel" src="https://img.shields.io/badge/Docs-GoModel-blue"></a>
</p>

<p align="center">
  <a href="https://news.ycombinator.com/item?id=47849097"><img alt="GoModel on Hacker News" src="https://hackerbadge.vercel.app/api?id=47849097"></a>
</p>

<p align="center">
  A fast and lightweight AI gateway written in Go, providing unified OpenAI-compatible and Anthropic-compatible APIs for OpenAI, Anthropic, Gemini, DeepSeek, xAI, Groq, OpenRouter, Z.ai, Azure OpenAI, Oracle, Ollama, and more.
</p>

<a href="docs/2026-07-07_demo.gif">
  <img src="docs/2026-07-07_demo.gif" alt="GoModel AI gateway dashboard showing AI usage analytics, observability panel, token and costs tracking, and estimated cost monitoring" width="100%">
</a>

## Quick Start

**Step 1:** Install and start GoModel

**macOS / Linux**

```bash
curl -fsSL https://gomodel.enterpilot.io/install.sh | sh
gomodel
```

**Windows (PowerShell)**

```powershell
irm https://gomodel.enterpilot.io/install.ps1 | iex
gomodel
```

**Docker**

```bash
docker run --rm -p 8080:8080 enterpilot/gomodel
```

**Step 2:** Add a provider

Open [http://localhost:8080/admin/dashboard/providers-config](http://localhost:8080/admin/dashboard/providers-config), select **Add Provider**, choose a provider type, and enter its credentials. The provider is available immediately without restarting GoModel.

**Step 3:** Make your first API call

This example uses an OpenAI model. If you added another provider, replace the
model ID with one shown on the dashboard's **Models** page.

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5-chat-latest",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

**That's it!** No provider environment variables or `config.yaml` are required.

For infrastructure-managed deployments, provider environment variables and `config.yaml` remain available as optional alternatives. See [`.env.template`](./.env.template) and [`config/config.example.yaml`](./config/config.example.yaml).

### Supported LLM Providers

GoModel supports OpenAI, Anthropic, Google Gemini, Vertex AI, DeepSeek, Groq,
Fireworks AI, Meta (Muse Spark), OpenRouter, Z.ai, xAI (Grok), Alibaba Cloud
Model Studio (Bailian), Kilo AI, MiniMax, Xiaomi MiMo, OpenCode Go, Azure OpenAI,
Oracle, Ollama, vLLM, Amazon Bedrock Runtime, Amazon Bedrock Mantle, and all
OpenAI-compatible providers.

See the [Providers Overview](./docs/providers/overview.mdx) for the full
per-provider feature matrix (chat, `/responses`, embeddings, files, batches,
passthrough), credentials, and configuration notes.

---

## Alternative Setup Methods

### Running from Source

**Prerequisites:** Go 1.26.4+

1. Start the server:

   ```bash
   make run
   ```

2. Add a provider from the dashboard's **Providers** page. To manage settings
   through environment variables instead, copy `.env.template` to `.env` and
   uncomment only the values you need.

### Docker Compose

**Infrastructure only** (Redis, PostgreSQL, MongoDB, Adminer - no image build):

```bash
docker compose up -d
# or: make infra
```

**Full stack** (adds GoModel + Prometheus; builds the app image):

```bash
cp .env.template .env
# Provider API keys are optional; you can add providers from the dashboard.
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
docker run --rm -p 8080:8080 gomodel
```

---

## API Endpoints

GoModel exposes OpenAI-compatible and Anthropic-compatible APIs, provider-native
passthrough, and operations routes. See the
[API Endpoints reference](./docs/advanced/api-endpoints.mdx) for the full
endpoint tables, and [Admin Endpoints](./docs/advanced/admin-endpoints.mdx) for
the admin REST API and dashboard.

---

## Gateway Configuration

Provider credentials can be managed from the dashboard. Environment variables
and an optional `config.yaml` are available for declarative configuration, with
environment variables overriding YAML values. See the [Configuration reference](./docs/advanced/configuration.mdx),
[`.env.template`](./.env.template), and [`config/config.example.yaml`](./config/config.example.yaml).

**Quick Start - Authentication:** By default `GOMODEL_MASTER_KEY` is unset. Without this key, API endpoints are unprotected and anyone can call them. This is insecure for production. **Strongly recommend** setting a strong secret before exposing the service. Add `GOMODEL_MASTER_KEY` to your `.env` or environment for production deployments.

---

See [DEVELOPMENT.md](docs/DEVELOPMENT.md) for testing, linting, and pre-commit setup.

---

# Roadmap

See the [Roadmap](./docs/about/roadmap.mdx) for commercial features and the public 0.2.0 milestone.

## Community

Join our [Discord](https://discord.gg/gaEB9BQSPH) to connect with other GoModel users.
