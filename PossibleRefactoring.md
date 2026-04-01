# Possible Refactoring

Ordered by lowest effort and lowest risk first.

## 1. Remove dead `CacheTypeBoth`

Effort: very low
Risk: very low

Why:
- Defined in `internal/responsecache/semantic.go`.
- No call sites found in the repo.

How verified:
- Symbol searched: `CacheTypeBoth`
- Command: `rg -n "CacheTypeBoth" internal`

Suggested action:
- Delete the constant and let tests confirm nothing depended on it.

## 2. Deduplicate the dashboard's empty `cacheOverview` object

Effort: low
Risk: very low

Why:
- The same shape is repeated in:
  - `internal/admin/dashboard/static/js/dashboard.js`
  - `internal/admin/dashboard/static/js/modules/usage.js`
  - `internal/admin/dashboard/static/js/modules/execution-plans.js`

Suggested action:
- Keep a single `emptyCacheOverview()` factory and reuse it everywhere.

## 3. Pick one owner for "cache overview is cached-only"

Effort: low
Risk: low

Why:
- The handler sets `CacheModeCached` in `internal/admin/handler.go`.
- Each reader sets it again in:
  - `internal/usage/reader_sqlite.go`
  - `internal/usage/reader_postgresql.go`
  - `internal/usage/reader_mongodb.go`
- `GetCacheOverview()` already implies cached-only behavior.

Suggested action:
- Keep the override in one place only.
- Prefer reader ownership so the behavior stays correct regardless of caller.

## 4. Remove the legacy `ResponseCacheMiddleware.Middleware()` path

Effort: medium
Risk: medium

Why:
- Production flow now uses `HandleRequest()` from `internal/server/translated_inference_service.go`.
- `.Middleware()` in `internal/responsecache/responsecache.go` is only referenced by tests.

How verified:
- Symbols searched: `Middleware()` and `HandleRequest(`
- Commands:
  - `rg -n "\\.Middleware\\(\\)" internal | sort`
  - `rg -n "HandleRequest\\(" internal | sort`

Suggested action:
- Before deleting the compatibility wrapper, keep equivalent cache-hit and cache-miss coverage around `HandleRequest()`.
- Existing tests in `internal/responsecache/handle_request_test.go` already cover core hit/miss flows and should be expanded first if wrapper-specific assertions are still needed.
- Delete the compatibility wrapper.
- Only remove `internal/responsecache/middleware_test.go` after `HandleRequest()`-level coverage fully preserves the hit/miss, response header/status, and cache population assertions currently carried by the middleware wrapper tests.

## 5. Centralize cache-type vocabulary across packages

Effort: medium to high
Risk: medium

Why:
- Overlapping cache constants and normalization logic exist in:
  - `internal/usage/cache_type.go`
  - `internal/auditlog/auditlog.go`
  - `internal/responsecache/semantic.go`
- This increases the chance of drift when new cache types or modes are added.

Suggested action:
- Introduce a small shared internal package for cache semantics.
- Do it only if it can be done without creating import cycles.

## Recommended order

1. Remove `CacheTypeBoth`.
2. Deduplicate the dashboard empty-state object.
3. Keep cached-only policy in one layer.
4. Remove the legacy middleware path.
5. Centralize cache semantics in a shared package.
