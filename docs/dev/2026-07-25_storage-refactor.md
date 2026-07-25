# Storage Refactor — Lessons and Remaining Work

Started 2026-07-25 on `refactor/rework2` (#586), continued on `refactor/rework3`
(#587).

Every persisted domain used to ship three near-identical store implementations
plus a factory — 18,259 lines, 15.5% of the hand-written backend, over 17
domains. Normalising placeholders and type names, the SQLite and PostgreSQL
halves were 50–83% textually identical, and the entire difference came down to
six mechanical things: the driver handle type, `?` vs `$n`, the no-rows
sentinel, the `RowsAffected` signature, `Exec` arity, and DDL column type names.

`internal/storage/sqlx` absorbs exactly those. What it deliberately does *not*
abstract, and why, is documented at the code: the type tokens in `dialect.go`,
the isolation difference on `InTx`, the read/write timestamp pair on
`TimestampArg` and `sqlx.Timestamp`, and the opt-in rationale on the `sqlxtest`
and `mongotest` packages. Genuinely dialect-specific SQL is kept behind a
labelled `Dialect()` check at each site rather than forced into a false
abstraction.

This file keeps the two things that outlive the pull requests: what the work got
wrong, and what is left.

## What this got wrong, and how it surfaced

Each marks a real gap, not just a fixed bug.

1. **`InTx` was documented as serializing read-then-write. It does not.**
   Writing the conformance test disproved the claim: SQLite's `BEGIN IMMEDIATE`
   makes concurrent transactions queue, but PostgreSQL's default READ COMMITTED
   lets both read the same `MAX` and the second fail on the unique index. The
   difference is pre-existing — the two workflow stores already behaved this
   way. The suite now asserts atomicity, which does hold on both, and the
   isolation difference is documented on `InTx` rather than hidden. Code
   allocating a key from a `MAX` must handle the conflict.

2. **A schema break for existing PostgreSQL deployments.** `workflow_versions`
   was the one table where the backends disagreed on representation: SQLite
   stored `created_at` as INTEGER unix seconds, PostgreSQL as `TIMESTAMPTZ`.
   Unifying on unix seconds left existing PostgreSQL databases unreadable.
   **The unit suite could not catch it: `sqlxtest` builds each case a fresh
   schema, so it never meets a table created by an earlier release.** A smoke
   test against a real PostgreSQL database that already had the table did.

   The conversion floors rather than casts, because `EXTRACT(EPOCH FROM
   ...)::bigint` *rounds* — a `.6`-second row would land a second in the future
   and disagree with the truncation `time.Unix` performs on every row written
   afterwards. It is also one-way. Operator-facing consequences are in
   `docs/advanced/configuration.mdx`.

3. **A nil-handling bug in the new shutdown ordering.** `closerOf` initially
   handled typed-nil `*Result` but not a nil `storage.Storage` interface, which
   reflect reports as *invalid* rather than as a nil pointer. An existing narrow
   test caught it; nothing covered `app.New` → `Shutdown` end to end, which is
   why a lifecycle test now exists.

**The general lesson: a conformance suite over fresh schemas proves the dialects
agree, but says nothing about upgrading a database written by an older release.**
Migration paths need tests that start from the old table shape — several now do
(`guardrails`, `mcpgateway`, `failover`, `ratelimit`, `workflows`) — and
`tests/e2e/upgrade-compat.sh` runs the previous release's binary against an
empty database, then this one against the result, across all three backends.

## What is left

- **The `usage` store and its reader.** The reader is the one place where the
  divergence is real analytics SQL — timezone-aware bucketing and grouping, and
  on SQLite a generated `CASE` over DST segments that PostgreSQL does natively
  with `AT TIME ZONE` — not mechanical duplication.

  The store cannot move without it. `RecalculatePricing` is a *store* method
  whose row filter is built by the *reader's* `sqliteUsageConditions` /
  `pgUsageConditions`, and those emit different placeholder styles, so a
  unified store cannot borrow either one. Attempted during #587 and reverted:
  it is ~2,100 lines of implementation plus ~1,500 of tests and wants its own
  pass.

  The two implementations also differ *semantically*: SQLite paginates
  `RecalculatePricing` by `id > lastID` in batches of 500, while PostgreSQL
  selects every matching row in one statement with `FOR UPDATE`. Those are
  different memory and locking profiles. **Decision taken:** keep both paths
  behind a labelled `Dialect()` check so neither engine's behaviour changes.

- **F5, the config-shadows-store precedence.** Nine subsystems implement it
  three different ways (shadow at read, replace at startup, upsert at startup),
  so removing an entry from `config.yaml` and restarting behaves differently per
  subsystem. A product-behaviour decision, not a refactor.

- **MongoDB stays hand-written** by decision: document semantics are not a SQL
  dialect. `internal/storage/mongotest` now lets a domain's suite run against
  it, and seven domains do: batch, failover, filestore, mcpgateway,
  pricingoverrides, responsestore, virtualmodels. Nine still have no test that
  touches a database: auditlog, authkeys, budget, conversationstore, guardrails,
  ratelimit, tagging, usage, workflows. The pattern to copy is a
  `runStoreSuite` written against the domain's `Store` interface that calls both
  `sqlxtest.Run` and `mongotest.Run`.

- **Timestamps render in a different zone per backend.** Writes are UTC
  everywhere: `Dialect.TimestampArg` normalises on both engines, and
  `auditlog.TestStoreWritesTimestampsInUTC` asserts it against the stored column
  rather than the reader. Reads are not normalised — SQLite returns the `Z` text
  it stored, pgx materialises a `TIMESTAMPTZ` in the server's local zone — so
  the same entry serialises as `…T12:30:00Z` or `…T14:30:00+02:00` depending on
  the backend, with an offset that shifts with DST. Same instant, and any client
  parsing RFC 3339 correctly is unaffected. Pre-existing: neither hand-written
  reader normalised either. Normalising is one line in `Scan`, but it changes
  the timestamp string in every PostgreSQL deployment's admin API responses, so
  it was left as a separate decision rather than folded into a refactor.

- **Cache-type vocabulary is spread over seven packages** (`admin`, `auditlog`,
  `live`, `ratelimit`, `responsecache`, `server`, `usage`), with an import-cycle
  risk in consolidating it. Best done once the usage reader has one owner.
