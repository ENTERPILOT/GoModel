<script>
  // Period totals for the active filters. Requests follow the request log's
  // cache scope (cached rows count unless "Hide cached requests" is on);
  // cost comes from the uncached summary so cached rows' avoided cost never
  // inflates it.
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import { formatCost, formatNumber } from "$lib/utils/format.js";
  import { usagePage } from "./usage.svelte.js";
  import {
    rewriteCostSaved,
    rewriteSavedTitle,
    rewriteSavingsVisible,
    rewriteTokensSaved,
    usagePageCostTitle,
    usagePageRequestsTitle,
    usagePageTotalRequests,
  } from "./usage-helpers.js";
  import CacheOverviewCards from "./CacheOverviewCards.svelte";

  const savingsVisible = $derived(rewriteSavingsVisible(usagePage.usageSummary));
</script>

<div class="cards">
  <div class="card">
    <div class="card-label">Total Requests</div>
    <div
      class="card-value"
      title={usagePageRequestsTitle(
        usagePage.usageSummary,
        usagePage.usageSummaryAll,
        usagePage.usageLogHideCached,
      )}
    >
      {#if usagePage.summaryLoading}
        <Spinner size={18} label="Loading usage summary" />
      {:else}
        {formatNumber(
          usagePageTotalRequests(
            usagePage.usageSummary,
            usagePage.usageSummaryAll,
            usagePage.usageLogHideCached,
          ),
        )}
      {/if}
    </div>
  </div>
  <div class="card">
    <div class="card-label">Estimated Cost</div>
    <div class="card-value" title={usagePageCostTitle(usagePage.usageSummary)}>
      {#if usagePage.summaryLoading}
        <Spinner size={18} label="Loading usage summary" />
      {:else}
        {formatCost(usagePage.usageSummary.total_cost)}
      {/if}
    </div>
  </div>
  {#if savingsVisible}
    <div class="card">
      <div class="card-label">Rewrite Saved</div>
      <div class="card-value" title={rewriteSavedTitle(usagePage.usageSummary)}>
        {formatCost(rewriteCostSaved(usagePage.usageSummary))}
      </div>
    </div>
    <div class="card">
      <div class="card-label">Tokens Saved</div>
      <div class="card-value" title={rewriteSavedTitle(usagePage.usageSummary)}>
        {formatNumber(rewriteTokensSaved(usagePage.usageSummary))}
      </div>
    </div>
  {/if}
  <CacheOverviewCards />
</div>
