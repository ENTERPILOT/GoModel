// Pure-logic tests for the remembered reporting window: what gets persisted,
// how a stored window is restored, and localized picker presentation.

import test from "node:test";
import assert from "node:assert/strict";

import {
  parseDateRange,
  serializeDateRange,
  windowEndingToday,
} from "../src/lib/stores/dateRangePrefs.js";
import {
  localizedRangeChartTitle,
  localizedRangeLabel,
  localizedRangeSpanLabel,
} from "../src/lib/stores/dateRangeText.js";
import * as m from "../src/lib/paraglide/messages.js";
import {
  addDaysToDateKey,
  daysBetweenDateKeys,
  isDateKey,
} from "../src/lib/utils/dateKeys.js";

const englishText = {
  lastDays: (count) => m.date_picker_last_days({ count }),
  days: (count) => m.date_picker_days({ count }),
  today: m.date_picker_today,
  range: m.date_picker_range,
  openRange: m.date_picker_open_range,
  chartTitle: {
    daily: m.date_range_chart_title_daily,
    weekly: m.date_range_chart_title_weekly,
    monthly: m.date_range_chart_title_monthly,
    yearly: m.date_range_chart_title_yearly,
  },
  date: (key) =>
    new Intl.DateTimeFormat("en", {
      day: "numeric",
      month: "short",
      year: "numeric",
      timeZone: "UTC",
    }).format(new Date(key + "T00:00:00Z")),
};

test("serializeDateRange stores a preset as a relative window", () => {
  assert.deepEqual(
    serializeDateRange({ selectedPreset: "7", startKey: "", endKey: "" }),
    { v: 1, mode: "preset", days: "7" },
  );
});

test("serializeDateRange marks a window ending today as following", () => {
  assert.deepEqual(
    serializeDateRange({
      selectedPreset: null,
      startKey: "2026-07-25",
      endKey: "2026-07-29",
      followsToday: true,
    }),
    { v: 1, mode: "custom", start: "2026-07-25", end: "2026-07-29", follow: true },
  );
});

test("serializeDateRange freezes a window that ended in the past", () => {
  const stored = serializeDateRange({
    selectedPreset: null,
    startKey: "2026-06-01",
    endKey: "2026-06-10",
    followsToday: false,
  });
  assert.equal(stored.follow, false);
});

test("serializeDateRange returns null for an incomplete window", () => {
  assert.equal(
    serializeDateRange({ selectedPreset: null, startKey: "2026-07-01", endKey: "" }),
    null,
  );
});

test("serializeDateRange only writes presets that parse back", () => {
  assert.equal(serializeDateRange({ selectedPreset: "abc" }), null);
  assert.equal(serializeDateRange({ selectedPreset: "0" }), null);
  assert.equal(serializeDateRange({ selectedPreset: "-7" }), null);
});

test("parseDateRange round-trips both modes", () => {
  const preset = serializeDateRange({ selectedPreset: "30" });
  assert.deepEqual(parseDateRange(JSON.stringify(preset)), {
    mode: "preset",
    days: "30",
  });

  const custom = serializeDateRange({
    selectedPreset: null,
    startKey: "2026-07-01",
    endKey: "2026-07-10",
    followsToday: false,
  });
  assert.deepEqual(parseDateRange(JSON.stringify(custom)), {
    mode: "custom",
    start: "2026-07-01",
    end: "2026-07-10",
    follow: false,
  });
});

test("parseDateRange rejects junk instead of throwing", () => {
  assert.equal(parseDateRange(null), null);
  assert.equal(parseDateRange(""), null);
  assert.equal(parseDateRange("not json"), null);
  assert.equal(parseDateRange('{"mode":"preset","days":"abc"}'), null);
  assert.equal(parseDateRange('{"mode":"preset","days":"0"}'), null);
  assert.equal(parseDateRange('{"mode":"custom","start":"2026-07-10"}'), null);
  assert.equal(
    parseDateRange('{"mode":"custom","start":"07/01/2026","end":"07/10/2026"}'),
    null,
  );
  // Inverted windows are never produced by the picker.
  assert.equal(
    parseDateRange('{"mode":"custom","start":"2026-07-10","end":"2026-07-01"}'),
    null,
  );
});

test("windowEndingToday slides a stored window onto the new day", () => {
  assert.deepEqual(windowEndingToday("2026-07-25", "2026-07-29", "2026-08-02"), {
    start: "2026-07-29",
    end: "2026-08-02",
  });
});

test("windowEndingToday keeps the span across a month boundary", () => {
  const moved = windowEndingToday("2026-07-01", "2026-07-31", "2026-08-15");
  assert.equal(daysBetweenDateKeys(moved.start, moved.end), 31);
  assert.equal(moved.end, "2026-08-15");
  assert.equal(moved.start, addDaysToDateKey("2026-08-15", -30));
});

test("windowEndingToday keeps a single-day window one day long", () => {
  assert.deepEqual(windowEndingToday("2026-07-29", "2026-07-29", "2026-07-30"), {
    start: "2026-07-30",
    end: "2026-07-30",
  });
});

test("rangeLabel names a preset window", () => {
  assert.equal(
    localizedRangeLabel({ selectedPreset: "14" }, englishText),
    "Last 14 days",
  );
});

test("rangeLabel shows one date when the window is a single day", () => {
  assert.equal(
    localizedRangeLabel(
      { startKey: "2026-07-29", endKey: "2026-07-29" },
      englishText,
    ),
    "Jul 29, 2026",
  );
});

test("rangeLabel names today instead of dating it", () => {
  const todayKey = "2026-07-30";
  assert.equal(
    localizedRangeLabel(
      { startKey: todayKey, endKey: todayKey, todayKey },
      englishText,
    ),
    "Today",
  );
  assert.equal(
    localizedRangeLabel(
      { startKey: "2026-07-01", endKey: todayKey, todayKey },
      englishText,
    ),
    "Jul 1, 2026 – Today",
  );
  assert.equal(
    localizedRangeLabel({ startKey: todayKey, todayKey }, englishText),
    "Today – ...",
  );
});

test("rangeLabel shows both edges of a multi-day window", () => {
  assert.equal(
    localizedRangeLabel(
      { startKey: "2026-07-01", endKey: "2026-07-29" },
      englishText,
    ),
    "Jul 1, 2026 – Jul 29, 2026",
  );
});

test("rangeLabel falls back while only the start is picked", () => {
  assert.equal(
    localizedRangeLabel({ startKey: "2026-07-01" }, englishText),
    "Jul 1, 2026 – ...",
  );
  assert.equal(localizedRangeLabel({}, englishText), "Last 30 days");
});

test("rangeSpanLabel counts the last N days for a window tracking today", () => {
  const todayKey = "2026-07-30";
  assert.equal(
    localizedRangeSpanLabel({ selectedPreset: "7" }, englishText),
    "Last 7 days",
  );
  assert.equal(
    localizedRangeSpanLabel(
      {
        startKey: "2026-07-11",
        endKey: todayKey,
        followsToday: true,
        todayKey,
      },
      englishText,
    ),
    "Last 20 days",
  );
  assert.equal(
    localizedRangeSpanLabel(
      {
        startKey: todayKey,
        endKey: todayKey,
        followsToday: true,
        todayKey,
      },
      englishText,
    ),
    "Today",
  );
});

test("rangeSpanLabel states the plain length of a frozen window", () => {
  const todayKey = "2026-07-30";
  assert.equal(
    localizedRangeSpanLabel(
      { startKey: "2026-06-01", endKey: "2026-06-30", todayKey },
      englishText,
    ),
    "30 days",
  );
  assert.equal(
    localizedRangeSpanLabel(
      { startKey: "2026-06-09", endKey: "2026-06-09", todayKey },
      englishText,
    ),
    "1 day",
  );
  assert.equal(
    localizedRangeSpanLabel({ startKey: "2026-06-09", todayKey }, englishText),
    "",
  );
});

test("localizedRangeChartTitle translates complete interval phrases", () => {
  assert.equal(
    localizedRangeChartTitle("daily", englishText),
    "Daily Token Usage",
  );
  assert.equal(
    localizedRangeChartTitle("monthly", englishText),
    "Monthly Token Usage",
  );
  assert.equal(
    localizedRangeChartTitle("unknown", englishText),
    "Daily Token Usage",
  );
});

test("parseDateRange rejects keys that are not real calendar days", () => {
  // Date.UTC would roll these over into a window the user never picked.
  assert.equal(
    parseDateRange('{"mode":"custom","start":"2026-02-30","end":"2026-02-30"}'),
    null,
  );
  assert.equal(
    parseDateRange('{"mode":"custom","start":"2026-99-99","end":"2026-99-99"}'),
    null,
  );
  assert.equal(
    parseDateRange('{"mode":"custom","start":"2025-02-29","end":"2025-03-01"}'),
    null,
  );
});

test("isDateKey accepts real days and rejects rolled-over ones", () => {
  assert.equal(isDateKey("2024-02-29"), true);
  assert.equal(isDateKey("2026-02-29"), false);
  assert.equal(isDateKey("2026-13-01"), false);
  assert.equal(isDateKey("2026-07-00"), false);
  assert.equal(isDateKey("07/30/2026"), false);
  assert.equal(isDateKey(""), false);
});

test("daysBetweenDateKeys counts inclusively and survives DST-shifted zones", () => {
  assert.equal(daysBetweenDateKeys("2026-07-29", "2026-07-29"), 1);
  assert.equal(daysBetweenDateKeys("2026-03-01", "2026-04-01"), 32);
  assert.equal(daysBetweenDateKeys("", "2026-04-01"), 0);
});
