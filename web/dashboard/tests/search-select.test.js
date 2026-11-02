import test from "node:test";
import assert from "node:assert/strict";

import {
  MAX_VISIBLE_OPTIONS,
  capSearchOptions,
  customSearchValue,
  filterSearchOptions,
  moveActiveIndex,
  normalizeSearchOption,
  popoverPlacement,
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

const models = [
  { value: "vllm/deepseek-v4.1-flash:1017", description: "vllm" },
  { value: "openai/gpt-4o", description: "openai" },
  { value: "ollama/qwen2.5:0.5b", description: "ollama" },
];

test("filterSearchOptions reaches a model by subsequence when nothing matches contiguously", () => {
  assert.deepEqual(
    filterSearchOptions(models, "dep1017").map((option) => option.value),
    ["vllm/deepseek-v4.1-flash:1017"],
  );
  assert.deepEqual(
    filterSearchOptions(models, "qwen05b").map((option) => option.value),
    ["ollama/qwen2.5:0.5b"],
  );
});

test("filterSearchOptions ranks a token-starting match above a tighter mid-word one", () => {
  const ranked = filterSearchOptions(
    [
      { value: "openrouter/microsoft/mai-image-2.5" },
      { value: "openai/gpt-image-2.5-flare" },
      { value: "gemini/gemini-2.5-flash" },
    ],
    "ge25",
  );
  // `ge25` is tightest inside "ima|ge25|", then inside "gpt-image25", then
  // across "gemini" and its version. The intended model comes first anyway.
  assert.deepEqual(
    ranked.map((option) => option.value),
    ["gemini/gemini-2.5-flash", "openai/gpt-image-2.5-flare", "openrouter/microsoft/mai-image-2.5"],
  );
});

test("filterSearchOptions ranks fuzzy matches by how tight the match is", () => {
  const ranked = filterSearchOptions(
    [
      { value: "vllm/deepseek-v4.1-flash:1017" },
      { value: "vllm/deepseek-v4.1-flash:1017-replica" },
    ],
    "dseek1017",
  );
  // Both match; the shorter id holds the same characters in a tighter window.
  assert.deepEqual(
    ranked.map((option) => option.value),
    ["vllm/deepseek-v4.1-flash:1017", "vllm/deepseek-v4.1-flash:1017-replica"],
  );
});

test("filterSearchOptions never mixes fuzzy rows into a contiguous match", () => {
  const options = [
    { value: "openai/gpt-4o", description: "openai" },
    { value: "dep-gpt-1017" },
  ];
  // "gpt4o" is a subsequence of "dep-gpt-1017" as well, but "openai/gpt-4o"
  // contains it contiguously, so the fuzzy-only row stays out.
  assert.deepEqual(
    filterSearchOptions(options, "gpt-4o").map((option) => option.value),
    ["openai/gpt-4o"],
  );
});

test("filterSearchOptions leaves descriptions out of the fuzzy level", () => {
  // The description names the provider; a subsequence over it would surface
  // rows nobody was looking for.
  assert.deepEqual(
    filterSearchOptions([{ value: "openai/gpt-4o", description: "provider-1017" }], "pvr1017"),
    [],
  );
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

const rect = (top, height = 32, left = 20, width = 300) => ({
  top,
  bottom: top + height,
  left,
  width,
});

test("popoverPlacement opens below the trigger when there is room", () => {
  assert.equal(
    popoverPlacement(rect(100), { top: 0, height: 800, width: 390 }),
    "top:138px;max-height:654px;left:8px;min-width:300px;max-width:374px;",
  );
});

test("popoverPlacement flips above, anchored by its bottom edge", () => {
  assert.equal(
    popoverPlacement(rect(600), { top: 0, height: 800, width: 1200 }),
    "top:594px;transform:translateY(-100%);max-height:586px;left:20px;min-width:300px;max-width:480px;",
  );
});

test("popoverPlacement fits the visual viewport left above a phone keyboard", () => {
  // Keyboard open: the visible area is the top 400px of the layout viewport,
  // so a trigger at 300 has more room above than below.
  assert.match(
    popoverPlacement(rect(300), { top: 0, height: 400, width: 390 }),
    /^top:294px;transform:translateY\(-100%\);max-height:286px;/,
  );
  // Panned visual viewport: room is measured from its offset, not from 0.
  assert.match(
    popoverPlacement(rect(260), { top: 200, height: 400, width: 390 }),
    /^top:298px;max-height:294px;/,
  );
});

test("popoverPlacement closes once the trigger leaves the visible area", () => {
  assert.equal(popoverPlacement(rect(-40), { top: 0, height: 800, width: 390 }), null);
  assert.equal(popoverPlacement(rect(800), { top: 0, height: 800, width: 390 }), null);
  assert.equal(popoverPlacement(rect(150), { top: 200, height: 400, width: 390 }), null);
  assert.notEqual(popoverPlacement(rect(-20), { top: 0, height: 800, width: 390 }), null);
});

test("capSearchOptions renders one page of matches and keeps the picked value", () => {
  const rows = Array.from({ length: 250 }, (_, index) => ({ value: "m" + index }));
  const page = capSearchOptions(rows, MAX_VISIBLE_OPTIONS);
  assert.equal(page.visible.length, MAX_VISIBLE_OPTIONS);
  assert.equal(page.visible[0].value, "m0");
  assert.equal(page.hidden, 250 - MAX_VISIBLE_OPTIONS);

  // The trigger may already show a value far down the list; opening the list
  // must still show it, or the highlight has nothing to land on.
  const pinned = capSearchOptions(rows, MAX_VISIBLE_OPTIONS, "m200");
  assert.equal(pinned.visible.length, MAX_VISIBLE_OPTIONS + 1);
  assert.equal(pinned.visible[pinned.visible.length - 1].value, "m200");
  // The pinned row is not one of the first page, so the announced cut is
  // unchanged: "the first 100 of 250", not "101 of 250".
  assert.equal(pinned.hidden, 250 - MAX_VISIBLE_OPTIONS);

  // A value already on the page is not appended twice.
  assert.equal(capSearchOptions(rows, MAX_VISIBLE_OPTIONS, "m3").visible.length, MAX_VISIBLE_OPTIONS);
  // Nothing to cut: the same array comes back.
  const small = rows.slice(0, 5);
  assert.equal(capSearchOptions(small, MAX_VISIBLE_OPTIONS).visible, small);
  assert.equal(capSearchOptions(small, MAX_VISIBLE_OPTIONS).hidden, 0);
  assert.deepEqual(capSearchOptions(undefined, MAX_VISIBLE_OPTIONS), { visible: [], hidden: 0 });
});
