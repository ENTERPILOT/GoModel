<script>
  // Daily/weekly/monthly/yearly token usage chart with the interval picker.
  import ChartCanvas from "$lib/components/molecules/ChartCanvas.svelte";
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import SegmentedControl from "$lib/components/atoms/SegmentedControl.svelte";
  import { chartColors } from "$lib/utils/chartTheme.js";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { dateRange } from "$lib/stores/dateRange.svelte.js";
  import { usageData } from "$lib/stores/usageData.svelte.js";
  import {
    fillMissingDays,
    buildOverviewSeries,
    overviewChartConfig,
  } from "./overviewChartLogic.js";
  import { resolveColor } from "./chartStyle.js";

  // onintervalchange: refetch hook fired after the interval switches.
  let { onintervalchange } = $props();

  const INTERVALS = ["daily", "weekly", "monthly", "yearly"];

  function setInterval(val) {
    dateRange.interval = val;
    onintervalchange?.();
  }

  function buildChart() {
    const daily = usageData.daily;
    if (daily.length === 0) return null;
    const start = dateRange.rangeStart();
    const end = dateRange.rangeEnd();
    const filled = fillMissingDays(daily, dateRange.interval, start, end);
    const cacheDaily = fillMissingDays(
      Array.isArray(usageData.cacheOverview.daily)
        ? usageData.cacheOverview.daily
        : [],
      dateRange.interval,
      start,
      end,
    );
    const series = buildOverviewSeries(filled, cacheDaily);
    return overviewChartConfig(chartColors(), series, {
      cacheEnabled: usageData.cacheAnalyticsEnabled(),
      resolve: resolveColor,
    });
  }
</script>

<div class="chart-container">
  <div class="chart-container-header">
    <h3>{dateRange.chartTitle()}</h3>
    <SegmentedControl
      ariaLabel="Usage chart interval"
      options={INTERVALS.map((val) => ({
        value: val,
        label: val.charAt(0).toUpperCase() + val.slice(1),
      }))}
      value={dateRange.interval}
      onchange={setInterval}
    />
  </div>
  <div class="chart-wrapper">
    <ChartCanvas build={buildChart} />
    {#if usageData.daily.length === 0 && usageData.loading}
      <div class="chart-empty-overlay">
        <Spinner size={24} label="Loading usage" />
      </div>
    {:else if usageData.daily.length === 0 && !auth.authError}
      <div class="chart-empty-overlay">
        <svg
          class="empty-state-icon"
          viewBox="0 0 220 262"
          fill="none"
          role="img"
          aria-label="No data"
          xmlns="http://www.w3.org/2000/svg"
        >
          <!-- GoModel hexagon mark: sharp mitered edges like the logotype -->
          <path
            d="M110 20 L187.9 65 L187.9 155 L110 200 L32.1 155 L32.1 65 Z"
            stroke="var(--accent)"
            stroke-width="5"
            stroke-linejoin="miter"
            opacity="0.5"
          ></path>
          <!-- faint chart gridlines -->
          <path
            d="M60 123 H160"
            stroke="currentColor"
            stroke-width="2"
            stroke-dasharray="1.5 7"
            stroke-linecap="round"
            opacity="0.18"
          ></path>
          <path
            d="M60 93 H160"
            stroke="currentColor"
            stroke-width="2"
            stroke-dasharray="1.5 7"
            stroke-linecap="round"
            opacity="0.18"
          ></path>
          <!-- empty chart baseline (the floor), level with the hexagon's lower shoulders -->
          <path
            d="M50 155 H170"
            stroke="currentColor"
            stroke-width="5"
            stroke-linecap="round"
            opacity="0.6"
          ></path>
          <!-- ghosted placeholder bars, floating just above the floor -->
          <rect x="57" y="113" width="14" height="36" rx="4" stroke="currentColor" stroke-width="2.8" stroke-dasharray="5 4" opacity="0.55"></rect>
          <rect x="80" y="91" width="14" height="58" rx="4" stroke="currentColor" stroke-width="2.8" stroke-dasharray="5 4" opacity="0.55"></rect>
          <rect x="103" y="75" width="14" height="74" rx="4" stroke="currentColor" stroke-width="2.8" stroke-dasharray="5 4" opacity="0.55"></rect>
          <rect x="126" y="97" width="14" height="52" rx="4" stroke="currentColor" stroke-width="2.8" stroke-dasharray="5 4" opacity="0.55"></rect>
          <rect x="149" y="119" width="14" height="30" rx="4" stroke="currentColor" stroke-width="2.8" stroke-dasharray="5 4" opacity="0.55"></rect>
          <!-- label -->
          <text x="110" y="250" text-anchor="middle" fill="currentColor" font-size="40">No data</text>
        </svg>
      </div>
    {/if}
  </div>
</div>
