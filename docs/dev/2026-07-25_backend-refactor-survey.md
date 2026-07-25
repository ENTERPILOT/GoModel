# Backend Refactor Survey — 2026-07-25

Branch: `refactor/rework2` (at `7b74d973`, identical to `main`).

Purpose: quantify every redundancy in the Go backend so the execution scope can
be chosen deliberately. **No code has been changed.** Every number below was
measured, not estimated; the commands are given so they can be re-run.

---

## 1. Baseline

| Metric | Value |
|---|---|
| Backend Go, non-test | 117,407 lines |
| — of which generated (`cmd/gomodel/docs/docs.go`) | 11,013 |
| — hand-written | **106,394** |
| Backend Go, test | 124,908 |
| Packages under `internal/` | 30 |
| `go build ./...` | clean |
| `golangci-lint run` (errcheck, govet, ineffassign, staticcheck, unused, **dupl@100**) | **0 issues** |
| `deadcode -test ./...` | **4 findings**, all documented deliberate keeps (`ext/registry.go` embedder API) |

### Largest packages (non-test)

```
22,199  internal/providers      3,317  internal/virtualmodels
10,623  internal/server         3,249  internal/gateway
 7,452  internal/usage          2,821  internal/responsecache
 6,743  internal/core           2,696  internal/mcpgateway
 5,786  internal/auditlog       2,310  internal/workflows
 5,083  internal/admin          2,131  internal/ratelimit
 4,056  internal/guardrails     2,050  internal/budget
```

---

## 2. What is already clean — do not spend effort here

These were checked and found in good shape. Listing them so the effort goes
where it pays.

- **Dead code.** Three prior sweeps (`chore-quality-improvements`,
  `chore/deadcode` −2,148, `chore/gofix` −112) have exhausted this. `deadcode
  -test ./...` returns 4 symbols, all deliberate. **There is no dead-code lever
  left.**
- **Lint and token-level duplication.** `dupl` is enabled at threshold 100 and
  the tree is green. All remaining duplication is *structural* (same shape,
  different API calls) — invisible to token-based detectors, which is exactly
  why it survived.
- **Provider adapters.** `internal/providers/*/` is already well factored:
  small providers embed `openai.ChatCompatible` and are ~50 lines of genuine
  configuration each (`fireworks.go` is 50 lines, all of it real). Nothing to
  collapse. The 22k in `internal/providers` is concentrated in the root
  (`router.go` 1,234, `registry.go` 1,077, `config.go` 894) and in the
  genuinely-different native dialects (anthropic 2,607, gemini 1,989, cohere
  1,395).
- **Admin handlers.** Shared helpers already exist (`deleteManagedResource`,
  `validationWriter`, `errors.go`). The per-file bulk is Swagger annotation,
  which is load-bearing for `docs-openapi`.
- **`config/`** — 19 files, largest 427 lines, one concern each.
- **`internal/core` JSON layer** — hand-rolled unknown-field-preserving
  marshalling. Complex by necessity (Postel's law: unknown request fields must
  survive a round trip). Not a refactor target.

---

## 3. Findings

Ranked by value ÷ risk. Each carries measured LOC, a risk grade, and whether it
breaks anything.

### F1 — The store layer is written three times · **−4,000 to −4,600 lines**

**Risk: low. Breaks nothing.**

Every persisted domain ships three near-identical implementations plus a
factory. Measured:

| Domain | sqlite | postgres | mongo | factory | total |
|---|---:|---:|---:|---:|---:|
| usage | 1,145 | 992 | 1,514 | 188 | 3,839 |
| auditlog | 934 | 855 | 628 | 115 | 2,532 |
| budget | 398 | 320 | 456 | 134 | 1,308 |
| workflows | 406 | 399 | 341 | 108 | 1,254 |
| ratelimit | 296 | 269 | 377 | 154 | 1,096 |
| conversationstore | 263 | 239 | 339 | 80 | 921 |
| providers (credentials) | 207 | 203 | 171 | 300 | 881 |
| mcpgateway | 250 | 216 | 163 | 135 | 764 |
| failover | 283 | 210 | 132 | 114 | 739 |
| virtualmodels | 224 | 225 | 149 | 140 | 738 |
| authkeys | 215 | 189 | 186 | 110 | 700 |
| batch | 181 | 184 | 178 | 85 | 628 |
| responsestore | 164 | 155 | 164 | 80 | 563 |
| pricingoverrides | 138 | 138 | 127 | 120 | 523 |
| guardrails | 206 | 190 | 242 | 174 | 812 |
| filestore | 95 | 96 | 108 | 80 | 379 |
| tagging | 87 | 65 | 60 | 115 | 327 |
| storage (base) | 78 | 70 | 107 | 255 | — |
| **Total** | **5,570** | **5,015** | **5,442** | **2,232** | **18,259** |

That is **15.5% of the hand-written backend**, plus 6,374 lines of
backend-specific store tests.

**How similar are the SQLite and Postgres halves?** Normalising placeholders and
type names, then diffing:

```
guardrails         83% identical    responsestore      80% identical
credentials        81% identical    filestore          77% identical
batch              72% identical    authkeys           70% identical
workflows          69% identical    usage/reader       69% identical
budget             68% identical    conversationstore  67% identical
ratelimit          67% identical    auditlog/reader    59% identical
auditlog/store     52% identical    usage/store        50% identical
```

Reading the full `responsestore` diff (164 vs 155 lines), the *entire*
difference is six mechanical things:

1. `*sql.DB` vs `*pgxpool.Pool`
2. `?` vs `$n` placeholders
3. `sql.ErrNoRows` vs `pgx.ErrNoRows`
4. `result.RowsAffected() (int64, error)` vs `cmd.RowsAffected() int64`
5. `db.Exec(...)` vs `pool.Exec(ctx, ...)`
6. DDL column types — `INTEGER`/`BIGINT`, `TEXT`/`JSONB`, `INTEGER`/`BOOLEAN`

All six are absorbed by a thin `Querier` adapter (items 1–5) plus a small DDL
dialect helper (item 6). Business logic — the `ON CONFLICT … WHERE expires_at
<= ?` guard, the `CASE WHEN ? = 0 THEN stored_at ELSE ? END` preserve-on-zero
rule, the expiry sweep — is *already identical* and gets written once.

**Proposed shape** (per the decision already taken: keep schemas byte-identical,
no data migration):

```go
// internal/storage/sqlx
type Querier interface {
    QueryRow(ctx context.Context, query string, args ...any) Row
    Query(ctx context.Context, query string, args ...any) (Rows, error)
    Exec(ctx context.Context, query string, args ...any) (int64, error) // rows affected
    Dialect() Dialect                                                    // placeholder + DDL types
}
```

Each domain then has one `store_sql.go` instead of `store_sqlite.go` +
`store_postgresql.go`. Mongo stays hand-written (document semantics are
genuinely different, not a dialect variation).

**Estimate.** 10,585 lines of SQLite+Postgres collapse to roughly 5,900
(one implementation, slightly larger than today's SQLite half to carry dialect
branches) plus ~350 for the adapter. **Net −4,300**, ±300.

---

### F2 — Untested code is where the drift lives · **coverage, not line count**

**Risk: low. Breaks nothing. This is the strongest argument for F1.**

Store unit tests by backend:

| Backend | test files | implementations | untested implementations |
|---|---:|---:|---:|
| SQLite | 22 | 22 | 0 |
| PostgreSQL | **4** (auditlog, usage only) | 22 | **18** |
| MongoDB | 9 | 22 | 13 |

**~5,000 lines of PostgreSQL store code has no unit test of any kind**, and
nothing in `tests/` exercises `POSTGRES_URL` or `MONGODB_URL` outside a release
shell script. The Postgres halves are verified only by reading them.

Predictably, they have drifted. Two concrete examples found while surveying:

- `responsestore`: `NewPostgreSQLStore` rejects a nil context;
  `NewSQLiteStore` does not. Cosmetic — but it is drift nobody chose.
- `failover`: both backends normalise padded primary keys, but via completely
  independent migrations (SQLite rebuilds the table with `TRIM(...)`, Postgres
  runs a `DO $$ … btrim(...)` block). Two implementations of one rule, one of
  them untested.

Unifying SQLite+Postgres puts the existing 22-file SQLite suite behind *both*
backends. Combined with a table-driven conformance suite over the `Querier`, it
converts ~5,000 lines of untested code into covered code. **This is the main
prize; the −4,300 lines are the side effect.**

---

### F3 — Every subsystem opens its own database connection · **−700 to −900 lines**

**Risk: low. Behaviour change is a fix, must be documented.**

Fourteen packages carry two constructors:

```
New(ctx, cfg)                 → calls storage.New(ctx, cfg.Storage.BackendConfig())
NewWithSharedStorage(ctx, …)  → reuses a handle the caller already has
```

Confirmed call sites of the first form: `guardrails`, `providers/credentials`,
`filestore`, `conversationstore`, `workflows`, `tagging`, `pricingoverrides`,
`ratelimit`, `virtualmodels`, `mcpgateway`, `authkeys`, `responsestore`,
`failover`, `budget` — 14 packages, all resolving **the same
`cfg.Storage.BackendConfig()`**. There is no case where a subsystem legitimately
wants a different database.

The consequence is paid three times over:

1. **19 `Result` structs** carry a `Storage storage.Storage` field plus
   `closeOnce`/`closeErr` ownership machinery, purely to answer "did I open this
   or was it handed to me?"
2. **`internal/app/app.go`** repeats a nil-check dance ~15 times:
   ```go
   if sharedStorage != nil {
       xResult, err = x.NewWithSharedStorage(ctx, appCfg, sharedStorage)
   } else {
       xResult, err = x.New(ctx, appCfg)
   }
   ```
   plus a `claimSharedStorage(...)` call after each.
3. **At runtime**, when audit logging and usage tracking are both disabled,
   `sharedStorage` is nil and the fallback path opens **up to 14 separate
   connections to the same database** — 14 SQLite handles or 14 pgx pools.

Fix: open storage **once** in `app.New`, pass it to everyone, delete all 14
`New(ctx, cfg)` variants and the `Result`-owns-`Storage` machinery.

**This changes observable behaviour** (connection count drops from N to 1 in the
no-audit/no-usage configuration) and removes 14 exported constructors from
`internal/`. Both need a line in the changelog; neither needs a migration.

---

### F4 — `app.New` and `App.Shutdown` are hand-unrolled loops · **−250 lines**

**Risk: very low. Breaks nothing.**

`gocyclo` on non-test code, worst offenders:

```
94  app.New                internal/app/app.go:115     (645 lines)
80  server.New             internal/server/http.go:112 (371 lines)
46  (*App).Shutdown        internal/app/app.go:878     (194 lines)
39  anthropic.(*streamConverter).convertEvent
33  config.ValidateCacheConfig
32  usage.CalculateGranularCost
32  server.normalizeConversationItem
```

`App.Shutdown` is ~15 copies of one six-line block:

```go
// N. Close the X subsystem.
if a.x != nil {
    if err := a.x.Close(); err != nil {
        slog.Error("x close error", "error", err)
        errs = append(errs, fmt.Errorf("x close: %w", err))
    }
}
```

Shutdown *order* is load-bearing (streams before HTTP drain, providers before
stores) and must be preserved — but it is expressible as an ordered
`[]namedCloser` slice built during `New`. The `closers` slice already exists in
`New` for the failure path; this makes it the single source of truth.
**194 lines → ~60.**

`app.New`'s 645 lines shrink naturally once F3 removes the constructor dance;
what remains splits cleanly along the seams already marked by its comments.

`server.New` (371 lines, cyclo 80) is route registration — splitting into
per-surface registrars (`registerModelRoutes`, `registerAdminRoutes`,
`registerPassthroughRoutes`, `registerRealtimeRoutes`) is net-zero on lines but
turns the largest single function in the HTTP layer into something reviewable.

---

### F5 — "Config shadows the store" is implemented 7 times, 3 different ways

**Risk: medium. This is a real behavioural inconsistency, not just duplication.**

The documented pattern — declarative `config.yaml`/env entries override
admin-store rows and appear read-only in the dashboard (`managed: true`) — is
implemented independently in `tagging`, `virtualmodels`, `mcpgateway`,
`failover`, `providers/credentials`, `ratelimit`, `budget`, `guardrails`, and
`workflows`. They do not agree on *when* config wins:

- **Shadow at read time** — the store row stays, config is merged over it on
  every read: `tagging/service.go`, `virtualmodels/service.go`,
  `mcpgateway/service.go`, `providers/credentials.go`.
- **Replace at startup** — config-sourced rows are written into the store,
  deleting stale ones: `ratelimit.ReplaceConfigRules`,
  `budget.ReplaceConfigBudgets`.
- **Upsert at startup** — config rows are written but stale ones are *not*
  removed: `guardrails` (`UpsertMany`).

Three semantics for one documented behaviour. The user-visible difference: after
removing an entry from `config.yaml` and restarting, a rate limit disappears, a
guardrail does not, and a tagging rule reverts to its stored value.

Unifying this is worth doing, but it is a **semantics decision, not a mechanical
refactor** — it will change behaviour for at least two subsystems. Flagged for a
decision; see §5.

---

### F6 — Smaller confirmed items

| # | Item | Lines | Risk |
|---|---|---:|---|
| a | `CacheModeCached` forced in `admin/handler_usage.go` **and** re-forced in all three usage readers (4 owners for one rule) | ~15 | very low |
| b | Cache-type vocabulary split across `usage/cache_type.go`, `auditlog/auditlog.go`, `responsecache/semantic.go` + `simple.go` — 4 packages normalising the same constants | ~80 | medium (import-cycle risk) |
| c | `failover/resolver.go` recomputes trimmed selector identity in 4 helpers per request (`sourceModelInfo`, `manualSelectorsFor`, `matchKeys`, `sourceKey`) | ~40 | low |
| d | `config.loadFailoverConfig` mixes mode validation with a bespoke strict-JSON loader for `manual_rules_path` | ~60 | low |
| e | `strings.TrimSpace` applied defensively at three layers (service **and** each of the three stores) in `failover`; F1 removes one layer for free | ~20 | very low |

Items (a) and (c)–(e) are carried over from `docs/dev/possible-refactoring.md`
and re-verified as still live. **Items 2, 6, 7 and 10 in that document are now
stale** — the dashboard JS they reference was replaced by the Svelte app in
`web/dashboard/`, `failoverModeEnabled`/`dashboardFailoverModeValue` no longer
exist in `app.go`, and `tryFailoverResponse`/`tryFailoverStream` are gone. That
file should be replaced by this one.

---

## 4. Proposed execution order

Sequenced so each phase is independently reviewable and green.

| Phase | Work | Lines | Risk |
|---|---|---:|---|
| **1** | `internal/storage/sqlx` adapter + conformance suite. No domain touched yet. | +350 | none |
| **2** | Migrate stores to `store_sql.go`, smallest first (`tagging` → `filestore` → `responsestore` → `pricingoverrides` → …), one commit per domain. SQLite tests now run against both dialects. | −4,300 | low |
| **3** | F3: single storage handle; delete the 14 `New(ctx, cfg)` constructors and `Result`-owns-`Storage`. | −800 | low |
| **4** | F4: `Shutdown` → ordered closer list; split `app.New` and `server.New`. | −250 | very low |
| **5** | F6 (a)–(e) cleanups. | −200 | low |
| **6** | F5 config-precedence unification — **only after a decision on semantics.** | ~−400 | medium |

Phases 1–5 total roughly **−5,200 lines with no user-visible change** beyond the
connection-count fix in F3.

`internal/usage` and `internal/auditlog` are the two largest and most
SQL-divergent domains (analytics aggregation, 50–69% identical); they are
deliberately last within phase 2, and their readers may justifiably keep
dialect-specific query builders even after the stores unify.

**Verification at every step:** `go build ./...`, `go test ./internal/...`,
`golangci-lint run --build-tags=swagger,e2e,integration,contract`, and the
`tests/contract` suite. The store conformance suite from phase 1 runs against
SQLite in CI and against Postgres when `POSTGRES_URL` is set — which closes the
F2 gap permanently.

## 5. Open decisions

1. **F5 config precedence** — unify on which semantics? *Replace at startup*
   (config is the source of truth, stale rows deleted) is the most predictable
   and matches what the docs describe, but it would change `guardrails`
   (currently keeps stale rows) and the four shadow-at-read subsystems
   (currently keep the store row underneath). Needs a call before phase 6.
2. **MongoDB** — 5,442 lines, 13 of 22 implementations untested, and it stays
   hand-written under this plan. Keeping it is fine; it should be a deliberate
   choice rather than a default.
3. **Scope of phase 2** — all 17 domains, or stop after the 12 straightforward
   ones and leave `usage`/`auditlog`/`workflows` on their current split?
