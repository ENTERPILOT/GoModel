# Architecture Review — 2026-07-04

Scope: whole repo on `chore/architecture` (HEAD `87d78d2c`). Evidence is cited as
`file:line`. Items already tracked in `docs/dev/possible-refactoring.md` are marked
**(known #N)** rather than re-explained.

Legend: **[COUPLING]** dependency/blast-radius problem · **[SMELL]** design debt ·
**[BUG]** incorrect behavior · **[REMOVE]** dead or redundant code ·
**[SIMPLIFY]** consolidation opportunity · **[INCORRECT]** factually wrong claim ·
**[GOOD]** deliberately called out as fine.

---

## 1. The headline problem: change amplification

Measured from git history (non-test files / top-level packages touched):

| Change | Files | Packages |
|---|---|---|
| Labelling system (#454) | 47 | 13 |
| Manual failover (#444) | 60 | 15 |
| Auth-key labels (#467) | 24 | 10 |
| Load-balanced virtual models (#433) | 27 | 9 |
| Perf pass (#413) | 121 | 29 |
| Add one provider — xiaomi (#389) | 16 | 9 |
| Add one provider — bailian (#392) | 12 | 9 |

A median feature touches **7–9 packages**. Five root causes, each detailed later:

1. **Triple-store persistence (§5).** Every persisted feature hand-writes sqlite,
   postgres, and mongo implementations: **11,670 LOC** of backend-specific store
   code across 12 features, plus 1,248 LOC of vector-store backends in
   `responsecache`. The shared layer (`internal/storage`, 390 LOC) is only
   connection factories. A new column = 3 store edits + ad-hoc migration per backend.
2. **Per-feature vertical stack with manual registration at every floor (§6, §7).**
   store ×3 → service → `app.go` wiring block → `admin.Handler` field + functional
   option + route → dashboard: JS module + manual `<script>` include + module-merge
   list + init fetch + page template + `index.html` include + sidebar + hard-coded
   valid-pages array. A new dashboard feature is **11–13 coordinated edits**, four of
   which are pure registration with no compiler/test guard.
3. **No single per-request event model (§6).** `usage`, `auditlog`, and `live` each
   capture request data independently; `responsecache` also stores usage/audit
   shapes. A new request attribute (labels) had to be threaded through each consumer
   separately — hence 13 packages for #454.
4. **Field lists maintained in many places (§3).** The Responses API field set is
   repeated in ~8 sites; chat decode in 3; every new IR field is shotgun surgery.
5. **Checked-in generated artifacts + documentation quadruplication (§8, §9).**
   `cmd/gomodel/docs/docs.go` (8,056 lines) + `docs/openapi.json` churn in every
   API-touching PR; each config knob is described in `.env.template`,
   `config.example.yaml`, `CLAUDE.md`, and `README.md`. Bailian (#392): 12 files
   changed, only **1** was the provider implementation.

None of this is accidental complexity in any single spot — each layer is locally
reasonable. The multiplier is the *product* of the layers.

---

## 2. Layering: server / gateway / app / core

**[SMELL] The server–gateway boundary is asserted but not real.**
`internal/server` (44 non-test files, 8,001 LOC) contains not just HTTP adapters but
the actual orchestration services: `translated_inference_service.go` (679),
`native_response_service.go` (628), `native_file_service.go` (531),
`audio_service.go` (375), `internal_chat_completion_executor.go` (281), plus
batch/conversation/realtime/passthrough services. Meanwhile `internal/gateway`
(3,198 LOC: `inference_execute.go` 565, `batch_orchestrator.go` 507,
`inference_prepare.go`, `attempts.go`, `failover.go`) holds *some* of the same
concern — request preparation/execution — and is imported only by `server`. Two
packages, one layer, split arbitrarily. Either move the `*_service.go` orchestrators
into `gateway` and keep `server` = echo handlers + encode/decode only, or merge
`gateway` into `server` and stop pretending there are two layers.

**[SMELL] `internal/app/app.go` is a god-wiring file.** `app.New()` spans
`app.go:82–541` (~460 lines) and every feature adds a block; `initAdmin`
(`app.go:877–966`) repeats the pattern. The file also owns feature-flag policy
helpers (`failoverFeatureEnabledGlobally`, `semanticResponseCacheConfigured`,
`app.go:1122–1149`) that belong to `config` **(known #6)**. Decompose wiring into
per-feature constructors so the composition root reads as a table of features.

**[COUPLING] Import fan-in/fan-out concentrations.** `internal/server` imports 19
internal packages; `internal/app` ~24; `internal/admin` 13. Partly inherent to
composition roots, but §6/§7 show much of it is avoidable registration plumbing.

**[SMELL] `internal/core` is several concepts wearing one name** (59 files,
6,756 LOC, imported by **41 packages** — anything added there couples everything):
(a) wire types for three dialects — coherent and well-executed; (b) request-scoped
runtime state (`context.go`, `request_snapshot.go`, `workflow.go`, `labels.go`);
(c) the semantic envelope of ADR-0002 (`semantic.go` 479, `semantic_canonical.go`);
(d) things that don't belong at all:

- **[COUPLING]** `core/batch_preparation.go:42–107` — `RewriteBatchSource` *drives
  provider file download/re-upload IO* from inside the type package.
- **[SMELL]** ~300 lines of model-catalog/pricing types with a hand-maintained
  16-field `FieldSources` list and a parallel 16-field `Clone`
  (`core/types.go:155–452`) — catalog concerns living next to `ChatRequest`.
- **[SMELL]** Swagger-only phantom fields inside IR structs
  (`Message.ContentSchema`, `core/types.go:76–79,123–124`, `responses.go:147–148`)
  with duplicate `json:"content"` tags and `//nolint:govet`.
- **[SMELL]** `core/errors.go:310–331` hardcodes an OpenRouter error-message
  heuristic — the *only* provider-quirk leak found in core (discipline is otherwise
  excellent, see [GOOD] below).
- **[SMELL]** `WhiteBoxPrompt` (`core/semantic.go:60–81`) is ADR-0002's "semantic
  envelope" under a name that describes neither a prompt nor white-boxing, carrying
  a type-erased `map[semanticCacheKey]any` second context.
- **[COUPLING]** 13 context keys form a hidden data bus (`core/context.go:8–52`) —
  snapshot, workflow, labels, guardrails hash, failover flag, usage policy flow via
  `context.Value`, so stage dependencies are invisible in signatures (semantic-cache
  correctness silently depends on `WithGuardrailsHash` having been called).

**[SIMPLIFY] 30 provider interfaces, half of them "Routable" twins.** Beyond
`interfaces.go`'s 22, `passthrough.go` and `realtime.go` add more. Every native
capability interface is mirrored by a `providerType string`-prefixed twin
(`NativeBatchProvider` `interfaces.go:41–47` vs `NativeBatchRoutableProvider`
`:63–69`; same for files, response lifecycle/utility, hints, passthrough, realtime),
kept in sync by hand, consumed via ~59 scattered type assertions. A `Routed[T]`
wrapper or resolver-returns-provider pattern would halve the surface. ADR-0004
promised a capability model; what shipped is interface sniffing.

**[COUPLING] Gateway fast-path hardcodes a provider whitelist** —
`gateway/inference_execute.go:174–178`: `switch providerType { case "openai",
"azure", "openrouter": }`. Byte-compatibility is a provider capability; it belongs
in the registry, not a string switch in the orchestrator. (The only
"if provider == X" found outside provider packages.)

**[GOOD]** The core `Provider` interface is small (6 methods,
`interfaces.go:10–28`); `core.ChatRequest` is lean (15 fields) — no LiteLLM-style
kitchen-sink god struct; core contains **zero provider-name conditionals** in
behavior (verified by grep over 16 provider names) except the OpenRouter heuristic
above; the gjson selector-peek two-tier parse (`semantic.go:316–365`) is the right
design, including a documented duplicate-key divergence note.

---

## 3. Dialect translation & the IR (deep-dive findings)

The dialect story is **hub-and-spoke and deliberate** (ADR-0007): OpenAI chat,
OpenAI Responses, and Anthropic `/v1/messages` all normalize into `core.ChatRequest`
(Responses is a semi-canonical sibling that lowers into chat via
`providers/responses_adapter.go:26`), and provider adapters translate outward.
That is the right architecture. The findings are about its execution:

**[BUG] `/v1/messages` ingress bypasses typed IR fields.**
`anthropicapi/request.go:443–460` writes `top_p` and `user` into `ExtraFields`
even though `core.ChatRequest` has typed `TopP` (`types.go:26`) and `User`
(`types.go:37`). Wire output is correct (merged at marshal), but internal readers
of the typed fields see zero values — `providers/responses_adapter.go:46` and
`providers/openai/compatible_provider.go:330` copy `req.User`, and any future
consumer of `req.TopP` silently misses Anthropic-dialect traffic. One field, two
channels, chosen per-ingress.

**[SMELL] Armed duplicate-JSON-key hazard.** `mergeUnknownJSONObject`
(`core/json_fields.go:279–309`) concatenates struct JSON and extras with **no key
dedup** (unlike `MergeUnknownJSONFields`, `:98–107`). The two-channel split above is
exactly the scenario that would emit duplicate keys — last-wins for most parsers,
but undefined-behavior territory across providers.

**[SMELL] The IR has a shadow contract in `ExtraFields` string keys.** Portable
fields ride untyped: `stop`, `reasoning_effort`, `response_format`, `verbosity`,
`cache_control`, plus the Anthropic-door `top_p`/`user`. Every backend dual-source
resolves (`resolveAnthropicTopP` `request_translation.go:597–611`,
`stopSequencesFromExtra` `:616–647`, `resolveAnthropicReasoningEffort` `:573–589`).
The set of well-known untyped keys is documented nowhere; adding a consumer means
grepping for `Lookup(`. Also `UnknownJSONFields.Lookup` is a linear rescan called
5+ times per Anthropic request (`json_fields.go:179–217`).

**[SMELL] Postel policy is inconsistent at the Anthropic front door.**
OpenAI-dialect ingress preserves unknown fields (`UnknownJSONFields` machinery);
`anthropicapi.DecodeMessagesRequest` (`request.go:20–22`) does a closed-struct
`json.Unmarshal` — unknown members (`service_tier`, `betas`, metadata extras)
silently vanish, neither preserved nor rejected.

**[SMELL] Native-dialect asymmetry — Anthropic clients lose `cache_control`.**
`anthropicapi.ContentBlock` (`types.go:42–57`) has no `cache_control` field, so a
native Anthropic client's cache breakpoints are dropped at ingress, while an
OpenAI-dialect client *can* reach Anthropic `cache_control` via `ExtraFields`
(`request_translation.go:551–565`). The gateway supports the feature for the
foreign dialect but not the home dialect. Related: thinking-budget quantization —
`budget_tokens: 15000` → effort bucket → re-expanded to `10000` upstream
(`request.go:419–433` ↔ `request_translation.go:83–92`). Both are ADR-0007
"accepted negatives," but given translation fidelity is a GoModel selling point,
the ADR's own deferred mitigation — an **Anthropic→Anthropic fast path** (preserve
original body, apply patches to it) — is the highest-value dialect fix available.

**[SIMPLIFY] The Responses field list is maintained in ~8 places.**
`core/responses.go:11–41` + `:53–81` + `:85–113` (`ResponseInputTokensRequest` and
`ResponseCompactRequest` are byte-identical 25-field triplets), known-field lists +
decode/copy + marshal/copy in `responses_json.go:12–357`, plus a 23-field copy in
`providers/openai/compatible_provider.go:307–335`. Adding one field is shotgun
surgery across ~8 sites; miss one and it silently drops. Same pattern smaller for
chat (`core/chat_json.go:6–61` triple-entry; marshal is automatic via the alias
trick at `:64–72`, decode isn't). Share an embedded struct or generate the codecs.

**[SMELL] `ResponsesInputElement.UnmarshalJSON` swallows type errors** — every
field decode is `_ = json.Unmarshal(...)` (`responses_json.go:398–443`); a client
sending `"call_id": 42` gets a silently-zeroed field instead of a 400,
inconsistent with core's otherwise-loud validation.

**[SIMPLIFY] anthropicapi ↔ providers/anthropic: hand-mirrored inverses.** ~8
deliberately-parallel wire DTO types (ADR-documented, fine), but also four mapping
tables that exist twice in opposite directions (tool_choice, thinking↔effort,
image source↔data URL, stop_reason↔finish_reason) and **two mirrored 320/338-line
SSE state machines** (`anthropicapi/stream.go` ↔ `providers/anthropic/chat_stream.go`)
kept consistent only by comments like "Budget thresholds mirror the anthropic
provider's mapping" (`request.go:417–418`). Round-trip trace confirmed:
`/v1/messages` → Anthropic provider double-converts through the hub with no fast
path.

**[SIMPLIFY] Five independent SSE parsers.** `internal/streaming`'s boundary parser
(`observed_sse_stream.go:106–177`), `anthropicapi/stream.go:103–126`,
`providers/anthropic/chat_stream.go`, `providers/responses_converter.go`,
`providers/chat_chunk_sse.go` — subtly different CRLF/`data:`/multi-line handling.
Extract one shared event scanner. Also: stream observers receive `map[string]any`
(`observed_sse_stream.go:23`), forcing downstream re-typing — the `EventFilter`
perf bolt-on hints the payload contract is at the wrong altitude.

**[SIMPLIFY] `internal/batchrewrite` is a cycle-break, not a concept.** Its 130
LOC are 10-line context writes + a generic map merge; the actual orchestration and
metadata types live in `core/batch_preparation.go`. Understanding batch rewriting
requires reading both packages. Plus a 2×2 matrix of near-identical cleanup
wrappers expressed as five functions (`batchrewrite/helpers.go:50–130`). Fold into
one real batch package.

**[GOOD]** `UnknownJSONFields` single-walk extraction + marshal-alias round-trip
preservation is genuinely good Postel engineering; untranslatable Anthropic
features are rejected with actionable 400s pointing at the passthrough route
(`request.go:203–207,362–365`) instead of being mistranslated; the `/v1/messages`
streaming observer ordering (`messages_handler.go:110–121`) let usage/audit stay
dialect-agnostic; `internal/streaming` itself is small, documented, and dependency-clean.

---

## 4. Provider subsystem

**[SMELL] `internal/providers` root is four packages in one.** `router.go` (1,182),
`registry.go` (943), `registry_init.go` (586), `config.go` (702) — registry,
router, config loader, and shared translation/base utilities cohabit, and every
provider subpackage imports the root. Split into `registry` / `routing` /
`confload` with shared helpers in a leaf package.

**[SIMPLIFY] 13 OpenAI-compatible wrappers ≈ 2,219 LOC in four divergent styles.**
- *Named-field delegation* (groq 161, oracle 79, bailian 282, vllm 175): ~20
  hand-written pass-through one-liners each; ~51 of oracle's 79 lines appear
  verbatim in groq.
- *Embed `CompatibleProvider`* (openrouter 113, azure 204 — azure also holds two
  extra `CompatibleProvider`s for resource-root routing, `azure.go:29–31`).
- *Embed `ChatCompatible`* (xiaomi 54, zai 54, minimax 86, opencodego 163): xiaomi
  and zai differ only in doc strings, name/URL literals, one field, one override.
- *Full re-implementation* (deepseek 199, xai 368, ollama 281): hand-roll the chat
  surface; xai re-implements native batch/files (`xai/xai.go:264–368`) duplicating
  `providers.*OpenAICompatibleFile*` helpers — **~600+ LOC re-doing what
  `openai.CompatibleProvider` already provides**, for quirks expressible as hooks
  (deepseek reasoning-effort remap `deepseek.go:77–99`; ollama native `/api/embed`).

Recommendation: make "OpenAI-compatible provider" **data, not code** — a spec
struct (name, base URL, auth style, headers, quirk hooks). Most wrappers become
table entries; keep code only for genuine novelty (azure's URL scheme, opencodego's
dual-dialect routing `opencodego.go:123–146`, ollama's native embeddings).

**[SMELL] Capability leakage via embedding.** Because `CompatibleProvider` carries
the full 29-method surface (incl. `CreateSpeech`/`CreateTranscription`,
`openai/audio.go`), embedding it makes openrouter/azure automatically satisfy
`core.AudioProvider` et al. whether or not the upstream supports it. Capability
should be declared, not inherited by embedding accident (ties into the
`Capabilities()` descriptor idea, §2).

**[REMOVE] 16 test-only `NewWithHTTPClient` constructors** — zero non-test callers
(e.g. `groq/groq.go:49–57`, `oracle/oracle.go:39–47`). Also: several nil-client
paths fall back to `http.DefaultClient` (anthropic:77, gemini:120, deepseek:53,
xai:58, ollama:71, `openai/compatible_provider.go:55`) — still resilient via
llmclient but silently losing the tuned transport from `httpclient`.

**[SIMPLIFY] Copy-paste drift artifacts.** `isValidClientRequestID` copied
verbatim ×3 (`openai/openai.go:78–88`, `openrouter/openrouter.go:103–113`,
`azure/azure.go:172–182`); request-ID header drift (`X-Request-ID` groq/xai/ollama
vs `X-Request-Id` deepseek/vllm vs `X-Client-Request-Id` openai/openrouter/azure);
`passthrough_semantics.go` near-dupes (openai vs vllm differ by 3 name strings +
one entry — azure/openrouter already show the fix: reuse
`openai.Registration.PassthroughSemanticEnricher`).

**[GOOD] llmclient/httpclient layering is correct and single-sourced.**
`httpclient` is the sole transport builder; `llmclient` (retry + circuit breaker +
SSE) obtains its client from it; resilience config is canonical in
`config/resilience.go` with exactly one breaker implementation. Timeouts layer
without duplication (`llmclient/client.go:400–403` deliberately doesn't retry
client timeouts). Minor dead code: `circuitBreaker.Allow()`
(`circuit_breaker.go:72–75`, zero refs; live path uses `acquire()`), `State()`
test-only, and `httpclient.ClientConfig`/`NewHTTPClient` never called with custom
config in prod (all uses are nil-config `NewDefaultHTTPClient`).

**[SMELL] Realtime websocket path bypasses all resilience.** Provider realtime
files only build URL+headers; the dial happens at `realtime/proxy.go:46` via
`coder/websocket.Dial` with the library-default HTTP client — no tuned transport,
no retry/CB. Transparent-proxy design makes retry arguably wrong, but the
untuned transport is an oversight.

**[GOOD]** Explicit registration per ADR-0001 (`cmd/gomodel/main.go:139–156`,
`factory.Add` panicking on bad registrations, `providers/factory.go:70–86`) — no
`init()` magic. **[GOOD]** `internal/failover`'s providers dependency is *types
only* (`providers.ModelInfo`/`ModelWithProvider`), with failover declaring its own
minimal `Registry`/`RuleProvider` interfaces (`failover/resolver.go:25–34`) — a
correctly-executed seam, not the inversion the raw import graph suggests.

**[REMOVE] `virtualmodels/provider.go` is a 572-line decorator with one
production-reachable method.** The main request path consumes `virtualmodels.Service`
through small resolver seams (`gateway/interfaces.go:13–22`; compile-time check at
`virtualmodels/service.go:547–557`) — that part is clean. But the `Provider`
decorator (~30 methods mirroring the full capability surface) is constructed in
production exactly once: `app.go:301`, as the *bootstrap* guardrail executor —
a role whose interface has one method (`guardrails.ChatCompletionExecutor`) and
which is superseded at `app.go:525` when `SetExecutor` installs the
`InternalChatCompletionExecutor`. `NewProvider` (`provider.go:35`) has no non-test
caller. Replace the bootstrap with a one-method adapter (or wire the internal
executor from the start) and delete ~550 lines — this also removes the standing
obligation to mirror every future capability method into the decorator.

---

## 5. Persistence

**The single largest redundancy in the codebase.** Backend-specific store LOC
(non-test), per feature:

| Feature | LOC | Files |
|---|---|---|
| usage | 3,375 | reader/store/recalculate ×3 backends |
| auditlog | 2,169 | reader/store ×3 |
| budget | 1,174 | store ×3 |
| workflows | 1,146 | store ×3 |
| guardrails | 638 | store ×3 |
| failover | 625 | store ×3 |
| virtualmodels | 598 | store ×3 |
| authkeys | 528 | store ×3 |
| batch | 503 | store ×3 (+memory) |
| pricingoverrides | 403 | store ×3 |
| filestore | 299 | store ×3 (+memory) |
| tagging | 212 | store ×3 |
| **Total** | **11,670** | |
| responsecache vector stores | 1,248 | qdrant/pgvector/pinecone/weaviate/map |

Against this, `internal/storage` is 390 LOC of connection factories +
`ResolveBackend`, and `sqlutil` 143 LOC. **[SMELL]** The shared layer owns almost
nothing: no migrations (each store does its own `CREATE TABLE IF NOT EXISTS`, e.g.
`guardrails/store_sqlite.go:27`), no dialect abstraction, no query helpers beyond
sqlutil. Every schema change is re-implemented three times with no mechanism
forcing parity — the usage readers are 723/551/971 lines of independently-written
analytics queries; behavioral drift between backends is a *when*, not an *if*.

**[SIMPLIFY] sqlite vs postgres are largely the same SQL modulo `?`/`$1`.** A thin
dialect shim (placeholder rebinding + upsert/returning helpers) over `database/sql`
would collapse the two SQL backends into one implementation for nearly every
feature — cutting roughly a third of the 11.7k LOC and halving every future schema
change.

**[DECISION NEEDED] Is MongoDB pulling its weight?** Consistently the largest
implementation (usage reader: 971 LOC of aggregation pipelines) and the most likely
to drift. Options: (a) keep, but contract-test all backends against one behavioral
suite; (b) demote to core stores only (no analytics readers); (c) deprecate.
Carrying a third backend at full parity is the most expensive standing commitment
in the repo.

**[SMELL] Migration story is "hope."** Adding `labels` (#454/#467) required
hand-written per-backend column handling. A central migration runner (even minimal
ordered-DDL-per-backend) would make schema evolution one artifact instead of N
conventions.

**[SMELL] Inconsistent durability coverage.** `batch`/`filestore` have
`store_memory.go`; other features don't; `conversationstore` and `responsestore`
are memory-*only* (data lost on restart). If intentional, document it — they're
the only features whose durability silently ignores `STORAGE_TYPE`.

---

## 6. Cross-cutting request features & dependency inversions

**[COUPLING] Inverted ownership between cache, guardrails, and loggers.**
- `GuardrailRuleDescriptor` + `ComputeGuardrailsHash` are **defined in the cache
  package** (`responsecache/semantic.go:614`, `:598`) though they're
  guardrail-domain concepts; `guardrails` imports `responsecache` to use them
  (`guardrails/registry.go:8`). Move them into `guardrails` (or `core`); the cache
  should consume an opaque hash — it already receives it via context
  (`gateway/inference_prepare.go:237`).
- **[SMELL] `httptest` in production:** `responsecache.HandleInternalRequest`
  (`responsecache/responsecache.go:181–204`) synthesizes `httptest.NewRequest` /
  `NewRecorder` so internal guardrail LLM calls
  (`server/internal_chat_completion_executor.go:150`) can pass through cache code
  modeled as HTTP middleware. Give the cache a transport-free API
  (`Lookup/Store(ctx, endpoint, body)`); the middleware becomes a thin adapter.
  This also unblocks deleting the legacy `.Middleware()` path **(known #4)**.
- `responsecache` also imports `auditlog` and `usage`; `live` imports both too.

**[SMELL] Three parallel per-request recorders.** `auditlog` (its middleware alone
is 889 lines), `usage`, and `live` each observe requests independently, and `live`
imports both others' types to publish previews. A single request-completed event
(ids, model resolution, tokens, cost, labels, latencies, bodies-if-enabled) with
`usage`/`auditlog`/`live` as subscribers would remove the cross-imports and
collapse the label-threading problem — #454's 13-package spread was mostly this.

**[GOOD]** The guardrail execution chain is clean and non-recursive: patcher
interface at the gateway seam (`gateway/inference_prepare.go:138`), pipeline
compiled by `workflows` (`workflows/compiler.go:30`), `RequestOriginGuardrail`
preventing re-entry (`server/internal_chat_completion_executor.go:66,82`). The
`Catalog` interface (`guardrails/catalog.go:7–11`) is a well-chosen seam.

**[INCORRECT] CLAUDE.md says guardrails are "configured via config.yaml only."**
There is a persisted `guardrail_definitions` store on all three backends
(`guardrails/store_sqlite.go:27` et al.), admin CRUD
(`admin/handler_guardrails.go:48,91`), and YAML is only a boot-time seed
(`app.go:318–322`). Fix the doc.

---

## 7. Admin API & dashboard

**[SMELL] `admin.Handler` is a 20-field god-struct** (`admin/handler.go:32–54`; 16
injected collaborators, functional options `:157–268`). It scales linearly with
features by construction. Per-feature handler structs registering through the
existing `RouteRegistrar` seam (`admin/routes.go:9–14`) would remove the bottleneck.

**[SMELL] Business logic in handlers** (belongs in the feature services):
`GenerateFailoverRules` builds a resolver and assembles suggestions
(`admin/handler_failover.go:180–232`); budget ratio/period math + clamping
(`admin/handler_budgets.go:320–355,405–413`); audit×usage join
(`admin/handler_audit.go:149–188`); virtual-model upsert policy — "preserve stored
Enabled on omit/rename" (`admin/handler_virtualmodels.go:154–222`).

**[SMELL] Dashboard: 13.3k LOC hand-written JS + 6.2k CSS + 29 templates, with four
unguarded manual registration points per feature**: `<script>` include
(`templates/layout.html:325`), module-merge list
(`static/js/dashboard.js:1198–1201`), init-fetch call (`dashboard.js:270–271`),
hard-coded valid-pages array (`dashboard.js:218–227`) — plus page template,
`index.html:2–10` include, and sidebar entry. Miss one and the feature silently
half-works. A small manifest (`{slug, factory, fetchOnInit}`) collapses four
registration points into one.

**[GOOD]** `admin/errors.go` (`validationWriter`, `deactivateByID`, `handleError`
at `handler.go:465–537`) is genuinely DRY; `handler_usage.go` is thin with a
generic `usageSliceResponse` helper; shared template partials, content-hashed asset
URLs (`dashboard/dashboard.go:112–124`), and per-module JS tests are all better
than typical embedded dashboards.

---

## 8. Configuration

**[GOOD] Mostly centralized.** 21 files / 5.1k LOC in `config/`, and only **4**
`os.Getenv` escapes in `internal/`:

- **[SMELL]** `providers/openrouter/openrouter.go:87` — `OPENROUTER_SITE_URL` /
  `OPENROUTER_APP_NAME`, undocumented hidden config.
- **[SMELL]** `providers/anthropic/request_translation.go:33` — default-max-tokens
  env var read at request time.
- `providers/opencodego/opencodego.go:108` — `OPENCODE_GO_MESSAGES_MODELS`
  (documented, but bypasses the config layer).
- `httpclient/client.go:42` — generic env helper (structural second config path).

Route all four through `config` so startup validation and docs stay honest.

**[SMELL] Inconsistent env↔YAML merge semantics per feature.** Tagging: env entry
*replaces* the whole YAML entry, unset companions reset to defaults. Virtual
models: env *merges over* YAML per `source`. Two mental models for the same
operation; pick one convention going forward.

**[SMELL] `config/cache.go` (404 lines)** — semantic cache with 4 nested
vector-store configs; fine today, but if another backend lands, move per-store
config parsing next to each store. Also **(known #9)**: extract manual
failover-rule JSON parsing out of `loadFailoverConfig`.

**[SIMPLIFY] Every knob is documented in 4 places** (`.env.template`,
`config.example.yaml`, `CLAUDE.md`, `README.md`). Generate the env reference from
one annotated source — CLAUDE.md's config section has already drifted once (§6's
guardrails error).

---

## 9. Generated artifacts, docs, tests — smaller findings

- **[SMELL]** `cmd/gomodel/docs/docs.go` (8,056 lines) + `docs/openapi.json` are
  checked-in generated files that churn in most PRs. Minimum: mark
  `linguist-generated` in `.gitattributes`; better: verify-in-CI
  (`make swagger && git diff --exit-code`) so PRs stop hand-carrying them.
- **[REMOVE]** `gateway/refactor_findings_test.go` — legitimate regression tests
  named after the review session that produced them; rename to describe behavior.
- **[SMELL]** Dated snapshot docs (`docs/dev/2026-03-16_ARCHITECTURE_SNAPSHOT.md`,
  `docs/2026-04-09_CODEBASE_SNAPSHOT.md`) look authoritative while 3+ months stale.
- **[GOOD]** Test culture: test LOC ≈ source LOC in most packages
  (`internal/server`: 15.8k test vs 8k source), plus e2e/integration/contract
  suites and dashboard JS tests. `internal/streaming`'s cross-chunk boundary logic
  carries a 429-line test file.

---

## 10. Consolidated REMOVE / redundancy list

1. 16 test-only `NewWithHTTPClient` provider constructors (§4).
2. ~600+ LOC of deepseek/xai/ollama re-implementations of the `CompatibleProvider`
   surface, incl. xai's duplicate batch/files code (§4).
3. `virtualmodels/provider.go`: ~550 of 572 lines production-dead — replace the
   bootstrap guardrail executor with a one-method adapter (§4).
4. `isValidClientRequestID` ×3; request-ID header-name drift; openai/vllm
   `passthrough_semantics.go` near-dupes (§4).
5. `circuitBreaker.Allow()` (zero refs), `State()` (test-only), unused
   `httpclient.ClientConfig`/`NewHTTPClient` custom-config surface (§4).
6. `ResponseInputTokensRequest`/`ResponseCompactRequest` byte-identical field
   triplets + the ~8-site Responses field list (§3).
7. `CacheTypeBoth` **(known #1)**; legacy `ResponseCacheMiddleware.Middleware()`
   **(known #4)**; duplicated failover loops **(known #7)**; `app.go` failover-mode
   helpers **(known #6)**; cache-type vocabulary triplication **(known #5)**;
   dashboard `cacheOverview` duplication **(known #2)**; cached-only double-set
   **(known #3)**; workflow default duplication **(known #10)**.
8. sqlite-vs-postgres store pairs once a dialect shim exists (§5) — the largest
   single deletion available (~3–4k LOC).
9. CLAUDE.md guardrails "config-only" claim — factually wrong (§6).
10. Swagger phantom `ContentSchema` fields in IR structs, if the doc tooling can
    express unions another way (§2).

## 11. Suggested sequencing

1. **Now (mechanical):** §10 items 1, 4, 5, 7, 9; config escape-hatch cleanup (§8);
   rename `refactor_findings_test.go`; `.gitattributes` for generated files.
2. **Correctness first:** fix the `/v1/messages` `top_p`/`user` typed-field bypass
   and dedup `mergeUnknownJSONObject` (§3 — a real bug plus its amplifier); decide
   the unknown-field policy at the Anthropic door (preserve, like OpenAI dialects).
3. **Ownership fixes (small, high leverage):** move guardrail hash types out of
   `responsecache`; transport-free response-cache API (kills `httptest`); push the
   four admin-handler logic blocks into services; shrink `virtualmodels.Provider`.
4. **Field-list collapse (§3):** one source of truth for Responses/chat codecs;
   promote `stop` to a typed field; document the remaining well-known
   `ExtraFields` keys in one place.
5. **Provider table-ification (§4):** spec-driven openai-compatible provider;
   wrappers become data; add a declared-capabilities descriptor to fix
   embedding-leakage and start collapsing the Routable-twin interfaces (§2).
6. **Persistence strategy (§5):** decide mongo's fate; SQL dialect shim + central
   migrations. Biggest lever on both LOC and future feature cost.
7. **Request-event unification (§6):** one request-completed event with
   usage/auditlog/live as subscribers — this is what turns the next
   labelling-sized feature from 13 packages into ~3.
8. **Registration manifests (§7):** dashboard module manifest; per-feature admin
   handlers; then decompose `app.New()` into feature wiring functions (§2).
9. **Layer honesty (§2):** move server's `*_service.go` orchestrators into
   `gateway` (or fold `gateway` into `server`); Anthropic→Anthropic fast path
   (ADR-0007's own deferred item) once the dialect layer is consolidated.

## Coverage note

Providers, dialect translation/core, guardrails/cache coupling, admin/dashboard,
persistence inventory, config, llmclient/httpclient, failover/virtualmodels, and
blast-radius history were audited in depth with file:line verification. The
file-by-file interior of `internal/server`'s orchestration services and the
usage/auditlog/live body-capture overlap were assessed from targeted reads plus
the import graph; a focused pass over `translated_inference_service.go`,
`auditlog/middleware.go`, and `live/broker.go` is recommended before executing
step 7.
