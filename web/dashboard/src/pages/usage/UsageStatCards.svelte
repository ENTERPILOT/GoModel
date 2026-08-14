<script>
  // Period totals for the active filters. Requests follow the request log's
  // cache scope (cached rows count unless "Hide cached requests" is on);
  // cost comes from the uncached summary so cached rows' avoided cost never
  // inflates it.
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import { formatCost, formatNumber } from "$lib/utils/format.js";
  import { usagePage } from "./usage.svelte.js";
  import {
    proSavedPercentText,
    proSavedTitle,
    proSavedValueText,
    rewriteSavingsVisible,
    usagePageCostTitle,
    usagePageRequestsTitle,
    usagePageTotalRequests,
  } from "./usage-helpers.js";
  import CacheOverviewCards from "./CacheOverviewCards.svelte";
  import * as m from "$lib/paraglide/messages.js";

  const savingsVisible = $derived(rewriteSavingsVisible(usagePage.usageSummary));
  // One savings number, in whatever unit the page's Tokens/Costs toggle is on.
  const savedValue = $derived(
    proSavedValueText(usagePage.usageSummary, usagePage.usageMode),
  );
  const savedPercent = $derived(
    proSavedPercentText(usagePage.usageSummary, usagePage.usageMode),
  );
</script>

<div class="cards">
  <div class="card">
    <div class="card-label">{m.overview_total_requests()}</div>
    <div
      class="card-value"
      title={usagePageRequestsTitle(
        usagePage.usageSummary,
        usagePage.usageSummaryAll,
        usagePage.usageLogHideCached,
      )}
    >
      {#if usagePage.summaryLoading}
        <Spinner size={18} label={m.usage_loading_summary()} />
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
    <div class="card-label">{m.overview_estimated_cost()}</div>
    <div class="card-value" title={usagePageCostTitle(usagePage.usageSummary)}>
      {#if usagePage.summaryLoading}
        <Spinner size={18} label={m.usage_loading_summary()} />
      {:else}
        {formatCost(usagePage.usageSummary.total_cost)}
      {/if}
    </div>
  </div>
  {#if savingsVisible}
    <div class="card">
      <div class="card-label">{m.usage_pro_saved()}</div>
      <div
        class="card-value pro-saved-value"
        title={proSavedTitle(usagePage.usageSummary, usagePage.usageMode)}
      >
        <span>{savedValue}</span>
        {#if savedPercent}
          <span class="pro-saved-delta">{savedPercent}</span>
        {/if}
      </div>
    </div>
  {/if}
  <CacheOverviewCards />
</div>

<style>
  /* The savings share trails the value on the same line, sized down so the
     card still reads as one number. */
  .pro-saved-value {
    align-items: baseline;
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }

  .pro-saved-delta {
    color: var(--success);
    font-size: 13px;
    font-weight: 600;
    letter-spacing: 0;
  }
</style>
