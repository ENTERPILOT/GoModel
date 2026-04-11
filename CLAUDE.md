# CLAUDE.md

Guidance for AI models (like Claude) working with this codebase.

## Project Overview

**GOModel** is a high-performance AI gateway in Go that routes requests to multiple AI model providers (OpenAI, Anthropic, Gemini, Groq, xAI, Oracle, Ollama). LiteLLM killer.

**Go:** 1.26.1
**Repo:** https://github.com/ENTERPILOT/GOModel

- **Stage:** Development - backward compatibility is not a concern
- **Design philosophy:**

1. [Postel's Law](https://en.wikipedia.org/wiki/Robustness_principle) (the Robustness Principle)_"Be conservative in what you send, be liberal in what you accept."_

- The gateway accepts client requests generously (e.g. `max_tokens` for any model) and adapts them to each provider's specific requirements before forwarding (e.g. translating `max_tokens` → `max_completion_tokens` for OpenAI reasoning models).
- The gateway accepts provider's response liberally and pass it to the user in a conservative OpenAI-compatible shape.

1. [The Twelve-Factor App](https://12factor.net/).

## Commands

```bash
make run               # Run server (requires .env with API key)
make build             # Build to bin/gomodel (with version injection)
make test              # Unit tests only
make test-e2e          # E2E tests (in-process mock, no Docker)
make test-integration  # Integration tests (requires Docker, 10m timeout)
make test-contract     # Contract replay tests (golden file validation)
make test-all          # All tests (unit + e2e + integration + contract)
make lint              # Run golangci-lint
make lint-fix          # Auto-fix lint issues
make tidy              # go mod tidy
make clean             # Remove bin/
make record-api        # Record API responses for contract tests
make swagger           # Regenerate Swagger docs
make infra             # Docker Compose: Redis, Postgres, MongoDB, Adminer only
make image             # Docker Compose: full stack (GOModel + Prometheus)
```

**Single test:** `go test ./internal/providers -v -run TestName`
**E2E single test:** `go test -v -tags=e2e ./tests/e2e/... -run TestName`
**Integration single test:** `go test -v -tags=integration -timeout=10m ./tests/integration/... -run TestName`
**Contract single test:** `go test -v -tags=contract -timeout=5m ./tests/contract/... -run TestName`

**Build tags:** E2E tests require `-tags=e2e`, integration tests require `-tags=integration`, contract tests require `-tags=contract`. The Makefile handles this automatically.

## Commit And PR Title Format

Use Conventional Commit format for commit subjects and PR titles:

`type(scope): short summary`

Allowed types: feat, fix, perf, docs, refactor, test, build, ci, chore, revert

Prefer squash-and-merge to keep the merged commit subject aligned with the PR title.

## Error Handling

- All errors returned to clients must be instances of `core.GatewayError`.
- Do not hide work in detached goroutines; respect context synchronously and return typed `core.GatewayError` values.
- Use the typed client-facing categories `provider_error`, `rate_limit_error`, `invalid_request_error`, `authentication_error`, and `not_found_error`.
- Public error responses must use the OpenAI-compatible shape:

```json
{
  "error": {
    "type": "provider_error",
    "message": "human readable message",
    "param": null,
    "code": null
  }
}
```

- If `param` or `code` metadata is available from validation or an upstream provider, it must be exposed in those fields; otherwise both fields must still be present with `null`.
- Update this document whenever behavior, configuration, providers, supported commands, or public API contracts change.

## Testing

- **Unit tests:** Alongside implementation files (`*_test.go`). No Docker.
- **E2E tests:** Currently in-process mock LLM server, no Docker. Tag: `-tags=e2e`
- **Integration tests:** Real databases via Docker-managed containers (Docker required). Tag: `-tags=integration`. Timeout: 10m.
- **Contract tests:** Golden file validation against real API responses. Tag: `-tags=contract`. Record new golden files: `make record-api`
- **Stress tests:** In `tests/stress/`

Docker Compose is optional and intended solely for manual storage-backend validation; automated tests must run without Docker (except integration tests which start ephemeral database containers through the Docker CLI).

```bash
# Manual storage testing with Docker Compose running
STORAGE_TYPE=postgresql POSTGRES_URL=postgres://gomodel:gomodel@localhost:5432/gomodel go run ./cmd/gomodel
STORAGE_TYPE=mongodb MONGODB_URL=mongodb://localhost:27017/gomodel go run ./cmd/gomodel
```

## Configuration Reference

Full reference: `.env.template` and `config/config.yaml`

**Key config groups:**

- **Server:**
  - `PORT` (8080)
  - `GOMODEL_MASTER_KEY` (empty = unsafe mode)
  - `BODY_SIZE_LIMIT` ("10M")
  - `ENABLE_PASSTHROUGH_ROUTES` (true: Enable provider-native passthrough routes under /p/{provider}/...)
  - `ALLOW_PASSTHROUGH_V1_ALIAS` (true: Allow /p/{provider}/v1/... aliases while keeping /p/{provider}/... canonical)
  - `ENABLED_PASSTHROUGH_PROVIDERS` (openai,anthropic: Comma-separated list of enabled passthrough providers)
- **Storage:** `STORAGE_TYPE` (sqlite), `SQLITE_PATH` (data/gomodel.db), `POSTGRES_URL`, `MONGODB_URL`
- **Models:** `MODELS_ENABLED_BY_DEFAULT` (true), `MODEL_OVERRIDES_ENABLED` (false), `KEEP_ONLY_ALIASES_AT_MODELS_ENDPOINT` (false); persisted overrides restrict/allow selectors with `user_paths`. When alias-only models listing is enabled, `GET /v1/models` returns only model aliases, not full concrete model specs, to operators.
- **Audit logging:** `LOGGING_ENABLED` (false), `LOGGING_LOG_BODIES` (false), `LOGGING_LOG_HEADERS` (false), `LOGGING_RETENTION_DAYS` (30)
- **Usage tracking:** `USAGE_ENABLED` (true), `ENFORCE_RETURNING_USAGE_DATA` (true), `USAGE_RETENTION_DAYS` (90)
- **Cache:** `CACHE_REFRESH_INTERVAL` (3600s), `REDIS_URL`, `REDIS_KEY_MODELS`, `REDIS_TTL_MODELS`. Exact response cache uses `cache.response.simple` in `config.yaml` (optional `enabled`); `REDIS_KEY_RESPONSES`, `REDIS_TTL_RESPONSES`, and `REDIS_URL` apply only when that block exists or when `RESPONSE_CACHE_SIMPLE_ENABLED=true`. Semantic response cache uses `cache.response.semantic` (optional `enabled`); when enabled, `embedder.provider` must name a key in the top-level `providers` map (no default embedder). At runtime that key is resolved against the same env-merged, credential-filtered provider set as routing (not YAML-only), so env-only credentials apply. `vector_store.type` must be set explicitly to one of `qdrant`, `pgvector`, `pinecone`, `weaviate` (each has its own nested config and `SEMANTIC_CACHE_*` env vars). Tuning via `SEMANTIC_CACHE_*` applies when the semantic block exists or `SEMANTIC_CACHE_ENABLED=true`.
- **HTTP client:** `HTTP_TIMEOUT` (600s), `HTTP_RESPONSE_HEADER_TIMEOUT` (600s)
- **Resilience:** Configured via `config/config.yaml` — global `resilience.retry.*` and `resilience.circuit_breaker.*` defaults with optional per-provider overrides under `providers.<name>.resilience.retry.*` and `providers.<name>.resilience.circuit_breaker.*`. Retry defaults: `max_retries` (3), `initial_backoff` (1s), `max_backoff` (30s), `backoff_factor` (2.0), `jitter_factor` (0.1). Circuit breaker defaults: `failure_threshold` (5), `success_threshold` (2), `timeout` (30s)
- **Metrics:** `METRICS_ENABLED` (false), `METRICS_ENDPOINT` (/metrics)
- **Guardrails:** Configured via `config/config.yaml` only (except `GUARDRAILS_ENABLED` env var)
- **Providers:** `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, `XAI_API_KEY`, `GROQ_API_KEY`, `ORACLE_API_KEY` (Oracle API key), `ORACLE_BASE_URL` (Oracle OpenAI-compatible base URL), `OLLAMA_BASE_URL`
