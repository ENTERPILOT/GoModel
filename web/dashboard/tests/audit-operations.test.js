import { test } from "node:test";
import assert from "node:assert/strict";

import {
  AUDIT_TYPES,
  auditEntryTypeVisible,
  auditOperationsQuery,
  auditTypeForPath,
  normalizeHiddenTypes,
} from "../src/pages/audit-logs/audit-operations.js";
import { auditLogWithLiveEntries, buildAuditLogQuery } from "../src/pages/audit-logs/audit-logic.js";

test("auditTypeForPath mirrors the gateway endpoint classification", () => {
  const cases = [
    ["/v1/chat/completions", "chat"],
    ["/v1/messages", "chat"],
    ["/v1/messages/batches/b_1", "batches"],
    ["/v1/responses/resp_1/input_items", "responses"],
    ["/v1/conversations", "responses"],
    ["/v1/files/f_1/content", "batches"],
    ["/v1/audio/transcriptions?x=1", "audio"],
    ["/v1/images/edits/", "images"],
    ["/v1/realtime/translations/calls", "realtime"],
    ["/mcp", "mcp"],
    ["/mcp/github", "mcp"],
    ["/mcpx", ""],
    ["/p/openai/v1/models", "passthrough"],
    ["/admin/audit/log", ""],
    ["", ""],
  ];
  for (const [path, want] of cases) {
    assert.equal(auditTypeForPath(path), want, path);
  }
});

test("normalizeHiddenTypes drops unknown keys and never hides everything", () => {
  assert.deepEqual(normalizeHiddenTypes(["mcp", "nope", "mcp"]), ["mcp"]);
  assert.deepEqual(normalizeHiddenTypes(AUDIT_TYPES.map((type) => type.key)), []);
  assert.deepEqual(normalizeHiddenTypes("mcp"), []);
});

test("auditOperationsQuery lists the operations of the visible types", () => {
  assert.equal(auditOperationsQuery([]), "");
  const ops = auditOperationsQuery(["mcp", "audio", "passthrough"]).split(",");
  assert.ok(ops.includes("chat_completions"));
  assert.ok(ops.includes("conversations"));
  assert.ok(!ops.includes("mcp"));
  assert.ok(!ops.includes("audio_speech"));
  assert.ok(!ops.includes("provider_passthrough"));
});

test("buildAuditLogQuery sends the operation filter only when a type is hidden", () => {
  const base = { dateQuery: "days=7", limit: 25, offset: 0 };
  assert.doesNotMatch(buildAuditLogQuery(base), /operation=/);
  const qs = buildAuditLogQuery({ ...base, hiddenTypes: ["mcp"] });
  assert.match(qs, /operation=chat_completions%2Cresponses%2C/);
  assert.doesNotMatch(qs, /%2Cmcp/);
});

test("auditEntryTypeVisible drops hidden and unclassified live rows", () => {
  assert.equal(auditEntryTypeVisible({ path: "/mcp" }, []), true);
  assert.equal(auditEntryTypeVisible({ path: "/mcp" }, ["mcp"]), false);
  assert.equal(auditEntryTypeVisible({ path: "/v1/chat/completions" }, ["mcp"]), true);
  assert.equal(auditEntryTypeVisible({}, ["mcp"]), false);
});

test("auditLogWithLiveEntries keeps pending previews of hidden types off the page", () => {
  const payload = { entries: [], total: 0, limit: 25, offset: 0 };
  const preview = (id, path) => ({ id, request_id: id, path, _live: true, _live_pending: true, _audit_flushed: false });
  const current = [preview("chat", "/v1/chat/completions"), preview("tool", "/mcp")];
  const next = auditLogWithLiveEntries(payload, current, { hiddenTypes: ["mcp"] });
  assert.deepEqual(next.entries.map((entry) => entry.id), ["chat"]);
  assert.equal(next.total, 1);
});
