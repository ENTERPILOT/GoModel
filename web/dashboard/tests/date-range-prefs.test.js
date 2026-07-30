// Pure-logic tests for the remembered reporting window: what gets persisted,
// how a stored window is restored, and the picker trigger label.

import test from "node:test";
import assert from "node:assert/strict";

import {
  parseDateRange,
  rangeLabel,
  serializeDateRange,
  windowEndingToday,
} from "../src/lib/stores/dateRangePrefs.js";
import {
  addDaysToDateKey,
  daysBetweenDateKeys,
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

test("daysBetweenDateKeys counts inclusively and survives DST-shifted zones", () => {
  assert.equal(daysBetweenDateKeys("2026-07-29", "2026-07-29"), 1);
  assert.equal(daysBetweenDateKeys("2026-03-01", "2026-04-01"), 32);
  assert.equal(daysBetweenDateKeys("", "2026-04-01"), 0);
});
