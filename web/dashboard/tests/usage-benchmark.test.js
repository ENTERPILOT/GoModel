// Usage page: cost-per-active-user benchmark helpers.

import test from "node:test";
import assert from "node:assert/strict";

import {
  BENCHMARK_MONTH_OPTIONS,
  benchmarkChangeLabel,
  benchmarkChartConfig,
  benchmarkCostBasisOptions,
  benchmarkHasPricingGaps,
  benchmarkHasUsage,
  benchmarkMonthLabel,
  benchmarkPricingGaps,
  benchmarkQuery,
  benchmarkStatus,
  benchmarkUnattributedCost,
  effectiveBenchmarkCostBasis,
  formatChangePercent,
} from "../src/pages/usage/benchmark-helpers.js";

const BENCHMARK = {
  parent: "/eng",
  time_zone: "UTC",
  currency: "USD",
  cost_basis: "provider",
  months: [
    { month: "2026-09", partial: false, active_users: 0, total_cost: 0, mean: null, p50: null, p90: null, max: null },
    { month: "2026-10", partial: false, active_users: 4, total_cost: 40, mean: 10, p50: 8, p90: 18, max: 20, p50_change_percent: null },
    { month: "2026-11", partial: true, active_users: 2, total_cost: 12, mean: 6, p50: 6, p90: 7.6, max: 8, p50_change_percent: null },
  ],
};

test("benchmarkQuery sends the filters, group, months and a billed basis", () => {
  const filters = { models: ["gpt-5"], providers: [], labels: ["pr 12"], userPaths: [], authKeys: [], endpoints: [], session: "" };
  assert.equal(
    benchmarkQuery({ filters, parent: "/eng", months: 13, costBasis: "billed" }),
    "model=gpt-5&label=pr%2012&parent=%2Feng&months=13&cost_basis=billed",
  );
  assert.equal(benchmarkQuery({}), "months=7", "provider cost and the default group are the API defaults");
});

test("benchmarkMonthLabel marks the partial current month", () => {
  assert.equal(benchmarkMonthLabel(BENCHMARK.months[1]), "2026-10");
  assert.match(benchmarkMonthLabel(BENCHMARK.months[2]), /2026-11.+/);
});

test("formatChangePercent signs the change and dashes the unknown", () => {
  assert.equal(formatChangePercent(4.25), "+4.3%");
  assert.equal(formatChangePercent(-25), "-25.0%");
  assert.equal(formatChangePercent(0), "0.0%");
  assert.equal(formatChangePercent(null), "–");
  assert.equal(formatChangePercent(undefined), "–");
});

test("benchmarkHasUsage needs an active user in some month", () => {
  assert.equal(benchmarkHasUsage(BENCHMARK), true);
  assert.equal(benchmarkHasUsage({ months: [BENCHMARK.months[0]] }), false);
  assert.equal(benchmarkHasUsage(null), false);
});

test("benchmarkChartConfig plots median and p90 over active users", () => {
  const config = benchmarkChartConfig({ text: "#000", grid: "#ccc" }, BENCHMARK);
  assert.equal(config.data.labels.length, 3);
  const [median, p90, users] = config.data.datasets;
  assert.equal(median.type, "line");
  assert.deepEqual(median.data, [null, 8, 6], "a month without users is a gap, not zero");
  assert.deepEqual(p90.data, [null, 18, 7.6]);
  assert.equal(p90.yAxisID, "cost");
  assert.equal(users.type, "bar");
  assert.equal(users.yAxisID, "users");
  assert.deepEqual(users.data, [0, 4, 2]);
  assert.equal(config.options.scales.users.position, "right");
});

test("the month picker stops at the API maximum of 12 months", () => {
  assert.equal(Math.max(...BENCHMARK_MONTH_OPTIONS), 12);
  assert.ok(BENCHMARK_MONTH_OPTIONS.includes(7), "the API default is offered");
});

test("benchmarkStatus shows the failed state over any data", () => {
  const cases = [
    [{ failed: true, data: BENCHMARK }, "failed"],
    [{ failed: true, data: null }, "failed"],
    [{ failed: false, data: null }, "empty"],
    [{ failed: false, data: { months: [BENCHMARK.months[0]] } }, "empty"],
    [{ failed: false, data: BENCHMARK }, "ready"],
    // Usage on the group itself is not "no usage".
    [{ failed: false, data: { months: [{ ...BENCHMARK.months[0], unattributed_cost: 3 }] } }, "ready"],
  ];
  for (const [state, want] of cases) {
    assert.equal(benchmarkStatus(state), want, JSON.stringify(state));
  }
  assert.equal(benchmarkStatus(), "empty");
});

test("the partial month reads as in progress, not as a decline", () => {
  const partial = BENCHMARK.months[2];
  const label = benchmarkChangeLabel({ ...partial, p50_change_percent: -59.4 });
  assert.doesNotMatch(label, /%/, "no percentage for a partial month");
  assert.notEqual(label, "–");
  assert.equal(benchmarkChangeLabel({ ...BENCHMARK.months[1], p50_change_percent: -25 }), "-25.0%");

  const [median, p90, users] = benchmarkChartConfig({ text: "#000", grid: "#ccc" }, BENCHMARK).data.datasets;
  for (const line of [median, p90]) {
    assert.deepEqual(line.segment.borderDash({ p0DataIndex: 1, p1DataIndex: 2 }), [2, 3], "dotted into the partial month");
    assert.equal(line.segment.borderDash({ p0DataIndex: 0, p1DataIndex: 1 }), undefined, "complete months keep their style");
    assert.equal(line.pointStyle[2], "rectRot");
  }
  assert.notEqual(users.backgroundColor[2], users.backgroundColor[1], "the partial month's bar is faded");
});

test("the billed toggle shows only with billed cost visible", () => {
  const config = (billed) => ({ billedCostVisible: () => billed });
  assert.equal(benchmarkCostBasisOptions(config(false)), null);
  assert.equal(benchmarkCostBasisOptions(null), null);
  assert.deepEqual(
    benchmarkCostBasisOptions(config(true)).map((option) => option.value),
    ["provider", "billed"],
  );
  assert.equal(effectiveBenchmarkCostBasis("billed", config(true)), "billed");
  assert.equal(effectiveBenchmarkCostBasis("billed", config(false)), "provider", "a hidden toggle never asks for billed");
  assert.equal(effectiveBenchmarkCostBasis("provider", config(true)), "provider");
});

test("pricing gaps and unattributed cost are surfaced", () => {
  const months = [
    { month: "2026-09", unpriced_users: 0, partly_priced_users: 0, unattributed_cost: 0 },
    { month: "2026-10", unpriced_users: 1, partly_priced_users: 2, unattributed_cost: 1.5 },
    { month: "2026-11", unpriced_users: 0, partly_priced_users: 0, unattributed_cost: 2 },
  ];
  assert.equal(benchmarkPricingGaps(months[0]), "–");
  assert.match(benchmarkPricingGaps(months[1]), /1.*2/);
  assert.equal(benchmarkHasPricingGaps({ months }), true);
  assert.equal(benchmarkHasPricingGaps({ months: [months[0]] }), false);
  assert.equal(benchmarkUnattributedCost({ months }), 3.5);
  assert.equal(benchmarkUnattributedCost(null), 0);
});
