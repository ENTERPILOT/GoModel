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
