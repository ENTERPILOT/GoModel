import { test } from "node:test";
import assert from "node:assert/strict";

import {
  REQUEST_COLUMNS,
  requestEntryLabels,
  normalizeRequestColumns,
  parseStoredRequestColumns,
} from "../src/pages/requests/request-columns.js";

const allKeys = REQUEST_COLUMNS.map((column) => column.key);

test("default request log columns keep today's row and leave labels opt-in", () => {
  const defaults = normalizeRequestColumns(null);
  assert.deepEqual(Object.keys(defaults), allKeys);
  for (const key of allKeys) {
    assert.equal(defaults[key], key !== "labels", key);
  }
  assert.equal(REQUEST_COLUMNS.length, new Set(allKeys).size);
});

test("normalizeRequestColumns keeps stored booleans, drops unknown keys and fills missing ones", () => {
  const got = normalizeRequestColumns({ path: false, labels: true, nope: true, model: "yes" });
  assert.equal(got.path, false);
  assert.equal(got.labels, true);
  assert.equal(got.model, true);
  assert.equal(got.status, true);
  assert.equal("nope" in got, false);
  assert.deepEqual(Object.keys(got), allKeys);
});

test("normalizeRequestColumns ignores values that are not plain objects", () => {
  const defaults = normalizeRequestColumns(null);
  for (const value of [undefined, "path", 7, ["path"]]) {
    assert.deepEqual(normalizeRequestColumns(value), defaults);
  }
});

test("parseStoredRequestColumns restores a stored choice and falls back on bad JSON", () => {
  const defaults = normalizeRequestColumns(null);
  assert.equal(parseStoredRequestColumns('{"duration":false}').duration, false);
  assert.deepEqual(parseStoredRequestColumns("{}"), defaults);
  assert.deepEqual(parseStoredRequestColumns("not json"), defaults);
  assert.deepEqual(parseStoredRequestColumns(null), defaults);
});

test("requestEntryLabels reads the labels of a list entry", () => {
  assert.deepEqual(requestEntryLabels({ data: { labels: ["team-a", "prod"] } }), ["team-a", "prod"]);
  assert.deepEqual(requestEntryLabels({ data: {} }), []);
  assert.deepEqual(requestEntryLabels({ data: { labels: "x" } }), []);
  assert.deepEqual(requestEntryLabels(null), []);
});
