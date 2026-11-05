// Pure helpers behind the SearchSelect molecule (no Svelte runtime, relative
// imports only) so tests/search-select.test.js can load them directly.

import { fuzzyMatch, normalizeQuery, substringIndex } from "../../utils/textMatch.js";

/**
 * Normalize a caller's option into {value, label, description}.
 * Strings become value/label pairs.
 */
export function normalizeSearchOption(option) {
  if (option === null || option === undefined) return null;
  if (typeof option !== "object") {
    const value = String(option);
    return { value, label: value, description: "" };
  }
  const value = String(option.value ?? "");
  return {
    value,
    label: String(option.label ?? value),
    description: String(option.description ?? ""),
  };
}

/**
 * Filter options by a free-text query, matching value, label and
 * description case-insensitively. Matches that start with the query rank
 * first (stable within each tier), then the ones that contain it. When
 * neither tier matches anything, the fuzzy level runs and its matches rank
 * by how tight the match is — `dep1017` reaches
 * `vllm/deepseek-v4.1-flash:1017`, `sonet` reaches `claude-sonnet`. A query
 * that already matches contiguously never mixes in fuzzy rows, so today's
 * results keep their rows and their order. An empty query returns every
 * option. Options sharing a value collapse to the first one, so the keyed
 * list the component renders never sees a duplicate key.
 *
 * @param {Array<unknown>} options
 * @param {string} query
 */
export function filterSearchOptions(options, query) {
  const seen = new Set();
  const normalized = (Array.isArray(options) ? options : [])
    .map(normalizeSearchOption)
    .filter((option) => {
      if (!option || seen.has(option.value)) return false;
      seen.add(option.value);
      return true;
    });
  const needle = normalizeQuery(query);
  if (!needle) return normalized;
  const prefix = [];
  const rest = [];
  for (const option of normalized) {
    const haystacks = [option.value, option.label, option.description];
    if (haystacks.some((text) => substringIndex(text, needle) === 0)) prefix.push(option);
    else if (haystacks.some((text) => substringIndex(text, needle) > 0)) rest.push(option);
  }
  if (prefix.length || rest.length) return prefix.concat(rest);

  // Fuzzy fallback. Descriptions describe the option rather than name it
  // (a provider name, "this model"), so a subsequence over them is noise.
  const scored = [];
  for (const option of normalized) {
    let best = null;
    for (const text of [option.value, option.label]) {
      best = betterFuzzyMatch(best, fuzzyMatch(text, needle));
    }
    if (best) scored.push({ option, match: best });
  }
  // A match that starts a token wins over a tighter one buried mid-word, then
  // the tighter window, then the earlier one. `ge25` prefers
  // `gemini/gemini-2.5-flash` over `openai/gpt-image-2.5-flare`, whose best run
  // is the `ge25` inside "ima|ge25|".
  return scored
    .sort((a, b) => {
      const left = fuzzyRank(a.match);
      const right = fuzzyRank(b.match);
      for (let i = 0; i < left.length; i++) {
        if (left[i] !== right[i]) return left[i] - right[i];
      }
      return 0; // equal: the sort keeps the input order
    })
    .map((entry) => entry.option);
}

/** Sort key for a fuzzy match: token start, then tightness, then position. */
function fuzzyRank(match) {
  return [match.boundary ? 0 : 1, match.span, match.start];
}

/**
 * Rank two fuzzy matches for the same needle: token start first, then the
 * tighter window, then the earlier one. Returns whichever should rank first.
 */
function betterFuzzyMatch(a, b) {
  if (!a) return b;
  if (!b) return a;
  if (a.boundary !== b.boundary) return a.boundary ? a : b;
  if (a.span !== b.span) return a.span < b.span ? a : b;
  return a.start <= b.start ? a : b;
}

/**
 * Whether a typed query is worth offering as a custom value: non-empty and
 * not already an option (by value, case-sensitively — values are identifiers).
 */
export function customSearchValue(options, query, allowCustom) {
  const value = String(query || "").trim();
  if (!allowCustom || !value) return "";
  const exists = (Array.isArray(options) ? options : [])
    .map(normalizeSearchOption)
    .some((option) => option && option.value === value);
  return exists ? "" : value;
}

/**
 * How many rows a search list renders at once. The model picker holds ~1000
 * options, and rebuilding every row on each keystroke is what made typing feel
 * queued: one keystroke cost 50-70 ms of main thread, so input events piled up
 * behind it. Past this many matches the user is expected to keep typing.
 */
export const MAX_VISIBLE_OPTIONS = 100;

/**
 * Cut a matched list down to the rows worth rendering. Keeps `pinnedValue`
 * visible when it sits beyond the cut (a single-select trigger already showing
 * a value the first page does not contain), so opening the list and typing do
 * not hide what is selected. `hidden` counts what the caller must announce;
 * the pinned row is not one of the first `limit`, so it does not subtract.
 *
 * @param {Array<unknown>} options
 * @param {number} limit
 * @param {string} pinnedValue
 * @returns {{visible: Array<unknown>, hidden: number}}
 */
export function capSearchOptions(options, limit, pinnedValue = "") {
  const list = Array.isArray(options) ? options : [];
  if (list.length <= limit) return { visible: list, hidden: 0 };
  const visible = list.slice(0, limit);
  if (pinnedValue) {
    const pinned = list.slice(limit).find((option) => option && option.value === pinnedValue);
    if (pinned) visible.push(pinned);
  }
  return { visible, hidden: list.length - limit };
}

/**
 * Move the keyboard highlight by `delta` rows, wrapping around. A -1 index
 * (nothing highlighted) moves to the first/last row. Returns -1 for an
 * empty list.
 */
export function moveActiveIndex(index, delta, length) {
  if (!length) return -1;
  if (index < 0) return delta >= 0 ? 0 : length - 1;
  return (((index + delta) % length) + length) % length;
}

/**
 * Split a typed multi-select entry into trimmed, de-duplicated values:
 * "anthropic/, openai/gpt-5" -> ["anthropic/", "openai/gpt-5"].
 */
export function splitSearchValues(query) {
  const values = [];
  for (const piece of String(query || "").split(/[,\n]/)) {
    const value = piece.trim();
    if (value && !values.includes(value)) values.push(value);
  }
  return values;
}

/**
 * Toggle `value` in a multi-select's values (append when absent, remove when
 * present). Returns a new array; the input is never mutated.
 */
export function toggleSearchValue(values, value) {
  const list = Array.isArray(values) ? values : [];
  const next = String(value ?? "");
  if (!next) return list.slice();
  return list.includes(next) ? list.filter((item) => item !== next) : list.concat(next);
}

/**
 * Order a multi-select's (already filtered) options with the selected ones
 * first, sorted by label, so they can be unchecked without scrolling; the
 * rest keep their order. Returns a new array.
 */
export function selectedFirst(options, values) {
  const list = Array.isArray(options) ? options : [];
  const chosen = new Set(Array.isArray(values) ? values : []);
  const selected = list
    .filter((option) => option && chosen.has(option.value))
    .sort((a, b) => a.label.localeCompare(b.label));
  const rest = list.filter((option) => option && !chosen.has(option.value));
  return selected.concat(rest);
}

const POPOVER_HEIGHT = 340; // search box plus the list's max-height
const POPOVER_GAP = 6;
const VIEWPORT_MARGIN = 8;
const POPOVER_MAX_WIDTH = 480;

/**
 * Inline style placing the fixed popover next to its trigger, or null when
 * the trigger has left the visible area and the popover should close.
 *
 * `trigger` is the trigger's client rect. `viewport` is the visible area in
 * the same coordinates: {top, height, width}. On phones that is the visual
 * viewport, which shrinks and pans when the on-screen keyboard opens while
 * the layout viewport (and so window.innerHeight) does not.
 *
 * The popover opens below the trigger, flips above it when there is more
 * room there, shrinks to that room, and never runs past the right edge. The
 * flipped popover is anchored by its bottom edge (translateY(-100%)), so its
 * position does not depend on its rendered height.
 */
export function popoverPlacement(trigger, viewport) {
  const visibleTop = viewport.top;
  const visibleBottom = viewport.top + viewport.height;
  if (trigger.bottom <= visibleTop || trigger.top >= visibleBottom) return null;

  const below = visibleBottom - trigger.bottom - POPOVER_GAP - VIEWPORT_MARGIN;
  const above = trigger.top - visibleTop - POPOVER_GAP - VIEWPORT_MARGIN;
  const flip = below < POPOVER_HEIGHT && above > below;
  const vertical = flip
    ? `top:${trigger.top - POPOVER_GAP}px;transform:translateY(-100%);max-height:${above}px;`
    : `top:${trigger.bottom + POPOVER_GAP}px;max-height:${below}px;`;
  const maxWidth = Math.min(POPOVER_MAX_WIDTH, viewport.width - 2 * VIEWPORT_MARGIN);
  const left = Math.max(VIEWPORT_MARGIN, Math.min(trigger.left, viewport.width - maxWidth - VIEWPORT_MARGIN));
  return `${vertical}left:${left}px;min-width:${Math.min(trigger.width, maxWidth)}px;max-width:${maxWidth}px;`;
}
