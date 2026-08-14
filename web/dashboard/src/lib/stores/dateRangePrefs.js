// Persistence shape for the shared reporting window. Localized presentation
// lives in dateRangeText.js; this module stays rune-free for node:test.
//
// Two kinds of window survive a reload:
//   - preset            "last N days", always relative to today
//   - custom + follow   a hand-picked window that ended on today, so it keeps
//                       tracking today and slides forward on a day change
//   - custom            a hand-picked window that ended in the past: frozen

import {
  addDaysToDateKey,
  daysBetweenDateKeys,
  isDateKey,
} from "../utils/dateKeys.js";

export const DATE_RANGE_STORAGE_KEY = "gomodel_date_range";

/** The window a dashboard with no saved preference opens on. */
export const DEFAULT_PRESET_DAYS = "30";

const STORAGE_VERSION = 1;

/** A preset is a whole number of days; anything else never round-trips. */
function isPresetDays(value) {
  return /^[1-9]\d*$/.test(String(value ?? ""));
}

/** The persisted payload for a window, or null when there is nothing to save. */
export function serializeDateRange({
  selectedPreset,
  startKey,
  endKey,
  followsToday,
}) {
  // Only persist what parseDateRange would accept back.
  if (selectedPreset && isPresetDays(selectedPreset)) {
    return { v: STORAGE_VERSION, mode: "preset", days: String(selectedPreset) };
  }
  if (selectedPreset) return null;
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
    return isPresetDays(value.days)
      ? { mode: "preset", days: String(value.days) }
      : null;
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
