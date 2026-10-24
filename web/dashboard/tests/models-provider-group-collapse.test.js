import { test } from "node:test";
import assert from "node:assert/strict";
import {
  parseCollapsedGroups,
  serializeCollapsedGroups,
  toggleCollapsedGroup,
} from "../src/pages/models/providerGroupCollapse.js";

test("parseCollapsedGroups keeps unique non-empty string keys", () => {
  const cases = [
    { name: "missing", raw: null, want: [] },
    { name: "empty", raw: "", want: [] },
    { name: "invalid JSON", raw: "{not json", want: [] },
    { name: "not an array", raw: '{"a":true}', want: [] },
    {
      name: "mixed entries",
      raw: '["provider-group:openai", 3, "", null, "provider-group:openai", "virtual-model-group"]',
      want: ["provider-group:openai", "virtual-model-group"],
    },
  ];
  for (const tc of cases) {
    assert.deepEqual(parseCollapsedGroups(tc.raw), tc.want, tc.name);
  }
});

test("toggleCollapsedGroup adds and removes a key without mutating input", () => {
  const start = ["provider-group:openai"];
  const added = toggleCollapsedGroup(start, "provider-group:anthropic");
  assert.deepEqual(added, ["provider-group:openai", "provider-group:anthropic"]);
  assert.deepEqual(start, ["provider-group:openai"]);

  const removed = toggleCollapsedGroup(added, "provider-group:openai");
  assert.deepEqual(removed, ["provider-group:anthropic"]);

  assert.deepEqual(toggleCollapsedGroup(start, ""), start);
  assert.deepEqual(toggleCollapsedGroup(undefined, "k"), ["k"]);
});

test("collapsed groups round-trip through storage serialization", () => {
  const keys = ["provider-group:openai", "virtual-model-group"];
  assert.deepEqual(parseCollapsedGroups(serializeCollapsedGroups(keys)), keys);
  assert.equal(serializeCollapsedGroups(null), "[]");
});
