import test from "node:test";
import assert from "node:assert/strict";

import {
  MIN_FUZZY_LENGTH,
  fieldsMatch,
  filterFields,
  fuzzyMatch,
  matchSegments,
  normalizeQuery,
  partitionMatches,
  substringIndex,
  textMatches,
} from "../src/lib/utils/textMatch.js";

const hits = (text, needle) =>
  matchSegments(text, needle)
    .filter((segment) => segment.hit)
    .map((segment) => segment.text);

test("normalizeQuery trims and lowercases, and treats missing input as empty", () => {
  assert.equal(normalizeQuery("  GPT-4O "), "gpt-4o");
  assert.equal(normalizeQuery(null), "");
  assert.equal(normalizeQuery(undefined), "");
});

test("substringIndex is case-insensitive and reports -1 when absent", () => {
  assert.equal(substringIndex("OpenAI/GPT-4o", "gpt"), 7);
  assert.equal(substringIndex("OpenAI/GPT-4o", "zzz"), -1);
  assert.equal(substringIndex("anything", ""), -1);
});

test("fuzzyMatch reaches a model id from characters spread across it", () => {
  // The case that started this: a port suffix plus the first letters of the
  // model name, none of them contiguous.
  const id = "vllm/deepseek-v4.1-flash:1017";
  assert.deepEqual(hits(id, "dep1017"), ["de", "p", "1017"]);
  assert.ok(fuzzyMatch(id, "dep1017"));
});

test("fuzzyMatch ignores separators on both sides", () => {
  assert.deepEqual(hits("openai/gpt-5.1", "gpt51"), ["gpt", "5", "1"]);
  // The dots and colon between "2", "5" and "0" are skipped, but the original
  // separators stay in the highlighted runs: the match is "0" and "5b".
  assert.deepEqual(hits("ollama/qwen2.5:0.5b", "qwen05b"), ["qwen", "0", "5b"]);
  assert.deepEqual(hits("anthropic/claude-sonnet", "claudesonnet"), ["claude", "sonnet"]);
});

test("fuzzyMatch absorbs an omitted or doubled letter", () => {
  // "sonet" is "sonnet" with one n missing: a subsequence, not a substring.
  assert.deepEqual(hits("anthropic/claude-sonnet", "sonet"), ["so", "net"]);
});

test("fuzzyMatch returns null below the minimum length and without a match", () => {
  assert.equal(fuzzyMatch("deepseek", "de"), null);
  assert.equal(fuzzyMatch("deepseek", "zzz"), null);
  assert.equal(fuzzyMatch("ab", "abc"), null);
  assert.equal(MIN_FUZZY_LENGTH, 3);
  // At exactly the minimum length the level runs; "dep" is a subsequence of
  // "deepseek" (d, e, then the p).
  assert.ok(fuzzyMatch("deepseek", "dep"));
});

test("fuzzyMatch reports the winning window: span, token start and position", () => {
  const compact = fuzzyMatch("openai/gpt-4o-mini", "gpt4");
  const scattered = fuzzyMatch("gemini-pro-tier-4", "gpt4");
  assert.ok(compact && scattered);
  assert.ok(compact.span < scattered.span);
  assert.equal(compact.boundary, true, "gpt starts a segment");
  assert.equal(compact.start, 7);
});

test("a match that starts a token beats a tighter one buried mid-word", () => {
  // `ge25` inside "ima|ge25|" is the tightest run of the three, and the least
  // likely thing the user meant. It must lose to the one starting "gemini".
  const tokenStart = fuzzyMatch("gemini/gemini-2.5-flash", "ge25");
  const midWord = fuzzyMatch("openrouter/microsoft/mai-image-2.5", "ge25");
  assert.ok(tokenStart && midWord);
  assert.equal(tokenStart.boundary, true);
  assert.equal(midWord.boundary, false);
  assert.ok(midWord.span < tokenStart.span, "the mid-word run is the tighter one");
});

test("textMatches keeps the substring level first and falls back to fuzzy", () => {
  assert.equal(textMatches("anything", ""), true);
  assert.equal(textMatches("openai/gpt-4o", "gpt"), true);
  assert.equal(textMatches("vllm/deepseek-v4.1-flash:1017", "dep1017"), true);
  assert.equal(textMatches("openai/gpt-4o", "zzz"), false);
});

test("fieldsMatch never lets a fuzzy match span two fields", () => {
  assert.equal(fieldsMatch(["vllm/deepseek", "flash:1017"], "dep1017"), false);
  assert.equal(fieldsMatch(["vllm/deepseek-v4.1-flash:1017"], "dep1017"), true);
  assert.equal(fieldsMatch([], "dep1017"), false);
});

test("partitionMatches splits the two levels and keeps input order", () => {
  const items = [{ text: "openai/gpt-4o" }, { text: "vllm/deepseek-v4.1-flash:1017" }, { text: "gpt-4o-mini" }];
  const { primary, fuzzy } = partitionMatches(items, "gpt-4o", (item) => [item.text]);
  assert.deepEqual(
    primary.map((item) => item.text),
    ["openai/gpt-4o", "gpt-4o-mini"],
  );
  assert.deepEqual(fuzzy, []);

  const fallback = partitionMatches(items, "dep1017", (item) => [item.text]);
  assert.deepEqual(fallback.primary, []);
  assert.deepEqual(
    fallback.fuzzy.map((item) => item.text),
    ["vllm/deepseek-v4.1-flash:1017"],
  );
});

test("filterFields only widens the result when the substring level finds nothing", () => {
  const items = [{ text: "deep-f1" }, { text: "dep1017" }, { text: "zzz" }];
  // "dep" is a substring of "dep1017", so the fuzzy-only matches stay out even
  // though other rows are dropped: the rule is list-level, not per row.
  assert.deepEqual(
    filterFields(items, "dep", (item) => [item.text]).map((item) => item.text),
    ["dep1017"],
  );
  assert.deepEqual(
    filterFields(items, "dp17", (item) => [item.text]).map((item) => item.text),
    ["dep1017"],
  );
});

test("filterFields returns every item for an empty query and nothing for a miss", () => {
  const items = [{ text: "a" }, { text: "b" }];
  assert.deepEqual(
    filterFields(items, "  ", (item) => [item.text]).map((item) => item.text),
    ["a", "b"],
  );
  assert.deepEqual(filterFields(items, "zzzz", (item) => [item.text]), []);
  assert.deepEqual(filterFields(undefined, "a", () => []), []);
});

test("matchSegments flags the contiguous hit and leaves the rest whole", () => {
  assert.deepEqual(matchSegments("anthropic/claude-sonnet", "claude"), [
    { text: "anthropic/", hit: false },
    { text: "claude", hit: true },
    { text: "-sonnet", hit: false },
  ]);
  assert.deepEqual(matchSegments("claude", "claude"), [{ text: "claude", hit: true }]);
});

test("matchSegments falls back to the fuzzy ranges", () => {
  assert.deepEqual(hits("vllm/deepseek-v4.1-flash:1017", "dep1017"), ["de", "p", "1017"]);
});

test("matchSegments leaves unmatched text as a single unflagged run", () => {
  assert.deepEqual(matchSegments("openai/gpt-4o", "zzz"), [{ text: "openai/gpt-4o", hit: false }]);
  assert.deepEqual(matchSegments("openai/gpt-4o", ""), [{ text: "openai/gpt-4o", hit: false }]);
  assert.deepEqual(matchSegments("", "gpt"), []);
});
