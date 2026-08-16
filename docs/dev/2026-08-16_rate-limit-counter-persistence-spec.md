# Rate Limit Counter Persistence

Status: approved design
Date: 2026-08-16
Extends: `docs/dev/2026-07-05_rate-limiting-spec.md` §8, §11.2, §11.7

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

## 4. Architecture

Two seams, only the first used as a live store today.

```
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

Cardinality stays one row per rule, not per caller.

Several replicas sharing Postgres or Mongo last-write-wins on that
row. That is accepted: this work does not give shared live counters, and
the schema has no `instance_id`. A restart loads whoever flushed last.
Use one instance, or wait for the Redis-Lua backend, when the limit
must be exact across replicas.

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
`ruleKey` stays unexported. No public API change except `Service.Start`
and the new config field.

The memory implementation is the current `limiter` plus snapshot
helpers. A future Redis-Lua type implements the same interface and
ignores the snapshot store.

## 6. Snapshot data model

One row per windowed rule. Concurrent rules (`period_seconds = 0`) are
never written.

```text
scope                      TEXT     -- user_path | provider | model
subject                    TEXT
period_seconds             INT64    -- > 0
requests_window_start      INT64    -- unix seconds, 0 if unused
requests_current           INT64
requests_previous          INT64
tokens_window_start        INT64
tokens_current             INT64
tokens_previous            INT64
updated_at                 INT64    -- unix seconds, diagnostics only
PRIMARY KEY (scope, subject, period_seconds)
```

SQL table: `rate_limit_counters`. Mongo collection: `rate_limit_counters`
with a unique index on `(scope, subject, period_seconds)`. Created at
store init with `CREATE TABLE IF NOT EXISTS` / `CreateIndex`. Older
databases just gain the table; no backfill.

Units match `windowCounter`: Unix seconds and `int64` counts. After
load, `advance()` already zeros a window more than one period old, so
rows do not need a TTL.

Package type (exported only if tests in another package need it;
otherwise unexported is fine):

```go
type windowSnapshot struct {
    Scope, Subject                                      string
    PeriodSeconds                                       int64
    RequestsWindowStart, RequestsCurrent, RequestsPrevious int64
    TokensWindowStart, TokensCurrent, TokensPrevious    int64
}
```

A rule that only limits requests leaves the token fields at zero, and
the reverse. Zero `windowStart` with zero counts means “no window yet.”

## 7. Store methods

Add to the existing `Store` interface. No second factory.

- `LoadCounters(ctx) ([]windowSnapshot, error)` — all rows.
- `SaveCounters(ctx, []windowSnapshot) error` — **replaces** the
  table/collection with the provided set (delete all, then insert).
  SQL does this in one transaction. Mongo uses the same
  transaction-plus-standalone-fallback as `ReplaceConfigRules` today
  (replica-set e2e uses `rs0`; a hard transaction-only write would
  fail open on standalone Mongo). The payload is only **current
  windowed rules** (period > 0 still present in `Service.rules`),
  never leftover limiter-map entries for a rule that was dropped.
- `DeleteCounter(ctx, scope, subject, periodSeconds) error` — one row.
  Missing row is not an error.
- `DeleteAllCounters(ctx) error` — empty the table.

`Close` is unchanged.

## 8. Lifecycle

Reload builds the next generation while the current one is still
serving, then shuts the old one down, then starts the new one
(`run.serveUntilShutdown` / `watchForReload`). That order is load-bearing.

| Call | When | Persistence |
|---|---|---|
| `New` / `NewService` | `app.New`, tests | Empty limiter. No load, no flush, no loop. |
| `Start` | `App.startServer`, after the previous generation’s `Shutdown` | Load snapshot, apply rows whose key still matches a rule, then start the flush loop if `flush_interval > 0`. Mark the generation **active**. |
| `Close` (`Result.Close`) | `App.Shutdown` | Stop the loop. If **active**, write one last snapshot. Then close the store as today. |

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

1. Flush: lock `persistMu`, copy maps (only keys that are still
   windowed rules) **by value** (`windowCounter` structs, not the
   pointers `admit()` still mutates), `SaveCounters`, unlock.
2. `Reset` / `DeleteRule` / `ResetAll`: clear memory, lock `persistMu`,
   delete the row(s), unlock.

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
| `ResetRule` | `limiter.reset` | `DeleteCounter` under `persistMu` |
| `ResetAll` | `limiter.resetAll` | `DeleteAllCounters` under `persistMu` |
| `DeleteRule` | reset that key **and drop it from the limiter maps** | `DeleteCounter` under `persistMu` |
| `ReplaceConfigRules` | refresh rules; **prune limiter maps** for keys that are no longer a windowed rule | **do not touch the store** |

`ReplaceConfigRules` runs from `factory.New` → `seedConfiguredRules`
while the previous generation is still serving. Deleting snapshot rows
there would wipe a live hour/day window if the replacement is then
discarded (failed reload). Orphans are left for the **active**
generation: `Start` does not apply them; the next flush replace-all
drops them.

When the rule set changes (`ReplaceConfigRules`, `DeleteRule`), prune
the request/token maps so a later flush cannot reinsert a dropped
rule’s window. `SaveCounters` is only ever given current windowed
rules.

Reset and delete clear memory first (the operator-visible effect), then
the row under `persistMu`. A failed row delete is logged; the in-memory
reset still stands. `persistMu` is what stops the next flush from
putting the row back.

## 11. Error handling

Persistence never fails a request. `admit()` does not see store errors.

- **Load failure:** log and start with empty windows. Do not block the
  listener. Skip corrupt rows; restore the good ones. Still mark the
  generation active so later flushes work.
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
creates a `$QA_SUFFIX`-scoped user-path rule and deletes it.

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
  windows survive restart and `--reload`; concurrency does not; still
  per instance. Drop the “counters start fresh” wording on the CLI
  reload page.
- `docs/dev/2026-07-05_rate-limiting-spec.md` §8 and §11.7 — mark
  persistence done; §11.2 stays future work.
- `CLAUDE.md` / `Agents.md` — drop rate-limit counters from the
  “in-memory state resets on reload” list; keep session affinity and
  live log buffers.
- `.env.template` and `config/config.example.yaml` — document
  `RATE_LIMITS_FLUSH_INTERVAL` / `flush_interval`.

## 14. Follow-up (not this change)

Redis-Lua `counterBackend`: every `Admit` is an atomic script on shared
keys. No snapshots. Chosen when `REDIS_URL` is set, or behind an
explicit config once someone is running HA and wants N replicas to
share one limit. The interface in §5 is the only preparation this
change makes for that work.
