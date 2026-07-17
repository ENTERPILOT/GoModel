# Anthropic SDK drop-in compatibility — test findings

Date: 2026-07-17 · Branch: `fix/anthropic-sdk` @ 23cdb251 · SDK: `anthropic` (Python) 0.117.0

Goal: verify GoModel's `/v1/messages` surface works as a **drop-in replacement** for the
Anthropic API when accessed through the official Anthropic SDK
(`anthropic.Anthropic(base_url=<gateway>)`).

Setup: gateway built from this branch, run locally with the repo `.env` credentials
(exact response cache disabled to keep probes deterministic). Test scripts live in the
session scratchpad (`sdk_compat_test.py`, `sdk_compat_phase2.py`) — 52 checks total.

Models exercised (cheap/free tiers): `openai/gpt-4o-mini`, `gemini/gemini-2.5-flash-lite`,
`groq/llama-3.1-8b-instant` (+ `llama-3.3-70b-versatile`), `deepseek/deepseek-v4-flash`,
`anthropic/claude-haiku-4-5-20251001`, plus `/p/anthropic` passthrough.

## Resolution status (2026-07-17, same branch)

| Finding | Status |
| --- | --- |
| F1 x-api-key auth | **Fixed** — auth middleware falls back to `x-api-key` when no `Authorization` header (`internal/server/auth.go`). `Anthropic(api_key=...)` now works on translated and passthrough routes. |
| F2 stop_sequence lost | **Fixed for providers that report it** — the anthropic provider parses native `stop_sequence` and carries it as a `stop_sequence` choice/delta extension (same pattern as `reasoning_content`); the Messages dialect maps it back to `stop_reason: "stop_sequence"` + the matched value, stream and non-stream. OpenAI-family providers structurally can't report it (finish_reason conflates); documented. |
| F3 message_start usage=0 | **Fixed (best effort)** — `message_start` now carries the chars/4 heuristic estimate; authoritative usage still lands in `message_delta`, which SDK accumulators prefer. |
| F4 models list shape | **Fixed** — `GET /v1/models` renders the Anthropic list shape (`type`, `display_name`, `created_at`, `has_more`/`first_id`/`last_id`) when the request carries the `anthropic-version` header Anthropic SDKs always send. |
| F5 non-canonical 404 | **Fixed** — unknown routes return the canonical error envelope, Anthropic-shaped for Anthropic-dialect callers (`e.RouteNotFound` + `handleRouteNotFound`). Messages batches API itself stays unimplemented, now documented. |
| F6 cache_control dropped | **Documented** (`docs/advanced/anthropic-messages-api.mdx`) — real propagation needs cache-breakpoint representation in the canonical type; use `/p/anthropic` for prompt caching. |
| F7 heuristic count_tokens | **Documented** — passthrough gives exact counts. |
| F8 unsigned thinking blocks | **Documented** — by design; replay against the gateway works. |

Retest after fixes: phase 1 — 32 PASS, 0 real findings (remaining two entries are the
documented OpenAI stop_sequence limitation and the documented batches gap, now with a
canonical 404 envelope). Phase 2 — all pass including x-api-key on passthrough,
`stop_reason: "stop_sequence"` + matched value on the Claude path (stream and
non-stream), and message_start input estimate.

## Verdict

The core Messages API contract works well across all five providers — non-streaming,
streaming (full event sequence), system prompts, multi-turn, tool use (forced choice,
roundtrip, parallel, streaming `input_json_delta`), vision, thinking, and canonical
Anthropic error envelopes. **One auth blocker and a handful of contract deviations
stand between "works" and "drop-in".**

## Findings

### F1 — SDK default auth (`x-api-key`) is rejected · **blocker**

`anthropic.Anthropic(api_key=...)` sends the key in the `x-api-key` header — that is the
SDK default and what every Anthropic code sample does. GoModel's auth middleware only
reads `Authorization: Bearer` (`internal/server/auth.go`), so the request fails with
401 `missing authorization header`. Same on `/p/anthropic/...` passthrough.

Workaround today: `anthropic.Anthropic(auth_token=...)` (sends `Authorization: Bearer`).
That is exactly the "edit your code" step a drop-in replacement is supposed to avoid.

Suggested fix: in the auth middleware, fall back to the `x-api-key` header when no
`Authorization` header is present (keep Bearer precedence).

### F2 — `stop_sequence` information is lost · bug

`stop_sequences` are **applied** correctly (forwarded as OpenAI `stop`; output stops at
the sequence), but the response always reports `stop_reason: "end_turn"` and
`stop_sequence: null`. The Anthropic contract is `stop_reason: "stop_sequence"` plus the
matched sequence. Reproduced on non-stream and stream, and **also on the Anthropic
provider itself** (claude → OpenAI dialect → claude loses the info because OpenAI's
`finish_reason: "stop"` conflates natural stop with stop-sequence stop).

Fully fixing this for OpenAI-family providers is impossible from `finish_reason` alone,
but a good heuristic exists: when the request carried `stop_sequences` and the reply text
would plausibly have continued, or — for the anthropic provider — by preserving the
native `stop_reason`/`stop_sequence` through the internal translation instead of
collapsing to `finish_reason: "stop"`.

### F3 — streaming `message_start` carries `usage.input_tokens: 0` · deviation

Anthropic reports real `input_tokens` in the `message_start` event; GoModel emits zeros
there and only reports usage in the final `message_delta`
(`internal/anthropicapi/stream.go: ensureStarted`). The SDK's
`get_final_message()` merges the `message_delta` usage, so SDK users see correct totals —
but clients that read usage from `message_start` (cost meters, some proxies) see 0.
Structural cause: the OpenAI upstream only delivers usage in the last chunk, so the
gateway can't know input tokens at stream start. Could be improved with the same
heuristic estimator used by `count_tokens`, or documented as a known deviation.

### F4 — `client.models.list()` returns OpenAI-shaped objects · deviation

`GET /v1/models` parses in the SDK (pagination works), but items lack the Anthropic
fields: `type: "model"`, `display_name`, `created_at` (RFC3339). SDK objects come back
with those attributes `None`. Display names exist in the catalog metadata already —
serving the Anthropic shape on this route when the client sends `anthropic-version`
(or on an `/v1/models` sibling for the Messages dialect) would close this.
Note: `/p/anthropic/v1/models` passthrough returns the genuine Anthropic shape and
works perfectly (10 models, `claude-sonnet-5` first).

### F5 — Batches API absent; unknown `/v1/*` routes return non-canonical 404 · gap

`client.messages.batches.*` hits `/v1/messages/batches` → 404 with echo's default body
`{"message": "Not Found"}`, not the Anthropic error envelope. The OpenAI-dialect
`/v1/batches` exists, but Anthropic SDK users can't reach it. Two separable items:
(a) Messages-dialect batches is unimplemented (fine to defer — document it);
(b) unknown-route 404s under `/v1/` could use the canonical error envelope so SDK
clients raise a clean typed error.

### F6 — `cache_control` accepted but silently dropped · limitation

`cache_control` markers on system/content blocks are tolerated (no 400 — good), but the
translation flattens system prompts to plain strings and drops the markers, so **prompt
caching never activates**, even when the request routes to the Anthropic provider.
Usage responses show no cache fields. Fine for correctness, costs money for heavy users.
Worth documenting; propagating breakpoints on the anthropic-provider path would be the
real fix.

### F7 — `count_tokens` is heuristic · documented, keep an eye on it

`/v1/messages/count_tokens` returns chars/4 (per ADR-0007) — e.g. 113 for a prompt the
real tokenizer counts ~90. Passthrough (`/p/anthropic/v1/messages/count_tokens`) returns
exact counts (verified: 9 tokens). Users doing budget math against the translated route
should be pointed at the passthrough.

### F8 — thinking blocks have no `signature` · minor

Translated responses surface reasoning as `thinking` blocks with `signature: null`
(real Anthropic thinking blocks are signed). The SDK tolerates it, and GoModel drops
incoming thinking blocks on replay (by design), so multi-turn works against the gateway.
Only a client that captures gateway output and replays it against api.anthropic.com
directly would break. Verified thinking+tool-use multi-turn roundtrip works.

## Postel-lenient behaviors (working as intended, no action)

- Non-alternating roles and assistant-first message arrays are accepted (Anthropic
  rejects both with 400). Generous-input by design.
- `top_k` silently dropped (documented in ADR-0007 — would 400 on OpenAI-family
  providers if forwarded).
- `document` blocks and server tools (`web_search_*`, …) rejected with a clear 400
  invalid_request_error pointing at the `/p/anthropic` passthrough. Good DX.
- `metadata.user_id` mapped to OpenAI `user`; `temperature`/`top_p` forwarded.

## What passed (highlights)

- **Response shape**: `msg_` ids, `type/role/content/stop_reason/usage` correct on all
  5 providers; `max_tokens` truncation → `stop_reason: "max_tokens"`.
- **Streaming**: full canonical event sequence (`message_start` → `content_block_start/
  delta/stop` → `message_delta` → `message_stop`) on all providers; SDK accumulation and
  `get_final_message()` work; text, thinking (`thinking_delta`), and tool
  (`input_json_delta`) block types all stream correctly.
- **Tools**: forced `tool_choice`, `any`, `none`, `disable_parallel_tool_use`, parallel
  calls, tool_result as string / block list / `is_error`, full roundtrips on openai,
  gemini, groq(70B), anthropic.
- **Thinking**: `budget_tokens` and `adaptive` accepted; deepseek's native reasoning is
  correctly surfaced as Anthropic `thinking` blocks — a nice bonus the real Anthropic
  SDK ecosystem understands.
- **Vision**: base64 and URL image sources (URL failures upstream relay as clean 400s).
- **Errors**: 400/401/404 all carry the canonical `{"type":"error","error":{...}}`
  envelope and raise the right typed SDK exceptions (`BadRequestError`,
  `AuthenticationError`, `NotFoundError`). Unknown model → 404 `not_found_error`. ✔
- **Passthrough `/p/anthropic`**: basic, streaming, exact `count_tokens`, `models.list`
  all work (auth aside, see F1).

## Provider quirks observed (not gateway bugs)

- `groq/llama-3.1-8b-instant` fails forced tool calls with provider-side
  `tool call validation failed` (relayed correctly as 400). `llama-3.3-70b-versatile`
  works through the identical path.
- OpenAI refuses to download some image URLs (e.g. Wikimedia SVG thumbs) — relayed
  cleanly as `invalid_request_error`.

## Suggested priority

1. **F1** x-api-key auth — the single change that makes "point your SDK at GoModel" true.
2. **F2** stop_sequence preservation (at minimum on the anthropic provider path).
3. **F4** Anthropic-shaped model listing / **F3** message_start usage — nice-to-have parity.
4. **F5b** canonical 404 envelope under `/v1/`.
5. Document F6/F7 in the README (caching + token counting expectations).
