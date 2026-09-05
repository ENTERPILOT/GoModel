# ADR-0012: Plugin System

## Context

GoModel had two interception mechanisms and neither was a plugin system:

- `ext.RequestRewriter` ran post-auth on raw JSON bytes and could rewrite or
  reject a request, but could not see the resolved provider or touch the
  response.
- `guardrails.Guardrail` ran post-routing on a text-only message list and
  could edit text or error out, but could not reject with a status, insert
  non-system messages, see anything but text, or touch the response.

Both were compile-in only. Guardrail types were a hard-coded `switch` plus a
hand-written dashboard form, and adding one touched four places in core. The
guardrail pipeline ran same-step rules in parallel and silently kept the last
result. Nothing covered responses or streams.

The roadmap asked for plugins ("at least for guardrails") and for
response-side guardrails applied before output reaches the client. The
design proposal in `docs/dev/2026-09-03_plugins-and-guardrails-spec.md`
compared Bifrost, LiteLLM, Portkey, Kong, and Envoy and converged on one
contract. This ADR records what was built.

## Decision

### 1. One contract package, in the main module, standard library only

The public contract lives in `github.com/enterpilot/gomodel/pluginapi`, a
package of the main module rather than a separate Go module. It imports the
standard library only; a test enforces this.

The constraint is not stylistic. Go's `plugin` package refuses to load a
shared object unless every package present in both host and plugin was
built from identical sources with the same toolchain and flags. A
stdlib-only contract means a `.so` shares exactly the standard library plus
`pluginapi` with the host: internal GoModel changes, new providers, and
dashboard work never invalidate a plugin. Only a `pluginapi` change or a Go
toolchain change does.

`pluginapi.Version` is informational. It is printed by `gomodel --version`,
stamped into plugins by `gomodel plugin build`, and used to explain a
refused load; it is never used to accept or reject one, because the
toolchain already enforces identical sources.

`internal/core` types never appear in the contract. Plugins see the unified
view of section 3; `internal/plugins/exchange` owns the mapping in both
directions.

### 2. A single package, not one package per hook

An earlier draft split each hook kind into its own package so that adding a
hook would not change the package a `.so` shares with the host. That was
dropped:

- a new hook almost always needs a new field or enum value on `Exchange`
  anyway, so the shared set changes regardless;
- Go patch releases force a `.so` rebuild no matter what, so the design
  optimizes source compatibility and makes the rebuild cheap instead of rare;
- the split cost authors extra imports and package names (`prompt`,
  `stream`, `response`) that shadow the most natural local variables in
  exactly this code.

Changes to `pluginapi` are additive: new fields are appended, new hook
interfaces are added beside existing ones, and new `Action`, `PartKind`,
`EventKind`, and `Kind` values may appear. Plugins treat unknown values as
opaque.

### 3. The `Exchange` model and apply-back by message ID

Every hook receives one `*pluginapi.Exchange` per request, the same object in
every phase:

- `Meta`: a read-only snapshot of request context (identity, dialect,
  session, routing resolution, workflow version and features, provider
  attempts in response phases, prompt-cache planning).
- `Prompt`: the conversation as `Messages` with host-assigned stable IDs and
  typed `Parts` (text, image, audio, file, tool call, tool result, reasoning,
  refusal, opaque), plus `Tools`, `Params`, and the raw body.
- `Response`: a `Completion` with `Choices` using the same `Part` model.
- `Stream`: text accumulated so far per choice.
- `Headers`: inbound (credentials redacted), outbound, and upstream headers.
- `Values`: a per-request bag shared by every hook of the request.

Edits go through methods (`SetText`, `SetToolArguments`, `SetToolResult`,
`Insert`, `Append`, `Remove`, `SetParam`, `ReplaceText`, `SetFinishReason`)
that record a `Changes` map keyed by message ID (or `choice:<n>`). Apply-back
copies untouched messages from the original typed request verbatim, so
`ExtraFields`, `cache_control`, and multi-part structure survive; rewrites a
touched text part in place; encodes inserted messages from the unified form;
and drops removed ones. The previous restriction that only system messages
could be inserted or removed is lifted, because message IDs remove the
alignment ambiguity that motivated it. A removal that leaves a tool call
without its result (or the reverse) is reported as `DanglingToolError`, and
the host rejects a prompt that still has dangling pairs.

Anthropic `/v1/messages` requests are translated to the chat form before any
hook runs, so the mapping covers two request types (`ChatRequest`,
`ResponsesRequest`) and two response types. `Meta.Dialect` exposes the
original dialect. Any configured prompt, response, or stream plugin forces
the translated path for `/v1/messages`.

### 4. Phases and chain execution

Hook kinds are `request`, `prompt`, `response`, `stream`, `route`, and
`complete`. The workflow phases run by this release are `prompt`, `response`,
and `stream`:

- `prompt` runs where the translated request patcher ran, after routing and
  before the provider call, plus the batch preparer.
- `response` and `stream` wrap the provider result inside the translated
  inference service, before the Anthropic stream converter, so plugins always
  see the canonical OpenAI form.

`workflows.Compile` produces one compiled chain per phase. A chain groups
instances by `step`:

- steps run in ascending order;
- within a step, instances whose manifest declares `Mutates: false` run
  concurrently on a shallow copy of the exchange (their `Values` and response
  headers are merged back), then the single mutating instance runs on the
  exchange itself;
- a step with more than one mutating instance is a validation error;
- decisions merge by severity, `block` > `respond` > `warn` > `allow`, with
  step order breaking ties; the first blocking decision ends the chain after
  its step.

This replaces the silent last-writer-wins of the old pipeline.

Each chain has a hash computed from its members (name, type, step, fail mode,
config digest). The prompt-chain hash feeds the semantic response cache key,
so a rule change invalidates answers cached under different rules. Workflow
views expose all three as `chain_hashes`.

### 5. Decisions and fail modes

Every synchronous hook returns a `Decision` with an `Action`:

- `allow`: continue with the edits made;
- `block`: reject with `Status`, `Code`, `Message` rendered in the endpoint's
  native error dialect; `Status` defaults to 400 in the prompt phase and 502
  in the response and stream phases; `Code` defaults to `guardrail_blocked`;
- `respond`: short-circuit with a synthetic `Completion` as an ordinary
  assistant turn, HTTP 200, rendered as a one-turn stream for streaming
  requests;
- `warn`: continue, record `Detail` in the audit trail, and add
  `X-GoModel-Guardrail: warn; code=<code>` to the client response.

The non-standard 446/246 statuses some products use are not defaults; an
operator who wants parity sets `block_status` on the instance.

Each instance has `fail_mode` (`closed` | `open`) and `timeout_ms`. The
default is `closed` for `prompt`, `response`, and `stream` (a guardrail that
crashes must not let content through) and `open` for the other kinds. A
closed failure produces HTTP 500 with code `plugin_failure`; the instance
name goes to the logs and audit record, never to the client message. Panics
are recovered. `Init` has a fixed 10 s timeout.

Timeouts are enforced at the deadline: `plugins.Call` runs the hook in a
goroutine and returns when the hook returns or the context ends, whichever
is first, so a hook that ignores its context bounds neither latency nor
shutdown. An abandoned hook may still be running, so it fails open only when
it ran on its own copy of the exchange (a non-mutating hook in a step; the
copy is dropped instead of merged). A mutating hook or an in-flight stream
hook that is abandoned always fails the request. `Init` uses the same
mechanism against its 10 s deadline.

Instances are long-lived. A guardrail refresh reuses every instance whose
type, config, `user_path`, `fail_mode`, and `timeout_ms` are unchanged, so
plugin state survives reloads; replaced and deleted instances are retired
and closed two refresh intervals later, after workflows (which recompile on
the same interval) stopped referencing them and in-flight requests finished.
The guardrails subsystem closes every active and retired instance on
shutdown; the routing-strategy resolver is registered for shutdown as well.
A build of a stream plugin whose `StreamPolicy` is `buffer` fails unless the
plugin also implements `ResponseHook`, since buffering runs `OnResponse`.

Prompt-phase decisions are appended to the audit request-revision chain that
request rewriters already write; response and stream decisions are logged
with the request id, since the revision chain is request-only.

### 6. Streaming modes

Bytes already written cannot be recalled, so a `StreamHook` declares one of
three modes in its `StreamPolicy`, and the host does the work:

- `observe`: events are forwarded untouched; decisions are ignored except
  `terminate`. Zero added latency.
- `transform`: `streaming.TransformedSSEStream` parses each event into a
  `StreamEvent` for the canonical dialect, calls the in-flight instances in
  step order, and re-encodes only events a plugin changed (`replace` on text
  and reasoning deltas, `drop`, `terminate`). `LookbehindChars` withholds a
  tail of text per choice so a pattern spanning two chunks is visible in one
  event before any of it is sent; a non-text event or the upstream end
  flushes the tail. A pattern of up to N+1 characters is always caught at the
  cost of N characters of delay. The tail is re-presented after the plugin's
  earlier edit, with `StreamEvent.Overlap` marking it; the contract requires
  plugins to edit only matches extending past the overlap, which keeps
  non-idempotent replacements from compounding. Chat chunks with several
  choices are split per choice first so each is transformed. For Responses,
  the codec tracks the emitted text per content part and rewrites the
  `*.done` and terminal `response.*` events that restate it, so completion
  events agree with the transformed deltas.
- `buffer`: `streaming.BufferedSSEStream` drains upstream into a bounded
  buffer (default 4 MiB, exceeding it fails closed with
  `response_too_large`), sends the SSE comment `: gomodel-buffering` every
  15 s so proxies and clients do not time out, assembles a `Completion` from
  the events, runs the response chain plus the buffering instances'
  `OnResponse`, and then replays the original bytes (`allow`, `warn`),
  synthesizes a stream from the edited completion (`allow` with edits,
  `respond`), or emits a terminal error chunk (`block`).

Mixed chains: if any stream instance asks for `buffer`, or the workflow has
any `response` step, the whole stream is buffered and `transform` instances
run over the replay. When such plugins run, the handler reads the first chunk
before committing the response headers, so a `warn` decided over a buffered
response is delivered as the `X-GoModel-Guardrail` header when buffering
finished before the first keep-alive; a later warn is audit-only. A stream cut by `terminate`, or by a `block`/`respond`
from `OnStreamEnd`, ends with `finish_reason: "content_filter"` (mapped to
`stop_reason: "end_turn"` in the Anthropic dialect) and `[DONE]`; the client
keeps what it already received. That is the documented cost of `transform`,
and why a plugin that must not leak anything uses `buffer`.

### 7. Workflow payload version 2

`GuardrailStep` gains `phase`, and the array is renamed to `steps`:

```json
{
  "schema_version": 2,
  "features": { "cache": true, "audit": true, "usage": true, "budget": true, "guardrails": true, "failover": true },
  "steps": [
    { "ref": "pii-redact",  "phase": "prompt",   "step": 10 },
    { "ref": "secret-scan", "phase": "response", "step": 10 },
    { "ref": "secret-scan", "phase": "stream",   "step": 10 }
  ]
}
```

`phase` is `prompt` (default), `response`, or `stream`; the same `ref` may
appear once per phase; the instance's plugin must implement the phase.
Version 1 payloads (`guardrails: [{ref, step}]`) stay valid, are stored and
returned unchanged, and compile as prompt-phase steps, so no stored workflow
needs a migration. The dashboard always writes version 2. This amends
ADR-0003 section 7.

Workflows remain the one place that decides which instances run and in what
order. Instances are the same named, reusable objects the guardrail store
already held; `config.yaml` rules are seeded into that store and into the
managed default workflow at their `order` in their `phase`. Workflow scope
matching is unchanged and is how a plugin applies to some traffic and not
other traffic.

### 8. Loaders

A plugin type reaches the catalog in one of three ways:

- built in: `internal/plugins/builtin` registers `system_prompt`,
  `llm_based_altering`, `string_replace`, `header_edit`, `llm_judge`, and
  the `cheapest_healthy` route strategy, each importing only `pluginapi` so
  they double as reference implementations;
- compiled in: `ext.RegisterPlugin(factory)` before `run.Run`, the same
  surface Pro uses for rewriters;
- shared object: `plugins.load[]` entries opened by `internal/pluginload`.

The `.so` loader resolves a relative file inside `plugins.search_paths`
(symlinks followed and checked), verifies an optional SHA-256 pin, opens the
file, looks up `GoModelPlugin` (a `func() pluginapi.Plugin` constructor, or
a `pluginapi.Plugin` variable that limits the file to one instance) and the
optional `GoModelBuildInfo`, and checks that every declared `Kind` is backed
by the matching interface. Every failure is a startup error naming the file.

Constraints of Go's `plugin` package that the documentation states plainly:

- Linux, macOS, and FreeBSD only, and only with `CGO_ENABLED=1`. The default
  image stays static; `Dockerfile.plugins` builds `gomodel:<version>-plugins`
  with cgo on a glibc base and includes a `plugin-builder` target with the
  identical toolchain. `make build-plugins` produces the cgo binary locally.
- The plugin must be built with the same Go version, the same build flags
  (`-trimpath`, `-race`, `-tags`, `-gcflags`, `-asmflags`), and identical
  sources of every shared package. `gomodel plugin build` copies the flags
  recorded in the running binary, forces cgo, pins `GOTOOLCHAIN` to the
  host's Go version, stamps `GoModelBuildInfo`, and refuses an output built
  with a different Go version. A refused load is reported with both sides'
  Go version, module version, and flags instead of Go's generic message.
- Plugins cannot be unloaded. A changed `.so` takes effect on restart; a
  changed instance config re-runs `Init` on a fresh value.
- A `.so` is trusted code with full memory access. Loading one is equivalent
  to changing the binary; `search_paths` should be root-owned and `sha256`
  pinned in production. Sandboxing is out of scope; a subprocess or Wasm
  loader can be added later behind the same catalog.

### 9. Routing strategy plugins

A plugin implementing `pluginapi.RouteStrategy` is selected per virtual
model with `strategy: plugin`, `strategy_plugin: <plugin name>`, and
`strategy_config`, which is validated against the plugin's `Scope: route`
fields (`route_fields` in `GET /admin/plugins`). Route strategies are not
workflow steps: the choice belongs to the load-balancer definition. The
strategy list is `round_robin | cost | failover | adaptive | plugin`;
`adaptive` stays the name for the registry-level `ext.RouteSelector`.

Instance-scoped settings of a route plugin come from a guardrail definition
whose name and type both equal the plugin name (otherwise `{}`). Target
weights are ignored under the plugin strategy. `Select` runs under a 250 ms
timeout with panic recovery; a missing or non-route plugin, a failed `Init`,
an invalid `strategy_config`, a `Select` error, panic, or timeout, or an
answer outside the viable pool falls back to weighted round robin with one
warning per virtual model, so a broken strategy degrades to default routing
rather than failing requests. `cheapest_healthy` ships built in as the
reference implementation.

### 10. Deferred

Declared in the contract or the spec but not run by this release:

- `request` and `complete` hook kinds: the interfaces, `Kind` values, and
  load-time validation exist; no runtime calls them. `ext.RequestRewriter`
  remains the request-phase mechanism.
- `Headers.Upstream` is recorded and logged, not forwarded to providers.
  Prompt-phase edits to `Headers.Request` apply to the live request (later
  plugins, request logging) but are not forwarded to the provider.
- `RouteRequest.Prompt` is nil; `RouteChoice.Reason` reaches debug logs
  only. Instance-scoped `secret` fields reach a route plugin redacted.
- `Host.History` returns an error; earlier Responses turns referenced by
  `previous_response_id` or a conversation are not loaded.
- The response phase does not run on response-cache hits, because the cache
  is served by middleware before the handler; a policy tightened after an
  answer was cached applies once the prompt-chain hash changes the key.
- A `concurrent` prompt step (running a non-mutating check alongside the
  provider call) is not implemented.
- A `warn` decided after the stream headers went out (a `transform` stream's
  `OnStreamEnd`, or buffering longer than the 15 s keep-alive) is not
  delivered to the client; trailers are not used.
- Audio, realtime, and MCP tool-call hooks.

## Consequences

### Positive

- **One contract for every interception point**: prompt, response, and
  stream guardrails and routing strategies share `Exchange`, `Decision`, and
  the same configuration and dashboard surface
- **Schema-driven operations**: a plugin declares fields and the dashboard,
  config validation, secrets masking, and `GET /admin/guardrails/types`
  follow without frontend or core changes
- **Lossless edits**: apply-back by message ID keeps provider-specific fields
  and prompt-cache markers on untouched messages
- **Honest streaming**: the latency trade-off is explicit per plugin instead
  of hidden, and blocking without leakage is possible through buffering
- **Deterministic chains**: one mutator per step and severity merging replace
  last-writer-wins
- **Legible `.so` failures**: stdlib-only contract, `gomodel plugin build`,
  and build-info stamping turn the toolchain constraint into a one-line CI
  step and a readable error

### Negative

- **Rebuild churn for `.so` plugins**: every GoModel release and Go patch
  release requires rebuilding shared objects
- **A second image variant**: cgo builds double the image matrix and cannot
  be cross-compiled from the build platform
- **In-process trust**: a `.so` can do anything the binary can; there is no
  isolation
- **Buffered streams change client timing**: time-to-first-token becomes
  time-to-last-token whenever any instance buffers or a response step exists
- **Larger surface to test**: apply-back round trips for three dialects, chunk
  boundaries with lookbehind, keep-alive comments, and decision merging all
  need focused tests

## Notes

This ADR records the runtime and contract. It does not define:

- an out-of-process or Wasm loader
- audio, realtime, or MCP hooks
- a general workflow DSL (ADR-0003's fixed phase order stays)

The design rationale and vendor comparison are in
`docs/dev/2026-09-03_plugins-and-guardrails-spec.md`. User documentation is
`docs/advanced/plugins.mdx` and `docs/advanced/guardrails.mdx`.
