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

This spec does §11.7 and extracts the seam §11.2 will need. It does not
implement Redis-Lua.

## 2. Goals

- Request and token sliding windows survive process restart, SIGTERM, and
  `--reload`.
- `admit()` stays in memory. Persistence is a background snapshot, never on
  the request path.
- A later Redis-Lua backend can replace the in-memory limiter without
  changing `Acquire`, the usage tap, or `RouteAvailable`.
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

Two seams, only the first used as a live store today.

```text
Service.Acquire / RecordTokens / RouteAvailable / Status / Reset
        │
        ▼
  counterBackend          (unexported; package ratelimit)
        │
        ├── memoryBackend     current limiter + snapshot load/save
        └── (later) redisLua  live INCR; no snapshots
```

Snapshots are an implementation detail of `memoryBackend`. They persist
only the sliding windows. The backend talks to the existing `Store`,
which grows four methods implemented by the SQL and Mongo stores already
used for rules.

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
window key**. That is accepted: this work does not give shared live
counters, and the schema has no `instance_id`. A restart loads whoever
flushed last. Use one instance, or wait for the Redis-Lua backend, when
the limit must be exact across replicas.

## 5. `counterBackend`

Unexported interface in `internal/ratelimit`, matching what `limiter`
already does:

- `Admit(rules []Rule, now time.Time) (HeaderSnapshot, []ruleKey, *ExceededError)`
- `Available(rules []Rule, now time.Time) bool`
- `Release(held []ruleKey)`
- `RecordTokens(rules []Rule, tokens int64, now time.Time)`
- `Status(rule Rule, now time.Time) Status`
- `Reset(key ruleKey)`
- `ResetAll()`

`Service` holds a `counterBackend` instead of a concrete `*limiter`.
`ruleKey` stays unexported and already includes `partition` (`#670`).
`Reset(key)` keeps today’s `sameDefinition` behavior: it clears every
partition of that `(scope, subject, period)` definition.

Public API additions: `Service.Start` and the flush-interval config
field. `Service.Close` already exists (stops the child-expiry worker);
persistence `Close` must call it after the final snapshot.

The memory implementation is the current `limiter` plus snapshot
helpers. A future Redis-Lua type implements the same interface and
ignores the snapshot store.

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
updated_at                 INT64    -- unix seconds, diagnostics only
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
rows do not need a TTL. Load still skips a child row whose window is
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
  snapshot, then delete rows that are no longer in the set. Never
  delete-all first: a crash or a failed insert must leave the previous
  generation intact. SQL does upsert+prune in one transaction. Mongo
  uses the same algorithm, with the existing transaction-plus-
  standalone-fallback. The payload is every **live window key** still
  in the limiter maps whose definition is a current windowed rule
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

Orphan snapshot rows (rule gone, row still there) are dropped only by
an **active** generation: `Start` applies matching keys and ignores the
rest; the next flush replace-all omits them. Construction must not
delete snapshot rows — see §10.

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

Document in `.env.template`, `config/config.example.yaml`,
`docs/features/rate-limits.mdx`, and `CLAUDE.md` / `Agents.md`. Remove
the wording that counters reset on restart / `--reload`. Keep the
wording that counters are per instance (N replicas ≈ N × limit) and
that concurrency is in-memory only.

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
discarded (failed reload). Orphans are left for the **active**
generation: `Start` does not apply them; the next flush replace-all
drops them.

Do not add a second prune path. `Refresh` already drops limiter keys
when a definition is removed or shared/per-child mode changes; a later
flush therefore cannot reinsert those windows. `SaveCounters` is only
ever given live keys whose definition is still a windowed rule.

A per-child row is applied on `Start` only if the matching definition
still has `PerChild=true`. A shared row is applied only if that
definition is not per-child. A mode flip therefore starts empty for
that definition, matching `Refresh`.

Reset and delete clear memory first (the operator-visible effect), then
the row under `persistMu`. A failed row delete is logged; the in-memory
reset still stands. `persistMu` is what stops the next flush from
putting the row back.

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
- **Reset/delete row failure:** log at error level. Memory is already
  cleared.
- **Migrate:** creating the new table/collection fails store init the
  same way a missing `rate_limits` table would — that is a hard start
  error, not a soft persist error.

## 12. Testing

### Unit / store

- Snapshot encode/restore: `estimate` after load matches the pre-flush
  value for both request and token windows.
- A snapshot whose `windowStart` is more than one period old advances
  to zero.
- Concurrent keys never appear in a snapshot.
- `New` does not read or write. `Start` loads. `Close` after `Start`
  writes a final snapshot. `Close` without `Start` writes nothing.
- `flush_interval=0` still loads and still flushes on `Close`; the loop
  never ticks.
- `ResetRule` / `ResetAll` / `DeleteRule` clear memory and the row; a
  later `Start` on a new service does not resurrect them.
- `ReplaceConfigRules` dropping a config rule prunes the limiter maps
  and does not call `DeleteCounter`. After `Start`, the next flush
  omit that key from `SaveCounters`.
- An in-flight flush cannot resurrect a completed `ResetRule` /
  `DeleteRule` (`persistMu`).
- SQL store (and Mongo, same as rules) round-trip: replace, load,
  delete one, delete all. Migration creates `rate_limit_counters` on an
  existing DB.
- Config: default interval is 1; `RATE_LIMITS_FLUSH_INTERVAL=0` is
  valid; a negative value is rejected.
- Existing admit/release/header tests stay in-memory. A recording
  `Store` asserts `Admit` does not call `SaveCounters`.
- Per-child: two children of one template flush as two rows (same
  `subject`, different `partition`); after `Start` each child is still
  isolated. Reset of the template deletes both rows. A restored
  partition is registered with the expiry worker. A shared-rule row is
  not applied to a definition that is now `PerChild`, and the reverse.
- OSS without `quota_templates` still persists shared / provider /
  model windows. Per-child config continues to abort startup / reject
  admin writes; persistence does not change that gate.

### Release E2E

Add to `tests/e2e/release-e2e-scenarios.md` (after S204). Update the
file header count and the stateful-note list.

Shared helper in the common environment block:

- Export `RELEASE_STACK_DIR` (default `/tmp/gomodel-release-stack`).
- `reload_release_gateway <name> <base_url>` sends `SIGHUP` to
  `$RELEASE_STACK_DIR/<name>/server.pid`, then waits until that
  gateway’s `logs/server.log` contains a **new** `configuration reloaded`
  line, then retries `/health` briefly. `configuration reloaded` is
  logged after the old `Shutdown` and before the new
  `StartWithListener`; the next request may sit in the held accept
  queue until the new listener is up. A request sent immediately after
  `kill -HUP` can still hit the old generation — the log wait is
  required.

Use **hour** windows so the cap outlives the reload wait. Each scenario
creates a `$QA_SUFFIX`-scoped **shared** user-path rule (`per_child`
unset) and deletes it. The release stack is OSS and has no
`quota_templates` entitlement; a per-child admin write would 403.
Per-child persistence is the unit tests above, not this matrix.

- **S205 — Request-window counters survive `--reload` (SQLite).**
  `max_requests=1` on `$BASE_URL`. First chat succeeds. Reload
  `sqlite-main` (old `Close` writes the snapshot; no sleep required
  for the happy path). Second chat is `429` with
  `code: rate_limit_exceeded`. Delete the rule. Crash-before-`Close`
  (periodic flush only) is a unit test, not this scenario.
- **S206 — `reset-one` stays cleared across `--reload`.** Same shape:
  burn the hour window, `reset-one`, reload `sqlite-main`. Next chat
  succeeds. Delete the rule. `persistMu` is what makes this
  deterministic.
- **S207 — Same request-window survival on PostgreSQL and MongoDB.**
  S203-style loop over `$PG_BASE_URL` / `$MONGO_BASE_URL`, reloading
  `pg-smoke` and `mongo-smoke`. Distinct paths, delete each rule.

S205–S207 reload a shared gateway. That is safe in this sequential
runner (same class as S137). Token-window reload is covered by S157
plus unit tests; concurrent is not persisted and is not an E2E case.

## 13. Docs and comments

- `docs/features/rate-limits.mdx` and `docs/advanced/cli.mdx` —
  windows survive restart and `--reload`, including each active
  per-child partition; concurrency does not; still per instance. Drop
  the “counters start fresh” wording on the CLI reload page.
- `docs/dev/2026-07-05_rate-limiting-spec.md` §8 and §11.7 — mark
  persistence done; §11.2 stays future work.
- `CLAUDE.md` / `Agents.md` — drop rate-limit counters from the
  “in-memory state resets on reload” list; keep session affinity and
  live log buffers.
- `.env.template` and `config/config.example.yaml` — document
  `RATE_LIMITS_FLUSH_INTERVAL` / `flush_interval`.

## 14. Follow-up (not this change)

Redis-Lua `counterBackend`: every `Admit` is an atomic script on shared
keys (the key must include `partition`, same as `ruleKey`). No
snapshots. Chosen when `REDIS_URL` is set, or behind an explicit config
once someone is running HA and wants N replicas to share one limit.
The interface in §5 is the only preparation this change makes for that
work.
