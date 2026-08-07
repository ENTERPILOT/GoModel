import test from "node:test";
import assert from "node:assert/strict";

import {
  buildConversationMessages,
  buildConversationView,
  buildFollowUpHeaders,
  buildFollowUpRequest,
  canBuildFollowUpRequest,
  canShowConversation,
  conversationEntryByRequestID,
  conversationEntryIsLatest,
  conversationFollowUpEntry,
  extractConversationErrorMessage,
  formatFunctionArguments,
  functionExpandedContent,
  followUpEndpointKind,
  interactionParentID,
  latestConversationEntry,
  latestRenderableConversationEntry,
  matchLiveConversationEntry,
  mergedConversationEntryIDs,
  shouldHydrateConversation,
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
  assert.equal(messages[2].toolCalls[0].id, "call-1");
  // The tool result resolves its function name through the call-id map.
  assert.equal(messages[3].functionName, "get_weather");
  assert.equal(messages[3].functionCallID, "call-1");
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
          { type: "function_call", id: "fc-9", call_id: "call-9", name: "lookup", arguments: '{"q":"x"}' },
          { type: "function_call_output", call_id: "call-9", output: "42" },
          { type: "tool_use", id: "toolu-10", name: "save_weather", input: { city: "Paris" } },
          { type: "tool_result", tool_use_id: "toolu-10", content: [{ type: "text", text: "Weather saved" }] },
        ],
      },
      response_body: {
        output: [
          { type: "message", role: "assistant", content: [{ type: "output_text", text: "Pong" }] },
          { type: "function_call", name: "lookup", arguments: '{"q":"y"}' },
          { type: "tool_use", id: "toolu-11", name: "store_weather", input: { city: "Rome" } },
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
    "function_call",
    "function_result",
    "assistant",
    "function_call",
    "function_call",
  ]);
  assert.equal(messages[0].text, "Be terse.");
  assert.equal(messages[2].toolCalls[0].id, "call-9");
  assert.equal(messages[3].functionName, "lookup");
  assert.equal(messages[3].functionCallID, "call-9");
  assert.equal(messages[3].text, "42");
  assert.equal(messages[4].toolCalls[0].name, "save_weather");
  assert.equal(messages[4].toolCalls[0].id, "toolu-10");
  assert.deepEqual(messages[4].toolCalls[0].arguments, { city: "Paris" });
  assert.equal(messages[5].functionName, "save_weather");
  assert.equal(messages[5].functionCallID, "toolu-10");
  assert.equal(messages[5].text, "Weather saved");
  assert.equal(messages[6].text, "Pong");
  assert.equal(messages[8].toolCalls[0].name, "store_weather");
  assert.equal(messages[8].toolCalls[0].id, "toolu-11");
  assert.deepEqual(messages[8].toolCalls[0].arguments, { city: "Rome" });
  assert.ok(messages.every((m) => m.isAnchor === false));
});

test("messages top-level system prompts support strings and content blocks", () => {
  const base = {
    timestamp: "2026-07-06T12:00:00Z",
    path: "/v1/messages",
    data: { request_body: { messages: [{ role: "user", content: "Hi" }] } },
  };
  const stringEntry = {
    ...base,
    id: "system-string",
    data: { request_body: { ...base.data.request_body, system: "Be concise." } },
  };
  const blockEntry = {
    ...base,
    id: "system-block",
    data: { request_body: {
      ...base.data.request_body,
      system: [{ type: "text", text: "Use tools carefully." }],
    } },
  };

  assert.deepEqual(buildConversationMessages([stringEntry], stringEntry.id).map((m) => m.text), [
    "Be concise.", "Hi",
  ]);
  assert.deepEqual(buildConversationMessages([blockEntry], blockEntry.id).map((m) => m.text), [
    "Use tools carefully.", "Hi",
  ]);
});

test("messages API tool-use blocks preserve inputs, results and call IDs", () => {
  const entry = {
    id: "anthropic-tools",
    timestamp: "2026-07-06T12:00:00Z",
    path: "/v1/messages",
    data: {
      request_body: {
        messages: [
          { role: "user", content: "Weather?" },
          { role: "assistant", content: [
            { type: "text", text: "I will check." },
            { type: "tool_use", id: "toolu-1", name: "weather", input: { city: "Paris" } },
          ] },
          { role: "user", content: [
            { type: "tool_result", tool_use_id: "toolu-1", content: "Sunny" },
          ] },
        ],
      },
      response_body: {
        role: "assistant",
        content: [
          { type: "text", text: "It is sunny." },
          { type: "tool_use", id: "toolu-2", name: "forecast", input: { days: 3 } },
        ],
      },
    },
  };

  const messages = buildConversationMessages([entry], entry.id);
  assert.deepEqual(messages.map((message) => message.role), [
    "user", "assistant", "function_result", "assistant",
  ]);
  assert.deepEqual(messages[1].toolCalls[0], {
    name: "weather", arguments: { city: "Paris" }, id: "toolu-1",
  });
  assert.equal(messages[2].text, "Sunny");
  assert.equal(messages[2].functionName, "weather");
  assert.equal(messages[2].functionCallID, "toolu-1");
  assert.equal(messages[3].text, "It is sunny.");
  assert.equal(messages[3].toolCalls[0].id, "toolu-2");
});

test("non-text content gets safe transcript placeholders and refusals remain visible", () => {
  const entry = {
    id: "multimodal",
    timestamp: "2026-07-06T12:00:00Z",
    path: "/v1/chat/completions",
    data: {
      request_body: { messages: [{ role: "user", content: [
        { type: "text", text: "Inspect these" },
        { type: "image_url", image_url: { url: "https://example.com/private.png" } },
        { type: "input_file", filename: "report.pdf", file_id: "file-1" },
      ] }] },
      response_body: { choices: [{ message: { role: "assistant", content: null, refusal: "I cannot inspect it." } }] },
    },
  };

  const messages = buildConversationMessages([entry], entry.id);
  assert.equal(messages[0].text, "Inspect these\n[Image]\n[File: report.pdf]");
  assert.equal(messages[1].text, "I cannot inspect it.");
});

test("Responses refusal-only output remains visible", () => {
  const entry = {
    id: "responses-refusal",
    timestamp: "2026-07-06T12:00:00Z",
    path: "/v1/responses",
    data: {
      request_body: { input: "Inspect this" },
      response_body: {
        output: [{
          type: "message",
          role: "assistant",
          content: [{ type: "refusal", refusal: "I cannot inspect it." }],
        }],
      },
    },
  };

  const messages = buildConversationMessages([entry], entry.id);
  assert.deepEqual(messages.map((message) => message.text), ["Inspect this", "I cannot inspect it."]);
  assert.equal(messages[1].role, "assistant");
});

test("prompt-cache estimates fill interaction messages by cached share", () => {
  const entry = {
    id: "prompt-cache",
    timestamp: "2026-07-06T12:00:00Z",
    path: "/v1/chat/completions",
    usage: { estimated_cached_characters: 9 },
    data: { request_body: { messages: [
      { role: "system", content: "System" },
      { role: "user", content: "Hello" },
    ] } },
  };

  const messages = buildConversationMessages([entry], entry.id);
  assert.equal(messages[0].promptCacheRatio, 1);
  assert.equal(messages[1].promptCacheRatio, 3 / 5);
});

test("prompt-cache fill stays contiguous when rendered text trims whitespace", () => {
  const entry = {
    id: "prompt-cache-whitespace",
    timestamp: "2026-07-06T12:00:00Z",
    path: "/v1/chat/completions",
    usage: { estimated_cached_characters: 7 },
    data: { request_body: { messages: [
      { role: "system", content: "  First  " },
      { role: "user", content: "Second" },
    ] } },
  };

  const messages = buildConversationMessages([entry], entry.id);
  assert.equal(messages[0].text, "First");
  assert.equal(messages[0].promptCacheRatio, 1);
  assert.equal(messages[1].promptCacheRatio, 2 / 6);
});

test("prompt-cache estimates skip non-text attachment placeholders", () => {
  const entry = {
    id: "prompt-cache-image",
    timestamp: "2026-07-06T12:00:00Z",
    path: "/v1/chat/completions",
    usage: { estimated_cached_characters: 20 },
    data: { request_body: { messages: [{ role: "user", content: [
      { type: "text", text: "Look" },
      { type: "image_url", image_url: { url: "https://example.com/image.png" } },
      { type: "text", text: "again" },
    ] }] } },
  };

  const message = buildConversationMessages([entry], entry.id)[0];
  assert.equal(message.text, "Look\n[Image]\nagain");
  assert.equal(message.promptCacheRatio, 1);
});

test("prompt-cache estimates do not count image-only or file-only placeholders", () => {
  const entry = {
    id: "prompt-cache-attachments-only",
    timestamp: "2026-07-06T12:00:00Z",
    path: "/v1/chat/completions",
    usage: { estimated_cached_characters: 100 },
    data: { request_body: { messages: [
      { role: "user", content: [
        { type: "image_url", image_url: { url: "https://example.com/image.png" } },
      ] },
      { role: "user", content: [
        { type: "input_file", filename: "report.pdf", file_id: "file-1" },
      ] },
    ] } },
  };

  const messages = buildConversationMessages([entry], entry.id);
  assert.deepEqual(messages.map((message) => message.text), ["[Image]", "[File: report.pdf]"]);
  assert.deepEqual(messages.map((message) => message.promptCacheRatio), [0, 0]);
});

test("prompt-cache estimates include tool-call names and arguments", () => {
  const entry = {
    id: "prompt-cache-tool",
    timestamp: "2026-07-06T12:00:00Z",
    path: "/v1/chat/completions",
    usage: { estimated_cached_characters: 200 },
    data: { request_body: { messages: [{
      role: "assistant",
      content: "Calling the tool",
      tool_calls: [{ id: "call-1", function: {
        name: "get_weather",
        arguments: '{"city":"Paris"}',
      } }],
    }] } },
  };

  const message = buildConversationMessages([entry], entry.id)[0];
  assert.equal(message.promptCacheRatio, 1);
});

test("tool-only assistant bubbles derive their fill from tool-call content", () => {
  const entry = {
    id: "prompt-cache-tool-only",
    timestamp: "2026-07-06T12:00:00Z",
    path: "/v1/chat/completions",
    usage: { estimated_cached_characters: 5 },
    data: { request_body: { messages: [{
      role: "assistant",
      content: "",
      tool_calls: [{ function: { name: "lookup", arguments: '{"q":"x"}' } }],
    }] } },
  };

  const message = buildConversationMessages([entry], entry.id)[0];
  assert.ok(message.promptCacheRatio > 0 && message.promptCacheRatio < 1);
});

test("tool-call cache measurement uses compact payload JSON, not display formatting", () => {
  const entry = {
    id: "prompt-cache-tool-compact",
    timestamp: "2026-07-06T12:00:00Z",
    path: "/v1/chat/completions",
    // lookup({"q":"x"}) is 17 characters; display whitespace must not
    // increase the cache measurement denominator.
    usage: { estimated_cached_characters: 17 },
    data: { request_body: { messages: [{
      role: "assistant",
      content: "",
      tool_calls: [{ function: { name: "lookup", arguments: { q: "x" } } }],
    }] } },
  };

  const message = buildConversationMessages([entry], entry.id)[0];
  assert.equal(message.promptCacheRatio, 1);
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
  assert.equal(formatFunctionArguments({ arguments: { q: "object" } }), '{\n  "q": "object"\n}');
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

test("a follow-up anchored from history selects its new fork instead of a newer sibling", () => {
  const anchor = {
    id: "archived",
    timestamp: "2026-08-03T11:00:00Z",
    data: {
      request_body: { messages: [{ role: "user", content: "start" }] },
      response_body: { choices: [{ message: { role: "assistant", content: "original" } }] },
    },
  };
  const sibling = {
    id: "existing-branch",
    timestamp: "2026-08-03T11:03:00Z",
    data: {
      request_headers: { "X-GoModel-Interaction-Parent": anchor.id },
      request_body: { messages: [
        { role: "user", content: "start" },
        { role: "assistant", content: "original" },
        { role: "user", content: "existing branch" },
      ] },
    },
  };
  const followUp = {
    id: "sent-follow-up",
    timestamp: "2026-08-03T11:02:00Z",
    data: {
      request_headers: { "X-GoModel-Interaction-Parent": anchor.id },
      request_body: { messages: [
        { role: "user", content: "start" },
        { role: "assistant", content: "original" },
        { role: "user", content: "new fork" },
      ] },
      response_body: { choices: [{ message: { role: "assistant", content: "new answer" } }] },
    },
  };

  const view = buildConversationView([followUp, sibling, anchor], followUp.id);
  assert.deepEqual(view.entryIDs, [followUp.id]);
  assert.equal(view.messages.some((message) => message.text === "existing branch"), false);
  assert.equal(view.messages.some((message) => message.text === "new fork"), true);
  assert.equal(view.messages.some((message) => message.text === "new answer"), true);
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

test("only the selected anchor's flush triggers persisted hydration", () => {
  assert.equal(shouldHydrateConversation("audit.flushed", "older", "newer"), false);
  assert.equal(shouldHydrateConversation("audit.completed", "newer", "newer"), false);
  assert.equal(shouldHydrateConversation("audit.flushed", "newer", "newer"), true);
});

test("empty hydration never erases known live conversation records", () => {
  assert.deepEqual(mergedConversationEntryIDs(["older", "live-anchor"], []), [
    "older",
    "live-anchor",
  ]);
  assert.deepEqual(mergedConversationEntryIDs(["older"], [
    { id: "older" },
    { id: "persisted-anchor" },
  ]), ["older", "persisted-anchor"]);
});

test("equal timestamps are ordered deterministically by audit id", () => {
  const timestamp = "2026-07-06T12:00:00Z";
  const first = {
    id: "log-a",
    timestamp,
    data: {
      request_body: { input: "First" },
      response_body: { id: "resp-a" },
    },
  };
  const second = {
    id: "log-b",
    timestamp,
    data: {
      request_body: { input: "Second", previous_response_id: "resp-a" },
      response_body: { id: "resp-b" },
    },
  };

  const view = buildConversationView([second, first], first.id);
  assert.deepEqual(view.entryIDs, ["log-a", "log-b"]);
  assert.deepEqual(view.messages.map((message) => message.text), ["First", "Second"]);
  assert.equal(latestConversationEntry([second, first]), second);
});

test("prompt-cache presentation does not duplicate Responses delta history", () => {
  const first = {
    id: "responses-a",
    timestamp: "2026-07-06T12:00:00Z",
    path: "/v1/responses",
    data: {
      request_body: { input: "same" },
      response_body: { id: "resp-a" },
    },
  };
  const second = {
    id: "responses-b",
    timestamp: "2026-07-06T12:01:00Z",
    path: "/v1/responses",
    usage: { estimated_cached_characters: 4 },
    data: {
      request_body: {
        previous_response_id: "resp-a",
        input: [
          { role: "user", content: "same" },
          { role: "user", content: "next" },
        ],
      },
      response_body: { id: "resp-b" },
    },
  };

  const messages = buildConversationMessages([first, second], first.id);
  assert.deepEqual(messages.map((message) => message.text), ["same", "next"]);
  assert.equal(messages[1].promptCacheRatio, 0);
});

test("Responses cache fill covers chained history before the new delta", () => {
  const first = {
    id: "responses-cache-a",
    timestamp: "2026-07-06T12:00:00Z",
    path: "/v1/responses",
    data: {
      request_body: { input: "same" },
      response_body: {
        id: "resp-cache-a",
        output: [{
          type: "message",
          role: "assistant",
          content: [{ type: "output_text", text: "answer" }],
        }],
      },
    },
  };
  const second = {
    id: "responses-cache-b",
    timestamp: "2026-07-06T12:01:00Z",
    path: "/v1/responses",
    usage: { estimated_cached_characters: 10 },
    data: {
      request_body: { previous_response_id: "resp-cache-a", input: "next" },
      response_body: { id: "resp-cache-b" },
    },
  };

  const messages = buildConversationMessages([first, second], first.id);
  assert.deepEqual(messages.map((message) => message.text), ["same", "answer", "next"]);
  assert.deepEqual(messages.map((message) => message.promptCacheRatio), [1, 1, 0]);
});

test("Responses cache fill distinguishes missing usage from an explicit zero", () => {
  const first = {
    id: "responses-known-cache",
    timestamp: "2026-07-06T12:00:00Z",
    path: "/v1/responses",
    usage: { estimated_cached_characters: 4 },
    data: {
      request_body: { input: "same" },
      response_body: {
        id: "resp-known-cache",
        output: [{
          type: "message",
          role: "assistant",
          content: [{ type: "output_text", text: "answer" }],
        }],
      },
    },
  };
  const second = {
    id: "responses-unknown-cache",
    timestamp: "2026-07-06T12:01:00Z",
    path: "/v1/responses",
    data: {
      request_body: { previous_response_id: "resp-known-cache", input: "next" },
      response_body: { id: "resp-unknown-cache" },
    },
  };

  const missingUsage = buildConversationMessages([first, second], first.id);
  assert.deepEqual(missingUsage.map((message) => message.promptCacheRatio), [1, 0, 0]);

  const explicitZero = buildConversationMessages([
    first,
    { ...second, usage: { estimated_cached_characters: 0 } },
  ], first.id);
  assert.deepEqual(explicitZero.map((message) => message.promptCacheRatio), [0, 0, 0]);
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
  const unchainable = {
    path: "/v1/responses",
    data: { request_body: { input: "Hi", previous_response_id: "resp_old" } },
  };
  assert.equal(canBuildFollowUpRequest(unchainable), false);
  assert.equal(buildFollowUpRequest(unchainable, "Next"), null);
  assert.equal(canBuildFollowUpRequest({
    path: "/v1/responses",
    data: { request_body: { input: "Hi", conversation: "  " } },
  }), false);
  assert.equal(buildFollowUpRequest({
    path: "/v1/responses",
    data: {
      request_body: { input: "Hi", conversation: "conv_123", previous_response_id: "resp_old" },
      response_body: { id: "resp_123" },
    },
  }, "Next").previous_response_id, undefined);
  assert.equal(canBuildFollowUpRequest(entry), true);
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
      "content-type": "application/json; charset=utf-8",
      Accept: "application/json",
      "Content-Encoding": "gzip",
      "Idempotency-Key": "old-operation",
    } },
  };
  const headers = buildFollowUpHeaders(entry, "log-1");
  assert.equal(headers.Authorization, undefined);
  assert.equal(headers["X-Request-Id"], undefined);
  assert.equal(headers["content-type"], undefined);
  assert.equal(headers.Accept, undefined);
  assert.equal(headers["Content-Encoding"], undefined);
  assert.equal(headers["Idempotency-Key"], undefined);
  assert.equal(headers["X-Custom"], "keep");
  assert.equal(headers["X-Session-Id"], "session-9");
  assert.equal(headers["X-GoModel-User-Path"], undefined);
  assert.equal(headers["X-GoModel-Interaction-Parent"], "log-1");
  assert.equal(interactionParentID({ data: { request_headers: headers } }), "log-1");
});

test("follow-up correlation selects only the submitted child request", () => {
  const entries = [
    { id: "sibling", request_id: "request-other" },
    { id: "submitted", request_id: "request-submitted" },
  ];
  assert.equal(conversationEntryByRequestID(entries, "request-submitted").id, "submitted");
  assert.equal(conversationEntryByRequestID(entries, "request-missing"), null);

  const headers = buildFollowUpHeaders(entries[1], "parent", " request-new ");
  assert.equal(headers["X-Request-ID"], "request-new");
});

test("follow-up correlation clears for the persisted child and accepts its descendant", () => {
  const parent = { id: "parent", session_id: "session-1" };
  const unrelated = {
    id: "sibling",
    request_id: "request-other",
    session_id: "session-1",
  };
  const rejected = matchLiveConversationEntry(
    [parent], parent.id, parent.session_id, "request-submitted", unrelated);
  assert.equal(rejected.accepted, false);
  assert.equal(rejected.followUpRequestID, "request-submitted");

  const submitted = {
    id: "submitted",
    request_id: "request-submitted",
    session_id: "session-1",
  };
  const childMatch = matchLiveConversationEntry(
    [parent], parent.id, parent.session_id, "request-submitted", submitted);
  assert.equal(childMatch.accepted, true);
  assert.equal(childMatch.submittedChild, true);
  assert.equal(childMatch.followUpRequestID, "");

  const descendant = {
    id: "descendant",
    request_id: "request-descendant",
    data: { request_headers: { "X-GoModel-Interaction-Parent": submitted.id } },
  };
  const descendantMatch = matchLiveConversationEntry(
    [parent, submitted], submitted.id, parent.session_id,
    childMatch.followUpRequestID, descendant);
  assert.equal(descendantMatch.accepted, true);
  assert.equal(descendantMatch.submittedChild, false);
});

test("follow-up headers do not change session scoping", () => {
  const rootSession = buildFollowUpHeaders({
    id: "root-log",
    data: { request_headers: {
      "X-Session-Id": "ses_038f24fd0ffepd013fh3piDcdV",
    } },
  }, "root-log");
  assert.equal(rootSession["X-Session-Id"], "ses_038f24fd0ffepd013fh3piDcdV");
  assert.equal(rootSession["X-GoModel-User-Path"], undefined);

  const scopedSession = buildFollowUpHeaders({
    id: "scoped-log",
    data: { request_headers: {
      "X-Session-Id": "scoped-529bff5b6264795393a9fb1e1da35906",
    } },
  }, "scoped-log");
  assert.equal(scopedSession["X-Session-Id"], "scoped-529bff5b6264795393a9fb1e1da35906");
  assert.equal(scopedSession["X-GoModel-User-Path"], undefined);

  const autoSession = buildFollowUpHeaders({
    id: "auto-log",
    data: { request_headers: {
      "X-Session-Id": "auto-529bff5b6264795393a9fb1e1da35906",
    } },
  }, "auto-log");
  assert.equal(autoSession["X-Session-Id"], "auto-529bff5b6264795393a9fb1e1da35906");

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
