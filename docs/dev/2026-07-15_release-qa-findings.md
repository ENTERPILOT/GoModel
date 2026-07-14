# Release QA findings — 2026-07-15 (post-v0.1.51, HEAD 5cc918e3)

Scope: everything merged since v0.1.51 — MCP gateway (#502), MCP audit body
logging (#534), provider request health (#521), module rename (#522), dep
bumps — plus the full standard release matrix.

## Results

- **Standard matrix S01–S162: 162/162 pass, zero regressions.**
  S151/S152 (Ollama) and S153/S154 (Fireworks) self-skipped: no local Ollama
  server; Fireworks account still suspended.
- **New scenarios S163–S172 (this QA round): 10/10 pass**, two of them pin
  known defects (below) rather than the documented behavior.

## New coverage added

- `tests/e2e/mockmcp`: standalone mock MCP upstream built on the same
  `modelcontextprotocol/go-sdk` the gateway uses. Serves `alpha`
  (tools `echo`/`add`, prompt `greeting`, resource `mock://alpha/info`,
  instructions marker; `X-Mock-Token`-gated when `MOCK_MCP_TOKEN` is set) and
  `beta` (tools `search`/`fetch`). `manage-release-e2e-stack.sh` builds and
  runs it on port 18090 and refuses to start when a foreign process holds the
  port (a stale instance answering `/healthz` once masked a failed bind).
- Scenarios S163–S172 in `release-e2e-scenarios.md` (matrix now 172): admin
  MCP CRUD with secret redaction and the catalog inspector; aggregated
  handshake with merged instructions and deterministic namespaced
  `tools/list`; `tools/call` with usage-entry assertions; per-server endpoint
  and `X-MCP-Servers` narrowing; audit JSON-RPC body capture + nosniff;
  negatives (unknown server 404, unknown tool error, session binding, stdio
  upsert rejected); `***` secret round-trip proved against the token-gated
  upstream; provider `request_health`; `mcp_servers` store parity on
  PostgreSQL and MongoDB; namespaced prompts + resources relay.

## Findings

### F1 — Spec gap: unique bare-name `tools/call` is not implemented

> **FIXED (2026-07-15, same-day follow-up):** the aggregated session now
> installs a receiving middleware that rewrites a unique bare `tools/call`
> name to its namespaced registration (`bareToolAliases` +
> `bareToolCallMiddleware` in `internal/mcpgateway/service.go`). Ambiguous
> bare names and collisions with literal namespaced names stay errors; the
> dead `ResolveNamespacedName` was removed. S165 now asserts the spec'd
> behavior.

The MCP spec (`docs/dev/2026-07-07_mcp-gateway-spec.md`, "longest prefix
match, falling back to a unique bare name") and CLAUDE.md both promise that
`tools/call` on `/mcp` accepts a unique bare tool name. The gateway only
registers namespaced names on the aggregated session, so a unique bare name
returns JSON-RPC `-32602 unknown tool`. `mcpgateway.ResolveNamespacedName`
is exported and unit-tested but never called from production code — a
vestige of the spec'd resolution design. Pinned in S165 (`KNOWN GAP`).

Fix options: register bare aliases when unique, or resolve in a
`tools/call` interceptor via `ResolveNamespacedName` + uniqueness check.

### F2 — Bug (security-relevant): header-based user paths never reach the MCP layer

> **FIXED (2026-07-15, same-day follow-up):** `RequestSnapshotCapture` now
> normalizes the user-path header and stamps it into the request context for
> model-interaction endpoints that are not `IngressManaged` (`/mcp`, realtime,
> audio), so session binding, `user_paths` scoping, rate limits, budgets, and
> usage attribution all see header identity. A managed key's bound path still
> wins (auth middleware runs later and shadows the stamped value). S168 now
> asserts stolen-session 404, rate-limit 429 on `/mcp` posts, and subtree
> `user_paths` visibility for header principals.

`/mcp` is deliberately not `IngressManaged`
(`internal/core/endpoints.go:147`), so `RequestSnapshotCapture` returns
before stamping `X-GoModel-User-Path` into the request snapshot. As a result
`core.UserPathFromContext` is `""` for every MCP request whose identity
comes from the header (unsafe mode, or master-key mode using the header to
separate consumers). Managed-key callers are unaffected
(`applyAuthKeyResult` sets the effective user path on the context).

Verified consequences on the running stack (all header-identity):

1. **Session-to-principal binding is a no-op**: a different principal (other
   header path, or no header) presenting a leaked `Mcp-Session-Id` gets 200
   and the owner's tool snapshot; docs promise 404. Managed-key intruders
   are correctly rejected (404), and cross-pinned-endpoint reuse is
   correctly rejected (404). Pinned in S168 (`KNOWN BUG`).
2. **`user_paths` server scoping fails closed**: a server scoped to
   `/team/secret` is invisible to the legitimate `/team/secret/dev` header
   caller (and everyone else) — the feature is unusable without managed
   keys.
3. **User-path rate limits and budgets do not gate MCP posts**: a
   1 req/min rule on a probe path 429s the second chat call but three `/mcp`
   POSTs under the same header all pass (admission resolves the path to
   `/`). `enforceBudget` uses the same source. Contradicts CLAUDE.md
   ("Every MCP POST is gated by user-path rate limits and budgets").
4. **Observability disagrees with enforcement**: MCP usage entries record
   the header path (read raw from the header in `recordToolCall`) and audit
   entries record it via middleware, so logs look correctly attributed while
   enforcement never saw the identity.

Why tests miss it: `internal/mcpgateway` unit tests wrap the service in a
test middleware that stamps `WithEffectiveUserPath` from the header
themselves. Suggested fix: stamp the normalized user-path header into the
context (or snapshot) for `OperationMCP` requests, or have the MCP service
fall back to the raw header the way `recordToolCall` already does.

### F3 — Verified-good list (new features)

- Admin MCP server CRUD; header secrets redacted as `***` in every admin
  read; `***` on PUT preserves the stored secret (proved end-to-end against
  the token-gated upstream: reconnect still succeeds, and a wrong token
  degrades the server with the upstream error surfaced in `last_error`).
- Aggregation: upstream `instructions` merged into `initialize`,
  deterministic sorted namespaced `tools/list`, `X-MCP-Servers` narrowing,
  per-server endpoints expose original names, prompts namespaced, resources
  and `resources/read` relay verbatim.
- #534: MCP POST audit entries carry `provider="mcp"`, the JSON-RPC method
  or tool name as `requested_model`, the request frame in
  `data.request_body`, and the SSE-decoded response frame in
  `data.response_body`; `X-Content-Type-Options: nosniff` set on all MCP
  responses.
- Usage entries per `tools/call`: `provider="mcp"`, `provider_name`=server
  slug, `model`=namespaced tool, `request_id`/`user_path` from headers.
- `mcp_servers` store round-trips on PostgreSQL and MongoDB; tool calls work
  through both gateways.
- Runtime stdio registration rejected with 400 (RCE guard); unknown pinned
  server 404; garbage session id → SDK 404 "session not found".
- #521 `request_health`: 600s window, per-model counts, `circuit_state`,
  `last_error_model`; the ≥3-errors-AND-≥50%-rate flag gate verified (4
  errors over 41 requests correctly leaves the provider Healthy);
  `status_reason` present on every provider.
- #522: binary builds and runs as `github.com/enterpilot/gomodel`.

## Notes / minor

- Sessionless MCP POSTs (no `Mcp-Session-Id`) are served statelessly by the
  SDK (a `tools/list` without initialize works). Part of the streamable
  spec; worth a deliberate decision on whether admission/binding semantics
  should differ there.
- `ResolveNamespacedName` was dead production code — removed with the F1 fix
  (the bare-name fallback uses a per-session alias map instead).
