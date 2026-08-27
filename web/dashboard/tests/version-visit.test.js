// Pure-logic tests for the dashboard's daily update-check gate: reading the
// visit cookie and deciding whether today's check has already happened.

import test from "node:test";
import assert from "node:assert/strict";

import {
  VISIT_COOKIE,
  checkPlan,
  checkedToday,
  localDateKey,
  readVisitCookie,
} from "../src/lib/stores/versionVisit.js";

test("localDateKey formats the browser's own date, zero padded", () => {
  assert.equal(localDateKey(new Date(2026, 7, 26, 23, 59)), "2026-08-26");
  assert.equal(localDateKey(new Date(2026, 0, 5, 0, 1)), "2026-01-05");
});

test("readVisitCookie finds the marker among other cookies", () => {
  const header = `theme=dark; ${VISIT_COOKIE}=2026-08-26-abc; other=1`;
  assert.equal(readVisitCookie(header), "2026-08-26-abc");
});

test("readVisitCookie is not fooled by a cookie whose name ends the same way", () => {
  assert.equal(readVisitCookie(`not_${VISIT_COOKIE}=2026-08-26-abc`), "");
});

test("readVisitCookie returns empty for an absent or empty header", () => {
  assert.equal(readVisitCookie(""), "");
  assert.equal(readVisitCookie("theme=dark"), "");
  assert.equal(readVisitCookie(undefined), "");
});

test("readVisitCookie survives a value that is not valid percent-encoding", () => {
  assert.equal(readVisitCookie(`${VISIT_COOKIE}=%E0%A4%A`), "");
});

test("checkedToday is true only for today's marker", () => {
  assert.equal(checkedToday("2026-08-26-abc", "2026-08-26"), true);
  assert.equal(checkedToday("2026-08-25-abc", "2026-08-26"), false);
});

test("checkedToday treats a missing or malformed cookie as due", () => {
  for (const value of ["", null, undefined, "garbage", "2026-08-26"]) {
    assert.equal(checkedToday(value, "2026-08-26"), false, `value: ${value}`);
  }
});

test("checkedToday requires the separator after the date", () => {
  assert.equal(checkedToday("2026-08-26xabc", "2026-08-26"), false);
});

// Regression: gating the request on the cookie made the update indicator show
// on the day's first page load and disappear on every load after it. The
// status must be fetched every time; only the timing depends on the cookie.
test("the status is fetched on every page load, checked in today or not", () => {
  assert.equal(checkPlan("2026-08-26-abc", "2026-08-26").fetch, true);
  assert.equal(checkPlan("2026-08-25-abc", "2026-08-26").fetch, true);
  assert.equal(checkPlan("", "2026-08-26").fetch, true);
  assert.equal(checkPlan("garbage", "2026-08-26").fetch, true);
});

test("only a browser that has not checked in today delays its request", () => {
  assert.equal(checkPlan("2026-08-26-abc", "2026-08-26").delayed, false);
  assert.equal(checkPlan("2026-08-25-abc", "2026-08-26").delayed, true);
  assert.equal(checkPlan("", "2026-08-26").delayed, true);
});
