import { test } from "node:test";
import assert from "node:assert/strict";
import {
  allGroupsCollapsed,
  parseCollapsedGroups,
  serializeCollapsedGroups,
  setGroupsCollapsed,
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

test("setGroupsCollapsed collapses or expands every given group", () => {
  const start = ["provider-group:openai", "provider-group:other"];
  const groups = ["provider-group:openai", "provider-group:anthropic", ""];
  const cases = [
    {
      name: "collapse all keeps unrelated keys without duplicates",
      collapsed: true,
      want: ["provider-group:openai", "provider-group:other", "provider-group:anthropic"],
    },
    { name: "expand all removes only the given groups", collapsed: false, want: ["provider-group:other"] },
  ];
  for (const tc of cases) {
    assert.deepEqual(setGroupsCollapsed(start, groups, tc.collapsed), tc.want, tc.name);
  }
  assert.deepEqual(start, ["provider-group:openai", "provider-group:other"]);
  assert.deepEqual(setGroupsCollapsed(undefined, undefined, true), []);
});

test("allGroupsCollapsed needs at least one group, all of them collapsed", () => {
  const cases = [
    { name: "no groups", keys: ["a"], groups: [], want: false },
    { name: "some collapsed", keys: ["a"], groups: ["a", "b"], want: false },
    { name: "all collapsed", keys: ["a", "b", "c"], groups: ["a", "b"], want: true },
    { name: "missing keys", keys: undefined, groups: ["a"], want: false },
  ];
  for (const tc of cases) {
    assert.equal(allGroupsCollapsed(tc.keys, tc.groups), tc.want, tc.name);
  }
});
