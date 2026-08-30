import test from "node:test";
import assert from "node:assert/strict";

import {
  customSearchValue,
  filterSearchOptions,
  moveActiveIndex,
  normalizeSearchOption,
  selectedFirst,
  splitSearchValues,
  toggleSearchValue,
} from "../src/lib/components/molecules/searchSelectLogic.js";
import { marqueeDuration } from "../src/lib/utils/attachments.js";

test("marqueeDuration scales with hidden width but stays readable", () => {
  assert.equal(marqueeDuration(0), 1.5);
  assert.equal(marqueeDuration(60), 1.5);
  assert.equal(marqueeDuration(300), 5);
  assert.equal(marqueeDuration(300, 100), 3);
  assert.equal(marqueeDuration("nope"), 1.5);
});

const options = [
  { value: "openai/gpt-4o", description: "openai" },
  { value: "anthropic/claude-sonnet", label: "Claude Sonnet", description: "anthropic" },
  { value: "ollama/qwen2.5:0.5b", description: "ollama" },
  "plain",
];

test("normalizeSearchOption fills label and description", () => {
  assert.deepEqual(normalizeSearchOption("x"), { value: "x", label: "x", description: "" });
  assert.deepEqual(normalizeSearchOption({ value: 7 }), { value: "7", label: "7", description: "" });
  assert.deepEqual(normalizeSearchOption({ value: "a", label: "A", description: "d" }), {
    value: "a",
    label: "A",
    description: "d",
  });
  assert.equal(normalizeSearchOption(null), null);
});

test("filterSearchOptions matches value, label and description, prefix first", () => {
  assert.equal(filterSearchOptions(options, "").length, 4);
  assert.deepEqual(
    filterSearchOptions(options, "claude").map((o) => o.value),
    ["anthropic/claude-sonnet"],
  );
  assert.deepEqual(
    filterSearchOptions(options, "OLLAMA").map((o) => o.value),
    ["ollama/qwen2.5:0.5b"],
  );
  // "o" prefixes two options and only appears inside "anthropic/…", so the
  // prefix tier must come first.
  assert.deepEqual(
    filterSearchOptions(options, "o").map((o) => o.value),
    ["openai/gpt-4o", "ollama/qwen2.5:0.5b", "anthropic/claude-sonnet"],
  );
  assert.deepEqual(filterSearchOptions(options, "zzz"), []);
  assert.deepEqual(filterSearchOptions(undefined, "x"), []);
});

test("filterSearchOptions drops options that repeat a value", () => {
  const duplicated = [
    { value: "gpt-4o", description: "openai" },
    { value: "gpt-4o", description: "azure" },
    "gpt-4o",
  ];
  assert.deepEqual(filterSearchOptions(duplicated, ""), [{ value: "gpt-4o", label: "gpt-4o", description: "openai" }]);
  assert.equal(filterSearchOptions(duplicated, "gpt").length, 1);
});

test("customSearchValue offers typed text only when allowed and new", () => {
  assert.equal(customSearchValue(options, " my-alias ", true), "my-alias");
  assert.equal(customSearchValue(options, "plain", true), "");
  assert.equal(customSearchValue(options, "my-alias", false), "");
  assert.equal(customSearchValue(options, "   ", true), "");
});

test("moveActiveIndex wraps around and enters from either end", () => {
  assert.equal(moveActiveIndex(-1, 1, 3), 0);
  assert.equal(moveActiveIndex(-1, -1, 3), 2);
  assert.equal(moveActiveIndex(2, 1, 3), 0);
  assert.equal(moveActiveIndex(0, -1, 3), 2);
  assert.equal(moveActiveIndex(1, 1, 3), 2);
  assert.equal(moveActiveIndex(0, 1, 0), -1);
});

test("splitSearchValues splits a typed multi-select entry on commas and newlines", () => {
  assert.deepEqual(splitSearchValues(" anthropic/, openai/gpt-5\nanthropic/ "), ["anthropic/", "openai/gpt-5"]);
  assert.deepEqual(splitSearchValues(""), []);
});

test("toggleSearchValue appends absent values and removes present ones without mutating", () => {
  const values = ["a"];
  assert.deepEqual(toggleSearchValue(values, "b"), ["a", "b"]);
  assert.deepEqual(toggleSearchValue(values, "a"), []);
  assert.deepEqual(toggleSearchValue(values, ""), ["a"]);
  assert.deepEqual(values, ["a"]);
});

test("selectedFirst lists selected options first, sorted by label, and keeps the rest in order", () => {
  const rows = filterSearchOptions(options, "");
  const ordered = selectedFirst(rows, ["plain", "openai/gpt-4o"]);
  assert.deepEqual(
    ordered.map((option) => option.value),
    ["openai/gpt-4o", "plain", "anthropic/claude-sonnet", "ollama/qwen2.5:0.5b"],
  );
  assert.deepEqual(selectedFirst(rows, []).map((o) => o.value), rows.map((o) => o.value));
});
