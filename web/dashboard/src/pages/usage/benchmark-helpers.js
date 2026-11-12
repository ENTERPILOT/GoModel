// Cost-per-active-user benchmark helpers: build the /admin/usage/benchmark
// query, label its months, format the change and build the chart (median and
// p90 cost per active user as lines, active users as bars on a second axis).
// Pure functions for node:test.

import { formatCost, formatCostShort } from "../../lib/utils/format.js";
import { barColors, chartTickFont, chartTooltip } from "../../lib/utils/chartTheme.js";
import { usageQuery } from "./usage-query.js";
import * as m from "../../lib/paraglide/messages.js";

// Month ranges offered, each ending with the current month: six complete
// months (the API default), a year, and the API maximum.
export const BENCHMARK_MONTH_OPTIONS = [7, 13, 24];
export const DEFAULT_BENCHMARK_MONTHS = 7;

// benchmarkQuery renders the request: the page filters (the benchmark has
// its own months, so no date window), the group, the month count and the
// cost basis. Provider cost is the API default and is left out.
export function benchmarkQuery({ filters = {}, parent = "", months = DEFAULT_BENCHMARK_MONTHS, costBasis = "provider" } = {}) {
  const pairs = [];
  const qs = usageQuery({ filters });
  if (parent) pairs.push("parent=" + encodeURIComponent(parent));
  pairs.push("months=" + months);
  if (costBasis === "billed") pairs.push("cost_basis=billed");
  return [qs, ...pairs].filter(Boolean).join("&");
}

// benchmarkMonths returns the response's months, oldest first.
export function benchmarkMonths(data) {
  return data && Array.isArray(data.months) ? data.months : [];
}

// benchmarkMonthLabel names a month, marking the partial current one.
export function benchmarkMonthLabel(month) {
  return month.partial ? m.usage_benchmark_partial_month({ month: month.month }) : month.month;
}

// formatChangePercent renders a month-over-month change: signed, one
// decimal, "–" when there is nothing to compare with.
export function formatChangePercent(value) {
  if (value == null || !Number.isFinite(Number(value))) return "–";
  const change = Number(value);
  return (change > 0 ? "+" : "") + change.toFixed(1) + "%";
}

// benchmarkHasUsage reports whether any month had an active user.
export function benchmarkHasUsage(data) {
  return benchmarkMonths(data).some((month) => month.active_users > 0);
}

// benchmarkChartConfig plots median and p90 cost per active user (left axis)
// over the months, with active users as bars on the right axis. Months
// without active users leave gaps in the cost lines rather than reading as
// free.
export function benchmarkChartConfig(colors, data) {
  const months = benchmarkMonths(data);
  const palette = barColors();
  const ticks = { color: colors.text, font: chartTickFont() };
  const line = (label, key, color, dashed) => ({
    type: "line",
    label,
    data: months.map((month) => month[key] ?? null),
    yAxisID: "cost",
    borderColor: color,
    backgroundColor: color,
    borderWidth: 2,
    borderDash: dashed ? [6, 4] : [],
    pointRadius: 3,
    spanGaps: false,
    order: 0,
  });
  return {
    type: "bar",
    data: {
      labels: months.map(benchmarkMonthLabel),
      datasets: [
        line(m.usage_benchmark_median(), "p50", palette[0], false),
        line(m.usage_benchmark_p90(), "p90", palette[2], true),
        {
          type: "bar",
          label: m.usage_benchmark_active_users(),
          data: months.map((month) => month.active_users || 0),
          yAxisID: "users",
          backgroundColor: palette[1],
          borderWidth: 0,
          maxBarThickness: 36,
          order: 1,
        },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      animation: { duration: 0 },
      scales: {
        x: { grid: { display: false }, border: { display: false }, ticks },
        cost: {
          position: "left",
          beginAtZero: true,
          grid: { color: colors.grid },
          border: { display: false },
          ticks: { ...ticks, callback: (v) => formatCostShort(v) },
        },
        users: {
          position: "right",
          beginAtZero: true,
          grid: { display: false },
          border: { display: false },
          ticks: { ...ticks, precision: 0 },
        },
      },
      plugins: {
        legend: { display: true, labels: { color: colors.text, font: { size: 12 } } },
        tooltip: chartTooltip(colors, {
          label: (c) =>
            c.dataset.label + ": " + (c.dataset.yAxisID === "users" ? Number(c.parsed.y).toLocaleString() : formatCost(c.parsed.y)),
        }),
      },
    },
  };
}
