<script>
  // Cache overview stat cards (rendered inside the page's .cards grid).
  // Values come from the shared cacheOverview, which the usage page fetches
  // with its facet filters applied.
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import { usageData } from "$lib/stores/usageData.svelte.js";
  import { formatCost, formatNumber } from "$lib/utils/format.js";
  import * as m from "$lib/paraglide/messages.js";
</script>

{#snippet cacheCard(label, value)}
  <div class="card">
    <div class="card-label">{label}</div>
    <div class="card-value">
      {#if usageData.cacheLoading}
        <Spinner size={18} label={m.usage_loading()} />
      {:else}
        {value}
      {/if}
    </div>
  </div>
{/snippet}

{#if usageData.cacheAnalyticsEnabled()}
  {@render cacheCard(
    m.usage_cache_saved(),
    formatCost(usageData.cacheOverview.summary.total_saved_cost),
  )}
  {@render cacheCard(
    m.overview_cache_hits(),
    formatNumber(usageData.cacheOverview.summary.total_hits),
  )}
{/if}
