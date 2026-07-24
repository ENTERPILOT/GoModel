// Ported from the pure-logic cases of
// internal/admin/dashboard/static/js/modules/pricing.test.cjs.
import test from "node:test";
import assert from "node:assert/strict";

import {
  pricingRecalculateDatePayload,
  pricingRecalculatePayload,
  pricingRecalculateSummary,
} from "../src/pages/settings/pricing-logic.js";

test("pricingRecalculatePayload uses preset days and trims filter fields", () => {
  const payload = pricingRecalculatePayload(
    { selectedPreset: "14" },
    " /team/alpha ",
    " openai/gpt-4o ",
    "recalculate",
  );

  assert.equal(
    JSON.stringify(payload),
    JSON.stringify({
      days: 14,
      user_path: "/team/alpha",
      selector: "openai/gpt-4o",
      confirmation: "recalculate",
    }),
  );
});

test("pricingRecalculatePayload uses custom date range", () => {
  const payload = pricingRecalculatePayload(
    {
      selectedPreset: null,
      customStartDate: new Date(Date.UTC(2026, 3, 1)),
      customEndDate: new Date(Date.UTC(2026, 3, 2)),
    },
    "",
    "",
    "recalculate",
  );

  assert.equal(payload.start_date, "2026-04-01");
  assert.equal(payload.end_date, "2026-04-02");
});

test("pricingRecalculateDatePayload falls back to 30 days for an unparsable preset", () => {
  assert.deepEqual(pricingRecalculateDatePayload({ selectedPreset: "soon" }), {
    days: 30,
  });
});

test("pricingRecalculateDatePayload uses today as the end-date fallback", () => {
  const payload = pricingRecalculateDatePayload({
    selectedPreset: null,
    customStartDate: new Date(Date.UTC(2026, 3, 1)),
    customEndDate: null,
    today: new Date(Date.UTC(2026, 3, 5)),
  });

  assert.equal(payload.start_date, "2026-04-01");
  assert.equal(payload.end_date, "2026-04-05");
});

test("pricingRecalculateSummary reports counts and missing pricing metadata", () => {
  assert.match(
    pricingRecalculateSummary({
      matched: 3,
      recalculated: 3,
      with_pricing: 2,
      without_pricing: 1,
    }),
    /^Pricing recalculated for 3 of 3 usage records\. 1 usage record still lacks pricing metadata\.$/,
  );

  assert.equal(
    pricingRecalculateSummary({ matched: 1, recalculated: 1 }),
    "Pricing recalculated for 1 of 1 usage record.",
  );

  assert.equal(
    pricingRecalculateSummary(null),
    "Pricing recalculated for 0 of 0 usage records.",
  );
});
