// Ported from internal/admin/dashboard/static/js/modules/live-logs.test.cjs
// against the extracted pure logic in src/pages/audit-logs/live-logs-logic.js.
// Transport-only legacy cases (fetch/base-path plumbing) are replaced by the
// pure liveLogsStreamPath cursor test; everything else runs the exact merge
// engine the Svelte singleton uses.

import test from "node:test";
import assert from "node:assert/strict";

import {
  auditLivePauseMessage,
  liveLogsMethods,
  liveLogsStreamPath,
} from "../src/pages/audit-logs/live-logs-logic.js";
import { auditRecordKey, mergeAuditRecord } from "../src/pages/audit-logs/audit-records.js";

function createLiveLogsApp(overrides = {}) {
  return {
    liveLogsLastSeq: 0,
    skippedLiveUsageByRequestId: null,
    auditLog: { entries: [], total: 0, limit: 25, offset: 0 },
    usageLog: { entries: [], total: 0, limit: 50, offset: 0 },
    auditSearch: "",
    auditMethod: "",
    auditStatusCode: "",
    auditStream: "",
    usageLogSearch: "",
    usageFilterModel: "",
    usageFilterProvider: "",
    usageFilterLabel: "",
    usageFilterUserPath: "",
    usageLogHideCached: false,
    auditGroupSessions: false,
    auditThreadChildren: {},
    auditRecords: {},
    auditRecordChanges: [],
    customStartDate: null,
    customEndDate: null,
    followsToday: false,
    page: "audit-logs",
    fetchUsageCalls: 0,
    fetchAuditCalls: 0,
    fetchUsage() {
      this.fetchUsageCalls++;
    },
    fetchAuditLog() {
      this.fetchAuditCalls++;
    },
    cacheAuditRecord(entry, eventType) {
      const key = auditRecordKey(entry);
      const merged = mergeAuditRecord(this.auditRecords[key], entry);
      this.auditRecords[key] = merged;
      this.auditRecordChanges.push({ key, eventType });
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
  assert.equal(liveLogsStreamPath(0), "/admin/live/logs?types=audit,usage");
  assert.equal(liveLogsStreamPath(42), "/admin/live/logs?types=audit,usage&cursor=42");
});

test("live audit lifecycle events merge into one dashboard row by request id", () => {
  const app = createLiveLogsApp();

  app.applyLiveLogEvent({
    seq: 1,
    type: "audit.started",
    data: { id: "audit-1", request_id: "req-1", method: "POST", path: "/v1/chat/completions" },
  });
  app.applyLiveLogEvent({
    seq: 2,
    type: "audit.updated",
    data: { id: "audit-1", request_id: "req-1", requested_model: "gpt-test", provider: "openai" },
  });
  app.applyLiveLogEvent({
    seq: 3,
    type: "audit.completed",
    data: { id: "audit-1", request_id: "req-1", status_code: 200, duration_ns: 1000 },
  });
  app.applyLiveLogEvent({
    seq: 4,
    type: "audit.flushed",
    data: { id: "audit-1", request_id: "req-1", status_code: 200, duration_ns: 1000 },
  });

  assert.equal(app.liveLogsLastSeq, 4);
  assert.equal(app.auditLog.entries.length, 1);
  assert.equal(app.auditLog.entries[0].requested_model, "gpt-test");
  assert.equal(app.auditLog.entries[0].status_code, 200);
  assert.equal(app.auditLog.entries[0]._live_state, "audit.flushed");
  assert.equal(app.auditLog.entries[0]._live_pending, false);
  assert.equal(app.auditLog.entries[0]._audit_flushed, true);
});

test("audit.stream events merge partial response bodies and keep rows pending", () => {
  const app = createLiveLogsApp();

  app.applyLiveLogEvent({
    seq: 1,
    type: "audit.started",
    data: { id: "audit-1", request_id: "req-1", method: "POST", path: "/v1/chat/completions" },
  });
  app.applyLiveLogEvent({
    seq: 2,
    type: "audit.stream",
    data: {
      id: "audit-1",
      request_id: "req-1",
      status_code: 200,
      stream: true,
      data: {
        response_body: { choices: [{ index: 0, message: { role: "assistant", content: "partial" } }] },
        response_body_partial: true,
      },
    },
  });

  const streaming = app.auditLog.entries[0];
  assert.equal(streaming._response_partial, true);
  assert.equal(streaming._live_pending, true);
  assert.equal(streaming._live_state, "audit.stream");
  assert.equal(streaming.data.response_body.choices[0].message.content, "partial");

  app.applyLiveLogEvent({
    seq: 3,
    type: "audit.completed",
    data: {
      id: "audit-1",
      request_id: "req-1",
      status_code: 200,
      duration_ns: 1000,
      data: {
        response_body: { choices: [{ index: 0, message: { role: "assistant", content: "final" } }] },
      },
    },
  });

  const completed = app.auditLog.entries[0];
  assert.equal(completed._response_partial, false);
  assert.equal(completed._live_state, "audit.completed");
  assert.equal(completed.data.response_body.choices[0].message.content, "final");
});

test("live audit merges update the normalized record cache", () => {
  const app = createLiveLogsApp();

  app.applyLiveLogEvent({
    seq: 1,
    type: "audit.started",
    data: { id: "audit-1", request_id: "req-1" },
  });
  app.applyLiveLogEvent({
    seq: 2,
    type: "audit.stream",
    data: { id: "audit-1", request_id: "req-1", data: { response_body: { choices: [] }, response_body_partial: true } },
  });

  assert.equal(app.auditRecordChanges.length, 2);
  assert.equal(app.auditRecords["audit-1"]._response_partial, true);
});

test("live audit removed event drops suppressed preview rows", () => {
  const app = createLiveLogsApp();
  app.auditLog.entries = [{ id: "audit-1", request_id: "req-1" }];
  app.auditLog.total = 1;

  app.applyLiveLogEvent({
    seq: 4,
    type: "audit.removed",
    data: { id: "audit-1", request_id: "req-1" },
  });

  assert.deepEqual(app.auditLog.entries, []);
  assert.equal(app.auditLog.total, 0);
});

test("live audit removed event decrements total by removed row count", () => {
  const app = createLiveLogsApp();
  app.auditLog.entries = [
    { id: "audit-1", request_id: "req-1" },
    { id: "audit-2", request_id: "req-1" },
    { id: "audit-3", request_id: "req-3" },
  ];
  app.auditLog.total = 3;

  app.applyLiveLogEvent({
    seq: 5,
    type: "audit.removed",
    data: { request_id: "req-1" },
  });

  assert.deepEqual(app.auditLog.entries, [{ id: "audit-3", request_id: "req-3" }]);
  assert.equal(app.auditLog.total, 1);
});

test("live audit inserts are blocked while a custom audit date range is active", () => {
  const app = createLiveLogsApp();

  app.customStartDate = new Date();
  app.applyLiveLogEvent({
    seq: 1,
    type: "audit.started",
    data: { id: "audit-start", request_id: "req-start" },
  });

  app.customStartDate = null;
  app.customEndDate = new Date();
  app.applyLiveLogEvent({
    seq: 2,
    type: "audit.started",
    data: { id: "audit-end", request_id: "req-end" },
  });

  assert.equal(app.auditLog.entries.length, 0);
  assert.equal(app.auditLog.total, 0);
});

test("live audit inserts flow while the custom date range ends today", () => {
  const app = createLiveLogsApp();

  // A persisted custom window that follows the current day (e.g. Jul 16 ->
  // today) must not pause live inserts: entries arriving now are inside it.
  app.customStartDate = new Date(Date.now() - 7 * 24 * 60 * 60 * 1000);
  app.customEndDate = new Date();
  app.followsToday = true;

  app.applyLiveLogEvent({
    seq: 1,
    type: "audit.started",
    data: { id: "audit-today", request_id: "req-today" },
  });

  assert.equal(app.auditLog.entries.length, 1);
  assert.equal(app.auditLog.entries[0].id, "audit-today");
  assert.equal(app.auditLog.total, 1);
});

test("live audit inserts stay blocked for a custom range detached from today", () => {
  const app = createLiveLogsApp();

  app.customStartDate = new Date(Date.now() - 14 * 24 * 60 * 60 * 1000);
  app.customEndDate = new Date(Date.now() - 7 * 24 * 60 * 60 * 1000);
  app.followsToday = false;

  app.applyLiveLogEvent({
    seq: 1,
    type: "audit.started",
    data: { id: "audit-past", request_id: "req-past" },
  });

  assert.equal(app.auditLog.entries.length, 0);
  assert.equal(app.auditLog.total, 0);
});

test("audit live pause reason names the blocking condition", () => {
  const cases = [
    { name: "flowing", overrides: {}, want: null },
    {
      name: "pagination wins",
      overrides: { auditLog: { entries: [], total: 0, limit: 25, offset: 25 } },
      want: "pagination",
    },
    { name: "filters", overrides: { auditStatusCode: "500" }, want: "filters" },
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
    assert.equal(app.auditLivePauseReason(), tc.want, tc.name);
  }
});

test("audit live pause messages tell the operator how to resume", () => {
  assert.match(auditLivePauseMessage("date_range"), /Set it to today/);
  assert.match(auditLivePauseMessage("filters"), /filters/);
  assert.match(auditLivePauseMessage("pagination"), /first page/);
  assert.equal(auditLivePauseMessage(null), "");
});

test("live audit inserts are blocked while filters or pagination are active", () => {
  const app = createLiveLogsApp();
  app.auditSearch = "gpt";

  app.applyLiveLogEvent({
    seq: 1,
    type: "audit.started",
    data: { id: "audit-1", request_id: "req-1" },
  });
  assert.equal(app.auditLog.entries.length, 0);

  app.auditSearch = "";
  app.auditLog.offset = 25;
  app.applyLiveLogEvent({
    seq: 2,
    type: "audit.started",
    data: { id: "audit-2", request_id: "req-2" },
  });
  assert.equal(app.auditLog.entries.length, 0);
});

test("paused list insertion still updates normalized records", () => {
  const app = createLiveLogsApp({ auditSearch: "filtered" });
  app.applyLiveLogEvent({
    seq: 1,
    type: "audit.updated",
    data: {
      id: "audit-hidden",
      session_id: "session-open-in-drawer",
      data: { request_body: { input: "keep following me" } },
    },
  });

  assert.deepEqual(app.auditLog.entries, []);
  assert.equal(
    app.auditRecords["audit-hidden"].data.request_body.input,
    "keep following me",
  );
});

test("live audit inserts cap the preview buffer at the page limit", () => {
  const app = createLiveLogsApp();
  app.auditLog.limit = 3;

  for (let i = 1; i <= 5; i++) {
    app.applyLiveLogEvent({
      seq: i,
      type: "audit.started",
      data: { id: "audit-" + i, request_id: "req-" + i },
    });
  }

  assert.equal(app.auditLog.entries.length, 3);
  assert.equal(app.auditLog.total, 5);
  assert.equal(app.auditLog.entries[0].id, "audit-5");
  assert.equal(app.auditLog.entries[2].id, "audit-3");
});

test("live logs parser handles CRLF-separated SSE frames", async () => {
  const app = createLiveLogsApp();

  await app.consumeLiveLogsBody(liveReaderFromChunks([
    'data: {"seq":1,"type":"heartbeat"}\r\n\r\n',
    'data: {"seq":2,"type":"audit.started","data":{"id":"audit-1","request_id":"req-1"}}\r\n\r\n',
  ]));

  assert.equal(app.liveLogsLastSeq, 2);
  assert.equal(app.auditLog.entries.length, 1);
  assert.equal(app.auditLog.entries[0].id, "audit-1");
});

test("live logs parser joins split chunks and flushes a trailing frame", async () => {
  const app = createLiveLogsApp();

  await app.consumeLiveLogsBody(liveReaderFromChunks([
    'data: {"seq":1,"type":"audit.star',
    'ted","data":{"id":"audit-1","request_id":"req-1"}}\n\ndata: {"seq":2,"type":"heartbeat"}',
  ]));

  assert.equal(app.liveLogsLastSeq, 2);
  assert.equal(app.auditLog.entries.length, 1);
});

test("live usage event updates usage log and enriches matching audit row", () => {
  const app = createLiveLogsApp();
  app.auditLog.entries = [{ id: "audit-1", request_id: "req-1" }];

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
  assert.equal(app.auditLog.entries[0].usage.total_tokens, 14);
  assert.equal(app.auditLog.entries[0]._usage_live_pending, true);

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
  assert.equal(app.auditLog.entries[0]._usage_flushed, true);
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
  app.auditLog.entries = [{
    id: "audit-cache",
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
  assert.equal(app.auditLog.entries[0].usage.total_tokens, 14);
  assert.equal(app.auditLog.entries[0]._usage_live_pending, true);
  assert.equal(app.auditLog.entries[0]._usage_live_state, "usage.completed");
});

test("hidden cached live usage attaches to later audit previews", () => {
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
    type: "audit.completed",
    data: {
      id: "audit-cache",
      request_id: "req-cache",
      cache_type: "exact",
      status_code: 200,
    },
  });

  assert.equal(app.auditLog.entries[0].usage.total_tokens, 14);
  assert.equal(app.auditLog.entries[0]._usage_live_state, "usage.completed");
  assert.equal(app.auditLog.entries[0]._usage_live_pending, true);
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
  assert.equal(app.auditLog.entries[0].usage.total_tokens, 14);
  assert.equal(app.auditLog.entries[0]._usage_flushed, true);
  assert.equal(app.auditLog.entries[0]._usage_live_pending, false);

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
  app.auditLog.entries = [{ id: "audit-cache", request_id: "req-cache" }];

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
  assert.equal(app.auditLog.entries[0]._usage_flushed, true);
  assert.equal(app.auditLog.entries[0].usage.total_tokens, 14);
  assert.equal(app.skippedLiveUsageByRequestId["req-cache"].total_tokens, 14);
});

test("live usage audit summary uses normalized split prompt-cache tokens", () => {
  const app = createLiveLogsApp();
  app.auditLog.entries = [{ id: "audit-cache", request_id: "req-cache" }];

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

  const summary = app.auditLog.entries[0].usage;
  assert.equal(summary.input_tokens, 175);
  assert.equal(summary.uncached_input_tokens, 100);
  assert.equal(summary.cached_input_tokens, 50);
  assert.equal(summary.cache_write_input_tokens, 25);
  assert.equal(summary.output_tokens, 20);
  assert.equal(summary.total_tokens, 195);
  assert.equal(summary.cached_input_ratio, 50 / 175);
  assert.equal(summary.estimated_cached_characters, 200);
});

test("audit detail merge preserves existing live lifecycle state", () => {
  const app = createLiveLogsApp();
  app.auditLog.entries = [{
    id: "audit-1",
    request_id: "req-1",
    _live: true,
    _live_state: "audit.completed",
    _live_pending: true,
    _audit_flushed: false,
    data: {
      workflow_features: { cache: true },
    },
  }];

  const merged = app.mergeLiveAuditEntry({
    id: "audit-1",
    request_id: "req-1",
    data: {
      request_headers: { authorization: "Bearer redacted" },
    },
  }, "audit.detail");

  assert.equal(merged._live, true);
  assert.equal(merged._live_state, "audit.completed");
  assert.equal(merged._live_pending, true);
  assert.equal(merged._audit_flushed, false);
  assert.equal(merged._detail_loaded, true);
  assert.deepEqual(merged.data.workflow_features, { cache: true });
  assert.deepEqual(merged.data.request_headers, { authorization: "Bearer redacted" });
});

test("live audit lifecycle patches merge captured detail data", () => {
  const app = createLiveLogsApp();
  app.auditLog.entries = [{
    id: "audit-1",
    request_id: "req-1",
    _detail_loaded: true,
    data: {
      request_headers: { authorization: "Bearer redacted" },
      response_body: { id: "chatcmpl_123" },
    },
  }];

  app.applyLiveLogEvent({
    seq: 9,
    type: "audit.flushed",
    data: {
      id: "audit-1",
      request_id: "req-1",
      status_code: 200,
      data: {
        response_headers: { "x-request-id": "req-1" },
      },
    },
  });

  assert.equal(app.auditLog.entries[0].status_code, 200);
  assert.equal(app.auditLog.entries[0]._live_state, "audit.flushed");
  assert.equal(app.auditLog.entries[0]._live_pending, false);
  assert.deepEqual(app.auditLog.entries[0].data.request_headers, { authorization: "Bearer redacted" });
  assert.deepEqual(app.auditLog.entries[0].data.response_headers, { "x-request-id": "req-1" });
  assert.deepEqual(app.auditLog.entries[0].data.response_body, { id: "chatcmpl_123" });
});

test("late queued events do not regress flushed live rows to pending", () => {
  const app = createLiveLogsApp();

  app.applyLiveLogEvent({
    seq: 1,
    type: "audit.flushed",
    data: { id: "audit-1", request_id: "req-1", status_code: 200, duration_ns: 1000 },
  });
  app.applyLiveLogEvent({
    seq: 2,
    type: "audit.completed",
    data: { id: "audit-1", request_id: "req-1", status_code: 200, duration_ns: 1000 },
  });

  assert.equal(app.auditLog.entries[0]._live_state, "audit.flushed");
  assert.equal(app.auditLog.entries[0]._live_pending, false);
  assert.equal(app.auditLog.entries[0]._audit_flushed, true);

  app.auditLog.entries[0].usage = { entries: 1 };
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
  assert.equal(app.auditLog.entries[0]._usage_live_state, "usage.flushed");
  assert.equal(app.auditLog.entries[0]._usage_live_pending, false);
  assert.equal(app.auditLog.entries[0]._usage_flushed, true);
});

test("flushed expanded live audit rows retry detail fetch", () => {
  const app = createLiveLogsApp();
  let detailFetches = 0;
  app.auditLog.entries = [{
    id: "audit-1",
    request_id: "req-1",
    _live: true,
    _live_state: "audit.completed",
    _live_pending: true,
    _audit_flushed: false,
  }];
  app.isAuditEntryExpanded = (entry) => String(entry && entry.id || "") === "audit-1";
  app.fetchAuditEntryDetail = (entry) => {
    detailFetches++;
    assert.equal(entry.id, "audit-1");
    assert.equal(entry._live_state, "audit.flushed");
  };

  app.applyLiveLogEvent({
    seq: 5,
    type: "audit.flushed",
    data: { id: "audit-1", request_id: "req-1", status_code: 200 },
  });

  assert.equal(detailFetches, 1);
});

test("failed live events clear pending state", () => {
  const app = createLiveLogsApp();

  app.applyLiveLogEvent({
    seq: 1,
    type: "audit.completed",
    data: { id: "audit-1", request_id: "req-1" },
  });
  app.applyLiveLogEvent({
    seq: 2,
    type: "audit.failed",
    data: { id: "audit-1", request_id: "req-1" },
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

  assert.equal(app.auditLog.entries[0]._live_state, "audit.failed");
  assert.equal(app.auditLog.entries[0]._live_pending, false);
  assert.equal(app.usageLog.entries[0]._live_state, "usage.failed");
  assert.equal(app.usageLog.entries[0]._live_pending, false);
  assert.equal(app.auditLog.entries[0]._usage_live_state, "usage.failed");
  assert.equal(app.auditLog.entries[0]._usage_live_pending, false);
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
  assert.equal(app.fetchAuditCalls, 1);
});

test("audit detail fetch gating: skips captured data, waits for live flush", () => {
  const app = createLiveLogsApp();

  // Already-captured detail data: no fetch.
  assert.equal(app.auditEntryShouldFetchDetail({
    id: "audit-1",
    request_id: "req-1",
    data: { request_headers: { authorization: "Bearer redacted" } },
  }), false);

  // Live row still streaming: wait for the flush.
  assert.equal(app.auditEntryShouldFetchDetail({
    id: "audit-1",
    _live: true,
    _live_state: "audit.updated",
    _live_pending: true,
    _audit_flushed: false,
    data: { request_body: { model: "gpt-test" } },
  }), false);

  // Flushed live row fetches persisted detail even with preview data.
  assert.equal(app.auditEntryShouldFetchDetail({
    id: "audit-1",
    _live: true,
    _live_state: "audit.flushed",
    _live_pending: false,
    _audit_flushed: true,
    data: { request_body: { model: "gpt-test" } },
  }), true);

  // Compact (workflow-only) data on a persisted row fetches detail.
  assert.equal(app.auditEntryShouldFetchDetail({
    id: "audit-1",
    data: { workflow_features: { cache: true } },
  }), true);
});

// --- Session-thread grouping -------------------------------------------------

test("grouped mode folds a new live entry into its on-screen thread", () => {
  const app = createLiveLogsApp({ auditGroupSessions: true });
  app.auditLog.entries = [
    { id: "head-a", request_id: "req-1", session_id: "s-a", session_count: 2 },
    { id: "other", session_id: "s-b", session_count: 1 },
  ];
  app.auditLog.total = 2;
  app.auditThreadChildren = {
    "s-a": { loading: false, entries: [{ id: "old-child" }], total: 2 },
  };

  app.mergeLiveAuditEntry(
    { id: "live-3", request_id: "req-3", session_id: "s-a", path: "/v1/chat/completions" },
    "audit.started",
  );

  // Thread bubbles to the top with the new entry as head and count bumped.
  assert.deepEqual(
    app.auditLog.entries.map((entry) => entry.id),
    ["live-3", "other"],
  );
  assert.equal(app.auditLog.entries[0].session_count, 3);
  assert.equal(app.auditLog.entries[0]._live_pending, true);
  // Total is unchanged: same number of threads.
  assert.equal(app.auditLog.total, 2);
  // The displaced head moved into the loaded children list, count-free.
  const children = app.auditThreadChildren["s-a"];
  assert.deepEqual(children.entries.map((entry) => entry.id), ["head-a", "old-child"]);
  assert.equal(children.entries[0].session_count, undefined);
  assert.equal(children.total, 3);
});

test("parent headers do not assign sessions before the server resolves them", () => {
  const app = createLiveLogsApp({ auditGroupSessions: true });
  app.auditLog.entries = [
    { id: "head-a", session_id: "s-a", session_count: 2 },
  ];

  app.mergeLiveAuditEntry({
    id: "live-3",
    request_id: "req-3",
    path: "/v1/chat/completions",
    data: { request_headers: { "X-GoModel-Interaction-Parent": "head-a" } },
  }, "audit.started");

  assert.equal(app.auditLog.entries.length, 2);
  assert.equal(app.auditLog.entries[0].id, "live-3");
  assert.equal(app.auditLog.entries[0].session_id, undefined);
  assert.equal(app.auditLog.entries[1].id, "head-a");
});

test("grouped fold retains the displaced head of an unexpanded thread", () => {
  const app = createLiveLogsApp({ auditGroupSessions: true });
  app.auditLog.entries = [
    { id: "head-a", session_id: "s-a", session_count: 2 },
  ];
  app.auditLog.total = 1;

  app.mergeLiveAuditEntry({ id: "live-2", session_id: "s-a" }, "audit.started");

  assert.equal(app.auditLog.entries[0].id, "live-2");
  assert.equal(app.auditLog.entries[0].session_count, 3);
  // The displaced head lands in a partial children list (loaded: false keeps
  // the lazy full fetch armed) so its later live events merge in place
  // instead of re-counting as new session members.
  const children = app.auditThreadChildren["s-a"];
  assert.equal(children.loaded, false);
  assert.equal(children.loading, false);
  assert.deepEqual(children.entries.map((entry) => entry.id), ["head-a"]);
  assert.equal(children.entries[0].session_count, undefined);
  assert.equal(children.total, 1);
});

test("grouped mode without a matching thread prepends a singleton head", () => {
  const app = createLiveLogsApp({ auditGroupSessions: true });
  app.auditLog.entries = [{ id: "head-a", session_id: "s-a", session_count: 2 }];
  app.auditLog.total = 1;

  app.mergeLiveAuditEntry({ id: "live-9", session_id: "s-new" }, "audit.started");

  assert.deepEqual(
    app.auditLog.entries.map((entry) => entry.id),
    ["live-9", "head-a"],
  );
  assert.equal(app.auditLog.total, 2);
});

test("flat mode never folds by session", () => {
  const app = createLiveLogsApp({ auditGroupSessions: false });
  app.auditLog.entries = [{ id: "row-1", session_id: "s-a" }];
  app.auditLog.total = 1;

  app.mergeLiveAuditEntry({ id: "row-2", session_id: "s-a" }, "audit.started");

  assert.deepEqual(
    app.auditLog.entries.map((entry) => entry.id),
    ["row-2", "row-1"],
  );
  assert.equal(app.auditLog.total, 2);
});

test("grouped fold respects the live insert gate", () => {
  const app = createLiveLogsApp({ auditGroupSessions: true, auditSearch: "x" });
  app.auditLog.entries = [{ id: "head-a", session_id: "s-a", session_count: 2 }];
  app.auditLog.total = 1;

  app.mergeLiveAuditEntry({ id: "live-3", session_id: "s-a" }, "audit.started");

  assert.deepEqual(app.auditLog.entries.map((entry) => entry.id), ["head-a"]);
  assert.equal(app.auditLog.entries[0].session_count, 2);
});

test("live updates merge into displaced entries living in children lists", () => {
  const app = createLiveLogsApp({ auditGroupSessions: true });
  app.auditLog.entries = [{ id: "head", session_id: "s-a", session_count: 2 }];
  app.auditThreadChildren = {
    "s-a": {
      loading: false,
      entries: [{ id: "child-1", request_id: "req-c1", status_code: null }],
      total: 2,
    },
  };

  app.mergeLiveAuditEntry(
    { id: "child-1", request_id: "req-c1", status_code: 200 },
    "audit.flushed",
  );

  const child = app.auditThreadChildren["s-a"].entries[0];
  assert.equal(child.status_code, 200);
  assert.equal(child._audit_flushed, true);
  // The head list is untouched.
  assert.deepEqual(app.auditLog.entries.map((entry) => entry.id), ["head"]);
});

test("late usage updates enrich displaced entries living in children lists", () => {
  const app = createLiveLogsApp({ auditGroupSessions: true });
  app.auditLog.entries = [{ id: "head", request_id: "req-head", session_id: "s-a", session_count: 2 }];
  app.auditThreadChildren = {
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

  const child = app.auditThreadChildren["s-a"].entries[0];
  assert.equal(child.usage.input_tokens, 10);
  assert.equal(child.usage.output_tokens, 4);
  assert.equal(child.usage.total_tokens, 14);
  assert.equal(app.auditLog.entries[0].usage, undefined);
});

test("audit.detail events hydrate children-list entries", () => {
  const app = createLiveLogsApp({ auditGroupSessions: true });
  app.auditThreadChildren = {
    "s-a": { loading: false, entries: [{ id: "child-1" }], total: 2 },
  };

  const merged = app.mergeLiveAuditEntry(
    { id: "child-1", data: { request_body: { model: "gpt-4o" } } },
    "audit.detail",
  );

  assert.equal(merged._detail_loaded, true);
  assert.deepEqual(
    app.auditThreadChildren["s-a"].entries[0].data.request_body,
    { model: "gpt-4o" },
  );
});

test("audit.removed cleans children lists and decrements the head count", () => {
  const app = createLiveLogsApp({ auditGroupSessions: true });
  app.auditLog.entries = [{ id: "head", session_id: "s-a", session_count: 3 }];
  app.auditLog.total = 1;
  app.auditThreadChildren = {
    "s-a": {
      loading: false,
      entries: [{ id: "child-1" }, { id: "child-2" }],
      total: 3,
    },
  };

  app.removeLiveAuditEntry({ id: "child-1" });

  assert.deepEqual(
    app.auditThreadChildren["s-a"].entries.map((entry) => entry.id),
    ["child-2"],
  );
  assert.equal(app.auditThreadChildren["s-a"].total, 2);
  assert.equal(app.auditLog.entries[0].session_count, 2);
  // Head list itself is untouched by a child removal.
  assert.equal(app.auditLog.total, 1);
});

test("audit.removed promotes a loaded child when a grouped head disappears", () => {
  const app = createLiveLogsApp({ auditGroupSessions: true });
  app.auditLog.entries = [{ id: "head", session_id: "s-a", session_count: 3 }];
  app.auditLog.total = 1;
  app.auditThreadChildren = {
    "s-a": {
      loading: false,
      entries: [
        { id: "child-2", session_id: "s-a" },
        { id: "child-1", session_id: "s-a" },
      ],
      total: 3,
    },
  };

  app.removeLiveAuditEntry({ id: "head" });

  assert.deepEqual(app.auditLog.entries.map((entry) => entry.id), ["child-2"]);
  assert.equal(app.auditLog.entries[0].session_count, 2);
  assert.equal(app.auditLog.total, 1);
  assert.deepEqual(
    app.auditThreadChildren["s-a"].entries.map((entry) => entry.id),
    ["child-1"],
  );
  assert.equal(app.auditThreadChildren["s-a"].total, 2);
});

test("audit.removed reloads an unexpanded grouped thread when its head disappears", () => {
  const app = createLiveLogsApp({ auditGroupSessions: true });
  app.auditLog.entries = [{ id: "head", session_id: "s-a", session_count: 3 }];
  app.auditLog.total = 1;

  app.removeLiveAuditEntry({ id: "head" });

  assert.deepEqual(app.auditLog.entries, []);
  assert.equal(app.auditLog.total, 1);
  assert.equal(app.fetchAuditCalls, 1);
});

test("a sessionless live row re-folds on the pre-response session update", () => {
  const app = createLiveLogsApp({ auditGroupSessions: true });
  app.auditLog.entries = [{ id: "head-a", session_id: "s-a", session_count: 2 }];
  app.auditLog.total = 1;
  app.auditThreadChildren = {
    "s-a": { loading: false, entries: [{ id: "old-child" }], total: 2 },
  };

  // audit.started fires before session detection: the row arrives sessionless
  // and prepends as its own singleton thread.
  app.mergeLiveAuditEntry({ id: "live-1", request_id: "req-1" }, "audit.started");
  assert.equal(app.auditLog.entries.length, 2);
  assert.equal(app.auditLog.total, 2);

  // SessionCapture delivers audit.updated before the provider handler runs:
  // the row folds without waiting for a response or terminal event.
  app.mergeLiveAuditEntry(
    { id: "live-1", request_id: "req-1", session_id: "s-a" },
    "audit.updated",
  );

  assert.deepEqual(app.auditLog.entries.map((entry) => entry.id), ["live-1"]);
  assert.equal(app.auditLog.entries[0].session_count, 3);
  assert.equal(app.auditLog.total, 1);
  // The displaced head moved into the loaded children.
  assert.deepEqual(
    app.auditThreadChildren["s-a"].entries.map((entry) => entry.id),
    ["head-a", "old-child"],
  );
});

test("re-fold leaves rows alone in flat mode and without a matching head", () => {
  const flat = createLiveLogsApp({ auditGroupSessions: false });
  flat.auditLog.entries = [{ id: "row-a", session_id: "s-a" }];
  flat.auditLog.total = 1;
  flat.mergeLiveAuditEntry({ id: "row-a", session_id: "s-a", status_code: 200 }, "audit.flushed");
  assert.equal(flat.auditLog.entries.length, 1);

  const grouped = createLiveLogsApp({ auditGroupSessions: true });
  grouped.auditLog.entries = [{ id: "solo", session_id: "s-new" }];
  grouped.auditLog.total = 1;
  grouped.mergeLiveAuditEntry({ id: "solo", session_id: "s-new", status_code: 200 }, "audit.flushed");
  assert.deepEqual(grouped.auditLog.entries.map((entry) => entry.id), ["solo"]);
  assert.equal(grouped.auditLog.total, 1);
});

test("interleaved first-session requests do not inflate the thread count", () => {
  // Regression: two live requests open a brand-new session while the page is
  // watching. Both audit.started events arrive sessionless, later events
  // deliver the session id, and the demoted row keeps producing lifecycle
  // events. Before the fix each late event for a displaced (and dropped) row
  // re-counted it as a new session member, inflating the badge 2x-3x.
  const app = createLiveLogsApp({ auditGroupSessions: true });

  app.mergeLiveAuditEntry(
    { id: "a", request_id: "req-a", timestamp: "2026-07-28T10:00:00Z" },
    "audit.started",
  );
  app.mergeLiveAuditEntry(
    { id: "b", request_id: "req-b", timestamp: "2026-07-28T10:00:02Z" },
    "audit.started",
  );
  app.mergeLiveAuditEntry(
    { id: "b", request_id: "req-b", session_id: "s-1", timestamp: "2026-07-28T10:00:02Z" },
    "audit.updated",
  );
  // A gains the session id: the two rows collapse into one thread and A is
  // demoted under B (the newer request).
  app.mergeLiveAuditEntry(
    { id: "a", request_id: "req-a", session_id: "s-1", timestamp: "2026-07-28T10:00:00Z" },
    "audit.updated",
  );
  assert.deepEqual(app.auditLog.entries.map((entry) => entry.id), ["b"]);
  assert.equal(app.auditLog.entries[0].session_count, 2);
  assert.equal(app.auditLog.total, 1);

  // Late lifecycle events for both requests merge in place: the count and the
  // thread total must not move again.
  app.mergeLiveAuditEntry(
    { id: "a", request_id: "req-a", session_id: "s-1", status_code: 200 },
    "audit.completed",
  );
  app.mergeLiveAuditEntry(
    { id: "b", request_id: "req-b", session_id: "s-1", status_code: 200 },
    "audit.completed",
  );
  app.mergeLiveAuditEntry(
    { id: "a", request_id: "req-a", session_id: "s-1", status_code: 200 },
    "audit.flushed",
  );
  app.mergeLiveAuditEntry(
    { id: "b", request_id: "req-b", session_id: "s-1", status_code: 200 },
    "audit.flushed",
  );

  assert.deepEqual(app.auditLog.entries.map((entry) => entry.id), ["b"]);
  assert.equal(app.auditLog.entries[0].session_count, 2);
  assert.equal(app.auditLog.entries[0]._audit_flushed, true);
  assert.equal(app.auditLog.total, 1);
  const children = app.auditThreadChildren["s-1"];
  assert.deepEqual(children.entries.map((entry) => entry.id), ["a"]);
  assert.equal(children.entries[0]._audit_flushed, true);
  assert.equal(children.loaded, false);
});

test("re-fold keeps the newest request as head when completions arrive out of order", () => {
  const app = createLiveLogsApp({ auditGroupSessions: true });
  // A starts first, B starts later; both sessionless.
  app.mergeLiveAuditEntry(
    { id: "req-a", timestamp: "2026-07-27T10:00:00Z" },
    "audit.started",
  );
  app.mergeLiveAuditEntry(
    { id: "req-b", timestamp: "2026-07-27T10:00:05Z" },
    "audit.started",
  );
  // B completes first and gains the session id (no other head yet: stays put).
  app.mergeLiveAuditEntry(
    { id: "req-b", timestamp: "2026-07-27T10:00:05Z", session_id: "s-a", status_code: 200 },
    "audit.flushed",
  );
  // A completes last: it must fold UNDER B, which is the newer request.
  app.mergeLiveAuditEntry(
    { id: "req-a", timestamp: "2026-07-27T10:00:00Z", session_id: "s-a", status_code: 200 },
    "audit.flushed",
  );

  assert.deepEqual(app.auditLog.entries.map((entry) => entry.id), ["req-b"]);
  assert.equal(app.auditLog.entries[0].session_count, 2);
  assert.equal(app.auditLog.total, 1);
});
