<script>
  // Tokens cache meter: share of input tokens over the selected period split
  // into regular / prompt-cached / locally-cached segments.
  import { usageData } from "$lib/stores/usageData.svelte.js";
  import { formatNumber } from "$lib/utils/format.js";
  import {
    cacheMeterCategories,
    cacheMeterSegments,
    cacheMeterVisible,
    cacheMeterSegmentTitle,
    cacheMeterAriaLabel,
  } from "./cacheMeterLogic.js";

  const cacheEnabled = $derived(usageData.cacheAnalyticsEnabled());
  const categories = $derived(
    cacheMeterCategories(usageData.summary, usageData.cacheOverview, cacheEnabled),
  );
  const segments = $derived(
    cacheMeterSegments(usageData.summary, usageData.cacheOverview, cacheEnabled),
  );
  const visible = $derived(
    cacheMeterVisible(usageData.summary, usageData.cacheOverview, cacheEnabled),
  );
</script>

<div class="cache-meter">
  <div class="cache-meter-header">
    <h3>Tokens</h3>
    <span class="cache-meter-subtitle">Share of input tokens over the selected period</span>
  </div>
  <div
    class="cache-meter-bar"
    class:is-empty={!visible}
    role="img"
    aria-label={cacheMeterAriaLabel(segments)}
  >
    {#each segments as seg (seg.key)}
      <div
        class="cache-meter-segment"
        style="width: {seg.pct}%; background: var({seg.colorVar})"
        title={cacheMeterSegmentTitle(seg)}
      >
        {#if seg.pct >= 8}
          <span class="cache-meter-segment-label">{seg.pct}%</span>
        {/if}
      </div>
    {/each}
    {#if !visible}
      <span class="cache-meter-empty">No usage in the selected period yet</span>
    {/if}
  </div>
  <div class="cache-meter-legend">
    {#each categories as seg (seg.key)}
      <div class="cache-meter-legend-item" title={cacheMeterSegmentTitle(seg)}>
        <span class="cache-meter-swatch" style="background: var({seg.colorVar})"
        ></span>
        <span class="cache-meter-legend-label">{seg.label}</span>
        <span class="cache-meter-legend-pct mono">{seg.pct}%</span>
        <span class="cache-meter-legend-tokens mono">{formatNumber(seg.tokens)}</span>
      </div>
    {/each}
  </div>
</div>
