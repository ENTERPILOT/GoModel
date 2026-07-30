// Persistence shape and trigger label for the shared reporting window.
// Rune-free and relative-imported so node:test can exercise it directly.
//
// Two kinds of window survive a reload:
//   - preset            "last N days", always relative to today
//   - custom + follow   a hand-picked window that ended on today, so it keeps
//                       tracking today and slides forward on a day change
//   - custom            a hand-picked window that ended in the past: frozen

import {
  addDaysToDateKey,
  daysBetweenDateKeys,
} from "../utils/dateKeys.js";

export const DATE_RANGE_STORAGE_KEY = "gomodel_date_range";

const STORAGE_VERSION = 1;

const MONTH_NAMES = [
  "Jan",
  "Feb",
  "Mar",
  "Apr",
  "May",
  "Jun",
  "Jul",
  "Aug",
  "Sep",
  "Oct",
  "Nov",
  "Dec",
];

function isDateKey(value) {
  return typeof value === "string" && /^\d{4}-\d{2}-\d{2}$/.test(value);
}

/** The persisted payload for a window, or null when there is nothing to save. */
export function serializeDateRange({
  selectedPreset,
  startKey,
  endKey,
  followsToday,
}) {
  if (selectedPreset) {
    return { v: STORAGE_VERSION, mode: "preset", days: String(selectedPreset) };
  }
  if (isDateKey(startKey) && isDateKey(endKey)) {
    return {
      v: STORAGE_VERSION,
      mode: "custom",
      start: startKey,
      end: endKey,
      follow: followsToday === true,
    };
  }
  return null;
}

/** Parse a persisted payload (JSON string or object); null when unusable. */
export function parseDateRange(raw) {
  let value = raw;
  if (typeof raw === "string") {
    try {
      value = JSON.parse(raw);
    } catch {
      return null;
    }
  }
  if (!value || typeof value !== "object") return null;

  if (value.mode === "preset") {
    const days = String(value.days ?? "");
    return /^[1-9]\d*$/.test(days) ? { mode: "preset", days } : null;
  }
  if (
    value.mode === "custom" &&
    isDateKey(value.start) &&
    isDateKey(value.end) &&
    value.start <= value.end
  ) {
    return {
      mode: "custom",
      start: value.start,
      end: value.end,
      follow: value.follow === true,
    };
  }
  return null;
}

/** Slide a window so it ends on `todayKey`, keeping its span. */
export function windowEndingToday(startKey, endKey, todayKey) {
  const span = daysBetweenDateKeys(startKey, endKey);
  if (span < 1 || !isDateKey(todayKey)) return { start: startKey, end: endKey };
  return { start: addDaysToDateKey(todayKey, -(span - 1)), end: todayKey };
}

function formatDateKeyShort(key) {
  const [year, month, day] = key.split("-");
  return MONTH_NAMES[Number(month) - 1] + " " + Number(day) + ", " + year;
}

/** Label for the picker trigger. A one-day window shows a single date. */
export function rangeLabel({ selectedPreset, startKey, endKey }) {
  if (selectedPreset) return "Last " + selectedPreset + " days";
  if (isDateKey(startKey) && isDateKey(endKey)) {
    if (startKey === endKey) return formatDateKeyShort(startKey);
    return formatDateKeyShort(startKey) + " – " + formatDateKeyShort(endKey);
  }
  if (isDateKey(startKey)) return formatDateKeyShort(startKey) + " – ...";
  return "Last 30 days";
}
