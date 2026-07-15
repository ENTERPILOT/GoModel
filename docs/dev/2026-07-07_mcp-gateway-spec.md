# MCP Gateway Specification

Status: implemented (this branch)
Date: 2026-07-07

## 1. Why

Agent clients (Claude Code, Claude Desktop, Cursor, VS Code, OpenAI Codex)
consume tools over the Model Context Protocol. Teams running many MCP servers
hit the same wall they hit with model providers before gateways existed:
credentials scattered across every client config, no access control, no audit
trail, no usage attribution, and each client limited to a handful of
configured servers. Every comparable gateway (LiteLLM, Bifrost, Kong,
Portkey) now ships an MCP gateway; GoModel has none.

### What users of other gateways need and use

Researched July 2026 across LiteLLM, Bifrost, Kong AI Gateway, Portkey,
Docker MCP Gateway, MetaMCP, MCPJungle, TBXark/mcp-proxy, and hosted hubs
(docs, source, GitHub issues, HN/Reddit threads).

Table stakes (all serious gateways):

- **Aggregation**: N upstream MCP servers exposed through one authenticated
  endpoint, tools namespaced per server (LiteLLM `{server}-{tool}`, Bifrost
  `{client}-{tool}`).
- **Credential boundary**: gateway holds upstream credentials; clients only
  hold a gateway key. The MCP spec *forbids* token passthrough — the gateway
  must terminate the client credential and inject per-server credentials.
- **Tool filtering**: per-server allow/deny lists (LiteLLM `allowed_tools`,
  Bifrost `tools_to_execute`, Docker `--tools`).
- **Transports**: streamable HTTP first-class; SSE legacy; stdio for the long
  tail of local-only servers.

Loudest unmet needs / complaints:

1. **Context bloat** — the #1 ecosystem pain. Naive aggregators concatenate
   tool lists; clients degrade past ~40-50 tools (66k tokens of tool
   definitions measured in the wild). Mitigation: filtered per-key views and
   per-request server scoping.
2. **Per-key filtered tool views** — MetaMCP's most-upvoted open issues;
   enterprise gateways charge for it. Kong filters `tools/list` per consumer
   so clients never see tools they cannot call.
3. **Session brokering bugs** — LiteLLM opened a new upstream session per
   operation (broke every stateful server, #20242) and its stateless mode
   cancelled concurrent calls (#24522).
4. **Empty-but-connected upstreams** — Bifrost registers a server whose
   `tools/list` failed as connected with zero tools, unrecoverable without
   manual toggling (#4314).
5. **DB-as-source-of-truth drift** — LiteLLM's Prisma-backed MCP registry
   breaks across upgrades (#24013, #24433); users ask for declarative config.
6. **stdio-via-admin-API is an RCE class** — LiteLLM CVE-2026-42271
   (exploited in the wild, CISA KEV): admin-registered subprocess = code
   execution on the gateway host.
7. **Tool-name collisions and length limits** — Bifrost's prefixing exceeds
   Bedrock's 64-char tool-name cap (#3788); LiteLLM double-prefixes (#23348).
8. **Lost fidelity** — Bifrost lossily rewrites tool JSON Schemas (#2389) and
   non-deterministic tool ordering breaks provider prompt caching (#3362);
   LiteLLM dropped upstream `instructions` for a year (#13119).
9. **No observability** — MCPJungle's top requests are interaction logs;
   enterprises call the gateway "the single chokepoint where security policy,
   access control, and observability can be enforced".

Differentiators worth building:

- Per-key/user-path filtered `tools/list` (Kong-style least-privilege
  discovery) mapped onto GoModel's existing `user_paths` idiom.
- Config-as-code seeds + read-only-in-dashboard overlay (GoModel's existing
  virtual-models/tagging pattern) — directly answers LiteLLM's drift issues.
- Usage/audit/labels on every tool call through the existing pipeline.
- Honest health: a failed `tools/list` marks the server degraded and keeps
  re-probing (GoModel's stale-inventory carry-forward, applied to MCP).

## 2. Requirements

In scope (v1):

- **Aggregated MCP endpoint** `/mcp` (streamable HTTP, spec 2025-11-25 via
  SDK; sessions handled by the SDK) exposing tools, prompts, and resources of
  all upstream servers visible to the caller, namespaced `{server}_{tool}`.
- **Per-server endpoints** `/mcp/{server}` exposing one upstream with
  original (un-prefixed) tool names.
- **Upstream transports**: streamable HTTP (`http`, default), legacy SSE
  (`sse`), stdio (`stdio` — declarative config only, never admin API).
- Gateway auth: existing bearer auth (master key / managed keys) on all MCP
  routes; per-request user_path and labels flow as everywhere else.
- **Access control**: per-server `user_paths` visibility (subtree match, same
  semantics as virtual models), per-server `allowed_tools`/`disallowed_tools`
  operator filters, and per-request scoping via the `X-MCP-Servers` header.
  `tools/list` is filtered — clients never discover tools they cannot call.
- **Sources**: `mcp.servers` in `config.yaml` + `MCP_SERVERS` env (JSON,
  merged per name, env wins) as infrastructure-as-code, plus an
  `mcp_servers` store managed by admin API/dashboard. Config-sourced entries
  override store rows with the same name and are read-only in the dashboard
  (tagging pattern).
- **Observability**: one usage entry per `tools/call` (server, tool,
  duration, sizes, error flag; user_path + labels), audit-log capture on MCP
  routes, request rate limiting and budget gating per user_path.
- **Health**: lazy connect, background re-probe of failed upstreams, catalog
  refresh on upstream `list_changed` notifications, admin
  status + reconnect verbs. A server whose listing failed is `degraded`, not
  silently empty.
- Feature gate `MCP_ENABLED` (default `true`; zero cost when no servers are
  configured).

Out of scope (v1) — documented future work, see §10:

- Bridging server→client requests: sampling, elicitation, roots (deprecated
  in the 2026-07-28 spec RC; legally omitted by not negotiating the
  capabilities).
- Resource subscriptions, completion, logging passthrough, tasks.
- Upstream OAuth flows (static headers with `${ENV}` refs only in v1).
- Inline MCP tool injection into `/v1/chat/completions`, and provider-native
  `mcp` tool shapes on `/v1/responses` / `/v1/messages`.
- Per-tool pricing / cost attribution.
- Legacy SSE **downstream** transport (SSE upstream is supported).

## 3. Design

### Architecture

Aggregating gateway (not a blind relay) built on the official
`github.com/modelcontextprotocol/go-sdk` (v1.6.1): the SDK terminates the
protocol on both legs — `mcp.Server` + `StreamableHTTPHandler` downstream,
`mcp.Client` with `StreamableClientTransport`/`SSEClientTransport`/
`CommandTransport` upstream. Chosen over mark3labs/mcp-go (pre-1.0, no
2026-07-28 track) and over a hand-rolled relay (the spec is churning; the
SDK already implements the upcoming sessionless revision in v1.7 pre).
Docker MCP Gateway and Envoy AI Gateway made the same choice.

`internal/mcpgateway` owns everything:

- `upstreams.go` — one persistent `ClientSession` per enabled server, lazy
  dial, health state (connected/degraded/disabled), background re-probe,
  catalog snapshot per server (tools/prompts/resources with raw schemas
  preserved as `json.RawMessage`), refresh on `*ListChangedHandler`.
- `service.go` — per-session `*mcp.Server` construction: visibility filter
  (enabled ∩ user_paths ∩ X-MCP-Servers header ∩ per-server tool filters),
  namespaced registration with forwarding handlers, composed `Instructions`.
- `store.go` + `store_sqlite/postgresql/mongodb.go` + `factory.go` — the
  standard GoModel store trio for admin-managed servers.
- `usage.go` — usage-entry emission per tool call.

Upstream session topology: **shared** — one session per upstream serves all
downstream sessions. Correct because v1 does not bridge per-user credentials
or server→client requests; it avoids LiteLLM's session-per-operation bug and
the connection-multiplication of per-client fan-out.

### Naming

Server names: `[a-z0-9][a-z0-9_-]*`, max 64. Aggregated tools are
`{server}_{tool}`; `tools/call` resolves by longest registered-server prefix
match, falling back to a unique bare name (Postel). Per-server endpoints use
original names. Ordering is deterministic (sorted) to keep provider prompt
caches stable.

### Downstream sessions

SDK-managed (`Mcp-Session-Id`, in-process, idle timeout 30m). Each session
gets a tool snapshot at initialize; catalog changes reach new sessions
(existing sessions keep a stable view — documented). Session state is an
optimization, not a correctness dependency: the 2026-07-28 spec revision
removes sessions, and the SDK's stateless mode is the migration path.

### Failure semantics

- Upstream dial/list failure ⇒ server `degraded`, previous catalog kept
  (stale carry-forward), re-probe every 60s.
- `tools/call` to an unreachable upstream ⇒ JSON-RPC error (infrastructure
  failure), never a fabricated tool result. Upstream `isError` tool results
  relay verbatim.
- Unknown server in `X-MCP-Servers` ⇒ ignored (filter semantics).

### Security

- No token passthrough: client bearer terminates at the gateway; upstream
  headers come only from server config (values support `${ENV}`).
- stdio servers: declarative config only. The admin API and dashboard
  reject `stdio` entries (LiteLLM CVE-2026-42271 class). Documented.
- Secret header values are redacted in admin API reads and dashboard.
- MCP routes are authenticated on every request (sessions are never an auth
  substitute).

## 4. Config surface

```yaml
mcp:
  enabled: true                 # MCP_ENABLED
  servers:                      # map keyed by server name; MCP_SERVERS env (JSON object) merges over YAML per name
    github:
      url: https://api.githubcopilot.com/mcp
      transport: http           # http (default) | sse | stdio
      headers:
        Authorization: "Bearer ${GITHUB_PAT}"
      description: GitHub tools
      enabled: true             # default true
      allowed_tools: []         # allowlist; empty = all
      disallowed_tools: []      # blocklist (applied after allowlist)
      user_paths: []            # visibility restriction; empty = everyone
      tool_timeout: 30s         # per tools/call
    local-files:
      transport: stdio          # config/env only — rejected via admin API
      command: npx
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/data"]
      env:
        HOME: "${HOME}"
```

Startup validation mirrors virtual models: invalid names, missing url/command
for the transport, or credential-looking labels abort startup with a clear
error. `MCP_ENABLED=true` with zero servers is a no-op.

## 5. Admin API

- `GET /admin/mcp-servers` — list with runtime status (`connected` /
  `degraded` / `disabled` / `connecting`), tool count, last error, source
  (config vs admin), redacted headers.
- `PUT /admin/mcp-servers` — upsert (http/sse only; config-sourced names are
  read-only).
- `DELETE /admin/mcp-servers/{name}`
- `POST /admin/mcp-servers/{name}/reconnect` — force redial + relist.

Dashboard: "MCP Servers" page (list + status + CRUD form), config-sourced
rows shown read-only, mirroring virtual models.

## 6. Observability

- Usage entry per `tools/call`: `Provider="mcp"`, `ProviderName=server`,
  `Model=namespaced tool`, `Endpoint=/mcp[...]`, duration/sizes/error in
  `RawData`, user_path + labels as everywhere.
- Audit middleware captures MCP HTTP exchanges (paths classified as model
  interactions in `core/endpoints.go`).
- Rate limits: user_path-scoped rules gate every MCP POST carrying a JSON-RPC
  request (429 + Retry-After). Budgets: same gate as realtime sessions.

## 7. Protocol coverage

| Method | Handling |
|---|---|
| initialize / initialized / ping | terminated locally by SDK; instructions composed from visible upstreams |
| tools/list | aggregated, filtered, namespaced, deterministic order |
| tools/call | routed by prefix; raw args relayed; result relayed verbatim |
| prompts/list, prompts/get | aggregated / routed (namespaced like tools) |
| resources/list, resources/templates/list, resources/read | aggregated; read routed by exact URI, then template match |
| upstream `list_changed` notifications (tools, prompts, resources) | catalog resync; new downstream sessions see updates |
| notifications/cancelled, progress | request-scoped, handled by SDK per leg |
| sampling, elicitation, roots, subscriptions, completion, logging, tasks | not negotiated in v1 |

## 8. Testing

- Unit: config parsing/merge/validation, namespacing + resolution
  (collisions, bare-name fallback), visibility filtering (user_paths, header
  scoping, allow/deny), store trio, admin handler, usage emission.
- E2E (`tests/e2e/mcp_test.go`): real in-process upstream MCP servers built
  with the same SDK; a real MCP client connects through the gateway and
  drives initialize → tools/list → tools/call (success, filtered-out tool,
  degraded upstream, auth failure, per-server endpoint).

## 9. Rejected alternatives

- **Blind JSON-RPC relay**: perfect fidelity but no merged catalog, no
  per-tool policy, and still forced to manage sessions/credentials; the
  ecosystem norm is aggregation.
- **mark3labs/mcp-go**: friendlier per-session ergonomics but pre-1.0 with
  breaking minors and no visible next-spec track.
- **Per-downstream upstream fan-out**: only needed for per-user upstream
  credentials / sampling bridging; deferred with them.
- **stdio via admin API**: RCE-in-the-wild class; declarative-only is a
  security decision, not a gap.

## 10. Future work

- Per-user upstream auth (client-scoped header forwarding, OAuth
  client-credentials with cached tokens, then full PKCE relay).
- Serving MCP `sampling/createMessage` with GoModel's own model routing (an
  AI gateway is uniquely positioned; LiteLLM's acknowledged gap).
- Inline tool injection into chat completions + `POST /v1/mcp/tools/execute`
  (Bifrost's suggest-then-execute), provider-native `mcp` tool shapes.
- Live per-session tool-list diffs (push `list_changed` into open sessions).
- Tool-description pinning / change detection (rug-pull defense), payload
  policy hooks, semantic/progressive tool disclosure.
- Encryption at rest for admin-managed upstream headers (needs a
  key-management story first; until then config/env declarations keep
  secrets out of the store entirely, and the docs say so).
- Per-tool pricing; resource subscriptions; stateless downstream mode when
  the 2026-07-28 spec lands in a stable SDK.
