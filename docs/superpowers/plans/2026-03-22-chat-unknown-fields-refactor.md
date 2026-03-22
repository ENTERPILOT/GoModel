# Chat Unknown Fields Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace map-based unknown-field preservation for the chat request family with a lower-allocation raw unknown-field container while preserving round-trip behavior.

**Architecture:** Introduce a new raw unknown-field container in `internal/core/json_fields.go` that stores an object containing only unknown members and merges it back into marshaled output without `map[string]json.RawMessage`. Migrate only the chat-family request types to that container first so tests and benchmarks prove the approach before expanding to other request families.

**Tech Stack:** Go, `encoding/json`, existing `go test` suite

---

### Task 1: Add Raw Unknown-Field Container

**Files:**
- Modify: `internal/core/json_fields.go`
- Test: `internal/core/chat_json_test.go`

- [ ] **Step 1: Write the failing test**

Add assertions that chat unknown fields can still be queried and round-tripped after replacing direct `map` access with container lookup methods.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core -run 'TestChatRequestJSON_(RoundTripPreservesUnknownFields|PreservesUnknownFields|PreservesUnknownNestedFields)'`
Expected: FAIL because the new lookup/container API does not exist yet.

- [ ] **Step 3: Write minimal implementation**

Implement a raw-object container plus helpers that:
- extract only unknown fields from a JSON object into one raw JSON object
- expose lookup helpers for tests and rare consumers
- merge unknown fields back into an already-marshaled base object without rebuilding a map

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core -run 'TestChatRequestJSON_(RoundTripPreservesUnknownFields|PreservesUnknownFields|PreservesUnknownNestedFields)'`
Expected: PASS

### Task 2: Migrate Chat Family Types

**Files:**
- Modify: `internal/core/types.go`
- Modify: `internal/core/chat_json.go`
- Modify: `internal/core/message_json.go`
- Modify: `internal/core/chat_content.go`
- Test: `internal/core/chat_json_test.go`
- Test: `internal/core/types_test.go`

- [ ] **Step 1: Write the failing test**

Update chat-family tests to use the new unknown-field container API for top-level, nested message, tool-call, function-call, and content-part unknown fields.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/core -run 'Test(ChatRequestJSON|ChatRequestJSON_RoundTripPreservesUnknownFields)'`
Expected: FAIL because the structs and marshal/unmarshal paths still use `map[string]json.RawMessage`.

- [ ] **Step 3: Write minimal implementation**

Change chat-family `ExtraFields` members to the new container type and migrate marshal/unmarshal and clone helpers accordingly.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/core -run 'Test(ChatRequestJSON|ChatRequestJSON_RoundTripPreservesUnknownFields)'`
Expected: PASS

### Task 3: Focused Verification

**Files:**
- Modify: `internal/core/chat_json_test.go`
- Modify: `internal/core/types_test.go`

- [ ] **Step 1: Run focused package verification**

Run: `go test ./internal/core`
Expected: PASS

- [ ] **Step 2: Run formatting**

Run: `gofmt -w internal/core/json_fields.go internal/core/types.go internal/core/chat_json.go internal/core/message_json.go internal/core/chat_content.go internal/core/chat_json_test.go internal/core/types_test.go`
Expected: no output

- [ ] **Step 3: Re-run focused package verification**

Run: `go test ./internal/core`
Expected: PASS
