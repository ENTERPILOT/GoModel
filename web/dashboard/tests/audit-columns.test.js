import { test } from "node:test";
import assert from "node:assert/strict";

import {
  AUDIT_COLUMNS,
  auditEntryLabels,
  normalizeAuditColumns,
  parseStoredAuditColumns,
} from "../src/pages/audit-logs/audit-columns.js";

const allKeys = AUDIT_COLUMNS.map((column) => column.key);

test("default audit columns keep today's row and leave labels opt-in", () => {
  const defaults = normalizeAuditColumns(null);
  assert.deepEqual(Object.keys(defaults), allKeys);
  for (const key of allKeys) {
    assert.equal(defaults[key], key !== "labels", key);
  }
  assert.equal(AUDIT_COLUMNS.length, new Set(allKeys).size);
});

test("normalizeAuditColumns keeps stored booleans, drops unknown keys and fills missing ones", () => {
  const got = normalizeAuditColumns({ path: false, labels: true, nope: true, model: "yes" });
  assert.equal(got.path, false);
  assert.equal(got.labels, true);
  assert.equal(got.model, true);
  assert.equal(got.status, true);
  assert.equal("nope" in got, false);
  assert.deepEqual(Object.keys(got), allKeys);
});

test("normalizeAuditColumns ignores values that are not plain objects", () => {
  const defaults = normalizeAuditColumns(null);
  for (const value of [undefined, "path", 7, ["path"]]) {
    assert.deepEqual(normalizeAuditColumns(value), defaults);
  }
});

test("parseStoredAuditColumns restores a stored choice and falls back on bad JSON", () => {
  const defaults = normalizeAuditColumns(null);
  assert.equal(parseStoredAuditColumns('{"duration":false}').duration, false);
  assert.deepEqual(parseStoredAuditColumns("{}"), defaults);
  assert.deepEqual(parseStoredAuditColumns("not json"), defaults);
  assert.deepEqual(parseStoredAuditColumns(null), defaults);
});

test("auditEntryLabels reads the labels of a list entry", () => {
  assert.deepEqual(auditEntryLabels({ data: { labels: ["team-a", "prod"] } }), ["team-a", "prod"]);
  assert.deepEqual(auditEntryLabels({ data: {} }), []);
  assert.deepEqual(auditEntryLabels({ data: { labels: "x" } }), []);
  assert.deepEqual(auditEntryLabels(null), []);
});
