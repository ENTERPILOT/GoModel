// Chart.js config for the horizontal usage breakdown charts. Pure config
// building — the caller supplies theme colors and a CSS color resolver, so
// node:test can exercise it without a DOM.

import { formatTokensShort } from "../../lib/utils/format.js";
import { chartTickFont, chartTooltip } from "../../lib/utils/chartTheme.js";
import * as m from "../../lib/paraglide/messages.js";

// Horizontal input/output bars: one row per entity (model, user path, label).
// In the default diverging view the input-side series (paid input,
// prompt-cached reads) grow left from zero and the output-side series
// (output, locally-cached tokens) grow right; in the stacked view everything
// piles rightward from zero as segments of one bar. Series reuse the overview
// chart's token palette (paid browns, translucent cache blues) so they read
// consistently across the dashboard; the legend (and, when diverging, the
// left/right split) carries identity, not color alone. Cache series appear
// only when they have data.
export function horizontalUsageChartConfig(colors, labels, series, options) {
  const { stacked = false, costs = false, resolve = (expr) => expr } = options || {};
  const fmtShort = (v) => (costs ? "$" + Math.abs(v).toFixed(2) : formatTokensShort(Math.abs(v)));
  const fmtExact = (v) => (costs ? "$" + Math.abs(v).toFixed(4) : Math.abs(v).toLocaleString());
  const inputSide = (values) => values.map((v) => (stacked ? Math.abs(v) : -Math.abs(v)));
  const bar = (label, data, color) => ({
    label: label,
    data: data,
    backgroundColor: color,
    borderColor: "transparent",
    borderWidth: 0,
    borderRadius: 4,
    maxBarThickness: 22,
  });
  const hasData = (values) => (values || []).some((v) => Math.abs(v) > 0);
  const datasets = [
    bar(costs ? m.usage_column_input_cost() : m.usage_column_input_tokens(), inputSide(series.inputs), resolve("var(--token-input)")),
    bar(costs ? m.usage_column_output_cost() : m.usage_column_output_tokens(), series.outputs, resolve("var(--token-output)")),
  ];
  if (hasData(series.prompts)) {
    datasets.push(
      bar(costs ? m.usage_prompt_cached_cost() : m.usage_column_prompt_cached(), inputSide(series.prompts), resolve("var(--token-prompt)")),
    );
  }
  // Local cache hits carry both sides, so each joins its own half of the axis
  // (they pile together in the stacked view).
  if (!costs && hasData(series.localIns)) {
    datasets.push(bar(m.usage_locally_cached_input(), inputSide(series.localIns), resolve("var(--token-local)")));
  }
  if (!costs && hasData(series.localOuts)) {
    datasets.push(bar(m.usage_locally_cached_output(), series.localOuts, resolve("var(--token-local)")));
  }
  return {
    type: "bar",
    data: {
      labels: labels,
      datasets: datasets,
    },
    options: {
      indexAxis: "y",
      responsive: true,
      maintainAspectRatio: false,
      animation: { duration: 0 },
      layout: { padding: { top: 8 } },
      scales: {
        x: {
          stacked: true,
          beginAtZero: true,
          // Emphasize the zero divider between the diverging halves.
          grid: stacked
            ? { color: colors.grid }
            : { color: (ctx) => (ctx.tick && ctx.tick.value === 0 ? colors.text : colors.grid) },
          border: { display: false },
          ticks: {
            color: colors.text,
            font: chartTickFont(),
            callback: (v) => fmtShort(v),
          },
        },
        y: {
          stacked: true,
          grid: { display: false },
          border: { display: false },
          ticks: {
            color: colors.text,
            font: chartTickFont(),
            autoSkip: false,
          },
        },
      },
      plugins: {
        legend: {
          labels: { color: colors.text, font: { size: 12 } },
        },
        tooltip: chartTooltip(colors, {
          label: (c) => c.dataset.label + ": " + fmtExact(c.parsed.x),
          footer: (items) => {
            let total = 0;
            items.forEach((it) => {
              total += Math.abs(Number(it.parsed.x)) || 0;
            });
            return m.overview_total_label({ total: fmtExact(total) });
          },
        }),
      },
    },
  };
}
