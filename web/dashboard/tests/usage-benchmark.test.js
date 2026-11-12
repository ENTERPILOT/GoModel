// Usage page: cost-per-active-user benchmark helpers.

import test from "node:test";
import assert from "node:assert/strict";

import {
  benchmarkChartConfig,
  benchmarkHasUsage,
  benchmarkMonthLabel,
  benchmarkQuery,
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
    { month: "2026-11", partial: true, active_users: 2, total_cost: 12, mean: 6, p50: 6, p90: 7.6, max: 8, p50_change_percent: -25 },
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
