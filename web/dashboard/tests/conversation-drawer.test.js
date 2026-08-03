import test from "node:test";
import assert from "node:assert/strict";

import {
  buildConversationMessages,
  buildConversationView,
  buildFollowUpHeaders,
  buildFollowUpRequest,
  canShowConversation,
  conversationEntryIsLatest,
  conversationFollowUpEntry,
  extractConversationErrorMessage,
  functionExpandedContent,
  followUpEndpointKind,
  interactionParentID,
  latestConversationEntry,
  latestRenderableConversationEntry,
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
  assert.equal(canShowConversation({ path: "/v1/messages" }), true);
  assert.equal(canShowConversation({ path: "/messages" }), true);
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

test("the latest request snapshot is displayed once in timestamp order", () => {
  const first = {
    id: "log-a",
    timestamp: "2026-07-06T11:00:00Z",
    data: { request_body: { messages: [{ role: "user", content: "one" }] } },
  };
  const second = {
    id: "log-b",
    timestamp: "2026-07-06T12:00:00Z",
    data: { request_body: { messages: [
      { role: "user", content: "one" },
      { role: "user", content: "two" },
    ] } },
  };

  const messages = buildConversationMessages([second, first], "log-b");
  assert.deepEqual(messages.map((m) => m.text), ["one", "two"]);
  assert.deepEqual(messages.map((m) => m.isAnchor), [false, true]);
});

test("a changed historical message replaces the older snapshot instead of repeating it", () => {
  const first = {
    id: "log-a",
    timestamp: "2026-07-06T11:00:00Z",
    data: {
      request_body: { messages: [{ role: "user", content: "one" }] },
      response_body: { choices: [{ message: { role: "assistant", content: "draft answer" } }] },
    },
  };
  const second = {
    id: "log-b",
    timestamp: "2026-07-06T12:00:00Z",
    data: {
      request_body: { messages: [
        { role: "user", content: "one" },
        { role: "assistant", content: "normalized answer" },
        { role: "user", content: "two" },
      ] },
      response_body: { choices: [{ message: { role: "assistant", content: "latest answer" } }] },
    },
  };

  const messages = buildConversationMessages([second, first], "log-a");
  assert.deepEqual(messages.map((m) => m.text), [
    "one",
    "normalized answer",
    "two",
    "latest answer",
  ]);
  assert.equal(messages.filter((m) => m.text === "one").length, 1);
  assert.equal(messages.some((m) => m.text === "draft answer"), false);
});

test("session transcripts collapse resent chat history and dim records after the anchor", () => {
  const first = {
    id: "log-1",
    timestamp: "2026-07-06T11:00:00Z",
    data: {
      request_body: { messages: [{ role: "user", content: "one" }] },
      response_body: { choices: [{ message: { role: "assistant", content: "first" } }] },
    },
  };
  const second = {
    id: "log-2",
    timestamp: "2026-07-06T12:00:00Z",
    data: {
      request_body: { messages: [
        { role: "user", content: "one" },
        { role: "assistant", content: "first" },
        { role: "user", content: "two" },
      ] },
      response_body: { choices: [{ message: { role: "assistant", content: "second" } }] },
    },
  };

  const messages = buildConversationMessages([second, first], "log-1");
  assert.deepEqual(messages.map((m) => m.text), ["one", "first", "two", "second"]);
  assert.deepEqual(messages.map((m) => m.isAfterAnchor), [false, false, true, true]);
});

test("a divergent later snapshot does not replace or dim the selected conversation", () => {
  const titleRequest = {
    id: "title-log",
    timestamp: "2026-08-03T11:10:29.500441Z",
    data: {
      request_body: { messages: [
        { role: "system", content: "You are a title generator." },
        { role: "user", content: "test" },
      ] },
      response_body: { choices: [{ message: { role: "assistant", content: "Quick test message" } }] },
    },
  };
  const mainRequest = {
    id: "main-log",
    timestamp: "2026-08-03T11:10:29.582520Z",
    data: {
      request_body: { messages: [
        { role: "system", content: "You are OpenCode." },
        { role: "user", content: "test" },
      ] },
      response_body: { choices: [{ message: { role: "assistant", content: "What do you want to test?" } }] },
    },
  };

  const view = buildConversationView([mainRequest, titleRequest], "title-log");
  assert.deepEqual(view.messages.map((m) => m.text), [
    "You are a title generator.",
    "test",
    "Quick test message",
  ]);
  assert.ok(view.messages.every((m) => m.isAfterAnchor === false));
  assert.deepEqual(view.entryIDs, ["title-log"]);
});

test("branch projection admits compatible snapshots and explicit parent links", () => {
  const anchor = {
    id: "log-1",
    timestamp: "2026-08-03T11:00:00Z",
    data: {
      request_body: { messages: [{ role: "user", content: [{ type: "image_url", image_url: { url: "one" } }] }] },
      response_body: { choices: [{ message: { role: "assistant", content: "first" } }] },
    },
  };
  const compatible = {
    id: "log-2",
    timestamp: "2026-08-03T11:01:00Z",
    data: { request_body: { messages: [
      { role: "user", content: [{ type: "image_url", image_url: { url: "one" } }] },
      { role: "assistant", content: "first" },
      { role: "user", content: "next" },
    ] } },
  };
  const linkedPending = {
    id: "log-3",
    timestamp: "2026-08-03T11:02:00Z",
    data: { request_headers: { "X-GoModel-Interaction-Parent": "log-2" } },
  };

  const view = buildConversationView([linkedPending, compatible, anchor], "log-1");
  assert.deepEqual(view.entryIDs, ["log-1", "log-2", "log-3"]);
  assert.equal(view.messages.some((message) => message.text === "next"), true);
});

test("unclassified live entries in the same session stay outside the selected branch", () => {
  const view = buildConversationView([
    {
      id: "anchor",
      timestamp: "2026-08-03T11:00:00Z",
      data: { request_body: { messages: [{ role: "user", content: "selected" }] } },
    },
    { id: "unknown-live", timestamp: "2026-08-03T11:01:00Z", _live: true, data: {} },
  ], "anchor");
  assert.deepEqual(view.entryIDs, ["anchor"]);
});

test("follow-up endpoint gate supports only chat, responses, and messages", () => {
  assert.equal(followUpEndpointKind("/v1/chat/completions?beta=1"), "chat");
  assert.equal(followUpEndpointKind("/responses"), "responses");
  assert.equal(followUpEndpointKind("/v1/messages/"), "messages");
  assert.equal(followUpEndpointKind("/v1/embeddings"), "");
  assert.equal(followUpEndpointKind("/custom/chat"), "");
});

test("follow-ups use the selected audit record instead of the latest session record", () => {
  const entries = [
    { id: "selected", timestamp: "2026-07-06T11:00:00Z", path: "/v1/responses" },
    { id: "latest", timestamp: "2026-07-06T12:00:00Z", path: "/v1/chat/completions" },
  ];

  assert.equal(conversationFollowUpEntry(entries, "selected"), entries[0]);
  assert.equal(conversationFollowUpEntry(entries, "missing"), null);
});

test("latest-selection detection enables follow-latest only for the newest record", () => {
  const entries = [
    { id: "newest", timestamp: "2026-07-06T12:00:00Z" },
    { id: "older", timestamp: "2026-07-06T11:00:00Z" },
  ];

  assert.equal(latestConversationEntry(entries), entries[0]);
  assert.equal(conversationEntryIsLatest(entries, "newest"), true);
  assert.equal(conversationEntryIsLatest(entries, "older"), false);
});

test("follow-latest waits for request data before moving to a classified live entry", () => {
  const anchor = {
    id: "selected",
    timestamp: "2026-08-03T11:00:00Z",
    data: { request_body: { messages: [{ role: "user", content: "selected history" }] } },
  };
  const unrelated = {
    id: "unrelated",
    timestamp: "2026-08-03T11:01:00Z",
    data: { request_body: { messages: [{ role: "user", content: "wrong history" }] } },
  };
  const pending = {
    id: "pending",
    timestamp: "2026-08-03T11:02:00Z",
    _live: true,
    data: { request_headers: { "X-GoModel-Interaction-Parent": "selected" } },
  };
  const entries = [pending, unrelated, anchor];
  const view = buildConversationView(entries, anchor.id);

  assert.deepEqual(view.entryIDs, ["selected", "pending"]);
  assert.deepEqual(view.messages.map((message) => message.text), ["selected history"]);
  assert.equal(latestRenderableConversationEntry(entries, view.entryIDs), anchor);

  pending.data.request_body = {
    messages: [
      { role: "user", content: "selected history" },
      { role: "user", content: "follow-up" },
    ],
  };
  assert.equal(latestRenderableConversationEntry(entries, view.entryIDs), pending);
});

test("chat and messages follow-ups append the assistant result and plain user text", () => {
  const chat = {
    path: "/v1/chat/completions",
    data: {
      request_body: { model: "gpt-4o", temperature: 0.2, messages: [{ role: "user", content: "Hi" }] },
      response_body: { choices: [{ message: { role: "assistant", content: "Hello" } }] },
    },
  };
  const chatBody = buildFollowUpRequest(chat, " Next ");
  assert.equal(chatBody.model, "gpt-4o");
  assert.equal(chatBody.temperature, 0.2);
  assert.deepEqual(chatBody.messages.map((m) => [m.role, m.content]), [
    ["user", "Hi"], ["assistant", "Hello"], ["user", "Next"],
  ]);

  const anthropic = {
    path: "/v1/messages",
    data: {
      request_body: { model: "claude", messages: [{ role: "user", content: "Hi" }] },
      response_body: { role: "assistant", content: [{ type: "text", text: "Hello" }] },
    },
  };
  const messagesBody = buildFollowUpRequest(anthropic, "Next");
  assert.deepEqual(messagesBody.messages[1], anthropic.data.response_body);
  assert.deepEqual(messagesBody.messages[2], { role: "user", content: "Next" });
  assert.deepEqual(buildConversationMessages([{ id: "a", timestamp: "2026-07-06T12:00:00Z", ...anthropic }], "a").map((m) => m.text), [
    "Hi", "Hello",
  ]);
});

test("responses follow-ups preserve options and chain from the latest response", () => {
  const entry = {
    path: "/v1/responses",
    data: {
      request_body: { model: "gpt-5", stream: true, input: "Hi" },
      response_body: { id: "resp_123", output: [] },
    },
  };
  assert.deepEqual(buildFollowUpRequest(entry, "Next"), {
    model: "gpt-5",
    stream: true,
    input: "Next",
    previous_response_id: "resp_123",
  });
  assert.equal(buildFollowUpRequest({
    path: "/v1/responses",
    data: { request_body: { input: "Hi", previous_response_id: "resp_old" } },
  }, "Next").previous_response_id, undefined);
});

test("follow-up headers preserve application context without replaying credentials", () => {
  const entry = {
    id: "log-2",
    session_id: "/team\0session-9",
    user_path: "/team",
    data: { request_headers: {
      Authorization: "[REDACTED]",
      "X-Session-Id": "session-9",
      "X-Custom": "keep",
      "X-Request-Id": "old-request",
      "Content-Encoding": "gzip",
      "Idempotency-Key": "old-operation",
    } },
  };
  const headers = buildFollowUpHeaders(entry, "log-1");
  assert.equal(headers.Authorization, undefined);
  assert.equal(headers["X-Request-Id"], undefined);
  assert.equal(headers["Content-Encoding"], undefined);
  assert.equal(headers["Idempotency-Key"], undefined);
  assert.equal(headers["X-Custom"], "keep");
  assert.equal(headers["X-Session-Id"], "session-9");
  assert.equal(headers["X-GoModel-User-Path"], undefined);
  assert.equal(headers["X-GoModel-Interaction-Parent"], "log-1");
  assert.equal(interactionParentID({ data: { request_headers: headers } }), "log-1");
});

test("follow-up headers do not change session scoping", () => {
  const rootSession = buildFollowUpHeaders({
    id: "root-log",
    session_id: "ses_038f24fd0ffepd013fh3piDcdV",
    user_path: "/",
    data: { request_headers: {
      "X-Session-Id": "ses_038f24fd0ffepd013fh3piDcdV",
    } },
  }, "root-log");
  assert.equal(rootSession["X-Session-Id"], "ses_038f24fd0ffepd013fh3piDcdV");
  assert.equal(rootSession["X-GoModel-User-Path"], undefined);

  const scopedSession = buildFollowUpHeaders({
    id: "scoped-log",
    session_id: "scoped-529bff5b6264795393a9fb1e1da35906",
    user_path: "/team",
    data: { request_headers: {} },
  }, "scoped-log");
  assert.equal(scopedSession["X-Session-Id"], undefined);
  assert.equal(scopedSession["X-GoModel-User-Path"], undefined);

  const autoSession = buildFollowUpHeaders({
    id: "auto-log",
    session_id: "auto-529bff5b6264795393a9fb1e1da35906",
    data: { request_headers: { "X-GoModel-User-Path": "/team" } },
  }, "auto-log");
  assert.equal(autoSession["X-Session-Id"], undefined);

  const capturedUserPath = buildFollowUpHeaders({
    id: "team-log",
    data: { request_headers: { "X-GoModel-User-Path": "/team" } },
  }, "team-log");
  assert.equal(capturedUserPath["X-GoModel-User-Path"], "/team");
});

test("follow-up headers replace a captured parent instead of duplicating it", () => {
  const headers = buildFollowUpHeaders({
    id: "log-2",
    data: { request_headers: { "X-Gomodel-Interaction-Parent": "log-1" } },
  }, "log-2");
  const parentNames = Object.keys(headers).filter((name) =>
    name.toLowerCase() === "x-gomodel-interaction-parent");
  assert.deepEqual(parentNames, ["X-GoModel-Interaction-Parent"]);
  assert.equal(new Headers(headers).get("x-gomodel-interaction-parent"), "log-2");
});
