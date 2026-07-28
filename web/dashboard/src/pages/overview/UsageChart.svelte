<script>
  // Daily/weekly/monthly/yearly token usage chart with the interval picker.
  import ChartCanvas from "$lib/components/molecules/ChartCanvas.svelte";
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import NoDataIllustration from "$lib/components/atoms/NoDataIllustration.svelte";
  import SegmentedControl from "$lib/components/atoms/SegmentedControl.svelte";
  import { chartColors, resolveCssColor as resolveColor } from "$lib/utils/chartTheme.ts";
  import { auth } from "$lib/stores/auth.svelte.ts";
  import { dateRange } from "$lib/stores/dateRange.svelte.ts";
  import { usageData } from "$lib/stores/usageData.svelte.ts";
  import {
    fillMissingDays,
    buildOverviewSeries,
    overviewChartConfig,
  } from "./overviewChartLogic.js";

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
        <NoDataIllustration />
      </div>
    {/if}
  </div>
</div>
