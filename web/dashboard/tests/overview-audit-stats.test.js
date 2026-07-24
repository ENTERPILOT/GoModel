// Ported from the legacy audit-stats.test.cjs (pure-logic cases; fetch
// plumbing now lives in overviewState.svelte.js and is exercised via the
// normalization helpers here).
import test from "node:test";
import assert from "node:assert/strict";

import {
  emptyAuditStats,
  normalizeAuditStats,
  auditStatsHasData,
  auditLatencyHasData,
  auditStatsSuccessRateText,
  formatDurationMs,
  auditStatsAvgLatencyText,
  auditStatsBucketLabel,
  auditStatsTooltipTitle,
  auditStatusChartConfig,
  auditLatencyChartConfig,
  createProviderColorPicker,
} from "../src/pages/overview/auditStatsLogic.js";

const colors = {
  grid: "#111",
  text: "#222",
  tooltipBg: "#333",
  tooltipBorder: "#444",
  tooltipText: "#555",
};

test("normalizeAuditStats tolerates malformed payloads", () => {
  const empty = normalizeAuditStats(null);
  assert.equal(empty.interval, "day");
  assert.equal(empty.buckets.length, 0);
  assert.equal(empty.provider_latency.length, 0);

  const normalized = normalizeAuditStats({
    interval: "hour",
    buckets: [{ start: "2026-01-16T10:00:00Z", requests: 3 }],
    summary: { requests: 3 },
    provider_latency: "nope",
  });
  assert.equal(normalized.interval, "hour");
  assert.equal(normalized.buckets.length, 1);
  assert.equal(normalized.provider_latency.length, 0);
});

test("auditStatsHasData follows the summary request count", () => {
  assert.equal(auditStatsHasData(emptyAuditStats()), false);
  assert.equal(auditStatsHasData({ summary: { requests: 12 } }), true);
  assert.equal(auditLatencyHasData({ provider_latency: [] }), false);
  assert.equal(auditLatencyHasData({ provider_latency: [{ provider: "openai" }] }), true);
});

test("auditStatsSuccessRateText formats the ratio and its absence", () => {
  assert.equal(auditStatsSuccessRateText(emptyAuditStats()), "—");
  assert.equal(
    auditStatsSuccessRateText({ summary: { requests: 4, success_rate: 0.9944 } }),
    "99.4%",
  );
});

test("formatDurationMs scales through ms, s, and min", () => {
  assert.equal(formatDurationMs(812.4), "812 ms");
  assert.equal(formatDurationMs(2350), "2.35 s");
  assert.equal(formatDurationMs(90000), "1.5 min");
  assert.equal(formatDurationMs("nope"), "-");
  assert.equal(auditStatsAvgLatencyText({ summary: { avg_duration_ms: 2350 } }), "2.35 s");
  assert.equal(auditStatsAvgLatencyText({ summary: {} }), "—");
});

test("status chart stacks 2xx/4xx/5xx and adds Other only when present", () => {
  const stats = normalizeAuditStats({
    interval: "day",
    buckets: [
      { start: "2026-01-16T00:00:00Z", requests: 5, status_2xx: 3, status_4xx: 1, status_5xx: 1, status_other: 0 },
      { start: "2026-01-17T00:00:00Z", requests: 2, status_2xx: 2, status_4xx: 0, status_5xx: 0, status_other: 0 },
    ],
    summary: { requests: 7 },
  });

  const config = auditStatusChartConfig(colors, stats.buckets, {
    interval: stats.interval,
    zone: "UTC",
  });
  assert.equal(config.type, "bar");
  assert.equal(config.data.datasets.length, 3);
  assert.deepEqual(config.data.datasets.map((d) => d.label), ["2xx", "4xx", "5xx"]);
  assert.deepEqual(config.data.datasets[0].data, [3, 2]);
  assert.equal(config.options.scales.x.stacked, true);
  assert.equal(config.options.scales.y.stacked, true);

  stats.buckets[0].status_other = 2;
  const withOther = auditStatusChartConfig(colors, stats.buckets, {
    interval: stats.interval,
    zone: "UTC",
  });
  assert.equal(withOther.data.datasets.length, 4);
  assert.equal(withOther.data.datasets[3].label, "Other");
});

test("latency chart keeps nil buckets as gaps and colors by provider identity", () => {
  const stats = normalizeAuditStats({
    interval: "hour",
    buckets: [{ start: "2026-01-16T10:00:00Z" }, { start: "2026-01-16T11:00:00Z" }],
    summary: { requests: 3 },
    provider_latency: [
      { provider: "openai", requests: [2, 0], avg_duration_ms: [200.5, null] },
      { provider: "anthropic", requests: [1, 1], avg_duration_ms: [400, 410] },
    ],
  });

  const providerColor = createProviderColorPicker(["#c2845a", "#7a9e7e", "#d4a574"]);
  const config = auditLatencyChartConfig(colors, stats.buckets, stats.provider_latency, {
    interval: stats.interval,
    zone: "UTC",
    providerColor,
  });
  assert.equal(config.type, "line");
  assert.equal(config.data.datasets.length, 2);
  assert.deepEqual(config.data.datasets[0].data, [200.5, null]);
  // Distinct palette colors in first-seen order, stable across re-renders.
  assert.equal(config.data.datasets[0].borderColor, "#c2845a");
  assert.equal(config.data.datasets[1].borderColor, "#7a9e7e");
  assert.equal(providerColor("openai"), "#c2845a");
  // Hourly interval bridges a single quiet bucket.
  assert.equal(config.data.datasets[0].spanGaps, 2);
});

test("hourly labels mark midnight with the short date", () => {
  assert.equal(
    auditStatsBucketLabel({ start: "2026-01-16T00:00:00Z" }, "hour", "UTC"),
    "Jan 16",
  );
  assert.equal(
    auditStatsBucketLabel({ start: "2026-01-16T14:00:00Z" }, "hour", "UTC"),
    "14:00",
  );
  assert.equal(
    auditStatsBucketLabel({ start: "2026-01-16T14:00:00Z" }, "day", "UTC"),
    "Jan 16",
  );
});

test("labels follow the dashboard effective timezone, not the browser locale", () => {
  // Midnight UTC is 09:00 in Tokyo — an hourly bucket must label the hour in
  // the dashboard's timezone, and the day flip must follow it too.
  assert.equal(
    auditStatsBucketLabel({ start: "2026-01-16T00:00:00Z" }, "hour", "Asia/Tokyo"),
    "09:00",
  );
  assert.equal(
    auditStatsBucketLabel({ start: "2026-01-15T15:00:00Z" }, "hour", "Asia/Tokyo"),
    "Jan 16",
  );
  assert.equal(
    auditStatsBucketLabel({ start: "2026-01-16T00:00:00Z" }, "hour", "UTC"),
    "Jan 16",
  );
  assert.equal(
    auditStatsTooltipTitle({ start: "2026-01-16T00:00:00Z" }, "day", "UTC"),
    "Jan 16, 2026",
  );
  // Hourly tooltips defer to the timestamp formatter.
  assert.equal(
    auditStatsTooltipTitle(
      { start: "2026-01-16T10:00:00Z" },
      "hour",
      "UTC",
      (ts) => "ts:" + ts,
    ),
    "ts:2026-01-16T10:00:00Z",
  );
});
