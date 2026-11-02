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
  substringRange,
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
  // One text holding both candidates: `ge`+`25` sits tight inside
  // "ima|ge25|" and looser across "gemini" and its version. A matcher that
  // always took the tightest window would report the mid-word one.
  const text = "gemini-2.5-image25";
  const match = fuzzyMatch(text, "ge25");
  assert.ok(match);
  assert.equal(match.boundary, true);
  assert.ok(text[match.start] === "g", "the winning window starts the model name");

  // The same two candidates considered separately: the mid-word run is the
  // tighter one, so tightness alone would have preferred it.
  const tokenStart = fuzzyMatch("gemini/gemini-2.5-flash", "ge25");
  const midWord = fuzzyMatch("openrouter/microsoft/mai-image-2.5", "ge25");
  assert.ok(tokenStart && midWord);
  assert.equal(tokenStart.boundary, true);
  assert.equal(midWord.boundary, false);
  assert.ok(midWord.span < tokenStart.span);
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

test("folding that changes the length does not shift the reported ranges", () => {
  // "İ".toLowerCase() is two code points, so an index taken on the folded copy
  // would point at the wrong character, or past the end of the string.
  const text = "İABC";
  assert.equal(text.length, 4);
  const range = substringRange(text, "abc");
  assert.deepEqual(range, { start: 1, end: 4 });
  assert.equal(text.slice(range.start, range.end), "ABC");

  const match = fuzzyMatch(text, "abc");
  assert.equal(match.ranges.length, 1);
  assert.deepEqual(match.ranges[0], { start: 1, end: 4 });
  assert.deepEqual(hits(text, "abc"), ["ABC"]);
});

test("a surrogate pair is one character but two code units", () => {
  const text = "🙂abc";
  const range = substringRange(text, "abc");
  assert.deepEqual(range, { start: 2, end: 5 });
  assert.equal(text.slice(range.start, range.end), "abc");
  assert.deepEqual(matchSegments(text, "abc"), [
    { text: "🙂", hit: false },
    { text: "abc", hit: true },
  ]);
});

test("substringIndex reports a character start, so 0 still means a prefix", () => {
  assert.equal(substringIndex("İstanbul", "stan"), 1);
  assert.equal(substringIndex("İstanbul", "İ"), 0);
  assert.equal(substringIndex("anything", "missing"), -1);
});

test("a query with a space still matches across two fields", () => {
  // Filters used to search one space-joined haystack; splitting the fields
  // must not drop those rows.
  const rows = [{ name: "Production", description: "team key" }, { name: "Production", description: "other" }];
  assert.deepEqual(
    filterFields(rows, "production team", (row) => [row.name, row.description]).map((row) => row.description),
    ["team key"],
  );
  // The join is substring-only: a subsequence must not be assembled across
  // the boundary, which is the reason the fields were split in the first place.
  assert.deepEqual(filterFields([{ a: "Production", b: "team" }], "prducionteam", (row) => [row.a, row.b]), []);
});

test("a query pasted from the data finds that data even when folding expands", () => {
  // "İ".toLowerCase() is two code points, so a plain lowercase query carried a
  // combining dot the text never had: searching the field's own text missed it.
  const query = normalizeQuery("İstanbul");
  assert.equal(query, "istanbul");
  assert.deepEqual(substringRange("İstanbul", query), { start: 0, end: 8 });
  assert.deepEqual(matchSegments("İstanbul", query), [{ text: "İstanbul", hit: true }]);
});

test("an expanding fold and an astral character do not shift the range", () => {
  // Five code points, six code units: "İ" folds to two, the emoji is one code
  // point made of two units. Indexes counted on the folded string pointed at
  // the wrong character, or past the end.
  const text = "İ🙂ABC";
  assert.deepEqual(substringRange(text, "abc"), { start: 3, end: 6 });
  assert.equal(text.slice(3, 6), "ABC");
  const match = fuzzyMatch(text, "abc");
  assert.ok(match);
  assert.ok(match.ranges.every((range) => Number.isInteger(range.start) && Number.isInteger(range.end)));
  assert.equal(text.slice(match.ranges[0].start, match.ranges[0].end), "ABC");
});
