import test from "node:test";
import assert from "node:assert/strict";

import { licenseIndicator } from "../src/lib/utils/licenseIndicator.js";

const NOW = Date.parse("2026-10-24T12:00:00Z");

// A license whose term ends at midnight UTC after `day`, with `graceDays` of
// grace, the way GoModel Pro reports it.
function license(day, { trial = false, graceDays = 0 } = {}) {
  const end = Date.parse(`${day}T00:00:00Z`) + 24 * 60 * 60 * 1000;
  return {
    LICENSE_BADGE: "PRO",
    LICENSE_TRIAL: trial ? "on" : "",
    LICENSE_EXPIRES_AT: new Date(end).toISOString(),
    LICENSE_VALID_UNTIL: new Date(end + graceDays * 86400000).toISOString(),
  };
}

test("no badge shows nothing", () => {
  assert.equal(licenseIndicator({}, NOW), null);
  assert.equal(licenseIndicator({ LICENSE_TRIAL: "on" }, NOW), null);
});

test("a license without an expiry shows only the badge", () => {
  assert.deepEqual(licenseIndicator({ LICENSE_BADGE: "PRO" }, NOW), {
    badge: "PRO",
    chip: null,
  });
});

test("a trial always counts down, turning to warning in its last week", () => {
  assert.deepEqual(licenseIndicator(license("2026-11-07", { trial: true }), NOW).chip, {
    kind: "trial",
    days: 15,
    tone: "normal",
    endDate: "2026-11-07",
  });
  const lastWeek = licenseIndicator(license("2026-10-26", { trial: true }), NOW).chip;
  assert.equal(lastWeek.days, 3);
  assert.equal(lastWeek.tone, "warning");
});

test("a full license counts down only in its last 21 days", () => {
  assert.equal(licenseIndicator(license("2026-11-20", { graceDays: 7 }), NOW).chip, null);
  const soon = licenseIndicator(license("2026-11-04", { graceDays: 7 }), NOW).chip;
  assert.equal(soon.kind, "expiring");
  assert.equal(soon.days, 12);
  assert.equal(soon.tone, "normal");
  assert.equal(licenseIndicator(license("2026-10-28", { graceDays: 7 }), NOW).chip.tone, "warning");
});

test("in the grace period the chip counts days since the end, in red", () => {
  assert.deepEqual(licenseIndicator(license("2026-10-21", { graceDays: 7 }), NOW).chip, {
    kind: "grace",
    days: 3,
    graceDays: 5,
    graceEndDate: "2026-10-28",
    tone: "danger",
    endDate: "2026-10-21",
  });
});

test("after the grace period the license shows as expired", () => {
  const chip = licenseIndicator(license("2026-10-01", { graceDays: 7 }), NOW).chip;
  assert.equal(chip.kind, "expired");
  assert.equal(chip.days, 23);
  assert.equal(chip.tone, "danger");
});

test("an ended trial has no grace period", () => {
  const chip = licenseIndicator(license("2026-10-23", { trial: true }), NOW).chip;
  assert.equal(chip.kind, "expired");
  assert.equal(chip.days, 1);
});

test("unparseable times show only the badge", () => {
  assert.deepEqual(
    licenseIndicator({ LICENSE_BADGE: "PRO", LICENSE_EXPIRES_AT: "soon" }, NOW),
    { badge: "PRO", chip: null },
  );
});
