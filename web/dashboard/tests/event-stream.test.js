// Transport tests for the shared SSE client. The audit-log merge suite
// (live-logs.test.js) still exercises it end-to-end through
// consumeLiveLogsBody; these cover the framing rules directly, including the
// cases only the overview's usage stream used to hit.

import test from "node:test";
import assert from "node:assert/strict";

import {
  MAX_RECONNECT_ATTEMPTS,
  consumeEventStream,
  nextReconnect,
} from "../src/lib/api/eventStream.js";

function readerFromChunks(chunks) {
  const encoder = new TextEncoder();
  let i = 0;
  return {
    async read() {
      if (i >= chunks.length) return { done: true };
      return { done: false, value: encoder.encode(chunks[i++]) };
    },
  };
}

async function collect(chunks) {
  const events = [];
  await consumeEventStream(readerFromChunks(chunks), (event) =>
    events.push(event),
  );
  return events;
}

test("splits CRLF- and LF-separated frames", async () => {
  const events = await collect([
    'data: {"seq":1}\r\n\r\ndata: {"seq":2}\n\n',
  ]);
  assert.deepEqual(events, [{ seq: 1 }, { seq: 2 }]);
});

test("joins a frame split across chunks and flushes the trailing frame", async () => {
  const events = await collect(['data: {"se', 'q":1}\n\ndata: {"seq":2}']);
  assert.deepEqual(events, [{ seq: 1 }, { seq: 2 }]);
});

test("joins multi-line data payloads", async () => {
  const events = await collect(['data: {"seq":\ndata: 1}\n\n']);
  assert.deepEqual(events, [{ seq: 1 }]);
});

test("skips comment/heartbeat frames and unparsable JSON", async () => {
  const events = await collect([
    ": keep-alive\n\n",
    "data: not json\n\n",
    'data: {"seq":3}\n\n',
  ]);
  assert.deepEqual(events, [{ seq: 3 }]);
});

test("ignores a trailing frame that is only whitespace", async () => {
  const events = await collect(['data: {"seq":1}\n\n', "\n"]);
  assert.deepEqual(events, [{ seq: 1 }]);
});

test("reconnect backoff doubles from 500ms and caps at the attempt limit", () => {
  const delays = [];
  let attempts = 0;
  for (let i = 0; i < 8; i++) {
    const next = nextReconnect(attempts);
    attempts = next.attempt;
    delays.push(next.delay);
  }
  assert.deepEqual(delays, [500, 1000, 2000, 4000, 8000, 16000, 16000, 16000]);
  assert.equal(attempts, MAX_RECONNECT_ATTEMPTS);
});
