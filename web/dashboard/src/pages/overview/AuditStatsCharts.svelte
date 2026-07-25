<script>
  // Requests by Status + Provider Latency charts fed by /admin/audit/stats.
  import ChartCanvas from "$lib/components/molecules/ChartCanvas.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import { chartColors, resolveCssColor as resolveColor } from "$lib/utils/chartTheme.js";
  import { formatNumber } from "$lib/utils/format.js";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { auditStatsState } from "./overviewState.svelte.js";
  import {
    auditStatsHasData,
    auditLatencyHasData,
    auditStatsSuccessRateText,
    auditStatsSummaryCount,
    auditStatsAvgLatencyText,
    auditStatusChartConfig,
    auditLatencyChartConfig,
    createProviderColorPicker,
  } from "./auditStatsLogic.js";

  // Colors are handed out in first-seen order and kept while the dashboard
  // stays open, so a provider's line color is stable across refetches.
  const providerColor = createProviderColorPicker();

  const stats = $derived(auditStatsState.stats);
  let latencyHelpOpen = $state(false);

  const LATENCY_HELP_TEXT =
    "Average duration of successful requests as measured at the gateway, per provider. Local cache hits and failed requests are excluded; streamed responses count until the stream completes.";

  function chartOptions() {
    return {
      interval: stats.interval,
      zone: timezone.effectiveTimezone(),
      resolve: resolveColor,
      formatTimestamp: (ts) => timezone.formatTimestamp(ts),
    };
  }
</script>

{#if auditStatsHasData(stats)}
  <div class="model-chart-section audit-stats-section">
    <div class="model-chart-header audit-stats-header">
      <h3>Requests by Status</h3>
      <div class="audit-stats-kpis" role="group" aria-label="Request status summary">
        <span
          class="audit-stats-kpi"
          title="Share of requests answered with a 2xx status"
        >
          <span class="audit-stats-kpi-label">Success</span>
          <span class="audit-stats-kpi-value mono"
          >{auditStatsSuccessRateText(stats)}</span>
        </span>
        <span class="audit-stats-kpi">
          <span class="audit-stats-kpi-dot audit-stats-dot-2xx" aria-hidden="true"
          ></span>
          <span class="audit-stats-kpi-label">2xx</span>
          <span class="audit-stats-kpi-value mono"
          >{formatNumber(auditStatsSummaryCount(stats, "status_2xx"))}</span>
        </span>
        <span class="audit-stats-kpi">
          <span class="audit-stats-kpi-dot audit-stats-dot-4xx" aria-hidden="true"
          ></span>
          <span class="audit-stats-kpi-label">4xx</span>
          <span class="audit-stats-kpi-value mono"
          >{formatNumber(auditStatsSummaryCount(stats, "status_4xx"))}</span>
        </span>
        <span class="audit-stats-kpi">
          <span class="audit-stats-kpi-dot audit-stats-dot-5xx" aria-hidden="true"
          ></span>
          <span class="audit-stats-kpi-label">5xx</span>
          <span class="audit-stats-kpi-value mono"
          >{formatNumber(auditStatsSummaryCount(stats, "status_5xx"))}</span>
        </span>
      </div>
    </div>
    <div class="bar-chart-wrap">
      <ChartCanvas
        build={() =>
          auditStatusChartConfig(chartColors(), stats.buckets, chartOptions())}
      />
    </div>
  </div>

  {#if auditLatencyHasData(stats)}
    <div class="model-chart-section audit-stats-section">
      <div class="model-chart-header audit-stats-header">
        <InlineHelpSection
          copyId="audit-latency-help-copy"
          label="provider latency help"
          text={LATENCY_HELP_TEXT}
        >
          {#snippet title()}<h3>Provider Latency</h3>{/snippet}
        </InlineHelpSection>
        <div class="audit-stats-kpis">
          <span
            class="audit-stats-kpi"
            title="Average duration of successful, uncached requests across all providers"
          >
            <span class="audit-stats-kpi-label">Avg</span>
            <span class="audit-stats-kpi-value mono"
            >{auditStatsAvgLatencyText(stats)}</span>
          </span>
        </div>
      </div>
      <div class="bar-chart-wrap">
        <ChartCanvas
          build={() =>
            auditLatencyChartConfig(
              chartColors(),
              stats.buckets,
              stats.provider_latency,
              { ...chartOptions(), providerColor },
            )}
        />
      </div>
    </div>
  {/if}
{/if}

<style>
  /* Audit stats charts (Requests by Status / Provider Latency) */
  /* The Activity calendar above only spaces itself upward, so these sections
     bring their own top margin; between the two charts it collapses with the
     24px bottom margin into a single gap. */
  .audit-stats-section {
    margin-top: 24px;
  }

  .audit-stats-header {
    align-items: flex-start;
  }

  .audit-stats-kpis {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 14px;
  }

  .audit-stats-kpi {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    color: var(--text-muted);
    white-space: nowrap;
  }

  .audit-stats-kpi-value {
    color: var(--text);
    font-size: 13px;
  }

  .audit-stats-kpi-dot {
    width: 8px;
    height: 8px;
    border-radius: 2px;
    flex-shrink: 0;
  }

  .audit-stats-dot-2xx {
    background: var(--success);
  }

  .audit-stats-dot-4xx {
    background: var(--warning);
  }

  .audit-stats-dot-5xx {
    background: var(--danger);
  }
</style>
