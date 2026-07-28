<script>
  // Live Token Throughput widget (overview-only). A rolling, stacked bar
  // chart of token volume bucketed by a selectable granularity; the data
  // engine (polling + usage SSE signal) lives in liveTokensState.svelte.js.
  import ChartCanvas from "$lib/components/molecules/ChartCanvas.svelte";
  import SegmentedControl from "$lib/components/atoms/SegmentedControl.svelte";
  import { chartColors, resolveCssColor as resolveColor } from "$lib/utils/chartTheme.ts";
  import { liveTokensState } from "./liveTokensState.svelte.js";
  import {
    GRANULARITY_OPTIONS,
    bucketsToSeries,
    liveTokensHasData,
    liveTokensLegendValue,
    liveTokensWindowLabel,
    liveTokensChartAriaLabel,
    liveTokensChartConfig,
  } from "./liveTokensLogic.js";

  const series = $derived(
    bucketsToSeries(liveTokensState.buckets, liveTokensState.granularity),
  );
  const totals = $derived(series.totals);

  function seriesColors() {
    return {
      input: resolveColor("var(--token-input)"),
      output: resolveColor("var(--token-output)"),
      prompt: resolveColor("var(--token-prompt)"),
      local: resolveColor("var(--token-local)"),
    };
  }

  const legendItems = [
    { metric: "input", label: "Input Tokens", colorVar: "--token-input" },
    { metric: "output", label: "Output Tokens", colorVar: "--token-output" },
    { metric: "prompt", label: "Prompt (Input) Cached", colorVar: "--token-prompt" },
    { metric: "local", label: "Locally Cached", colorVar: "--token-local" },
  ];
</script>

<div class="chart-container live-tokens">
  <div class="chart-container-header">
    <div class="live-tokens-heading">
      <h3>Live Token Throughput</h3>
      <span class="live-tokens-subtitle">
        <span
          class="live-dot"
          class:is-streaming={liveTokensState.active}
          aria-hidden="true"
        ></span>
        <span>{liveTokensWindowLabel(liveTokensState.granularity)}</span>
      </span>
    </div>
    <SegmentedControl
      ariaLabel="Live token throughput granularity"
      options={GRANULARITY_OPTIONS}
      value={liveTokensState.granularity}
      onchange={(granularity) => liveTokensState.setGranularity(granularity)}
    />
  </div>
  <div class="live-tokens-legend">
    {#each legendItems as item (item.metric)}
      <div class="live-tokens-legend-item">
        <span class="live-tokens-swatch" style="background: var({item.colorVar})"
        ></span>
        <span class="live-tokens-legend-label">{item.label}</span>
        <span class="live-tokens-legend-value mono"
        >{liveTokensLegendValue(totals, item.metric)}</span>
      </div>
    {/each}
  </div>
  <div class="chart-wrapper">
    <ChartCanvas
      ariaLabel={liveTokensChartAriaLabel(totals, liveTokensState.granularity)}
      build={() =>
        liveTokensChartConfig(
          chartColors(),
          seriesColors(),
          series,
          liveTokensState.granularity,
        )}
    />
    {#if !liveTokensHasData(totals)}
      <div class="chart-empty-overlay live-tokens-empty">
        <span class="live-tokens-empty-text">Waiting for live requests…</span>
      </div>
    {/if}
  </div>
</div>

<style>
  /* Live token throughput chart */
  /* The generic .chart-container has no margin (siblings space it), but this card
     is followed by the cache meter, which only spaces below itself — so give the
     live card the same 28px rhythm as .cards/.cache-meter. */
  .live-tokens {
    margin-bottom: 28px;
  }

  .live-tokens-heading {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .live-tokens-subtitle {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    color: var(--text-muted);
  }

  .live-tokens-legend {
    display: flex;
    flex-wrap: wrap;
    gap: 8px 18px;
    margin-bottom: 16px;
  }

  .live-tokens-legend-item {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    font-size: 12px;
    color: var(--text-muted);
  }

  .live-tokens-swatch {
    width: 10px;
    height: 10px;
    border-radius: 2px;
    flex-shrink: 0;
  }

  .live-tokens-legend-value {
    color: var(--text);
    font-weight: 600;
  }

  .live-tokens-empty .live-tokens-empty-text {
    font-size: 13px;
    color: var(--text-muted);
  }
</style>
