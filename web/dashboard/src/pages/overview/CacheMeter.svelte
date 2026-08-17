<script>
  // Tokens cache meter: share of input tokens over the selected period split
  // into regular / prompt-cached / locally-cached segments.
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import { usageData } from "$lib/stores/usageData.svelte.js";
  import { formatNumber } from "$lib/utils/format.js";
  import {
    cacheMeterCategories,
    cacheMeterSegments,
    cacheMeterVisible,
    cacheMeterSegmentTitle,
    cacheMeterAriaLabel,
  } from "./cacheMeterLogic.js";
  import * as m from "$lib/paraglide/messages.js";

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
  // With segments on screen, refetches settle in place; the spinner only
  // covers the first load of an empty meter.
  const loading = $derived(
    !visible && (usageData.loading || usageData.cacheLoading),
  );
</script>

<div class="cache-meter">
  <div class="cache-meter-header">
    <h3>{m.overview_tokens()}</h3>
    <span class="cache-meter-subtitle">{m.overview_cache_share()}</span>
  </div>
  <!-- role="img" makes descendants presentational, which would hide the
       loading spinner's role="status" from assistive technology — so the
       image semantics only apply once the meter actually shows data. -->
  <div
    class="cache-meter-bar"
    class:is-empty={!visible}
    role={loading ? undefined : "img"}
    aria-label={loading ? undefined : cacheMeterAriaLabel(segments)}
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
    {#if loading}
      <Spinner size={16} label={m.usage_loading()} />
    {:else if !visible}
      <span class="cache-meter-empty">{m.overview_no_usage()}</span>
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

<style>
  .cache-meter-header {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: 4px 12px;
    margin-bottom: 16px;
  }

  .cache-meter-header :global(h3) {
    font-size: 16px;
    font-weight: 600;
  }

  .cache-meter-subtitle {
    font-size: 13px;
    color: var(--text-muted);
  }

  .cache-meter-bar {
    display: flex;
    width: 100%;
    height: 28px;
    border-radius: 6px;
    overflow: hidden;
    background: var(--bg-surface-hover);
  }

  .cache-meter-segment {
    display: flex;
    align-items: center;
    justify-content: center;
    height: 100%;
    min-width: 3px;
    overflow: hidden;
    transition: width 0.3s ease;
  }

  .cache-meter-segment-label {
    color: #fff;
    font-size: 12px;
    font-weight: 600;
    line-height: 1;
    white-space: nowrap;
    /* Keeps white legible on the lighter brown slice in dark mode. */
    text-shadow: 0 1px 2px rgba(0, 0, 0, 0.45);
  }

  .cache-meter-segment + .cache-meter-segment {
    box-shadow: -1px 0 0 var(--bg-surface);
  }

  .cache-meter-bar.is-empty {
    height: auto;
    min-height: 28px;
    align-items: center;
    justify-content: center;
    padding: 6px 12px;
    background: var(--bg-surface-hover);
  }

  .cache-meter-legend {
    display: flex;
    flex-wrap: wrap;
    gap: 10px 24px;
    margin-top: 16px;
  }

  .cache-meter-legend-item {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
  }

  .cache-meter-swatch {
    width: 12px;
    height: 12px;
    border-radius: 3px;
    flex-shrink: 0;
  }

  .cache-meter-legend-label {
    color: var(--text);
  }

  .cache-meter-legend-pct {
    color: var(--text);
    font-weight: 600;
  }

  .cache-meter-legend-tokens {
    color: var(--text-muted);
  }

  .cache-meter-empty {
    font-size: 13px;
    color: var(--text-muted);
    text-align: center;
  }
</style>
