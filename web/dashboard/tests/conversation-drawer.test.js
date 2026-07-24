// Ported from internal/admin/dashboard/static/js/modules/conversation-drawer.test.cjs
// against the extracted pure logic in src/pages/audit-logs/conversation-helpers.js
// (message shaping) and live-logs-logic.js (the live-pending decision that
// drives openConversation's live-vs-persisted branch). DOM/fetch-flow cases
// stay in the Svelte store and are not duplicated here.

import test from "node:test";
import assert from "node:assert/strict";

import {
  buildConversationMessages,
  canShowConversation,
  extractConversationErrorMessage,
  functionExpandedContent,
} from "../src/pages/audit-logs/conversation-helpers.js";
import { liveLogsMethods } from "../src/pages/audit-logs/live-logs-logic.js";

const live = liveLogsMethods();

function liveEntry(overrides = {}) {
  return {
    id: "audit-1",
    _live: true,
    _live_pending: true,
    _live_state: "audit.updated",
    path: "/v1/chat/completions",
    timestamp: "2026-07-06T12:00:00Z",
    data: {
      request_body: { messages: [{ role: "user", content: "Hi" }] },
    },
    ...overrides,
  };
}

test("live entries render locally: pending live state skips the persisted fetch", () => {
  // openConversation branches on auditEntryLiveDetailPending: a live entry
  // still streaming renders from preview data; flushed/persisted entries
  // hydrate from /admin/audit/conversation.
  assert.equal(live.auditEntryLiveDetailPending(liveEntry()), true);
  assert.equal(live.auditEntryLiveDetailPending(liveEntry({ _live_state: "audit.failed" })), true);
  assert.equal(live.auditEntryLiveDetailPending(liveEntry({
    _live_state: "audit.flushed",
    _live_pending: false,
    _audit_flushed: true,
  })), false);
  assert.equal(live.auditEntryLiveDetailPending({ id: "audit-2", path: "/v1/chat/completions" }), false);

  const entry = liveEntry();
  const messages = buildConversationMessages([entry], entry.id);
  assert.equal(messages.length, 1);
  assert.equal(messages[0].text, "Hi");
  assert.equal(messages[0].role, "user");
  assert.equal(messages[0].isAnchor, true);
});

test("streaming chunks re-render as partial assistant messages", () => {
  const streamed = liveEntry({
    _live_state: "audit.stream",
    _response_partial: true,
    data: {
      request_body: { messages: [{ role: "user", content: "Hi" }] },
      response_body: { choices: [{ index: 0, message: { role: "assistant", content: "Par" } }] },
    },
  });

  const messages = buildConversationMessages([streamed], streamed.id);
  assert.equal(messages.length, 2);
  assert.equal(messages[1].text, "Par");
  assert.equal(messages[1].role, "assistant");

  // The spinner logic: a streaming state is not settled, completed is.
  assert.equal(live.liveAuditStateSettled("audit.stream"), false);
  assert.equal(live.liveAuditStateSettled("audit.completed"), true);
});

test("chat-completions threads shape system, tool and assistant turns", () => {
  const entry = {
    id: "log-1",
    timestamp: "2026-07-06T12:00:00Z",
    path: "/v1/chat/completions",
    data: {
      request_body: {
        messages: [
          { role: "system", content: "You are helpful." },
          { role: "user", content: "What is the weather?" },
          {
            role: "assistant",
            content: "",
            tool_calls: [{ id: "call-1", function: { name: "get_weather", arguments: '{"city":"Paris"}' } }],
          },
          { role: "tool", tool_call_id: "call-1", content: "sunny" },
        ],
      },
      response_body: {
        choices: [{ message: { role: "assistant", content: "It is sunny." } }],
      },
    },
  };

  const messages = buildConversationMessages([entry], "log-1");
  assert.deepEqual(messages.map((m) => m.role), [
    "system",
    "user",
    "assistant",
    "function_result",
    "assistant",
  ]);
  assert.equal(messages[0].roleLabel, "System Prompt");
  assert.equal(messages[2].toolCalls[0].name, "get_weather");
  // The tool result resolves its function name through the call-id map.
  assert.equal(messages[3].functionName, "get_weather");
  assert.equal(messages[3].text, "sunny");
  assert.equal(messages[4].text, "It is sunny.");
  assert.ok(messages.every((m) => m.isAnchor));
  assert.ok(messages.every((m, i) => m.uid === "log-1-" + (i + 1)));
});

test("responses-API threads shape input items, function calls and outputs", () => {
  const entry = {
    id: "log-2",
    timestamp: "2026-07-06T12:00:00Z",
    path: "/v1/responses",
    data: {
      request_body: {
        instructions: "Be terse.",
        input: [
          { role: "user", content: "Ping" },
          { type: "function_call", call_id: "call-9", name: "lookup", arguments: '{"q":"x"}' },
          { type: "function_call_output", call_id: "call-9", output: "42" },
        ],
      },
      response_body: {
        output: [
          { type: "message", role: "assistant", content: [{ type: "output_text", text: "Pong" }] },
          { type: "function_call", name: "lookup", arguments: '{"q":"y"}' },
        ],
      },
    },
  };

  const messages = buildConversationMessages([entry], "other-log");
  assert.deepEqual(messages.map((m) => m.role), [
    "system",
    "user",
    "function_call",
    "function_result",
    "assistant",
    "function_call",
  ]);
  assert.equal(messages[0].text, "Be terse.");
  assert.equal(messages[3].functionName, "lookup");
  assert.equal(messages[3].text, "42");
  assert.equal(messages[4].text, "Pong");
  assert.ok(messages.every((m) => m.isAnchor === false));
});

test("entries with errors append an error message extracted from nested payloads", () => {
  const entry = {
    id: "log-3",
    timestamp: "2026-07-06T12:00:00Z",
    path: "/v1/chat/completions",
    data: {
      request_body: { messages: [{ role: "user", content: "Hi" }] },
      error_message: JSON.stringify({
        error: { message: JSON.stringify({ error: { message: "model overloaded" } }) },
      }),
    },
  };

  assert.equal(extractConversationErrorMessage(entry), "model overloaded");

  const messages = buildConversationMessages([entry], "log-3");
  assert.equal(messages.length, 2);
  assert.equal(messages[1].role, "error");
  assert.equal(messages[1].roleLabel, "Error");
  assert.equal(messages[1].text, "model overloaded");
});

test("canShowConversation gates by path and payload", () => {
  assert.equal(canShowConversation(null), false);
  assert.equal(canShowConversation({ path: "/v1/chat/completions" }), true);
  assert.equal(canShowConversation({ path: "/v1/responses?stream=true" }), true);
  assert.equal(canShowConversation({ path: "/v1/embeddings" }), false);
  assert.equal(canShowConversation({ path: "/v1/models" }), false);
  assert.equal(canShowConversation({
    path: "/v1/models",
    data: { request_body: { messages: [] } },
  }), true);
  assert.equal(canShowConversation({
    path: "/v1/models",
    data: { response_body: { choices: [] } },
  }), true);
});

test("functionExpandedContent pretty-prints function call arguments", () => {
  const msg = {
    role: "function_call",
    toolCalls: [
      { name: "lookup", arguments: '{"q":"x"}' },
      { name: "raw", arguments: "not-json" },
    ],
  };
  assert.equal(
    functionExpandedContent(msg),
    'lookup({\n  "q": "x"\n})\n\nraw(not-json)',
  );
  assert.equal(functionExpandedContent({ role: "function_result", text: "42" }), "42");
});

test("messages sort by entry timestamp across a multi-entry thread", () => {
  const first = {
    id: "log-a",
    timestamp: "2026-07-06T11:00:00Z",
    data: { request_body: { messages: [{ role: "user", content: "one" }] } },
  };
  const second = {
    id: "log-b",
    timestamp: "2026-07-06T12:00:00Z",
    data: { request_body: { messages: [{ role: "user", content: "two" }] } },
  };

  const messages = buildConversationMessages([second, first], "log-b");
  assert.deepEqual(messages.map((m) => m.text), ["one", "two"]);
  assert.deepEqual(messages.map((m) => m.isAnchor), [false, true]);
});
