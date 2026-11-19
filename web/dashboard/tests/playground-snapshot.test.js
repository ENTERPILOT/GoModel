import test from "node:test";
import assert from "node:assert/strict";

import {
  canOpenInPlayground,
  canOpenUsageEntryInPlayground,
  playgroundSnapshot,
  usageEntryRequestLogQuery,
} from "../src/pages/playground/playgroundSnapshot.js";
import { buildPlaygroundRequest } from "../src/pages/playground/playgroundLogic.js";

function entry(path, requestBody, extra = {}) {
  return { id: "log-1", path, data: { request_body: requestBody }, ...extra };
}

test("a chat completion maps to the chat endpoint with its text messages", () => {
  const snapshot = playgroundSnapshot(
    entry("/v1/chat/completions", {
      model: " gpt-4o ",
      stream: true,
      messages: [
        { role: "developer", content: "Be terse." },
        { role: "user", content: "Hi" },
        { role: "assistant", content: "Hello!" },
      ],
    }),
  );
  assert.deepEqual(snapshot, {
    endpoint: "chat",
    model: "gpt-4o",
    stream: true,
    partial: false,
    messages: [
      { role: "system", content: "Be terse." },
      { role: "user", content: "Hi" },
      { role: "assistant", content: "Hello!" },
    ],
  });
});

test("a complete snapshot rebuilds the request that was logged", () => {
  const body = {
    model: "gpt-4o",
    messages: [
      { role: "system", content: "Be terse." },
      { role: "user", content: "Hi" },
    ],
    stream: true,
  };
  const snapshot = playgroundSnapshot(entry("/v1/chat/completions", body));
  assert.equal(snapshot.partial, false);
  assert.deepEqual(buildPlaygroundRequest(snapshot.endpoint, snapshot), body);
});

test("content parts keep their text and report what was dropped", () => {
  const snapshot = playgroundSnapshot(
    entry("/v1/chat/completions", {
      model: "gpt-4o",
      messages: [
        {
          role: "user",
          content: [
            { type: "text", text: "What is this?" },
            { type: "image_url", image_url: { url: "data:image/png;base64,AAAA" } },
            { type: "text", text: "Answer briefly." },
          ],
        },
      ],
    }),
  );
  assert.equal(snapshot.partial, true);
  assert.deepEqual(snapshot.messages, [
    { role: "user", content: "What is this?\n\nAnswer briefly." },
  ]);
});

test("tool calls, tool results and extra parameters make the snapshot partial", () => {
  const cases = [
    {
      name: "sampling parameter",
      body: { model: "m", temperature: 0.2, messages: [{ role: "user", content: "Hi" }] },
      messages: [{ role: "user", content: "Hi" }],
    },
    {
      name: "tool call without text and its result",
      body: {
        model: "m",
        messages: [
          { role: "user", content: "Weather?" },
          { role: "assistant", content: null, tool_calls: [{ id: "call_1" }] },
          { role: "tool", tool_call_id: "call_1", content: "sunny" },
        ],
      },
      messages: [{ role: "user", content: "Weather?" }],
    },
    {
      name: "extra message field",
      body: { model: "m", messages: [{ role: "user", name: "ann", content: "Hi" }] },
      messages: [{ role: "user", content: "Hi" }],
    },
  ];
  for (const { name, body, messages } of cases) {
    const snapshot = playgroundSnapshot(entry("/v1/chat/completions", body));
    assert.equal(snapshot.partial, true, name);
    assert.deepEqual(snapshot.messages, messages, name);
  }
});

test("a Responses request maps instructions and input items", () => {
  assert.deepEqual(
    playgroundSnapshot(entry("/v1/responses", { model: "gpt-5", instructions: "Be terse.", input: "Hi" })),
    {
      endpoint: "responses",
      model: "gpt-5",
      stream: false,
      partial: false,
      messages: [
        { role: "system", content: "Be terse." },
        { role: "user", content: "Hi" },
      ],
    },
  );

  const snapshot = playgroundSnapshot(
    entry("/v1/responses", {
      model: "gpt-5",
      input: [
        { role: "user", content: [{ type: "input_text", text: "Weather?" }] },
        { type: "function_call", call_id: "c1", name: "weather", arguments: "{}" },
        { type: "function_call_output", call_id: "c1", output: "sunny" },
        { type: "message", role: "assistant", content: [{ type: "output_text", text: "Sunny." }] },
      ],
    }),
  );
  assert.equal(snapshot.partial, true);
  assert.deepEqual(snapshot.messages, [
    { role: "user", content: "Weather?" },
    { role: "assistant", content: "Sunny." },
  ]);
});

test("a Responses request continuing a stored response is partial", () => {
  const snapshot = playgroundSnapshot(
    entry("/v1/responses", { model: "gpt-5", previous_response_id: "resp_1", input: "And then?" }),
  );
  assert.equal(snapshot.partial, true);
  assert.deepEqual(snapshot.messages, [{ role: "user", content: "And then?" }]);
});

test("an Anthropic Messages request maps the system prompt and text blocks", () => {
  const snapshot = playgroundSnapshot(
    entry("/v1/messages", {
      model: "claude-sonnet-4-5",
      max_tokens: 4096,
      system: [{ type: "text", text: "Be terse." }],
      messages: [
        { role: "user", content: "Hi" },
        { role: "assistant", content: [{ type: "text", text: "Hello!" }] },
        { role: "user", content: [{ type: "tool_result", tool_use_id: "t1", content: "x" }] },
      ],
    }),
  );
  assert.equal(snapshot.endpoint, "messages");
  // max_tokens has no control in the Playground, and the tool result is dropped.
  assert.equal(snapshot.partial, true);
  assert.deepEqual(snapshot.messages, [
    { role: "system", content: "Be terse." },
    { role: "user", content: "Hi" },
    { role: "assistant", content: "Hello!" },
  ]);

  const plain = playgroundSnapshot(
    entry("/v1/messages", { model: "claude", system: "Be terse.", messages: [{ role: "user", content: "Hi" }] }),
  );
  assert.equal(plain.partial, false);
  assert.deepEqual(plain.messages[0], { role: "system", content: "Be terse." });
});

test("the model falls back to the entry's requested model", () => {
  const snapshot = playgroundSnapshot(
    entry("/v1/chat/completions", { messages: [{ role: "user", content: "Hi" }] }, { requested_model: "gpt-4o" }),
  );
  assert.equal(snapshot.model, "gpt-4o");
});

test("entries without a replayable request body yield no snapshot", () => {
  const messages = [{ role: "user", content: "Hi" }];
  const cases = [
    ["no entry", null],
    ["no data", { id: "log-1", path: "/v1/chat/completions" }],
    ["body logging off", entry("/v1/chat/completions", undefined)],
    ["non-JSON body", entry("/v1/chat/completions", "not json")],
    ["embeddings", entry("/v1/embeddings", { model: "m", input: "Hi" })],
    ["passthrough", entry("/p/openai/v1/chat/completions", { model: "m", messages })],
    ["response retrieval", entry("/v1/responses/resp_1", { model: "m", input: "Hi" })],
    ["no messages array", entry("/v1/chat/completions", { model: "m" })],
    ["no text at all", entry("/v1/chat/completions", { model: "m", messages: [{ role: "tool", content: "x" }] })],
    ["blank text", entry("/v1/responses", { model: "m", input: "   " })],
  ];
  for (const [name, value] of cases) {
    assert.equal(playgroundSnapshot(value), null, name);
    assert.equal(canOpenInPlayground(value), false, name);
  }
});

test("canOpenInPlayground accepts full entries and slim list entries with a conversation payload", () => {
  const body = { model: "m", messages: [{ role: "user", content: "Hi" }] };
  assert.equal(canOpenInPlayground(entry("/v1/chat/completions?x=1", body)), true);

  const slim = { id: "log-1", path: "/v1/messages", bodies_omitted: true, conversation_payload: true, data: {} };
  assert.equal(canOpenInPlayground(slim), true);
  assert.equal(canOpenInPlayground({ ...slim, conversation_payload: false }), false);
  assert.equal(canOpenInPlayground({ ...slim, bodies_omitted: false }), false);
  assert.equal(canOpenInPlayground({ ...slim, id: "" }), false);
  assert.equal(canOpenInPlayground({ ...slim, path: "/v1/embeddings" }), false);
  // The response alone can make the payload conversation-shaped.
  assert.equal(
    canOpenInPlayground({ ...slim, data: { request_body_too_big_to_handle: true } }),
    false,
  );
});

test("usage rows qualify by request id and endpoint", () => {
  assert.equal(canOpenUsageEntryInPlayground({ request_id: "req-1", endpoint: "/v1/responses" }), true);
  assert.equal(canOpenUsageEntryInPlayground({ request_id: " ", endpoint: "/v1/responses" }), false);
  assert.equal(canOpenUsageEntryInPlayground({ request_id: "req-1", endpoint: "/v1/embeddings" }), false);
  assert.equal(canOpenUsageEntryInPlayground({ request_id: "req-1", endpoint: "/v1/realtime" }), false);
  assert.equal(canOpenUsageEntryInPlayground(null), false);
});

test("the usage row lookup pins the request id and a window around its day", () => {
  const query = new URLSearchParams(
    usageEntryRequestLogQuery({ request_id: " req 1 ", timestamp: "2026-03-01T00:30:00Z" }),
  );
  assert.deepEqual(Object.fromEntries(query), {
    request_id: "req 1",
    limit: "1",
    start_date: "2026-02-28",
    end_date: "2026-03-02",
  });

  const undated = new URLSearchParams(usageEntryRequestLogQuery({ request_id: "req-1" }));
  assert.deepEqual(Object.fromEntries(undated), { request_id: "req-1", limit: "1" });
});
