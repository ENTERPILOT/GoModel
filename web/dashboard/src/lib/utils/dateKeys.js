// UTC day-key ("YYYY-MM-DD") math. Plain module with no Svelte runtime, so
// rune stores and node-tested page logic can share one implementation.

function pad(n) {
  return String(n).padStart(2, "0");
}

/** Parse a day key into a UTC midnight Date; null when it is malformed. */
export function dateKeyToDate(key) {
  if (!key) return null;
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(key);
  if (!match) return null;
  return new Date(
    Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3])),
  );
}

/** Render a Date as its UTC day key; "" when it is not a usable Date. */
export function dateToDateKey(date) {
  if (
    !date ||
    typeof date.getTime !== "function" ||
    Number.isNaN(date.getTime())
  ) {
    return "";
  }
  return (
    date.getUTCFullYear() +
    "-" +
    pad(date.getUTCMonth() + 1) +
    "-" +
    pad(date.getUTCDate())
  );
}

/** Day key `days` after `key` (negative shifts back); "" when malformed. */
export function addDaysToDateKey(key, days) {
  const date = dateKeyToDate(key);
  if (!date) return "";
  date.setUTCDate(date.getUTCDate() + days);
  return dateToDateKey(date);
}

/**
 * True for a key naming a real calendar day. The shape alone is not enough:
 * Date.UTC silently rolls "2026-02-30" over to March, so an impossible key
 * has to be rejected by round-tripping it.
 */
export function isDateKey(key) {
  const date = dateKeyToDate(key);
  return !!date && dateToDateKey(date) === key;
}

/** Inclusive day count between two keys; 0 when either key is malformed. */
export function daysBetweenDateKeys(startKey, endKey) {
  const start = dateKeyToDate(startKey);
  const end = dateKeyToDate(endKey);
  if (!start || !end) return 0;
  return Math.round((end.getTime() - start.getTime()) / 86400000) + 1;
}
