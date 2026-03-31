# User Path Tracking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add canonical `X-GoModel-User-Path` support for audit logs, usage tracking, admin filtering, and execution-plan matching with hierarchical fallback.

**Architecture:** Capture and normalize the header once at ingress, carry it through the request-scoped model, persist it on audit/usage records, and teach execution-plan matching to resolve path ancestry without layering multiple plans. Keep storage simple by persisting one canonical path string and deriving ancestors only at read/match time.

**Tech Stack:** Go, Echo, SQLite, PostgreSQL, MongoDB, Alpine.js dashboard, Go test

---

## Task 1: Canonical User Path Core

**Files:**
- Create: `internal/core/user_path.go`
- Test: `internal/core/user_path_test.go`
- Modify: `internal/core/request_snapshot.go`
- Modify: `internal/server/request_snapshot.go`

- [ ] **Step 1: Write failing normalization and ancestry tests**

```go
func TestNormalizeUserPath(t *testing.T) {}
func TestUserPathAncestors(t *testing.T) {}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core -run 'TestNormalizeUserPath|TestUserPathAncestors'`

- [ ] **Step 3: Write minimal implementation**

```go
func NormalizeUserPath(raw string) (string, error) {}
func UserPathAncestors(path string) []string {}
```

- [ ] **Step 4: Thread canonical user_path through request snapshot capture**

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/core -run 'TestNormalizeUserPath|TestUserPathAncestors'`

## Task 2: Execution Plan Path Scope

**Files:**
- Modify: `internal/core/execution_plan.go`
- Modify: `internal/executionplans/types.go`
- Modify: `internal/executionplans/service.go`
- Modify: `internal/executionplans/view.go`
- Modify: `internal/executionplans/compiler.go`
- Modify: `internal/admin/handler.go`
- Test: `internal/executionplans/types_test.go`
- Test: `internal/executionplans/service_test.go`
- Test: `internal/admin/handler_executionplans_test.go`

- [ ] **Step 1: Write failing scope validation and matching tests**
- [ ] **Step 2: Run focused execution-plan tests and verify RED**

Run: `go test ./internal/executionplans ./internal/admin -run 'ExecutionPlan|NormalizeScope'`

- [ ] **Step 3: Add `scope_user_path` to request/admin/storage/runtime types**
- [ ] **Step 4: Implement hierarchical fallback matching**
- [ ] **Step 5: Update admin scope validation to canonicalize path input**
- [ ] **Step 6: Run focused tests and verify GREEN**

Run: `go test ./internal/executionplans ./internal/admin -run 'ExecutionPlan|NormalizeScope'`

## Task 3: Audit Log Persistence And Filtering

**Files:**
- Modify: `internal/auditlog/auditlog.go`
- Modify: `internal/auditlog/middleware.go`
- Modify: `internal/auditlog/store_sqlite.go`
- Modify: `internal/auditlog/store_postgresql.go`
- Modify: `internal/auditlog/store_mongodb.go`
- Modify: `internal/auditlog/reader.go`
- Modify: `internal/auditlog/reader_sqlite.go`
- Modify: `internal/auditlog/reader_postgresql.go`
- Modify: `internal/auditlog/reader_mongodb.go`
- Test: `internal/admin/handler_test.go`
- Test: `tests/integration/auditlog_test.go`

- [ ] **Step 1: Write failing audit filter and persistence tests**
- [ ] **Step 2: Run focused audit tests and verify RED**

Run: `go test ./internal/auditlog ./internal/admin ./tests/integration -run 'Audit|user_path'`

- [ ] **Step 3: Add persisted/indexed `user_path` support across stores**
- [ ] **Step 4: Capture `user_path` from the request-scoped model into audit entries**
- [ ] **Step 5: Add subtree filter support to readers and admin handler**
- [ ] **Step 6: Run focused audit tests and verify GREEN**

Run: `go test ./internal/auditlog ./internal/admin ./tests/integration -run 'Audit|user_path'`

## Task 4: Usage Persistence And Filtering

**Files:**
- Modify: `internal/usage/usage.go`
- Modify: `internal/usage/extractor.go`
- Modify: `internal/usage/stream_observer.go`
- Modify: `internal/usage/store_sqlite.go`
- Modify: `internal/usage/store_postgresql.go`
- Modify: `internal/usage/store_mongodb.go`
- Modify: `internal/usage/reader.go`
- Modify: `internal/usage/reader_sqlite.go`
- Modify: `internal/usage/reader_postgresql.go`
- Modify: `internal/usage/reader_mongodb.go`
- Test: `internal/usage/usage_test.go`
- Test: `internal/admin/handler_test.go`
- Test: `tests/integration/usage_test.go`

- [ ] **Step 1: Write failing usage capture/filter tests**
- [ ] **Step 2: Run focused usage tests and verify RED**

Run: `go test ./internal/usage ./internal/admin ./tests/integration -run 'Usage|user_path'`

- [ ] **Step 3: Add `user_path` to usage entries and stores**
- [ ] **Step 4: Thread `user_path` into non-streaming and streaming usage extraction**
- [ ] **Step 5: Add subtree filters to usage readers and admin handler**
- [ ] **Step 6: Run focused usage tests and verify GREEN**

Run: `go test ./internal/usage ./internal/admin ./tests/integration -run 'Usage|user_path'`

## Task 5: Dashboard Authoring And Filters

**Files:**
- Modify: `internal/admin/dashboard/static/js/modules/execution-plans.js`
- Modify: `internal/admin/dashboard/static/js/modules/usage.js`
- Modify: `internal/admin/dashboard/static/js/modules/audit-list.js`
- Test: `internal/admin/dashboard/static/js/modules/execution-plans.test.js`

- [ ] **Step 1: Write failing UI/module tests for `scope_user_path`**
- [ ] **Step 2: Run focused dashboard tests and verify RED**

Run: `node --test internal/admin/dashboard/static/js/modules/execution-plans.test.js`

- [ ] **Step 3: Add `scope_user_path` authoring and display support**
- [ ] **Step 4: Add `user_path` filter plumbing to usage and audit list requests**
- [ ] **Step 5: Run focused dashboard tests and verify GREEN**

Run: `node --test internal/admin/dashboard/static/js/modules/execution-plans.test.js`

## Task 6: Full Verification

**Files:**
- Verify only

- [ ] **Step 1: Run focused package suites**

Run: `go test ./internal/core ./internal/executionplans ./internal/auditlog ./internal/usage ./internal/admin/...`

- [ ] **Step 2: Run integration coverage for touched behavior**

Run: `go test ./tests/integration/...`

- [ ] **Step 3: Run full project tests if focused suites are green**

Run: `go test ./...`
