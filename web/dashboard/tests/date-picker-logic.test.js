import test from "node:test";
import assert from "node:assert/strict";

import {
  calendarDays,
  isSameMonth,
  shiftMonth,
} from "../src/lib/components/molecules/datePickerLogic.js";

const utc = (y, m, d = 1) => new Date(Date.UTC(y, m, d));
const key = (d) => d.toISOString().slice(0, 10);

test("calendarDays always returns a 6-week grid", () => {
  for (let month = 0; month < 12; month++) {
    assert.equal(calendarDays(utc(2026, month), 0, key).length, 42);
  }
});

test("calendarDays marks only the offset month as current", () => {
  // March 2026 starts on a Sunday, so the Monday-first grid leads with 6 blanks.
  const days = calendarDays(utc(2026, 2), 0, key);
  const current = days.filter((d) => d.current);
  assert.equal(current.length, 31);
  assert.equal(current[0].day, 1);
  assert.equal(current.at(-1).day, 31);
  assert.equal(days[0].current, false);
  assert.equal(days.indexOf(current[0]), 6);
});

test("calendarDays handles a leap February", () => {
  const current = calendarDays(utc(2024, 1), 0, key).filter((d) => d.current);
  assert.equal(current.length, 29);
});

test("calendarDays keys are unique and prefixed by month bucket", () => {
  const days = calendarDays(utc(2026, 2), 0, key);
  assert.equal(new Set(days.map((d) => d.key)).size, 42);
  assert.match(days[0].key, /^p-/);
  assert.match(days[6].key, /^c-/);
  assert.match(days.at(-1).key, /^n-/);
});

test("shiftMonth moves by whole months and normalizes to the first", () => {
  assert.equal(shiftMonth(utc(2026, 2, 17), -1).toISOString(), utc(2026, 1).toISOString());
  assert.equal(shiftMonth(utc(2026, 11), 1).toISOString(), utc(2027, 0).toISOString());
});

test("shiftMonth refuses to move past the limit", () => {
  const june = utc(2026, 5);
  assert.equal(shiftMonth(june, 1, utc(2026, 5)), june);
  assert.equal(shiftMonth(june, 1, utc(2026, 6)).toISOString(), utc(2026, 6).toISOString());
});

test("isSameMonth compares year and month only", () => {
  assert.equal(isSameMonth(utc(2026, 5), utc(2026, 5, 28)), true);
  assert.equal(isSameMonth(utc(2026, 5), utc(2025, 5, 28)), false);
  assert.equal(isSameMonth(utc(2026, 5), utc(2026, 6, 1)), false);
});
