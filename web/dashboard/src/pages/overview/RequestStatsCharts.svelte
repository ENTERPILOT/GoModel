<script>
  // Requests by Status + Provider Latency charts fed by /admin/request-logs/stats.
  import ChartCanvas from "$lib/components/molecules/ChartCanvas.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import { chartColors, resolveCssColor as resolveColor } from "$lib/utils/chartTheme.js";
  import { formatNumber } from "$lib/utils/format.js";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { requestStatsState } from "./overviewState.svelte.js";
  import {
    requestStatsHasData,
    requestLatencyHasData,
    requestStatsSuccessRateText,
    requestStatsSummaryCount,
    requestStatsAvgLatencyText,
    requestStatusChartConfig,
    requestLatencyChartConfig,
    createProviderColorPicker,
  } from "./requestStatsLogic.js";
  import * as m from "$lib/paraglide/messages.js";

  // Colors are handed out in first-seen order and kept while the dashboard
  // stays open, so a provider's line color is stable across refetches.
  const providerColor = createProviderColorPicker();

  const stats = $derived(requestStatsState.stats);
  let latencyHelpOpen = $state(false);

  function chartOptions() {
    return {
      interval: stats.interval,
      zone: timezone.effectiveTimezone(),
      resolve: resolveColor,
      formatTimestamp: (ts) => timezone.formatTimestamp(ts),
    };
  }
</script>


{#if requestStatsHasData(stats)}
  <div class="model-chart-section request-log-stats-section">
    <div class="model-chart-header request-log-stats-header">
      <h3>{m.overview_requests_by_status()}</h3>
      <div
        class="request-log-stats-kpis"
        role="group"
        aria-label={m.overview_request_status_summary()}
      >
        <span
          class="request-log-stats-kpi"
          title={m.overview_success_rate_help()}
        >
          <span class="request-log-stats-kpi-label">{m.overview_success()}</span>
          <span class="request-log-stats-kpi-value mono"
          >{requestStatsSuccessRateText(stats)}</span>
        </span>
        <span class="request-log-stats-kpi">
          <span class="request-log-stats-kpi-dot request-log-stats-dot-2xx" aria-hidden="true"
          ></span>
          <span class="request-log-stats-kpi-label">2xx</span>
          <span class="request-log-stats-kpi-value mono"
          >{formatNumber(requestStatsSummaryCount(stats, "status_2xx"))}</span>
        </span>
        <span class="request-log-stats-kpi">
          <span class="request-log-stats-kpi-dot request-log-stats-dot-4xx" aria-hidden="true"
          ></span>
          <span class="request-log-stats-kpi-label">4xx</span>
          <span class="request-log-stats-kpi-value mono"
          >{formatNumber(requestStatsSummaryCount(stats, "status_4xx"))}</span>
        </span>
        <span class="request-log-stats-kpi">
          <span class="request-log-stats-kpi-dot request-log-stats-dot-5xx" aria-hidden="true"
          ></span>
          <span class="request-log-stats-kpi-label">5xx</span>
          <span class="request-log-stats-kpi-value mono"
          >{formatNumber(requestStatsSummaryCount(stats, "status_5xx"))}</span>
        </span>
      </div>
    </div>
    <div class="bar-chart-wrap">
      <ChartCanvas
        build={() =>
          requestStatusChartConfig(chartColors(), stats.buckets, chartOptions())}
      />
    </div>

  </div>

  {#if requestLatencyHasData(stats)}
    <div class="model-chart-section request-log-stats-section">
      <div class="model-chart-header request-log-stats-header">
        <InlineHelpSection
          copyId="request-log-latency-help-copy"
          label={m.overview_provider_latency_help_label()}
          text={m.overview_provider_latency_help()}
        >
          {#snippet title()}<h3>{m.overview_provider_latency()}</h3>{/snippet}
        </InlineHelpSection>
        <div class="request-log-stats-kpis">
          <span
            class="request-log-stats-kpi"
            title={m.overview_provider_latency_average_help()}
          >
            <span class="request-log-stats-kpi-label">{m.overview_average_short()}</span>
            <span class="request-log-stats-kpi-value mono"
            >{requestStatsAvgLatencyText(stats)}</span>
          </span>
        </div>
      </div>
      <div class="bar-chart-wrap">
        <ChartCanvas
          build={() =>
            requestLatencyChartConfig(
              chartColors(),
              stats.buckets,
              stats.provider_latency,
              { ...chartOptions(), providerColor },
            )}
        />
      </div>
    </div>
  {/if}
{:else if requestStatsState.loading}
  <!-- First load: hold the section's place instead of popping it in. -->
  <div class="model-chart-section request-log-stats-section">
    <LoadingState label={m.overview_loading_request_stats()} />

  </div>
{/if}

<style>
  /* Request stats charts (Requests by Status / Provider Latency) */
  /* The Activity calendar above only spaces itself upward, so these sections
     bring their own top margin; between the two charts it collapses with the
     24px bottom margin into a single gap. */

  .request-log-stats-section {
    margin-top: 24px;
  }


  .request-log-stats-header {
    align-items: flex-start;
  }

  .request-log-stats-kpis {

    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 16px;
  }

  .request-log-stats-kpi {
    display: inline-flex;

    align-items: center;
    gap: 8px;
    font-size: 12px;
    color: var(--text-muted);

    white-space: nowrap;
  }

  .request-log-stats-kpi-value {
    color: var(--text);
    font-size: 13px;
  }

  .request-log-stats-kpi-dot {

    width: 8px;
    height: 8px;
    border-radius: var(--radius-control);

    flex-shrink: 0;
  }

  .request-log-stats-dot-2xx {
    background: var(--success);
  }

  .request-log-stats-dot-4xx {
    background: var(--warning);
  }

  .request-log-stats-dot-5xx {
    background: var(--danger);
  }
</style>
