import test from "node:test";
import assert from "node:assert/strict";

import {
  auditRecordChangesAfter,
  isLiveAuditRecordChange,
  mergeAuditRecord,
} from "../src/pages/audit-logs/audit-records.js";

test("slim and replayed records cannot erase captured interaction bodies", () => {
  const full = {
    id: "audit-1",
    _live: true,
    _live_state: "audit.completed",
    data: {
      request_body: { messages: [{ role: "user", content: "Hello" }] },
      response_body: { choices: [{ message: { role: "assistant", content: "Hi" } }] },
    },
  };

  const merged = mergeAuditRecord(full, {
    id: "audit-1",
    bodies_omitted: true,
    data: { request_body_captured: true, response_body_captured: true },
  });

  assert.deepEqual(merged.data.request_body, full.data.request_body);
  assert.deepEqual(merged.data.response_body, full.data.response_body);
  assert.equal(merged._live_state, "audit.completed");
  assert.equal(merged.bodies_omitted, false);
});

test("record lifecycle and full-detail state are monotonic", () => {
  const merged = mergeAuditRecord({
    id: "audit-1",
    _live_state: "audit.flushed",
    _audit_flushed: true,
    _detail_loaded: true,
    bodies_omitted: false,
    data: { request_body: { input: "hello" } },
  }, {
    id: "audit-1",
    _live_state: "audit.updated",
    _live_pending: true,
    bodies_omitted: true,
    data: { workflow_features: { cache: true } },
  });

  assert.equal(merged._live_state, "audit.flushed");
  assert.equal(merged._audit_flushed, true);
  assert.equal(merged._live_pending, false);
  assert.equal(merged._detail_loaded, true);
  assert.equal(merged.bodies_omitted, false);
  assert.deepEqual(merged.data.request_body, { input: "hello" });
});

test("batched record changes are consumed without dropping intermediate events", () => {
  const changes = [
    { version: 7, key: "older", eventType: "audit.flushed" },
    { version: 8, key: "newer", eventType: "audit.updated" },
    { version: 9, key: "newer", eventType: "audit.flushed" },
  ];
  assert.deepEqual(auditRecordChangesAfter(changes, 7), changes.slice(1));
  assert.deepEqual(auditRecordChangesAfter(changes, 9), []);
  assert.equal(isLiveAuditRecordChange(changes[1]), true);
  assert.equal(isLiveAuditRecordChange({ eventType: "audit.list" }), false);
});

test("slim revision metadata cannot erase loaded rewrite bodies", () => {
  const detailed = mergeAuditRecord(null, {
    id: "audit-9",
    data: {
      request_revisions: [
        { seq: 1, rewriter: "compress", bytes_before: 200, bytes_after: 100,
          body: { messages: [] }, detail: { blocks: 3 } },
      ],
    },
  });

  // A later conversation/list refresh ships metadata-only revisions.
  const merged = mergeAuditRecord(detailed, {
    id: "audit-9",
    data: {
      request_revisions: [
        { seq: 1, rewriter: "compress", bytes_before: 200, bytes_after: 100 },
        { seq: 2, rewriter: "guard", bytes_before: 100, bytes_after: 100, no_change: true },
      ],
    },
  });

  assert.equal(merged.data.request_revisions.length, 2);
  assert.deepEqual(merged.data.request_revisions[0].body, { messages: [] });
  assert.deepEqual(merged.data.request_revisions[0].detail, { blocks: 3 });
  assert.equal(merged.data.request_revisions[1].no_change, true);
});

test("slim guardrail outcomes cannot erase loaded plugin detail", () => {
  const detailed = mergeAuditRecord(null, {
    id: "audit-12",
    data: {
      guardrails: [
        { seq: 1, phase: "prompt", instance: "pii", action: "allow", detail: { hits: 2 } },
        { seq: 2, phase: "prompt", instance: "policy", action: "block", code: "x", detail: { rule: "r1" } },
      ],
    },
  });

  // A later list refresh ships outcomes without detail; a live event may
  // even ship fewer of them.
  const merged = mergeAuditRecord(detailed, {
    id: "audit-12",
    data: {
      guardrails: [
        { seq: 1, phase: "prompt", instance: "pii", action: "allow" },
        { seq: 2, phase: "prompt", instance: "policy", action: "block", code: "x", message: "late" },
        { seq: 3, phase: "response", instance: "scan", action: "allow" },
      ],
    },
  });
  assert.equal(merged.data.guardrails.length, 3);
  assert.deepEqual(merged.data.guardrails[0].detail, { hits: 2 });
  assert.deepEqual(merged.data.guardrails[1].detail, { rule: "r1" });
  assert.equal(merged.data.guardrails[1].message, "late");
  assert.equal(merged.data.guardrails[2].detail, undefined);

  // A patch without outcomes keeps the ones already held.
  const kept = mergeAuditRecord(detailed, { id: "audit-12", data: { response_body: {} } });
  assert.equal(kept.data.guardrails.length, 2);

  // A live event carrying fewer outcomes than held updates the ones it has
  // and keeps the rest.
  const partial = mergeAuditRecord(merged, {
    id: "audit-12",
    data: { guardrails: [{ seq: 2, phase: "prompt", instance: "policy", action: "block", code: "y" }] },
  });
  assert.deepEqual(
    partial.data.guardrails.map((outcome) => outcome.seq),
    [1, 2, 3],
  );
  assert.equal(partial.data.guardrails[1].code, "y");
  assert.deepEqual(partial.data.guardrails[1].detail, { rule: "r1" });
  assert.deepEqual(partial.data.guardrails[0].detail, { hits: 2 });
});
