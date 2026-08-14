// Exports pure localized labels, span tooltips, and chart titles for the shared
// reporting window. Callers supply i18n operations so this module remains
// rune-free and directly testable.

import { daysBetweenDateKeys, isDateKey } from "../utils/dateKeys.js";
import { DEFAULT_PRESET_DAYS } from "./dateRangePrefs.js";

function dateEdgeLabel(key, todayKey, text) {
  return key === todayKey ? text.t("datePicker.today") : text.date(key);
}

export function localizedRangeLabel(
  { selectedPreset, startKey, endKey, todayKey },
  text,
) {
  if (selectedPreset) {
    return text.plural("datePicker.lastDays", Number(selectedPreset));
  }

  if (isDateKey(startKey) && isDateKey(endKey)) {
    const start = dateEdgeLabel(startKey, todayKey, text);
    if (startKey === endKey) return start;
    return text.t("datePicker.range", {
      start,
      end: dateEdgeLabel(endKey, todayKey, text),
    });
  }
  if (isDateKey(startKey)) {
    return text.t("datePicker.openRange", {
      start: dateEdgeLabel(startKey, todayKey, text),
    });
  }
  return text.plural("datePicker.lastDays", Number(DEFAULT_PRESET_DAYS));
}

export function localizedRangeSpanLabel(
  { selectedPreset, startKey, endKey, followsToday, todayKey },
  text,
) {
  if (selectedPreset) {
    return text.plural("datePicker.lastDays", Number(selectedPreset));
  }
  if (!isDateKey(startKey) || !isDateKey(endKey)) return "";

  const days = daysBetweenDateKeys(startKey, endKey);
  if (followsToday || endKey === todayKey) {
    return days === 1
      ? text.t("datePicker.today")
      : text.plural("datePicker.lastDays", days);
  }
  return text.plural("datePicker.days", days);
}

export function localizedRangeChartTitle(interval, text) {
  return text.t(CHART_TITLE_KEYS[interval] || CHART_TITLE_KEYS.daily);
}

const CHART_TITLE_KEYS = {
  daily: "dateRange.chartTitle.daily",
  weekly: "dateRange.chartTitle.weekly",
  monthly: "dateRange.chartTitle.monthly",
  yearly: "dateRange.chartTitle.yearly",
};
