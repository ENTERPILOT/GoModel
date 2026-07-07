# Rate Limiting Specification

Status: implemented (this branch)
Date: 2026-07-05

## 1. Why

GoModel has spend controls (budgets) but no traffic controls. Operators cannot
cap how *fast* a team or key consumes the gateway, protect provider quotas from
a runaway client, or give tenants predictable request/token allowances. Every
comparable gateway ships this; it is the most-used governance feature after
budgets.

### What users of other gateways need and use

Researched July 2026 across LiteLLM, Bifrost, Portkey, Kong AI Gateway,
Cloudflare AI Gateway, and Helicone (docs, source, GitHub issues).

Table stakes (all serious gateways):

- **Request limits (RPM)** and **token limits (TPM)** per API key / consumer,
  breach → **429** with a descriptive OpenAI-shaped error.
  LiteLLM: `rpm_limit`/`tpm_limit` per key/user/team; Bifrost:
  `request_max_limit`/`token_max_limit` with independent reset durations per
  virtual key; Portkey: request- or token-based limits per provider key;
  Kong: `tokens_count_strategy` with fixed/sliding windows.
- **Money budgets as a separate feature** with longer horizons (GoModel already
  has this; Bifrost splits 402 budget vs 429 rate, LiteLLM `max_budget`).
- **Hierarchy**: key → team → org scoping (LiteLLM key/user/team/org; Bifrost
  VK/team/customer). GoModel's `user_path` tree already models this.
- **Concurrency caps**: LiteLLM `max_parallel_requests` is a first-class,
  commonly set knob (protects against long-streaming pileups RPM cannot catch).

Loudest unmet needs / complaints (mostly LiteLLM issue tracker):

1. Multi-instance counter consistency (years of issues; drove LiteLLM's v3
   Redis-Lua rewrite). Bifrost OSS default is per-replica in-memory counters
   with periodic DB flush; exact sync is an enterprise gossip feature.
2. TPM accuracy under concurrency (post-call-only accounting overshoots).
3. Per-model-per-key limits (`model_rpm_limit` came from user demand).
4. **Daily windows (RPD/TPD)** for free-tier provider quotas — requested
   repeatedly, closed "not planned" in LiteLLM.
5. Honest `Retry-After` headers on 429 (LiteLLM drops/mangles them in several
   paths).
6. Priority/fairness instead of naked 429s (enterprise-only in LiteLLM).

Differentiators worth copying:

- **`x-ratelimit-*` success headers** (OpenAI spelling). Only LiteLLM does
  this; Bifrost, Portkey, and Cloudflare do not. OpenAI SDK users already
  parse them, so this fits Postel's Law.
- Kong's client headers + integer-seconds reset; Bifrost's clear
  request-vs-token breach messages.

## 2. Requirements

In scope (v1):

- Per-`user_path` rules with **max requests** and **max tokens** per window.
- Windows: `minute` (default mental model: RPM/TPM), `hour`, `day`, or custom
  `period_seconds` — daily windows are a deliberate gap-filler vs LiteLLM.
- **Concurrency caps** (max in-flight requests) as a window-less limit.
- Enforcement on every model inference path: chat completions, responses,
  messages, embeddings, audio, passthrough, realtime sessions, native batch
  submission (requests/tokens only; batch holds no concurrency slot).
- 429 with OpenAI-shaped error body, code `rate_limit_exceeded`, accurate
  `Retry-After`, and `x-ratelimit-*` headers on both success and breach.
- Management: admin API + dashboard page + YAML/env seeding (infrastructure as
  code), mirroring budgets exactly.
- Feature gate `RATE_LIMITS_ENABLED` (default `true`; zero cost when no rules
  exist).

Out of scope (v1) — documented future work, see §10:

- Per-model / per-model-per-path rules.
- Distributed (Redis) counters; v1 enforces per instance.
- Pre-call token estimation/reservation; v1 post-accounts tokens.
- Priority tiers, fair queuing, dynamic capacity division.
- Workflow feature gating (budgets have `features.budget`; rate limits get a
  workflow flag only if demanded).

## 3. Concepts and data model

A **rule** is one limit for one `user_path` subtree and one period:

```go
type Rule struct {
    UserPath      string // normalized, "/" = global
    PeriodSeconds int64  // 60, 3600, 86400, custom > 0, or 0 = "concurrent"
    MaxRequests   *int64 // nil = not limited by requests
    MaxTokens     *int64 // nil = not limited by tokens; invalid for period 0
    Source        string // "config" | "manual" (same semantics as budgets)
    CreatedAt, UpdatedAt time.Time
}
```

Primary key: `(user_path, period_seconds)`. Named periods: `minute`=60,
`hour`=3600, `day`=86400, `concurrent`=0. A `concurrent` rule's `MaxRequests`
is the max number of in-flight requests.

**Matching** reuses budget semantics: a rule applies to its path and all
descendants (`/team` matches `/team/app`, not `/team-x`). All matching rules
are checked; the first exhausted one rejects the request.

**Aggregation**: a rule owns ONE shared counter for its whole subtree — a rule
on `/team` limits the *sum* of traffic under `/team/**`, exactly like a budget
on `/team` caps the subtree's total spend. Per-key limits are expressed by
binding each managed key to its own `user_path` and defining rules there. This
keeps counter cardinality equal to the number of rules (no per-caller state,
no eviction concerns).

## 4. Enforcement algorithm

In-memory, per instance. No DB reads or writes on the hot path (unlike
budgets, which run a `SUM()` per request — fine for coarse spend windows,
wrong for per-minute admission control).

- **Requests — sliding window counter** (Cloudflare/Kong style): per rule keep
  `windowStart`, `current`, `previous`. Estimated usage =
  `previous × (1 − elapsed/period) + current`. Admission increments `current`
  after the check passes. This prevents the 2× boundary burst of naive fixed
  windows for one extra integer per rule.
- **Tokens — post-accounted sliding window**: admission rejects when the token
  window is already at/over `MaxTokens`; actual tokens are added when usage is
  recorded after the response completes. One request can therefore overshoot a
  token window once — the same documented behavior as Kong ("token counts come
  from the response") and pre-v3 LiteLLM. Token counts use `TotalTokens`.
- **Concurrency — gauge**: admission increments if `inFlight < MaxRequests`,
  and the reservation's `Release()` (idempotent) decrements when the request —
  including a streaming response or a realtime session — finishes.
- Requests count on admission even if the provider later fails (the work was
  admitted). Response-cache hits return before enforcement and count nothing,
  matching budget behavior and avoiding LiteLLM's "cached tokens counted
  toward TPM" bug class.

**Token accounting integration**: a decorator (`ratelimit.UsageTap`) wraps the
`usage.LoggerInterface` and feeds `UsageEntry.TotalTokens` (keyed by
`UsageEntry.UserPath`) into matching token windows before delegating. Entries
with `CacheType != ""` are skipped. Every usage-producing path — non-streaming
handlers, `StreamUsageObserver`, realtime `response.done`, audio — already
funnels through `Write`, so all modalities are covered by one hook.

Consequence: **token limits require usage tracking** (`USAGE_ENABLED=true`),
the same dependency budgets have. Request and concurrency limits work without
it. Startup logs a warning if token rules exist while usage tracking is off.

## 5. Request-path integration

Mirrors budgets: not a middleware, but an explicit call inside each dispatch
function, immediately **before** `enforceBudget` (rate limit checks are
in-memory and cheaper than the budget's DB query, so they run first):

```go
release, err := enforceRateLimit(c, s.rateLimiter) // 429 on breach
if err != nil { return handleError(c, err) }
defer release()
```

Call sites (the exact `enforceBudget` sites):
`translated_inference_service.go` (chat, responses, embeddings),
`messages_handler.go`, `audio_service.go`, `passthrough_service.go`,
`realtime_service.go` (the handler blocks for the session lifetime, so
`Release` on return holds the concurrency slot for the whole session), and the
batch orchestrator hook (`Acquire`+immediate `Release`, so batch submissions
count toward request windows without pinning a concurrency slot).

Identity comes from `core.UserPathFromContext` (set by the auth middleware
from the managed key's bound path or the user-path header), defaulting to `/`.

## 6. Client contract

Breach (any limit):

- Status **429**, body in the OpenAI error shape:

```json
{
  "error": {
    "type": "rate_limit_error",
    "message": "rate limit exceeded for /team/alpha: minute limit 100 requests used; retry after 12s",
    "code": "rate_limit_exceeded",
    "param": null
  }
}
```

- `Retry-After`: integer seconds until the sliding-window estimate would
  actually admit a retry — not just the next bucket boundary, where the
  previous window can still weigh enough to reject (constant `1` for
  concurrency breaches).
- The code stays `rate_limit_exceeded` for every limit kind — OpenAI SDKs and
  retry libraries key off that exact string; the message spells out whether
  requests, tokens, or concurrency tripped (Bifrost-style clarity without
  Bifrost's nonstandard codes).

Success (whenever at least one windowed rule matched):

- `x-ratelimit-limit-requests`, `x-ratelimit-remaining-requests`,
  `x-ratelimit-reset-requests` (integer seconds), and the `-tokens` triple —
  each taken from the most-constrained matching rule (least remaining) for its
  dimension. OpenAI spelling, so existing SDK plumbing picks them up.

## 7. Configuration surface

Feature gate (default on — with no rules the check is a nil/empty fast path):

```env
RATE_LIMITS_ENABLED=true
```

YAML (`rate_limits:` in `config.yaml`), mirroring `budgets:`:

```yaml
rate_limits:
  enabled: true
  user_paths:
    - path: "/team/alpha"
      limits:
        - period: "minute"
          max_requests: 100
          max_tokens: 50000
        - period: "day"
          max_requests: 10000
        - period: "concurrent"
          max_requests: 10
```

Env seeding, mirroring `SET_BUDGET_*` (path from suffix, `__` = segment
separator), with a compact named syntax:

```env
SET_RATE_LIMIT_TEAM__ALPHA="rpm=100,tpm=50000,rpd=10000,concurrent=10"
SET_RATE_LIMIT_="rpm=1000"
```

`rpm/tpm` → minute, `rph/tph` → hour, `rpd/tpd` → day, `concurrent` →
concurrency cap. A JSON array of limit objects is also accepted for custom
periods. Env entries replace the whole YAML entry for the same normalized
path. Config-sourced rules are re-seeded on startup (stale ones removed) and
are read-only in the dashboard; manual (admin API/dashboard) rows win over
config seeds at the same key — identical lifecycle to budgets.

Admin API (mirrors `/admin/budgets`):

- `GET /admin/rate-limits` — rules with live status (used, remaining,
  in-flight, window start/end).
- `PUT /admin/rate-limits` — upsert `{user_path, limit_key:{period|period_seconds}, max_requests?, max_tokens?}`.
- `DELETE /admin/rate-limits` — `{user_path, limit_key:{...}}`.
- `POST /admin/rate-limits/reset-one`, `POST /admin/rate-limits/reset` —
  zero live counters (ops escape hatch).

Dashboard: a "Rate Limits" page (list + create/edit/delete/reset), modeled on
the Budgets page; gated by the `RATE_LIMITS_ENABLED` dashboard-config flag.

## 8. Persistence

Rule definitions persist in a new `rate_limits` store with the standard
three-backend layout (`store_sqlite.go`, `store_postgresql.go`,
`store_mongodb.go`) resolved via `storage.ResolveBackend`, plus a
`factory.go` `Result{Service, Store, Storage}` supporting shared storage —
byte-for-byte the budget module pattern. No settings table: windows are
epoch-aligned UTC; rate limits do not need budget-style calendar anchors.

**Counters are ephemeral.** A restart starts fresh windows. For minute windows
this is invisible; a `day` window can under-count after a restart. That
tradeoff is documented — durable long-horizon control is what budgets (DB-sum
based) are for. Bifrost makes the same tradeoff in its default mode (memory
counters, 10 s DB flush).

## 9. Multi-instance stance

v1 enforces **per instance**: N replicas ⇒ effective limit ≈ N × configured.
This is stated plainly in the docs (LiteLLM hid this for years and it became
their top complaint; Bifrost OSS has the same semantics). The counter store
sits behind a small interface so a Redis backend (INCR + Lua, LiteLLM-v3
style) can be added without touching enforcement call sites. Budgets remain
the cross-instance-consistent control since they read the shared DB.

## 10. Scoped rules addendum (2026-07-05, second round)

Rules gained a scope discriminator: `(scope, subject, period_seconds)` is the
rule identity, where scope is `user_path` (the original consumer control),
`provider` (a configured provider instance by name), or `model` (subject
`openai/gpt-4o` pins one provider's model; bare `gpt-4o` matches the model on
any provider; matching is case-insensitive). One engine, one table, one
dashboard page serve all three.

The scopes differ in *enforcement posture*, not machinery:

- **user_path** stays admission control: breach -> 429 at ingress. Switching
  targets cannot relieve a consumer limit, so routing never consults it.
- **provider/model** are *routing constraints*. Admission checks them for the
  resolved primary route, but a breach does not 429 outright when failover
  targets exist: the request is admitted against its consumer limits, the
  stored 429 is stamped into the context, and dispatch skips the primary
  provider (calling it would serve the request and defeat the limit) and
  enters the failover sweep seeded with that error. The virtual-model
  balancer prefers targets with capacity via an explicit `TargetCapacity`
  probe (post-merge fix: originally a rate-limit-aware `Catalog.ModelAvailable`
  decorator, which conflated "throttled" with "dead" — hiding saturated
  aliases from /v1/models and sending fully saturated aliases down the
  all-targets-down 502 path). When every live target is saturated the
  balancer falls back to the first declared target so admission returns the
  honest 429. The failover sweep skips saturated candidates via a
  `RouteGate` on the orchestrator. The client only sees 429 when no viable
  target remains (or immediately, when no failover is configured).

Token accounting for provider/model scopes charges the *executed* route from
the usage entry (`ProviderName`/`Model`), which is exactly why these scopes
dodge the requested-vs-executed ambiguity that deferred per-model consumer
rules in round one: the provider's window must be charged for what actually
ran, and the entry records precisely that. Failed failover attempts are not
counted toward request windows (known undercount, documented); tokens are
always attributed correctly.

Read-model notes: `RouteAvailable` is a lock-held read-only probe (advance +
estimate without commit), so balancer polling consumes nothing. Batch
submission skips provider/model rules -- a batch file can mix models. Config
adds `rate_limits.providers`/`rate_limits.models` blocks and
`SET_PROVIDER_RATE_LIMIT_<NAME>` (distinct prefix to avoid the
`SET_RATE_LIMIT_*` suffix space; underscores -> hyphens like provider env
vars; model rules are YAML/admin-only since model ids are not env-safe).
Pre-scope tables/documents migrate in place at store init on all three
backends.

## 11. Future work

1. **Per-(user_path x model) matrix rules**: consumer-scoped model limits
   still carry the requested-vs-executed attribution question plus rule
   cardinality/precedence design; deferred until demanded.
2. **Redis counter backend** for exact multi-replica enforcement.
3. **Pre-call token reservation** (estimate + post-call reconciliation,
   LiteLLM v3 style) to close the one-request TPM overshoot.
4. **Priority / fairness**: reserve capacity fractions per label or path
   instead of hard 429s (top LiteLLM enterprise upsell).
5. Upstream `x-ratelimit-*` passthrough when GoModel itself imposes no limit.
6. Workflow `features.rate_limit` gating, if a use case appears.
7. Counter persistence across restarts (periodic flush) for day windows.
8. Attempt-level request counting for failover targets (today only the
   resolved primary route is charged a request; tokens are always correct).
9. Virtual-model (alias) subjects for model rules -- needs the requested
   alias carried onto usage entries for token attribution.

## 12. Testing

- `config`: YAML + `SET_RATE_LIMIT_*` parsing, replacement semantics,
  validation errors, usage-dependency warning.
- `ratelimit`: normalization; sliding-window math (boundary behavior, prior
  window decay); token post-accounting incl. cache-hit skip; concurrency
  acquire/release idempotency; path matching; statuses; config seeding
  (`ReplaceConfigRules`).
- Stores: SQLite CRUD round-trip (+ Mongo container test, mirroring budget's).
- `server`: 429 body/code, `Retry-After`, success headers, enforcement order
  vs budget, and release-on-completion for streaming.
- `admin`: handler CRUD + reset routes.
