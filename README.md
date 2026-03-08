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

Example model identifiers are illustrative and subject to change; consult provider catalogs for current models.

<table>
  <tr>
    <th colspan="3">Provider</th>
    <th colspan="8">Features</th>
  </tr>
  <tr>
    <th>Name</th>
    <th>Credential</th>
    <th>Example&nbsp;Model</th>
    <th>Chat</th>
    <th>Passthru</th>
    <th>Voice</th>
    <th>Image</th>
    <th>Video</th>
    <th>/responses</th>
    <th>Embed</th>
    <th>Cache</th>
  </tr>
  <tr>
    <td>OpenAI</td>
    <td><code>OPENAI_API_KEY</code></td>
    <td><code>gpt&#8209;4o&#8209;mini</code></td>
    <td>✅</td><td>🚧</td><td>🚧</td><td>🚧</td><td>🚧</td><td>🚧</td><td>✅</td><td>🚧</td>
  </tr>
  <tr>
    <td>Anthropic</td>
    <td><code>ANTHROPIC_API_KEY</code></td>
    <td><code>claude&#8209;sonnet&#8209;4&#8209;20250514</code></td>
    <td>✅</td><td>🚧</td><td>🚧</td><td>🚧</td><td>🚧</td><td>🚧</td><td>❌</td><td>🚧</td>
  </tr>
  <tr>
    <td>Google&nbsp;Gemini</td>
    <td><code>GEMINI_API_KEY</code></td>
    <td><code>gemini&#8209;2.5&#8209;flash</code></td>
    <td>✅</td><td>🚧</td><td>🚧</td><td>🚧</td><td>🚧</td><td>🚧</td><td>✅</td><td>🚧</td>
  </tr>
  <tr>
    <td>Groq</td>
    <td><code>GROQ_API_KEY</code></td>
    <td><code>llama&#8209;3.3&#8209;70b&#8209;versatile</code></td>
    <td>✅</td><td>🚧</td><td>🚧</td><td>🚧</td><td>🚧</td><td>🚧</td><td>✅</td><td>🚧</td>
  </tr>
  <tr>
    <td>xAI&nbsp;(Grok)</td>
    <td><code>XAI_API_KEY</code></td>
    <td><code>grok&#8209;2</code></td>
    <td>✅</td><td>🚧</td><td>🚧</td><td>🚧</td><td>🚧</td><td>🚧</td><td>✅</td><td>🚧</td>
  </tr>
  <tr>
    <td>Ollama</td>
    <td><code>OLLAMA_BASE_URL</code></td>
    <td><code>llama3.2</code></td>
    <td>✅</td><td>🚧</td><td>🚧</td><td>🚧</td><td>🚧</td><td>🚧</td><td>✅</td><td>🚧</td>
  </tr>
</table>

✅ Supported  🚧 Coming soon  ❌ Unsupported

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
| `/v1/models` | GET | List available models |
| `/health` | GET | Health check |
| `/metrics` | GET | Prometheus metrics (when enabled) |
| `/admin/api/v1/usage/summary` | GET | Aggregate token usage statistics |
| `/admin/api/v1/usage/daily` | GET | Per-period token usage breakdown |
| `/admin/api/v1/models` | GET | List models with provider type |
| `/admin/dashboard` | GET | Admin dashboard UI |

---

## Configuration

GOModel is configured through environment variables. See [`.env.template`](.env.template) for all options.

Key settings:

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | Server port |
| `GOMODEL_MASTER_KEY` | (none) | API key for authentication |
| `CACHE_TYPE` | `local` | Cache backend (`local` or `redis`) |
| `STORAGE_TYPE` | `sqlite` | Storage backend (`sqlite`, `postgresql`, `mongodb`) |
| `METRICS_ENABLED` | `false` | Enable Prometheus metrics |
| `LOGGING_ENABLED` | `false` | Enable audit logging |

**Quick Start — Authentication:** By default `GOMODEL_MASTER_KEY` is unset. Without this key, API endpoints are unprotected and anyone can call them. This is insecure for production. **Strongly recommend** setting a strong secret before exposing the service. Add `GOMODEL_MASTER_KEY` to your `.env` or environment for production deployments.

---

See [DEVELOPMENT.md](DEVELOPMENT.md) for testing, linting, and pre-commit setup.

---

# Roadmap

## Features

| Feature                    | Basic | Full |
| -------------------------- |:-----:|:----:|
| Billing Management         | 🚧   | 🚧   |
| Full-observability         | 🚧   | 🚧   |
| Budget management          | 🚧   | 🚧   |
| Many keys support          | 🚧   | 🚧   |
| Administrative endpoints   | ✅   | 🚧   |
| Guardrails                 | ✅   | 🚧   |
| SSO                        | 🚧   | 🚧   |
| System Prompt (GuardRails) | ✅   | 🚧   |

## Integrations

| Integration   | Basic | Full |
| ------------- |:-----:|:----:|
| Prometheus    | ✅    | 🚧   |
| DataDog       | 🚧   | 🚧   |
| OpenTelemetry | 🚧   | 🚧   |

✅ Supported  🚧 Coming soon

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=enterpilot/gomodel&type=date&legend=top-left)](https://www.star-history.com/#enterpilot/gomodel&type=date&legend=top-left)
