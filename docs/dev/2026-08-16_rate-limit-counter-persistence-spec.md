# Rate Limit Counter Persistence

Status: approved design (updated 2026-08-16 for `#670` per-child templates)
Date: 2026-08-16
Extends: `docs/dev/2026-07-05_rate-limiting-spec.md` §8, §11.2, §11.7
Depends on: `feat(quotas): add per-child user-path templates` (`#670`)

## 1. Why

Request and token windows live only in process memory. A restart or
`gomodel --reload` (SIGHUP) starts them at zero. Minute windows barely
notice; an hour or day cap — the thing operators use for daily provider
quotas and team allowances — silently resets. The original spec documented
this and listed two follow-ups:

- §11.7 counter persistence across restarts (this work)
- §11.2 Redis live counters for exact multi-replica enforcement (later)

This spec does §11.7. It does not implement Redis-Lua, and does not
build a seam for it in advance (§5).

## 2. Goals

- Request and token sliding windows survive process restart, SIGTERM, and
  `--reload`.
- `admit()` stays in memory. Persistence is a background snapshot, never on
  the request path.
- Default on whenever rate limits are on. No extra dependency. Uses the
  existing SQLite / Postgres / Mongo store that already holds the rules.
- Crash loses at most one flush interval. Graceful stop and reload lose
  nothing.

## 3. Non-goals

- Shared counters across replicas. N instances still mean about N × the
  configured limit. Redis-Lua is the follow-up.
- Persisting concurrency gauges. After a restart nothing is in flight.
- Pre-call token reservation, per-(path × model) rules, or any other item
  from the original §11.
- A separate persist-on/off flag. `RATE_LIMITS_ENABLED=false` already
  builds no limiter. `RATE_LIMITS_FLUSH_INTERVAL=0` is the escape hatch
  that turns off the periodic loop only.
- Changing `#670` semantics or the `quota_templates` entitlement gate.
  Persistence lives in OSS `internal/ratelimit`. Pro (`../gomodel-pro`)
  only enables the capability; it gets snapshot restore of child
  partitions for free.

## 4. Architecture

```text
Service.Acquire / RecordTokens / RouteAvailable / Status / Reset
        │
        ▼
   limiter (in memory, authoritative)
        │  snapshot() / restore()
        ▼
   Store.LoadCounters / SaveCounters / DeleteCounter / DeleteAllCounters
```

Snapshots persist only the sliding windows, through the existing `Store`
that already holds the rules — four new methods on the SQL and Mongo
implementations, no second factory and no new dependency.

Cardinality is one row per **live window key**, not one per rule
definition:

- A shared rule (`per_child=false`, and every provider/model rule) still
  has one window. `partition` is empty.
- A per-child user-path template (`#670`) has one window per active
  direct child. `partition` is `Rule.EffectiveSubject` (for example
  `/customers/alice` under a template on `/customers`). Deeper paths
  share that child’s counter, same as live enforcement.

Idle child partitions are already dropped from memory after two periods
(`limiter_expiry.go`). The snapshot only writes keys that are still in
the limiter maps, so table size tracks live children, not historical
ones.

Several replicas sharing Postgres or Mongo last-write-wins **per
window key** — no replica's save deletes a key it did not write, so
replicas overwrite each other only where they overlap, and never wipe
each other's table. That is accepted: this work does not give shared
live counters, and the schema has no `instance_id`. A restart loads
whoever flushed that key last. Use one instance, or wait for the
Redis-Lua backend, when the limit must be exact across replicas.

## 5. No backend interface (decided against)

An earlier draft extracted a `counterBackend` interface so a Redis-Lua
implementation could drop in later. It is not built, on purpose: there
is one implementation, so the interface would be an abstraction with no
second caller to justify it. `Service` keeps its concrete `*limiter`,
and the Redis work — if it happens — extracts the seam then, against a
real second implementation rather than a guess at one.

What this change actually adds to the package surface is `Service.Start`
and the flush-interval option. `Service.Close` already existed (it stops
the child-expiry worker) and now writes the final snapshot first.

## 6. Snapshot data model

One row per live window key. Concurrent rules (`period_seconds = 0`)
are never written.

```text
scope                      TEXT     -- user_path | provider | model
subject                    TEXT     -- rule definition subject (template path)
partition                  TEXT     -- "" for shared; child path for per-child
period_seconds             INT64    -- > 0
requests_window_start      INT64    -- unix seconds, 0 if unused
requests_current           INT64
requests_previous          INT64
tokens_window_start        INT64
tokens_current             INT64
tokens_previous            INT64
updated_at                 INT64    -- unix seconds; drives staleness collection
PRIMARY KEY (scope, subject, partition, period_seconds)
```

`partition` is `ruleKey.partition` / `EffectiveSubject`. It is **not**
the request’s full user path. A template on `/customers` stores
`subject=/customers`, `partition=/customers/alice`, never
`/customers/alice/app`.

SQL table: `rate_limit_counters`. Mongo collection: `rate_limit_counters`
with a unique index on `(scope, subject, partition, period_seconds)`.
Created at store init with `CREATE TABLE IF NOT EXISTS` / `CreateIndex`.
Older databases just gain the table; no backfill.

Units match `windowCounter`: Unix seconds and `int64` counts. After
load, `advance()` already zeros a window more than one period old, so
rows need no TTL of their own; `SaveCounters` collects them once they
stop being written. Load still skips a child row whose window is
already past the in-memory expiry horizon (`windowStart + 2*period`),
and must call `trackCounterExpiry` for every restored partition so the
existing worker keeps pruning.

Package type (exported only if tests in another package need it;
otherwise unexported is fine):

```go
type windowSnapshot struct {
    Scope, Subject, Partition                           string
    PeriodSeconds                                       int64
    RequestsWindowStart, RequestsCurrent, RequestsPrevious int64
    TokensWindowStart, TokensCurrent, TokensPrevious    int64
}
```

A rule that only limits requests leaves the token fields at zero, and
the reverse. Zero `windowStart` with zero counts means “no window yet.”
A shared rule’s `Partition` is `""`.

## 7. Store methods

Add to the existing `Store` interface. No second factory.

- `LoadCounters(ctx) ([]windowSnapshot, error)` — all rows. A
  malformed row is skipped and logged; a query failure is a load
  error.
- `SaveCounters(ctx, []windowSnapshot) error` — **upsert** each
  snapshot (stamping `updated_at`), then collect rows that went two of
  their own periods without a write. Nothing is ever deleted to make
  room for a write, so a crash mid-save costs at most the flush
  interval, never a whole window. Two periods is the same staleness
  bound `restore` applies, so collection can only remove rows a load
  would have discarded anyway — which also means a save is not
  destructive to rows this instance does not know about (see the
  replica note in §4). SQL is one upsert loop plus one bounded
  `DELETE` in a transaction; Mongo is one unordered `BulkWrite` plus
  the equivalent `DeleteMany`, no transaction needed because no step
  depends on another. The payload is every **live window key** still in
  the limiter maps whose definition is a current windowed rule
  (period > 0). That includes one row per active per-child partition.
  It never includes leftover map entries for a dropped definition, and
  never includes concurrent keys.
- `DeleteCounter(ctx, scope, subject, periodSeconds) error` — every
  partition of that **definition** (matches `limiter.reset` /
  `sameDefinition`). Missing rows are not an error. Resetting a
  per-child template must not leave sibling children in the table.
- `DeleteAllCounters(ctx) error` — empty the table.

`Close` is unchanged.

## 8. Lifecycle

Reload builds the next generation while the current one is still
serving, then shuts the old one down, then starts the new one
(`run.serveUntilShutdown` / `watchForReload`). That order is load-bearing.

| Call | When | Persistence |
|---|---|---|
| `New` / `NewService` | `app.New`, tests | Empty limiter. No load, no flush, no loop. |
| `Start` | `App.startServer`, after the previous generation’s `Shutdown` | Load snapshot, apply rows whose **definition** still matches a current rule and whose `partition` matches the rule’s mode (empty iff `!PerChild`), re-arm child expiry, then start the flush loop if `flush_interval > 0`. Mark the generation **active** only if load succeeded. Start is idempotent. A failed load leaves the generation idle so it cannot flush an empty snapshot over durable windows. |
| `Close` (`Result.Close`) | `App.Shutdown` | Stop the flush loop. If **active**, write one last snapshot. Then stop the expiry worker and close the store. Close is idempotent. |

An idle replacement that is built and then discarded (failed listen,
process exiting during rebuild) is not active, so its `Close` must not
write an empty snapshot over the live one.

`App.startServer` calls `rateLimits.Service.Start(ctx)` before the HTTP
server accepts connections. Tests that construct a `Service` and never
call `Start` keep today’s in-memory-only behavior.

Flush copies the request and token maps under the limiter mutex, then
writes outside the lock. `admit()` never waits on storage.

A second mutex (`persistMu`) serializes snapshot writes against
reset/delete row deletes. `admit()` does not take it.

1. Flush: lock `persistMu`, copy maps **by value** (`windowCounter`
   structs, not the pointers `admit()` still mutates). Keep a key only
   when its definition is still a windowed rule. Merge request+token
   windows that share a `ruleKey` into one `windowSnapshot`.
   `SaveCounters`, unlock.
2. `Reset` / `DeleteRule` / `ResetAll`: clear memory (all partitions of
   the definition), lock `persistMu`, delete those snapshot rows,
   unlock.

Without that, a flush that sampled before a reset can `SaveCounters`
after the row delete and resurrect the burned window (S206 would flake).

Orphan snapshot rows (rule gone, row still there) are inert: `Start`
applies matching keys and ignores the rest, and they are collected two
periods after the last write. Construction must not delete snapshot
rows — see §10.

## 9. Configuration

```yaml
rate_limits:
  enabled: true
  flush_interval: 1   # seconds; 0 = no periodic loop
```

```env
RATE_LIMITS_FLUSH_INTERVAL=1
```

- Default `1` (set next to `Enabled: true` in `config.Load` defaults).
- `0` disables the periodic loop only. `Start` still loads. `Close` of
  an active generation still writes once. `--reload` and SIGTERM stay
  correct; SQLite’s single connection gets no extra writer on a timer.
- Negative values are rejected at config validation.
- No maximum. A large value just widens crash loss.
- Persistence is on whenever `RATE_LIMITS_ENABLED` is on. There is no
  second flag.

## 10. Service operations vs snapshot

| Operation | Memory | Snapshot |
|---|---|---|
| `Admit` / `RecordTokens` | update windows | next flush / Close |
| `ResetRule` | `limiter.reset` (all partitions of the definition) | `DeleteCounter` under `persistMu` (all partitions); store errors are returned |
| `ResetAll` | `limiter.resetAll` | `DeleteAllCounters` under `persistMu`; store errors are returned |
| `DeleteRule` | `limiter.reset` of that definition | `DeleteCounter` under `persistMu`; store errors are returned |
| `ReplaceConfigRules` / `Refresh` | already prunes maps when a definition disappears or `PerChild` flips (`Service.Refresh` after `#670`) | **do not touch the store** |

`ReplaceConfigRules` runs from `factory.New` → `seedConfiguredRules`
while the previous generation is still serving. Deleting snapshot rows
there would wipe a live hour/day window if the replacement is then
discarded (failed reload). Orphans are harmless instead: `Start` does
not apply them, and staleness collection removes them.

Do not add a second prune path. `Refresh` already drops limiter keys
when a definition is removed or shared/per-child mode changes; a later
flush therefore cannot reinsert those windows. `SaveCounters` is only
ever given live keys whose definition is still a windowed rule.

A per-child row is applied on `Start` only if the matching definition
still has `PerChild=true`. A shared row is applied only if that
definition is not per-child. A mode flip therefore starts empty for
that definition, matching `Refresh`.

Reset and delete clear memory first (the operator-visible effect), then
the row under `persistMu`. A failed row delete is returned to the
caller: the in-memory reset stands, but the operator has to know the
window can come back on the next restart. `persistMu` is what stops the
next flush from putting the row back.

## 11. Error handling

Persistence never fails a request. `admit()` does not see store errors.

- **Load failure:** log and leave the generation **idle**. Do not
  block the listener. Do not flush. The previous snapshot stays on
  disk. Skip corrupt rows; restore the good ones. Only a successful
  load (including “no rows”) activates persistence.
- **Periodic flush failure:** log and retry on the next tick. Memory
  stays authoritative.
- **Shutdown flush failure:** log and continue teardown. Do not hang
  past the existing shutdown budget.
- **Reset/delete row failure:** returned to the admin caller. Memory is
  already cleared, so the reset took effect for this process; the error
  says the durable row may outlive it.
- **Migrate:** creating the new table/collection fails store init the
  same way a missing `rate_limits` table would — that is a hard start
  error, not a soft persist error.

## 12. Testing

The suites live in `internal/ratelimit`; this section records only what
they are there to pin, not a per-test inventory.

- `New` neither reads nor writes. `Start` loads. `Close` after `Start`
  writes once. `Close` without `Start` writes nothing, and neither does
  admission — the "only a serving generation persists" rule of §8.
- A failed load leaves the generation idle, so a store hiccup cannot
  cost the persisted windows.
- `Close` during an in-flight load does not restore after shutdown.
- An in-flight flush cannot resurrect a completed reset or delete
  (`persistMu`), and a failed row delete reaches the caller.
- Per-child partitions round-trip independently, re-arm the expiry
  worker, and never cross-apply between shared and template modes.
- Concurrent keys are never written.
- One counter-store suite (`runCounterStoreSuite`) runs on SQLite,
  PostgreSQL and MongoDB: field-exact round trip, an omitted row
  surviving a save, a two-period-stale row being collected, and both
  delete paths. A malformed row is skipped, not fatal — asserted per
  backend, since each has its own decoder.

### Release E2E

S205–S207 in `tests/e2e/release-e2e-scenarios.md` cover reload survival
on SQLite, `reset-one` staying cleared across a reload, and the same on
PostgreSQL and MongoDB. Hour windows, so the cap outlives the reload
wait. Shared user-path rules only: the release stack is OSS and a
per-child admin write would 403, so per-child persistence is covered by
the unit suites instead.

The one non-obvious part is the shared `reload_release_gateway` helper.
It sends `SIGHUP`, then waits for a **new** `configuration reloaded`
line in that gateway's log before probing `/health`. That line is
logged after the old generation's `Shutdown` and before the new
`StartWithListener`, and a request sent straight after `kill -HUP` can
still be served by the old generation — without the log wait the
scenarios race.

Crash-before-`Close` (periodic flush only) and token-window reload stay
unit tests; concurrency is not persisted, so it has no E2E case.

## 13. Follow-up (not this change)

Redis live counters: every `Admit` becomes an atomic script on shared
keys (the key must include `partition`, same as `ruleKey`). No
snapshots. Chosen when `REDIS_URL` is set, or behind an explicit config
once someone is running HA and wants N replicas to share one limit.
That work extracts whatever seam it needs from the concrete `limiter`;
this change deliberately leaves none behind (§5).
