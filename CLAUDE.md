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
  - `REALTIME_ENABLED` (true: Expose the realtime speech-to-speech websocket at `/v1/realtime` and the `/p/{provider}/v1/realtime` upgrade. The canonical `/v1/realtime` route needs only `REALTIME_ENABLED`; the `/p/{provider}/v1/realtime` upgrade additionally requires passthrough routes enabled (`ENABLE_PASSTHROUGH_ROUTES`) with the provider listed in `ENABLED_PASSTHROUGH_PROVIDERS`. The gateway is a transparent websocket reverse proxy — it injects provider credentials and relays the provider's realtime event schema verbatim (no translation), so clients connect without provider API keys. Only providers implementing realtime accept sessions. Currently: OpenAI and xAI/Grok Voice Agent (both `wss://…/v1/realtime`); Z.ai/Zhipu GLM-Realtime (`wss://…/api/paas/v4/realtime`); Bailian/Qwen-Omni (`wss://dashscope…/api-ws/v1/realtime`); and Azure OpenAI (`wss://<resource>/openai/realtime?api-version=…&deployment=…`, `api-key` header). All use OpenAI's realtime event schema (Z.ai adds extensions that relay transparently). Provider-specific notes: xAI voice models (e.g. `grok-voice-latest`) aren't in upstream `/models` discovery, so configure them via `XAI_MODELS`, and xAI bills realtime per-minute (no token usage reported); Azure realtime requires a realtime-capable `AZURE_API_VERSION` (the default may be too old) and the model selects the Azure deployment. (MiniMax was evaluated but skipped — its conversational realtime schema is not OpenAI-compatible.) Sessions are gated by the same model-access and budget rules as other model endpoints; usage is tracked per `response.done` event, accepting both the OpenAI singular and Alibaba plural token-detail spellings. The same flag also exposes the OpenAI-compatible WebRTC surface (via the optional `core.RealtimeCallProvider` interface — OpenAI and xAI at the shared `…/v1/realtime/{calls,client_secrets}` shape, and Azure OpenAI at its GA `<resource>/openai/v1/realtime/{calls,client_secrets}` surface with `api-key` auth and no api-version; xAI gates WebRTC calls per team, so unauthorized accounts get the upstream 403 relayed while client_secrets works. Bailian is deliberately not wired: its WebRTC is allowlist-only with a per-customer endpoint provided by sales, plus no call id in the answer; Z.ai has no WebRTC realtime): `POST /v1/realtime/calls` exchanges SDP (raw `application/sdp` offer with `?model=`, or multipart `sdp` + `session` JSON fields; the session/query model is rewritten to the resolved provider model so aliases and virtual models work) and relays the answer with a gateway-relative `Location: /v1/realtime/calls/{call_id}` header; `POST /v1/realtime/client_secrets` mints ephemeral browser credentials routed by `session.model` (falling back to the nested transcription model); and `GET /v1/realtime?call_id=…` attaches to an existing call as a sideband websocket (an in-memory per-instance call registry recalls the route for calls created through the same instance — 6h TTL, capped; otherwise pass explicit `model`+`provider` params). WebRTC media and events flow directly between client and provider, so after creating a call the gateway attaches its own best-effort sideband observer websocket to record usage per `response.done` (entries carry endpoint `/v1/realtime/calls`; skipped when usage tracking is off, and gateway-relayed sideband attaches for registry-known calls don't tap usage to avoid double counting). WebRTC signaling counts toward request-scoped rate limits, but concurrent-scope rules can't span a WebRTC call's lifetime since only signaling transits the gateway; ephemeral client secrets authenticate clients directly against the provider, so those sessions bypass the gateway entirely and are untracked.)
- **Storage:** `STORAGE_TYPE` (sqlite), `SQLITE_PATH` (default: `data/gomodel.db` when a `./data` directory exists — existing deployments, Docker; otherwise the OS per-user data dir, e.g. `~/.local/share/gomodel/gomodel.db` — see `internal/platformdir`; the local model cache resolves `.cache` vs the OS cache dir the same way), `POSTGRES_URL`, `MONGODB_URL`. `/v1/responses` snapshots and `/v1/conversations` history persist to the configured backend (30-day TTL, hourly sweep); the in-memory fallback stores are byte-capped and used only by embedded setups that skip app wiring.
- **Models:** `MODELS_ENABLED_BY_DEFAULT` (true), `KEEP_ONLY_ALIASES_AT_MODELS_ENDPOINT` (false), `CONFIGURED_PROVIDER_MODELS_MODE` (`fallback` or `allowlist`, default `fallback`; `allowlist` skips upstream `/models` for providers with configured lists); persisted overrides restrict/allow selectors with `user_paths`. When alias-only models listing is enabled, `GET /v1/models` returns only model aliases, not full concrete model specs, to operators.
- **Virtual models:** Redirects (aliases / load balancers) and access policies are managed in the admin dashboard and persisted to the `virtual_models` store. A redirect with one target is a plain alias; a redirect with several targets is load balanced by `strategy`: `round_robin` (default; rotates across targets, honoring per-target `weight`) or `cost` (always routes to the cheapest catalog-priced available target, falling back to the first target when none are priced). Unavailable targets are skipped, so a redirect works while any target is live. Virtual models can also be declared as infrastructure-as-code under `virtual_models:` in `config.yaml` or via the `VIRTUAL_MODELS` env var (a JSON array; env merges over YAML, winning per `source`). Declarative entries are validated at startup, override admin-store rows with the same `source`, and are read-only in the dashboard. Startup validation is catalog-independent: structure plus explicit target `provider` names — a name matching no configured provider (a typo) aborts startup listing the registered providers; a name declared under `providers:` but unregistered (e.g. credentials unset in this environment) only warns and the target stays unavailable; target *model* availability is never a startup gate (checked at resolve time, since the catalog loads asynchronously).
- **MCP gateway:** `MCP_ENABLED` (true: expose the MCP-protocol endpoints; a no-op until servers are declared). GoModel aggregates upstream MCP (Model Context Protocol) servers behind the authenticated streamable-HTTP endpoint `/mcp` (POST JSON-RPC, GET notification stream, DELETE session end) and per-server endpoints `/mcp/{server}`. On `/mcp`, tools and prompts are namespaced `{server}_{name}` with deterministic ordering; `tools/call` accepts the namespaced name (longest server-prefix match) or a unique bare name; `/mcp/{server}` exposes original names. Tools, prompts, resources, and resource templates relay with raw schemas/results verbatim; upstream `instructions` are merged into the gateway's `initialize` result. Servers come from three sources with the usual precedence: `mcp.servers:` map in `config.yaml`, the `MCP_SERVERS` env var (JSON object merged over YAML per name), and the `mcp_servers` admin store (dashboard MCP Servers page / `/admin/mcp-servers` GET/PUT/DELETE + `POST .../{name}/reconnect` + `GET .../{name}/catalog` for the per-server tools/prompts/resources inspector); declarative entries are validated at startup, shadow same-name store rows, and are read-only in the dashboard (secret header values are redacted as `***` in admin reads, and a `***` value on PUT preserves the stored secret). Per-server fields: `url` + `transport` (`http` streamable default, `sse` legacy), or declarative-only `stdio` (`command`/`args`/`env` — rejected via admin API/dashboard because runtime-registered subprocesses would be an RCE vector), `headers` (upstream credentials, `${ENV}` supported; the gateway is a credential boundary — client bearer tokens are never forwarded upstream), `allowed_tools`/`disallowed_tools`, `user_paths` (visibility subtree scoping like virtual models — filtered out of `tools/list`, not just blocked at call time), `tool_timeout` (30s default). The `X-MCP-Servers` request header narrows a session to a comma-separated server subset. One upstream session is shared per server (lazy dial, redial-once on death); a failed listing marks the server `degraded` keeping its last catalog (stale carry-forward, 60s re-probe, 5m re-list, `list_changed` notifications trigger resync). Downstream sessions are SDK-managed (`Mcp-Session-Id`, 30m idle timeout), bound to the initializing user path (a different principal presenting the session ID gets 404), and each session sees a visibility-filtered tool snapshot taken at initialize. Every MCP POST is gated by user-path rate limits and budgets; every `tools/call` writes a usage entry (`provider="mcp"`, `provider_name`=server, `model`=namespaced tool, duration/sizes/error in raw data, labels/user_path as usual) and MCP paths are audit-logged model interactions whose entries are labelled with the JSON-RPC method (tool/prompt name for calls) and `provider="mcp"`, so request-log and live-log rows are self-describing; with `LOGGING_LOG_BODIES` the JSON-RPC request and response frames (SSE replies decoded) are captured on POST entries too. Server→client MCP features (sampling, elicitation, roots) and resource subscriptions are not negotiated in v1. Spec: `docs/dev/2026-07-07_mcp-gateway-spec.md`.
- **Tagging:** Every request can be labelled from configured HTTP headers. Rules are managed in the dashboard (Settings → "Tagging based on headers", persisted to the `tagging_settings` store) or declared as infrastructure-as-code under `tagging.headers:` in `config.yaml` / numbered env vars `TAGGING_HEADER_1=X-My-Tags` with optional `TAGGING_HEADER_1_PREFIX` (trimmed from each extracted label only), `TAGGING_HEADER_1_DONOTPASS` (default false: headers are forwarded as-is; true strips the header before provider forwarding on passthrough/realtime routes — translated routes never forward client headers), and `TAGGING_HEADER_1_DELIMITER` (default `,`; one header value can carry several labels). An env entry replaces the whole YAML entry with the same header name (unset companion vars reset fields to defaults rather than inheriting YAML values); declarative entries override admin-store rows and are read-only in the dashboard. Credential-bearing headers (`Authorization`, `Cookie`, API-key headers, …) are rejected as tagging sources. Managed API keys can also carry labels (`labels` on `POST /admin/auth-keys`, replaceable later via `PUT /admin/auth-keys/{id}/labels` where `[]` clears, or API Keys → Create API Key / Edit Labels in the dashboard); every request authenticated with the key gets them, merged and de-duplicated with header-extracted labels. Labels are recorded on usage entries (`labels`) and audit log entries (`data.labels`). The dashboard usage page shows a by-label breakdown (`GET /admin/usage/labels`) and label chips with a label filter on the request log (`label` query param on `GET /admin/usage/log`).
- **Audit logging:** `LOGGING_ENABLED` (true), `LOGGING_LOG_BODIES` (true), `LOGGING_LOG_AUDIO_BODIES` (false: refines `LOGGING_LOG_BODIES` for audio endpoints — base64 audio for both `/v1/audio/speech` output and `/v1/audio/transcriptions` upload (≤8 MB each, else `too_large`) + dashboard playback, plus transcription upload metadata; no effect unless `LOGGING_LOG_BODIES` is on, in which case audio-off records a placeholder), `LOGGING_LOG_HEADERS` (true), `LOGGING_RETENTION_DAYS` (30)
- **Usage tracking:** `USAGE_ENABLED` (true), `ENFORCE_RETURNING_USAGE_DATA` (true), `USAGE_RETENTION_DAYS` (90). Callers can read their own status without admin access via `GET /v1/usage`: usage summary over a date window (`start_date`/`end_date`/`days`, default last 30 days UTC) plus budget and rate-limit statuses, all scoped to the caller's effective user path (managed key binding, else the user-path header).
- **Rate limits:** `RATE_LIMITS_ENABLED` (true; no-op until rules exist). Every rule has a scope: `user_path` (consumer control; subtree with ONE shared counter per rule — per-key limits = give each key its own path), `provider` (caps one configured provider instance across all consumers/models), or `model` (subject `openai/gpt-4o` pins one provider's model, bare `gpt-4o` covers it on any provider; matching case-insensitive). Limits: `max_requests`/`max_tokens` per period (`minute`/`hour`/`day`/custom `period_seconds`, sliding window) plus `concurrent` (period_seconds 0: `max_requests` = max in-flight; realtime sessions hold a slot for the session, batch submissions don't — and batch skips provider/model rules since batch files can mix models). Enforcement covers every model endpoint; user-path breaches return 429 (`code: rate_limit_exceeded`) with `Retry-After`, successes carry `x-ratelimit-{limit,remaining,reset}-{requests,tokens}` from the most-constrained matching rule; cache hits bypass. Saturated providers/models are instead routed around: virtual-model load balancing prefers targets with capacity (falling back to the first declared target when all are saturated, so the client gets an honest 429 rather than an unavailable-model error; saturation never affects catalog membership or /v1/models listing), a saturated primary route with configured failover rules skips the primary provider and is served by the sweep (which also skips saturated candidates), and only requests with no viable alternative get 429. Token windows are charged to the provider/model that actually executed (from the usage entry), so accounting stays correct under aliasing/failover. Managed in the dashboard (Rate Limits page: scope selector) / `/admin/rate-limits` (GET/PUT/DELETE + `POST .../reset-one`, `POST .../reset`; requests take `scope`+`subject`, with `user_path` as shorthand for user-path rules), or as infrastructure-as-code under `rate_limits.{user_paths,providers,models}:` in `config.yaml` / `SET_RATE_LIMIT_<PATH>` env vars (`rpm/tpm/rph/tph/rpd/tpd/concurrent=N` compact syntax or a JSON rule array; `__` separates path segments) and `SET_PROVIDER_RATE_LIMIT_<NAME>` (same syntax; suffix underscores become hyphens; model rules are YAML/admin-only). Env replaces the whole YAML entry for the same subject; config-sourced rules are read-only in the dashboard and manual edits win over config seeds, like budgets. Token limits are post-accounted from usage entries, so they require `USAGE_ENABLED=true` (startup warns otherwise) and one request can overshoot a token window. Counters are in-memory per instance (N replicas ≈ N× limit) and reset on restart — budgets remain the durable cross-instance control.
- **Dashboard live logs:**
  - `DASHBOARD_LIVE_LOGS_ENABLED` (true): keep enabled for low-latency dashboard previews; set false only when live streams are not needed or memory/socket usage must be minimized. With `LOGGING_LOG_BODIES` also enabled, in-flight streamed responses render chunk-by-chunk in the request log and Interactions drawer (throttled `audit.stream` events, published only while a dashboard is connected; partial bodies are never buffered server-side).
  - `DASHBOARD_LIVE_LOGS_BUFFER_SIZE` (10000): effective size is capped at `DASHBOARD_LIVE_LOGS_REPLAY_LIMIT + 1` (older events can never be replayed); lower it below the replay limit only to shrink memory at the cost of more replay resets. Buffered events are compact previews — request/response bodies are never retained in the buffer (connected dashboards get them live; history hydrates from persisted audit entries).
  - `DASHBOARD_LIVE_LOGS_REPLAY_LIMIT` (1000): increase when clients commonly reconnect after long gaps (30+ seconds at high traffic); decrease to reduce replay latency and memory. Also bounds the live log buffer.
  - `DASHBOARD_LIVE_LOGS_HEARTBEAT_SECONDS` (15): decrease to 5-10s when proxies need frequent liveness checks; increase to reduce idle network chatter.
- **Cache:** `CACHE_REFRESH_INTERVAL` (3600s: full model re-discovery; also drives dashboard provider health "Last checked"), `PROVIDER_RECHECK_INTERVAL` (60s: fast re-probe of only the providers whose last refresh failed, 0 disables), `REDIS_URL`, `REDIS_KEY_MODELS`, `REDIS_TTL_MODELS`. A provider whose refresh fails keeps its previous inventory marked stale: direct requests still route to it (honest 502/503), virtual-model load balancing skips it (`ModelAvailable`), and the dashboard shows Degraded. Exact response cache uses `cache.response.simple` in `config.yaml` (optional `enabled`); `REDIS_KEY_RESPONSES`, `REDIS_TTL_RESPONSES`, and `REDIS_URL` apply only when that block exists or when `RESPONSE_CACHE_SIMPLE_ENABLED=true`. Semantic response cache uses `cache.response.semantic` (optional `enabled`); when enabled, `embedder.provider` must name a key in the top-level `providers` map (no default embedder). At runtime that key is resolved against the same env-merged, credential-filtered provider set as routing (not YAML-only), so env-only credentials apply. `vector_store.type` must be set explicitly to one of `qdrant`, `pgvector`, `pinecone`, `weaviate` (each has its own nested config and `SEMANTIC_CACHE_*` env vars). Tuning via `SEMANTIC_CACHE_*` applies when the semantic block exists or `SEMANTIC_CACHE_ENABLED=true`.
- **HTTP client:** `HTTP_TIMEOUT` (600s), `HTTP_RESPONSE_HEADER_TIMEOUT` (600s); also settable via the `http:` block in `config.yaml` (env vars win)
- **Resilience:** Configured via `config/config.yaml` - global `resilience.retry.*` and `resilience.circuit_breaker.*` defaults with optional per-provider overrides under `providers.<name>.resilience.retry.*` and `providers.<name>.resilience.circuit_breaker.*`. Retry defaults: `max_retries` (3), `initial_backoff` (1s), `max_backoff` (30s), `backoff_factor` (2.0), `jitter_factor` (0.1). Circuit breaker defaults: `failure_threshold` (5), `success_threshold` (2), `timeout` (30s). Breaker state is per-process and exported as the `gomodel_circuit_breaker_state` gauge when metrics are enabled. The dashboard's provider status also folds in real-traffic request health: each provider's runtime row carries `request_health` (last observed breaker state plus per-model request/error counts over a 10-minute sliding window; a model with ≥3 errors and a ≥50% error rate is flagged). An open breaker turns the provider card's status pill to "Circuit Open" (unhealthy), a half-open breaker or a flagged model degrades an otherwise healthy provider ("Recovering"/"Degraded"), and the expanded card details list recent per-model traffic with the latest error. Signals only worsen the discovery-based status, never improve it; tracking is in-memory per instance, and providers with no recent requests show discovery-based status only.
- **Metrics:** `METRICS_ENABLED` (false), `METRICS_ENDPOINT` (/metrics)
- **Guardrails:** Definitions are persisted in the `guardrail_definitions` store and managed via the admin API/dashboard; `config/config.yaml` entries are validated and upserted into that store at startup (a seed, not the source of truth). `GUARDRAILS_ENABLED` env var gates the feature.
- **Provider API key rotation:** Any API-key provider accepts several keys: `<PROVIDER>[_SUFFIX]_API_KEY_<n>` env vars (numbered from 2; `_1` is accepted as a synonym for the unsuffixed key) or `providers.<name>.api_keys` in `config.yaml` (merged after `api_key`, de-duplicated, unresolved `${...}` entries dropped; env replaces the whole YAML list). Two or more keys turn on round-robin rotation, drawn per outbound HTTP request — including retries, so a 429'd request retries under the next key. Realtime websocket sessions pick a key per session. Counters are in-memory per instance. The trailing number names a key, not a provider: `OPENAI_API_KEY_2` is key 2 of `openai`, while `OPENAI_REGION_2_API_KEY` is the sole key of provider `openai-region-2`. **Rotation defeats provider prompt caching** (providers scope the cache to the key that filled it); use it to lift per-key rate limits, not to save cost. Keyless (Ollama, vLLM) and non-API-key providers (Vertex, Bedrock) are unaffected.
- **Providers:** `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `ANTHROPIC_DEFAULT_MAX_TOKENS` (optional default `max_tokens` for Anthropic-translated requests that omit it; default 4096), `GEMINI_API_KEY`, `USE_GOOGLE_GEMINI_NATIVE_API` (true by default; false uses Gemini's OpenAI-compatible chat API), `XAI_API_KEY`, `GROQ_API_KEY`, `FIREWORKS_API_KEY`, `FIREWORKS_BASE_URL` (optional Fireworks AI endpoint override; default `https://api.fireworks.ai/inference/v1`), `META_API_KEY`, `META_BASE_URL` (optional Meta Model API endpoint override; default `https://api.meta.ai/v1`; Muse Spark models, e.g. `muse-spark-1.1`), `OPENROUTER_API_KEY`, `OPENROUTER_SITE_URL`/`OPENROUTER_APP_NAME` (optional OpenRouter attribution headers), `ZAI_API_KEY`, `ZAI_BASE_URL` (optional Z.ai endpoint override), `MINIMAX_API_KEY`, `MINIMAX_BASE_URL` (optional MiniMax endpoint override), `XIAOMI_API_KEY`, `XIAOMI_BASE_URL` (optional Xiaomi MiMo endpoint override), `OPENCODE_GO_API_KEY`, `OPENCODE_GO_BASE_URL` (optional OpenCode Go/Zen endpoint override; default `https://opencode.ai/zen/go/v1`), `OPENCODE_GO_MESSAGES_MODELS` (optional comma-separated model IDs routed to the Anthropic-native `/messages` endpoint instead of `/chat/completions`; default `qwen3.7-max`), `BAILIAN_API_KEY`, `BAILIAN_BASE_URL` (optional Bailian base URL for region switching; default `https://dashscope.aliyuncs.com/compatible-mode/v1`), `AZURE_API_KEY`, `AZURE_BASE_URL` (Azure OpenAI deployment base URL), `AZURE_API_VERSION` (optional Azure API version), `ORACLE_API_KEY` (Oracle API key), `ORACLE_BASE_URL` (Oracle OpenAI-compatible base URL), `BEDROCK_BASE_URL` (Bedrock Runtime region or endpoint), `BEDROCK_MANTLE_API_KEY`, `BEDROCK_MANTLE_BASE_URL` (Mantle region or endpoint), `BEDROCK_MANTLE_API_MODE` (`auto`, `openai`, or `standard`), `<PROVIDER>[_SUFFIX]_MODELS` (comma-separated configured model list for any provider type), `OLLAMA_BASE_URL`, `VLLM_BASE_URL`, `VLLM_API_KEY` (optional upstream vLLM bearer token)
- **Provider model metadata:** `providers.<name>.models` accepts either model IDs (strings) or `{id, metadata}` objects. When `metadata` is supplied (`display_name`, `context_window`, `max_output_tokens`, `modes`, `capabilities`, `pricing`, …) it is merged onto the remote ai-model-list entry during enrichment, with operator values winning per-field. Primary use case: advertising context windows, capabilities, and pricing for local models (Ollama) and other custom endpoints whose IDs are not in the upstream registry.
