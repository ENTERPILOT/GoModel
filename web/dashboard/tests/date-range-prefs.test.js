// Pure-logic tests for the remembered reporting window: what gets persisted,
// how a stored window is restored, and the picker trigger label.

import test from "node:test";
import assert from "node:assert/strict";

import {
  parseDateRange,
  rangeLabel,
  rangeSpanLabel,
  serializeDateRange,
  windowEndingToday,
} from "../src/lib/stores/dateRangePrefs.js";
import {
  addDaysToDateKey,
  daysBetweenDateKeys,
  isDateKey,
} from "../src/lib/utils/dateKeys.js";

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
  assert.equal(rangeLabel({ selectedPreset: "14" }), "Last 14 days");
});

test("rangeLabel shows one date when the window is a single day", () => {
  assert.equal(
    rangeLabel({ startKey: "2026-07-29", endKey: "2026-07-29" }),
    "Jul 29, 2026",
  );
});

test("rangeLabel names today instead of dating it", () => {
  const todayKey = "2026-07-30";
  assert.equal(
    rangeLabel({ startKey: todayKey, endKey: todayKey, todayKey }),
    "Today",
  );
  assert.equal(
    rangeLabel({ startKey: "2026-07-01", endKey: todayKey, todayKey }),
    "Jul 1, 2026 – Today",
  );
  assert.equal(rangeLabel({ startKey: todayKey, todayKey }), "Today – ...");
});

test("rangeLabel shows both edges of a multi-day window", () => {
  assert.equal(
    rangeLabel({ startKey: "2026-07-01", endKey: "2026-07-29" }),
    "Jul 1, 2026 – Jul 29, 2026",
  );
});

test("rangeLabel falls back while only the start is picked", () => {
  assert.equal(rangeLabel({ startKey: "2026-07-01" }), "Jul 1, 2026 – ...");
  assert.equal(rangeLabel({}), "Last 30 days");
});

test("rangeSpanLabel counts the last N days for a window tracking today", () => {
  const todayKey = "2026-07-30";
  assert.equal(rangeSpanLabel({ selectedPreset: "7" }), "Last 7 days");
  assert.equal(
    rangeSpanLabel({
      startKey: "2026-07-11",
      endKey: todayKey,
      followsToday: true,
      todayKey,
    }),
    "Last 20 days",
  );
  assert.equal(
    rangeSpanLabel({
      startKey: todayKey,
      endKey: todayKey,
      followsToday: true,
      todayKey,
    }),
    "Today",
  );
});

test("rangeSpanLabel states the plain length of a frozen window", () => {
  const todayKey = "2026-07-30";
  assert.equal(
    rangeSpanLabel({ startKey: "2026-06-01", endKey: "2026-06-30", todayKey }),
    "30 days",
  );
  assert.equal(
    rangeSpanLabel({ startKey: "2026-06-09", endKey: "2026-06-09", todayKey }),
    "1 day",
  );
  assert.equal(rangeSpanLabel({ startKey: "2026-06-09", todayKey }), "");
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
