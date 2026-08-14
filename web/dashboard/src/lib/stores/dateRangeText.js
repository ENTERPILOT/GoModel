// Pure localized labels, span tooltips, and chart titles for the shared
// reporting window. This module stays rune-free for node:test.

import { daysBetweenDateKeys, isDateKey } from "../utils/dateKeys.js";
import { formatDate } from "../i18n/locale.js";
import * as m from "../paraglide/messages.js";
import { DEFAULT_PRESET_DAYS } from "./dateRangePrefs.js";

function lastDays(count) {
  return m.date_picker_last_days({ count: Number(count) });
}

function dateEdgeLabel(key, todayKey) {
  if (key === todayKey) return m.date_picker_today();
  return formatDate(new Date(key + "T00:00:00Z"), {
    day: "numeric",
    month: "short",
    year: "numeric",
    timeZone: "UTC",
  });
}

export function rangeLabel({ selectedPreset, startKey, endKey, todayKey }) {
  if (selectedPreset) return lastDays(selectedPreset);

  if (isDateKey(startKey) && isDateKey(endKey)) {
    if (startKey > endKey) return lastDays(DEFAULT_PRESET_DAYS);
    const start = dateEdgeLabel(startKey, todayKey);
    if (startKey === endKey) return start;
    return m.date_picker_range({
      start,
      end: dateEdgeLabel(endKey, todayKey),
    });
  }
  if (isDateKey(startKey)) {
    return m.date_picker_open_range({
      start: dateEdgeLabel(startKey, todayKey),
    });
  }
  return lastDays(DEFAULT_PRESET_DAYS);
}

export function rangeSpanLabel({
  selectedPreset,
  startKey,
  endKey,
  followsToday,
  todayKey,
}) {
  if (selectedPreset) return lastDays(selectedPreset);
  if (!isDateKey(startKey) || !isDateKey(endKey)) return "";
  if (startKey > endKey) return "";

  const days = daysBetweenDateKeys(startKey, endKey);
  if (followsToday || endKey === todayKey) {
    return days === 1
      ? m.date_picker_today()
      : lastDays(days);
  }
  return m.date_picker_days({ count: days });
}

export function rangeChartTitle(interval) {
  switch (interval) {
    case "weekly":
      return m.date_range_chart_title_weekly();
    case "monthly":
      return m.date_range_chart_title_monthly();
    case "yearly":
      return m.date_range_chart_title_yearly();
    default:
      return m.date_range_chart_title_daily();
  }
}
