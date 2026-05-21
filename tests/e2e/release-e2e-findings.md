# Release E2E Findings

Run date: 2026-05-21
Branch: `main` @ `72a0e68` (after pulling `feat(server): add Anthropic
Messages API ingress endpoint` #343 and `fix(providers): support OpenRouter
virtual model IDs` #344).

## Scope

1. Ran the full existing release E2E matrix (`S01`–`S95`).
2. Added 14 new scenarios (`S96`–`S109`) covering the new Anthropic Messages
   API ingress endpoints (`POST /v1/messages`, `POST /v1/messages/count_tokens`)
   and ran them.

The runner only parsed 2-digit scenario IDs (`/^### S[0-9][0-9] /`). It was
widened to `/^### S[0-9][0-9]+ /` in `run-release-e2e.sh` so `S96`–`S109` (and
any future 3-digit IDs) parse. No other runner behavior changed.

## Stack

Dedicated release stack on ports 18080-18084 via
`tests/e2e/manage-release-e2e-stack.sh start`, backed by Docker
redis/postgres/mongodb. All 5 gateways reported `health=ok`.

## Result summary

| Suite | Scenarios | Passed | Failed |
|-------|-----------|--------|--------|
| Existing matrix `S01`-`S95` | 95 | 94 | 1 (`S91`, see below) |
| New Messages API `S96`-`S109` | 14 | 13 | 1 (`S102`, real bug) |
| **Total** | **109** | **107** | **2** |

`S91` is a test-host flake (passes on a clean re-run). `S102` caught a real
product bug, which has since been fixed in this branch — after the fix, the
**full 109/109 matrix passes**.

## S91 — flaky, not a product bug

`S91 Live preview heartbeat from an idle subscriber` failed on the first run
with `curl: (28) Operation timed out after 801269 milliseconds` even though the
curl had `--max-time 20`. The host went to sleep mid-scenario, suspending the
process for ~13 minutes; the SSE connection was dead on resume.

Re-running `S91` under `caffeinate -i` passed in 21s. **No product issue** — a
test-host environment artifact. Recommendation: run the release matrix under
`caffeinate -i` to avoid idle-sleep flakes.

## S102 — REAL BUG (FIXED): `stop_reason` wrong for tool-use responses on `/v1/messages`

**Status: fixed in this branch.** `stopReasonFromFinish` now returns `"tool_use"`
whenever the response carries tool calls, before the `finish_reason` switch.
`TestFromChatResponseStopReasons` gained `stop_with_tool_calls` and
`empty_with_tool_calls` cases. `S102` re-run against the rebuilt binary returns
`stop_reason: "tool_use"` and passes. Details below.


`S102 Forced tool use round-trip` failed. The response correctly contains a
`tool_use` content block, but `stop_reason` is `"end_turn"` instead of
`"tool_use"`:

```json
{
  "type": "message",
  "stop_reason": "end_turn",
  "content": [
    { "type": "tool_use", "id": "call_…", "name": "get_weather",
      "input": { "city": "Paris" } }
  ]
}
```

### Root cause

`internal/anthropicapi/response.go` → `stopReasonFromFinish(finish, hasToolCalls)`
only consults `hasToolCalls` in the `case ""` branch:

```go
case "stop":
    return "end_turn"
...
case "":
    if hasToolCalls {
        return "tool_use"
    }
```

When a tool is forced via `tool_choice`, OpenAI (`gpt-4.1-nano`) returns
`finish_reason: "stop"` **with** a non-empty `tool_calls` list — confirmed with a
direct `/v1/chat/completions` call:

```json
{ "finish_reason": "stop", "has_tool_calls": 1 }
```

`stopReasonFromFinish` then takes the `case "stop"` branch and returns
`"end_turn"`, ignoring the tool calls.

### Impact

Anthropic's Messages API contract guarantees `stop_reason: "tool_use"` whenever
the response contains `tool_use` blocks. Anthropic-compatible clients (including
the Anthropic SDK tool-use loop) branch on `stop_reason == "tool_use"` to decide
whether to execute a tool and continue the conversation. With `"end_turn"`, such
a client treats the turn as finished and **never executes the tool** — agentic
tool-use loops through `/v1/messages` break whenever the upstream provider
reports `finish_reason: "stop"` alongside tool calls (the common case for
OpenAI-family providers with a forced `tool_choice`).

### Fix applied

`stop_reason` is `"tool_use"` whenever the response carries tool calls,
regardless of `finish_reason`. `stopReasonFromFinish` now checks `hasToolCalls`
first:

```go
func stopReasonFromFinish(finish string, hasToolCalls bool) string {
    if hasToolCalls {
        return "tool_use"
    }
    switch finish { ... }
}
```

### Test-coverage gap (closed)

`internal/anthropicapi/response_test.go` `TestFromChatResponseStopReasons`
previously exercised each `finish_reason` in isolation but never the
combination `finish_reason: "stop"` **+** non-empty tool calls — the exact case
that failed. Rows `stop_with_tool_calls` and `empty_with_tool_calls` were added.

## New scenarios `S96`-`S109` (Messages API ingress)

All pass except `S102`. Notable confirmations:

- `S96` — native Anthropic model: correct envelope, `id` normalized to `msg_…`,
  Anthropic-style `usage.input_tokens` / `output_tokens`.
- `S97` — provider-agnostic translation: an OpenAI model served through
  `/v1/messages` returns a valid Anthropic envelope (`id` becomes
  `msg_chatcmpl-…`, `model` is the resolved `gpt-4.1-nano-2025-04-14`).
- `S98` — streaming emits the Anthropic SSE event sequence
  (`message_start` → `content_block_start` → `content_block_delta`/`text_delta`
  → `message_delta` → `message_stop`).
- `S99` — polymorphic `system` as a text-block array is honored.
- `S100` — multi-turn conversation with a prior `assistant` turn.
- `S101` — `/v1/messages/count_tokens` returns a positive heuristic estimate.
- `S103` — multimodal image (`source.type: "url"`) input works.
- `S104` — alias resolution applies to `/v1/messages`.
- `S105`-`S108` — error paths return the Anthropic error envelope
  (`{"type":"error","error":{"type":"invalid_request_error",…}}`) with HTTP 400:
  missing `max_tokens`, empty `messages`, unknown model, and unsupported content
  block type (`document`).
- `S109` — `/v1/messages` requests are recorded in the audit log under the
  `/v1/messages` path and are searchable by request ID.

## Recommendations

1. The `S102` `stop_reason` bug is fixed in this branch — ship the fix with the
   `/v1/messages` feature, since it breaks tool-use loops for OpenAI-family
   providers.
2. Run the release matrix under `caffeinate -i` on laptops to avoid idle-sleep
   flakes like the first `S91` run.
