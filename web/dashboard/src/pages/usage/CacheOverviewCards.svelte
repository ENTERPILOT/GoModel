<script>
  // Cache overview stat cards (rendered inside the page's .cards grid).
  // Values come from the shared cacheOverview, which the usage page fetches
  // with its facet filters applied.
  import { usageData } from "$lib/stores/usageData.svelte.js";
  import { formatCost, formatNumber } from "$lib/utils/format.js";
</script>

{#if usageData.cacheAnalyticsEnabled()}
  <div class="card">
    <div class="card-label">Cache Saved</div>
    <div class="card-value">
      {formatCost(usageData.cacheOverview.summary.total_saved_cost)}
    </div>
  </div>
  <div class="card">
    <div class="card-label">Cache Hits</div>
    <div class="card-value">
      {formatNumber(usageData.cacheOverview.summary.total_hits)}
    </div>
  </div>
{/if}
