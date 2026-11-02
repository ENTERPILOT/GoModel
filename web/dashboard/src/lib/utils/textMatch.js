// Free-text matching shared by every dashboard search and filter. Pure, no
// Svelte runtime and relative imports only, so tests import it directly.
//
// Two levels, in a fixed order:
//
//   1. substring — the text contains the query contiguously, case-insensitive.
//      This is what every filter did before, and it keeps its exact results.
//   2. fuzzy — the query's characters appear in order with gaps allowed, and
//      separators are ignored on both sides. `dep1017` reaches
//      `vllm/deepseek-v4.1-flash:1017`, `gpt51` reaches `openai/gpt-5.1`.
//
// The fuzzy level only runs when the substring level found nothing, so a query
// that already works keeps returning the same rows in the same order. Below
// MIN_FUZZY_LENGTH compacted characters it does not run at all: a two-letter
// subsequence matches most of a catalog and the list stops being a filter.
//
// Every match is reported as ranges in the *original* string, so the caller can
// mark what it matched. `toLowerCase()` can change the length of a string
// ("İ" folds to two code points), and an index taken on the folded copy would
// then point at the wrong character — or past the end. `fold()` keeps the
// mapping, and both levels resolve their ranges through it.

export const MIN_FUZZY_LENGTH = 3;


// Separators are dropped before the fuzzy comparison only. Model ids are full
// of them (`vllm/deepseek-v4.1-flash:1017`), and typing a model name without
// them is the common case.
const SEPARATOR = /[-._:/\s]/;

/**
 * Trim and fold a user's query the way `fold` folds the searched text, so a
 * query copied out of the data finds that data: `İstanbul` pasted in full
 * matches `İstanbul` (a plain `toLowerCase` would leave a combining dot the
 * text never has). Empty input matches everything.
 */
export function normalizeQuery(query) {
  return fold(String(query ?? "").trim()).chars.join("");
}

/**
 * Lowercase a string while remembering where each folded character came from.
 *
 * Returns `chars` (one folded code point per original code point) plus the
 * `start` and `end` of that code point in the original string. Folding is
 * capped at one code point per input code point on purpose: a mapping that can
 * grow cannot be mapped back, which is the whole point of this function.
 *
 * The common case — folding preserves the length — skips the per-code-point
 * walk, because every index is already its own answer.
 */
function fold(text) {
  const source = String(text ?? "");
  const chars = [];
  const starts = [];
  const ends = [];
  for (let i = 0; i < source.length; ) {
    const code = source.codePointAt(i);
    const char = String.fromCodePoint(code);
    const end = i + char.length;
    const folded = char.toLowerCase();
    chars.push(String.fromCodePoint(folded.codePointAt(0)));
    starts.push(i);
    ends.push(end);
    i = end;
  }
  return { chars, starts, ends, source };
}

// Characters whose lowercase depends on the rest of the string: Greek sigma
// takes its final form at the end of a word, Turkish dotted/dotless i depends
// on the locale. `toLowerCase()` on the whole string applies that context, the
// per-code-point fold does not, so a string holding one of these must go
// through the fold — otherwise the two sides are folded by different rules and
// `ΟΣ` stops matching `ΟΣ`.
const CONTEXT_SENSITIVE = /[\u0130\u0131\u03a3\u03c2]/;

/**
 * Whether a native case-insensitive search can answer for this string. It can
 * when folding leaves every code point in place and the lowercase does not
 * depend on the position of the character.
 */
function foldKeepsLength(text) {
  const source = String(text ?? "");
  return source.toLowerCase().length === source.length && !CONTEXT_SENSITIVE.test(source);
}

/** Index of `needle` (an array of code points) inside `haystack`, or -1. */
function indexOfChars(haystack, needle) {
  if (!needle.length || needle.length > haystack.length) return -1;
  outer: for (let i = 0; i + needle.length <= haystack.length; i++) {
    for (let j = 0; j < needle.length; j++) {
      if (haystack[i + j] !== needle[j]) continue outer;
    }
    return i;
  }
  return -1;
}

/**
 * Range of the first contiguous, case-insensitive occurrence of `needle` in
 * `text`, as code-unit indexes into `text`, or null. Folding usually preserves
 * the length, and then the native search already answers in code units; only
 * an expanding fold ("İ") needs the mapping.
 */
export function substringRange(text, needle) {
  if (!needle) return null;
  const source = String(text ?? "");
  const wanted = String(needle);
  if (foldKeepsLength(source) && foldKeepsLength(wanted)) {
    const lower = source.toLowerCase();
    const want = wanted.toLowerCase();
    const start = lower.indexOf(want);
    return start < 0 ? null : { start, end: start + want.length };
  }
  // A fold that adds or drops code points ("İ" folds to two) means neither
  // side can be searched with native indexes; compare the folded characters
  // and report the range through the mapping. Both sides fold the same way, so
  // a query pasted from the data still finds the data.
  const hay = fold(source);
  const chars = fold(wanted).chars;
  const at = indexOfChars(hay.chars, chars);
  if (at < 0) return null;
  return { start: hay.starts[at], end: hay.ends[at + chars.length - 1] };
}

/**
 * Where the first contiguous, case-insensitive occurrence of `needle` starts,
 * as a code-unit index into `text`, or -1. A start of 0 means the text begins
 * with the needle, which is what the prefix tier reads.
 */
export function substringIndex(text, needle) {
  const range = substringRange(text, needle);
  return range ? range.start : -1;
}

/**
 * Compact a string for subsequence matching: lowercase, separators removed,
 * plus where each surviving character sits in the original string, so a match
 * can be mapped back for highlighting.
 */
function compact(text) {
  const folded = fold(text);
  const chars = [];
  const starts = [];
  const ends = [];
  for (let i = 0; i < folded.chars.length; i++) {
    const char = folded.chars[i];
    if (SEPARATOR.test(char)) continue;
    chars.push(char);
    starts.push(folded.starts[i]);
    ends.push(folded.ends[i]);
  }
  return { chars, starts, ends, source: folded.source };
}

/**
 * Whether a position in the original string starts a token: the first
 * character, or the one right after a separator. A match that starts there is
 * what the user usually meant — `ge25` should prefer `gemini/gemini-2.5-flash`
 * over `openai/gpt-image-2.5-flare`, where the tighter run sits mid-word
 * (`ima|ge25|`).
 */
function startsToken(text, index) {
  return index === 0 || SEPARATOR.test(text[index - 1]);
}

/**
 * Tightest window that contains `needle` as a subsequence *starting exactly
 * at* `from`. Forward-greedy from a fixed start is already the tightest
 * window with that start, so no backward pass is needed. Returns null when the
 * needle does not match from there.
 */
function windowAt(haystack, needle, from) {
  let j = 0;
  let i = from;
  for (; i < haystack.length && j < needle.length; i++) {
    if (haystack[i] === needle[j]) j++;
  }
  if (j < needle.length) return null;
  return [from, i];
}

/**
 * Shortest window of `haystack` that contains `needle` as a subsequence, as
 * [start, end) indexes into haystack. Greedy left-to-right matching is not
 * enough: it finds *a* match, not the tightest one, and the tightest one is
 * what tells `deepseek` apart from characters scattered across a long id.
 * Returns null when there is no match.
 */
function shortestWindow(haystack, needle) {
  let best = null;
  let start = 0;
  while (start <= haystack.length - needle.length) {
    const window = windowAt(haystack, needle, start);
    if (!window) break;
    const end = window[1];
    // Walk back from the end to find the latest start that still matches, so
    // the window shrinks to the shortest one ending here.
    let k = end - 1;
    let back = needle.length - 1;
    let windowStart = start;
    for (; k >= start; k--) {
      if (haystack[k] === needle[back]) {
        back--;
        if (back < 0) {
          windowStart = k;
          break;
        }
      }
    }
    if (!best || end - windowStart < best[1] - best[0]) best = [windowStart, end];
    start = windowStart + 1;
  }
  return best;
}

/**
 * Tightest window among those that start a token in the original string, or
 * null when the needle only matches mid-word.
 */
function shortestTokenWindow(hay, wanted) {
  let best = null;
  for (let i = 0; i < hay.chars.length; i++) {
    if (hay.chars[i] !== wanted[0]) continue;
    if (!startsToken(hay.source, hay.starts[i])) continue;
    const window = windowAt(hay.chars, wanted, i);
    // A match that fails from one start fails from every later one: the rest of
    // the field is a shorter suffix, so it cannot hold the needle either. The
    // failing path therefore costs one scan instead of one per token start.
    if (!window) break;
    if (!best || window[1] - window[0] < best[1] - best[0]) best = window;
    if (best[1] - best[0] === wanted.length) break; // cannot be tighter
  }
  return best;
}

/**
 * Which characters of `window` matched, preferring a contiguous run at the
 * window's end: that is what keeps `1017` highlighted as one run instead of
 * `1` in the middle and `017` at the end. Returns null when the anchored
 * suffix leaves a prefix that cannot be matched before it.
 */
function matchPositions(haystack, wanted, window) {
  const [windowStart, windowEnd] = window;
  let suffix = 0;
  while (
    suffix < wanted.length &&
    haystack[windowEnd - 1 - suffix] === wanted[wanted.length - 1 - suffix]
  ) {
    suffix++;
  }
  const prefixCount = wanted.length - suffix;
  const prefix = [];
  if (prefixCount > 0) {
    let at = windowStart;
    const prefixEnd = windowEnd - suffix; // exclusive
    for (let j = 0; j < prefixCount; j++) {
      while (at < prefixEnd && haystack[at] !== wanted[j]) at++;
      if (at >= prefixEnd) break;
      prefix.push(at);
      at++;
    }
    if (prefix.length !== prefixCount) return null;
  }
  const positions = prefix;
  for (let k = 0; k < suffix; k++) positions.push(windowEnd - suffix + k);
  return positions;
}

/**
 * Fuzzy match of a normalized `needle` against `text`, or null.
 *
 * Returns the matched ranges in `text` (for highlighting) and the window that
 * won: its span, whether it starts a token, and where it starts. Callers rank
 * with those — a token-starting match outranks a tighter mid-word one.
 *
 * @returns {{ranges: Array<{start: number, end: number}>, span: number,
 *            boundary: boolean, start: number}|null}
 */
export function fuzzyMatch(text, needle) {
  const wanted = compact(needle).chars;
  if (wanted.length < MIN_FUZZY_LENGTH) return null;
  const hay = compact(text);
  if (hay.chars.length < wanted.length) return null;

  // The token-starting window is the one that ranks, so it is found first and
  // the general search only runs when there is none.
  const tokenWindow = shortestTokenWindow(hay, wanted);
  const window = tokenWindow || shortestWindow(hay.chars, wanted);
  if (!window) return null;

  const positions = matchPositions(hay.chars, wanted, window);
  if (!positions) return null;

  const ranges = [];
  for (const index of positions) {
    const start = hay.starts[index];
    const end = hay.ends[index];
    const last = ranges[ranges.length - 1];
    if (last && last.end === start) last.end = end;
    else ranges.push({ start, end });
  }
  return {
    ranges,
    span: window[1] - window[0],
    boundary: Boolean(tokenWindow),
    start: hay.starts[window[0]],
  };
}

/**
 * Whether `text` matches a normalized `needle` at either level. With an empty
 * needle everything matches, so callers can skip the empty-query branch.
 */
export function textMatches(text, needle) {
  if (!needle) return true;
  if (substringIndex(text, needle) >= 0) return true;
  return fuzzyMatch(text, needle) !== null;
}

/**
 * Whether any of `fields` matches a normalized `needle` at either level. Use
 * it where the caller filters by a per-item predicate instead of splitting a
 * list (an inactive-ancestor rule, a tree prune).
 */
export function fieldsMatch(fields, needle) {
  const list = Array.isArray(fields) ? fields : [];
  return list.some((field) => textMatches(field, needle));
}

/**
 * Split `items` into the ones the query matches by substring and the ones it
 * only matches fuzzily. Both keep the input order. Callers that filter a list
 * use `filterFields`, which picks one bucket; callers that rank suggestions
 * use this to concatenate them.
 *
 * `fieldsOf(item)` returns the searchable text of one item, one entry per
 * field — not a joined string, or a subsequence could match across the
 * boundary between two fields that share no such text.
 */
export function partitionMatches(items, needle, fieldsOf, substringOnlyFieldsOf = null) {
  const list = Array.isArray(items) ? items : [];
  const query = normalizeQuery(needle);
  const primary = [];
  const fuzzy = [];
  if (!query) return { primary: list.slice(), fuzzy };
  // Filters used to search one space-joined haystack, so a query with a space
  // could start in one field and end in the next ("production team" across a
  // name and a description). That still has to match; a subsequence across the
  // boundary would not, so the join is only read by the substring level, and
  // only when the query can actually span it.
  const maySpanFields = query.includes(" ");
  for (const item of list) {
    const fields = (fieldsOf(item) || []).map((field) => String(field ?? "")).filter(Boolean);
    if (fields.some((field) => substringIndex(field, query) >= 0)) {
      primary.push(item);
      continue;
    }
    if (maySpanFields && substringIndex(fields.join(" "), query) >= 0) {
      primary.push(item);
      continue;
    }
    // Fields the caller keeps for the substring level only — a haystack joined
    // with a separator of its own (a comma-joined tag list, say). They must not
    // reach the fuzzy level, or a subsequence would use characters from two
    // entries and match text that appears nowhere.
    if (
      substringOnlyFieldsOf &&
      (substringOnlyFieldsOf(item) || []).some((field) => substringIndex(field, query) >= 0)
    ) {
      primary.push(item);
      continue;
    }
    if (fields.some((field) => fuzzyMatch(field, query) !== null)) fuzzy.push(item);
  }
  return { primary, fuzzy };
}

/**
 * Filter `items` by a free-text query: the substring matches when there are
 * any, the fuzzy ones only when there are none. This is the rule every filter
 * in the dashboard follows, kept in one place so they cannot drift.
 */
export function filterFields(items, needle, fieldsOf, substringOnlyFieldsOf = null) {
  const { primary, fuzzy } = partitionMatches(items, needle, fieldsOf, substringOnlyFieldsOf);
  return primary.length ? primary : fuzzy;
}

/**
 * Split `text` into runs for rendering a match: contiguous hits flagged, the
 * gaps between them not. Follows the same two levels as the filters, so a row
 * that appears is always a row with at least one hit run. Returns the whole
 * text as a single unflagged run when nothing matches, which keeps the caller
 * free of a special case.
 *
 * @returns {Array<{text: string, hit: boolean}>}
 */
export function matchSegments(text, needle) {
  const value = String(text ?? "");
  const query = normalizeQuery(needle);
  const whole = value ? [{ text: value, hit: false }] : [];
  if (!query || !value) return whole;

  const range = substringRange(value, query);
  if (range) {
    const segments = [];
    if (range.start > 0) segments.push({ text: value.slice(0, range.start), hit: false });
    segments.push({ text: value.slice(range.start, range.end), hit: true });
    if (range.end < value.length) segments.push({ text: value.slice(range.end), hit: false });
    return segments;
  }

  const match = fuzzyMatch(value, query);
  if (!match) return whole;
  const segments = [];
  let at = 0;
  for (const hit of match.ranges) {
    if (hit.start > at) segments.push({ text: value.slice(at, hit.start), hit: false });
    segments.push({ text: value.slice(hit.start, hit.end), hit: true });
    at = hit.end;
  }
  if (at < value.length) segments.push({ text: value.slice(at), hit: false });
  return segments;
}
