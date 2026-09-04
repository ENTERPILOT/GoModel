# Plugins and Guardrails: Architecture Proposal

Status: implemented (2026-09-04). The decisions are recorded in
`docs/adr/0012-plugin-system.md`; user documentation lives in
`docs/advanced/plugins.mdx` and `docs/advanced/guardrails.mdx`. This document
is kept as the design rationale. Where the implementation deviates from the
text below, the code and the ADR win:

- `pluginapi` is a package in the main module, not a separate module. A
  `.so` therefore pins `github.com/enterpilot/gomodel` at the host version;
  splitting it into its own module remains a tag-time option for v1.
- Instances live only in `guardrails.rules[]` and the dashboard; the
  `plugins:` section only loads `.so` files (`search_paths`, `load[]` with
  `file` and `sha256`). Per-instance timeouts are `timeout_ms`; the
  `concurrent` step flag (LiteLLM-style during-call) is not implemented.
- `request` and `complete` hooks are part of the contract but not called yet;
  `ext.RequestRewriter` stays the request-phase mechanism.
- `Headers.Upstream` is collected but not forwarded; `Host.History` is
  unavailable; `Meta.Cache.PlannedPrefixMessages` is not populated; the
  response phase does not run on response-cache hits.
- `gomodel plugin build` mirrors the host's build flags instead of forcing
  `-trimpath` (a mismatch either way is refused by `plugin.Open`), and stamps
  build info through a `GoModelBuildInfo` symbol in the plugin's main package.
- Route strategies receive `RouteRequest.Prompt == nil` in this version and
  take instance-scoped settings from a guardrail definition named after the
  plugin.

## 1. Problem

GoModel has two interception mechanisms today and neither is a plugin system:

| Mechanism | Where | Sees | Can do | Cannot do |
|---|---|---|---|---|
| `ext.RequestRewriter` (`ext/ext.go`) | post-auth, pre-routing | raw JSON bytes, redacted headers, user path, session | rewrite body, reject with status, annotate response headers | see the resolved provider or model, touch the response |
| `guardrails.Guardrail` (`internal/guardrails/guardrails.go`) | post-routing, pre-provider | a text-only `[]Message` | edit text, add or remove system messages, error out | insert or remove non-system messages, reject with a status, see anything but text, touch the response |

Both are compile-in only. Neither covers responses, streams, passthrough,
embeddings, or audio. The guardrail pipeline runs same-step rules in parallel
and silently keeps only the last result (`pipeline.go:110-125`). Guardrail
types are a hard-coded `switch` in `definitions.go` plus a hand-written
dashboard form schema, and the YAML config has a typed sibling struct per
type (`config/guardrails.go`), so a new type touches four places in core.

The roadmap lists "Plugins, at least for guardrails" and "response-side
guardrails applied before output reaches the client". This document designs
both.

## 2. Goals and non-goals

Goals:

- One plugin contract that covers request editing, guardrails, response
  guardrails, streaming guardrails, routing strategies, and later audio.
- Two ways to ship a plugin: compiled into the binary (trusted, in-tree or
  in a custom `main`), or loaded from a `.so` file at startup.
- One unified object plugins interact with, regardless of which endpoint
  the request came in on. Edits on that object flow back losslessly into
  the raw request or response.
- Plugins can read prompt-cache, session, routing, and identity state.
- Responses can be blocked before they reach the client, including
  streaming responses, with a documented trade-off between latency and
  completeness.
- Existing config, dashboard guardrails, and workflow step ordering keep
  working. The two shipped guardrail types become built-in plugins.

Non-goals for the first release:

- Sandboxing untrusted code. A `.so` runs in-process with full memory
  access. Operators who need isolation get a subprocess or Wasm loader later
  behind the same contract (section 8.4).
- A general workflow DSL. ADR-0003's fixed phase order stays.
- Passthrough (`/p/*`) body editing beyond what the semantic envelope
  already understands.

## 2a. What Bifrost, LiteLLM, Portkey, Kong, and Envoy do

Verified against current sources on 2026-09-03. Only the decisions that
shaped this proposal are listed.

| Topic | Bifrost (Go) | LiteLLM (Python) | Portkey | Kong AI | Envoy ext_proc |
|---|---|---|---|---|---|
| Hook phases | `HTTPTransportPreAuth/Pre/Post`, `PreRequest` (routing, once), `PreLLM`/`PostLLM` (per attempt, incl. fallbacks), `StreamChunk` | `pre_call`, `during_call` (parallel with the LLM call), `post_call`, `logging_only`, MCP variants | `before_request_hooks`, `after_request_hooks` | per-plugin `guarding_mode: INPUT/OUTPUT/BOTH` | request and response body modes `NONE/STREAMED/BUFFERED/FULL_DUPLEX` |
| Unified type | `BifrostRequest` union with one of ~10 sub-requests set; Anthropic translated to chat or responses | raw `dict` plus `GenericGuardrailAPIInputs{texts, images, tools, tool_calls, structured_messages}` | raw JSON | raw JSON, `text_source` selects which messages | raw bytes |
| Actions | short-circuit `Response`/`Stream`/`Error`; guardrails: `detect_only`, `block`, `redact` (reversible placeholders) | return dict (modify), raise (block), `ModifyResponseException` (200 with `finish_reason: content_filter`) | `deny` (446) or pass with results (246); `async: true` is the default and is log-only | block 400/403, `allow_masking`, `recover_redacted` | `CONTINUE`, `CONTINUE_AND_REPLACE`, `ImmediateResponse` |
| Failure | plugin errors logged and ignored (fail-open); guardrails fail closed on conflicting rewrites | re-raises (fail-closed); per-guardrail `unreachable_fallback` | webhook `timeout` with default verdict true | `stop_on_error: true` | `failure_mode_allow: false` |
| Ordering | `placement: pre_builtin/builtin/post_builtin` + `order`; post hooks run in reverse | callback order; `run_in_parallel` for block-only checks | `sequential` flag per hook | plugin priority | filter chain |
| Streaming block | context gate: `PauseStream`, `ResumeStream`, `EndStream`; if any rule can block, hold the whole stream and replay paced by `stream_replay_event_interval_ms` | per-guardrail: buffer all then re-emit (Bedrock, Presidio), or sample every N chunks with `stream_holdback_chars`; `streaming_buffer_until_moderated` | output guardrails buffer the full stream and append a result chunk after `[DONE]`; never cut mid-stream | `response_buffer_size` windows checked as tokens stream | `STREAMED` per chunk or `BUFFERED` whole body |
| Dynamic loading | stdlib `plugin.Open` on `.so`, symbols looked up by name, needs a dynamically linked build (`DYNAMIC=1`); Wasm deprecated in favor of planned webhooks | Python module path in config | webhook guardrails | Go plugins as external plugin servers over a socket | Wasm or ext_proc gRPC |
| Routing | `PreRequestHook` sets provider/model/fallbacks; `KeySelector`, `KeyPoolFilter` callbacks; no strategy enum | `CustomRoutingStrategyBase` via `set_custom_routing_strategy` (SDK only) | n/a | n/a | n/a |
| Audio | `speech_stream`, `transcription_stream` request types in the union; stream hook covers them | TTS input text guarded; transcription output not wired in proxy | not on audio | n/a | n/a |

Takeaways applied below:

- Everyone converged on the same action set: allow, block, respond with a
  safe message, redact, warn. Section 3.3 uses that set.
- Two products (Bifrost, Portkey) return 446 for a blocked request and
  246 for "passed with warnings". That is non-standard HTTP, and SDK
  retry logic treats 4xx as final anyway. Section 3.3 defaults to 400 and
  200 and lets the operator pick 446 and 246 per instance if they want
  parity.
- Nobody streams a blockable response without buffering; the differences
  are in how they hide the buffering. Section 6 exposes the choice
  instead of hiding it.
- LiteLLM's `during_call` (run a check concurrently with the provider
  call, gate the response on it) is cheap to implement and removes the
  check's latency from the critical path. Section 5 adds it.
- Bifrost runs `PreLLM` per attempt, including fallbacks, and pays for it
  with a reverse-order post-hook protocol. GoModel keeps prompt plugins
  once per request: the patched prompt is reused across failover attempts,
  and `Meta.Attempts` tells response plugins what happened.
- Bifrost's `.so` experience is the cautionary tale: static default
  binaries cannot load plugins, and a one-patch-version dependency drift
  fails the load. Section 8.2's stdlib-only contract package and version
  handshake exist to make that failure legible.
- Bifrost guards MCP tool calls with the same rule model. GoModel has an
  MCP gateway; an `mcp` hook kind (tool arguments in, result out) fits the
  same `Decision` vocabulary and is listed under phase 6.

## 3. Plugin contract

### 3.1 Package layout

A new Go module `github.com/enterpilot/gomodel/pluginapi`, versioned
separately from the gateway, holds every type a plugin touches. It must
depend on the standard library only. This is not a style preference: Go's
`plugin` package refuses to load a `.so` unless every package shared between
host and plugin was built from identical source and dependency versions. If
the contract module imported `echo` or `goccy/go-json`, plugin authors would
have to pin those too. The module is a single package: every hook
interface, the `Exchange` types, `Decision`, and `Host` sit in
`pluginapi`, so a plugin has one import and one documentation page.
Section 3.5 explains what that means for compatibility. Today's `ext`
package imports `echo`, so it cannot be the contract; it stays as the
compile-in registration surface and gains a `RegisterPlugin` call.

`internal/core` types (`ChatRequest`, `ResponsesRequest`, ...) never appear
in the contract. Plugins see the unified view from section 4; core owns the
mapping in both directions.

### 3.2 Base interface and optional hooks

```go
package pluginapi

type Manifest struct {
    Name        string   // stable id, used in config and workflows
    Version     string   // plugin's own version, for logs and dashboard
    Description string
    // BuiltWith is filled by the `gomodel plugin build` helper (GoModel
    // module version, Go version) and used only for diagnostics: a .so
    // that fails to load gets an error naming both versions instead of
    // Go's "plugin was built with a different version of package".
    BuiltWith   BuildInfo
    // Kinds declares which hooks the plugin implements, so core can
    // validate config before calling anything. Checked against the
    // interfaces the value actually satisfies at load time.
    Kinds []Kind
    // Mutates declares that the plugin edits the Prompt, Completion, or
    // stream. Non-mutating plugins may share a step with a mutating one
    // and may run concurrently with the provider call (section 5).
    Mutates bool
    // ConfigSchema drives the dashboard form and config validation.
    ConfigSchema []Field
}

// Field is one dashboard form field and one validated config key. It is
// the public shape of today's internal guardrails.TypeField, so the
// existing GuardrailEditor renders plugin fields with no frontend change.
type Field struct {
    Key         string
    Label       string
    Input       Input      // text | textarea | number | select | checkboxes | secret | model
    Required    bool
    Help        string
    Placeholder string
    Default     any
    Options     []Option   // select and checkboxes
    // Scope says which editor shows the field: "instance" (default, the
    // guardrail editor) or "route" (the virtual model editor, section 7).
    Scope       FieldScope
}

type Option struct { Value, Label string }

type Plugin interface {
    Manifest() Manifest
    // Init receives the instance config (validated against ConfigSchema)
    // and a Host for logging, metrics, and gateway-internal inference.
    // Called once per configured instance at startup or on admin update.
    Init(ctx context.Context, config json.RawMessage, host Host) error
    Close(ctx context.Context) error
}
```

Hooks are optional interfaces detected structurally, the same pattern
`ext.ResponseFeedbackObserver` already uses. A plugin implements the ones
it needs:

```go
// Kind values: "request", "prompt", "response", "stream", "route", "audio".

// RequestHook runs after auth and session detection, before model
// resolution. Body edits here affect routing. Replaces ext.RequestRewriter
// (which stays as a thin adapter over this hook).
type RequestHook interface {
    OnRequest(ctx context.Context, x *Exchange) (Decision, error)
}

// PromptHook is the guardrails phase: after routing, before the provider
// call. Sees the resolved provider and model. May modify, block, or answer
// the request itself.
type PromptHook interface {
    OnPrompt(ctx context.Context, x *Exchange) (Decision, error)
}

// ResponseHook runs on a complete non-streaming response, or on the
// assembled response of a buffered stream, before it is sent to the client.
type ResponseHook interface {
    OnResponse(ctx context.Context, x *Exchange) (Decision, error)
}

// StreamHook runs per parsed stream event. StreamPolicy tells core how to
// drive it (section 6).
type StreamHook interface {
    StreamPolicy() StreamPolicy
    OnStreamEvent(ctx context.Context, x *Exchange, ev *StreamEvent) (StreamDecision, error)
    OnStreamEnd(ctx context.Context, x *Exchange) (Decision, error)
}

// RouteStrategy is a load-balancing strategy for virtual models
// (section 7). Generalizes ext.RouteSelector.
type RouteStrategy interface {
    Select(ctx context.Context, req RouteRequest) (RouteChoice, error)
    OnAttemptEnd(outcome RouteOutcome)
}

// CompleteHook runs after the client response is fully written. Never
// blocks the client; errors are logged only.
type CompleteHook interface {
    OnComplete(ctx context.Context, x *Exchange)
}
```

### 3.3 Decisions

Every synchronous hook returns a `Decision`. This is the "action" vocabulary
that Portkey, LiteLLM, and Bifrost each expose in slightly different forms,
folded into one struct so the dashboard and audit log can render it
uniformly.

```go
type Action string

const (
    ActionAllow   Action = "allow"   // continue, possibly with edits already applied to x
    ActionBlock   Action = "block"   // reject with Status/Code/Message in the endpoint's native error dialect
    ActionRespond Action = "respond" // short-circuit: send Response as the completion (HTTP 200)
    ActionWarn    Action = "warn"    // continue; record Detail in audit and add X-GoModel-Guardrail headers
)

type Decision struct {
    Action  Action
    Status  int    // block only; default 400 (request phases) or 502 (response phases).
                   // Operators wanting Bifrost/Portkey parity set block_status: 446
                   // and warn_status: 246 on the instance.
    Code    string // machine-readable, e.g. "content_policy"
    Message string
    // Response is the synthetic completion for ActionRespond, in unified
    // form. Core renders it in the request's dialect and, for streaming
    // requests, as a well-formed single-chunk stream.
    Response *Completion
    // Detail is a JSON-serializable summary recorded in the audit trail's
    // revision chain. Must not contain secrets.
    Detail any
}
```

`ActionRespond` is what most guardrail products call "block with a safe
message": the client gets an ordinary assistant turn ("I can't help with
that"), which keeps agent loops alive instead of surfacing a 4xx. Both
options exist because both are needed.

### 3.4 Failure handling

Each configured instance has `fail_mode: closed | open` and `timeout`.
Defaults: `closed` for `prompt`, `response`, and `stream` hooks (a
guardrail that crashes must not let content through), `open` for
`request`, `route`, and `complete` hooks (an editor or router that crashes
degrades to default behavior). Panics are recovered and treated as errors.
A `closed` failure produces HTTP 500 with code `plugin_failure` and the
plugin name in the audit record only, never in the client message.

### 3.5 Compatibility policy

There is no API version constant to compare, because neither loader in
scope can use one. The compiler checks a compiled-in plugin. Go's `plugin`
package refuses a `.so` unless every package present in both host and
plugin was built from identical source with the same toolchain, so a `.so`
that loads at all already has the host's contract. A protocol version
arrives with the out-of-process loader (section 8.4), where a
serialization boundary makes it meaningful.

Two rules keep the contract liberal for plugin authors:

- `pluginapi` is its own Go module, depending on the standard library
  only. A `.so` shares with the host exactly the stdlib plus this one
  module. GoModel internals, providers, and dashboard changes never affect
  a plugin; only a `pluginapi` release or a Go toolchain change does.
- Changes are additive only. New fields are appended, new hook interfaces
  are added beside the existing ones, new `Action`, `PartKind`, and
  `EventKind` values may appear and plugins treat unknown values as
  opaque. Removals and renames wait for a major version of the module and
  are announced one minor release ahead. Plugin source therefore keeps
  compiling across minor releases; only the binary artifact ages.

`pluginapi` is a single package. An earlier draft split each hook kind
into its own package so that adding a hook would not change the package a
`.so` shares with the host. That was dropped: a new hook almost always
needs a new field or enum value on `Exchange` anyway, Go patch releases
force a `.so` rebuild regardless, and the split cost authors extra imports
and package names (`prompt`, `stream`, `response`) that shadow the most
natural local variables in exactly this code. The rebuild is a CI step
against a pinned GoModel version, so the design optimizes source
compatibility and makes the rebuild cheap instead of rare.

What a host change costs a plugin:

| Host change | Plugin source edit | `.so` rebuild | Compiled-in rebuild |
|---|---|---|---|
| New hook kind, field, or enum value in `pluginapi` | no | yes | with the binary, as always |
| Internal GoModel change, new provider, dashboard work | no | no | with the binary |
| Go toolchain upgrade (including patch releases) | no | yes | with the binary |
| Removal or rename (major version only) | yes | yes | with the binary |

Making the rebuild cheap:

- Each GoModel release names the `pluginapi` version and Go version it
  was built with in `gomodel version` and `/admin/plugins`.
- `gomodel plugin build` runs `go build -buildmode=plugin` with the host's
  recorded flags and stamps `BuiltWith` (Go version, `pluginapi` version)
  so a refused load is reported as "built against pluginapi v0.3.0 with
  go1.27.0, host has v0.4.0 with go1.27.1" instead of Go's generic
  message.
- A `gomodel-plugin-builder:<version>` image pins the matching toolchain,
  so an operator's CI rebuilds every `.so` with one line per GoModel
  upgrade.

### 3.6 Host

```go
type Host interface {
    Logger() *slog.Logger            // pre-tagged with plugin name
    // Inference runs an internal chat completion through the gateway
    // (routing, usage, budgets apply; request_origin=plugin). Used by
    // LLM-based guardrails. Replaces guardrails.ChatCompletionExecutor.
    Inference() Inference
    // History loads stored earlier turns for Responses requests that
    // reference previous_response_id or a conversation (section 4.2).
    History(ctx context.Context, meta Meta) ([]Message, error)
    Metrics() Metrics                // counter/histogram registration under plugin_<name>_*
}
```

`Inference` takes and returns unified `Prompt` and `Completion` values, so a
plugin never needs `internal/core`.

## 4. The unified object: `Exchange`

ADR-0002 named the request-side semantic envelope `WhiteBoxPrompt`, and
`core.WhiteBoxPrompt` exists (`internal/core/semantic.go`), but it is a cache
of typed requests keyed by operation, not a cross-endpoint content model.
The architecture review also flagged its name. The proposal keeps
`core.WhiteBoxPrompt` as the internal backing store and exposes a public
facade to plugins:

```go
type Exchange struct {
    Meta     Meta          // read-only identity and routing facts
    Prompt   *Prompt       // unified request; nil for non-inference routes
    Response *Completion   // unified response; nil until the response phase
    Stream   *StreamState  // accumulated stream state; nil for non-streaming
    Headers  *Headers      // inbound request headers (editable) and outbound response headers (append-only)
    Values   Values        // per-request KV bag for passing state between a plugin's own hooks
}
```

### 4.1 `Meta`

Read-only snapshot of what core already puts in `context.Context` today
(the twenty keys in `internal/core/context.go`), plus the resolution result:

| Field | Source |
|---|---|
| `RequestID`, `Dialect` (`openai`, `anthropic_messages`), `Endpoint`, `Operation` | snapshot, `RequestDialectFromContext` |
| `UserPath`, `AuthKeyID`, `Labels` | auth key binding, tagging |
| `SessionID` | session detector |
| `RequestedModel`, `Provider`, `ProviderName`, `Model`, `VirtualModelSource` | `Workflow.Resolution` (empty in the `request` phase) |
| `WorkflowVersionID`, `Features` | `Workflow.Policy` |
| `Stream` | `WhiteBoxPrompt.StreamRequested` |
| `Attempts []Attempt` | `gateway.ProviderAttempt` (response phases only) |
| `Cache CacheInfo` | see 4.4 |

### 4.2 `Prompt`

One content model for chat completions, Responses, Anthropic messages, and
batch items:

```go
type Prompt struct {
    System   []Part          // instructions / system messages, in order
    Messages []Message       // the conversation
    Tools    []Tool          // read-only in v1
    Params   Params          // model, temperature, max_tokens, stop, reasoning effort, ...
    // Raw is the current raw JSON body. Read-only; edit through PatchRaw.
    Raw      json.RawMessage
}

type Message struct {
    ID        string   // stable within the exchange; survives edits
    Role      Role     // system | user | assistant | tool | developer
    Parts     []Part
    Name      string
    ToolCallID string
    // CacheBreakpoint reports an explicit cache_control marker on this
    // message (Anthropic dialect), see 4.4.
    CacheBreakpoint bool
}

type Part struct {
    Kind       PartKind   // text | image | audio | file | tool_call | tool_result | reasoning | refusal | opaque
    Text       string     // text, reasoning, refusal
    MediaType  string
    Data       []byte     // decoded inline media; nil when URL-referenced
    URL        string
    ToolCall   *ToolCall
    ToolResult *ToolResult
    // Opaque parts are content core does not model (a provider-specific
    // part type). They round-trip unchanged.
}

type ToolCall struct {
    ID        string          // the provider-visible call id (chat tool_calls[].id, Responses call_id, Anthropic tool_use.id)
    Name      string
    Arguments json.RawMessage // as sent; a chat string argument is exposed parsed
    Server    string          // MCP server name when the call went through the MCP gateway, else empty
}

type ToolResult struct {
    CallID  string
    Parts   []Part            // text, image, or opaque; Anthropic allows rich results
    IsError bool
}
```

The conversation is the whole history the client sent, in order, with tool
calls kept where each dialect puts them:

| Dialect | Assistant call | Tool result |
|---|---|---|
| chat/completions | `assistant` message with `tool_calls[]` becomes one `Message{Role: assistant}` with one `tool_call` part per call, after any text part | `role: tool` message becomes `Message{Role: tool, ToolCallID}` with one `tool_result` part |
| Responses | `function_call` input item becomes an `assistant` message with one `tool_call` part; consecutive items from the same turn are kept as separate messages so IDs stay stable | `function_call_output` item becomes a `tool` message |
| Anthropic messages | `tool_use` content block becomes a `tool_call` part inside the assistant message, in block order | `tool_result` block inside a `user` message becomes a `tool_result` part of that user message, with `ToolResult.CallID`; the `Role` stays `user`, since the pairing is by ID rather than by role |
| built-in tools (web search, computer use, MCP) | same shape; `ToolCall.Server` is set for MCP gateway calls, provider built-in call items are `opaque` parts with `ToolCall` filled where the ID and name are known |

Helpers save every guardrail from re-deriving the same views:

```go
func (p *Prompt) LastUser() *Message                 // most recent user turn
func (p *Prompt) Text(roles ...Role) string          // concatenated text parts, Kong's text_source
func (p *Prompt) ToolCalls() []ToolCallRef           // every call in history with its message ID and whether a result exists
func (p *Prompt) NewSince(n int) []Message           // messages after the first n, for only_scan_new_messages
```

Edits go through methods, not field assignment, so core can track what
changed and re-encode only touched nodes:

```go
func (p *Prompt) SetText(msgID string, partIdx int, text string)
func (p *Prompt) SetToolArguments(msgID, callID string, args json.RawMessage)
func (p *Prompt) SetToolResult(msgID, callID string, parts []Part)
func (p *Prompt) Insert(at int, m Message) string        // returns new ID
func (p *Prompt) Remove(msgID string)
func (p *Prompt) SetParam(name string, value any)
func (p *Prompt) PatchRaw(patch []JSONPatchOp) error     // escape hatch for unmodeled fields
```

Two limits on history worth stating:

- `Prompt` holds what is in the request body. With Responses
  `previous_response_id` or `conversation`, earlier turns live in
  `responsestore` and `conversationstore` and are not in the body. A plugin
  that needs them calls `Host.History(ctx, meta) ([]Message, error)`, which
  reads the stored items in the same unified form. Core does not load them
  by default, since most guardrails only look at the new turn and the load
  is a storage round trip.
- Removing a message that carries a `tool_call` part whose result is still
  present, or the reverse, is rejected at apply time; the pairing helper
  `ToolCalls()` reports what would break so a plugin can remove both.

Apply-back generalizes the existing `applyMessagesToChatPreservingEnvelope`
and `applyMessagesToResponses`: an untouched message is copied from the
original typed request verbatim (all `ExtraFields`, `cache_control`,
multi-part structure intact); a touched text part is rewritten in place
inside its original structured content; inserted messages are encoded from
the unified form. The current restriction that only system messages may be
inserted or removed is lifted, because the message-ID model removes the
alignment ambiguity that motivated it.

For `/v1/messages`, the native fast path (`messages_native.go:34`) already
disables itself when a request patcher exists. Same rule for plugins: any
configured `prompt`, `response`, or `stream` plugin forces the translated
path. The Anthropic dialect is still visible to the plugin through
`Meta.Dialect`, and errors render in the Anthropic envelope.

### 4.3 `Completion`

```go
type Completion struct {
    ID           string
    Model        string
    Choices      []Choice     // one for Responses and Anthropic; n for chat completions
    Usage        Usage
    FinishReason string
    Raw          json.RawMessage
}

type Choice struct {
    Message Message           // same Part model: text, tool_call, reasoning, refusal
}
```

A response that asks the client to run tools is therefore a `Choice` whose
message has `tool_call` parts, so one response guardrail can inspect
outgoing tool arguments (a shell command, a URL) exactly as it inspects
text. Tool-call arguments also arrive as `tool_call_delta` stream events,
which a `transform` stream plugin sees with the accumulated argument JSON
so far in `x.Stream`.

Same edit methods as `Prompt`. Setting `FinishReason` to `content_filter`
is the OpenAI-compatible way to say a response was cut; core maps it to
`stop_reason: "end_turn"` plus an `X-GoModel-Guardrail` header in the
Anthropic dialect, which has no equivalent.

### 4.4 Prompt-cache and session state

The request the user cares about: plugins must be able to see, and avoid
breaking, prompt caching. Three pieces of state are exposed:

- `Message.CacheBreakpoint` and `Prompt.CacheBreakpoints()`: explicit
  Anthropic `cache_control` markers, read from `ExtraFields` where
  `internal/providers/cache_control.go` finds them today.
- `Meta.Cache.PlannedPrefixMessages`: how many leading messages the
  provider prompt-cache planner (`internal/providers/cache_planner.go`)
  will mark as the cached prefix for the resolved provider, computed after
  routing. A plugin that edits message `i < PlannedPrefixMessages` breaks
  the cache for this session; a plugin that inserts at the tail does not.
  Editors should prefer appending. Core logs a debug line when a plugin
  edits inside the planned prefix.
- `Meta.SessionID` and `Meta.Cache.SessionTarget`: the sticky
  provider/model for this session, so routing plugins can honour affinity
  the way `ext.RouteRequest.SessionTarget` documents.

Response cache interaction: the guardrails hash already feeds the semantic
cache key (`responsecache/semantic.go`). It becomes the hash of the
effective plugin chain for the request phases, so a config change
invalidates cached answers produced under different rules. Response-phase
plugins run on cache hits as well as misses, because a policy tightened
after an answer was cached must still apply.

### 4.5 `Headers`

`Headers.Request` is the inbound header set with credential headers
redacted (`core.IsCredentialHeader`), editable; edits affect upstream
passthrough headers and the audit record. `Headers.Response` is append-only
and merged into the client response, replacing `ext.Result.ResponseHeader`.
`Headers.Upstream` lets a `prompt` plugin add static upstream headers,
which fills the gap that no `extra_headers` provider config exists today.

## 5. Phases and execution model

```
auth -> session -> [request plugins] -> model resolution / virtual models ([route plugin])
     -> [prompt plugins] -> rate limit / budget / cache lookup -> provider
     -> [response plugins | stream plugins] -> client -> [complete plugins]
```

- `request` plugins run where `RequestRewriteMiddleware` runs now
  (`internal/server/http.go:385`). `ext.RequestRewriter` is re-implemented
  as an adapter that builds an `Exchange` from bytes, so Pro compression
  keeps working unchanged.
- `prompt` plugins run where `TranslatedRequestPatcher` runs now
  (`internal/gateway/inference_prepare.go:99`), plus the batch preparer.
- `response` and `stream` plugins wrap the provider result inside
  `handleStreamingReadCloser` and the non-streaming dispatch in
  `translated_inference_service.go`, before the Anthropic stream converter,
  so they always see the canonical OpenAI form.

A `prompt` plugin whose manifest declares `Mutates: false` may set
`concurrent: true` on its instance. Core then starts the provider call
without waiting for it and joins on the decision before the first byte of
the response is written to the client. A `block` or `respond` decision
discards the provider result (usage is still recorded). This is LiteLLM's
`during_call` and it takes a classifier's round trip off the critical path
for non-streaming requests and off time-to-first-token for streaming ones.

Ordering keeps ADR-0003's `step` semantics per phase, with one correction:
a step that contains more than one plugin declaring `Mutates: true` in its
manifest is a validation error. Parallel steps are for concurrent checks
(three classifiers at step 10), and their decisions merge by severity:
`block` beats `respond` beats `warn` beats `allow`. Sequential steps are
for editors. This replaces the silent last-writer-wins in `pipeline.go`.

## 5a. Integration with the workflows system

Plugins are not a parallel system next to workflows. Workflows stay the one
place that decides *which* instances run on a request and in *what order*;
plugins are what the steps refer to. Concretely:

- A plugin instance (config `plugins.instances[]` or a dashboard
  `guardrails.Definition` with `type: plugin:<name>`) is a named, reusable
  object in the same store `system_prompt` and `llm_based_altering`
  definitions live in today. Config-declared instances are seeded into
  that store at startup exactly as `configGuardrailDefinitions` does now.
- The workflow payload references instances by name. `GuardrailStep`
  gains `phase` and the array is renamed to `steps`; `guardrails` remains
  accepted and is read as `steps` with `phase: prompt`, so every stored
  version keeps compiling without a migration:

  ```json
  {
    "schema_version": 2,
    "features": { "cache": true, "audit": true, "usage": true, "guardrails": true },
    "steps": [
      { "ref": "pii-redact",     "phase": "prompt",   "step": 10 },
      { "ref": "toxicity-check", "phase": "prompt",   "step": 10, "concurrent": true },
      { "ref": "safety-prompt",  "phase": "prompt",   "step": 20 },
      { "ref": "secret-scan",    "phase": "response", "step": 10 },
      { "ref": "secret-scan",    "phase": "stream",   "step": 10 }
    ]
  }
  ```

- `workflows.Compile` produces one compiled chain per phase instead of one
  `*guardrails.Pipeline`. The chain hash per phase replaces the single
  guardrails hash; the prompt-phase hash is what feeds the semantic cache
  key.
- Workflow scope matching (global, provider, provider plus model, user
  path) is unchanged and is how a plugin gets applied to some traffic and
  not other traffic. No per-plugin scoping field is added; the existing
  `Definition.UserPath` stays as a hard guard the instance enforces on
  itself.
- `features.guardrails` keeps its meaning as the process-level and
  workflow-level upper bound, now covering all phases. The env switch
  `GUARDRAILS_ENABLED` is kept and `PLUGINS_ENABLED` is an alias.
- `request` and `route` phases are the exception, because they run before
  a workflow is matched (`request`) or during model resolution (`route`).
  `request` plugins are process-global, registered in `plugins.instances`
  with `phase: request`, ordered by `step`, the same way `ext.Rewriter`s
  are global today. `route` strategies are referenced from the virtual
  model (`strategy_plugin`), not from a workflow, since the choice belongs
  to the load balancer definition.
- Audit records already carry `workflow_version_id`; each plugin decision
  is appended to the request revision chain that rewriters write today
  (`recordRequestRevision`), so a request traces to the exact workflow
  version and the exact plugin decisions taken under it.

## 6. Streaming responses

Chunks already written to the client cannot be recalled, so "block content
before it hits the user" on a stream has exactly three honest shapes. The
plugin picks one through `StreamPolicy`; core does the buffering.

```go
type StreamMode string

const (
    StreamObserve   StreamMode = "observe"   // read-only, zero added latency
    StreamTransform StreamMode = "transform" // per-event allow/drop/rewrite/terminate
    StreamBuffer    StreamMode = "buffer"    // hold the whole stream, run OnResponse on the assembled Completion, then replay
)

type StreamPolicy struct {
    Mode StreamMode
    // LookbehindChars (transform only): core withholds this many trailing
    // characters of text so a pattern spanning two chunks (a phone number,
    // an API key) can still be matched before it is flushed. 0 disables.
    LookbehindChars int
    // MaxBufferBytes (buffer only): cap before core fails closed. Default 4 MiB.
    MaxBufferBytes int
}

type StreamEvent struct {
    Seq     int
    Kind    EventKind      // text_delta | tool_call_delta | reasoning_delta | finish | usage | other
    Choice  int
    Text    string         // for text and reasoning deltas
    Raw     json.RawMessage
}

type StreamDecision struct {
    Action    StreamAction // pass | drop | replace | terminate
    Text      string       // replace: new delta text
    Terminate *Decision    // terminate: how to end (block -> error event, respond -> canned final text)
}
```

Behaviour per mode:

- `observe` reuses `streaming.ObservedSSEStream` unchanged.
- `transform` adds a `streaming.TransformedSSEStream` that parses each
  event into a `StreamEvent` for the request's canonical dialect, calls the
  chain, and re-encodes only events a plugin changed. Drop and replace
  edit the `delta.content` of the raw chunk and leave every other field
  untouched. `terminate` writes one final chunk with
  `finish_reason: "content_filter"` (or an Anthropic `error` event) and
  `[DONE]`, then closes the upstream body. The client keeps whatever it
  already received; that is the documented cost of this mode.
- `buffer` drains the upstream into a bounded buffer, sends an SSE comment
  line (`: gomodel-buffering`) every 15 s so proxies and clients do not
  time out, assembles a `Completion` from the events (the same logic
  `usage.StreamUsageObserver` and the audit stream observer use to read
  deltas), runs the `response` chain, and then either replays the original
  bytes unchanged (allow, warn), synthesizes a stream from the modified
  `Completion` (allow with edits, respond), or emits a single error chunk
  (block). Time-to-first-token becomes time-to-last-token; the client sees
  a valid stream either way.

`x.Stream` exposes the text accumulated so far per choice, so a
`transform` plugin can implement windowed checks (Kong's
`response_buffer_size`, LiteLLM's `streaming_sampling_rate`) by
re-scanning the tail every N events without core adding a fourth mode.

Mixed chains: if any plugin asks for `buffer`, the whole stream is
buffered and `transform` plugins run over the replay. `LookbehindChars` is
the recommended default for regex redactors (`64`), because it preserves
streaming for the common case and only costs a few characters of delay.

Non-streaming requests and `respond` decisions in a streaming request are
handled by rendering the `Completion` into a one-chunk stream, which the
`response` cache already knows how to do for cache hits.

## 7. Routing strategy plugins

`VirtualModelConfig.Strategy` gains one value and two fields:

```yaml
virtual_models:
  - source: smart-router
    strategy: plugin            # new: round_robin | cost | adaptive | failover | plugin
    strategy_plugin: latency-aware   # plugin instance name; required with strategy: plugin
    strategy_config:            # per virtual model; keys are the plugin's Scope: route fields, validated against them
      p95_window: 5m
    targets: [...]
```

`RouteRequest` is `ext.RouteRequest` plus `Meta` (so a strategy can route
by user path, label, or prompt size) and the `Prompt` (read-only, so a
"small prompt to cheap model" policy needs no separate classifier stage).
`RouteChoice` returns the qualified target and an optional `Reason` recorded
in the attempt log. The `adaptive` strategy stays as the name for the
single registry-level `ext.RouteSelector`; `plugin` selects a named
instance per virtual model, which is the "new position plus fields on
today's strategy" the requirement asks for. Failover chains, capacity
probes, and session pinning remain core's job exactly as `ext.RouteSelector`
documents.

## 8. Loading plugins

### 8.1 Compiled in (trusted)

Two sub-cases, both zero-cost when unused:

- In-tree built-ins under `internal/plugins/<name>` registered in a
  `builtin.Catalog` at init. Phase 1 ships `system_prompt`,
  `llm_based_altering` (ported), `regex_replace` (content editor with
  lookbehind streaming support), `header_edit`, `keyword_block`, and
  `json_schema_check` as reference implementations that exercise every
  hook kind.
- Custom binaries call `ext.RegisterPlugin(p pluginapi.Plugin)` before
  `run.Run`, the same way Pro registers rewriters today.

### 8.2 Shared object (`.so`)

```yaml
plugins:
  search_paths: ["/etc/gomodel/plugins"]     # PLUGINS_SEARCH_PATHS; empty disables .so loading
  instances:
    - name: acme-guard
      type: acme-guard                        # builtin name, or a .so manifest Name
      file: acme-guard.so                     # optional; defaults to <type>.so under search_paths
      sha256: "..."                           # optional pin; mismatch fails startup
      config: { threshold: 0.8 }
      fail_mode: closed
      timeout: 750ms
```

The loader (`internal/pluginload`) calls `plugin.Open`, looks up the
exported symbol `GoModelPlugin` (type `pluginapi.Plugin` or a
`func() pluginapi.Plugin` constructor so one `.so` can serve several
instances) and verifies that every `Kind` in the manifest is backed by the
matching interface. Any mismatch is a startup error naming the file, never a
runtime surprise.

Hard constraints of Go's `plugin` package, which the docs must state
plainly:

- Linux, macOS, and FreeBSD only, and `CGO_ENABLED=1`. The published image
  builds with `CGO_ENABLED=0` (`Dockerfile:24`), so `.so` support needs a
  second build variant (`gomodel:<ver>-plugins`) built with cgo on
  `debian-slim` rather than `scratch`. The default image stays static.
- The plugin must be built with the exact same Go toolchain version, the
  same `GOFLAGS` (notably `-trimpath`), and the same version of every
  package it shares with the host. With a stdlib-only `pluginapi` module
  that reduces to: same Go version and same `pluginapi` module version.
  The `gomodel plugin build` helper and the `gomodel-plugin-builder`
  image (section 3.5) make that a one-line CI step. Expect to rebuild
  whenever `pluginapi` or the Go toolchain changes.
- Plugins cannot be unloaded. Config edits that change a `.so` path take
  effect on restart; edits to `config` re-run `Init` on the existing
  instance (`Close` then `Init`), which is enough for the dashboard flow.
- A `.so` is trusted code. Document that loading one is equivalent to
  changing the binary, and that `search_paths` should be root-owned.

### 8.3 Dashboard and admin API

Both dashboard editors are already schema-driven or server-driven, so
plugins extend them by declaring fields, not by shipping frontend code:

- Guardrail editor. `guardrails.Definition` gains `Type = "plugin:<name>"`.
  `GET /admin/guardrails/types` (`Service.TypeDefinitions()`) appends one
  `TypeDefinition` per loaded plugin that implements a `prompt`,
  `response`, or `stream` hook, built from `Manifest.ConfigSchema` fields
  with `Scope: instance`, with `Defaults` taken from each field's
  `Default`. `GuardrailEditor.svelte` already renders `text`, `textarea`,
  `number`, `select`, and `checkboxes` from that payload. Two inputs are
  added: `secret` (write-only, redacted in `GET`, so an API key for an
  external classifier never round-trips to the browser) and `model` (a
  provider/model picker fed by the catalog, which `llm_based_altering`
  needs and hand-codes today). Core validates a submitted config against
  the schema before calling `Init`, so a plugin gets typed, required-checked
  input and can still reject it with its own error from `Init`.
- Virtual model editor. The strategy dropdown is served by the admin API
  (`VIRTUAL_MODEL_STRATEGIES` in `handler.go`, where `adaptive` already
  appears only when a selector is registered). Each loaded `RouteStrategy`
  plugin adds one dropdown entry `plugin:<name>` with the plugin's label,
  and selecting it renders the plugin's `Scope: route` fields under the
  targets table, stored as `strategy_config` on the virtual model. The same
  schema validates the YAML `strategy_config` for declarative virtual
  models. A plugin that needs both a global setting (an endpoint and API
  key) and a per-virtual-model setting (a latency budget) declares one
  field of each scope; the instance-scoped values are entered once on the
  Guardrails page (or in `plugins.instances[].config`) and the
  route-scoped values per virtual model.
- Workflow editor. Step rows gain a phase selector; the ref dropdown lists
  instances filtered to those implementing a hook for the chosen phase,
  from the manifest's `Kinds`. The `/admin/plugins` endpoint
lists loaded plugins with manifest, source (`builtin`, `registered`,
`/path/x.so`), hook kinds, health, and last error. Workflow payloads gain
`phase` on each step (`request | prompt | response | stream`, default
`prompt`), which keeps every existing stored workflow valid.

### 8.4 Other loaders, later

The loader is an interface (`Loader.Load(spec) (pluginapi.Plugin, error)`),
so a hashicorp/go-plugin subprocess loader (cross-language, isolated, adds
an RPC hop per hook) or a Wasm loader (extism or wazero; sandboxed,
portable, no cgo, but every `Exchange` crosses a serialization boundary)
can be added without touching the contract. Neither is in scope now; `.so`
is what was asked for and it is the only option with zero per-call
overhead.

## 9. Audio (planned shape, not in the first phases)

- `/v1/audio/transcriptions` and `/translations`: an `AudioInputHook`
  receives the upload (bytes, media type, duration if known, `Meta`) and
  may block or replace the bytes (for example strip metadata, or reject
  over a size or duration policy). The transcript is a `Completion` with
  one text part, so existing `response` plugins apply to speech-to-text
  output for free.
- `/v1/audio/speech`: the input text is a `Prompt` with one user message,
  so `prompt` plugins apply. An `AudioOutputHook` receives the synthesized
  bytes for logging, watermarking, or blocking.
- Realtime: `realtime.Hooks.MapClientFrame` is already a mutating frame
  hook. A `RealtimeHook` maps client and server frames through the same
  `StreamDecision` vocabulary; text events (`response.text.delta`,
  `conversation.item.input_audio_transcription.completed`) become
  `StreamEvent`s, audio frames stay opaque in v1.

`CapabilitiesForEndpoint` in `internal/core/workflow.go` gains
`Plugins: true` for these operations when the hooks land.

## 10. Implementation phases

Each phase is independently shippable and keeps existing behaviour.

1. Contract and built-ins (foundation).
   `pluginapi` package; `Exchange`, `Prompt`, `Completion`, `Decision`;
   `builtin.Catalog`; `ext.RegisterPlugin`; adapter that runs the current
   `guardrails.Guardrail` and `ext.RequestRewriter` through the new
   pipeline; port `system_prompt` and `llm_based_altering`; generalized
   apply-back with message IDs; step validation for `Mutates`. Tests:
   round-trip apply-back for chat, Responses, Anthropic, batch items with
   `ExtraFields` and `cache_control` preserved.
2. Response phase, non-streaming.
   `Completion` mapping for chat, Responses, Anthropic; `response` step
   phase in workflows; `respond` and `block` rendering per dialect; cache
   hit path; `keyword_block` and `regex_replace` built-ins. Reversible
   redaction (placeholders on the way in, restored on the way out, as
   Bifrost and Kong offer) is a natural third built-in once `Values`
   carries state from the prompt phase to the response phase.
3. Streaming.
   `TransformedSSEStream`, buffer mode with keep-alive comments and
   `Completion` assembly, lookbehind, termination chunks; force translated
   path for `/v1/messages` when stream plugins exist. Tests: chunk
   boundaries, `[DONE]` handling, Anthropic converter ordering, client
   disconnect during buffering.
4. Routing and headers.
   `strategy: plugin` with `strategy_plugin` and `strategy_config`;
   `RouteStrategy` adapter over `ext.RouteSelector`; `Headers.Upstream`;
   `header_edit` built-in.
5. `.so` loader.
   `internal/pluginload`, config section, sha256 pin, version handshake,
   `gomodel plugin build` helper, cgo image variant,
   `gomodel-plugin-builder` image, `/admin/plugins`,
   dashboard plugin list, `secret` and `model` inputs, strategy dropdown entries and `strategy_config` fields in the virtual model editor, `docs/advanced/plugins.mdx`, ADR-0003 amendment for the `steps` payload
   and an example plugin under `examples/plugins/`.
6. Audio, realtime, and MCP tool-call hooks.

## 11. Open decisions for review

1. Naming: `Exchange` / `Prompt` / `Completion` as proposed, or keep
   `WhiteBoxPrompt` as the public name of the request view for continuity
   with ADR-0002.
2. Default `fail_mode` for `response` plugins: `closed` is safer, but it
   means a crashing observer-style plugin turns a 200 into a 500. The
   proposal keeps `closed` and relies on `observe`-only plugins declaring
   no `Mutates`, which core could treat as `open` automatically.
3. Whether `request`-phase plugins should see `Prompt` at all, or only
   `Raw` plus `Headers` (cheaper, and routing has not happened so the
   dialect is known but the provider is not). Proposal: `Prompt` is built
   lazily on first access, so it costs nothing for header-only plugins.
4. Buffer mode keep-alive: SSE comment lines are ignored by every
   mainstream SDK, but a client that counts bytes or applies a first-byte
   timeout could still misbehave. Alternative is to send nothing and rely
   on the operator raising client timeouts.
5. Whether to ship the cgo image variant at all, or document `.so`
   support as "build the gateway yourself with cgo". The variant is cheap
   in CI but doubles the image matrix.
