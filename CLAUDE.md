# CLAUDE.md

Guidance for AI models (like Claude) working with this codebase.

## Project Overview

**GOModel** is a high-performance AI gateway in Go that routes requests to multiple LLM providers (OpenAI, Anthropic, Gemini, Groq, xAI, Ollama). Drop-in LiteLLM replacement.

- **Module:** `gomodel` | **Go:** 1.25.0 | **Repo:** https://github.com/ENTERPILOT/GOModel
- **Stage:** Development—backward compatibility is not a concern
- **Design philosophy:** [Postel's Law](https://en.wikipedia.org/wiki/Robustness_principle) (the Robustness Principle) — *"Be conservative in what you send, be liberal in what you accept."* The gateway accepts client requests generously (e.g. `max_tokens` for any model) and adapts them to each provider's specific requirements before forwarding (e.g. translating `max_tokens` → `max_completion_tokens` for OpenAI reasoning models).

## Commands

```bash
make run               # Run server (requires .env with API key)
make build             # Build to bin/gomodel (with version injection)
make test              # Unit tests only
make test-e2e          # E2E tests (in-process mock, no Docker)
make test-integration  # Integration tests (requires Docker/testcontainers, 10m timeout)
make test-contract     # Contract tests (golden file validation)
make test-all          # All tests (unit + e2e + integration + contract)
make lint              # Run golangci-lint
make lint-fix          # Auto-fix lint issues
make tidy              # go mod tidy
make clean             # Remove bin/
make record-api        # Record API responses for contract tests
make swagger           # Regenerate Swagger docs
```

**Single test:** `go test ./internal/providers -v -run TestName`
**E2E single test:** `go test -v -tags=e2e ./tests/e2e/... -run TestName`
**Integration single test:** `go test -v -tags=integration -timeout=10m ./tests/integration/... -run TestName`
**Contract single test:** `go test -v -tags=contract -timeout=5m ./tests/contract/... -run TestName`

**Build tags:** E2E tests require `-tags=e2e`, integration tests require `-tags=integration`, contract tests require `-tags=contract`. The Makefile handles this automatically.

## Architecture

**Request flow:**
```
Client → Echo Middleware (logger → recover → body limit → audit log → auth)
       → Handler → GuardedProvider (guardrails pipeline, if enabled)
       → Router → Provider (llmclient with retries + circuit breaker)
       → Upstream API
```

**Core components:**
- `internal/app/app.go` — Application orchestrator. Wires all dependencies, manages lifecycle. Shutdown sequences teardown in correct order.
- `internal/core/interfaces.go` — `Provider` interface (ChatCompletion, StreamChatCompletion, ListModels, Responses, StreamResponses). `RoutableProvider` adds `Supports()` and `GetProviderType()`. `AvailabilityChecker` for providers without API keys (Ollama).
- `internal/core/errors.go` — `GatewayError` with typed categories mapping to HTTP status codes (see Error Handling below).
- `internal/providers/factory.go` — Provider instantiation via explicit `factory.Add()` calls. Observability hooks are set on the factory *before* registering providers. Factory passes `ProviderOptions` (hooks + resolved resilience config) to provider constructors.
- `internal/providers/registry.go` — Model-to-provider mapping with local/Redis cache and hourly background refresh.
- `internal/providers/router.go` — Routes by model name, returns error if registry not initialized.
- `internal/guardrails/` — Pluggable guardrails pipeline. `GuardedProvider` wraps a `RoutableProvider` and applies guardrails *before* routing. Guardrails operate on normalized `[]Message` DTOs decoupled from API types. Currently supports `system_prompt` type with inject/override/decorator modes.
- `internal/llmclient/client.go` — Base HTTP client for all providers. Retry settings come from `config.RetryConfig` (resolved per-provider). Circuit breaker (closed → open → half-open). Observability hooks. Streaming does NOT retry.
- `internal/auditlog/` — Request/response audit logging with buffered writes. Middleware generates `X-Request-ID` if missing. Sensitive headers auto-redacted. Streaming has separate `StreamLogWrapper`.
- `internal/usage/` — Token usage tracking with buffered writes. Normalizes tokens across providers (input/output/total) + provider-specific `RawData` (cached tokens, reasoning tokens, etc.).
- `internal/storage/` — Unified storage abstraction (SQLite default, PostgreSQL, MongoDB). Shared by audit logging and usage tracking — connection created once.
- `internal/server/http.go` — Echo HTTP server setup with middleware stack and route definitions.
- `internal/server/auth.go` — Bearer token auth via `GOMODEL_MASTER_KEY`. Constant-time comparison. Skips `/health` and metrics endpoint.
- `internal/observability/metrics.go` — Prometheus metrics via hooks injected at factory level: `gomodel_requests_total`, `gomodel_request_duration_seconds`, `gomodel_requests_in_flight`.
- `internal/cache/` — Local file or Redis cache backends for model registry.

**Startup:** Config load (defaults → YAML → env vars) → Register providers with factory → Init providers (cache → async model load → background refresh → router) → Init audit logging → Init usage tracking (shares storage if same backend) → Build guardrails pipeline → Create server → Start listening

**Shutdown (in order):** HTTP server (stop accepting) → Providers (stop refresh + close cache) → Usage tracking (flush buffer) → Audit logging (flush buffer)

**Config cascade:** Code defaults → `config/config.yaml` (optional, supports `${VAR}` and `${VAR:-default}` expansion) → Environment variables (always win). Provider discovery via known env vars (`OPENAI_API_KEY`, etc.).

## Project Structure

```
cmd/gomodel/           # Entrypoint, provider registration
cmd/recordapi/         # Record API responses for contract tests
config/                # Config loading (defaults → YAML → env vars)
internal/
  app/                 # Application orchestration and lifecycle
  core/                # Interfaces, types, errors, context helpers
  providers/           # Provider implementations, router, registry, factory
    openai/            # OpenAI provider
    anthropic/         # Anthropic provider
    gemini/            # Gemini provider
    groq/              # Groq provider
    xai/               # xAI provider
    ollama/            # Ollama provider (no API key, uses base URL)
  guardrails/          # Guardrails pipeline (system prompt injection/override/decorator)
  llmclient/           # Base HTTP client with retries, circuit breaker, hooks
  httpclient/          # Low-level HTTP client with connection pooling
  auditlog/            # Audit logging with SQLite/PostgreSQL/MongoDB backends
  usage/               # Token usage tracking with buffered writes
  storage/             # Unified storage abstraction (SQLite, PostgreSQL, MongoDB)
  cache/               # Local/Redis cache backends (for model registry)
  server/              # Echo HTTP server, handlers, auth middleware
  observability/       # Prometheus metrics (hooks-based)
  version/             # Build-time version injection
tests/
  e2e/                 # E2E tests (in-process mock, no Docker, -tags=e2e)
  integration/         # Integration tests (testcontainers, real DBs, -tags=integration)
  contract/            # Contract tests (golden files, -tags=contract)
  stress/              # Stress tests
docs/                  # Documentation
helm/                  # Kubernetes Helm charts
```

## Adding a Provider

1. Create `internal/providers/{name}/` implementing `core.Provider`
2. Export a `Registration` variable: `var Registration = providers.Registration{Type: "{name}", New: New}`
3. Register in `cmd/gomodel/main.go` via `factory.Add({name}.Registration)`
4. Add API key env var to `.env.template` and to `knownProviders` in `config/config.go`

**No API key** (like Ollama): implement `core.AvailabilityChecker` so the provider is skipped if unreachable. Config uses `BaseURL` env var instead of API key.

## Error Handling

All errors returned to clients use `core.GatewayError` with typed categories:

| ErrorType | HTTP Status | When |
|---|---|---|
| `provider_error` | 502 | Upstream 5xx or network failure |
| `rate_limit_error` | 429 | Upstream 429 |
| `invalid_request_error` | 400 | Bad client input or upstream 4xx |
| `authentication_error` | 401 | Missing/invalid auth |
| `not_found_error` | 404 | Unknown model or resource |

- Use `core.ParseProviderError()` to convert upstream HTTP errors to the correct `GatewayError` type
- Handlers call `handleError()` which checks for `GatewayError` via `errors.As` and returns typed JSON
- Unexpected errors return 500 with a generic message (original error not exposed to clients)

## Testing

- **Unit tests:** Alongside implementation files (`*_test.go`). No Docker.
- **E2E tests:** In-process mock LLM server, no Docker. Tag: `-tags=e2e`
- **Integration tests:** Real databases via testcontainers (Docker required). Tag: `-tags=integration`. Timeout: 10m.
- **Contract tests:** Golden file validation against real API responses. Tag: `-tags=contract`. Record new golden files: `make record-api`
- **Stress tests:** In `tests/stress/`

Docker Compose is optional and intended solely for manual storage-backend validation; automated tests must run without Docker (except integration tests which use testcontainers).

```bash
# Manual storage testing with Docker Compose
STORAGE_TYPE=postgresql POSTGRES_URL=postgres://gomodel:gomodel@localhost:5432/gomodel go run ./cmd/gomodel
STORAGE_TYPE=mongodb MONGODB_URL=mongodb://localhost:27017/gomodel go run ./cmd/gomodel
```

## Configuration Reference

Full reference: `.env.template` and `config/config.yaml`

**Key config groups:**
- **Server:** `PORT` (8080), `GOMODEL_MASTER_KEY` (empty = unsafe mode), `BODY_SIZE_LIMIT` ("10M")
- **Storage:** `STORAGE_TYPE` (sqlite), `SQLITE_PATH` (data/gomodel.db), `POSTGRES_URL`, `MONGODB_URL`
- **Audit logging:** `LOGGING_ENABLED` (false), `LOGGING_LOG_BODIES` (false), `LOGGING_LOG_HEADERS` (false), `LOGGING_RETENTION_DAYS` (30)
- **Usage tracking:** `USAGE_ENABLED` (true), `ENFORCE_RETURNING_USAGE_DATA` (true), `USAGE_RETENTION_DAYS` (90)
- **Cache:** `CACHE_TYPE` (local), `CACHE_REFRESH_INTERVAL` (3600s), `REDIS_URL`, `REDIS_KEY_MODELS`, `REDIS_TTL_MODELS`, `REDIS_KEY_RESPONSES`, `REDIS_TTL_RESPONSES`
- **HTTP client:** `HTTP_TIMEOUT` (600s), `HTTP_RESPONSE_HEADER_TIMEOUT` (600s)
- **Resilience:** Configured via `config/config.yaml` — global `resilience.retry.*` and `resilience.circuit_breaker.*` defaults with optional per-provider overrides under `providers.<name>.resilience.retry.*` and `providers.<name>.resilience.circuit_breaker.*`. Retry defaults: `max_retries` (3), `initial_backoff` (1s), `max_backoff` (30s), `backoff_factor` (2.0), `jitter_factor` (0.1). Circuit breaker defaults: `failure_threshold` (5), `success_threshold` (2), `timeout` (30s)
- **Metrics:** `METRICS_ENABLED` (false), `METRICS_ENDPOINT` (/metrics)
- **Guardrails:** Configured via `config/config.yaml` only (except `GUARDRAILS_ENABLED` env var)
- **Providers:** `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, `XAI_API_KEY`, `GROQ_API_KEY`, `OLLAMA_BASE_URL`

## Documentation Maintenance

After completing any code change, routinely check whether documentation needs updating. This applies to all three documentation layers:

1. **In-code documentation** (Go doc comments on exported types, functions, interfaces) — Update when changing public APIs, adding new exported symbols, or modifying function signatures/behavior.
2. **Mintlify / technical docs** (`docs/` directory) — Update `docs/advanced/*.mdx` pages when changing configuration options or guardrails behavior. Update `docs/adr/` when making significant architectural decisions. Update `docs/plans/` if implementation diverges from existing plans. Check `docs.json` if new pages need to be added to the navigation.

**When to update:**
- Adding a new provider, endpoint, config option, or feature
- Changing existing behavior, defaults, or API contracts
- Renaming or removing configuration variables
- Adding or modifying middleware, guardrails, or storage backends
- Changing build/test commands or requirements

**How to check:** After making changes, scan the affected documentation layers for stale or missing information. Do not add speculative documentation for unimplemented features — only document what exists.

## Key Details

1. Providers are registered explicitly via `factory.Add()` in main.go — order matters, first registered wins for duplicate model names
2. Router requires an initialized registry — check `ModelCount() > 0` before routing
3. Streaming returns `io.ReadCloser` — caller must close. Streaming requests do NOT retry.
4. Models auto-refresh hourly by default (configurable via `CACHE_REFRESH_INTERVAL` or `cache.refresh_interval` in YAML, in seconds)
5. Auth via `GOMODEL_MASTER_KEY` — if unset, server runs in unsafe mode with a warning. Uses `Bearer` token in `Authorization` header. Constant-time comparison.
6. Observability hooks (`OnRequestStart`/`OnRequestEnd`) are set on the factory *before* provider registration, then injected into `llmclient`
7. `X-Request-ID` is auto-generated (UUID) if not present in request. Propagates through context to providers and audit logs.
8. Sensitive headers (Authorization, Cookie, X-API-Key, etc.) are automatically redacted in audit logs
9. Usage tracking normalizes tokens to `input_tokens`/`output_tokens`/`total_tokens` across all providers. Provider-specific data (cached tokens, reasoning tokens) stored in `RawData` as JSON.
10. Storage is shared between audit logging and usage tracking when both use the same backend — connection created once
11. Circuit breaker defaults: 5 failures to open, 2 successes to close, 30s timeout. Half-open state allows single probe request.
12. Ollama requires no API key — enabled via `OLLAMA_BASE_URL`. Implements `AvailabilityChecker` and is skipped if unreachable.
13. `GuardedProvider` wraps the Router — guardrails run *before* routing, not inside providers. They operate on normalized `[]Message` DTOs, decoupled from API-specific request types.
14. Config loading: `.env` loaded first (godotenv), then code defaults, then optional YAML, then env vars always win. YAML supports `${VAR:-default}` expansion.
15. Resilience config (retry and circuit-breaker settings) is global with optional per-provider overrides. `config.ResilienceConfig` is the canonical type shared between the config and llmclient packages. Provider-level overrides use nullable `RawProviderConfig` which is merged with global defaults via `buildProviderConfig()` during config resolution.
