<script>
  // One usage breakdown section (by model, user path, or label): diverging /
  // stacked horizontal bar chart with a table view toggle.
  import ChartCanvas from "$lib/components/molecules/ChartCanvas.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import { chartColors } from "$lib/utils/chartTheme.js";
  import {
    formatCost,
    formatNumber,
    providerDisplayValue,
    qualifiedModelDisplay,
  } from "$lib/utils/format.js";
  import { usagePage } from "./usage.svelte.js";
  import {
    chartWrapHeight,
    divergingDataFrom,
    isChartView,
    labelColor,
    usageRowTotalTokens,
    usageRowsBySelectedValue,
    userPathUsageChartVisible,
  } from "./usage-helpers.js";
  import { horizontalUsageChartConfig, resolveCssColor } from "./usage-chart-config.js";

  /** @type {{ kind: "model" | "userPath" | "label" }} */
  let { kind } = $props();

  const META = {
    model: {
      group: "Model usage view",
      noun: "model usage",
      tokensTitle: "Token Usage by Model",
      costsTitle: "Cost by Model",
    },
    userPath: {
      group: "User path usage view",
      noun: "user path usage",
      tokensTitle: "Usage by User Path",
      costsTitle: "Cost by User Path",
    },
    label: {
      group: "Label usage view",
      noun: "label usage",
      tokensTitle: "Usage by Label",
      costsTitle: "Cost by Label",
    },
  };
  const meta = $derived(META[kind]);

  const labelFor = $derived(
    kind === "model"
      ? (row) => qualifiedModelDisplay(row)
      : kind === "userPath"
        ? (row) => row.user_path || "/"
        : (row) => row.label,
  );

  function rowKey(row) {
    if (kind === "model")
      return (row.provider_name || row.provider || "-") + "/" + row.model;
    if (kind === "userPath") return row.user_path || "/";
    return row.label;
  }

  const rows = $derived(
    kind === "model"
      ? usagePage.modelUsage
      : kind === "userPath"
        ? usagePage.userPathUsage
        : usagePage.labelUsage,
  );
  const view = $derived(
    kind === "model"
      ? usagePage.modelUsageView
      : kind === "userPath"
        ? usagePage.userPathUsageView
        : usagePage.labelUsageView,
  );
  const loading = $derived(
    kind === "model"
      ? usagePage.modelUsageLoading
      : kind === "userPath"
        ? usagePage.userPathUsageLoading
        : usagePage.labelUsageLoading,
  );
  const costs = $derived(usagePage.usageMode === "costs");
  const visible = $derived(
    kind === "userPath" ? userPathUsageChartVisible(rows) : rows.length > 0,
  );
  const title = $derived(costs ? meta.costsTitle : meta.tokensTitle);
  const series = $derived(divergingDataFrom(rows, labelFor, costs));
  const tableRows = $derived(usageRowsBySelectedValue(rows, costs));

  function buildChart() {
    if (!isChartView(view)) return null;
    return horizontalUsageChartConfig(chartColors(), series.labels, series, {
      stacked: view === "stacked",
      costs,
      resolve: resolveCssColor,
    });
  }

  // Label-usage inline help.
  const helpCopyId = "label-usage-help-copy";
  const helpText =
    "One request can have multiple labels. Such a request counts once under each of its labels, so label rows can overlap and add up to more than the period totals.";
</script>

{#snippet viewToggle()}
  <div class="chart-view-toggle" role="group" aria-label={meta.group}>
    <button
      type="button"
      class="chart-view-btn"
      class:active={view === "chart"}
      aria-pressed={view === "chart"}
      aria-label="Show {meta.noun} chart"
      title="Chart"
      onclick={() => usagePage.toggleUsageChartView(kind, "chart")}
    >
      <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 4v16M12 8h7M12 12H7M12 16h4" /></svg>
    </button>
    <button
      type="button"
      class="chart-view-btn"
      class:active={view === "stacked"}
      aria-pressed={view === "stacked"}
      aria-label="Show {meta.noun} stacked chart"
      title="Stacked"
      onclick={() => usagePage.toggleUsageChartView(kind, "stacked")}
    >
      <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 4v16M5 8h6m2 0h4M5 12h9m2 0h3M5 16h4m2 0h2" /></svg>
    </button>
    <button
      type="button"
      class="chart-view-btn"
      class:active={view === "table"}
      aria-pressed={view === "table"}
      aria-label="Show {meta.noun} table"
      title="Table"
      onclick={() => usagePage.toggleUsageChartView(kind, "table")}
    >
      <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 6h16M4 12h16M4 18h16M9 6v12M15 6v12" /></svg>
    </button>
  </div>
{/snippet}

{#if visible}
  <div class="model-chart-section">
    <div class="model-chart-header">
      {#if kind === "label"}
        <InlineHelpSection copyId={helpCopyId} label="label usage help" text={helpText}>
          {#snippet title()}<h3>{title}</h3>{/snippet}
          {#snippet extra()}
            {#if loading}<Spinner size={14} label="Loading {meta.noun}" />{/if}
          {/snippet}
        </InlineHelpSection>
      {:else}
        <h3>{title}</h3>
        {#if loading}<Spinner size={14} label="Loading {meta.noun}" />{/if}
      {/if}
      {@render viewToggle()}
    </div>
    {#if isChartView(view)}
      <div class="bar-chart-wrap" style:height="{chartWrapHeight(series.labels.length)}px">
        <ChartCanvas build={buildChart} />
      </div>
    {:else}
      <div class="table-wrapper usage-chart-table-wrapper">
        <table class="data-table usage-chart-data-table">
          <thead>
            <tr>
              {#if kind === "model"}
                <th>Model</th>
                <th>Provider</th>
              {:else if kind === "userPath"}
                <th>User Path</th>
              {:else}
                <th>Label</th>
                <th class="col-price">Requests</th>
              {/if}
              <th class="col-price">Input Tokens</th>
              <th class="col-price">Output Tokens</th>
              <th class="col-price" title="Provider prompt-cache reads included in the input tokens">Prompt Cached</th>
              <th class="col-price" title="Tokens served from GoModel's local response cache (excluded from the other columns)">Local Cached</th>
              <th class="col-price">Total Tokens</th>
              <th class="col-price">Input Cost</th>
              <th class="col-price">Output Cost</th>
              <th class="col-price">Total Cost</th>
            </tr>
          </thead>
          <tbody>
            {#each tableRows as row (rowKey(row))}
              <tr>
                {#if kind === "model"}
                  <td class="mono font-size-md">{row.model || "-"}</td>
                  <td><span class="provider-badge">{providerDisplayValue(row) || "-"}</span></td>
                {:else if kind === "userPath"}
                  <td class="mono font-size-md">{row.user_path || "/"}</td>
                {:else}
                  <td>
                    <button
                      type="button"
                      class="usage-label-chip"
                      class:active={usagePage.usageFilterLabel === row.label}
                      style="--label-color: {labelColor(row.label)}"
                      title={usagePage.usageLabelChipTitle(row.label)}
                      onclick={() => usagePage.toggleUsageLabelFilter(row.label)}
                    >{row.label}</button>
                  </td>
                  <td class="col-price">{formatNumber(row.requests)}</td>
                {/if}
                <td class="col-price">{formatNumber(row.input_tokens)}</td>
                <td class="col-price">{formatNumber(row.output_tokens)}</td>
                <td
                  class="col-price"
                  title={row.cached_input_cost != null
                    ? "~" + formatCost(row.cached_input_cost) + " at current cached-input pricing"
                    : ""}>{formatNumber(row.cached_input_tokens || 0)}</td
                >
                <td
                  class="col-price"
                  title="{formatNumber(row.local_cached_input_tokens || 0)} input + {formatNumber(
                    row.local_cached_output_tokens || 0,
                  )} output"
                  >{formatNumber(
                    (row.local_cached_input_tokens || 0) + (row.local_cached_output_tokens || 0),
                  )}</td
                >
                <td class="col-price">{formatNumber(usageRowTotalTokens(row))}</td>
                <td class="col-price">{formatCost(row.input_cost)}</td>
                <td class="col-price">{formatCost(row.output_cost)}</td>
                <td class="col-price">{formatCost(row.total_cost)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </div>
{:else if loading}
  <!-- Loading affordance while the breakdown fetch is in flight. -->
  <div class="model-chart-section usage-breakdown-loading">
    <Spinner size={20} label="Loading {meta.noun}" />
  </div>
{/if}

<style>
  .usage-breakdown-loading {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 120px;
  }
.chart-view-toggle {
  display: inline-flex;
  align-items: center;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 2px;
}

.chart-view-btn {
  width: 30px;
  height: 28px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  background: transparent;
  border: none;
  border-radius: 4px;
  color: var(--text-muted);
  cursor: pointer;
  transition: all 0.15s;
}

.chart-view-btn:hover {
  color: var(--text);
}

.chart-view-btn.active {
  background: var(--accent);
  color: #fff;
}

.chart-view-btn :global(svg) {
  width: 16px;
  height: 16px;
  fill: none;
  stroke: currentcolor;
  stroke-width: 2;
  stroke-linecap: round;
  stroke-linejoin: round;
}

.usage-chart-table-wrapper {
  margin-top: 0;
  overflow-x: auto;
  -webkit-overflow-scrolling: touch;
}

.usage-chart-data-table {
  min-width: max-content;
}

.usage-chart-data-table :global(th), .usage-chart-data-table :global(td) {
  white-space: nowrap;
}
</style>
