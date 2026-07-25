# Backend Refactor — Survey and Outcome, 2026-07-25

Branch: `refactor/rework2` (surveyed at `7b74d973`).

Part 1 is the survey that scoped the work. Part 2 records what was executed,
what it cost, what it broke, and what is deliberately left.

---

# Part 1 — Survey

## 1. Baseline

| Metric | Value |
|---|---|
| Backend Go, non-test | 117,407 lines |
| — of which generated (`cmd/gomodel/docs/docs.go`) | 11,013 |
| — hand-written | **106,394** |
| Backend Go, test | 124,908 |
| Packages under `internal/` | 30 |
| `golangci-lint run` (errcheck, govet, ineffassign, staticcheck, unused, **dupl@100**) | **0 issues** |
| `deadcode -test ./...` | **4 findings**, all documented deliberate keeps |

## 2. What was already clean — no effort spent there

- **Dead code.** Three prior sweeps exhausted it. `deadcode -test ./...` returns
  4 symbols, all deliberate (`ext/registry.go`, the documented embedder API).
- **Lint and token-level duplication.** `dupl` runs at threshold 100 and the
  tree is green. All remaining duplication was *structural* — same shape,
  different API calls — which is precisely why a token-based detector never
  saw it.
- **Provider adapters.** `internal/providers/*/` is already well factored:
  small providers embed `openai.ChatCompatible` and are ~50 lines of real
  configuration each.
- **Admin handlers**, **`config/`**, and the **`internal/core` JSON layer**:
  all appropriately factored for what they do.

## 3. The finding

Every persisted domain shipped three near-identical store implementations plus
a factory: **18,259 lines, 15.5% of the hand-written backend**, over 17
domains, plus 6,374 lines of backend-specific store tests.

Normalising placeholders and type names, the SQLite and PostgreSQL halves were
50–83% textually identical. Reading the full `responsestore` diff, the *entire*
difference was six mechanical things: the driver handle type, `?` vs `$n`, the
no-rows sentinel, the `RowsAffected` signature, `Exec` arity, and DDL column
type names.

**The coverage asymmetry mattered more than the line count:**

| Backend | store test files | implementations | untested |
|---|---:|---:|---:|
| SQLite | 22 | 22 | 0 |
| PostgreSQL | **4** | 22 | **18** |
| MongoDB | 9 | 22 | 13 |

and the four PostgreSQL "tests" that existed only asserted generated SQL
strings — **no PostgreSQL store code executed against a database anywhere in
the suite.** Predictably it had drifted: `failover` normalised padded primary
keys through two independently written migrations, only one of them verified.

---

# Part 2 — Outcome

## 4. What was executed

| Phase | Work | Status |
|---|---|---|
| 1 | `internal/storage/sqlx` adapter + conformance suite | done |
| 2 | Migrate stores to one implementation | **16 of 17 domains** |
| 3 | Single storage connection; delete the duplicate constructors | done |
| 4 | `Shutdown` → ordered closer list | done |
| 5 | Small cleanups | partly; see §7 |

Net across the branch: **130 files changed, +8,460 / −10,470**.

| | before | after |
|---|---:|---:|
| SQLite store code | 5,570 | 1,705 |
| PostgreSQL store code | 5,015 | 1,448 |
| Unified `store_sql*.go` | — | 3,948 |
| `internal/storage/sqlx` | — | 820 |
| Factories | 2,232 | 1,762 |

**PostgreSQL subtests executing against a real database: 0 → 91.**

## 5. The adapter

`internal/storage/sqlx` absorbs exactly the five mechanical differences and
nothing else. Two things it deliberately does *not* abstract, both settled by
probing the drivers rather than assuming:

- **Value binding and scanning.** Both drivers already agree. A Go `bool` binds
  to SQLite `INTEGER` and PostgreSQL `BOOLEAN`; an `INTEGER` scans into
  `*bool`; `TEXT`/`JSON`/`JSONB` all scan into `[]byte`; nullable columns scan
  into `*string`/`*int64` on both. The `boolToSQLite` helpers and the
  `sql.NullString`-vs-`*string` split in the old stores were accidental
  divergence, not dialect requirements.
- **Genuinely dialect-specific SQL.** Kept behind a `Dialect()` check and
  labelled, rather than forced into a false abstraction. Three places qualify:
  - `conversationstore`'s JSON mutations — SQLite JSON1 functions vs PostgreSQL
    jsonb operators. These must mutate JSON server-side in a single `UPDATE` or
    concurrent Responses turns overwrite each other; there is no portable
    spelling.
  - `failover` and `ratelimit` legacy migrations — column rename in place vs
    table rebuild, and `PRAGMA` vs `information_schema` introspection.
  - `budget.SumUsageCost` — SQLite stores the usage timestamp as text and must
    convert it; PostgreSQL compares a real timestamp and must quote `"usage"`.

`sqlxtest.Run` executes a suite against every available dialect: SQLite always,
PostgreSQL when `GOMODEL_TEST_POSTGRES_URL` is set. The variable is
deliberately *not* `POSTGRES_URL` — a suite that creates and drops schemas
should not point at a configured application database by accident.

## 6. Three things this got wrong, and how they surfaced

Recording these because each marks a real gap, not just a fixed bug.

1. **`InTx` was documented as serializing read-then-write. It does not.**
   Writing the conformance test disproved the claim: SQLite's `BEGIN IMMEDIATE`
   makes concurrent transactions queue, but PostgreSQL's default READ COMMITTED
   lets both read the same `MAX` and the second fail on the unique index. This
   difference is pre-existing — the two workflow stores already behaved this
   way. The suite now asserts atomicity, which does hold on both, and the
   isolation difference is documented on `InTx` rather than hidden.

2. **A schema break for existing PostgreSQL deployments.** `workflow_versions`
   was the one table where the backends disagreed on representation: SQLite
   stored `created_at` as INTEGER unix seconds, PostgreSQL as `TIMESTAMPTZ`.
   Unifying on unix seconds — which every other table already used on both
   engines — left existing PostgreSQL databases unreadable. **The unit suite
   could not catch this: `sqlxtest` builds each case a fresh schema, so it never
   meets a table created by an earlier release.** A smoke test against a real
   PostgreSQL database that already had the table did. Fixed with an in-place
   `EXTRACT(EPOCH FROM ...)` conversion in `NewSQLStore`, plus a test starting
   from the legacy shape.

   **Two consequences operators need to know.** The conversion keeps the
   instant but not the representation: a workflow's `created_at` in
   `GET /admin/workflows` is floored to whole seconds and renders as UTC
   (`2026-07-25T14:04:29.08001+02:00` becomes `2026-07-25T12:04:29Z`). It
   floors rather than casts because `EXTRACT(EPOCH FROM ...)::bigint` *rounds*
   — a `.6`-second row would land a second in the future and disagree with the
   truncation `time.Unix` performs on every row written afterwards. Nothing
   depends on the lost precision — `active` is enforced by a unique partial
   index, so the one `ORDER BY created_at DESC ... LIMIT 1` query cannot tie —
   and this is the granularity SQLite always had. And the conversion is
   **one-way**: once the new binary has started against a PostgreSQL database,
   an older binary rolled back onto it fails at startup with `cannot scan int8
   (OID 20) in binary format into *time.Time`. Roll back by restoring a dump
   taken before the upgrade, not by swapping the binary.

3. **A nil-handling bug in the new shutdown ordering.** `closerOf` initially
   handled typed-nil `*Result` but not a nil `storage.Storage` interface, which
   reflect reports as *invalid* rather than as a nil pointer. An existing
   narrow test caught it; nothing covered `app.New` → `Shutdown` end to end,
   which is why a lifecycle test now exists.

The general lesson: **a conformance suite over fresh schemas proves the dialects
agree, but says nothing about upgrading a database written by an older
release.** Migration paths need tests that start from the old table shape —
several now do (`guardrails`, `mcpgateway`, `failover`, `ratelimit`,
`workflows`).

## 7. What is deliberately not done

- **The `usage` store, and both analytics readers.**

  The readers (`usage` 1,376 lines, `auditlog` 892) are the one place where the
  divergence is real analytics SQL — 7 SQLite date-function sites versus 6
  PostgreSQL ones for bucketing and grouping — not mechanical duplication. They
  deserve a deliberate decision about whether a shared query builder beats two
  honest implementations.

  The **`usage` store** was scoped as mechanical and is not, which is worth
  recording because the estimate came from counting dialect-specific constructs
  in the store file alone and that missed two couplings:

  1. `RecalculatePricing` is a store method whose row queries are built by the
     *reader's* `sqliteUsageConditions` / `pgUsageConditions`. Unifying the
     store without the reader means keeping a dialect switch that reaches into
     reader internals.
  2. The two implementations differ **semantically, not syntactically**: SQLite
     paginates by `id > lastID` in batches of 500, while PostgreSQL selects
     every matching row in one statement with `FOR UPDATE` row locking. Those
     are different memory and locking profiles, and picking one would change
     behaviour on the other engine.

  So `usage` should follow its reader rather than lead it. `auditlog`'s store,
  which had no such coupling, was migrated.
- **`CacheModeCached` set in four places** (F6a). Removing the handler's copy
  in favour of reader ownership breaks three admin tests that assert it through
  a *stub* reader; with the readers still split and not uniformly tested, that
  would weaken coverage to force a cleanup through. It belongs with the reader
  unification, when there is exactly one owner.
- **F5, the config-shadows-store precedence.** Nine subsystems implement it
  three different ways (shadow at read, replace at startup, upsert at startup),
  so removing an entry from `config.yaml` and restarting behaves differently
  per subsystem. This is a product-behaviour decision, not a refactor, and was
  explicitly deferred.
- **MongoDB.** Stays hand-written by decision: document semantics are not a SQL
  dialect. 13 of its 22 implementations remain untested.
- **F6 (b)–(e)**: cache-type vocabulary spread over four packages (import-cycle
  risk), `failover/resolver.go` recomputing selector identity per request,
  `config.loadFailoverConfig` mixing validation with a bespoke JSON loader.
- **Timestamps render in a different zone per backend.** Writes are UTC
  everywhere — `Dialect.TimestampArg` normalises on both engines, and
  `auditlog.TestStoreWritesTimestampsInUTC` asserts it against the stored
  column rather than the reader. Reads are *not* normalised: SQLite returns the
  `Z` text it stored, while pgx materialises a `TIMESTAMPTZ` in the server's
  local zone, so the same entry serialises as `…T12:30:00Z` or
  `…T14:30:00+02:00` depending on the backend, with an offset that also shifts
  with DST. Same instant, and any client parsing RFC 3339 correctly is
  unaffected. This is pre-existing — neither hand-written reader normalised
  either — and `sqlx.Timestamp` preserves it deliberately. Normalising is one
  line in `Scan`, but it changes the timestamp string in every PostgreSQL
  deployment's admin API responses, so it was left as a separate decision
  rather than folded into a refactor.

`docs/dev/possible-refactoring.md` items 2, 6, 7 and 10 are **stale** — the
dashboard JS they reference was replaced by the Svelte app, and the failover
helpers they name no longer exist. Items 3, 5, 8, 9 remain live and are carried
into §7 above.

## 8. Verification

Every commit passed the full pre-commit gate: `make test-race`, `make lint`
and `make fix-check` across all build tags
(`swagger,e2e,integration,contract`). Beyond that:

- CI runs a `postgres:18-alpine` service and sets
  `GOMODEL_TEST_POSTGRES_URL`, so all 91 PostgreSQL subtests execute there
  rather than skip.
- The built binary was smoke-tested on both backends: boot, `/health`, an
  auth-gated `/v1/models`, and graceful shutdown. This is what caught the
  `workflow_versions` schema break, which no unit test could have.
- The full release matrix (`tests/e2e/release-e2e-scenarios.md`, 196 scenarios
  over six gateways and real provider upstreams) passed with no failures and no
  skips, as did `tests/e2e/test-iac-virtualmodels.sh` (19 checks) and the
  dashboard suite (331 tests).
- **An upgrade harness** (`tests/e2e/upgrade-compat.sh`) boots the `main` binary against an empty database,
  writes a row through every store domain, restarts it so the snapshot comes
  from storage rather than from the caches the writes populated, then boots
  this branch on the same database. All 17 domain reads come back identical and
  every domain still accepts writes, on SQLite, PostgreSQL and MongoDB. This is
  what established the `created_at` representation change and the one-way
  migration in §6.2 — a fresh-schema conformance suite sees neither.

**To run the PostgreSQL half locally:**

```bash
make infra   # brings up postgres on :5432
GOMODEL_TEST_POSTGRES_URL="postgres://gomodel:gomodel@localhost:5432/gomodel" \
  go test ./internal/...
```

Without the variable the PostgreSQL subtests skip and SQLite still covers every
store, so CI stays green either way — but the variable should be set in CI, or
the coverage this work bought is left on the floor.
