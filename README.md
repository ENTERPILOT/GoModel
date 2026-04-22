# GoModel - AI Gateway Written in Go

[![CI](https://github.com/ENTERPILOT/GoModel/actions/workflows/test.yml/badge.svg)](https://github.com/ENTERPILOT/GoModel/actions/workflows/test.yml)
[![Docs](https://img.shields.io/badge/docs-gomodel-blue)](https://gomodel.enterpilot.io/docs)
[![Discord](https://img.shields.io/badge/Discord-Join-5865F2?logo=discord&logoColor=white)](https://discord.gg/gaEB9BQSPH)
[![Docker Pulls](https://img.shields.io/docker/pulls/enterpilot/gomodel)](https://hub.docker.com/r/enterpilot/gomodel)
[![Go Version](https://img.shields.io/github/go-mod/go-version/ENTERPILOT/GoModel)](https://github.com/ENTERPILOT/GoModel/blob/main/go.mod)

A high-performance AI gateway written in Go, providing a unified OpenAI-compatible API for OpenAI, Anthropic, Gemini, xAI, Groq, OpenRouter, Z.ai, Azure OpenAI, Oracle, Ollama, and more.

<a href="docs/dashboard.gif">
  <img src="docs/dashboard.gif" alt="Animated GoModel AI gateway dashboard showing usage analytics, token tracking, and estimated cost monitoring" width="100%">
</a>

## Quick Start - Deploy the AI Gateway

**Step 1:** Start GoModel

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
  -e ZAI_API_KEY="your-zai-key" \
  -e XAI_API_KEY="your-xai-key" \
  -e AZURE_API_KEY="your-azure-key" \
  -e AZURE_BASE_URL="https://your-resource.openai.azure.com/openai/deployments/your-deployment" \
  -e AZURE_API_VERSION="2024-10-21" \
  -e ORACLE_API_KEY="your-oracle-key" \
  -e ORACLE_BASE_URL="https://inference.generativeai.us-chicago-1.oci.oraclecloud.com/20231130/actions/v1" \
  -e ORACLE_MODELS="openai.gpt-oss-120b,xai.grok-3" \
  -e OLLAMA_BASE_URL="http://host.docker.internal:11434/v1" \
  -e VLLM_BASE_URL="http://host.docker.internal:8000/v1" \
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

**That's it!** GoModel automatically detects which providers are available based on the credentials you supply.

### Supported LLM Providers

Example model identifiers are illustrative and subject to change; consult provider catalogs for current models. Feature columns reflect gateway API support, not every individual model capability exposed by an upstream provider.

| Provider      | Credential                                                        | Example Model              | Chat | `/responses` | Embed | Files | Batches | Passthru |
| ------------- | ----------------------------------------------------------------- | -------------------------- | :--: | :----------: | :---: | :---: | :-----: | :------: |
| OpenAI        | `OPENAI_API_KEY`                                                  | `gpt-4o-mini`              |  ✅  |      ✅      |  ✅   |  ✅   |   ✅    |    ✅    |
| Anthropic     | `ANTHROPIC_API_KEY`                                               | `claude-sonnet-4-20250514` |  ✅  |      ✅      |  ❌   |  ❌   |   ✅    |    ✅    |
| Google Gemini | `GEMINI_API_KEY`                                                  | `gemini-2.5-flash`         |  ✅  |      ✅      |  ✅   |  ✅   |   ✅    |    ❌    |
| Groq          | `GROQ_API_KEY`                                                    | `llama-3.3-70b-versatile`  |  ✅  |      ✅      |  ✅   |  ✅   |   ✅    |    ❌    |
| OpenRouter    | `OPENROUTER_API_KEY`                                              | `google/gemini-2.5-flash`  |  ✅  |      ✅      |  ✅   |  ✅   |   ✅    |    ✅    |
| Z.ai          | `ZAI_API_KEY` (`ZAI_BASE_URL` optional)                           | `glm-5.1`                  |  ✅  |      ✅      |  ✅   |  ❌   |   ❌    |    ✅    |
| xAI (Grok)    | `XAI_API_KEY`                                                     | `grok-2`                   |  ✅  |      ✅      |  ✅   |  ✅   |   ✅    |    ❌    |
| Azure OpenAI  | `AZURE_API_KEY` + `AZURE_BASE_URL` (`AZURE_API_VERSION` optional) | `gpt-4o`                   |  ✅  |      ✅      |  ✅   |  ✅   |   ✅    |    ✅    |
| Oracle        | `ORACLE_API_KEY` + `ORACLE_BASE_URL`                              | `openai.gpt-oss-120b`      |  ✅  |      ✅      |  ❌   |  ❌   |   ❌    |    ❌    |
| Ollama        | `OLLAMA_BASE_URL`                                                 | `llama3.2`                 |  ✅  |      ✅      |  ✅   |  ❌   |   ❌    |    ❌    |
| vLLM          | `VLLM_BASE_URL` (`VLLM_API_KEY` optional)                         | `meta-llama/Llama-3.1-8B-Instruct` | ✅ | ✅ | ✅ | ❌ | ❌ | ✅ |

✅ Supported ❌ Unsupported

For Z.ai's GLM Coding Plan, set `ZAI_BASE_URL=https://api.z.ai/api/coding/paas/v4`.
For Oracle, set `ORACLE_MODELS=openai.gpt-oss-120b,xai.grok-3` when the
upstream `/models` endpoint is unavailable.
For vLLM, set `VLLM_API_KEY` only if the upstream server was started with
`--api-key`.

---

## Alternative Setup Methods

### Running from Source

**Prerequisites:** Go 1.26.2+

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

**Infrastructure only** (Redis, PostgreSQL, MongoDB, Adminer - no image build):

```bash
docker compose up -d
# or: make infra
```

**Full stack** (adds GoModel + Prometheus; builds the app image):

```bash
cp .env.template .env
# Add your API keys to .env
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

## OpenAI-Compatible API Endpoints

| Endpoint                           | Method                                       | Description                                                                                                  |
| ---------------------------------- | -------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| `/v1/chat/completions`             | POST                                         | Chat completions (streaming supported)                                                                       |
| `/v1/responses`                    | POST                                         | OpenAI Responses API                                                                                         |
| `/v1/embeddings`                   | POST                                         | Text embeddings                                                                                              |
| `/v1/files`                        | POST                                         | Upload a file (OpenAI-compatible multipart)                                                                  |
| `/v1/files`                        | GET                                          | List files                                                                                                   |
| `/v1/files/{id}`                   | GET                                          | Retrieve file metadata                                                                                       |
| `/v1/files/{id}`                   | DELETE                                       | Delete a file                                                                                                |
| `/v1/files/{id}/content`           | GET                                          | Retrieve raw file content                                                                                    |
| `/v1/batches`                      | POST                                         | Create a native provider batch (OpenAI-compatible schema; inline `requests` supported where provider-native) |
| `/v1/batches`                      | GET                                          | List stored batches                                                                                          |
| `/v1/batches/{id}`                 | GET                                          | Retrieve one stored batch                                                                                    |
| `/v1/batches/{id}/cancel`          | POST                                         | Cancel a pending batch                                                                                       |
| `/v1/batches/{id}/results`         | GET                                          | Retrieve native batch results when available                                                                 |
| `/p/{provider}/...`                | GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS | Provider-native passthrough with opaque upstream responses                                                   |
| `/v1/models`                       | GET                                          | List available models                                                                                        |
| `/health`                          | GET                                          | Health check                                                                                                 |
| `/metrics`                         | GET                                          | Prometheus metrics (when enabled)                                                                            |
| `/admin/api/v1/usage/summary`      | GET                                          | Aggregate token usage statistics                                                                             |
| `/admin/api/v1/usage/daily`        | GET                                          | Per-period token usage breakdown                                                                             |
| `/admin/api/v1/usage/models`       | GET                                          | Usage breakdown by model                                                                                     |
| `/admin/api/v1/usage/log`          | GET                                          | Paginated usage log entries                                                                                  |
| `/admin/api/v1/audit/log`          | GET                                          | Paginated audit log entries                                                                                  |
| `/admin/api/v1/audit/conversation` | GET                                          | Conversation thread around one audit log entry                                                               |
| `/admin/api/v1/models`             | GET                                          | List models with provider type                                                                               |
| `/admin/api/v1/models/categories`  | GET                                          | List model categories                                                                                        |
| `/admin/dashboard`                 | GET                                          | Admin dashboard UI                                                                                           |
| `/swagger/index.html`              | GET                                          | Swagger UI (when enabled)                                                                                    |

---

## Gateway Configuration

GoModel is configured through environment variables and an optional `config.yaml`. Environment variables override YAML values. See [`.env.template`](.env.template) and [`config/config.example.yaml`](config/config.example.yaml) for the available options.

Key settings:

| Variable                        | Default                           | Description                                                                      |
| ------------------------------- | --------------------------------- | -------------------------------------------------------------------------------- |
| `PORT`                          | `8080`                            | Server port                                                                      |
| `GOMODEL_MASTER_KEY`            | (none)                            | API key for authentication                                                       |
| `ENABLE_PASSTHROUGH_ROUTES`     | `true`                            | Enable provider-native passthrough routes under `/p/{provider}/...`              |
| `ALLOW_PASSTHROUGH_V1_ALIAS`    | `true`                            | Allow `/p/{provider}/v1/...` aliases while keeping `/p/{provider}/...` canonical |
| `ENABLED_PASSTHROUGH_PROVIDERS` | `openai,anthropic,openrouter,zai,vllm` | Comma-separated list of enabled passthrough providers                            |
| `STORAGE_TYPE`                  | `sqlite`                          | Storage backend (`sqlite`, `postgresql`, `mongodb`)                              |
| `METRICS_ENABLED`               | `false`                           | Enable Prometheus metrics                                                        |
| `LOGGING_ENABLED`               | `false`                           | Enable audit logging                                                             |
| `GUARDRAILS_ENABLED`            | `false`                           | Enable the configured guardrails pipeline                                        |

**Quick Start - Authentication:** By default `GOMODEL_MASTER_KEY` is unset. Without this key, API endpoints are unprotected and anyone can call them. This is insecure for production. **Strongly recommend** setting a strong secret before exposing the service. Add `GOMODEL_MASTER_KEY` to your `.env` or environment for production deployments.

---

## Response Caching

GoModel has a two-layer response cache that reduces LLM API costs and latency for repeated or semantically similar requests.

### Layer 1 - Exact-match cache

Hashes the full request body (path + `Workflow` + body) and returns a stored response on byte-identical requests. Sub-millisecond lookup. Activate by environment variables: `RESPONSE_CACHE_SIMPLE_ENABLED` and `REDIS_URL`.

Responses served from this layer carry `X-Cache: HIT (exact)`.

### Layer 2 - Semantic cache

Embeds the last user message via your configured provider’s OpenAI-compatible `/v1/embeddings` API (`cache.response.semantic.embedder.provider` must name a key in the top-level `providers` map) and performs a KNN vector search. Semantically equivalent queries - e.g. _"What's the capital of France?"_ vs _"Which city is France's capital?"_ - can return the same cached response without an upstream LLM call.

Expected hit rates: ~60–70% in high-repetition workloads vs. ~18% for exact-match alone.

Responses served from this layer carry `X-Cache: HIT (semantic)`.

Supported vector backends: `qdrant`, `pgvector`, `pinecone`, `weaviate` (set `cache.response.semantic.vector_store.type` and the matching nested block).

Both cache layers run **after** guardrail/workflow patching so they always see the final prompt. Use `Cache-Control: no-cache` or `Cache-Control: no-store` to bypass caching per-request.

---

See [DEVELOPMENT.md](DEVELOPMENT.md) for testing, linting, and pre-commit setup.

---

# Roadmap to 0.2.0

### Must Have

- [ ] Intelligent routing
- [ ] Broader provider support: Oracle model configuration via environment variables, plus Cohere, Command A, Operational, and DeepSeek V3
- [ ] Budget management with limits per `user_path` and/or API key
- [ ] Editable model pricing for accurate cost tracking and budgeting
- [ ] Full support for the OpenAI `/responses` and `/conversations` lifecycle
- [ ] Prompt cache visibility showing how much of each prompt was cached by the provider
- [ ] Guardrails hardening: better UI, simpler architecture, easier custom guardrails, and response-side guardrails before output reaches the client
- [ ] Passthrough for all providers, beyond the current OpenAI and Anthropic beta
- [ ] Fix failover charts in the dashboard

### Should Have

- [ ] Cluster mode

## Community

Join our [Discord](https://discord.gg/gaEB9BQSPH) to connect with other GoModel users.

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=enterpilot/gomodel&type=date&legend=top-left)](https://www.star-history.com/#enterpilot/gomodel&type=date&legend=top-left)
