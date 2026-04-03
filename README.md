# GOModel - AI Gateway Written in Go

[![CI](https://github.com/ENTERPILOT/GOModel/actions/workflows/test.yml/badge.svg)](https://github.com/ENTERPILOT/GOModel/actions/workflows/test.yml)
[![Docs](https://img.shields.io/badge/docs-gomodel-blue)](https://gomodel.enterpilot.io/docs)
[![Discord](https://img.shields.io/badge/Discord-Join-5865F2?logo=discord&logoColor=white)](https://discord.gg/gaEB9BQSPH)
[![Docker Pulls](https://img.shields.io/docker/pulls/enterpilot/gomodel)](https://hub.docker.com/r/enterpilot/gomodel)
[![Go Version](https://img.shields.io/github/go-mod/go-version/ENTERPILOT/GOModel)](https://github.com/ENTERPILOT/GOModel/blob/main/go.mod)

A high-performance AI gateway written in Go, providing a unified OpenAI-compatible API for OpenAI, Anthropic, Gemini, xAI, Groq, OpenRouter, Azure OpenAI, Oracle, Ollama, and more.

<a href="docs/dashboard.gif">
  <img src="docs/dashboard.gif" alt="Animated GOModel AI gateway dashboard showing usage analytics, token tracking, and estimated cost monitoring" width="100%">
</a>

## Quick Start - Deploy the AI Gateway

**Step 1:** Start GOModel

```bash
docker run --rm -p 8080:8080 \
  -e LOGGING_ENABLED=true \
  -e LOGGING_LOG_BODIES=true \
  -e LOG_FORMAT=text \
  -e LOGGING_LOG_HEADERS=true \
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
  -e OPENROUTER_API_KEY="your-openrouter-key" \
  -e XAI_API_KEY="your-xai-key" \
  -e AZURE_API_KEY="your-azure-key" \
  -e AZURE_BASE_URL="https://your-resource.openai.azure.com/openai/deployments/your-deployment" \
  -e AZURE_API_VERSION="2024-10-21" \
  -e ORACLE_API_KEY="your-oracle-key" \
  -e ORACLE_BASE_URL="https://inference.generativeai.us-chicago-1.oci.oraclecloud.com/20231130/actions/v1" \
  -e OLLAMA_BASE_URL="http://host.docker.internal:11434/v1" \
  enterpilot/gomodel
```

⚠️ Avoid passing secrets via `-e` on the command line - they can leak via shell history and process lists. For production, use `docker run --env-file .env` to load API keys from a file instead.

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

### Supported LLM Providers

Example model identifiers are illustrative and subject to change; consult provider catalogs for current models. Feature columns reflect gateway API support, not every individual model capability exposed by an upstream provider.

| Provider | Credential | Example Model | Chat | `/responses` | Embed | Files | Batches | Passthru |
|----------|------------|---------------|:----:|:------------:|:-----:|:-----:|:-------:|:--------:|
| OpenAI | `OPENAI_API_KEY` | `gpt-4o-mini` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Anthropic | `ANTHROPIC_API_KEY` | `claude-sonnet-4-20250514` | ✅ | ✅ | ❌ | ❌ | ✅ | ✅ |
| Google Gemini | `GEMINI_API_KEY` | `gemini-2.5-flash` | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ |
| Groq | `GROQ_API_KEY` | `llama-3.3-70b-versatile` | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ |
| OpenRouter | `OPENROUTER_API_KEY` | `google/gemini-2.5-flash` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| xAI (Grok) | `XAI_API_KEY` | `grok-2` | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ |
| Azure OpenAI | `AZURE_API_KEY` + `AZURE_BASE_URL` (`AZURE_API_VERSION` optional) | `gpt-4o` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Oracle | `ORACLE_API_KEY` + `ORACLE_BASE_URL` | `openai.gpt-oss-120b` | ✅ | ✅ | ❌ | ❌ | ❌ | ❌ |
| Ollama | `OLLAMA_BASE_URL` | `llama3.2` | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ |

✅ Supported  ❌ Unsupported

---

## Alternative Setup Methods

### Running from Source

**Prerequisites:** Go 1.26+

1. Create a `.env` file:

   ```bash
   cp .env.template .env
   ```

2. Add your API keys to `.env` (at least one required).

3. Start the server:

   ```bash
   make run
   ```

### Docker Compose

**Infrastructure only** (Redis, PostgreSQL, MongoDB, Adminer — no image build):

```bash
docker compose up -d
# or: make infra
```

**Full stack** (adds GOModel + Prometheus; builds the app image):

```bash
cp .env.template .env
# Add your API keys to .env
docker compose --profile app up -d
# or: make image
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

## OpenAI-Compatible API Endpoints

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

## Gateway Configuration

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

**Quick Start - Authentication:** By default `GOMODEL_MASTER_KEY` is unset. Without this key, API endpoints are unprotected and anyone can call them. This is insecure for production. **Strongly recommend** setting a strong secret before exposing the service. Add `GOMODEL_MASTER_KEY` to your `.env` or environment for production deployments.

---

## Response Caching

GOModel has a two-layer response cache that reduces LLM API costs and latency for repeated or semantically similar requests.

### Layer 1 — Exact-match cache

Hashes the full request body (path + `ExecutionPlan` + body) and returns a stored response on byte-identical requests. Sub-millisecond lookup. Activate by pointing it at Redis:

```yaml
# config/config.yaml
cache:
  response:
    simple:
      redis:
        url: redis://localhost:6379
        ttl: 3600   # seconds; default 3600
```

Or via environment variables: `REDIS_URL`, `REDIS_KEY_RESPONSES`, `REDIS_TTL_RESPONSES`.

Responses served from this layer carry `X-Cache: HIT (exact)`.

### Layer 2 — Semantic cache

Embeds the last user message via your configured provider’s OpenAI-compatible `/v1/embeddings` API (`cache.response.semantic.embedder.provider` must name a key in the top-level `providers` map) and performs a KNN vector search. Semantically equivalent queries — e.g. *"What's the capital of France?"* vs *"Which city is France's capital?"* — can return the same cached response without an upstream LLM call.

Expected hit rates: ~60–70% in high-repetition workloads vs. ~18% for exact-match alone.

Responses served from this layer carry `X-Cache: HIT (semantic)`.

Supported vector backends: `qdrant`, `pgvector`, `pinecone`, `weaviate` (set `cache.response.semantic.vector_store.type` and the matching nested block).

Both cache layers run **after** guardrail/execution-plan patching so they always see the final prompt. Use `Cache-Control: no-cache` or `Cache-Control: no-store` to bypass caching per-request.

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
| Semantic response cache | ✅ | Exact-match Redis plus optional semantic layer (API embeddings, `qdrant` / `pgvector` / `pinecone` / `weaviate`) — see [ADR-0006](docs/adr/0006-semantic-response-cache.md). |

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

## Community

Join our [Discord](https://discord.gg/gaEB9BQSPH) to connect with other GOModel users.

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=enterpilot/gomodel&type=date&legend=top-left)](https://www.star-history.com/#enterpilot/gomodel&type=date&legend=top-left)
