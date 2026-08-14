// Exports pure localized labels, span tooltips, and chart titles for the shared
// reporting window. Callers supply i18n operations so this module remains
// rune-free and directly testable.

import { daysBetweenDateKeys, isDateKey } from "../utils/dateKeys.js";
import { DEFAULT_PRESET_DAYS } from "./dateRangePrefs.js";

function dateEdgeLabel(key, todayKey, text) {
  return key === todayKey ? text.today() : text.date(key);
}

export function localizedRangeLabel(
  { selectedPreset, startKey, endKey, todayKey },
  text,
) {
  if (selectedPreset) {
    return text.lastDays(Number(selectedPreset));
  }

  if (isDateKey(startKey) && isDateKey(endKey)) {
    if (startKey > endKey) {
      return text.lastDays(Number(DEFAULT_PRESET_DAYS));
    }
    const start = dateEdgeLabel(startKey, todayKey, text);
    if (startKey === endKey) return start;
    return text.range({
      start,
      end: dateEdgeLabel(endKey, todayKey, text),
    });
  }
  if (isDateKey(startKey)) {
    return text.openRange({
      start: dateEdgeLabel(startKey, todayKey, text),
    });
  }
  return text.lastDays(Number(DEFAULT_PRESET_DAYS));
}

export function localizedRangeSpanLabel(
  { selectedPreset, startKey, endKey, followsToday, todayKey },
  text,
) {
  if (selectedPreset) {
    return text.lastDays(Number(selectedPreset));
  }
  if (!isDateKey(startKey) || !isDateKey(endKey)) return "";
  if (startKey > endKey) return "";

  const days = daysBetweenDateKeys(startKey, endKey);
  if (followsToday || endKey === todayKey) {
    return days === 1
      ? text.today()
      : text.lastDays(days);
  }
  return text.days(days);
}

export function localizedRangeChartTitle(interval, text) {
  const title = Object.hasOwn(text.chartTitle, interval)
    ? text.chartTitle[interval]
    : text.chartTitle.daily;
  return title();
}
