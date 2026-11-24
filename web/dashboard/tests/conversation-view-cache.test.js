// Interactions drawer: rebuilding the transcript of a long session.
//
// The drawer rebuilds the transcript on every live event of the open
// conversation, and every request of a chat session resends the whole history.
// These tests pin that a rebuild reads only the bodies it has not seen yet and
// still returns what a first build returns.

import test from "node:test";
import assert from "node:assert/strict";

import {
  buildConversationView,
  unchangedConversationEntries,
} from "../src/pages/requests/conversation-helpers.js";

// countedMessage is a chat message whose content counts how often it is read.
function countedMessage(role, content, reads) {
  return Object.defineProperty({ role }, "content", {
    enumerable: true,
    get() {
      reads.count++;
      return content;
    },
  });
}

// sessionEntries builds a chat session of `turns` requests, each resending the
// history before it, the way an agent replays its conversation. readsOf names
// the counter for the reads of one request's body.
function sessionEntries(turns, readsOf = () => ({ count: 0 })) {
  const history = [["system", "You are a coding agent."]];
  const entries = [];
  for (let turn = 1; turn <= turns; turn++) {
    history.push(["user", "question " + turn]);
    entries.push({
      id: "entry-" + String(turn).padStart(3, "0"),
      timestamp: new Date(Date.UTC(2026, 8, 4, 12, 0, turn)).toISOString(),
      path: "/v1/chat/completions",
      data: {
        request_body: {
          messages: history.map(([role, content]) => countedMessage(role, content, readsOf(turn))),
        },
        response_body: {
          choices: [{ message: { role: "assistant", content: "answer " + turn } }],
        },
      },
    });
    history.push(["assistant", "answer " + turn]);
  }
  return entries;
}

test("a rebuild reads only the request bodies it has not seen", () => {
  const earlier = { count: 0 };
  const latest = { count: 0 };
  const entries = sessionEntries(30, (turn) => (turn < 30 ? earlier : latest));

  buildConversationView(entries.slice(0, 29), "entry-029");
  const firstBuild = earlier.count;
  assert.ok(firstBuild > 0);

  // The same records again, as on a live event of another request.
  buildConversationView(entries.slice(0, 29), "entry-029");
  assert.equal(earlier.count, firstBuild);

  // One more request arrives and becomes the selected one: only its body is new.
  buildConversationView(entries, "entry-030");
  assert.equal(earlier.count, firstBuild);
  assert.ok(latest.count > 0);
});

test("a rebuild returns what a first build of the same records returns", () => {
  const entries = sessionEntries(12);

  for (const anchorID of ["entry-012", "entry-005"]) {
    const first = buildConversationView(entries, anchorID);
    const again = buildConversationView(entries, anchorID);
    assert.deepEqual(again, first);
    // Fresh objects share nothing with the records built above.
    assert.deepEqual(first, buildConversationView(sessionEntries(12), anchorID));
    assert.equal(first.messages.length, 2 * 12 + 1);
  }
});

test("rebuilds do not grow the history a later snapshot is compared with", () => {
  const chat = (id, second, messages, answer) => ({
    id,
    timestamp: "2026-09-04T12:00:0" + second + "Z",
    path: "/v1/chat/completions",
    data: {
      request_body: { messages },
      response_body: { choices: [{ message: { role: "assistant", content: answer } }] },
    },
  });
  // The second request repeats the first answer twice before its question.
  const build = () => [
    chat("entry-001", 1, [{ role: "user", content: "one" }], "answer"),
    chat("entry-002", 2, [
      { role: "user", content: "one" },
      { role: "assistant", content: "answer" },
      { role: "assistant", content: "answer" },
      { role: "user", content: "two" },
    ], "done"),
  ];
  const entries = build();

  const first = buildConversationView(entries, "entry-002");
  buildConversationView(entries, "entry-002");
  const third = buildConversationView(entries, "entry-002");

  assert.deepEqual(third, first);
  assert.deepEqual(first, buildConversationView(build(), "entry-002"));
  assert.deepEqual(
    first.messages.map((message) => message.entryID),
    ["entry-001", "entry-001", "entry-002", "entry-002", "entry-002"],
  );
});

test("a streamed response updates the transcript although the request body is unchanged", () => {
  const entries = sessionEntries(3);
  const streaming = (text) => [
    ...entries.slice(0, 2),
    {
      ...entries[2],
      data: {
        ...entries[2].data,
        response_body: { choices: [{ message: { role: "assistant", content: text } }] },
      },
    },
  ];

  const partial = buildConversationView(streaming("ans"), "entry-003").messages;
  const complete = buildConversationView(streaming("answer 3"), "entry-003").messages;

  assert.equal(partial.at(-1).text, "ans");
  assert.equal(complete.at(-1).text, "answer 3");
  assert.equal(complete.length, partial.length);
});

test("a body is read again when a tool call id resolves to another function name", () => {
  const toolResult = {
    id: "entry-002",
    timestamp: "2026-09-04T12:00:02Z",
    path: "/v1/chat/completions",
    data: {
      request_body: {
        messages: [
          { role: "user", content: "weather?" },
          { role: "tool", tool_call_id: "call_1", content: "sunny" },
        ],
      },
    },
  };
  const functionName = (entries) =>
    buildConversationView(entries, "entry-002").messages
      .find((message) => message.role === "function_result").functionName;

  // Alone, nothing names the call.
  assert.equal(functionName([toolResult]), "");

  // The request that issued the call arrives later, as a hydrated older turn.
  const toolCall = {
    id: "entry-001",
    timestamp: "2026-09-04T12:00:01Z",
    path: "/v1/chat/completions",
    data: {
      request_body: { messages: [{ role: "user", content: "weather?" }] },
      response_body: {
        choices: [{
          message: {
            role: "assistant",
            tool_calls: [{ id: "call_1", function: { name: "get_weather", arguments: "{}" } }],
          },
        }],
      },
    },
  };
  assert.equal(functionName([toolCall, toolResult]), "get_weather");
  assert.equal(functionName([toolResult]), "");
});

test("an error in a response body is still reported on a rebuild", () => {
  const entries = [{
    id: "entry-001",
    timestamp: "2026-09-04T12:00:01Z",
    path: "/v1/chat/completions",
    data: {
      request_body: { messages: [{ role: "user", content: "hi" }] },
      response_body: { error: { message: "{\"error\":{\"message\":\"rate limited\"}}" } },
    },
  }];

  const first = buildConversationView(entries, "entry-001").messages;
  const again = buildConversationView(entries, "entry-001").messages;

  assert.equal(first.at(-1).role, "error");
  assert.equal(first.at(-1).text, "rate limited");
  assert.deepEqual(again, first);
});

test("the entry list keeps its identity until one of its records is replaced", () => {
  const first = { id: "a" };
  const second = { id: "b" };
  const previous = [first, second];

  assert.equal(unchangedConversationEntries(previous, [first, second]), previous);

  const replaced = [first, { ...second }];
  assert.equal(unchangedConversationEntries(previous, replaced), replaced);
  const reordered = [second, first];
  assert.equal(unchangedConversationEntries(previous, reordered), reordered);
  const grown = [first, second, { id: "c" }];
  assert.equal(unchangedConversationEntries(previous, grown), grown);
  const shrunk = [first];
  assert.equal(unchangedConversationEntries(previous, shrunk), shrunk);
  const initial = [first];
  assert.equal(unchangedConversationEntries(undefined, initial), initial);
});
