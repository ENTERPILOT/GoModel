# Claude Agent SDK Support

Status checked: 2026-05-22

## Short answer

GoModel is probably already close to supporting Anthropic's Agent SDK through
Anthropic passthrough.

The supported path should be:

```bash
export ANTHROPIC_BASE_URL=http://localhost:8080/p/anthropic
export ANTHROPIC_AUTH_TOKEN=$GOMODEL_MASTER_KEY
```

With GoModel configured with its own upstream `ANTHROPIC_API_KEY`, the SDK's
Anthropic Messages calls should flow as:

```text
Claude Agent SDK -> /p/anthropic/v1/messages -> GoModel -> Anthropic /v1/messages
```

This should work because the Agent SDK is built on Claude Code, and Anthropic's
gateway requirements for Claude Code are exactly the native Messages endpoints
GoModel can expose through passthrough:

- `POST /v1/messages`
- `POST /v1/messages/count_tokens`
- forwarding `anthropic-beta` and `anthropic-version`

It is not yet safe to market this as full Claude Agent SDK support. GoModel has
not been validated against the current Python and TypeScript SDKs, and the
managed `/v1/messages` route is only a portable subset of Anthropic's native
Messages API. For full SDK compatibility, passthrough should remain the primary
path.

## What the SDK expects

The current Claude Agent SDK packages are:

- Python: `claude-agent-sdk`
- TypeScript: `@anthropic-ai/claude-agent-sdk`

The SDK runs the same agent loop and tool runtime used by Claude Code. It can
read and edit files, run shell commands, search the web, call MCP tools, use
subagents, apply hooks, and maintain sessions. At the model boundary that means
GoModel should expect normal Claude Code-style Anthropic traffic rather than a
small single-turn client request.

The gateway-facing requirements are:

- Anthropic-compatible base URL configured with `ANTHROPIC_BASE_URL`.
- Gateway auth through `ANTHROPIC_AUTH_TOKEN` or equivalent SDK environment.
- Native Messages request and response shapes.
- Native Messages SSE event streams.
- Native `count_tokens` behavior for context budgeting.
- Forwarded `anthropic-beta` and `anthropic-version` headers.
- Preserved Claude Code attribution headers:
  - `X-Claude-Code-Session-Id`
  - `X-Claude-Code-Agent-Id`
  - `X-Claude-Code-Parent-Agent-Id`
- Long-lived requests. The Agent SDK defaults allow long API calls and retries,
  and tool loops can run for much longer than a normal chat completion.

Subscription-backed usage is a separate topic. Anthropic's docs say Agent SDK
and `claude -p` usage on subscription plans will draw from a separate monthly
Agent SDK credit starting 2026-06-15. They also state that third-party products
should use the API-key authentication methods unless previously approved.
GoModel's normal gateway path should therefore stay API-key backed unless there
is a separate compliance and product decision to support subscription-backed
harnesses.

## Current support assessment

### Supported now

- GoModel already has a Claude Code guide using Anthropic passthrough:
  `ANTHROPIC_BASE_URL=http://localhost:8080/p/anthropic`.
- Anthropic passthrough is enabled by default.
- `/p/anthropic/v1/...` is normalized to the Anthropic provider's native path.
  This should cover `/v1/messages`, `/v1/messages/count_tokens`, and
  `/v1/models`.
- Passthrough strips client `Authorization` and `X-Api-Key`, then applies the
  server-side upstream Anthropic credential.
- Passthrough forwards normal request headers that the SDK needs, including
  `anthropic-beta`, `anthropic-version`, and `X-Claude-Code-*`.
- Passthrough SSE responses are streamed without body translation.
- GoModel classifies `/p/...` as a model interaction route and clears the
  per-request write deadline, so long streams are not constrained by the
  server-wide 30 second write timeout.
- The managed Anthropic Messages ingress exists at:
  - `POST /v1/messages`
  - `POST /v1/messages/count_tokens`
- The managed route supports text, images, custom tools, `tool_choice`, basic
  thinking output, Anthropic-style non-streaming responses, and Anthropic-style
  SSE conversion.

### Needs validation

- Python SDK `query(...)` pointed at GoModel passthrough.
- Python SDK `ClaudeSDKClient` pointed at GoModel passthrough.
- TypeScript SDK `query(...)` pointed at GoModel passthrough.
- Text-only agent runs.
- Streaming agent runs.
- Built-in file tools:
  - `Read`
  - `Write`
  - `Edit`
  - `Glob`
  - `Grep`
- `Bash` tool calls and command-heavy sessions.
- `WebSearch` and `WebFetch`.
- SDK MCP servers and SDK-created MCP tools.
- Subagents, including the `X-Claude-Code-Agent-Id` and
  `X-Claude-Code-Parent-Agent-Id` headers.
- Hooks and permission callbacks.
- Session resume and continuation.
- Structured output.
- Large contexts and request bodies against GoModel's default body-size limit.
- Long-running streams against GoModel, proxies, and load balancers.
- Gateway model discovery with `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1`.
- Usage and cost extraction from passthrough streams.
- Native Anthropic error bodies as seen by the SDK.

### Known or likely gaps

- No first-class Claude Agent SDK guide.
- No SDK smoke examples in the repository.
- No contract tests against `claude-agent-sdk` or
  `@anthropic-ai/claude-agent-sdk`.
- The existing Claude Code guide recommends
  `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1`. Full Agent SDK support should
  validate whether this workaround is still needed with Anthropic passthrough.
- Managed `/v1/messages/count_tokens` is heuristic, not tokenizer-exact. This
  is risky for SDK context budgeting; native passthrough should be used when the
  SDK needs exact Anthropic behavior.
- Managed `/v1/messages` drops or rejects several native Anthropic features:
  - `cache_control` prompt-cache breakpoints
  - input `thinking` and extended-thinking signatures
  - server/built-in tools
  - `top_k`
  - `document` and other non-text/image blocks
- Managed `/v1/messages` can route to non-Anthropic providers, so it cannot
  guarantee Anthropic-native behavior unless capabilities are explicitly gated.
- Passthrough error handling currently normalizes provider errors through
  GoModel's error path. Verify that the body and status are compatible with the
  SDK's Anthropic error parser.
- Passthrough audit and usage observers see SDK traffic, but subagent/session
  attribution from `X-Claude-Code-*` headers is not yet surfaced as a first-class
  reporting dimension.

## Implementation checklist

### P0: Prove passthrough SDK compatibility

- Add `docs/guides/claude-agent-sdk.mdx`.
  - Show `ANTHROPIC_BASE_URL=http://localhost:8080/p/anthropic`.
  - Show `ANTHROPIC_AUTH_TOKEN=$GOMODEL_MASTER_KEY`.
  - Explain that GoModel still needs an upstream `ANTHROPIC_API_KEY`.
  - Explain API-key-backed gateway usage separately from Claude plan
    subscription-backed usage.
  - Recommend passthrough as the SDK compatibility path.
  - Document that managed `/v1/messages` is a portable subset, not full SDK
    compatibility.
- Add runnable examples under `examples/claude-agent-sdk/`.
  - Python `query(...)` text-only example.
  - Python `ClaudeSDKClient` streaming example.
  - TypeScript `query(...)` text-only example.
  - A low-risk tool example using `Read`, `Glob`, and `Grep`.
- Add manual or CI smoke tests that boot GoModel and run both SDKs against the
  passthrough base URL.
- Verify these endpoints with the SDK:
  - `POST /p/anthropic/v1/messages`
  - `POST /p/anthropic/v1/messages/count_tokens`
  - `GET /p/anthropic/v1/models`
- Test with and without `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1`, then update
  the Claude Code guide with the current recommendation.

### P0: Make passthrough fidelity explicit

- Add tests that Anthropic passthrough forwards:
  - `anthropic-beta`
  - `anthropic-version`
  - `X-Claude-Code-Session-Id`
  - `X-Claude-Code-Agent-Id`
  - `X-Claude-Code-Parent-Agent-Id`
- Add tests that passthrough strips client auth headers and replaces them with
  GoModel's configured upstream Anthropic credential.
- Add SSE passthrough tests with Anthropic event names:
  - `message_start`
  - `content_block_start`
  - `content_block_delta`
  - `content_block_stop`
  - `message_delta`
  - `message_stop`
  - `ping`
  - `error`
- Verify passthrough error responses stay compatible with Anthropic SDK parsing.
- Verify streamed usage is captured for passthrough `/messages` responses.

### P1: Improve SDK observability

- Capture Claude Code session and agent headers into audit and usage metadata:
  - `X-Claude-Code-Session-Id`
  - `X-Claude-Code-Agent-Id`
  - `X-Claude-Code-Parent-Agent-Id`
- Add dashboard filters for SDK session ID and agent ID if the fields prove
  useful in real traffic.
- Decide whether User-Path can be derived from one of those headers by
  configuration, or whether users should keep sending an explicit
  `X-GoModel-User-Path` / managed-key user path.
- Document privacy implications: SDK traffic can contain source files, command
  output, tool results, and MCP data.

### P1: Validate advanced SDK features

- Run SDK examples that exercise MCP servers.
- Run SDK examples that exercise subagents and parent/child agent attribution.
- Run SDK examples that exercise session resume.
- Run SDK examples that exercise structured output.
- Run SDK examples that exercise permission callbacks and hooks.
- Confirm these features do not require endpoints beyond Anthropic Messages,
  `count_tokens`, and optional gateway model discovery.

### P1: Tighten managed `/v1/messages`

- Keep documenting passthrough as the full-fidelity path.
- If the selected provider is Anthropic, optionally support native
  `/v1/messages/count_tokens` instead of the heuristic estimate.
- Preserve or explicitly reject more Anthropic-native fields with clear errors:
  - `cache_control`
  - `thinking` signatures
  - `document`
  - server/built-in tool definitions
  - beta-specific fields
- Add capability metadata so non-Anthropic providers fail early for
  Anthropic-native SDK features instead of receiving malformed translated
  requests.

### P1: Validate long-running behavior

- Run a multi-turn SDK session that includes file reads, tool calls, and
  streaming output for at least 10 minutes.
- Verify request cancellation propagates cleanly to the upstream Anthropic
  request.
- Verify SDK retry behavior does not double-count usage in GoModel.
- Verify large file/context requests against `BODY_SIZE_LIMIT`.
- Document recommended proxy and load-balancer timeouts for SDK traffic.

### P2: Subscription-backed harness investigation

- Treat this separately from Agent SDK API support.
- Review Anthropic's current terms and gateway docs before implementation.
- Decide whether GoModel should support only API-key-backed Agent SDK traffic,
  or whether subscription-backed Claude Code / Agent SDK use is in scope.
- If it is in scope, design a separate auth flow rather than mixing Claude plan
  credentials into the existing `ANTHROPIC_API_KEY` provider configuration.

## Suggested public claim

Until the P0 work is done:

> GoModel supports Claude Code today and should work with the Claude Agent SDK
> through Anthropic passthrough. Full SDK compatibility is being validated.

After P0:

> GoModel supports the Claude Agent SDK through Anthropic Messages passthrough
> for text, streaming, basic built-in tool loops, and gateway model discovery.
> The managed `/v1/messages` endpoint supports a portable Anthropic Messages
> subset for cross-provider routing.

After P1 advanced validation:

> GoModel supports the Claude Agent SDK through Anthropic Messages passthrough
> for MCP, subagents, sessions, hooks, structured output, and long-running
> agent workflows.

## References

- Anthropic Claude Agent SDK overview:
  https://code.claude.com/docs/en/agent-sdk/overview
- Anthropic Claude Agent SDK quickstart:
  https://code.claude.com/docs/en/agent-sdk/quickstart
- Anthropic Claude Code LLM gateway requirements:
  https://code.claude.com/docs/en/llm-gateway
- GoModel Claude Code guide:
  `docs/guides/claude-code.mdx`
- GoModel Anthropic Messages API guide:
  `docs/advanced/anthropic-messages-api.mdx`
- GoModel passthrough guide:
  `docs/features/passthrough-api.mdx`
