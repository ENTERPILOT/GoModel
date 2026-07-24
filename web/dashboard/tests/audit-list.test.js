// Pure-logic tests for the audit-logs page, ported from the legacy
// audit-list.test.cjs (Alpine/DOM-harness cases are covered by the Svelte
// components and were not ported).

import test from "node:test";
import assert from "node:assert/strict";

import {
  auditCacheRatioLabel,
  auditCacheRatioPillLabel,
  auditCacheSharePercent,
  auditEntryErrorMessage,
  auditEntryLiveInProgress,
  auditHasCachedTokens,
  auditLogAllowsLiveEntries,
  auditLogWithLiveEntries,
  auditPanes,
  auditPromptCacheHighlight,
  auditRequestPane,
  auditRequestRevisionPane,
  auditRequestRevisions,
  auditResponsePane,
  auditRetentionHighlight,
  auditRetentionPrefix,
  auditRetentionText,
  auditRevisionPercentLabel,
  auditTabKeydownTarget,
  buildAuditLogQuery,
  formatDurationNs,
  formatJSON,
  markExpandedEntry,
  pruneExpandedEntries,
} from "../src/pages/audit-logs/audit-logic.js";

const noFilters = {
  search: "",
  method: "",
  statusCode: "",
  stream: "",
  customStartDate: null,
  customEndDate: null,
};

test("auditRetentionText describes finite and indefinite retention", () => {
  assert.equal(auditRetentionText("30"), "Audit logs are retained for 30 days.");
  assert.equal(auditRetentionText("1"), "Audit logs are retained for 1 day.");
  assert.equal(auditRetentionText("0"), "Audit logs are retained indefinitely.");
});

test("auditRetentionText hides missing or invalid retention values", () => {
  assert.equal(auditRetentionText(undefined), "");
  assert.equal(auditRetentionText("-1"), "");
  assert.equal(auditRetentionText("unknown"), "");
});

test("auditRetentionPrefix and auditRetentionHighlight split finite and indefinite retention", () => {
  assert.equal(auditRetentionPrefix("30"), "Audit logs are retained for ");
  assert.equal(auditRetentionHighlight("30"), "30 days");

  assert.equal(auditRetentionPrefix("1"), "Audit logs are retained for ");
  assert.equal(auditRetentionHighlight("1"), "1 day");

  assert.equal(auditRetentionPrefix("0"), "Audit logs are retained ");
  assert.equal(auditRetentionHighlight("0"), "indefinitely");
});

test("auditRetentionPrefix and auditRetentionHighlight hide missing or invalid retention values", () => {
  assert.equal(auditRetentionPrefix(undefined), "");
  assert.equal(auditRetentionHighlight(undefined), "");
  assert.equal(auditRetentionPrefix("-1"), "");
  assert.equal(auditRetentionHighlight("-1"), "");
});

test("auditRequestPane returns the shared request-pane contract", () => {
  const entry = {
    data: {
      request_headers: { authorization: "Bearer redacted" },
      request_body: { model: "gpt-5", stream: false },
      request_body_too_big_to_handle: true,
    },
  };

  const pane = auditRequestPane(entry);

  assert.equal(pane.title, "Request");
  assert.equal(pane.entry, entry);
  assert.deepEqual(pane.copyHeaders, entry.data.request_headers);
  assert.deepEqual(pane.copyBody, entry.data.request_body);
  assert.equal(pane.showErrorMessage, false);
  assert.equal(pane.errorMessage, null);
  assert.equal(pane.showHeaders, true);
  assert.deepEqual(pane.headers, entry.data.request_headers);
  assert.equal(pane.showBody, true);
  assert.deepEqual(pane.body, entry.data.request_body);
  assert.equal(pane.showEmpty, false);
  assert.equal(pane.emptyMessage, "Request details were not captured.");
  assert.equal(pane.showTooLarge, true);
  assert.equal(pane.tooLargeMessage, "Request body was too large to capture.");
});

test("auditResponsePane returns the shared response-pane contract", () => {
  const entry = {
    data: {
      error_message: "provider timeout",
      response_headers: { "x-request-id": "abc123" },
      response_body: { id: "resp_123" },
      response_body_too_big_to_handle: false,
    },
  };

  const pane = auditResponsePane(entry);

  assert.equal(pane.title, "Response");
  assert.equal(pane.entry, entry);
  assert.deepEqual(pane.copyHeaders, entry.data.response_headers);
  assert.deepEqual(pane.copyBody, entry.data.response_body);
  assert.equal(pane.showErrorMessage, true);
  assert.equal(pane.errorMessage, "provider timeout");
  assert.equal(pane.showHeaders, true);
  assert.equal(pane.showBody, true);
  assert.equal(pane.showEmpty, false);
  assert.equal(pane.emptyMessage, "Response details were not captured.");
  assert.equal(pane.showTooLarge, false);
  assert.equal(pane.tooLargeMessage, "Response body was too large to capture.");
});

test("auditEntryLiveInProgress stays true while a partial response body is streaming", () => {
  const streaming = {
    _live: true,
    _live_pending: true,
    _live_state: "audit.stream",
    _response_partial: true,
    status_code: 200,
    data: { response_body: { choices: [] } },
  };

  assert.equal(auditEntryLiveInProgress(streaming), true);
  assert.equal(
    auditEntryLiveInProgress({
      ...streaming,
      _live_state: "audit.completed",
      _response_partial: false,
      duration_ns: 1000,
    }),
    false,
  );
});

test("audit panes surface pending spinners and streaming badges for live rows", () => {
  const waiting = {
    _live: true,
    _live_pending: true,
    _live_state: "audit.updated",
    data: {},
  };
  const waitingResponse = auditResponsePane(waiting);
  assert.equal(waitingResponse.showPending, true);
  assert.equal(waitingResponse.showEmpty, false);
  assert.equal(waitingResponse.pendingMessage, "Response in progress…");
  const waitingRequest = auditRequestPane(waiting);
  assert.equal(waitingRequest.showPending, true);
  assert.equal(waitingRequest.showEmpty, false);
  assert.equal(waitingRequest.pendingMessage, "Waiting for request data…");

  const streaming = {
    _live: true,
    _live_pending: true,
    _live_state: "audit.stream",
    _response_partial: true,
    status_code: 200,
    data: {
      response_body: {
        choices: [{ index: 0, message: { role: "assistant", content: "partial" } }],
      },
    },
  };
  const streamingPane = auditResponsePane(streaming);
  assert.equal(streamingPane.streaming, true);
  assert.equal(streamingPane.showBody, true);
  assert.equal(streamingPane.showPending, false);

  // A stale partial flag on an already-settled entry must not keep the badge.
  const settled = {
    ...streaming,
    _live_state: "audit.completed",
    duration_ns: 1000,
  };
  assert.equal(auditResponsePane(settled).streaming, false);

  const persisted = { data: {} };
  const persistedPane = auditResponsePane(persisted);
  assert.equal(persistedPane.showPending, false);
  assert.equal(persistedPane.showEmpty, true);
  assert.equal(persistedPane.streaming, false);
});

test("audit cache helpers summarize cached prompt usage and derive a preview from the request body", () => {
  const extractSegments = (body) => [body.instructions, body.messages[0].content];

  const entry = {
    usage: {
      input_tokens: 200,
      cached_input_tokens: 150,
      cache_write_input_tokens: 32,
      estimated_cached_characters: 600,
    },
    data: {
      request_body: {
        instructions: "You are a meticulous assistant.",
        messages: [{ role: "user", content: "Summarize the attached policy memo." }],
      },
    },
  };

  assert.equal(auditHasCachedTokens(entry), true);
  assert.equal(auditCacheSharePercent(entry), 75);
  assert.equal(auditCacheRatioLabel(entry), "75.0% cached");
  assert.equal(auditCacheRatioPillLabel(entry), "75.0% cached");
  assert.deepEqual(auditPromptCacheHighlight(entry, extractSegments), {
    characters: 600,
    segments: [
      "You are a meticulous assistant.",
      "Summarize the attached policy memo.",
    ],
  });

  const pane = auditRequestPane(entry, extractSegments);
  assert.equal(pane.bodyCacheRatioLabel, "75.0% cached");
  assert.deepEqual(pane.promptCacheHighlight, {
    characters: 600,
    segments: [
      "You are a meticulous assistant.",
      "Summarize the attached policy memo.",
    ],
  });
});

test("auditEntryLiveInProgress marks only live rows still waiting for a response", () => {
  assert.equal(
    auditEntryLiveInProgress({
      _live: true,
      _live_pending: true,
      _live_state: "audit.started",
    }),
    true,
  );

  assert.equal(
    auditEntryLiveInProgress({
      _live: true,
      _live_pending: true,
      _live_state: "audit.completed",
      status_code: 200,
      duration_ns: 123000000,
    }),
    false,
  );

  assert.equal(
    auditEntryLiveInProgress({
      _live: false,
      _live_pending: false,
    }),
    false,
  );
});

test("formatDurationNs rejects non-finite values", () => {
  assert.equal(formatDurationNs("not-a-number"), "-");
  assert.equal(formatDurationNs(Number.NaN), "-");
  assert.equal(formatDurationNs(Number.POSITIVE_INFINITY), "-");
  assert.equal(formatDurationNs(0), "pending");
  assert.equal(formatDurationNs("1500"), "2 µs");
  assert.equal(formatDurationNs(1230000000), "1.23 s");
});

test("auditResponsePane surfaces error message from captured error body", () => {
  const entry = {
    data: {
      response_body: {
        error: {
          type: "provider_error",
          message: "http2: timeout awaiting response headers",
        },
      },
    },
  };

  const pane = auditResponsePane(entry);

  assert.equal(
    auditEntryErrorMessage(entry),
    "http2: timeout awaiting response headers",
  );
  assert.equal(pane.showErrorMessage, true);
  assert.equal(pane.errorMessage, "http2: timeout awaiting response headers");
  assert.equal(pane.showEmpty, false);
});

test("auditEntryErrorMessage extracts JSON encoded gateway error text", () => {
  const entry = {
    data: {
      error_message:
        '{"error":{"message":"circuit breaker is open - provider temporarily unavailable"}}',
    },
  };

  assert.equal(
    auditEntryErrorMessage(entry),
    "circuit breaker is open - provider temporarily unavailable",
  );
});

test("auditEntryErrorMessage ignores successful response fields", () => {
  const entry = {
    data: {
      response_body: {
        id: "chatcmpl_123",
        choices: [{ message: { content: "hello" } }],
      },
    },
  };

  assert.equal(auditEntryErrorMessage(entry), "");
  assert.equal(auditResponsePane(entry).showErrorMessage, false);
});

test("auditEntryErrorMessage ignores nested error objects on successful responses without top-level error shape", () => {
  const entry = {
    status_code: 200,
    data: {
      response_body: {
        output: {
          error: {
            message: "should not be treated as a response error",
          },
        },
      },
    },
  };

  assert.equal(auditEntryErrorMessage(entry), "");
});

test("auditEntryErrorMessage reads top-level provider error shapes without relying on status code", () => {
  const entry = {
    data: {
      response_body: {
        message: "provider timeout",
        type: "provider_error",
      },
    },
  };

  assert.equal(auditEntryErrorMessage(entry), "provider timeout");
});

test("auditLogWithLiveEntries preserves live preview rows that are not flushed yet", () => {
  const payload = {
    entries: [{ id: "audit-db", request_id: "req-db" }],
    total: 1,
    limit: 25,
    offset: 0,
  };
  const current = [
    {
      id: "audit-live",
      request_id: "req-live",
      _live: true,
      _live_pending: true,
      _audit_flushed: false,
    },
  ];

  const next = auditLogWithLiveEntries(payload, current, noFilters);

  assert.equal(next.entries.length, 2);
  assert.equal(next.entries[0].id, "audit-live");
  assert.equal(next.entries[1].id, "audit-db");
  assert.equal(next.total, 2);
});

test("auditLogWithLiveEntries lets persisted rows replace matching live previews", () => {
  const payload = {
    entries: [{ id: "audit-db", request_id: "req-live" }],
    total: 1,
    limit: 25,
    offset: 0,
  };
  const current = [
    {
      id: "audit-live",
      request_id: "req-live",
      _live: true,
      _live_pending: true,
      _audit_flushed: false,
    },
  ];

  const next = auditLogWithLiveEntries(payload, current, noFilters);

  assert.equal(next.entries.length, 1);
  assert.equal(next.entries[0].id, "audit-db");
  assert.equal(next.total, 1);
});

test("auditLogAllowsLiveEntries respects filters and custom date ranges", () => {
  const now = new Date();
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  const tomorrow = new Date(now);
  tomorrow.setDate(now.getDate() + 1);

  assert.equal(auditLogAllowsLiveEntries({ offset: 0 }, noFilters), true);
  assert.equal(auditLogAllowsLiveEntries({ offset: 25 }, noFilters), false);
  assert.equal(
    auditLogAllowsLiveEntries({ offset: 0 }, { ...noFilters, search: "x" }),
    false,
  );

  assert.equal(
    auditLogAllowsLiveEntries(
      { offset: 0 },
      { ...noFilters, customStartDate: tomorrow },
    ),
    false,
  );
  assert.equal(
    auditLogAllowsLiveEntries(
      { offset: 0 },
      { ...noFilters, customEndDate: yesterday },
    ),
    false,
  );
  assert.equal(
    auditLogAllowsLiveEntries(
      { offset: 0 },
      { ...noFilters, customStartDate: yesterday, customEndDate: tomorrow },
    ),
    true,
  );
});

test("buildAuditLogQuery sends the consolidated audit search and select filters only", () => {
  const qs = buildAuditLogQuery({
    dateQuery: "days=30",
    limit: 25,
    offset: 0,
    search: "team/alpha",
    method: "POST",
    statusCode: "500",
    stream: "true",
  });

  assert.match(qs, /^days=30&limit=25&offset=0/);
  assert.match(qs, /search=team%2Falpha/);
  assert.match(qs, /method=POST/);
  assert.match(qs, /status_code=500/);
  assert.match(qs, /stream=true/);
  assert.doesNotMatch(qs, /[?&](model|provider|path|user_path)=/);
});

test("buildAuditLogQuery omits unset filters", () => {
  const qs = buildAuditLogQuery({
    dateQuery: "start_date=2026-07-01&end_date=2026-07-15",
    limit: 25,
    offset: 50,
    search: "",
    method: "",
    statusCode: "",
    stream: "",
  });

  assert.equal(qs, "start_date=2026-07-01&end_date=2026-07-15&limit=25&offset=50");
});

test("markExpandedEntry lazily marks an opened audit row for details rendering", () => {
  const next = markExpandedEntry({}, { id: "audit-1" });
  assert.deepEqual(next, { "audit-1": true });

  // Already-expanded rows and keyless entries leave the map untouched.
  assert.equal(markExpandedEntry(next, { id: "audit-1" }), next);
  assert.equal(markExpandedEntry(next, {}), next);
});

test("pruneExpandedEntries drops expanded state for rows no longer on the page", () => {
  const pruned = pruneExpandedEntries(
    { "audit-1": true, "audit-2": true },
    [{ id: "audit-2" }, { id: "audit-3" }],
  );

  assert.deepEqual(pruned, { "audit-2": true });

  // No change returns the same map instance.
  const unchanged = { "audit-2": true };
  assert.equal(pruneExpandedEntries(unchanged, [{ id: "audit-2" }]), unchanged);
});

test("auditTabKeydownTarget roves the tablist with arrow and home/end keys", () => {
  const entry = {
    data: {
      request_body: { model: "gpt-5" },
      response_body: { ok: true },
    },
  };

  // A plain entry (no failed attempts) yields exactly the request + response tabs.
  const ids = auditPanes(entry).map((p) => p.id);
  assert.equal(ids.join(","), "request,response");

  // Next/previous, wrapping at the ends.
  assert.equal(auditTabKeydownTarget("ArrowRight", ids, "request"), "response");
  assert.equal(auditTabKeydownTarget("ArrowDown", ids, "request"), "response");
  assert.equal(auditTabKeydownTarget("ArrowRight", ids, "response"), "request");
  assert.equal(auditTabKeydownTarget("ArrowLeft", ids, "request"), "response");
  assert.equal(auditTabKeydownTarget("ArrowUp", ids, "request"), "response");

  // Home/End jump to the ends.
  assert.equal(auditTabKeydownTarget("Home", ids, "response"), "request");
  assert.equal(auditTabKeydownTarget("End", ids, "request"), "response");

  // Unhandled keys leave the selection untouched.
  assert.equal(auditTabKeydownTarget("Tab", ids, "request"), null);
  assert.equal(auditTabKeydownTarget("a", ids, "request"), null);
});

test("auditPanes inserts request revision tabs between request and response", () => {
  const revision = {
    seq: 1,
    rewriter: "pro-token-compression",
    bytes_before: 1572,
    bytes_after: 1249,
    body: { model: "gpt-5", compressed: true },
    detail: { tokens_saved_estimate: 89, blocks_replaced: 1 },
  };
  const entry = {
    data: {
      request_body: { model: "gpt-5" },
      response_body: { ok: true },
      request_revisions: [revision],
    },
  };

  assert.equal(
    auditPanes(entry)
      .map((p) => p.id)
      .join(","),
    "request,revision-1,response",
  );

  const pane = auditRequestRevisionPane(entry, revision);
  assert.equal(pane.title, "Rewritten");
  assert.equal(pane.kind, "pro-token-compression");
  assert.equal(pane.seq, 0); // a single revision hides the #seq chip
  assert.equal(pane.headersTitle, "What changed");
  assert.equal(pane.headers.bytes, "1572 → 1249");
  assert.equal(pane.headers.detail.tokens_saved_estimate, 89);
  assert.equal(pane.savingsLabel, "-21%");
  assert.equal(pane.showBody, true);
  assert.equal(pane.showTooLarge, false);

  // Without a captured body the pane explains why instead of showing JSON.
  const bare = auditRequestRevisionPane(entry, {
    seq: 2,
    rewriter: "x",
    bytes_before: 10,
    bytes_after: 8,
  });
  assert.equal(bare.showBody, false);
  assert.equal(bare.showTooLarge, true);

  // Multiple revisions keep their sequence chips and stable tab ids.
  entry.data.request_revisions = [revision, { ...revision, seq: 2 }];
  assert.equal(
    auditPanes(entry)
      .map((p) => p.id)
      .join(","),
    "request,revision-1,revision-2,response",
  );
  assert.equal(auditRequestRevisionPane(entry, revision).seq, 1);
});

test("auditRevisionPercentLabel reports the share of the body removed", () => {
  assert.equal(
    auditRevisionPercentLabel({ bytes_before: 1572, bytes_after: 1249 }),
    "-21%",
  );
  assert.equal(
    auditRevisionPercentLabel({ bytes_before: 1000, bytes_after: 560 }),
    "-44%",
  );
  assert.equal(
    auditRevisionPercentLabel({ bytes_before: 1000, bytes_after: 954 }),
    "-4.6%",
  );
  // Missing sizes or a revision that grew the body renders no percent.
  assert.equal(auditRevisionPercentLabel({ bytes_before: 0, bytes_after: 10 }), "");
  assert.equal(
    auditRevisionPercentLabel({ bytes_before: 100, bytes_after: 120 }),
    "",
  );
  assert.equal(auditRevisionPercentLabel({}), "");
  assert.equal(auditRevisionPercentLabel(null), "");
});

test("entries without revisions render no revision tabs", () => {
  const entry = {
    data: { request_body: { model: "gpt-5" }, response_body: { ok: true } },
  };
  assert.equal(
    auditPanes(entry)
      .map((p) => p.id)
      .join(","),
    "request,response",
  );
  assert.equal(auditRequestRevisions(entry).length, 0);
});

test("formatJSON pretty-prints JSON strings and objects, passing other text through", () => {
  assert.equal(formatJSON(null), "Not captured");
  assert.equal(formatJSON(""), "Not captured");
  assert.equal(formatJSON('{"a":1}'), '{\n  "a": 1\n}');
  assert.equal(formatJSON("plain text"), "plain text");
  assert.equal(formatJSON({ a: 1 }), '{\n  "a": 1\n}');
});
