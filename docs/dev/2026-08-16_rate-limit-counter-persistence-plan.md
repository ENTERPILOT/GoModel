# Rate Limit Counter Persistence Implementation Plan

> **For agentic workers:** Execute task-by-task. Spec:
> `docs/dev/2026-08-16_rate-limit-counter-persistence-spec.md`.

**Goal:** Persist request/token sliding windows across restart and `--reload`
using the existing SQL/Mongo store, without putting I/O on `admit()`.

**Architecture:** Keep the in-memory limiter as the live store. Snapshot
windows (including per-child `partition` keys) to `rate_limit_counters`.
`New` does not load or flush. `Start` (from `App.startServer`) loads then
optionally ticks. `Close` of an **active** generation writes once.
`persistMu` serializes flush vs reset/delete. Construction never deletes
snapshot rows.

**Tech Stack:** Go, existing `internal/ratelimit` limiter + SQL/Mongo stores,
`RATE_LIMITS_FLUSH_INTERVAL` (default 1s).

---

## Files

- Modify: `config/ratelimit.go`, `config/config.go`, `config/ratelimit_test.go`
- Modify: `internal/ratelimit/store.go`, `store_sql.go`, `store_mongodb.go`,
  `service.go`, `factory.go`, `service_test.go`, `store_sql_test.go`,
  `store_mongodb_test.go`
- Create: `internal/ratelimit/snapshot.go`, `persist.go`, `persist_test.go`,
  `snapshot_test.go`
- Modify: `internal/app/app.go`
- Modify: docs, `.env.template`, `config/config.example.yaml`,
  `tests/e2e/release-e2e-scenarios.md`

### Task 1: Config

`FlushInterval int` on `RateLimitsConfig` (`yaml:"flush_interval"`
`env:"RATE_LIMITS_FLUSH_INTERVAL"`). Default `1` in `config.Load`. Reject
`< 0`. `0` is valid (no periodic loop).

### Task 2: Store

Add `LoadCounters`, `SaveCounters` (replace-all), `DeleteCounter` (all
partitions of a definition), `DeleteAllCounters`. SQL table and Mongo
collection `rate_limit_counters`, PK
`(scope, subject, partition, period_seconds)`. Mongo uses
transaction-plus-standalone-fallback like `ReplaceConfigRules`.

### Task 3: Snapshot + persist

`windowSnapshot` with `Partition`. Limiter `snapshot`/`restore` copy
`windowCounter` by value. Restore skips mode-mismatch and expired child
windows (`windowStart + 2*period`); re-arms `trackCounterExpiry`.
`Service.Start` / flush loop / `Close` compose with existing expiry
`Close`. `Reset*`/`DeleteRule` delete rows under `persistMu`.
`Refresh`/`ReplaceConfigRules` do not touch the snapshot store.

### Task 4: Wire + docs + E2E

`factory` passes flush interval. `App.startServer` calls `Start` before
listen. Docs: windows survive bounce; concurrency does not; still per
instance. S205–S207 shared hour rules + `reload_release_gateway`.
