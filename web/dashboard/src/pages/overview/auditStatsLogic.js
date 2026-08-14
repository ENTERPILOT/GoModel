// Audit stats (Requests by Status + Provider Latency) logic. Pure functions
// so bucketing, labels, and chart configs are testable with node.

import { barColors, chartTickFont, chartTooltip } from "../../lib/utils/chartTheme.js";
import * as m from "../../lib/paraglide/messages.js";
import { getLocale } from "../../lib/paraglide/runtime.js";
import { formatNumber } from "../../lib/i18n/locale.js";
import { formatTokensShort } from "../../lib/utils/format.js";

export function emptyAuditStats() {
  return {
    interval: "day",
    buckets: [],
    summary: { requests: 0 },
    provider_latency: [],
  };
}

export function normalizeAuditStats(payload) {
  const stats = payload && typeof payload === "object" ? payload : {};
  return {
    interval: stats.interval === "hour" ? "hour" : "day",
    buckets: Array.isArray(stats.buckets) ? stats.buckets : [],
    summary:
      stats.summary && typeof stats.summary === "object"
        ? stats.summary
        : { requests: 0 },
    provider_latency: Array.isArray(stats.provider_latency)
      ? stats.provider_latency
      : [],
  };
}

export function auditStatsHasData(stats) {
  return Number((stats && stats.summary && stats.summary.requests) || 0) > 0;
}

export function auditLatencyHasData(stats) {
  return (
    (stats && Array.isArray(stats.provider_latency)
      ? stats.provider_latency
      : []
    ).length > 0
  );
}

export function auditStatsSuccessRateText(stats) {
  const rate = stats && stats.summary ? stats.summary.success_rate : null;
  if (rate === null || rate === undefined) return "—";
  return (Math.round(Number(rate) * 1000) / 10).toFixed(1) + "%";
}

export function auditStatsSummaryCount(stats, key) {
  return Number((stats && stats.summary && stats.summary[key]) || 0);
}

export function formatDurationMs(ms) {
  const v = Number(ms);
  if (!Number.isFinite(v)) return "-";
  if (v >= 60000) return (v / 60000).toFixed(1) + " min";
  if (v >= 1000) return (v / 1000).toFixed(2) + " s";
  return Math.round(v) + " ms";
}

export function auditStatsAvgLatencyText(stats) {
  const avg = stats && stats.summary ? stats.summary.avg_duration_ms : null;
  if (avg === null || avg === undefined) return "—";
  return formatDurationMs(Number(avg));
}

// Date parts in the dashboard's effective timezone: the server buckets by the
// same X-GoModel-Timezone the dashboard sends, so labels must not drift to
// the browser's locale when the two timezones differ.
function auditStatsDateParts(d, zone) {
  try {
    const byType = {};
    const locale = getLocale();
    new Intl.DateTimeFormat(locale, {
      timeZone: zone,
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      hourCycle: "h23",
    })
      .formatToParts(d)
      .forEach((part) => {
        byType[part.type] = part.value;
      });
    return {
      year: byType.year,
      month: byType.month,
      day: byType.day,
      hour: Number(byType.hour),
      shortDate: new Intl.DateTimeFormat(locale, {
        timeZone: zone,
        month: "short",
        day: "numeric",
      }).format(d),
      fullDate: new Intl.DateTimeFormat(locale, {
        timeZone: zone,
        year: "numeric",
        month: "short",
        day: "numeric",
      }).format(d),
    };
  } catch {
    const month = new Intl.DateTimeFormat(getLocale(), { month: "short" }).format(d);
    return {
      year: String(d.getFullYear()),
      month,
      day: String(d.getDate()),
      hour: d.getHours(),
      shortDate: month + " " + d.getDate(),
      fullDate: month + " " + d.getDate() + ", " + d.getFullYear(),
    };
  }
}

// Axis labels: hourly buckets show the hour and mark midnight with the short
// date; daily buckets show the short date.
export function auditStatsBucketLabel(bucket, interval, zone) {
  const d = new Date(bucket.start);
  if (Number.isNaN(d.getTime())) return String(bucket.start || "");
  const parts = auditStatsDateParts(d, zone);
  const day = parts.shortDate;
  if (interval !== "hour") return day;
  if (parts.hour === 0) return day;
  return String(parts.hour).padStart(2, "0") + ":00";
}

export function auditStatsTooltipTitle(bucket, interval, zone, formatTimestamp) {
  const d = new Date(bucket.start);
  if (Number.isNaN(d.getTime())) return String(bucket.start || "");
  if (interval === "hour") return formatTimestamp(bucket.start);
  const parts = auditStatsDateParts(d, zone);
  return parts.fullDate;
}

// Charts resolve the dashboard's status tokens (success/warning/danger) so
// the stacked bars match the status badges in the request log, in both themes.
function auditStatsColors(resolve) {
  return {
    ok: resolve("var(--success)"),
    clientError: resolve("var(--warning)"),
    serverError: resolve("var(--danger)"),
    other: resolve("color-mix(in srgb, var(--text-muted) 55%, transparent)"),
  };
}

export function auditStatusChartConfig(colors, buckets, options = {}) {
  const interval = options.interval === "hour" ? "hour" : "day";
  const zone = options.zone;
  const resolve = options.resolve || ((expr) => expr);
  const formatTimestamp = options.formatTimestamp || ((ts) => String(ts));
  const labels = buckets.map((b) => auditStatsBucketLabel(b, interval, zone));
  const statusColors = auditStatsColors(resolve);
  const surface = resolve("var(--bg-surface)");
  const num = (v) => Number(v) || 0;
  // Surface-colored borders read as gaps where stacked segments touch,
  // keeping the status classes separable without relying on hue alone.
  const bar = (label, data, color) => ({
    label: label,
    data: data,
    backgroundColor: color,
    borderColor: surface,
    borderWidth: 1,
    borderSkipped: false,
    borderRadius: 2,
    maxBarThickness: 28,
  });
  const datasets = [
    bar("2xx", buckets.map((b) => num(b.status_2xx)), statusColors.ok),
    bar("4xx", buckets.map((b) => num(b.status_4xx)), statusColors.clientError),
    bar("5xx", buckets.map((b) => num(b.status_5xx)), statusColors.serverError),
  ];
  if (buckets.some((b) => num(b.status_other) > 0)) {
    datasets.push(
      bar(m.overview_other(), buckets.map((b) => num(b.status_other)), statusColors.other),
    );
  }
  return {
    type: "bar",
    data: { labels: labels, datasets: datasets },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      animation: { duration: 0 },
      interaction: { mode: "index", intersect: false },
      plugins: {
        legend: { labels: { color: colors.text, font: { size: 12 } } },
        tooltip: chartTooltip(colors, {
          title: (items) =>
            items.length
              ? auditStatsTooltipTitle(
                  buckets[items[0].dataIndex],
                  interval,
                  zone,
                  formatTimestamp,
                )
              : "",
          label: (c) => c.dataset.label + ": " + formatNumber(c.parsed.y),
          footer: (items) => {
            let total = 0;
            items.forEach((it) => {
              total += Number(it.parsed.y) || 0;
            });
            return m.overview_total_label({ total: formatNumber(total) });
          },
        }),
      },
      scales: {
        x: {
          stacked: true,
          grid: { display: false },
          border: { display: false },
          ticks: {
            color: colors.text,
            font: chartTickFont(),
            maxRotation: 0,
            autoSkip: true,
            maxTicksLimit: 12,
          },
        },
        y: {
          stacked: true,
          beginAtZero: true,
          grid: { color: colors.grid },
          border: { display: false },
          ticks: {
            color: colors.text,
            font: chartTickFont(),
            precision: 0,
            callback: (v) => formatTokensShort(v),
          },
        },
      },
    },
  };
}

// Distinct categorical colors handed out in first-seen order, so a provider
// keeps its color while the dashboard stays open and near-identical hues
// (which hashing can produce) never end up as neighboring lines.
export function createProviderColorPicker(palette = barColors()) {
  const assigned = {};
  return function providerColor(provider) {
    if (!(provider in assigned)) {
      assigned[provider] = palette[Object.keys(assigned).length % palette.length];
    }
    return assigned[provider];
  };
}

export function auditLatencyChartConfig(colors, buckets, series, options = {}) {
  const interval = options.interval === "hour" ? "hour" : "day";
  const zone = options.zone;
  const formatTimestamp = options.formatTimestamp || ((ts) => String(ts));
  const providerColor = options.providerColor || createProviderColorPicker();
  const labels = buckets.map((b) => auditStatsBucketLabel(b, interval, zone));
  const datasets = series.map((s) => ({
    label: s.provider,
    data: (s.avg_duration_ms || []).map((v) =>
      v === null || v === undefined ? null : Number(v),
    ),
    borderColor: providerColor(s.provider),
    backgroundColor: providerColor(s.provider),
    fill: false,
    tension: 0.3,
    borderWidth: 2,
    pointRadius: 0,
    pointHoverRadius: 4,
    // Bridge a single quiet bucket so one idle hour doesn't cut the line, but
    // keep longer outages visible as gaps. The category axis measures gaps in
    // bucket indices.
    spanGaps: interval === "hour" ? 2 : false,
  }));
  return {
    type: "line",
    data: { labels: labels, datasets: datasets },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      animation: { duration: 0 },
      interaction: { mode: "index", intersect: false },
      plugins: {
        legend: { labels: { color: colors.text, font: { size: 12 } } },
        tooltip: chartTooltip(colors, {
          title: (items) =>
            items.length
              ? auditStatsTooltipTitle(
                  buckets[items[0].dataIndex],
                  interval,
                  zone,
                  formatTimestamp,
                )
              : "",
          label: (c) => {
            const requests = (
              (series[c.datasetIndex] && series[c.datasetIndex].requests) || []
            )[c.dataIndex];
            const count = Number(requests) || 0;
            return (
              c.dataset.label +
              ": " +
              formatDurationMs(c.parsed.y) +
              (count > 0 ? " (" + count.toLocaleString() + " req)" : "")
            );
          },
        }),
      },
      scales: {
        x: {
          grid: { color: colors.grid },
          border: { display: false },
          ticks: {
            color: colors.text,
            font: chartTickFont(),
            maxRotation: 0,
            autoSkip: true,
            maxTicksLimit: 12,
          },
        },
        y: {
          beginAtZero: true,
          grid: { color: colors.grid },
          border: { display: false },
          ticks: {
            color: colors.text,
            font: chartTickFont(),
            callback: (v) => formatDurationMs(v),
          },
        },
      },
    },
  };
}
