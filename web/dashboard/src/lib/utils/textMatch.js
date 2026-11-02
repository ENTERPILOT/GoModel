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

export const MIN_FUZZY_LENGTH = 3;

// Separators are dropped before the fuzzy comparison only. Model ids are full
// of them (`vllm/deepseek-v4.1-flash:1017`), and typing a model name without
// them is the common case.
const SEPARATOR = /[-._:/\s]/;

/** Trim and lowercase a user's query; empty input matches everything. */
export function normalizeQuery(query) {
  return String(query ?? "").trim().toLowerCase();
}

/**
 * Index of the first contiguous, case-insensitive occurrence of `needle` in
 * `text`, or -1. `needle` is expected to be normalized already.
 */
export function substringIndex(text, needle) {
  if (!needle) return -1;
  return String(text ?? "").toLowerCase().indexOf(needle);
}

/**
 * Compact a string for subsequence matching: lowercase, separators removed,
 * plus the index in the original string of each surviving character, so a
 * match can be mapped back for highlighting.
 */
function compact(text) {
  const source = String(text ?? "");
  const lower = source.toLowerCase();
  let chars = "";
  const indexes = [];
  for (let i = 0; i < lower.length; i++) {
    const char = lower[i];
    if (SEPARATOR.test(char)) continue;
    chars += char;
    indexes.push(i);
  }
  return { chars, indexes, text: source };
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
    let j = 0;
    let i = start;
    for (; i < haystack.length && j < needle.length; i++) {
      if (haystack[i] === needle[j]) j++;
    }
    if (j < needle.length) break;
    const end = i; // exclusive
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
 * window with that start, so no backward pass is needed.
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
 * Tightest window among those that start a token in the original string, or
 * null when the needle only matches mid-word.
 */
function shortestTokenWindow(hay, wanted) {
  let best = null;
  for (let i = 0; i < hay.chars.length; i++) {
    if (hay.chars[i] !== wanted[0]) continue;
    if (!startsToken(hay.text, hay.indexes[i])) continue;
    const window = windowAt(hay.chars, wanted, i);
    if (window && (!best || window[1] - window[0] < best[1] - best[0])) best = window;
    if (best && best[1] - best[0] === wanted.length) break; // cannot be tighter
  }
  return best;
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
  const wanted = compact(String(needle ?? "")).chars;
  if (wanted.length < MIN_FUZZY_LENGTH) return null;
  const hay = compact(text);
  if (hay.chars.length < wanted.length) return null;

  const tokenWindow = shortestTokenWindow(hay, wanted);
  const anyWindow = shortestWindow(hay.chars, wanted);
  const window = tokenWindow || anyWindow;
  if (!window) return null;
  const [windowStart, windowEnd] = window;

  // Which characters of the window matched. The longest contiguous run ending
  // at the window's end is anchored there, and the rest of the needle matches
  // forward from the window's start: that is what keeps `1017` highlighted as
  // one run instead of `1` in the middle and `017` at the end.
  let suffix = 0;
  while (
    suffix < wanted.length &&
    hay.chars[windowEnd - 1 - suffix] === wanted[wanted.length - 1 - suffix]
  ) {
    suffix++;
  }
  let prefix = [];
  const prefixCount = wanted.length - suffix;
  if (prefixCount > 0) {
    let at = windowStart;
    const prefixEnd = windowEnd - suffix; // exclusive
    for (let j = 0; j < prefixCount; j++) {
      while (at < prefixEnd && hay.chars[at] !== wanted[j]) at++;
      if (at >= prefixEnd) break;
      prefix.push(at);
      at++;
    }
    if (prefix.length !== prefixCount) {
      // The anchored suffix and a prefix match cannot coexist here; drop the
      // anchoring and match the needle forward from the window's start.
      suffix = 0;
      prefix = [];
      let cursor = windowStart;
      for (const char of wanted) {
        while (hay.chars[cursor] !== char) cursor++;
        prefix.push(cursor);
        cursor++;
      }
    }
  }

  const positions = prefix.slice();
  for (let k = 0; k < suffix; k++) positions.push(windowEnd - suffix + k);

  // Map the window back to the original string, merging neighbours into runs.
  const ranges = [];
  for (const index of positions) {
    const start = hay.indexes[index];
    const end = start + 1;
    const last = ranges[ranges.length - 1];
    if (last && last.end === start) last.end = end;
    else ranges.push({ start, end });
  }
  return {
    ranges,
    span: windowEnd - windowStart,
    boundary: Boolean(tokenWindow),
    start: hay.indexes[windowStart],
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
 * Split `items` into the ones the query matches by substring and the ones it
 * only matches fuzzily. Both keep the input order. Callers that filter a list
 * use `filterFields`, which picks one bucket; callers that rank suggestions
 * use this to concatenate them.
 *
 * `fieldsOf(item)` returns the searchable text of one item, one entry per
 * field — not a joined string, or a subsequence could match across the
 * boundary between two fields that share no such text.
 */
export function partitionMatches(items, needle, fieldsOf) {
  const list = Array.isArray(items) ? items : [];
  const query = normalizeQuery(needle);
  const primary = [];
  const fuzzy = [];
  if (!query) return { primary: list.slice(), fuzzy };
  for (const item of list) {
    const fields = fieldsOf(item) || [];
    if (fields.some((field) => substringIndex(field, query) >= 0)) primary.push(item);
    else if (fields.some((field) => fuzzyMatch(field, query) !== null)) fuzzy.push(item);
  }
  return { primary, fuzzy };
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
 * Filter `items` by a free-text query: the substring matches when there are
 * any, the fuzzy ones only when there are none. This is the rule every filter
 * in the dashboard follows, kept in one place so they cannot drift.
 */
export function filterFields(items, needle, fieldsOf) {
  const { primary, fuzzy } = partitionMatches(items, needle, fieldsOf);
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

  const index = substringIndex(value, query);
  if (index >= 0) {
    const segments = [];
    if (index > 0) segments.push({ text: value.slice(0, index), hit: false });
    segments.push({ text: value.slice(index, index + query.length), hit: true });
    if (index + query.length < value.length) {
      segments.push({ text: value.slice(index + query.length), hit: false });
    }
    return segments;
  }

  const match = fuzzyMatch(value, query);
  if (!match) return whole;
  const segments = [];
  let at = 0;
  for (const range of match.ranges) {
    if (range.start > at) segments.push({ text: value.slice(at, range.start), hit: false });
    segments.push({ text: value.slice(range.start, range.end), hit: true });
    at = range.end;
  }
  if (at < value.length) segments.push({ text: value.slice(at), hit: false });
  return segments;
}
