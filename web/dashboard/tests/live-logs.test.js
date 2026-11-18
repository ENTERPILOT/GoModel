// Ported from internal/admin/dashboard/static/js/modules/live-logs.test.cjs
// against the extracted pure logic in src/pages/requests/live-logs-logic.js.
// Transport-only legacy cases (fetch/base-path plumbing) are replaced by the
// pure liveLogsStreamPath cursor test; everything else runs the exact merge
// engine the Svelte singleton uses.

import test from "node:test";
import assert from "node:assert/strict";

import {
  requestLogLivePauseMessage,
  liveLogsMethods,
  liveLogsStreamPath,
  normalizeLiveLogEvent,
} from "../src/pages/requests/live-logs-logic.js";
import { requestLogRecordKey, mergeRequestLogRecord } from "../src/pages/requests/request-log-records.js";
import { emptyUsageFilters, usageFiltersEmpty } from "../src/pages/usage/usage-query.js";

// filtersWith stands in for the usage filter state the gate asks isEmpty().
function filtersWith(values) {
  return { isEmpty: () => usageFiltersEmpty({ ...emptyUsageFilters(), ...values }) };
}

function createLiveLogsApp(overrides = {}) {
  return {
    liveLogsLastSeq: 0,
    skippedLiveUsageByRequestId: null,
    requestLog: { entries: [], total: 0, limit: 25, offset: 0 },
    usageLog: { entries: [], total: 0, limit: 50, offset: 0 },
    requestLogSearch: "",
    requestLogMethod: "",
    requestLogStatusCode: "",
    requestLogStream: "",
    usageLogSearch: "",
    usageFilters: filtersWith({}),
    usageLogHideCached: false,
    requestLogGroupSessions: false,
    requestThreadChildren: {},
    requestLogRecords: {},
    requestLogRecordChanges: [],
    customStartDate: null,
    customEndDate: null,
    followsToday: false,
    page: "requests",
    fetchUsageCalls: 0,
    fetchRequestLogCalls: 0,
    fetchUsage() {
      this.fetchUsageCalls++;
    },
    fetchRequestLog() {
      this.fetchRequestLogCalls++;
    },
    cacheRequestLogRecord(entry, eventType) {
      const key = requestLogRecordKey(entry);
      const merged = mergeRequestLogRecord(this.requestLogRecords[key], entry);
      this.requestLogRecords[key] = merged;
      this.requestLogRecordChanges.push({ key, eventType });
      return merged;
    },
    ...liveLogsMethods(),
    ...overrides,
  };
}

function liveReaderFromChunks(chunks) {
  const encoder = new TextEncoder();
  const values = chunks.map((chunk) => encoder.encode(chunk));
  let index = 0;
  return {
    async read() {
      if (index >= values.length) {
        return { done: true };
      }
      return { done: false, value: values[index++] };
    },
  };
}

test("live logs stream path carries the replay cursor", () => {
  assert.equal(liveLogsStreamPath(0), "/admin/live/logs?types=request_log,audit,usage");
  assert.equal(liveLogsStreamPath(42), "/admin/live/logs?types=request_log,audit,usage&cursor=42");
});

test("live request log lifecycle events merge into one dashboard row by request id", () => {
  const app = createLiveLogsApp();

  app.applyLiveLogEvent({
    seq: 1,
    type: "request_log.started",
    data: { id: "request-log-1", request_id: "req-1", method: "POST", path: "/v1/chat/completions" },
  });
  app.applyLiveLogEvent({
    seq: 2,
    type: "request_log.updated",
    data: { id: "request-log-1", request_id: "req-1", requested_model: "gpt-test", provider: "openai" },
  });
  app.applyLiveLogEvent({
    seq: 3,
    type: "request_log.completed",
    data: { id: "request-log-1", request_id: "req-1", status_code: 200, duration_ns: 1000 },
  });
  app.applyLiveLogEvent({
    seq: 4,
    type: "request_log.flushed",
    data: { id: "request-log-1", request_id: "req-1", status_code: 200, duration_ns: 1000 },
  });

  assert.equal(app.liveLogsLastSeq, 4);
  assert.equal(app.requestLog.entries.length, 1);
  assert.equal(app.requestLog.entries[0].requested_model, "gpt-test");
  assert.equal(app.requestLog.entries[0].status_code, 200);
  assert.equal(app.requestLog.entries[0]._live_state, "request_log.flushed");
  assert.equal(app.requestLog.entries[0]._live_pending, false);
  assert.equal(app.requestLog.entries[0]._request_log_flushed, true);
});

// A replica still on v0.2.5 sends request log events under their old audit.*
// names; they must drive the row exactly as the request_log.* ones do.
test("legacy audit.* live events are handled as request_log.* events", () => {
  const app = createLiveLogsApp();

  app.applyLiveLogEvent({
    seq: 1,
    type: "audit.started",
    data: { id: "request-log-1", request_id: "req-1", method: "POST", _live_state: "audit.started", _live_pending: true },
  });
  assert.equal(app.requestLog.entries.length, 1);
  assert.equal(app.requestLog.entries[0]._live_state, "request_log.started");

  app.applyLiveLogEvent({
    seq: 2,
    type: "audit.flushed",
    data: { id: "request-log-1", request_id: "req-1", status_code: 200, _live_state: "audit.flushed" },
  });
  assert.equal(app.requestLog.entries.length, 1);
  assert.equal(app.requestLog.entries[0]._live_state, "request_log.flushed");
  assert.equal(app.requestLog.entries[0]._live_pending, false);
  assert.equal(app.requestLog.entries[0]._request_log_flushed, true);

  app.applyLiveLogEvent({
    seq: 3,
    type: "audit.started",
    data: { id: "request-log-2", request_id: "req-2", method: "POST" },
  });
  app.applyLiveLogEvent({ seq: 4, type: "audit.removed", data: { id: "request-log-2", request_id: "req-2" } });
  assert.deepEqual(app.requestLog.entries.map((entry) => entry.id), ["request-log-1"]);
  assert.equal(app.liveLogsLastSeq, 4);
});

test("normalizeLiveLogEvent renames only legacy request log names", () => {
  const legacy = { seq: 7, type: "audit.updated", data: { id: "a", _live_state: "audit.updated" } };
  assert.deepEqual(normalizeLiveLogEvent(legacy), {
    seq: 7,
    type: "request_log.updated",
    data: { id: "a", _live_state: "request_log.updated" },
  });
  // The event the stream parser handed over is left as it was.
  assert.equal(legacy.type, "audit.updated");
  assert.equal(legacy.data._live_state, "audit.updated");

  assert.deepEqual(normalizeLiveLogEvent({ type: "audit.removed" }), { type: "request_log.removed" });

  for (const event of [
    { seq: 1, type: "request_log.updated", data: { _live_state: "request_log.updated" } },
    { seq: 2, type: "usage.completed", data: { _live_state: "usage.completed" } },
    { seq: 3, type: "heartbeat" },
    { seq: 4, type: "reset" },
  ]) {
    assert.equal(normalizeLiveLogEvent(event), event);
  }
});

test("request_log.stream events merge partial response bodies and keep rows pending", () => {
  const app = createLiveLogsApp();

  app.applyLiveLogEvent({
    seq: 1,
    type: "request_log.started",
    data: { id: "request-log-1", request_id: "req-1", method: "POST", path: "/v1/chat/completions" },
  });
  app.applyLiveLogEvent({
    seq: 2,
    type: "request_log.stream",
    data: {
      id: "request-log-1",
      request_id: "req-1",
      status_code: 200,
      stream: true,
      data: {
        response_body: { choices: [{ index: 0, message: { role: "assistant", content: "partial" } }] },
        response_body_partial: true,
      },
    },
  });

  const streaming = app.requestLog.entries[0];
  assert.equal(streaming._response_partial, true);
  assert.equal(streaming._live_pending, true);
  assert.equal(streaming._live_state, "request_log.stream");
  assert.equal(streaming.data.response_body.choices[0].message.content, "partial");

  app.applyLiveLogEvent({
    seq: 3,
    type: "request_log.completed",
    data: {
      id: "request-log-1",
      request_id: "req-1",
      status_code: 200,
      duration_ns: 1000,
      data: {
        response_body: { choices: [{ index: 0, message: { role: "assistant", content: "final" } }] },
      },
    },
  });

  const completed = app.requestLog.entries[0];
  assert.equal(completed._response_partial, false);
  assert.equal(completed._live_state, "request_log.completed");
  assert.equal(completed.data.response_body.choices[0].message.content, "final");
});

test("live request log merges update the normalized record cache", () => {
  const app = createLiveLogsApp();

  app.applyLiveLogEvent({
    seq: 1,
    type: "request_log.started",
    data: { id: "request-log-1", request_id: "req-1" },
  });
  app.applyLiveLogEvent({
    seq: 2,
    type: "request_log.stream",
    data: { id: "request-log-1", request_id: "req-1", data: { response_body: { choices: [] }, response_body_partial: true } },
  });

  assert.equal(app.requestLogRecordChanges.length, 2);
  assert.equal(app.requestLogRecords["request-log-1"]._response_partial, true);
});

test("live request log removed event drops suppressed preview rows", () => {
  const app = createLiveLogsApp();
  app.requestLog.entries = [{ id: "request-log-1", request_id: "req-1" }];
  app.requestLog.total = 1;

  app.applyLiveLogEvent({
    seq: 4,
    type: "request_log.removed",
    data: { id: "request-log-1", request_id: "req-1" },
  });

  assert.deepEqual(app.requestLog.entries, []);
  assert.equal(app.requestLog.total, 0);
});

test("live request log removed event decrements total by removed row count", () => {
  const app = createLiveLogsApp();
  app.requestLog.entries = [
    { id: "request-log-1", request_id: "req-1" },
    { id: "request-log-2", request_id: "req-1" },
    { id: "request-log-3", request_id: "req-3" },
  ];
  app.requestLog.total = 3;

  app.applyLiveLogEvent({
    seq: 5,
    type: "request_log.removed",
    data: { request_id: "req-1" },
  });

  assert.deepEqual(app.requestLog.entries, [{ id: "request-log-3", request_id: "req-3" }]);
  assert.equal(app.requestLog.total, 1);
});

test("live request log inserts are blocked while a custom request log date range is active", () => {
  const app = createLiveLogsApp();

  app.customStartDate = new Date();
  app.applyLiveLogEvent({
    seq: 1,
    type: "request_log.started",
    data: { id: "request-log-start", request_id: "req-start" },
  });

  app.customStartDate = null;
  app.customEndDate = new Date();
  app.applyLiveLogEvent({
    seq: 2,
    type: "request_log.started",
    data: { id: "request-log-end", request_id: "req-end" },
  });

  assert.equal(app.requestLog.entries.length, 0);
  assert.equal(app.requestLog.total, 0);
});

test("live request log inserts flow while the custom date range ends today", () => {
  const app = createLiveLogsApp();

  // A persisted custom window that follows the current day (e.g. Jul 16 ->
  // today) must not pause live inserts: entries arriving now are inside it.
  app.customStartDate = new Date(Date.now() - 7 * 24 * 60 * 60 * 1000);
  app.customEndDate = new Date();
  app.followsToday = true;

  app.applyLiveLogEvent({
    seq: 1,
    type: "request_log.started",
    data: { id: "request-log-today", request_id: "req-today" },
  });

  assert.equal(app.requestLog.entries.length, 1);
  assert.equal(app.requestLog.entries[0].id, "request-log-today");
  assert.equal(app.requestLog.total, 1);
});

test("live request log inserts stay blocked for a custom range detached from today", () => {
  const app = createLiveLogsApp();

  app.customStartDate = new Date(Date.now() - 14 * 24 * 60 * 60 * 1000);
  app.customEndDate = new Date(Date.now() - 7 * 24 * 60 * 60 * 1000);
  app.followsToday = false;

  app.applyLiveLogEvent({
    seq: 1,
    type: "request_log.started",
    data: { id: "request-log-past", request_id: "req-past" },
  });

  assert.equal(app.requestLog.entries.length, 0);
  assert.equal(app.requestLog.total, 0);
});

test("request log live pause reason names the blocking condition", () => {
  const cases = [
    { name: "flowing", overrides: {}, want: null },
    {
      name: "pagination wins",
      overrides: { requestLog: { entries: [], total: 0, limit: 25, offset: 25 } },
      want: "pagination",
    },
    { name: "filters", overrides: { requestLogStatusCode: "500" }, want: "filters" },
    {
      name: "detached date range",
      overrides: { customStartDate: new Date(), followsToday: false },
      want: "date_range",
    },
    {
      name: "today-anchored range flows",
      overrides: { customStartDate: new Date(), customEndDate: new Date(), followsToday: true },
      want: null,
    },
  ];
  for (const tc of cases) {
    const app = createLiveLogsApp(tc.overrides);
    assert.equal(app.requestLogLivePauseReason(), tc.want, tc.name);
  }
});

test("request log live pause messages tell the operator how to resume", () => {
  assert.match(requestLogLivePauseMessage("date_range"), /Set it to today/);
  assert.match(requestLogLivePauseMessage("filters"), /filters/);
  assert.match(requestLogLivePauseMessage("pagination"), /first page/);
  assert.equal(requestLogLivePauseMessage(null), "");
});

test("live request log inserts are blocked while filters or pagination are active", () => {
  const app = createLiveLogsApp();
  app.requestLogSearch = "gpt";

  app.applyLiveLogEvent({
    seq: 1,
    type: "request_log.started",
    data: { id: "request-log-1", request_id: "req-1" },
  });
  assert.equal(app.requestLog.entries.length, 0);

  app.requestLogSearch = "";
  app.requestLog.offset = 25;
  app.applyLiveLogEvent({
    seq: 2,
    type: "request_log.started",
    data: { id: "request-log-2", request_id: "req-2" },
  });
  assert.equal(app.requestLog.entries.length, 0);
});

test("paused list insertion still updates normalized records", () => {
  const app = createLiveLogsApp({ requestLogSearch: "filtered" });
  app.applyLiveLogEvent({
    seq: 1,
    type: "request_log.updated",
    data: {
      id: "request-log-hidden",
      session_id: "session-open-in-drawer",
      data: { request_body: { input: "keep following me" } },
    },
  });

  assert.deepEqual(app.requestLog.entries, []);
  assert.equal(
    app.requestLogRecords["request-log-hidden"].data.request_body.input,
    "keep following me",
  );
});

test("live request log inserts cap the preview buffer at the page limit", () => {
  const app = createLiveLogsApp();
  app.requestLog.limit = 3;

  for (let i = 1; i <= 5; i++) {
    app.applyLiveLogEvent({
      seq: i,
      type: "request_log.started",
      data: { id: "request-log-" + i, request_id: "req-" + i },
    });
  }

  assert.equal(app.requestLog.entries.length, 3);
  assert.equal(app.requestLog.total, 5);
  assert.equal(app.requestLog.entries[0].id, "request-log-5");
  assert.equal(app.requestLog.entries[2].id, "request-log-3");
});

test("live logs parser handles CRLF-separated SSE frames", async () => {
  const app = createLiveLogsApp();

  await app.consumeLiveLogsBody(liveReaderFromChunks([
    'data: {"seq":1,"type":"heartbeat"}\r\n\r\n',
    'data: {"seq":2,"type":"request_log.started","data":{"id":"request-log-1","request_id":"req-1"}}\r\n\r\n',
  ]));

  assert.equal(app.liveLogsLastSeq, 2);
  assert.equal(app.requestLog.entries.length, 1);
  assert.equal(app.requestLog.entries[0].id, "request-log-1");
});

test("live logs parser joins split chunks and flushes a trailing frame", async () => {
  const app = createLiveLogsApp();

  await app.consumeLiveLogsBody(liveReaderFromChunks([
    'data: {"seq":1,"type":"request_log.star',
    'ted","data":{"id":"request-log-1","request_id":"req-1"}}\n\ndata: {"seq":2,"type":"heartbeat"}',
  ]));

  assert.equal(app.liveLogsLastSeq, 2);
  assert.equal(app.requestLog.entries.length, 1);
});

test("live usage event updates usage log and enriches matching request row", () => {
  const app = createLiveLogsApp();
  app.requestLog.entries = [{ id: "request-log-1", request_id: "req-1" }];

  app.applyLiveLogEvent({
    seq: 5,
    type: "usage.completed",
    data: {
      id: "usage-1",
      request_id: "req-1",
      model: "gpt-test",
      provider: "openai",
      input_tokens: 10,
      output_tokens: 4,
      total_tokens: 14,
    },
  });

  assert.equal(app.usageLog.entries.length, 1);
  assert.equal(app.usageLog.entries[0].id, "usage-1");
  assert.equal(app.requestLog.entries[0].usage.total_tokens, 14);
  assert.equal(app.requestLog.entries[0]._usage_live_pending, true);

  app.applyLiveLogEvent({
    seq: 6,
    type: "usage.flushed",
    data: {
      id: "usage-1",
      request_id: "req-1",
      model: "gpt-test",
      provider: "openai",
      input_tokens: 10,
      output_tokens: 4,
      total_tokens: 14,
    },
  });

  assert.equal(app.usageLog.entries[0]._live_pending, false);
  assert.equal(app.requestLog.entries[0]._usage_flushed, true);
});

test("active session filtering pauses non-matching live usage inserts", () => {
  const app = createLiveLogsApp({ usageFilters: filtersWith({ session: "scoped-session" }) });

  app.applyLiveLogEvent({
    seq: 1,
    type: "usage.completed",
    data: { id: "usage-1", request_id: "req-1", session_id: "other-session" },
  });

  assert.equal(app.usageLog.entries.length, 0);
  assert.equal(app.liveLogsLastSeq, 1);
});

test("active API key filtering pauses live usage inserts", () => {
  const app = createLiveLogsApp({ usageFilters: filtersWith({ authKeys: ["key-a"] }) });

  app.applyLiveLogEvent({
    seq: 1,
    type: "usage.completed",
    data: { id: "usage-1", request_id: "req-1", auth_key_id: "key-b" },
  });

  assert.equal(app.usageLog.entries.length, 0);
  assert.equal(app.liveLogsLastSeq, 1);
});

test("active multi-value filters pause live usage inserts; empty ones do not", () => {
  const filtered = createLiveLogsApp({ usageFilters: filtersWith({ labels: ["dev"], userPaths: ["/team"] }) });
  filtered.applyLiveLogEvent({
    seq: 1,
    type: "usage.completed",
    data: { id: "usage-1", request_id: "req-1" },
  });
  assert.equal(filtered.usageLog.entries.length, 0);

  const open = createLiveLogsApp({ usageFilters: filtersWith({ labels: [], models: [] }) });
  assert.equal(open.usageLiveInsertAllowed(), true);
});

test("active endpoint filtering pauses live usage inserts", () => {
  const app = createLiveLogsApp({ usageFilters: filtersWith({ endpoints: ["/v1/audio/speech"] }) });

  app.applyLiveLogEvent({
    seq: 1,
    type: "usage.completed",
    data: { id: "usage-1", request_id: "req-1", endpoint: "/v1/chat/completions" },
  });

  assert.equal(app.usageLog.entries.length, 0);
  assert.equal(app.liveLogsLastSeq, 1);
});

test("usage events forward the type to the live-token hook", () => {
  const app = createLiveLogsApp();
  const noted = [];
  app.noteLiveTokenUsage = (type) => noted.push(type);

  app.applyLiveLogEvent({
    seq: 1,
    type: "usage.completed",
    data: { id: "usage-1", request_id: "req-1", total_tokens: 14 },
  });

  assert.deepEqual(noted, ["usage.completed"]);
});

test("cached live usage events stay visible in default usage preview", () => {
  const app = createLiveLogsApp();
  app.requestLog.entries = [{
    id: "request-log-cache",
    request_id: "req-cache",
    cache_type: "exact",
    _live: true,
  }];

  app.applyLiveLogEvent({
    seq: 7,
    type: "usage.completed",
    data: {
      id: "usage-cache",
      request_id: "req-cache",
      cache_type: "exact",
      model: "gpt-test",
      provider: "openai",
      input_tokens: 10,
      output_tokens: 4,
      total_tokens: 14,
      total_cost: 0,
    },
  });

  assert.equal(app.liveLogsLastSeq, 7);
  assert.equal(app.usageLog.entries.length, 1);
  assert.equal(app.usageLog.total, 1);
  assert.equal(app.usageLog.entries[0].id, "usage-cache");
  assert.equal(app.usageLog.entries[0]._live_pending, true);
  assert.equal(app.requestLog.entries[0].usage.total_tokens, 14);
  assert.equal(app.requestLog.entries[0]._usage_live_pending, true);
  assert.equal(app.requestLog.entries[0]._usage_live_state, "usage.completed");
});

test("hidden cached live usage attaches to later request log previews", () => {
  const app = createLiveLogsApp();
  app.usageLogHideCached = true;

  app.applyLiveLogEvent({
    seq: 11,
    type: "usage.completed",
    data: {
      id: "usage-cache",
      request_id: "req-cache",
      cache_type: "exact",
      model: "gpt-test",
      provider: "openai",
      input_tokens: 10,
      output_tokens: 4,
      total_tokens: 14,
    },
  });

  assert.equal(app.usageLog.entries.length, 0);

  app.applyLiveLogEvent({
    seq: 12,
    type: "request_log.completed",
    data: {
      id: "request-log-cache",
      request_id: "req-cache",
      cache_type: "exact",
      status_code: 200,
    },
  });

  assert.equal(app.requestLog.entries[0].usage.total_tokens, 14);
  assert.equal(app.requestLog.entries[0]._usage_live_state, "usage.completed");
  assert.equal(app.requestLog.entries[0]._usage_live_pending, true);
  assert.equal(app.skippedLiveUsageByRequestId["req-cache"], undefined);

  app.applyLiveLogEvent({
    seq: 13,
    type: "usage.flushed",
    data: {
      id: "usage-cache",
      request_id: "req-cache",
      cache_type: "exact",
    },
  });

  assert.equal(app.usageLog.entries.length, 0);
  assert.equal(app.requestLog.entries[0].usage.total_tokens, 14);
  assert.equal(app.requestLog.entries[0]._usage_flushed, true);
  assert.equal(app.requestLog.entries[0]._usage_live_pending, false);

  app.usageLogHideCached = false;
  app.applyLiveLogEvent({
    seq: 14,
    type: "usage.flushed",
    data: {
      id: "usage-cache",
      request_id: "req-cache",
      cache_type: "exact",
    },
  });

  assert.equal(app.usageLog.entries.length, 1);
  assert.equal(app.usageLog.entries[0].total_tokens, 14);
  assert.equal(app.skippedLiveUsageByRequestId["req-cache"], undefined);
});

test("cached updates to visible live usage rows move to skipped when cached rows are hidden", () => {
  const app = createLiveLogsApp();
  app.requestLog.entries = [{ id: "request-log-cache", request_id: "req-cache" }];

  app.applyLiveLogEvent({
    seq: 15,
    type: "usage.completed",
    data: {
      id: "usage-cache",
      request_id: "req-cache",
      model: "gpt-test",
      provider: "openai",
      input_tokens: 10,
      output_tokens: 4,
      total_tokens: 14,
    },
  });

  assert.equal(app.usageLog.entries.length, 1);
  assert.equal(app.usageLog.total, 1);

  app.usageLogHideCached = true;
  app.applyLiveLogEvent({
    seq: 16,
    type: "usage.flushed",
    data: {
      id: "usage-cache",
      request_id: "req-cache",
      cache_type: "exact",
    },
  });

  assert.equal(app.usageLog.entries.length, 0);
  assert.equal(app.usageLog.total, 0);
  assert.equal(app.requestLog.entries[0]._usage_flushed, true);
  assert.equal(app.requestLog.entries[0].usage.total_tokens, 14);
  assert.equal(app.skippedLiveUsageByRequestId["req-cache"].total_tokens, 14);
});

test("live usage request log summary uses normalized split prompt-cache tokens", () => {
  const app = createLiveLogsApp();
  app.requestLog.entries = [{ id: "request-log-cache", request_id: "req-cache" }];

  app.applyLiveLogEvent({
    seq: 17,
    type: "usage.completed",
    data: {
      id: "usage-cache",
      request_id: "req-cache",
      input_tokens: 100,
      uncached_input_tokens: 100,
      cached_input_tokens: 50,
      cache_write_input_tokens: 25,
      output_tokens: 20,
      total_tokens: 120,
    },
  });

  const summary = app.requestLog.entries[0].usage;
  assert.equal(summary.input_tokens, 175);
  assert.equal(summary.uncached_input_tokens, 100);
  assert.equal(summary.cached_input_tokens, 50);
  assert.equal(summary.cache_write_input_tokens, 25);
  assert.equal(summary.output_tokens, 20);
  assert.equal(summary.total_tokens, 195);
  assert.equal(summary.cached_input_ratio, 50 / 175);
  assert.equal(summary.estimated_cached_characters, 200);
});

test("request log detail merge preserves existing live lifecycle state", () => {
  const app = createLiveLogsApp();
  app.requestLog.entries = [{
    id: "request-log-1",
    request_id: "req-1",
    _live: true,
    _live_state: "request_log.completed",
    _live_pending: true,
    _request_log_flushed: false,
    data: {
      workflow_features: { cache: true },
    },
  }];

  const merged = app.mergeLiveRequestEntry({
    id: "request-log-1",
    request_id: "req-1",
    data: {
      request_headers: { authorization: "Bearer redacted" },
    },
  }, "request_log.detail");

  assert.equal(merged._live, true);
  assert.equal(merged._live_state, "request_log.completed");
  assert.equal(merged._live_pending, true);
  assert.equal(merged._request_log_flushed, false);
  assert.equal(merged._detail_loaded, true);
  assert.deepEqual(merged.data.workflow_features, { cache: true });
  assert.deepEqual(merged.data.request_headers, { authorization: "Bearer redacted" });
});

test("live request log lifecycle patches merge captured detail data", () => {
  const app = createLiveLogsApp();
  app.requestLog.entries = [{
    id: "request-log-1",
    request_id: "req-1",
    _detail_loaded: true,
    data: {
      request_headers: { authorization: "Bearer redacted" },
      response_body: { id: "chatcmpl_123" },
    },
  }];

  app.applyLiveLogEvent({
    seq: 9,
    type: "request_log.flushed",
    data: {
      id: "request-log-1",
      request_id: "req-1",
      status_code: 200,
      data: {
        response_headers: { "x-request-id": "req-1" },
      },
    },
  });

  assert.equal(app.requestLog.entries[0].status_code, 200);
  assert.equal(app.requestLog.entries[0]._live_state, "request_log.flushed");
  assert.equal(app.requestLog.entries[0]._live_pending, false);
  assert.deepEqual(app.requestLog.entries[0].data.request_headers, { authorization: "Bearer redacted" });
  assert.deepEqual(app.requestLog.entries[0].data.response_headers, { "x-request-id": "req-1" });
  assert.deepEqual(app.requestLog.entries[0].data.response_body, { id: "chatcmpl_123" });
});

test("late queued events do not regress flushed live rows to pending", () => {
  const app = createLiveLogsApp();

  app.applyLiveLogEvent({
    seq: 1,
    type: "request_log.flushed",
    data: { id: "request-log-1", request_id: "req-1", status_code: 200, duration_ns: 1000 },
  });
  app.applyLiveLogEvent({
    seq: 2,
    type: "request_log.completed",
    data: { id: "request-log-1", request_id: "req-1", status_code: 200, duration_ns: 1000 },
  });

  assert.equal(app.requestLog.entries[0]._live_state, "request_log.flushed");
  assert.equal(app.requestLog.entries[0]._live_pending, false);
  assert.equal(app.requestLog.entries[0]._request_log_flushed, true);

  app.requestLog.entries[0].usage = { entries: 1 };
  app.applyLiveLogEvent({
    seq: 3,
    type: "usage.flushed",
    data: { id: "usage-1", request_id: "req-1", total_tokens: 14 },
  });
  app.applyLiveLogEvent({
    seq: 4,
    type: "usage.completed",
    data: { id: "usage-1", request_id: "req-1", total_tokens: 14 },
  });

  assert.equal(app.usageLog.entries[0]._live_state, "usage.flushed");
  assert.equal(app.usageLog.entries[0]._live_pending, false);
  assert.equal(app.usageLog.entries[0]._usage_flushed, true);
  assert.equal(app.requestLog.entries[0]._usage_live_state, "usage.flushed");
  assert.equal(app.requestLog.entries[0]._usage_live_pending, false);
  assert.equal(app.requestLog.entries[0]._usage_flushed, true);
});

test("flushed expanded live request rows retry detail fetch", () => {
  const app = createLiveLogsApp();
  let detailFetches = 0;
  app.requestLog.entries = [{
    id: "request-log-1",
    request_id: "req-1",
    _live: true,
    _live_state: "request_log.completed",
    _live_pending: true,
    _request_log_flushed: false,
  }];
  app.isRequestEntryExpanded = (entry) => String(entry && entry.id || "") === "request-log-1";
  app.fetchRequestEntryDetail = (entry) => {
    detailFetches++;
    assert.equal(entry.id, "request-log-1");
    assert.equal(entry._live_state, "request_log.flushed");
  };

  app.applyLiveLogEvent({
    seq: 5,
    type: "request_log.flushed",
    data: { id: "request-log-1", request_id: "req-1", status_code: 200 },
  });

  assert.equal(detailFetches, 1);
});

test("failed live events clear pending state", () => {
  const app = createLiveLogsApp();

  app.applyLiveLogEvent({
    seq: 1,
    type: "request_log.completed",
    data: { id: "request-log-1", request_id: "req-1" },
  });
  app.applyLiveLogEvent({
    seq: 2,
    type: "request_log.failed",
    data: { id: "request-log-1", request_id: "req-1" },
  });
  app.applyLiveLogEvent({
    seq: 3,
    type: "usage.completed",
    data: { id: "usage-1", request_id: "req-1", total_tokens: 14 },
  });
  app.applyLiveLogEvent({
    seq: 4,
    type: "usage.failed",
    data: { id: "usage-1", request_id: "req-1", total_tokens: 14 },
  });

  assert.equal(app.requestLog.entries[0]._live_state, "request_log.failed");
  assert.equal(app.requestLog.entries[0]._live_pending, false);
  assert.equal(app.usageLog.entries[0]._live_state, "usage.failed");
  assert.equal(app.usageLog.entries[0]._live_pending, false);
  assert.equal(app.requestLog.entries[0]._usage_live_state, "usage.failed");
  assert.equal(app.requestLog.entries[0]._usage_live_pending, false);
});

test("cached live usage events are suppressed when hide-cached toggle is on", () => {
  const app = createLiveLogsApp();
  app.usageLogHideCached = true;

  app.applyLiveLogEvent({
    seq: 10,
    type: "usage.completed",
    data: {
      id: "usage-cache",
      request_id: "req-cache",
      cache_type: "exact",
      model: "gpt-test",
      provider: "openai",
      input_tokens: 10,
      output_tokens: 4,
      total_tokens: 14,
    },
  });

  assert.equal(app.usageLog.entries.length, 0);
  assert.equal(app.usageLog.total, 0);

  app.applyLiveLogEvent({
    seq: 11,
    type: "usage.completed",
    data: {
      id: "usage-fresh",
      request_id: "req-fresh",
      model: "gpt-test",
      provider: "openai",
      input_tokens: 10,
      output_tokens: 4,
      total_tokens: 14,
    },
  });

  assert.equal(app.usageLog.entries.length, 1);
  assert.equal(app.usageLog.entries[0].id, "usage-fresh");
});

test("live reset asks normal REST endpoints to resync source of truth", () => {
  const app = createLiveLogsApp();

  app.applyLiveLogEvent({ seq: 8, type: "reset" });

  assert.equal(app.liveLogsLastSeq, 8);
  assert.equal(app.fetchUsageCalls, 1);
  assert.equal(app.fetchRequestLogCalls, 1);
});

test("request log detail fetch gating: skips captured data, waits for live flush", () => {
  const app = createLiveLogsApp();

  // Already-captured detail data: no fetch.
  assert.equal(app.requestEntryShouldFetchDetail({
    id: "request-log-1",
    request_id: "req-1",
    data: { request_headers: { authorization: "Bearer redacted" } },
  }), false);

  // Live row still streaming: wait for the flush.
  assert.equal(app.requestEntryShouldFetchDetail({
    id: "request-log-1",
    _live: true,
    _live_state: "request_log.updated",
    _live_pending: true,
    _request_log_flushed: false,
    data: { request_body: { model: "gpt-test" } },
  }), false);

  // Flushed live row fetches persisted detail even with preview data.
  assert.equal(app.requestEntryShouldFetchDetail({
    id: "request-log-1",
    _live: true,
    _live_state: "request_log.flushed",
    _live_pending: false,
    _request_log_flushed: true,
    data: { request_body: { model: "gpt-test" } },
  }), true);

  // Compact (workflow-only) data on a persisted row fetches detail.
  assert.equal(app.requestEntryShouldFetchDetail({
    id: "request-log-1",
    data: { workflow_features: { cache: true } },
  }), true);
});

// --- Session-thread grouping -------------------------------------------------

test("grouped mode folds a new live entry into its on-screen thread", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true });
  app.requestLog.entries = [
    { id: "head-a", request_id: "req-1", session_id: "s-a", session_count: 2 },
    { id: "other", session_id: "s-b", session_count: 1 },
  ];
  app.requestLog.total = 2;
  app.requestThreadChildren = {
    "s-a": { loading: false, entries: [{ id: "old-child" }], total: 2 },
  };

  app.mergeLiveRequestEntry(
    { id: "live-3", request_id: "req-3", session_id: "s-a", path: "/v1/chat/completions" },
    "request_log.started",
  );

  // Thread bubbles to the top with the new entry as head and count bumped.
  assert.deepEqual(
    app.requestLog.entries.map((entry) => entry.id),
    ["live-3", "other"],
  );
  assert.equal(app.requestLog.entries[0].session_count, 3);
  assert.equal(app.requestLog.entries[0]._live_pending, true);
  // Total is unchanged: same number of threads.
  assert.equal(app.requestLog.total, 2);
  // The displaced head moved into the loaded children list, count-free.
  const children = app.requestThreadChildren["s-a"];
  assert.deepEqual(children.entries.map((entry) => entry.id), ["head-a", "old-child"]);
  assert.equal(children.entries[0].session_count, undefined);
  assert.equal(children.total, 3);
});

test("parent headers do not assign sessions before the server resolves them", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true });
  app.requestLog.entries = [
    { id: "head-a", session_id: "s-a", session_count: 2 },
  ];

  app.mergeLiveRequestEntry({
    id: "live-3",
    request_id: "req-3",
    path: "/v1/chat/completions",
    data: { request_headers: { "X-GoModel-Interaction-Parent": "head-a" } },
  }, "request_log.started");

  assert.equal(app.requestLog.entries.length, 2);
  assert.equal(app.requestLog.entries[0].id, "live-3");
  assert.equal(app.requestLog.entries[0].session_id, undefined);
  assert.equal(app.requestLog.entries[1].id, "head-a");
});

test("grouped fold retains the displaced head of an unexpanded thread", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true });
  app.requestLog.entries = [
    { id: "head-a", session_id: "s-a", session_count: 2 },
  ];
  app.requestLog.total = 1;

  app.mergeLiveRequestEntry({ id: "live-2", session_id: "s-a" }, "request_log.started");

  assert.equal(app.requestLog.entries[0].id, "live-2");
  assert.equal(app.requestLog.entries[0].session_count, 3);
  // The displaced head lands in a partial children list (loaded: false keeps
  // the lazy full fetch armed) so its later live events merge in place
  // instead of re-counting as new session members.
  const children = app.requestThreadChildren["s-a"];
  assert.equal(children.loaded, false);
  assert.equal(children.loading, false);
  assert.deepEqual(children.entries.map((entry) => entry.id), ["head-a"]);
  assert.equal(children.entries[0].session_count, undefined);
  assert.equal(children.total, 1);
});

test("grouped mode without a matching thread prepends a singleton head", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true });
  app.requestLog.entries = [{ id: "head-a", session_id: "s-a", session_count: 2 }];
  app.requestLog.total = 1;

  app.mergeLiveRequestEntry({ id: "live-9", session_id: "s-new" }, "request_log.started");

  assert.deepEqual(
    app.requestLog.entries.map((entry) => entry.id),
    ["live-9", "head-a"],
  );
  assert.equal(app.requestLog.total, 2);
});

test("flat mode never folds by session", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: false });
  app.requestLog.entries = [{ id: "row-1", session_id: "s-a" }];
  app.requestLog.total = 1;

  app.mergeLiveRequestEntry({ id: "row-2", session_id: "s-a" }, "request_log.started");

  assert.deepEqual(
    app.requestLog.entries.map((entry) => entry.id),
    ["row-2", "row-1"],
  );
  assert.equal(app.requestLog.total, 2);
});

test("grouped fold respects the live insert gate", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true, requestLogSearch: "x" });
  app.requestLog.entries = [{ id: "head-a", session_id: "s-a", session_count: 2 }];
  app.requestLog.total = 1;

  app.mergeLiveRequestEntry({ id: "live-3", session_id: "s-a" }, "request_log.started");

  assert.deepEqual(app.requestLog.entries.map((entry) => entry.id), ["head-a"]);
  assert.equal(app.requestLog.entries[0].session_count, 2);
});

test("live updates merge into displaced entries living in children lists", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true });
  app.requestLog.entries = [{ id: "head", session_id: "s-a", session_count: 2 }];
  app.requestThreadChildren = {
    "s-a": {
      loading: false,
      entries: [{ id: "child-1", request_id: "req-c1", status_code: null }],
      total: 2,
    },
  };

  app.mergeLiveRequestEntry(
    { id: "child-1", request_id: "req-c1", status_code: 200 },
    "request_log.flushed",
  );

  const child = app.requestThreadChildren["s-a"].entries[0];
  assert.equal(child.status_code, 200);
  assert.equal(child._request_log_flushed, true);
  // The head list is untouched.
  assert.deepEqual(app.requestLog.entries.map((entry) => entry.id), ["head"]);
});

test("late usage updates enrich displaced entries living in children lists", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true });
  app.requestLog.entries = [{ id: "head", request_id: "req-head", session_id: "s-a", session_count: 2 }];
  app.requestThreadChildren = {
    "s-a": {
      loading: false,
      entries: [{ id: "child-1", request_id: "req-c1" }],
      total: 2,
    },
  };

  app.mergeLiveUsageEntry(
    { id: "usage-1", request_id: "req-c1", input_tokens: 10, output_tokens: 4 },
    "usage.completed",
  );

  const child = app.requestThreadChildren["s-a"].entries[0];
  assert.equal(child.usage.input_tokens, 10);
  assert.equal(child.usage.output_tokens, 4);
  assert.equal(child.usage.total_tokens, 14);
  assert.equal(app.requestLog.entries[0].usage, undefined);
});

test("request_log.detail events hydrate children-list entries", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true });
  app.requestThreadChildren = {
    "s-a": { loading: false, entries: [{ id: "child-1" }], total: 2 },
  };

  const merged = app.mergeLiveRequestEntry(
    { id: "child-1", data: { request_body: { model: "gpt-4o" } } },
    "request_log.detail",
  );

  assert.equal(merged._detail_loaded, true);
  assert.deepEqual(
    app.requestThreadChildren["s-a"].entries[0].data.request_body,
    { model: "gpt-4o" },
  );
});

test("request_log.removed cleans children lists and decrements the head count", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true });
  app.requestLog.entries = [{ id: "head", session_id: "s-a", session_count: 3 }];
  app.requestLog.total = 1;
  app.requestThreadChildren = {
    "s-a": {
      loading: false,
      entries: [{ id: "child-1" }, { id: "child-2" }],
      total: 3,
    },
  };

  app.removeLiveRequestEntry({ id: "child-1" });

  assert.deepEqual(
    app.requestThreadChildren["s-a"].entries.map((entry) => entry.id),
    ["child-2"],
  );
  assert.equal(app.requestThreadChildren["s-a"].total, 2);
  assert.equal(app.requestLog.entries[0].session_count, 2);
  // Head list itself is untouched by a child removal.
  assert.equal(app.requestLog.total, 1);
});

test("request_log.removed promotes a loaded child when a grouped head disappears", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true });
  app.requestLog.entries = [{ id: "head", session_id: "s-a", session_count: 3 }];
  app.requestLog.total = 1;
  app.requestThreadChildren = {
    "s-a": {
      loading: false,
      entries: [
        { id: "child-2", session_id: "s-a" },
        { id: "child-1", session_id: "s-a" },
      ],
      total: 3,
    },
  };

  app.removeLiveRequestEntry({ id: "head" });

  assert.deepEqual(app.requestLog.entries.map((entry) => entry.id), ["child-2"]);
  assert.equal(app.requestLog.entries[0].session_count, 2);
  assert.equal(app.requestLog.total, 1);
  assert.deepEqual(
    app.requestThreadChildren["s-a"].entries.map((entry) => entry.id),
    ["child-1"],
  );
  assert.equal(app.requestThreadChildren["s-a"].total, 2);
});

test("request_log.removed reloads an unexpanded grouped thread when its head disappears", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true });
  app.requestLog.entries = [{ id: "head", session_id: "s-a", session_count: 3 }];
  app.requestLog.total = 1;

  app.removeLiveRequestEntry({ id: "head" });

  assert.deepEqual(app.requestLog.entries, []);
  assert.equal(app.requestLog.total, 1);
  assert.equal(app.fetchRequestLogCalls, 1);
});

test("a sessionless live row re-folds on the pre-response session update", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true });
  app.requestLog.entries = [{ id: "head-a", session_id: "s-a", session_count: 2 }];
  app.requestLog.total = 1;
  app.requestThreadChildren = {
    "s-a": { loading: false, entries: [{ id: "old-child" }], total: 2 },
  };

  // request_log.started fires before session detection: the row arrives sessionless
  // and prepends as its own singleton thread.
  app.mergeLiveRequestEntry({ id: "live-1", request_id: "req-1" }, "request_log.started");
  assert.equal(app.requestLog.entries.length, 2);
  assert.equal(app.requestLog.total, 2);

  // SessionCapture delivers request_log.updated before the provider handler runs:
  // the row folds without waiting for a response or terminal event.
  app.mergeLiveRequestEntry(
    { id: "live-1", request_id: "req-1", session_id: "s-a" },
    "request_log.updated",
  );

  assert.deepEqual(app.requestLog.entries.map((entry) => entry.id), ["live-1"]);
  assert.equal(app.requestLog.entries[0].session_count, 3);
  assert.equal(app.requestLog.total, 1);
  // The displaced head moved into the loaded children.
  assert.deepEqual(
    app.requestThreadChildren["s-a"].entries.map((entry) => entry.id),
    ["head-a", "old-child"],
  );
});

test("re-fold leaves rows alone in flat mode and without a matching head", () => {
  const flat = createLiveLogsApp({ requestLogGroupSessions: false });
  flat.requestLog.entries = [{ id: "row-a", session_id: "s-a" }];
  flat.requestLog.total = 1;
  flat.mergeLiveRequestEntry({ id: "row-a", session_id: "s-a", status_code: 200 }, "request_log.flushed");
  assert.equal(flat.requestLog.entries.length, 1);

  const grouped = createLiveLogsApp({ requestLogGroupSessions: true });
  grouped.requestLog.entries = [{ id: "solo", session_id: "s-new" }];
  grouped.requestLog.total = 1;
  grouped.mergeLiveRequestEntry({ id: "solo", session_id: "s-new", status_code: 200 }, "request_log.flushed");
  assert.deepEqual(grouped.requestLog.entries.map((entry) => entry.id), ["solo"]);
  assert.equal(grouped.requestLog.total, 1);
});

test("interleaved first-session requests do not inflate the thread count", () => {
  // Regression: two live requests open a brand-new session while the page is
  // watching. Both request_log.started events arrive sessionless, later events
  // deliver the session id, and the demoted row keeps producing lifecycle
  // events. Before the fix each late event for a displaced (and dropped) row
  // re-counted it as a new session member, inflating the badge 2x-3x.
  const app = createLiveLogsApp({ requestLogGroupSessions: true });

  app.mergeLiveRequestEntry(
    { id: "a", request_id: "req-a", timestamp: "2026-07-28T10:00:00Z" },
    "request_log.started",
  );
  app.mergeLiveRequestEntry(
    { id: "b", request_id: "req-b", timestamp: "2026-07-28T10:00:02Z" },
    "request_log.started",
  );
  app.mergeLiveRequestEntry(
    { id: "b", request_id: "req-b", session_id: "s-1", timestamp: "2026-07-28T10:00:02Z" },
    "request_log.updated",
  );
  // A gains the session id: the two rows collapse into one thread and A is
  // demoted under B (the newer request).
  app.mergeLiveRequestEntry(
    { id: "a", request_id: "req-a", session_id: "s-1", timestamp: "2026-07-28T10:00:00Z" },
    "request_log.updated",
  );
  assert.deepEqual(app.requestLog.entries.map((entry) => entry.id), ["b"]);
  assert.equal(app.requestLog.entries[0].session_count, 2);
  assert.equal(app.requestLog.total, 1);

  // Late lifecycle events for both requests merge in place: the count and the
  // thread total must not move again.
  app.mergeLiveRequestEntry(
    { id: "a", request_id: "req-a", session_id: "s-1", status_code: 200 },
    "request_log.completed",
  );
  app.mergeLiveRequestEntry(
    { id: "b", request_id: "req-b", session_id: "s-1", status_code: 200 },
    "request_log.completed",
  );
  app.mergeLiveRequestEntry(
    { id: "a", request_id: "req-a", session_id: "s-1", status_code: 200 },
    "request_log.flushed",
  );
  app.mergeLiveRequestEntry(
    { id: "b", request_id: "req-b", session_id: "s-1", status_code: 200 },
    "request_log.flushed",
  );

  assert.deepEqual(app.requestLog.entries.map((entry) => entry.id), ["b"]);
  assert.equal(app.requestLog.entries[0].session_count, 2);
  assert.equal(app.requestLog.entries[0]._request_log_flushed, true);
  assert.equal(app.requestLog.total, 1);
  const children = app.requestThreadChildren["s-1"];
  assert.deepEqual(children.entries.map((entry) => entry.id), ["a"]);
  assert.equal(children.entries[0]._request_log_flushed, true);
  assert.equal(children.loaded, false);
});

test("re-fold keeps the newest request as head when completions arrive out of order", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true });
  // A starts first, B starts later; both sessionless.
  app.mergeLiveRequestEntry(
    { id: "req-a", timestamp: "2026-07-27T10:00:00Z" },
    "request_log.started",
  );
  app.mergeLiveRequestEntry(
    { id: "req-b", timestamp: "2026-07-27T10:00:05Z" },
    "request_log.started",
  );
  // B completes first and gains the session id (no other head yet: stays put).
  app.mergeLiveRequestEntry(
    { id: "req-b", timestamp: "2026-07-27T10:00:05Z", session_id: "s-a", status_code: 200 },
    "request_log.flushed",
  );
  // A completes last: it must fold UNDER B, which is the newer request.
  app.mergeLiveRequestEntry(
    { id: "req-a", timestamp: "2026-07-27T10:00:00Z", session_id: "s-a", status_code: 200 },
    "request_log.flushed",
  );

  assert.deepEqual(app.requestLog.entries.map((entry) => entry.id), ["req-b"]);
  assert.equal(app.requestLog.entries[0].session_count, 2);
  assert.equal(app.requestLog.total, 1);
});

test("live guardrail outcomes survive the merge into the request row", () => {
  const app = createLiveLogsApp();

  app.applyLiveLogEvent({
    seq: 1,
    type: "request_log.started",
    data: { id: "request-log-1", request_id: "req-1", method: "POST", path: "/v1/chat/completions" },
  });
  app.applyLiveLogEvent({
    seq: 2,
    type: "request_log.completed",
    data: {
      id: "request-log-1",
      request_id: "req-1",
      status_code: 403,
      duration_ns: 1000,
      data: {
        guardrails: [
          { seq: 1, phase: "prompt", step: 10, instance: "policy", action: "block", code: "content_policy" },
        ],
      },
    },
  });

  const completed = app.requestLog.entries[0];
  assert.equal(completed.data.guardrails.length, 1);
  assert.equal(completed.data.guardrails[0].action, "block");

  // The flush carries no data of its own: the outcomes stay on the row.
  app.applyLiveLogEvent({
    seq: 3,
    type: "request_log.flushed",
    data: { id: "request-log-1", request_id: "req-1", status_code: 403, duration_ns: 1000 },
  });
  const flushed = app.requestLog.entries[0];
  assert.equal(flushed._live_state, "request_log.flushed");
  assert.equal(flushed.data.guardrails[0].instance, "policy");
  assert.equal(flushed.data.guardrails[0].code, "content_policy");
});

// --- Other replicas' traffic ---------------------------------------------------
// A request served by another replica reaches this stream only once that
// replica persisted it, seconds late, as request_log.flushed / usage.flushed.

test("a late live request log entry is placed by its timestamp, not on top", () => {
  const app = createLiveLogsApp();
  app.requestLog.entries = [
    { id: "newest", timestamp: "2026-10-30T12:00:09Z" },
    { id: "older", timestamp: "2026-10-30T12:00:01Z" },
  ];
  app.requestLog.total = 2;

  app.applyLiveLogEvent({
    seq: 7,
    type: "request_log.flushed",
    data: { id: "remote", request_id: "req-remote", timestamp: "2026-10-30T12:00:05Z" },
  });

  assert.deepEqual(app.requestLog.entries.map((entry) => entry.id), ["newest", "remote", "older"]);
  assert.equal(app.requestLog.total, 3);
  assert.equal(app.requestLog.entries[1]._request_log_flushed, true);

  // A later event for an entry already listed merges in place.
  app.applyLiveLogEvent({
    seq: 8,
    type: "request_log.flushed",
    data: { id: "remote", request_id: "req-remote", timestamp: "2026-10-30T12:00:05Z", status_code: 200 },
  });
  assert.deepEqual(app.requestLog.entries.map((entry) => entry.id), ["newest", "remote", "older"]);
  assert.equal(app.requestLog.total, 3);
  assert.equal(app.requestLog.entries[1].status_code, 200);
});

test("a late live request log entry older than the whole page only counts", () => {
  const app = createLiveLogsApp();
  app.requestLog.limit = 2;
  app.requestLog.entries = [
    { id: "a", timestamp: "2026-10-30T12:00:09Z" },
    { id: "b", timestamp: "2026-10-30T12:00:08Z" },
  ];
  app.requestLog.total = 2;

  app.mergeLiveRequestEntry({ id: "remote", timestamp: "2026-10-30T12:00:01Z" }, "request_log.flushed");

  assert.deepEqual(app.requestLog.entries.map((entry) => entry.id), ["a", "b"]);
  assert.equal(app.requestLog.total, 3);
});

test("a late live usage entry is placed by its timestamp", () => {
  const app = createLiveLogsApp({ page: "usage" });
  app.usageLog.entries = [
    { id: "u-new", request_id: "req-new", timestamp: "2026-10-30T12:00:09Z" },
    { id: "u-old", request_id: "req-old", timestamp: "2026-10-30T12:00:01Z" },
  ];
  app.usageLog.total = 2;

  app.applyLiveLogEvent({
    seq: 3,
    type: "usage.flushed",
    data: { id: "u-remote", request_id: "req-remote", timestamp: "2026-10-30T12:00:05Z" },
  });

  assert.deepEqual(app.usageLog.entries.map((entry) => entry.id), ["u-new", "u-remote", "u-old"]);
  assert.equal(app.usageLog.total, 3);
  assert.equal(app.usageLog.entries[1]._usage_flushed, true);
});

test("grouped mode adds a late request older than its thread head as a child", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true });
  app.requestLog.entries = [
    { id: "other", session_id: "s-b", session_count: 1, timestamp: "2026-10-30T12:00:09Z" },
    { id: "head-a", session_id: "s-a", session_count: 2, timestamp: "2026-10-30T12:00:07Z" },
  ];
  app.requestLog.total = 2;
  app.requestThreadChildren = {
    "s-a": {
      loading: false,
      entries: [{ id: "old-child", timestamp: "2026-10-30T12:00:01Z" }],
      total: 1,
    },
  };

  app.mergeLiveRequestEntry(
    { id: "remote", session_id: "s-a", timestamp: "2026-10-30T12:00:05Z" },
    "request_log.flushed",
  );

  // The head and the thread's position are kept; only the count moves.
  assert.deepEqual(app.requestLog.entries.map((entry) => entry.id), ["other", "head-a"]);
  assert.equal(app.requestLog.entries[1].session_count, 3);
  assert.equal(app.requestLog.total, 2);
  const children = app.requestThreadChildren["s-a"];
  assert.deepEqual(children.entries.map((entry) => entry.id), ["remote", "old-child"]);
  assert.equal(children.total, 2);
});

test("grouped mode places a late new thread head by its timestamp", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true });
  app.requestLog.entries = [
    { id: "other", session_id: "s-b", session_count: 1, timestamp: "2026-10-30T12:00:09Z" },
    { id: "head-a", session_id: "s-a", session_count: 1, timestamp: "2026-10-30T12:00:03Z" },
  ];
  app.requestLog.total = 2;

  app.mergeLiveRequestEntry(
    { id: "remote", session_id: "s-a", timestamp: "2026-10-30T12:00:05Z" },
    "request_log.flushed",
  );

  assert.deepEqual(app.requestLog.entries.map((entry) => entry.id), ["other", "remote"]);
  assert.equal(app.requestLog.entries[1].session_count, 2);
  assert.deepEqual(app.requestThreadChildren["s-a"].entries.map((entry) => entry.id), ["head-a"]);
});

// Another replica's entries arrive once persisted, marked with that
// replica's write stamp. A page the server read after the stamp (as_of)
// already counted them, so they must not be counted again.
const AS_OF = 1_800_000_000_000_000;

test("a remote entry the grouped page already counted joins its thread without a recount", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true });
  app.requestLog = {
    entries: [{ id: "head-a", session_id: "s-a", session_count: 2, timestamp: "2026-10-30T12:00:10Z" }],
    total: 1,
    limit: 25,
    offset: 0,
    as_of: AS_OF,
  };

  app.mergeLiveRequestEntry(
    { id: "child", session_id: "s-a", timestamp: "2026-10-30T12:00:05Z", _remote_written_at: AS_OF - 1 },
    "request_log.flushed",
  );

  assert.equal(app.requestLog.entries[0].session_count, 2, "the fetched count already holds it");
  assert.equal(app.requestLog.total, 1);
  assert.deepEqual(app.requestThreadChildren["s-a"].entries.map((entry) => entry.id), ["child"]);
});

test("a remote entry persisted after the grouped page was read is counted", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true });
  app.requestLog = {
    entries: [{ id: "head-a", session_id: "s-a", session_count: 2, timestamp: "2026-10-30T12:00:10Z" }],
    total: 1,
    limit: 25,
    offset: 0,
    as_of: AS_OF,
  };

  app.mergeLiveRequestEntry(
    { id: "child", session_id: "s-a", timestamp: "2026-10-30T12:00:05Z", _remote_written_at: AS_OF + 1 },
    "request_log.flushed",
  );

  assert.equal(app.requestLog.entries[0].session_count, 3);
});

test("a local entry is counted whatever the page's as_of", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true });
  app.requestLog = {
    entries: [{ id: "head-a", session_id: "s-a", session_count: 2, timestamp: "2026-10-30T12:00:10Z" }],
    total: 1,
    limit: 25,
    offset: 0,
    as_of: AS_OF,
  };

  app.mergeLiveRequestEntry(
    { id: "live", session_id: "s-a", timestamp: "2026-10-30T12:00:20Z" },
    "request_log.started",
  );

  assert.equal(app.requestLog.entries[0].id, "live");
  assert.equal(app.requestLog.entries[0].session_count, 3);
});

test("a fetched children list does not recount a remote entry it already counted", () => {
  const app = createLiveLogsApp({ requestLogGroupSessions: true });
  app.requestLog = {
    entries: [{ id: "head-a", session_id: "s-a", session_count: 3, timestamp: "2026-10-30T12:00:10Z" }],
    total: 1,
    limit: 25,
    offset: 0,
    as_of: AS_OF,
  };
  app.requestThreadChildren = {
    "s-a": {
      loading: false,
      loaded: true,
      entries: [{ id: "old-child", timestamp: "2026-10-30T12:00:01Z" }],
      total: 2,
      as_of: AS_OF,
    },
  };

  app.mergeLiveRequestEntry(
    { id: "counted", session_id: "s-a", timestamp: "2026-10-30T12:00:05Z", _remote_written_at: AS_OF },
    "request_log.flushed",
  );
  assert.equal(app.requestLog.entries[0].session_count, 3);
  assert.equal(app.requestThreadChildren["s-a"].total, 2);

  app.mergeLiveRequestEntry(
    { id: "new", session_id: "s-a", timestamp: "2026-10-30T12:00:06Z", _remote_written_at: AS_OF + 5 },
    "request_log.flushed",
  );
  assert.equal(app.requestLog.entries[0].session_count, 4);
  assert.equal(app.requestThreadChildren["s-a"].total, 3);
  assert.deepEqual(
    app.requestThreadChildren["s-a"].entries.map((entry) => entry.id),
    ["new", "counted", "old-child"],
  );
});

test("the flat list total counts a remote entry only when the page has not", () => {
  const app = createLiveLogsApp();
  app.requestLog = {
    entries: [
      { id: "b", timestamp: "2026-10-30T12:00:10Z" },
      { id: "a", timestamp: "2026-10-30T12:00:09Z" },
    ],
    total: 40,
    limit: 2,
    offset: 0,
    as_of: AS_OF,
  };

  // Below the page: counted in its total, not shown.
  app.mergeLiveRequestEntry(
    { id: "older", timestamp: "2026-10-30T12:00:01Z", _remote_written_at: AS_OF - 10 },
    "request_log.flushed",
  );
  assert.deepEqual(app.requestLog.entries.map((entry) => entry.id), ["b", "a"]);
  assert.equal(app.requestLog.total, 40);

  app.mergeLiveRequestEntry(
    { id: "newer", timestamp: "2026-10-30T12:00:11Z", _remote_written_at: AS_OF + 10 },
    "request_log.flushed",
  );
  assert.deepEqual(app.requestLog.entries.map((entry) => entry.id), ["newer", "b"]);
  assert.equal(app.requestLog.total, 41);
});

test("the usage total counts a remote entry only when the page has not", () => {
  const app = createLiveLogsApp();
  app.usageLog = { entries: [], total: 7, limit: 50, offset: 0, as_of: AS_OF };

  app.mergeLiveUsageEntry(
    { id: "counted", request_id: "req-counted", timestamp: "2026-10-30T12:00:01Z", _remote_written_at: AS_OF },
    "usage.flushed",
  );
  assert.equal(app.usageLog.total, 7);

  app.mergeLiveUsageEntry(
    { id: "new", request_id: "req-new", timestamp: "2026-10-30T12:00:02Z", _remote_written_at: AS_OF + 1 },
    "usage.flushed",
  );
  assert.equal(app.usageLog.total, 8);

  app.mergeLiveUsageEntry({ id: "local", request_id: "req-local" }, "usage.completed");
  assert.equal(app.usageLog.total, 9, "local entries are always new to the page");
});

// A live event for a usage row already on the page updates it in place,
// whatever the view: the server put it on this page under these filters, and
// a later event for the same request does not change what they matched.
function fetchedUsagePage(app, page) {
  app.usageLog = {
    entries: [
      { id: "usage-b", request_id: "req-b", timestamp: "2026-10-30T12:00:10Z", total_tokens: 5 },
      { id: "usage-a", request_id: "req-a", timestamp: "2026-10-30T12:00:09Z", total_tokens: 3 },
    ],
    total: 12,
    limit: 2,
    offset: 0,
    ...page,
  };
}

test("a live update to a row on the unfiltered first page updates it in place", () => {
  const app = createLiveLogsApp();
  fetchedUsagePage(app);

  app.mergeLiveUsageEntry({ id: "usage-a", request_id: "req-a", total_tokens: 30 }, "usage.flushed");

  assert.deepEqual(app.usageLog.entries.map((entry) => entry.id), ["usage-b", "usage-a"]);
  assert.equal(app.usageLog.entries[1].total_tokens, 30);
  assert.equal(app.usageLog.total, 12);
});

test("a live update to a row on a filtered page updates it in place", () => {
  for (const view of [
    { usageFilters: filtersWith({ userPaths: ["/team"] }) },
    { usageLogSearch: "gpt" },
  ]) {
    const app = createLiveLogsApp(view);
    fetchedUsagePage(app);

    app.mergeLiveUsageEntry({ id: "usage-a", request_id: "req-a", total_tokens: 30 }, "usage.flushed");

    assert.deepEqual(app.usageLog.entries.map((entry) => entry.id), ["usage-b", "usage-a"]);
    assert.equal(app.usageLog.entries[1].total_tokens, 30);
    assert.equal(app.usageLog.entries[1]._usage_flushed, true);
    assert.equal(app.usageLog.total, 12, "the row still matches, so the total holds");
  }
});

test("a live update to a row on a later page updates it in place", () => {
  const app = createLiveLogsApp();
  fetchedUsagePage(app, { offset: 2 });

  app.mergeLiveUsageEntry({ id: "usage-b", request_id: "req-b", total_tokens: 50 }, "usage.flushed");

  assert.deepEqual(app.usageLog.entries.map((entry) => entry.id), ["usage-b", "usage-a"]);
  assert.equal(app.usageLog.entries[0].total_tokens, 50);
  assert.equal(app.usageLog.total, 12);
});

test("filtered and later pages take no new live rows and keep their totals", () => {
  for (const view of [
    { usageFilters: filtersWith({ models: ["gpt-5"] }) },
    { usageLogSearch: "gpt" },
    { page: { offset: 2 } },
  ]) {
    const app = createLiveLogsApp(view.page ? {} : view);
    fetchedUsagePage(app, view.page);

    app.mergeLiveUsageEntry({ id: "usage-new", request_id: "req-new", total_tokens: 1 }, "usage.completed");

    assert.deepEqual(app.usageLog.entries.map((entry) => entry.id), ["usage-b", "usage-a"]);
    assert.equal(app.usageLog.total, 12);
  }
});

test("a row an update moves out of the view leaves the page and its total", () => {
  // With paging on too: the hide-cached filter is one the page can check.
  const app = createLiveLogsApp({ usageLogHideCached: true });
  fetchedUsagePage(app, { offset: 2 });

  app.mergeLiveUsageEntry({ id: "usage-a", request_id: "req-a", cache_type: "exact" }, "usage.flushed");

  assert.deepEqual(app.usageLog.entries.map((entry) => entry.id), ["usage-b"]);
  assert.equal(app.usageLog.total, 11);
  assert.equal(app.skippedLiveUsageByRequestId["req-a"].total_tokens, 3);
});

test("a duplicate live usage event neither duplicates nor recounts the row", () => {
  for (const view of [{}, { usageFilters: filtersWith({ userPaths: ["/team"] }) }]) {
    const app = createLiveLogsApp(view);
    fetchedUsagePage(app, { limit: 50 });
    const event = { id: "usage-a", request_id: "req-a", total_tokens: 30 };

    app.mergeLiveUsageEntry(event, "usage.flushed");
    app.mergeLiveUsageEntry(event, "usage.flushed");

    assert.deepEqual(app.usageLog.entries.map((entry) => entry.id), ["usage-b", "usage-a"]);
    assert.equal(app.usageLog.total, 12);
  }

  const app = createLiveLogsApp();
  fetchedUsagePage(app, { limit: 50 });
  const fresh = { id: "usage-new", request_id: "req-new", timestamp: "2026-10-30T12:00:11Z" };
  app.mergeLiveUsageEntry(fresh, "usage.completed");
  app.mergeLiveUsageEntry(fresh, "usage.flushed");
  assert.deepEqual(app.usageLog.entries.map((entry) => entry.id), ["usage-new", "usage-b", "usage-a"]);
  assert.equal(app.usageLog.total, 13, "a new row is counted once");
});

test("a remote usage row the filtered page already shows keeps the total", () => {
  const app = createLiveLogsApp({ usageFilters: filtersWith({ userPaths: ["/team"] }) });
  fetchedUsagePage(app, { as_of: 1_800_000_000_000_000 });

  app.mergeLiveUsageEntry(
    { id: "usage-a", request_id: "req-a", total_tokens: 30, _remote_written_at: 1_800_000_000_000_001 },
    "usage.flushed",
  );

  assert.deepEqual(app.usageLog.entries.map((entry) => entry.id), ["usage-b", "usage-a"]);
  assert.equal(app.usageLog.total, 12);
});
