<script>
  // Overview summary cards: token totals, requests, cache hits, cost, the
  // prompt-cache gauge, and the provider/MCP status flags.
  import ChartCanvas from "$lib/components/molecules/ChartCanvas.svelte";
  import { router } from "$lib/stores/router.svelte.ts";
  import { usageData } from "$lib/stores/usageData.svelte.ts";
  import {
    formatNumber,
    formatCost,
    formatTokensShort,
    tokenCountTitle,
  } from "$lib/utils/format.ts";
  import {
    summaryTotalTokens,
    summaryTotalRequests,
    summaryTotalRequestsTitle,
    cacheOverviewTotalTokens,
  } from "./cacheMeterLogic.js";
  import {
    promptCacheRate,
    promptCacheRateText,
    promptCacheGaugeConfig,
  } from "./overviewChartLogic.js";
  import {
    providerStatusSummaryClass,
    providerStatusRatioText,
    providerStatusHasIssues,
    providerStatusSummaryText,
  } from "./providersLogic.js";
  import {
    mcpOverviewVisible,
    mcpOverviewRatioText,
    mcpOverviewSummaryClass,
    mcpOverviewSummaryText,
  } from "./mcpOverviewLogic.js";
  import { providerStatusState, mcpServersState } from "./overviewState.svelte.js";
  import { resolveCssColor as resolveColor } from "$lib/utils/chartTheme.ts";

  const summary = $derived(usageData.summary);
  const cacheOverview = $derived(usageData.cacheOverview);
  const cacheEnabled = $derived(usageData.cacheAnalyticsEnabled());
  const providerSummary = $derived(providerStatusState.status.summary);

  function scrollToProviderStatusSection() {
    const section = document.getElementById("provider-status-section");
    if (!section) return;
    section.scrollIntoView({ behavior: "smooth", block: "start" });
    section.focus({ preventScroll: true });
  }
</script>

{#snippet tokenCard(label, inputTokens, outputTokens, totalTokens)}
  <div class="card card-wide">
    <div class="card-label">{label}</div>
    <div class="card-value cache-token-value">
      <span
        class="cache-token-part"
        title={tokenCountTitle("Input tokens", inputTokens)}
      >
        <span>{formatTokensShort(inputTokens)}</span><span
          class="cache-token-marker">i</span>
      </span>
      <span class="cache-token-operator">+</span>
      <span
        class="cache-token-part"
        title={tokenCountTitle("Output tokens", outputTokens)}
      >
        <span>{formatTokensShort(outputTokens)}</span><span
          class="cache-token-marker">o</span>
      </span>
      <span class="cache-token-operator">=</span>
      <span
        class="cache-token-part"
        title={tokenCountTitle("Total tokens", totalTokens)}
      >{formatTokensShort(totalTokens)}</span>
    </div>
  </div>
{/snippet}

{#snippet statusCard(flagClass, statusClass, label, ratio, text, link)}
  <div class="card provider-status-flag {flagClass} {statusClass}">
    <div class="card-label">{label}</div>
    <div class="card-value provider-status-value">
      {ratio}
    </div>
    {#if link}
      <button
        type="button"
        class="provider-status-card-link"
        aria-label={link.title}
        title={link.title}
        onclick={link.onclick}
      >{text}</button>
    {:else}
      <span class="provider-status-card-note">{text}</span>
    {/if}
  </div>
{/snippet}

<div class="cards">
  {@render tokenCard(
    "Tokens",
    summary.total_input_tokens,
    summary.total_output_tokens,
    summaryTotalTokens(summary),
  )}
  <div class="card">
    <div class="card-label">Total Requests</div>
    <div
      class="card-value"
      title={summaryTotalRequestsTitle(summary, cacheOverview, cacheEnabled)}
    >{formatNumber(summaryTotalRequests(summary, cacheOverview, cacheEnabled))}</div>
  </div>
  {#if cacheEnabled}
    <div class="card">
      <div class="card-label">Cache Hits</div>
      <div class="card-value">{formatNumber(cacheOverview.summary.total_hits)}</div>
    </div>
  {/if}
  <div class="card">
    <div class="card-label">Estimated Cost</div>
    <div class="card-value">{formatCost(summary.total_cost)}</div>
  </div>
  {#if cacheEnabled}
    {@render tokenCard(
      "Local Cache",
      cacheOverview.summary.total_input_tokens,
      cacheOverview.summary.total_output_tokens,
      cacheOverviewTotalTokens(cacheOverview),
    )}
  {/if}
  <div
    class="card prompt-cache-card"
    title="Share of input tokens served from the provider prompt cache, over the selected period"
  >
    <div class="card-label">Prompt Cache Rate</div>
    <div
      class="prompt-cache-gauge"
      role="img"
      aria-label={"Prompt cache rate " + promptCacheRateText(summary)}
    >
      <ChartCanvas
        build={() =>
          promptCacheGaugeConfig(
            promptCacheRate(summary),
            resolveColor("var(--token-prompt)"),
            resolveColor("var(--bg-surface-hover)"),
          )}
      />
      <span class="prompt-cache-gauge-value">{promptCacheRateText(summary)}</span>
    </div>
  </div>
  {#if providerSummary.total > 0}
    {@render statusCard(
      "provider-status-overview-card",
      providerStatusSummaryClass(providerSummary),
      "Provider Status",
      providerStatusRatioText(providerSummary),
      providerStatusSummaryText(providerSummary),
      providerStatusHasIssues(providerSummary)
        ? { title: "View providers overview", onclick: scrollToProviderStatusSection }
        : null,
    )}
  {/if}
  {#if mcpOverviewVisible(mcpServersState.available, mcpServersState.servers)}
    {@render statusCard(
      "mcp-servers-flag",
      mcpOverviewSummaryClass(mcpServersState.servers),
      "MCP Servers",
      mcpOverviewRatioText(mcpServersState.servers),
      mcpOverviewSummaryText(mcpServersState.servers),
      { title: "View MCP servers", onclick: () => router.navigate("mcp-servers") },
    )}
  {/if}
</div>

<style>
  .provider-status-flag {
    grid-column: span 2;
  }

  .provider-status-overview-card {
    grid-column: span 1;
  }

  .provider-status-flag.is-healthy {
    border-color: color-mix(in srgb, var(--success) 45%, var(--border));
    background: color-mix(in srgb, var(--success) 10%, var(--bg-surface));
  }

  .provider-status-flag.is-degraded {
    border-color: color-mix(in srgb, var(--warning) 48%, var(--border));
    background: color-mix(in srgb, var(--warning) 26%, var(--bg-surface));
  }

  .provider-status-flag.is-unhealthy {
    border-color: color-mix(in srgb, var(--danger) 45%, var(--border));
    background: color-mix(in srgb, var(--danger) 10%, var(--bg-surface));
  }

  .provider-status-value {
    margin-bottom: 8px;
  }

  .provider-status-card-link {
    margin: 0;
    padding: 0;
    background: transparent;
    border: 0;
    color: var(--accent-strong, var(--accent));
    font: inherit;
    font-size: 13px;
    font-weight: 600;
    text-align: left;
    cursor: pointer;
  }

  .provider-status-card-link:hover {
    color: var(--text);
    text-decoration: underline;
  }

  .provider-status-card-link:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--accent) 32%, transparent);
    outline-offset: 3px;
    border-radius: 4px;
  }

  .provider-status-card-note {
    display: block;
    font-size: 13px;
    color: var(--text-muted);
  }

  @media (min-width: 720px) {
    .card-wide {
        grid-column: span 2;
      }
  }

  .cache-token-value {
    align-items: center;
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    line-height: 1;
  }

  .cache-token-part {
    align-items: center;
    display: inline-flex;
  }

  .cache-token-operator {
    color: var(--text-muted);
    font-size: 24px;
    font-weight: 600;
    letter-spacing: 0;
    line-height: 1;
  }

  .cache-token-marker {
    color: var(--text-muted);
    font-size: 14px;
    font-weight: 700;
    letter-spacing: 0;
    margin-left: 2px;
    text-transform: uppercase;
  }

  /* Prompt cache rate gauge (compact half-circle in a stat card). A 180° doughnut's
     natural box is 2:1, so the container is 2:1 and Chart.js sizes the canvas to it
     — the semicircle fills it exactly and stays round. */
  .prompt-cache-gauge {
    position: relative;
    width: 120px;
    height: 60px;
    margin: 8px auto 0;
    overflow: hidden;
  }

  .prompt-cache-gauge :global(canvas) {
    display: block;
  }

  .prompt-cache-gauge-value {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 4px;
    text-align: center;
    font-size: 18px;
    font-weight: 700;
    line-height: 1;
    color: var(--text);
  }

  @media (max-width: 768px) {
    .provider-status-flag {
        grid-column: span 1;
      }
  }

  /* MCP overview card: reuses the provider status flag accents (is-healthy /
     is-degraded) but stays a single-column card. */
  .mcp-servers-flag {
    grid-column: span 1;
  }
</style>
