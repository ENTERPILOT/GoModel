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
- Command: `rg -n "CacheTypeBoth"`

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
  - `rg -n "\\.Middleware\\(\\)" | sort`
  - `rg -n "HandleRequest\\(" | sort`

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

## 6. Centralize fallback-mode semantics in `config`

Effort: low
Risk: low

Why:
- `config.ResolveFallbackDefaultMode()` now owns the blank-to-`auto` defaulting rule.
- `internal/app/app.go` still re-implements fallback-mode parsing in:
  - `dashboardFallbackModeValue()`
  - `fallbackFeatureEnabledGlobally()`
  - `fallbackModeEnabled()`
- Those helpers currently perform their own `TrimSpace` / case-folding instead of reusing config-owned semantics.

Suggested action:
- Add small config-owned helpers for:
  - "is fallback enabled for this mode?"
  - "what dashboard mode should be exposed for this config?"
- Remove the ad hoc mode parsing from `internal/app/app.go`.
- This keeps blank, mixed-case, and future mode handling in one place.

## 7. Collapse the duplicated translated fallback attempt loops

Effort: medium
Risk: medium

Why:
- `internal/server/translated_inference_service.go` has two near-identical fallback loops:
  - `tryFallbackResponse()`
  - `tryFallbackStream()`
- Both:
  - fetch selectors
  - gate on `shouldAttemptFallback()`
  - derive `providerType`
  - log attempt/success messages
  - walk candidates while preserving the last error

Suggested action:
- Extract one shared iterator that owns:
  - selector traversal
  - provider-type lookup
  - attempt logging
  - last-error handling
- Keep the typed wrappers only for the response/stream result shapes.

## 8. Precompute fallback source identity once per resolution

Effort: medium
Risk: low to medium

Why:
- `internal/fallback/resolver.go` recomputes trimmed selector identity several times per request:
  - `sourceModelInfo()`
  - `modeFor()`
  - `manualSelectorsFor()`
  - `matchKeys()`
  - `sourceKey()`
- `modeFor()` and `manualSelectorsFor()` each rebuild the same ordered match-key list.

Suggested action:
- Introduce a small internal struct for one fallback resolution pass, containing:
  - source model info
  - canonical source key
  - ordered match keys
- Build it once in `ResolveFallbacks()` and pass it through helper calls.
- This would trim repeated string cleanup and make precedence rules easier to inspect.

## 9. Extract manual fallback-rule file parsing from `loadFallbackConfig`

Effort: medium
Risk: low to medium

Why:
- `config.loadFallbackConfig()` currently owns both:
  - fallback-mode validation/defaulting
  - the custom JSON loader for `manual_rules_path`
- The manual loader includes:
  - duplicate raw JSON key detection
  - `null` rejection
  - trailing-content validation
  - whitespace normalization
- That makes the config loader harder to scan than the rest of the config pipeline.

Suggested action:
- Move the manual-rule JSON parsing into a dedicated helper or file, for example `loadFallbackManualRules(path string)`.
- Keep `loadFallbackConfig()` focused on policy validation and wiring.
- Preserve the current strict error messages and test coverage while isolating the parser.

## 10. Pick one owner for execution-plan fallback defaults

Effort: medium
Risk: medium

Why:
- The managed backend default is set in `internal/app/app.go:defaultExecutionPlanInput()`.
- The dashboard draft default is separately hardcoded in `internal/admin/dashboard/static/js/modules/execution-plans.js:defaultExecutionPlanForm()`.
- We already changed both once to keep them aligned, which confirms the drift risk is real.

Suggested action:
- Prefer a single server-owned default surface for execution-plan authoring defaults.
- Options:
  - expose default feature flags from the admin config endpoint
  - derive the initial dashboard form from the active managed default workflow
- This reduces UI/backend drift for fallback and other execution-plan features.

## Recommended order

1. Remove `CacheTypeBoth`.
2. Deduplicate the dashboard empty-state object.
3. Keep cached-only policy in one layer.
4. Remove the legacy middleware path.
5. Centralize cache semantics in a shared package.
6. Centralize fallback-mode semantics in `config`.
7. Collapse the duplicated translated fallback attempt loops.
8. Precompute fallback source identity once per resolution.
9. Extract manual fallback-rule file parsing from `loadFallbackConfig`.
10. Pick one owner for execution-plan fallback defaults.
