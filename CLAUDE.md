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
  - `USER_PATH_HEADER` (`X-GoModel-User-Path`: Header used to read/write request `user_path` values)
  - `ENABLE_PASSTHROUGH_ROUTES` (true: Enable provider-native passthrough routes under /p/{provider}/...)
  - `ALLOW_PASSTHROUGH_V1_ALIAS` (true: Allow /p/{provider}/v1/... aliases while keeping /p/{provider}/... canonical)
  - `ENABLED_PASSTHROUGH_PROVIDERS` (openai,anthropic,openrouter,zai,vllm: Comma-separated list of enabled passthrough providers)
  - `REALTIME_ENABLED` (true: Expose the realtime speech-to-speech websocket at `/v1/realtime` and the `/p/{provider}/v1/realtime` upgrade. The canonical `/v1/realtime` route needs only `REALTIME_ENABLED`; the `/p/{provider}/v1/realtime` upgrade additionally requires passthrough routes enabled (`ENABLE_PASSTHROUGH_ROUTES`) with the provider listed in `ENABLED_PASSTHROUGH_PROVIDERS`. The gateway is a transparent websocket reverse proxy — it injects provider credentials and relays the provider's realtime event schema verbatim (no translation), so clients connect without provider API keys. Only providers implementing realtime accept sessions. Currently: OpenAI and xAI/Grok Voice Agent (both `wss://…/v1/realtime`); Z.ai/Zhipu GLM-Realtime (`wss://…/api/paas/v4/realtime`); Bailian/Qwen-Omni (`wss://dashscope…/api-ws/v1/realtime`); and Azure OpenAI (`wss://<resource>/openai/realtime?api-version=…&deployment=…`, `api-key` header). All use OpenAI's realtime event schema (Z.ai adds extensions that relay transparently). Provider-specific notes: xAI voice models (e.g. `grok-voice-latest`) aren't in upstream `/models` discovery, so configure them via `XAI_MODELS`, and xAI bills realtime per-minute (no token usage reported); Azure realtime requires a realtime-capable `AZURE_API_VERSION` (the default may be too old) and the model selects the Azure deployment. (MiniMax was evaluated but skipped — its conversational realtime schema is not OpenAI-compatible.) Sessions are gated by the same model-access and budget rules as other model endpoints; usage is tracked per `response.done` event, accepting both the OpenAI singular and Alibaba plural token-detail spellings.)
- **Storage:** `STORAGE_TYPE` (sqlite), `SQLITE_PATH` (data/gomodel.db), `POSTGRES_URL`, `MONGODB_URL`
- **Models:** `MODELS_ENABLED_BY_DEFAULT` (true), `KEEP_ONLY_ALIASES_AT_MODELS_ENDPOINT` (false), `CONFIGURED_PROVIDER_MODELS_MODE` (`fallback` or `allowlist`, default `fallback`; `allowlist` skips upstream `/models` for providers with configured lists); persisted overrides restrict/allow selectors with `user_paths`. When alias-only models listing is enabled, `GET /v1/models` returns only model aliases, not full concrete model specs, to operators.
- **Virtual models:** Redirects (aliases / load balancers) and access policies are managed in the admin dashboard and persisted to the `virtual_models` store. A redirect with one target is a plain alias; a redirect with several targets is load balanced by `strategy`: `round_robin` (default; rotates across targets, honoring per-target `weight`) or `cost` (always routes to the cheapest catalog-priced available target, falling back to the first target when none are priced). Unavailable targets are skipped, so a redirect works while any target is live. Virtual models can also be declared as infrastructure-as-code under `virtual_models:` in `config.yaml` or via the `VIRTUAL_MODELS` env var (a JSON array; env merges over YAML, winning per `source`). Declarative entries are validated at startup, override admin-store rows with the same `source`, and are read-only in the dashboard.
- **Tagging:** Every request can be labelled from configured HTTP headers. Rules are managed in the dashboard (Settings → "Tagging based on headers", persisted to the `tagging_settings` store) or declared as infrastructure-as-code under `tagging.headers:` in `config.yaml` / numbered env vars `TAGGING_HEADER_1=X-My-Tags` with optional `TAGGING_HEADER_1_PREFIX` (trimmed from each extracted label only), `TAGGING_HEADER_1_DONOTPASS` (default false: headers are forwarded as-is; true strips the header before provider forwarding on passthrough/realtime routes — translated routes never forward client headers), and `TAGGING_HEADER_1_DELIMITER` (default `,`; one header value can carry several labels). An env entry replaces the whole YAML entry with the same header name (unset companion vars reset fields to defaults rather than inheriting YAML values); declarative entries override admin-store rows and are read-only in the dashboard. Credential-bearing headers (`Authorization`, `Cookie`, API-key headers, …) are rejected as tagging sources. Managed API keys can also carry labels (`labels` on `POST /admin/auth-keys`, replaceable later via `PUT /admin/auth-keys/{id}/labels` where `[]` clears, or API Keys → Create API Key / Edit Labels in the dashboard); every request authenticated with the key gets them, merged and de-duplicated with header-extracted labels. Labels are recorded on usage entries (`labels`) and audit log entries (`data.labels`). The dashboard usage page shows a by-label breakdown (`GET /admin/usage/labels`) and label chips with a label filter on the request log (`label` query param on `GET /admin/usage/log`).
- **Audit logging:** `LOGGING_ENABLED` (false), `LOGGING_LOG_BODIES` (false), `LOGGING_LOG_AUDIO_BODIES` (false: refines `LOGGING_LOG_BODIES` for audio endpoints — base64 audio for both `/v1/audio/speech` output and `/v1/audio/transcriptions` upload (≤8 MB each, else `too_large`) + dashboard playback, plus transcription upload metadata; no effect unless `LOGGING_LOG_BODIES` is on, in which case audio-off records a placeholder), `LOGGING_LOG_HEADERS` (false), `LOGGING_RETENTION_DAYS` (30)
- **Usage tracking:** `USAGE_ENABLED` (true), `ENFORCE_RETURNING_USAGE_DATA` (true), `USAGE_RETENTION_DAYS` (90)
- **Dashboard live logs:**
  - `DASHBOARD_LIVE_LOGS_ENABLED` (true): keep enabled for low-latency dashboard previews; set false only when live streams are not needed or memory/socket usage must be minimized.
  - `DASHBOARD_LIVE_LOGS_BUFFER_SIZE` (10000): increase for sustained traffic above ~1000 live messages/sec or large bursts; decrease on tight memory budgets.
  - `DASHBOARD_LIVE_LOGS_REPLAY_LIMIT` (1000): increase when clients commonly reconnect after long gaps (30+ seconds at high traffic); decrease to reduce replay latency and memory.
  - `DASHBOARD_LIVE_LOGS_HEARTBEAT_SECONDS` (15): decrease to 5-10s when proxies need frequent liveness checks; increase to reduce idle network chatter.
- **Cache:** `CACHE_REFRESH_INTERVAL` (3600s), `REDIS_URL`, `REDIS_KEY_MODELS`, `REDIS_TTL_MODELS`. Exact response cache uses `cache.response.simple` in `config.yaml` (optional `enabled`); `REDIS_KEY_RESPONSES`, `REDIS_TTL_RESPONSES`, and `REDIS_URL` apply only when that block exists or when `RESPONSE_CACHE_SIMPLE_ENABLED=true`. Semantic response cache uses `cache.response.semantic` (optional `enabled`); when enabled, `embedder.provider` must name a key in the top-level `providers` map (no default embedder). At runtime that key is resolved against the same env-merged, credential-filtered provider set as routing (not YAML-only), so env-only credentials apply. `vector_store.type` must be set explicitly to one of `qdrant`, `pgvector`, `pinecone`, `weaviate` (each has its own nested config and `SEMANTIC_CACHE_*` env vars). Tuning via `SEMANTIC_CACHE_*` applies when the semantic block exists or `SEMANTIC_CACHE_ENABLED=true`.
- **HTTP client:** `HTTP_TIMEOUT` (600s), `HTTP_RESPONSE_HEADER_TIMEOUT` (600s)
- **Resilience:** Configured via `config/config.yaml` - global `resilience.retry.*` and `resilience.circuit_breaker.*` defaults with optional per-provider overrides under `providers.<name>.resilience.retry.*` and `providers.<name>.resilience.circuit_breaker.*`. Retry defaults: `max_retries` (3), `initial_backoff` (1s), `max_backoff` (30s), `backoff_factor` (2.0), `jitter_factor` (0.1). Circuit breaker defaults: `failure_threshold` (5), `success_threshold` (2), `timeout` (30s)
- **Metrics:** `METRICS_ENABLED` (false), `METRICS_ENDPOINT` (/metrics)
- **Guardrails:** Definitions are persisted in the `guardrail_definitions` store and managed via the admin API/dashboard; `config/config.yaml` entries are validated and upserted into that store at startup (a seed, not the source of truth). `GUARDRAILS_ENABLED` env var gates the feature.
- **Providers:** `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, `USE_GOOGLE_GEMINI_NATIVE_API` (true by default; false uses Gemini's OpenAI-compatible chat API), `XAI_API_KEY`, `GROQ_API_KEY`, `OPENROUTER_API_KEY`, `ZAI_API_KEY`, `ZAI_BASE_URL` (optional Z.ai endpoint override), `MINIMAX_API_KEY`, `MINIMAX_BASE_URL` (optional MiniMax endpoint override), `XIAOMI_API_KEY`, `XIAOMI_BASE_URL` (optional Xiaomi MiMo endpoint override), `OPENCODE_GO_API_KEY`, `OPENCODE_GO_BASE_URL` (optional OpenCode Go/Zen endpoint override; default `https://opencode.ai/zen/go/v1`), `OPENCODE_GO_MESSAGES_MODELS` (optional comma-separated model IDs routed to the Anthropic-native `/messages` endpoint instead of `/chat/completions`; default `qwen3.7-max`), `BAILIAN_API_KEY`, `BAILIAN_BASE_URL` (optional Bailian base URL for region switching; default `https://dashscope.aliyuncs.com/compatible-mode/v1`), `AZURE_API_KEY`, `AZURE_BASE_URL` (Azure OpenAI deployment base URL), `AZURE_API_VERSION` (optional Azure API version), `ORACLE_API_KEY` (Oracle API key), `ORACLE_BASE_URL` (Oracle OpenAI-compatible base URL), `<PROVIDER>[_SUFFIX]_MODELS` (comma-separated configured model list for any provider type), `OLLAMA_BASE_URL`, `VLLM_BASE_URL`, `VLLM_API_KEY` (optional upstream vLLM bearer token)
- **Provider model metadata:** `providers.<name>.models` accepts either model IDs (strings) or `{id, metadata}` objects. When `metadata` is supplied (`display_name`, `context_window`, `max_output_tokens`, `modes`, `capabilities`, `pricing`, …) it is merged onto the remote ai-model-list entry during enrichment, with operator values winning per-field. Primary use case: advertising context windows, capabilities, and pricing for local models (Ollama) and other custom endpoints whose IDs are not in the upstream registry.
