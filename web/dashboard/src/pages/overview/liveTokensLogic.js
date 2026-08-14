// Live token throughput logic.
// Pure helpers (bucket aggregation, labels, chart config); the polling/SSE
// engine lives in liveTokensState.svelte.js.

import { formatTokensShort } from "../../lib/utils/format.js";
import { chartTickFont, chartTooltip } from "../../lib/utils/chartTheme.js";
import { tokenAxisTicks } from "./chartStyle.js";
import * as m from "../../lib/paraglide/messages.js";

// apiName: backend granularity; refreshMs: how often to refetch to scroll the
// window (matched to the bucket width, capped for coarse views).
export const GRANULARITIES = {
  seconds: { apiName: "second", windowLabel: m.overview_last_60_seconds(), refreshMs: 2000 },
  minutes: { apiName: "minute", windowLabel: m.overview_last_60_minutes(), refreshMs: 5000 },
  hours: { apiName: "hour", windowLabel: m.overview_last_24_hours(), refreshMs: 20000 },
  days: { apiName: "day", windowLabel: m.overview_last_30_days(), refreshMs: 60000 },
};

export const GRANULARITY_OPTIONS = [
  { value: "seconds", label: m.overview_seconds() },
  { value: "minutes", label: m.overview_minutes() },
  { value: "hours", label: m.overview_hours() },
  { value: "days", label: m.overview_days() },
];

function zeroMetrics() {
  return { input: 0, output: 0, prompt: 0, local: 0 };
}

function pad2(n) {
  return String(n).padStart(2, "0");
}

function num(value) {
  const n = Number(value);
  return Number.isFinite(n) && n > 0 ? n : 0;
}

// Bucket-start label, formatted per granularity in local time.
function formatBucketLabel(granularity, startMs) {
  if (!Number.isFinite(startMs)) {
    return "";
  }
  const d = new Date(startMs);
  switch (granularity) {
    case "seconds":
      return (
        pad2(d.getHours()) + ":" + pad2(d.getMinutes()) + ":" + pad2(d.getSeconds())
      );
    case "minutes":
      return pad2(d.getHours()) + ":" + pad2(d.getMinutes());
    case "hours":
      return pad2(d.getHours()) + ":00";
    case "days":
    default:
      return pad2(d.getMonth() + 1) + "-" + pad2(d.getDate());
  }
}

// bucketsToSeries flattens the throughput buckets into chart columns plus the
// window totals shown in the legend.
export function bucketsToSeries(buckets, granularity) {
  const labels = [];
  const stamps = [];
  const cols = { input: [], output: [], prompt: [], local: [] };
  const totals = zeroMetrics();
  for (const bucket of buckets || []) {
    const startMs = Date.parse(bucket && bucket.start);
    const input = num(bucket && bucket.input_tokens);
    const output = num(bucket && bucket.output_tokens);
    const prompt = num(bucket && bucket.prompt_cached_tokens);
    const local = num(bucket && bucket.locally_cached_tokens);
    labels.push(formatBucketLabel(granularity, startMs));
    stamps.push(Number.isFinite(startMs) ? startMs : null);
    cols.input.push(input);
    cols.output.push(output);
    cols.prompt.push(prompt);
    cols.local.push(local);
    totals.input += input;
    totals.output += output;
    totals.prompt += prompt;
    totals.local += local;
  }
  return { labels, stamps, cols, totals };
}

export function liveTokensHasData(totals) {
  const s = totals || zeroMetrics();
  return s.input + s.output + s.prompt + s.local > 0;
}

export function liveTokensLegendValue(totals, metric) {
  return formatTokensShort(
    Math.max(0, Math.round((totals && totals[metric]) || 0)),
  );
}

export function liveTokensWindowLabel(granularity) {
  return (GRANULARITIES[granularity] || GRANULARITIES.minutes).windowLabel;
}

// Text equivalent of the chart for screen readers (the canvas itself is
// opaque to assistive tech).
export function liveTokensChartAriaLabel(totals, granularity) {
  return m.overview_live_chart_label({
    window: liveTokensWindowLabel(granularity).toLowerCase(),
    input: liveTokensLegendValue(totals, "input"),
    output: liveTokensLegendValue(totals, "output"),
    prompt: liveTokensLegendValue(totals, "prompt"),
    local: liveTokensLegendValue(totals, "local"),
  });
}

export function liveTokensChartConfig(colors, seriesColors, series, granularity) {
  const formatValue = (v) => formatTokensShort(Math.max(0, Math.round(v)));
  const stamps = series.stamps;
  const bar = (label, data, color) => ({
    label: label,
    data: data,
    backgroundColor: color,
    borderWidth: 0,
    borderRadius: 0,
    categoryPercentage: 1.0, // touching bars: fill the category, no inter-bar gap
    barPercentage: 1.0,
    stack: "tokens",
  });
  // Mark where the local calendar day changes between buckets with a dashed
  // separator + date label. Chart.js has no built-in for this on a category
  // axis (a time scale could, but needs a date-adapter lib we don't bundle),
  // so we draw it directly. Skipped for the Days view, where every bar is
  // already a separate day.
  const dayMarks = {
    id: "liveTokensDayMarks",
    afterDatasetsDraw: (chart) => {
      if (granularity === "days") return;
      const meta = chart.getDatasetMeta(0);
      const area = chart.chartArea;
      if (!meta || !meta.data || !area) return;
      const ctx = chart.ctx;
      ctx.save();
      ctx.font = "10px 'SF Mono', Menlo, Consolas, monospace";
      let prevKey = null;
      for (let i = 0; i < stamps.length && i < meta.data.length; i++) {
        const ms = stamps[i];
        if (!Number.isFinite(ms)) {
          prevKey = null;
          continue;
        }
        const d = new Date(ms);
        const key = d.getFullYear() + "-" + d.getMonth() + "-" + d.getDate();
        if (prevKey !== null && key !== prevKey && meta.data[i - 1]) {
          const markerColor = colors.dayMarker || colors.text;
          const x = Math.round((meta.data[i - 1].x + meta.data[i].x) / 2) + 0.5;
          ctx.globalAlpha = 0.6;
          ctx.strokeStyle = markerColor;
          ctx.setLineDash([3, 3]);
          ctx.lineWidth = 1;
          ctx.beginPath();
          ctx.moveTo(x, area.top);
          ctx.lineTo(x, area.bottom);
          ctx.stroke();
          ctx.setLineDash([]);
          // Full opacity + high-contrast color so the date label stays legible.
          ctx.globalAlpha = 1;
          ctx.fillStyle = markerColor;
          ctx.textAlign = "left";
          ctx.textBaseline = "bottom";
          ctx.fillText(
            d.toLocaleDateString(undefined, { month: "short", day: "numeric" }),
            x + 3,
            area.bottom - 2,
          );
        }
        prevKey = key;
      }
      ctx.restore();
    },
  };
  return {
    type: "bar",
    plugins: [dayMarks],
    data: {
      labels: series.labels,
      datasets: [
        bar(m.overview_input_tokens_series(), series.cols.input, seriesColors.input),
        bar(m.overview_output_tokens_series(), series.cols.output, seriesColors.output),
        bar(m.overview_prompt_input_cached(), series.cols.prompt, seriesColors.prompt),
        bar(m.overview_locally_cached(), series.cols.local, seriesColors.local),
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      animation: { duration: 0 },
      interaction: { mode: "index", intersect: false },
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
            maxTicksLimit: 8,
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
      plugins: {
        legend: { display: false },
        tooltip: chartTooltip(colors, {
          title: (items) => {
            if (!items.length) {
              return "";
            }
            const ms = stamps[items[0].dataIndex];
            if (!ms) {
              return items[0].label;
            }
            const date = new Date(ms);
            // Day buckets start at local midnight, so the time is always
            // 00:00:00 — show the date alone on the Days view.
            return granularity === "days"
              ? date.toLocaleDateString()
              : date.toLocaleString();
          },
          label: (c) => c.dataset.label + ": " + formatValue(c.parsed.y),
          footer: (items) => {
            let total = 0;
            items.forEach((it) => {
              total += Number(it.parsed.y) || 0;
            });
            return m.overview_total_label({ total: formatValue(total) });
          },
        }),
      },
    },
  };
}
