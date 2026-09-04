// Pure-logic tests for the audit-logs page, ported from the legacy
// audit-list.test.cjs (Alpine/DOM-harness cases are covered by the Svelte
// components and were not ported).

import test from "node:test";
import assert from "node:assert/strict";

import {
  auditCacheRatioLabel,
  auditCacheRatioPillLabel,
  auditCacheSharePercent,
  auditChangedRequestRevisions,
  auditEntryErrorMessage,
  auditEntryLiveInProgress,
  auditHasCachedTokens,
  auditLogAllowsLiveEntries,
  auditLogWithLiveEntries,
  auditNoChangeSteps,
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
  auditGroupedLogWithLiveEntries,
  auditIsThreadHead,
  auditLogFromSessions,
  auditSessionCount,
  auditSessionId,
  auditTabKeydownTarget,
  buildAuditLogQuery,
  buildAuditSessionQuery,
  pruneThreadMap,
  toggleExpandedThread,
  formatDurationNs,
  formatJSON,
  toggleExpandedEntry,
  mergeAuditThreadChildren,
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

test("toggleExpandedEntry keeps at most one audit row expanded", () => {
  const next = toggleExpandedEntry({}, { id: "audit-1" });
  assert.deepEqual(next, { "audit-1": true });

  // Opening another row folds the first; toggling that row again collapses it.
  const second = toggleExpandedEntry(next, { id: "audit-2" });
  assert.deepEqual(second, { "audit-2": true });
  assert.deepEqual(toggleExpandedEntry(second, { id: "audit-2" }), {});

  // Keyless entries leave the map untouched.
  assert.deepEqual(toggleExpandedEntry(next, { id: "audit-1" }), {});
  assert.equal(toggleExpandedEntry(next, {}), next);
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

test("rewriters that changed nothing become Request-tab pills, not tabs", () => {
  const entry = {
    data: {
      request_body: { model: "gpt-5" },
      response_body: { ok: true },
      request_revisions: [
        {
          seq: 1,
          rewriter: "pro-token-compression",
          bytes_before: 165689,
          bytes_after: 165689,
          no_change: true,
        },
      ],
    },
  };

  // The step is recorded, but it spends no tab.
  assert.equal(
    auditPanes(entry)
      .map((p) => p.id)
      .join(","),
    "request,response",
  );
  assert.equal(auditChangedRequestRevisions(entry).length, 0);

  const steps = auditNoChangeSteps(entry);
  assert.equal(steps.length, 1);
  assert.equal(steps[0].id, "step-1");
  assert.equal(steps[0].rewriter, "pro-token-compression");
  assert.equal(steps[0].label, "pro-token-compression: no change");
  assert.match(steps[0].title, /forwarded the request unchanged/);
  assert.deepEqual(auditRequestPane(entry).noChangeSteps, steps);

  // Mixed chains: only the rewrite that changed the body gets a pane, and a
  // lone changed revision still hides its #seq chip.
  entry.data.request_revisions.push({
    seq: 2,
    rewriter: "swap",
    bytes_before: 165689,
    bytes_after: 120000,
  });
  assert.equal(
    auditPanes(entry)
      .map((p) => p.id)
      .join(","),
    "request,revision-2,response",
  );
  assert.equal(auditNoChangeSteps(entry).length, 1);
  assert.equal(
    auditRequestRevisionPane(entry, entry.data.request_revisions[1]).seq,
    0,
  );

  // Entries written before no-change tracking have no flag and stay tabs.
  const legacy = {
    data: {
      request_revisions: [{ seq: 1, rewriter: "swap", bytes_before: 10, bytes_after: 8 }],
    },
  };
  assert.equal(auditChangedRequestRevisions(legacy).length, 1);
  assert.equal(auditNoChangeSteps(legacy).length, 0);
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

test("per-attempt response panes carry the tried model for the tab badge", () => {
  const entry = {
    data: {
      request_body: { model: "virtual-model-a" },
      response_body: { ok: true },
      attempts: [
        {
          seq: 1,
          kind: "primary",
          provider_name: "provider-a",
          model: "provider-a/Qwen/Qwen3.6-35B-A3B-FP8",
          status_code: 503,
          success: false,
        },
        {
          seq: 2,
          kind: "failover",
          provider_name: "provider-b",
          model: "provider-b/glm-5.3-flash",
          status_code: 400,
          success: false,
        },
        {
          seq: 3,
          kind: "failover",
          provider_name: "provider-b",
          model: "provider-b/glm-5.3-flash",
          status_code: 200,
          success: true,
        },
      ],
    },
  };

  const panes = auditPanes(entry);
  assert.equal(
    panes.map((p) => p.id).join(","),
    "request,response-1,response-2,response-3",
  );

  // Each failover pane carries the requested → tried model strip: the model
  // the request named and the concrete model that attempt targeted. Without
  // an alias resolution the source chip reads "requested", not "virtual".
  const first = panes.find((p) => p.id === "response-1").pane;
  assert.deepEqual(first.modelStrip, {
    label: "requested",
    source: "virtual-model-a",
    tried: "provider-a/Qwen/Qwen3.6-35B-A3B-FP8",
  });
  const second = panes.find((p) => p.id === "response-2").pane;
  assert.deepEqual(second.modelStrip, {
    label: "requested",
    source: "virtual-model-a",
    tried: "provider-b/glm-5.3-flash",
  });

  // Alias-resolved entries label the source "virtual" and prefer top-level
  // requested_model over entry.model and the request body (the same
  // precedence the metadata row uses).
  const aliased = {
    requested_model: "smart-vm",
    model: "entry-model",
    alias_used: true,
    data: {
      request_body: { model: "body-model" },
      attempts: [
        { seq: 1, kind: "primary", model: "provider-a/qwen-35b", status_code: 503, success: false },
        { seq: 2, kind: "failover", model: "provider-b/glm-flash", status_code: 200, success: true },
      ],
    },
  };
  assert.deepEqual(auditPanes(aliased).find((p) => p.id === "response-1").pane.modelStrip, {
    label: "virtual",
    source: "smart-vm",
    tried: "provider-a/qwen-35b",
  });

  // Without requested_model, top-level entry.model steps in before the body.
  const entryModel = {
    model: "entry-model",
    data: {
      attempts: [
        { seq: 1, kind: "primary", model: "provider-a/qwen-35b", status_code: 503, success: false },
        { seq: 2, kind: "failover", model: "provider-b/glm-flash", status_code: 200, success: true },
      ],
    },
  };
  assert.deepEqual(auditPanes(entryModel).find((p) => p.id === "response-1").pane.modelStrip, {
    label: "requested",
    source: "entry-model",
    tried: "provider-a/qwen-35b",
  });

  // A single successful attempt collapses to the plain response tab, which
  // carries no model strip (the row's model column already names it).
  const single = {
    data: {
      request_body: { model: "m" },
      response_body: { ok: true },
      attempts: [{ seq: 1, kind: "primary", model: "m", status_code: 200, success: true }],
    },
  };
  const singlePanes = auditPanes(single);
  assert.equal(singlePanes.map((p) => p.id).join(","), "request,response");
  assert.ok(!singlePanes.find((p) => p.id === "response").pane.modelStrip);

  // A single FAILED attempt keeps the per-attempt path but still hides the
  // strip — the same `single` rule as the seq/kind/status chips.
  const loneFailure = {
    data: {
      request_body: { model: "m" },
      attempts: [{ seq: 1, kind: "primary", model: "m", status_code: 503, success: false }],
    },
  };
  const lonePanes = auditPanes(loneFailure);
  assert.equal(lonePanes.map((p) => p.id).join(","), "request,response-1");
  assert.ok(!lonePanes.find((p) => p.id === "response-1").pane.modelStrip);

  // Entries that predate per-attempt model capture render no strip instead of
  // the "-" placeholder.
  const legacy = {
    data: {
      request_body: { model: "m" },
      attempts: [
        { seq: 1, kind: "primary", status_code: 503, success: false },
        { seq: 2, kind: "failover", model: "m", status_code: 200, success: true },
      ],
    },
  };
  const legacyPanes = auditPanes(legacy);
  assert.ok(!legacyPanes.find((p) => p.id === "response-1").pane.modelStrip);
  assert.deepEqual(legacyPanes.find((p) => p.id === "response-2").pane.modelStrip, {
    label: "requested",
    source: "m",
    tried: "m",
  });
});

test("formatJSON pretty-prints JSON strings and objects, passing other text through", () => {
  assert.equal(formatJSON(null), "Not captured");
  assert.equal(formatJSON(""), "Not captured");
  assert.equal(formatJSON('{"a":1}'), '{\n  "a": 1\n}');
  assert.equal(formatJSON("plain text"), "plain text");
  assert.equal(formatJSON({ a: 1 }), '{\n  "a": 1\n}');
});

// --- Session grouping helpers ------------------------------------------------

test("buildAuditSessionQuery encodes the session id and omits active list filters", () => {
  const qs = buildAuditSessionQuery({ sessionId: "team/app|s 1", limit: 100 });
  assert.equal(qs, "session_id=team%2Fapp%7Cs%201&limit=100&offset=0");
});

test("session head helpers handle missing ids and counts", () => {
  assert.equal(auditSessionId({ session_id: " s-1 " }), "s-1");
  assert.equal(auditSessionId({}), "");
  assert.equal(auditSessionCount({ session_count: 5 }), 5);
  assert.equal(auditSessionCount({ session_count: 0 }), 1);
  assert.equal(auditSessionCount({}), 1);
  assert.equal(auditIsThreadHead({ session_id: "s-1", session_count: 2 }), true);
  assert.equal(auditIsThreadHead({ session_id: "s-1", session_count: 1 }), false);
  assert.equal(auditIsThreadHead({ session_count: 3 }), false);
});

test("auditLogFromSessions maps thread summaries into head entries", () => {
  const payload = {
    sessions: [
      {
        request_count: 6,
        latest: { id: "log-3", session_id: "s-1", status_code: 200 },
      },
      { request_count: 1, latest: { id: "solo", status_code: 200 } },
      { request_count: 2 }, // no latest: dropped
    ],
    total: 12,
    limit: 25,
    offset: 25,
  };
  const mapped = auditLogFromSessions(payload);
  assert.equal(mapped.entries.length, 2);
  assert.equal(mapped.entries[0].id, "log-3");
  assert.equal(mapped.entries[0].session_count, 6);
  assert.equal(mapped.entries[1].id, "solo");
  assert.equal(mapped.entries[1].session_count, 1);
  assert.equal(mapped.total, 12);
  assert.equal(mapped.offset, 25);
});

test("mergeAuditThreadChildren drops heads by id and request_id", () => {
  const merged = mergeAuditThreadChildren(
    null,
    [
      { id: "log-3", request_id: "req-3" },
      { id: "log-2", request_id: "req-2" },
      { id: "other", request_id: "req-3" },
      { id: "log-1" },
    ],
    [{ id: "log-3", request_id: "req-3" }],
  );
  assert.deepEqual(
    merged.entries.map((entry) => entry.id),
    ["log-2", "log-1"],
  );
  assert.equal(merged.preservedCount, 0);
});

test("mergeAuditThreadChildren preserves unpersisted live entries on top", () => {
  const previous = {
    loading: true,
    loaded: false,
    entries: [
      // Still pending: the server does not know it yet.
      { id: "live-2", request_id: "req-live-2", _live: true },
      // Already persisted: the fetched page returns it, so dedup by identity.
      { id: "live-1", request_id: "req-live-1", _live: true },
      // Not live: must not survive a full fetch that omits it.
      { id: "stale", request_id: "req-stale" },
    ],
    total: 3,
  };
  const merged = mergeAuditThreadChildren(
    previous,
    [
      { id: "head", request_id: "req-head" },
      { id: "live-1", request_id: "req-live-1" },
      { id: "old-1", request_id: "req-old-1" },
    ],
    [{ id: "head", request_id: "req-head" }],
  );
  assert.deepEqual(
    merged.entries.map((entry) => entry.id),
    ["live-2", "live-1", "old-1"],
  );
  assert.equal(merged.preservedCount, 1);
});

test("mergeAuditThreadChildren keeps a head demoted while the fetch was in flight", () => {
  // Race: a new live request replaced the thread head during the session-page
  // fetch. The original head is now a demoted child — excluding only the
  // CURRENT head must keep it in the merged children.
  const originalHead = { id: "old-head", request_id: "req-old" };
  const currentHead = { id: "new-head", request_id: "req-new" };
  const previous = {
    loading: true,
    loaded: false,
    // foldLiveAuditIntoThread prepended the demoted head into the slot.
    entries: [{ ...originalHead }],
    total: 1,
  };
  const merged = mergeAuditThreadChildren(
    previous,
    [{ ...originalHead }, { id: "old-1", request_id: "req-1" }],
    [currentHead],
  );
  assert.deepEqual(
    merged.entries.map((entry) => entry.id),
    ["old-head", "old-1"],
  );
  assert.equal(merged.preservedCount, 0);
});

test("toggleExpandedThread flips per-session and pruneThreadMap drops off-page threads", () => {
  let map = toggleExpandedThread({}, "s-1");
  assert.deepEqual(map, { "s-1": true });
  map = toggleExpandedThread(map, "s-2");
  map = toggleExpandedThread(map, "s-1");
  assert.deepEqual(map, { "s-2": true });
  assert.equal(toggleExpandedThread(map, ""), map);

  const pruned = pruneThreadMap(
    { "s-2": true, gone: true },
    [{ session_id: "s-2" }, { id: "solo" }],
  );
  assert.deepEqual(pruned, { "s-2": true });
  // No change returns the same instance (reactivity-friendly).
  const same = { "s-2": true };
  assert.equal(pruneThreadMap(same, [{ session_id: "s-2" }]), same);
});

test("auditGroupedLogWithLiveEntries folds pending previews into fetched heads", () => {
  const payload = {
    entries: [
      { id: "head-a", session_id: "s-a", session_count: 3 },
      { id: "solo", session_id: "" },
    ],
    total: 2,
    limit: 25,
    offset: 0,
  };
  const pendingSameSession = {
    id: "live-1",
    session_id: "s-a",
    session_count: 4,
    _live: true,
    _live_pending: true,
  };
  const pendingNewSession = {
    id: "live-2",
    session_id: "s-b",
    _live: true,
    _live_pending: true,
  };
  const next = auditGroupedLogWithLiveEntries(
    payload,
    [pendingSameSession, pendingNewSession],
    {},
  );
  // s-b preview prepends as a new singleton thread; s-a preview replaces its head.
  assert.deepEqual(
    next.entries.map((entry) => entry.id),
    ["live-2", "live-1", "solo"],
  );
  assert.equal(next.entries[1].session_count, 4);
  assert.equal(next.total, 3);
});

test("auditGroupedLogWithLiveEntries increments counts for a new singleton preview", () => {
  const payload = {
    entries: [
      {
        id: "head-a",
        session_id: "s-a",
        session_count: 6,
      },
    ],
    total: 1,
    limit: 25,
    offset: 0,
  };
  const pending = {
    id: "live-1",
    session_id: "s-a",
    session_count: 1,
    _live: true,
    _live_pending: true,
  };

  const next = auditGroupedLogWithLiveEntries(payload, [pending], {});

  assert.deepEqual(next.entries.map((entry) => entry.id), ["live-1"]);
  assert.equal(next.entries[0].session_count, 7);
  assert.equal(next.total, 1);
});

test("auditGroupedLogWithLiveEntries keeps persisted rows over matching previews", () => {
  const payload = {
    entries: [{ id: "head-a", request_id: "req-1", session_id: "s-a", session_count: 2 }],
    total: 1,
    limit: 25,
    offset: 0,
  };
  const pending = {
    id: "head-a",
    request_id: "req-1",
    session_id: "s-a",
    _live: true,
    _live_pending: true,
  };
  const next = auditGroupedLogWithLiveEntries(payload, [pending], {});
  assert.deepEqual(next.entries.map((entry) => entry.id), ["head-a"]);
  assert.equal(next.entries[0].session_count, 2);
  assert.equal(next.total, 1);
});
