<script>
  // Overview summary cards: token totals, requests, cache hits, cost, the
  // prompt-cache gauge, and the provider/MCP status flags.
  import ChartCanvas from "$lib/components/molecules/ChartCanvas.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { usageData } from "$lib/stores/usageData.svelte.js";
  import {
    formatNumber,
    formatCost,
    formatTokensShort,
    tokenCountTitle,
  } from "$lib/utils/format.js";
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
  import { resolveColor } from "./chartStyle.js";

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

<div class="cards">
  <div class="card card-wide">
    <div class="card-label">Tokens</div>
    <div class="card-value cache-token-value">
      <span
        class="cache-token-part"
        title={tokenCountTitle("Input tokens", summary.total_input_tokens)}
      >
        <span>{formatTokensShort(summary.total_input_tokens)}</span><span
          class="cache-token-marker">i</span>
      </span>
      <span class="cache-token-operator">+</span>
      <span
        class="cache-token-part"
        title={tokenCountTitle("Output tokens", summary.total_output_tokens)}
      >
        <span>{formatTokensShort(summary.total_output_tokens)}</span><span
          class="cache-token-marker">o</span>
      </span>
      <span class="cache-token-operator">=</span>
      <span
        class="cache-token-part"
        title={tokenCountTitle("Total tokens", summaryTotalTokens(summary))}
      >{formatTokensShort(summaryTotalTokens(summary))}</span>
    </div>
  </div>
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
    <div class="card card-wide">
      <div class="card-label">Local Cache</div>
      <div class="card-value cache-token-value">
        <span
          class="cache-token-part"
          title={tokenCountTitle(
            "Input tokens",
            cacheOverview.summary.total_input_tokens,
          )}
        >
          <span>{formatTokensShort(cacheOverview.summary.total_input_tokens)}</span
          ><span class="cache-token-marker">i</span>
        </span>
        <span class="cache-token-operator">+</span>
        <span
          class="cache-token-part"
          title={tokenCountTitle(
            "Output tokens",
            cacheOverview.summary.total_output_tokens,
          )}
        >
          <span>{formatTokensShort(cacheOverview.summary.total_output_tokens)}</span
          ><span class="cache-token-marker">o</span>
        </span>
        <span class="cache-token-operator">=</span>
        <span
          class="cache-token-part"
          title={tokenCountTitle(
            "Total tokens",
            cacheOverviewTotalTokens(cacheOverview),
          )}
        >{formatTokensShort(cacheOverviewTotalTokens(cacheOverview))}</span>
      </div>
    </div>
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
    <div
      class="card provider-status-flag provider-status-overview-card {providerStatusSummaryClass(providerSummary)}"
    >
      <div class="card-label">Provider Status</div>
      <div class="card-value provider-status-value">
        {providerStatusRatioText(providerSummary)}
      </div>
      {#if providerStatusHasIssues(providerSummary)}
        <button
          type="button"
          class="provider-status-card-link"
          aria-label="View providers overview"
          title="View providers overview"
          onclick={scrollToProviderStatusSection}
        >{providerStatusSummaryText(providerSummary)}</button>
      {:else}
        <span class="provider-status-card-note"
        >{providerStatusSummaryText(providerSummary)}</span>
      {/if}
    </div>
  {/if}
  {#if mcpOverviewVisible(mcpServersState.available, mcpServersState.servers)}
    <div
      class="card provider-status-flag mcp-servers-flag {mcpOverviewSummaryClass(mcpServersState.servers)}"
    >
      <div class="card-label">MCP Servers</div>
      <div class="card-value provider-status-value">
        {mcpOverviewRatioText(mcpServersState.servers)}
      </div>
      <button
        type="button"
        class="provider-status-card-link"
        aria-label="View MCP servers"
        title="View MCP servers"
        onclick={() => router.navigate("mcp-servers")}
      >{mcpOverviewSummaryText(mcpServersState.servers)}</button>
    </div>
  {/if}
</div>
