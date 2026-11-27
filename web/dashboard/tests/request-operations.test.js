import { test } from "node:test";
import assert from "node:assert/strict";

import {
  REQUEST_TYPES,
  requestEntryTypeVisible,
  requestLogExcludeOperationsQuery,
  requestTypeForPath,
  normalizeHiddenTypes,
} from "../src/pages/requests/request-operations.js";
import { requestLogWithLiveEntries, buildRequestLogQuery, buildRequestLogSessionQuery } from "../src/pages/requests/request-log-logic.js";

test("requestTypeForPath mirrors the gateway endpoint classification", () => {
  const cases = [
    ["/v1/chat/completions", "chat"],
    ["/v1/messages", "chat"],
    ["/v1beta/models/{model}:generateContent", "chat"],
    ["/v1beta/models/{model}:streamGenerateContent", "chat"],
    ["/v1beta/models", ""],
    ["/v1/messages/batches/b_1", "batches"],
    ["/v1/responses/resp_1/input_items", "responses"],
    ["/v1/conversations", "responses"],
    ["/v1/files/f_1/content", "batches"],
    ["/v1/audio/transcriptions?x=1", "audio"],
    ["/v1/images/edits/", "images"],
    ["/v1/systemone", "systemone"],
    ["/v1/systemone/permute", "systemone"],
    ["/v1/systemone/separate/", "systemone"],
    ["/v1/systemone/other", ""],
    ["/v1/decisions", "systemone"],
    ["/v1/realtime/translations/calls", "realtime"],
    ["/mcp", "mcp"],
    ["/mcp/github", "mcp"],
    ["/mcpx", ""],
    ["/p/openai/v1/models", "passthrough"],
    ["/admin/request-logs", ""],
    ["", ""],
  ];
  for (const [path, want] of cases) {
    assert.equal(requestTypeForPath(path), want, path);
  }
});

test("normalizeHiddenTypes keeps known unique keys", () => {
  assert.deepEqual(normalizeHiddenTypes(["mcp", "nope", "mcp"]), ["mcp"]);
  const all = REQUEST_TYPES.map((type) => type.key);
  assert.deepEqual(normalizeHiddenTypes(all), all);
  assert.deepEqual(normalizeHiddenTypes("mcp"), []);
});

test("requestLogExcludeOperationsQuery lists the operations of the hidden types", () => {
  assert.equal(requestLogExcludeOperationsQuery([]), "");
  assert.equal(
    requestLogExcludeOperationsQuery(["mcp", "audio", "passthrough"]),
    "audio_speech,audio_transcriptions,audio_translations,provider_passthrough,mcp",
  );
});

test("requests list and session queries send exclude_operation only when a type is hidden", () => {
  const base = { dateQuery: "days=7", limit: 25, offset: 0 };
  assert.doesNotMatch(buildRequestLogQuery(base), /exclude_operation=/);
  assert.match(buildRequestLogQuery({ ...base, hiddenTypes: ["mcp"] }), /&exclude_operation=mcp$/);
  assert.doesNotMatch(buildRequestLogSessionQuery({ sessionId: "s" }), /exclude_operation=/);
  assert.match(
    buildRequestLogSessionQuery({ sessionId: "s", hiddenTypes: ["passthrough"] }),
    /&exclude_operation=provider_passthrough$/,
  );
});

test("requestEntryTypeVisible drops hidden types and keeps unclassified rows", () => {
  assert.equal(requestEntryTypeVisible({ path: "/mcp" }, []), true);
  assert.equal(requestEntryTypeVisible({ path: "/mcp" }, ["mcp"]), false);
  assert.equal(requestEntryTypeVisible({ path: "/v1/chat/completions" }, ["mcp"]), true);
  assert.equal(requestEntryTypeVisible({ path: "/sso/callback" }, ["mcp"]), true);
  assert.equal(requestEntryTypeVisible({}, ["mcp"]), true);
});

test("requestLogWithLiveEntries keeps pending previews of hidden types off the page", () => {
  const payload = { entries: [], total: 0, limit: 25, offset: 0 };
  const preview = (id, path) => ({ id, request_id: id, path, _live: true, _live_pending: true, _request_log_flushed: false });
  const current = [preview("chat", "/v1/chat/completions"), preview("tool", "/mcp")];
  const next = requestLogWithLiveEntries(payload, current, { hiddenTypes: ["mcp"] });
  assert.deepEqual(next.entries.map((entry) => entry.id), ["chat"]);
  assert.equal(next.total, 1);
});
