# GOModel

[![CI](https://github.com/ENTERPILOT/GOModel/actions/workflows/test.yml/badge.svg)](https://github.com/ENTERPILOT/GOModel/actions/workflows/test.yml)
[![Docs](https://img.shields.io/badge/docs-gomodel-blue)](https://gomodel.enterpilot.io/docs)
[![Discord](https://img.shields.io/badge/Discord-Join-5865F2?logo=discord&logoColor=white)](https://discord.gg/gaEB9BQSPH)

A high-performance AI gateway written in Go, providing a unified OpenAI-compatible API for multiple AI model providers, full-observability and more.

## Quick Start

**Step 1:** Start GOModel

```bash
docker run --rm -p 8080:8080 \
  -e OPENAI_API_KEY="your-openai-key" \
  enterpilot/gomodel
```

Pass only the provider credentials or base URL you need (at least one required):

```bash
docker run --rm -p 8080:8080 \
  -e OPENAI_API_KEY="your-openai-key" \
  -e ANTHROPIC_API_KEY="your-anthropic-key" \
  -e GEMINI_API_KEY="your-gemini-key" \
  -e GROQ_API_KEY="your-groq-key" \
  -e XAI_API_KEY="your-xai-key" \
  -e OLLAMA_BASE_URL="http://host.docker.internal:11434/v1" \
  enterpilot/gomodel
```

⚠️ Avoid passing secrets via `-e` on the command line—they can leak via shell history and process lists. For production, use `docker run --env-file .env` to load API keys from a file instead.

**Step 2:** Make your first API call

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5-chat-latest",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

**That's it!** GOModel automatically detects which providers are available based on the credentials you supply.

### Supported Providers

Example model identifiers are illustrative and subject to change; consult provider catalogs for current models. Feature columns reflect gateway API support, not every individual model capability exposed by an upstream provider.

| Provider | Credential | Example Model | Chat | `/responses` | Embed | Files | Batches | Passthru |
|----------|------------|---------------|:----:|:------------:|:-----:|:-----:|:-------:|:--------:|
| OpenAI | `OPENAI_API_KEY` | `gpt-4o-mini` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Anthropic | `ANTHROPIC_API_KEY` | `claude-sonnet-4-20250514` | ✅ | ✅ | ❌ | ❌ | ✅ | ✅ |
| Google Gemini | `GEMINI_API_KEY` | `gemini-2.5-flash` | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ |
| Groq | `GROQ_API_KEY` | `llama-3.3-70b-versatile` | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ |
| xAI (Grok) | `XAI_API_KEY` | `grok-2` | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ |
| Ollama | `OLLAMA_BASE_URL` | `llama3.2` | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ |

✅ Supported  ❌ Unsupported

---

## Alternative Setup Methods

### Running from Source

**Prerequisites:** Go 1.22+

1. Create a `.env` file:

   ```bash
   cp .env.template .env
   ```

2. Add your API keys to `.env` (at least one required).

3. Start the server:

   ```bash
   make run
   ```

### Docker Compose (Full Stack)

Includes GOModel + Redis + PostgreSQL + MongoDB + Adminer + Prometheus:

```bash
cp .env.template .env
# Add your API keys to .env
docker compose up -d
```

| Service | URL |
|---------|-----|
| GOModel API | http://localhost:8080 |
| Adminer (DB UI) | http://localhost:8081 |
| Prometheus | http://localhost:9090 |

### Building the Docker Image Locally

```bash
docker build -t gomodel .
docker run --rm -p 8080:8080 --env-file .env gomodel
```

---

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/chat/completions` | POST | Chat completions (streaming supported) |
| `/v1/responses` | POST | OpenAI Responses API |
| `/v1/embeddings` | POST | Text embeddings |
| `/v1/files` | POST | Upload a file (OpenAI-compatible multipart) |
| `/v1/files` | GET | List files |
| `/v1/files/{id}` | GET | Retrieve file metadata |
| `/v1/files/{id}` | DELETE | Delete a file |
| `/v1/files/{id}/content` | GET | Retrieve raw file content |
| `/v1/batches` | POST | Create a native provider batch (OpenAI-compatible schema; inline `requests` supported where provider-native) |
| `/v1/batches` | GET | List stored batches |
| `/v1/batches/{id}` | GET | Retrieve one stored batch |
| `/v1/batches/{id}/cancel` | POST | Cancel a pending batch |
| `/v1/batches/{id}/results` | GET | Retrieve native batch results when available |
| `/p/{provider}/...` | GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS | Provider-native passthrough with opaque upstream responses |
| `/v1/models` | GET | List available models |
| `/health` | GET | Health check |
| `/metrics` | GET | Prometheus metrics (when enabled) |
| `/admin/api/v1/usage/summary` | GET | Aggregate token usage statistics |
| `/admin/api/v1/usage/daily` | GET | Per-period token usage breakdown |
| `/admin/api/v1/usage/models` | GET | Usage breakdown by model |
| `/admin/api/v1/usage/log` | GET | Paginated usage log entries |
| `/admin/api/v1/audit/log` | GET | Paginated audit log entries |
| `/admin/api/v1/audit/conversation` | GET | Conversation thread around one audit log entry |
| `/admin/api/v1/models` | GET | List models with provider type |
| `/admin/api/v1/models/categories` | GET | List model categories |
| `/admin/dashboard` | GET | Admin dashboard UI |
| `/swagger/index.html` | GET | Swagger UI (when enabled) |

---

## Configuration

GOModel is configured through environment variables and an optional `config.yaml`. Environment variables override YAML values. See [`.env.template`](.env.template) and [`config/config.example.yaml`](config/config.example.yaml) for the available options.

Key settings:

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | Server port |
| `GOMODEL_MASTER_KEY` | (none) | API key for authentication |
| `ENABLE_PASSTHROUGH_ROUTES` | `true` | Enable provider-native passthrough routes under `/p/{provider}/...` |
| `ALLOW_PASSTHROUGH_V1_ALIAS` | `true` | Allow `/p/{provider}/v1/...` aliases while keeping `/p/{provider}/...` canonical |
| `ENABLED_PASSTHROUGH_PROVIDERS` | `openai,anthropic` | Comma-separated list of enabled passthrough providers |
| `CACHE_TYPE` | `local` | Cache backend (`local` or `redis`) |
| `STORAGE_TYPE` | `sqlite` | Storage backend (`sqlite`, `postgresql`, `mongodb`) |
| `METRICS_ENABLED` | `false` | Enable Prometheus metrics |
| `LOGGING_ENABLED` | `false` | Enable audit logging |
| `GUARDRAILS_ENABLED` | `false` | Enable the configured guardrails pipeline |

**Quick Start — Authentication:** By default `GOMODEL_MASTER_KEY` is unset. Without this key, API endpoints are unprotected and anyone can call them. This is insecure for production. **Strongly recommend** setting a strong secret before exposing the service. Add `GOMODEL_MASTER_KEY` to your `.env` or environment for production deployments.

---

See [DEVELOPMENT.md](DEVELOPMENT.md) for testing, linting, and pre-commit setup.

---

# Roadmap

## Shipped

| Area | Status | Notes |
| ---- | :----: | ----- |
| OpenAI-compatible API surface | ✅ | `/v1/chat/completions`, `/v1/responses`, `/v1/embeddings`, `/v1/files*`, `/v1/batches*`, and `/v1/models` are implemented. |
| Provider passthrough | ✅ | Provider-native passthrough routes are available under `/p/{provider}/...`. |
| Observability | ✅ | Prometheus metrics, audit logging, usage tracking, request IDs, and trace-header capture are implemented. |
| Administrative endpoints | ✅ | Admin API and dashboard ship with usage, audit, and model views. |
| Guardrails | ✅ | The guardrails pipeline is implemented and can be enabled from config. |
| System prompt guardrails | ✅ | `inject`, `override`, and `decorator` modes are supported. |

## In Progress

| Area | Status | Notes |
| ---- | :----: | ----- |
| Billing management | 🚧 | Usage and pricing primitives exist, but billing workflows are not complete. |
| Budget management | 🚧 | Gateway-level budget enforcement and policy controls are not implemented yet. |
| Guardrails depth | 🚧 | The system prompt guardrail is available today; broader guardrail types are still to come. |
| Observability integrations | 🚧 | Native Prometheus support exists; OpenTelemetry and DataDog integrations are still pending. |

## Planned

| Area | Status | Notes |
| ---- | :----: | ----- |
| Many keys support | 🚧 | The gateway still uses one configured credential/base URL per provider. |
| SSO / OIDC | 🚧 | No SSO implementation is present yet. |

✅ Shipped  🚧 Planned or in progress

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=enterpilot/gomodel&type=date&legend=top-left)](https://www.star-history.com/#enterpilot/gomodel&type=date&legend=top-left)
