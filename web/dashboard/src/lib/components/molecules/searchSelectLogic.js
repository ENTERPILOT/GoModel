// Pure helpers behind the SearchSelect molecule (no Svelte runtime, relative
// imports only) so tests/search-select.test.js can load them directly.

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
 * first (stable within each tier). An empty query returns every option.
 * Options sharing a value collapse to the first one, so the keyed list the
 * component renders never sees a duplicate key.
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
  const needle = String(query || "").trim().toLowerCase();
  if (!needle) return normalized;
  const prefix = [];
  const rest = [];
  for (const option of normalized) {
    const haystacks = [option.value, option.label, option.description].map((s) => s.toLowerCase());
    if (haystacks.some((s) => s.startsWith(needle))) prefix.push(option);
    else if (haystacks.some((s) => s.includes(needle))) rest.push(option);
  }
  return prefix.concat(rest);
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
