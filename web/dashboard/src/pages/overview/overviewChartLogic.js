// Overview usage chart + prompt-cache gauge logic.
// Pure functions so the chart math is testable with node.

import { chartTickFont, chartTooltip } from "../../lib/utils/chartTheme.ts";
import { tokenAxisTicks } from "./chartStyle.js";

function dateToKey(date) {
  return (
    date.getUTCFullYear() +
    "-" +
    String(date.getUTCMonth() + 1).padStart(2, "0") +
    "-" +
    String(date.getUTCDate()).padStart(2, "0")
  );
}

// fillMissingDays pads a daily series with zero rows over the reporting
// window, so quiet days render as gaps at zero instead of being skipped.
// Non-daily intervals pass through untouched (buckets are already dense).
export function fillMissingDays(daily, interval, startDate, endDate) {
  if (interval !== "daily" || !startDate || !endDate) {
    return daily;
  }
  const byDate = {};
  (daily || []).forEach((d) => {
    byDate[d.date] = d;
  });
  const result = [];
  for (
    let d = new Date(startDate);
    d <= endDate;
    d.setUTCDate(d.getUTCDate() + 1)
  ) {
    const key = dateToKey(d);
    result.push(
      byDate[key] || {
        date: key,
        input_tokens: 0,
        output_tokens: 0,
        total_tokens: 0,
        requests: 0,
        input_cost: null,
        output_cost: null,
        total_cost: null,
      },
    );
  }
  return result;
}

// buildOverviewSeries splits the filled daily usage into the overview chart's
// stacked series. Paid input = uncached + cache writes (prompt-cache reads
// are their own series); older rows lack the split, so fall back to the full
// input column when no split is present. The local series folds the response
// cache's input + output per bucket.
export function buildOverviewSeries(filledDaily, filledCacheDaily) {
  const num = (v) => Number(v) || 0;
  const labels = filledDaily.map((d) => d.date);
  const inputPaid = filledDaily.map((d) => {
    const split =
      num(d.uncached_input_tokens) +
      num(d.cache_write_input_tokens) +
      num(d.cached_input_tokens);
    return split > 0
      ? num(d.uncached_input_tokens) + num(d.cache_write_input_tokens)
      : num(d.input_tokens);
  });
  const output = filledDaily.map((d) => num(d.output_tokens));
  const prompt = filledDaily.map((d) => num(d.cached_input_tokens));

  const cacheByDate = {};
  (filledCacheDaily || []).forEach((d) => {
    cacheByDate[d.date] = d;
  });
  const local = labels.map((label) => {
    const c = cacheByDate[label];
    return c ? num(c.input_tokens) + num(c.output_tokens) : 0;
  });

  return { labels, inputPaid, output, prompt, local };
}

// Prompt cache rate: share of the period's provider input tokens that were
// served from the prompt cache. Denominator is the input "parts" (uncached +
// prompt-cached + cache writes), matching the cache meter's provider split.
export function promptCacheRate(summary) {
  const s = summary || {};
  const uncached = Math.max(0, Number(s.uncached_input_tokens) || 0);
  const cached = Math.max(0, Number(s.cached_input_tokens) || 0);
  const cacheWrite = Math.max(0, Number(s.cache_write_input_tokens) || 0);
  const denom = uncached + cached + cacheWrite;
  return denom > 0 ? (cached / denom) * 100 : 0;
}

export function promptCacheRateHasData(summary) {
  const s = summary || {};
  const denom =
    (Number(s.uncached_input_tokens) || 0) +
    (Number(s.cached_input_tokens) || 0) +
    (Number(s.cache_write_input_tokens) || 0);
  return denom > 0;
}

export function promptCacheRateText(summary) {
  if (!promptCacheRateHasData(summary)) return "—";
  return Math.round(promptCacheRate(summary)) + "%";
}

// Stacked area line chart: each series sits on top of the one below, so the
// band's top edge is the per-unit total. Same palette as the live throughput
// chart: paid tokens (input, output) are solid browns; cached tokens are
// dashed blues, the free "Locally Cached" lighter than the almost-free
// "Prompt cached".
export function overviewChartConfig(colors, series, options = {}) {
  const cacheEnabled = !!options.cacheEnabled;
  const resolve = options.resolve || ((expr) => expr);
  const fade = (expr, pct) =>
    resolve("color-mix(in srgb, " + expr + " " + pct + "%, transparent)");
  const line = (label, data, color, opts) =>
    Object.assign(
      {
        label: label,
        data: data,
        borderColor: color,
        backgroundColor: color,
        fill: false,
        tension: 0.3,
        borderWidth: 2,
        pointRadius: 0,
        pointHoverRadius: 4,
      },
      opts || {},
    );
  const datasets = [
    line("Input Tokens", series.inputPaid, resolve("var(--token-input)"), {
      fill: "origin",
    }),
    line("Output Tokens", series.output, resolve("var(--token-output)"), {
      fill: "-1",
    }),
    line(
      "Prompt (Input) Cached",
      series.prompt,
      resolve("var(--token-prompt)"),
      { fill: "-1", borderDash: [6, 4] },
    ),
  ];
  if (cacheEnabled) {
    datasets.push(
      line("Locally Cached", series.local, fade("var(--info)", 35), {
        fill: "-1",
        borderDash: [2, 3],
      }),
    );
  }
  return {
    type: "line",
    data: {
      labels: series.labels,
      datasets: datasets,
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      animation: { duration: 0 },
      interaction: { mode: "index", intersect: false },
      plugins: {
        legend: {
          labels: { color: colors.text, font: { size: 12 } },
        },
        tooltip: chartTooltip(colors, {
          label: (c) => c.dataset.label + ": " + c.parsed.y.toLocaleString(),
          footer: (items) => {
            let total = 0;
            items.forEach((it) => {
              total += Number(it.parsed.y) || 0;
            });
            return "Total: " + total.toLocaleString();
          },
        }),
      },
      scales: {
        x: {
          stacked: true,
          grid: { color: colors.grid },
          border: { display: false },
          ticks: {
            color: colors.text,
            font: chartTickFont(),
            maxRotation: 0,
            autoSkip: true,
            maxTicksLimit: 10,
          },
        },
        y: {
          stacked: true,
          beginAtZero: true,
          grid: { color: colors.grid },
          border: { display: false },
          ticks: tokenAxisTicks(colors),
        },
      },
    },
  };
}

// Half-circle gauge, filling clockwise from the left.
export function promptCacheGaugeConfig(pct, fillColor, trackColor) {
  const value = Math.max(0, Math.min(100, pct));
  return {
    type: "doughnut",
    data: {
      datasets: [
        {
          data: [value, 100 - value],
          backgroundColor: [fillColor, trackColor],
          borderWidth: 0,
          spacing: 0,
        },
      ],
    },
    options: {
      rotation: -90,
      circumference: 180,
      cutout: "84%",
      responsive: true,
      maintainAspectRatio: false,
      animation: { duration: 0 },
      layout: { padding: 1 },
      events: [],
      plugins: {
        legend: { display: false },
        tooltip: { enabled: false },
      },
    },
  };
}
