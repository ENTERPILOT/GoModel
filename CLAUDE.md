This project is GoModel - a high-performance, lightweight AI gateway that routes requests to multiple AI model providers through an OpenAI-compatible API.

## Core Principles

### Follow Postel’s Law

Accept user requests generously, adapt them to each provider’s requirements, and return conservative OpenAI-compatible responses.

Examples:

- Accept `max_tokens` from users even when a provider expects another field.
- Translate `max_tokens` to `max_completion_tokens` for OpenAI reasoning models when required.
- Normalize provider responses into an OpenAI-compatible shape.

### Follow The Twelve-Factor App

Prefer production-friendly service design:

- Configuration through environment variables.
- Stateless request handling.
- Clear separation between configuration, routing, provider adapters, and runtime behavior.
- Useful logs for containers and cloud environments.

Reference: https://12factor.net/

### Keep It Simple

Keep files small.

Prefer explicit, maintainable code over clever abstractions.

Do not add abstractions until a repeated pattern clearly justifies them.

### Use Good Defaults

Defaults should fit most users so well that they rarely need to change them.

When adding configuration:

- Choose a safe, practical default.
- Avoid requiring configuration for common use cases.
- Document when and why users should override the default.

## Implementation Guidance

When changing provider behavior:

- Preserve the OpenAI-compatible public API.
- Keep provider-specific logic isolated.
- Avoid leaking provider-specific quirks into user-facing behavior.
- Never expose API keys, authorization headers, or secrets in errors or logs.

When editing code:

- Make the smallest change that solves the problem.
- Use idiomatic Go.
- Prefer clear names, small interfaces, simple structs, and table-driven tests.
- Avoid hidden global state, unnecessary reflection, and premature optimization.
- Add or update tests for behavior changes.

Tests should cover request translation, response normalization, error handling, default configuration, and provider-specific parameter mapping.

## Documentation

Documentation should be concise, practical, and user-focused.

Show defaults, explain when to change them, and include minimal examples when useful.

## Commit and PR Format

Use Conventional Commits for commit subjects and PR titles:

```text
type(scope): short summary
```

Allowed types: `feat`, `fix`, `perf`, `docs`, `refactor`, `test`, `build`, `ci`, `chore`, `revert`

Examples:

```text
feat(openai): support reasoning model token mapping
fix(router): preserve request headers during provider retry
docs(config): document default provider timeout
```

Squash merges should preserve the PR title as the resulting commit subject.

## Pull Request Guidance

Before opening a PR:

- Ensure tests pass.
- Keep the change focused.
- Explain the user-visible impact.
- Mention provider-specific behavior when relevant.
- Update documentation for new configuration or API behavior.

If this repository is not the official GoModel repository, ask the user whether they also want to create a PR against the official repository:

[https://github.com/ENTERPILOT/GoModel/](https://github.com/ENTERPILOT/GoModel/)

## Configuration Reference

Full reference: `.env.template` and `config/config.yaml`

**Key config groups:**

- **Server:**
  - `PORT` (8080)
  - `GOMODEL_MASTER_KEY` (empty = unsafe mode)
  - `BODY_SIZE_LIMIT` ("10M")
  - `ENABLE_PASSTHROUGH_ROUTES` (true: Enable provider-native passthrough routes under /p/{provider}/...)
  - `ALLOW_PASSTHROUGH_V1_ALIAS` (true: Allow /p/{provider}/v1/... aliases while keeping /p/{provider}/... canonical)
  - `ENABLED_PASSTHROUGH_PROVIDERS` (openai,anthropic,openrouter,zai,vllm: Comma-separated list of enabled passthrough providers)
- **Storage:** `STORAGE_TYPE` (sqlite), `SQLITE_PATH` (data/gomodel.db), `POSTGRES_URL`, `MONGODB_URL`
- **Models:** `MODELS_ENABLED_BY_DEFAULT` (true), `MODEL_OVERRIDES_ENABLED` (true), `KEEP_ONLY_ALIASES_AT_MODELS_ENDPOINT` (false), `CONFIGURED_PROVIDER_MODELS_MODE` (`fallback` or `allowlist`, default `fallback`; `allowlist` skips upstream `/models` for providers with configured lists); persisted overrides restrict/allow selectors with `user_paths`. When alias-only models listing is enabled, `GET /v1/models` returns only model aliases, not full concrete model specs, to operators.
- **Audit logging:** `LOGGING_ENABLED` (false), `LOGGING_LOG_BODIES` (false), `LOGGING_LOG_HEADERS` (false), `LOGGING_RETENTION_DAYS` (30)
- **Usage tracking:** `USAGE_ENABLED` (true), `ENFORCE_RETURNING_USAGE_DATA` (true), `USAGE_RETENTION_DAYS` (90)
- **Cache:** `CACHE_REFRESH_INTERVAL` (3600s), `REDIS_URL`, `REDIS_KEY_MODELS`, `REDIS_TTL_MODELS`. Exact response cache uses `cache.response.simple` in `config.yaml` (optional `enabled`); `REDIS_KEY_RESPONSES`, `REDIS_TTL_RESPONSES`, and `REDIS_URL` apply only when that block exists or when `RESPONSE_CACHE_SIMPLE_ENABLED=true`. Semantic response cache uses `cache.response.semantic` (optional `enabled`); when enabled, `embedder.provider` must name a key in the top-level `providers` map (no default embedder). At runtime that key is resolved against the same env-merged, credential-filtered provider set as routing (not YAML-only), so env-only credentials apply. `vector_store.type` must be set explicitly to one of `qdrant`, `pgvector`, `pinecone`, `weaviate` (each has its own nested config and `SEMANTIC_CACHE_*` env vars). Tuning via `SEMANTIC_CACHE_*` applies when the semantic block exists or `SEMANTIC_CACHE_ENABLED=true`.
- **HTTP client:** `HTTP_TIMEOUT` (600s), `HTTP_RESPONSE_HEADER_TIMEOUT` (600s)
- **Resilience:** Configured via `config/config.yaml` - global `resilience.retry.*` and `resilience.circuit_breaker.*` defaults with optional per-provider overrides under `providers.<name>.resilience.retry.*` and `providers.<name>.resilience.circuit_breaker.*`. Retry defaults: `max_retries` (3), `initial_backoff` (1s), `max_backoff` (30s), `backoff_factor` (2.0), `jitter_factor` (0.1). Circuit breaker defaults: `failure_threshold` (5), `success_threshold` (2), `timeout` (30s)
- **Metrics:** `METRICS_ENABLED` (false), `METRICS_ENDPOINT` (/metrics)
- **Guardrails:** Configured via `config/config.yaml` only (except `GUARDRAILS_ENABLED` env var)
- **Providers:** `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, `XAI_API_KEY`, `GROQ_API_KEY`, `OPENROUTER_API_KEY`, `ZAI_API_KEY`, `ZAI_BASE_URL` (optional Z.ai endpoint override), `MINIMAX_API_KEY`, `MINIMAX_BASE_URL` (optional MiniMax endpoint override), `AZURE_API_KEY`, `AZURE_BASE_URL` (Azure OpenAI deployment base URL), `AZURE_API_VERSION` (optional Azure API version), `ORACLE_API_KEY` (Oracle API key), `ORACLE_BASE_URL` (Oracle OpenAI-compatible base URL), `<PROVIDER>[_SUFFIX]_MODELS` (comma-separated configured model list for any provider type), `OLLAMA_BASE_URL`, `VLLM_BASE_URL`, `VLLM_API_KEY` (optional upstream vLLM bearer token)
- **Provider model metadata:** `providers.<name>.models` accepts either model IDs (strings) or `{id, metadata}` objects. When `metadata` is supplied (`display_name`, `context_window`, `max_output_tokens`, `modes`, `capabilities`, `pricing`, …) it is merged onto the remote ai-model-list entry during enrichment, with operator values winning per-field. Primary use case: advertising context windows, capabilities, and pricing for local models (Ollama) and other custom endpoints whose IDs are not in the upstream registry.
