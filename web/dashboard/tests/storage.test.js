import test from "node:test";
import assert from "node:assert/strict";

import { readStoredRenamed } from "../src/lib/utils/storage.js";

// withStorage runs fn with window.localStorage backed by `initial`, and hands
// it the backing map so a test can see what was written.
function withStorage(initial, fn) {
  const values = new Map(Object.entries(initial));
  const previous = globalThis.window;
  globalThis.window = {
    localStorage: {
      getItem: (key) => (values.has(key) ? values.get(key) : null),
      setItem: (key, value) => values.set(key, String(value)),
    },
  };
  try {
    fn(values);
  } finally {
    if (previous === undefined) delete globalThis.window;
    else globalThis.window = previous;
  }
}

test("a renamed key reads its own value first", () => {
  withStorage({ gomodel_requests_columns: "new", gomodel_audit_columns: "old" }, (values) => {
    assert.equal(readStoredRenamed("gomodel_requests_columns", "gomodel_audit_columns", "{}"), "new");
    assert.equal(values.get("gomodel_audit_columns"), "old");
  });
});

test("a renamed key falls back to the old key once and keeps it", () => {
  withStorage({ gomodel_audit_columns: '{"path":false}' }, (values) => {
    assert.equal(
      readStoredRenamed("gomodel_requests_columns", "gomodel_audit_columns", "{}"),
      '{"path":false}',
    );
    // Copied, so the next read finds it under the new name; the old key stays
    // for a dashboard served by an older gateway.
    assert.equal(values.get("gomodel_requests_columns"), '{"path":false}');
    assert.equal(values.get("gomodel_audit_columns"), '{"path":false}');

    values.set("gomodel_audit_columns", "changed by an older dashboard");
    assert.equal(
      readStoredRenamed("gomodel_requests_columns", "gomodel_audit_columns", "{}"),
      '{"path":false}',
    );
  });
});

test("a renamed key returns the fallback when neither key is stored", () => {
  withStorage({}, (values) => {
    assert.equal(readStoredRenamed("gomodel_requests_columns", "gomodel_audit_columns", "{}"), "{}");
    assert.equal(values.size, 0);
  });
});

test("a renamed key returns the fallback without storage", () => {
  assert.equal(readStoredRenamed("gomodel_requests_columns", "gomodel_audit_columns", "{}"), "{}");
});
