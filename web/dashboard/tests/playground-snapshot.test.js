import test from "node:test";
import assert from "node:assert/strict";

import {
  canOpenInPlayground,
  canOpenUsageEntryInPlayground,
  playgroundSnapshot,
  requestLogFetchOutcome,
  usageEntryRequestLogQuery,
  usageRequestLogMatch,
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
    limit: "20",
    start_date: "2026-02-28",
    end_date: "2026-03-02",
  });

  const undated = new URLSearchParams(usageEntryRequestLogQuery({ request_id: "req-1" }));
  assert.deepEqual(Object.fromEntries(undated), { request_id: "req-1", limit: "20" });
});

test("roles are matched as own names, not inherited properties", () => {
  const snapshot = playgroundSnapshot(
    entry("/v1/chat/completions", {
      model: "m",
      messages: [
        { role: "constructor", content: "x" },
        { role: "toString", content: "y" },
        { role: "user", content: "Hi" },
      ],
    }),
  );
  assert.equal(snapshot.partial, true);
  assert.deepEqual(snapshot.messages, [{ role: "user", content: "Hi" }]);
});

test("a usage row matches the one request log entry it belongs to", () => {
  const usageRow = { request_id: "req-1", endpoint: "/v1/chat/completions", timestamp: "2026-03-01T10:00:05Z" };
  const logEntry = (id, timestamp, extra = {}) => ({
    id,
    request_id: "req-1",
    path: "/v1/chat/completions",
    timestamp,
    duration_ns: 5e9,
    ...extra,
  });
  const own = logEntry("log-own", "2026-03-01T10:00:00Z");
  const later = logEntry("log-later", "2026-03-01T11:00:00Z");

  assert.deepEqual(usageRequestLogMatch(usageRow, [own], 1), { entry: own, ambiguous: false });
  assert.deepEqual(usageRequestLogMatch(usageRow, [], 0), { entry: null, ambiguous: false });
  assert.deepEqual(usageRequestLogMatch(usageRow, null, undefined), { entry: null, ambiguous: false });

  // A reused request id: the newest entry is listed first, the row's own one
  // is found by the time its usage was recorded.
  assert.deepEqual(usageRequestLogMatch(usageRow, [later, own], 2), { entry: own, ambiguous: false });

  // Another endpoint or request id never matches.
  const embeddings = logEntry("log-emb", "2026-03-01T10:00:00Z", { path: "/v1/embeddings" });
  const otherID = logEntry("log-other", "2026-03-01T10:00:00Z", { request_id: "req-2" });
  assert.deepEqual(usageRequestLogMatch(usageRow, [embeddings, otherID, own], 3), {
    entry: own,
    ambiguous: false,
  });

  const ambiguous = [
    ["two entries span the usage time", [own, logEntry("log-twin", "2026-03-01T10:00:01Z")], 2],
    ["no entry spans the usage time", [later, logEntry("log-early", "2026-03-01T08:00:00Z")], 2],
    ["the page does not hold every match", [own], 21],
    ["the usage row has no timestamp", [later, own], 2, { ...usageRow, timestamp: undefined }],
  ];
  for (const [name, entries, total, row = usageRow] of ambiguous) {
    assert.deepEqual(usageRequestLogMatch(row, entries, total), { entry: null, ambiguous: true }, name);
  }
});

test("fetch outcomes separate handled, missing and failed responses", () => {
  const cases = [
    ["ok", { ok: true, status: 200 }],
    ["missing", { ok: false, status: 404 }],
    ["failed", { ok: false, status: 500 }],
    ["failed", { ok: false, status: 403, data: { error: { code: "forbidden" } } }],
    ["failed", null],
    ["silent", { ok: false, status: 401 }],
    ["silent", { ok: true, stale: true, status: 200 }],
    ["silent", { ok: false, status: 403, data: { error: { code: "dashboard_access_denied" } } }],
  ];
  for (const [want, result] of cases) {
    assert.equal(requestLogFetchOutcome(result), want, JSON.stringify(result));
  }
});
