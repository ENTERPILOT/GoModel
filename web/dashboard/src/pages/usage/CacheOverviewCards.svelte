<script>
  // Cache overview stat cards (rendered inside the page's .cards grid).
  // Values come from the shared cacheOverview, which the usage page fetches
  // with its facet filters applied.
  import { usageData } from "$lib/stores/usageData.svelte.js";
  import { formatCost, formatNumber } from "$lib/utils/format.js";
  import * as m from "$lib/paraglide/messages.js";
</script>

{#if usageData.cacheAnalyticsEnabled()}
  <div class="card">
    <div class="card-label">{m.usage_cache_saved()}</div>
    <div class="card-value">
      {formatCost(usageData.cacheOverview.summary.total_saved_cost)}
    </div>
  </div>
  <div class="card">
    <div class="card-label">{m.overview_cache_hits()}</div>
    <div class="card-value">
      {formatNumber(usageData.cacheOverview.summary.total_hits)}
    </div>
  </div>
{/if}
