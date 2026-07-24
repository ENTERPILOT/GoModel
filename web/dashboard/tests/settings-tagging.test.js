// Pure-logic tests for the tagging-settings editor (the legacy module had no
// tagging.test.cjs; these cover the ported normalize/payload helpers).
import test from "node:test";
import assert from "node:assert/strict";

import {
  defaultTaggingHeader,
  normalizeTaggingHeaders,
  taggingSettingsPayload,
  taggingErrorMessage,
} from "../src/pages/settings/tagging-logic.js";

test("normalizeTaggingHeaders fills defaults and normalizes the default delimiter", () => {
  const rows = normalizeTaggingHeaders({
    headers: [
      { header: "X-My-Tags", prefix: "tag-", do_not_pass: true, delimiter: ";" },
      { header: "X-Team", delimiter: ",", managed: true },
      {},
    ],
  });

  assert.deepEqual(rows, [
    {
      header: "X-My-Tags",
      prefix: "tag-",
      do_not_pass: true,
      delimiter: ";",
      managed: false,
    },
    { header: "X-Team", prefix: "", do_not_pass: false, delimiter: "", managed: true },
    { header: "", prefix: "", do_not_pass: false, delimiter: "", managed: false },
  ]);
});

test("normalizeTaggingHeaders tolerates malformed payloads", () => {
  assert.deepEqual(normalizeTaggingHeaders(null), []);
  assert.deepEqual(normalizeTaggingHeaders({}), []);
  assert.deepEqual(normalizeTaggingHeaders({ headers: "nope" }), []);
});

test("taggingSettingsPayload drops managed and empty rows and trims headers", () => {
  const payload = taggingSettingsPayload([
    { ...defaultTaggingHeader(), header: "  X-My-Tags  ", prefix: "tag-" },
    { ...defaultTaggingHeader(), header: "X-Config", managed: true },
    { ...defaultTaggingHeader(), header: "   " },
    {
      ...defaultTaggingHeader(),
      header: "X-Other",
      do_not_pass: true,
      delimiter: "|",
    },
  ]);

  assert.deepEqual(payload, {
    headers: [
      { header: "X-My-Tags", prefix: "tag-", do_not_pass: false, delimiter: "" },
      { header: "X-Other", prefix: "", do_not_pass: true, delimiter: "|" },
    ],
  });
});

test("taggingErrorMessage extracts the server rejection message", () => {
  assert.equal(
    taggingErrorMessage({
      error: { message: "header Authorization cannot be used for tagging" },
    }),
    "header Authorization cannot be used for tagging",
  );
  assert.equal(taggingErrorMessage({ error: {} }), "");
  assert.equal(taggingErrorMessage(null), "");
});
