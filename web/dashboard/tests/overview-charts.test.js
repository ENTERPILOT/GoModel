// Ported from the overview-relevant cases of the legacy charts.test.cjs
// (the usage-page bar chart cases belong to the usage page port). Covers the
// stacked usage series math, day filling, the prompt-cache rate, and the
// gauge config.
import test from "node:test";
import assert from "node:assert/strict";

import {
  fillMissingDays,
  buildOverviewSeries,
  overviewChartConfig,
  promptCacheRate,
  promptCacheRateHasData,
  promptCacheRateText,
  promptCacheGaugeConfig,
} from "../src/pages/overview/overviewChartLogic.js";

const colors = {
  grid: "#111",
  text: "#222",
  tooltipBg: "#333",
  tooltipBorder: "#444",
  tooltipText: "#555",
};

function utc(key) {
  const [y, m, d] = key.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, d));
}

test("overview chart maps daily rows to stacked input/output series", () => {
  const daily = [
    { date: "2026-03-28", input_tokens: 1, output_tokens: 2 },
    { date: "2026-03-29", input_tokens: 3, output_tokens: 4 },
  ];
  const series = buildOverviewSeries(daily, []);
  const config = overviewChartConfig(colors, series, { cacheEnabled: false });

  assert.equal(config.type, "line");
  assert.deepEqual(config.data.labels, ["2026-03-28", "2026-03-29"]);
  assert.deepEqual(config.data.datasets.map((d) => d.label), [
    "Input Tokens",
    "Output Tokens",
    "Prompt (Input) Cached",
  ]);
  assert.deepEqual(config.data.datasets[0].data, [1, 3]);
  assert.deepEqual(config.data.datasets[1].data, [2, 4]);
  assert.equal(config.options.scales.x.stacked, true);
  assert.equal(config.options.scales.y.stacked, true);
});

test("overview chart adds the Locally Cached series only when cache analytics is on", () => {
  const series = buildOverviewSeries(
    [{ date: "2026-03-29", input_tokens: 3, output_tokens: 4 }],
    [{ date: "2026-03-29", input_tokens: 10, output_tokens: 5 }],
  );

  assert.deepEqual(series.local, [15]);

  const withCache = overviewChartConfig(colors, series, { cacheEnabled: true });
  assert.equal(withCache.data.datasets.length, 4);
  assert.equal(withCache.data.datasets[3].label, "Locally Cached");
  assert.deepEqual(withCache.data.datasets[3].data, [15]);

  const withoutCache = overviewChartConfig(colors, series, { cacheEnabled: false });
  assert.equal(withoutCache.data.datasets.length, 3);
});

test("paid input splits out prompt-cache reads when the split is present", () => {
  const series = buildOverviewSeries(
    [
      {
        date: "2026-03-29",
        input_tokens: 100,
        output_tokens: 20,
        uncached_input_tokens: 30,
        cached_input_tokens: 60,
        cache_write_input_tokens: 10,
      },
      // Older rows without the split fall back to the full input column.
      { date: "2026-03-30", input_tokens: 50, output_tokens: 5 },
    ],
    [],
  );

  assert.deepEqual(series.inputPaid, [40, 50]);
  assert.deepEqual(series.prompt, [60, 0]);
  assert.deepEqual(series.output, [20, 5]);
});

test("fillMissingDays pads the daily window with zero rows", () => {
  const filled = fillMissingDays(
    [
      { date: "2026-03-28", input_tokens: 1, output_tokens: 2, total_tokens: 3, requests: 1 },
      { date: "2026-03-30", input_tokens: 3, output_tokens: 4, total_tokens: 7, requests: 1 },
    ],
    "daily",
    utc("2026-03-27"),
    utc("2026-03-31"),
  );

  assert.deepEqual(
    filled.map((d) => d.date),
    ["2026-03-27", "2026-03-28", "2026-03-29", "2026-03-30", "2026-03-31"],
  );
  assert.equal(filled[0].input_tokens, 0);
  assert.equal(filled[1].input_tokens, 1);
  assert.equal(filled[2].total_tokens, 0);
  assert.equal(filled[3].output_tokens, 4);
  assert.equal(filled[2].total_cost, null);
});

test("fillMissingDays passes non-daily intervals through untouched", () => {
  const weekly = [{ date: "2026-03-23", input_tokens: 1 }];
  assert.equal(fillMissingDays(weekly, "weekly", utc("2026-03-01"), utc("2026-03-31")), weekly);
});

test("prompt cache rate uses the provider input split as its denominator", () => {
  const summary = {
    uncached_input_tokens: 30,
    cached_input_tokens: 60,
    cache_write_input_tokens: 10,
  };
  assert.equal(promptCacheRateHasData(summary), true);
  assert.equal(promptCacheRate(summary), 60);
  assert.equal(promptCacheRateText(summary), "60%");

  assert.equal(promptCacheRateHasData({}), false);
  assert.equal(promptCacheRate({}), 0);
  assert.equal(promptCacheRateText({}), "—");
});

test("prompt cache gauge clamps to 0..100 and renders a half doughnut", () => {
  const config = promptCacheGaugeConfig(160, "#fill", "#track");
  assert.equal(config.type, "doughnut");
  assert.deepEqual(config.data.datasets[0].data, [100, 0]);
  assert.deepEqual(config.data.datasets[0].backgroundColor, ["#fill", "#track"]);
  assert.equal(config.options.rotation, -90);
  assert.equal(config.options.circumference, 180);

  const clampedLow = promptCacheGaugeConfig(-5, "#fill", "#track");
  assert.deepEqual(clampedLow.data.datasets[0].data, [0, 100]);
});
