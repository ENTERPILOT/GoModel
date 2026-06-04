# OpenAI Agents SDK Support

Status checked: 2026-06-02

## Short answer

GoModel is close to supporting the OpenAI Agents SDK for normal HTTP-based
model calls.

Basic Agents SDK runs should work when the SDK is pointed at GoModel as an
OpenAI-compatible endpoint and tracing is disabled or configured separately.
GoModel already exposes:

- `POST /v1/responses`
- `POST /v1/chat/completions`
- Responses streaming over SSE
- Responses lifecycle endpoints:
  - `GET /v1/responses/{id}`
  - `GET /v1/responses/{id}/input_items`
  - `POST /v1/responses/{id}/cancel`
  - `DELETE /v1/responses/{id}`
  - `POST /v1/responses/input_tokens`
  - `POST /v1/responses/compact`

That is enough for Codex-style Responses clients and likely enough for a simple
Agents SDK `Runner.run(...)` with text and function tools.

It is not yet safe to market as full Agents SDK support. The SDK uses newer
Responses fields, state-management modes, built-in tools, streaming events, and
optional websocket transport. Some of those are only pass-through today, and
some are not validated against the SDK.

## What the SDK expects

The OpenAI Agents SDK uses the Responses API by default for OpenAI models. Its
Responses request path can send fields such as:

- `previous_response_id`
- `conversation`
- `instructions`
- `model`
- `input`
- `include`
- `tools`
- `prompt`
- `temperature`
- `top_p`
- `truncation`
- `max_output_tokens`
- `tool_choice`
- `parallel_tool_calls`
- `stream`
- `text`
- `store`
- `prompt_cache_retention`
- `reasoning`
- `metadata`
- `context_management`
- SDK `extra_args` / `extra_body` fields

GoModel preserves unknown top-level Responses fields, so native OpenAI-compatible
providers receive many of these without code changes. The weak spot is the
translated-provider path, where Responses-only fields can leak into Chat
Completions requests or newer Responses input/output item types can lose their
exact shape.

## Current support assessment

### Supported now

- Basic non-streaming Responses calls.
- Basic streaming Responses calls over HTTP/SSE.
- Function tool calls and `function_call_output` items in the
  Responses-to-Chat adapter.
- `tools`, `tool_choice`, `parallel_tool_calls`, `temperature`,
  `max_output_tokens`, `reasoning`, and `metadata`.
- Native OpenAI-compatible passthrough for extra top-level request fields.
- Stored non-streaming response snapshots for local response retrieval and
  `input_items`.
- `responses.input_tokens` and `responses.compact` when the selected provider
  exposes native support.
- `/v1/conversations` lifecycle endpoints.
- `store: false` skips GoModel's local response snapshot.
- Unknown Responses input item types round-trip unchanged for native Responses
  providers; chat-translated providers now return a clear compatibility error.
- First OpenAI Agents SDK guide and runnable smoke examples.
- Manual Anthropic probes passed for direct OpenAI Responses calls, Python
  Agents SDK `Runner.run(...)`, function tool loops, and
  `Runner.run_streamed(...)` on 2026-06-02.

### Needs validation

- Python Agents SDK with `OpenAIResponsesModel`.
- JavaScript Agents SDK with the default Responses provider.
- `Runner.run_streamed(...)` against GoModel SSE streams.
- Function tool loops across multiple SDK turns.
- Handoffs and agents-as-tools, which become tool definitions at the model
  boundary.
- Structured outputs through the Responses `text` format field.
- Sessions that replay `result.to_input_list()` and SDK-managed local session
  history.
- `OpenAIResponsesCompactionSession` with `responses.compact`.
- Full Gemini Agents SDK probes against live upstream models.

### Known or likely gaps

- No SDK contract test suite in CI.
- `previous_response_id` is only safe when the upstream provider handles it
  natively. Chat-translated providers now return a clear compatibility error;
  local expansion from GoModel's stored Responses state is still not
  implemented.
- The Chat-to-Responses stream converter emits the core events needed for text
  and function calls, but the event sequence has not been validated against the
  current Agents SDK parsers.
- Websocket Responses transport is unsupported. The SDK can use HTTP/SSE, but
  `use_responses_websocket=True` needs a websocket-compatible `/responses`
  endpoint.
- Built-in Responses tools such as web search, file search, computer use, and
  tool search are only safe when the selected upstream provider natively
  supports those tool payloads. Chat-translated providers now reject hosted tool
  payloads instead of assuming provider compatibility.
- Anthropic rejects translated `response_format` and `verbosity` fields because
  there is no safe native mapping today.
- Prompt-managed flows and deferred tool loading need validation, especially
  when the SDK omits `model` because the prompt owns model selection.
- Tracing uploads go to OpenAI by default in the SDK. Users without an OpenAI
  Platform key need docs to disable tracing or configure a separate tracing
  processor/key.
- Python Agents SDK users must enable model ID pass-through on `MultiProvider`
  when sending GoModel namespaced model IDs such as `anthropic/...` or
  `gemini/...`; otherwise the SDK rejects unknown provider prefixes before
  calling GoModel.

## Implementation checklist

### P0: Prove basic SDK compatibility

- Done: add `docs/guides/openai-agents-sdk.mdx`.
  - Python example using `AsyncOpenAI(base_url="http://localhost:8080/v1",
    api_key="$GOMODEL_MASTER_KEY")`.
  - Python example using `OpenAIProvider` / `RunConfig`.
  - JavaScript example using an OpenAI provider pointed at GoModel.
  - Mention that tracing must be disabled or configured with a real OpenAI
    Platform key.
  - Mention that HTTP/SSE Responses is the supported path; websocket transport
    is not supported yet.
- Done: add a small runnable smoke test example under
  `docs/examples/openai-agents-sdk/`.
  - Text-only `Runner.run`.
  - Streaming `Runner.run_streamed`.
  - One local function tool.
- Still needed: add CI or manual contract tests that boot GoModel against the existing mock
  provider and run the smoke examples.

### P0: Preserve Responses items exactly

- Done: change Responses input decoding so unknown item types keep their original raw
  JSON shape.
- Keep typed conversion for known item types:
  - `message`
  - `function_call`
  - `function_call_output`
- Partially done: add tests for raw round-trip preservation of newer item types:
  - `reasoning`
  - `web_search_call`
  - `file_search_call`
  - `computer_call`
  - `mcp_call`
  - any item with `provider_data`
- Done: ensure native OpenAI-compatible providers receive those items unchanged.
- Done: ensure Chat-translated providers return a clear error or intentionally strip
  unsupported item types instead of sending malformed messages upstream.

### P0: Respect `store: false`

- Done: add a typed `Store *bool` field to `core.ResponsesRequest`.
- Done: when `store == false`, do not persist GoModel's local response snapshot by
  default.
- Add a config option only if operators need to override this for audit or
  debugging.
- Document the behavior in the Responses API guide and Agents SDK guide.

### P1: Add typed SDK request fields

Done: add typed fields to `core.ResponsesRequest` for fields the Agents SDK sends
regularly, while still preserving unknown fields:

- `PreviousResponseID string`
- `Conversation *ResponsesConversationRef`
- `Include []string`
- `Prompt any`
- `TopP *float64`
- `Truncation string`
- `Text any`
- `Store *bool`
- `PromptCacheRetention string`
- `ContextManagement any`
- `TopLogprobs *int`
- `User string`
- `ServiceTier string`
- `SafetyIdentifier string`

Use these fields for cache keys, audit summaries, compatibility decisions, and
provider-specific adaptation where relevant.

### P1: Make stateful Responses modes explicit

- Done: implement `/v1/conversations` lifecycle support.
- Done: reject `previous_response_id` and `conversation` on chat-translated
  providers with a clear compatibility error.
- Done: translate `text.format` to the Chat Completions `response_format`
  (`json_schema` / `json_object`) and pass `text.verbosity` through on
  chat-translated providers that support those fields; unknown text formats
  still return a clear error. Anthropic rejects these fields explicitly.
- Still needed: optionally expand previous stored responses into full input for
  chat-translated providers.
- Add tests for:
  - `result.to_input_list()` / local session replay
  - `previous_response_id` with native OpenAI provider
  - `previous_response_id` with a chat-translated provider
  - `conversation_id` unsupported behavior until conversations are implemented

### P1: Validate streaming against the SDK

- Run Python and JavaScript Agents SDK streaming clients against GoModel.
- Compare GoModel's chat-translated stream with native OpenAI Responses SSE
  ordering.
- Add missing stream events if the SDK requires them:
  - `response.content_part.added`
  - `response.output_text.done`
  - `response.content_part.done`
  - terminal `response.failed` / `response.incomplete` propagation
- Verify usage appears on the final SDK result for both native and
  chat-translated streams.

### P2: Feature capability gating

- Partially done: chat-translated providers reject hosted OpenAI Responses tools
  until a provider-specific capability mapping exists.
- Still needed: add model/provider capability metadata for Responses features:
  - function tools
  - structured outputs through `text.format`
  - multimodal input
  - web search
  - file search
  - computer use
  - tool search / deferred tool loading
  - response compaction
  - response lifecycle retrieval
  - conversations
  - websocket Responses transport
- Use the metadata to reject unsupported SDK requests early with clear
  OpenAI-compatible errors.
- Surface capability notes in `/v1/models` metadata and docs.

### P2: Subscription and harness compatibility

- Keep this separate from OpenAI Agents SDK support.
- Document that GoModel's normal path uses gateway credentials plus upstream
  provider API keys, not ChatGPT, Copilot, or Claude subscription credentials.
- Treat subscription-backed harness support as a separate compliance and product
  investigation before implementation.

## Suggested public claim

Until the P0 work is done:

> GoModel supports the OpenAI-compatible Responses API used by the OpenAI Agents
> SDK for basic HTTP flows, and full Agents SDK compatibility is being
> validated.

After P0:

> GoModel supports the OpenAI Agents SDK over HTTP Responses for text,
> streaming, function tools, and SDK-managed local sessions. Provider-native
> features such as hosted tools, conversations, and websocket transport depend
> on the selected upstream provider.
