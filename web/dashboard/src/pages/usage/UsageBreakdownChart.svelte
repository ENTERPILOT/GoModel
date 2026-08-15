<script>
  // One usage breakdown section (by model, user path, label, or session): diverging /
  // stacked horizontal bar chart with a table view toggle.
  import ChartCanvas from "$lib/components/molecules/ChartCanvas.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import Pagination from "$lib/components/molecules/Pagination.svelte";
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import { chartColors, resolveCssColor } from "$lib/utils/chartTheme.js";
  import {
    formatCost,
    formatNumber,
    providerDisplayValue,
    qualifiedModelDisplay,
  } from "$lib/utils/format.js";
  import { usagePage } from "./usage.svelte.js";
  import SessionIDChip from "./SessionIDChip.svelte";
  import {
    chartWrapHeight,
    divergingDataFrom,
    isChartView,
    labelColor,
    usageRowTotalTokens,
    usageRowsBySelectedValue,
    userPathUsageChartVisible,
  } from "./usage-helpers.js";
  import { horizontalUsageChartConfig } from "./usage-chart-config.js";
  import * as m from "$lib/paraglide/messages.js";

  /** @type {{ kind: "model" | "userPath" | "label" | "session" }} */
  let { kind } = $props();

  const META = {
    model: {
      group: m.usage_model_view(),
      noun: m.usage_model_noun(),
      tokensTitle: m.usage_tokens_by_model(),
      costsTitle: m.usage_cost_by_model(),
    },
    userPath: {
      group: m.usage_user_path_view(),
      noun: m.usage_user_path_noun(),
      tokensTitle: m.usage_by_user_path(),
      costsTitle: m.usage_cost_by_user_path(),
    },
    label: {
      group: m.usage_label_view(),
      noun: m.usage_label_noun(),
      tokensTitle: m.usage_by_label(),
      costsTitle: m.usage_cost_by_label(),
    },
    session: {
      group: m.usage_session_view(),
      noun: m.usage_session_noun(),
      tokensTitle: m.usage_by_recent_session(),
      costsTitle: m.usage_cost_by_recent_session(),
    },
  };
  const meta = $derived(META[kind]);

  const labelFor = $derived(
    kind === "model"
      ? (row) => qualifiedModelDisplay(row)
      : kind === "userPath"
        ? (row) => row.user_path || "/"
        : kind === "label"
          ? (row) => row.label
          : (row) => row.session_id,
  );

  function rowKey(row) {
    if (kind === "model")
      return (row.provider_name || row.provider || "-") + "/" + row.model;
    if (kind === "userPath") return row.user_path || "/";
    if (kind === "label") return row.label;
    return JSON.stringify([row.session_id, row.user_path || "/"]);
  }

  const rows = $derived(
    kind === "model"
      ? usagePage.modelUsage
      : kind === "userPath"
        ? usagePage.userPathUsage
        : kind === "label"
          ? usagePage.labelUsage
          : usagePage.sessionUsage.entries,
  );
  const view = $derived(
    kind === "model"
      ? usagePage.modelUsageView
      : kind === "userPath"
        ? usagePage.userPathUsageView
        : kind === "label"
          ? usagePage.labelUsageView
          : usagePage.sessionUsageView,
  );
  const loading = $derived(
    kind === "model"
      ? usagePage.modelUsageLoading
      : kind === "userPath"
        ? usagePage.userPathUsageLoading
        : kind === "label"
          ? usagePage.labelUsageLoading
          : usagePage.sessionUsageLoading,
  );
  const costs = $derived(usagePage.usageMode === "costs");
  const visible = $derived(
    kind === "userPath" ? userPathUsageChartVisible(rows) : rows.length > 0,
  );
  // Named `heading` (not `title`) so the title() snippet below doesn't
  // shadow it — inside that snippet, `title` refers to the snippet itself.
  const heading = $derived(costs ? meta.costsTitle : meta.tokensTitle);
  const series = $derived(divergingDataFrom(rows, labelFor, costs));
  const tableRows = $derived(
    kind === "session" ? rows : usageRowsBySelectedValue(rows, costs),
  );

  function buildChart() {
    if (!isChartView(view)) return null;
    return horizontalUsageChartConfig(chartColors(), series.labels, series, {
      stacked: view === "stacked",
      costs,
      resolve: resolveCssColor,
    });
  }

  const helpCopyId = $derived(kind + "-usage-help-copy");
  const helpText = $derived(
    kind === "session"
      ? m.usage_session_help()
      : m.usage_label_help(),
  );
</script>

{#snippet viewToggle()}
  <div class="chart-view-toggle" role="group" aria-label={meta.group}>
    <button
      type="button"
      class="chart-view-btn"
      class:active={view === "chart"}
      aria-pressed={view === "chart"}
      aria-label={m.usage_show_chart({ noun: meta.noun })}
      title={m.usage_chart()}
      onclick={() => usagePage.toggleUsageChartView(kind, "chart")}
    >
      <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 4v16M12 8h7M12 12H7M12 16h4" /></svg>
    </button>
    <button
      type="button"
      class="chart-view-btn"
      class:active={view === "stacked"}
      aria-pressed={view === "stacked"}
      aria-label={m.usage_show_stacked_chart({ noun: meta.noun })}
      title={m.usage_stacked()}
      onclick={() => usagePage.toggleUsageChartView(kind, "stacked")}
    >
      <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 4v16M5 8h6m2 0h4M5 12h9m2 0h3M5 16h4m2 0h2" /></svg>
    </button>
    <button
      type="button"
      class="chart-view-btn"
      class:active={view === "table"}
      aria-pressed={view === "table"}
      aria-label={m.usage_show_table({ noun: meta.noun })}
      title={m.usage_table()}
      onclick={() => usagePage.toggleUsageChartView(kind, "table")}
    >
      <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 6h16M4 12h16M4 18h16M9 6v12M15 6v12" /></svg>
    </button>
  </div>
{/snippet}

{#if visible}
  <div class="model-chart-section" class:session-usage-breakdown={kind === "session"}>
    <div class="model-chart-header">
      {#if kind === "label" || kind === "session"}
        <InlineHelpSection
          copyId={helpCopyId}
          label={kind === "session" ? m.usage_session_help_label() : m.usage_label_help_label()}
          text={helpText}
        >
          {#snippet title()}<h3>{heading}</h3>{/snippet}
          {#snippet extra()}
            {#if loading}<Spinner size={14} label={m.usage_loading_breakdown({ noun: meta.noun })} />{/if}
          {/snippet}
        </InlineHelpSection>
      {:else}
        <h3>{heading}</h3>
        {#if loading}<Spinner size={14} label={m.usage_loading_breakdown({ noun: meta.noun })} />{/if}
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
                <th>{m.usage_column_model()}</th>
                <th>{m.usage_column_provider()}</th>
              {:else if kind === "userPath"}
                <th>{m.usage_column_user_path()}</th>
              {:else if kind === "label"}
                <th>{m.usage_column_label()}</th>
                <th class="col-price">{m.usage_column_requests()}</th>
              {:else}
                <th>{m.usage_column_session_id()}</th>
                <th>{m.usage_column_user_path()}</th>
                <th class="col-price">{m.usage_column_requests()}</th>
              {/if}
              <th class="col-price">{m.usage_column_input_tokens()}</th>
              <th class="col-price">{m.usage_column_output_tokens()}</th>
              {#if kind !== "session"}
                <th class="col-price" title={m.usage_prompt_cached_help()}>{m.usage_column_prompt_cached()}</th>
                <th class="col-price" title={m.usage_local_cached_help()}>{m.usage_column_local_cached()}</th>
              {/if}
              <th class="col-price">{m.usage_column_total_tokens()}</th>
              <th class="col-price">{m.usage_column_input_cost()}</th>
              <th class="col-price">{m.usage_column_output_cost()}</th>
              <th class="col-price">{m.usage_column_total_cost()}</th>
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
                {:else if kind === "label"}
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
                {:else}
                  <td>
                    <SessionIDChip
                      sessionID={row.session_id}
                      active={usagePage.usageFilterSession === row.session_id}
                      onfilter={(sessionID) => usagePage.filterBySession(sessionID, row.user_path)}
                    />
                  </td>
                  <td class="mono font-size-md">{row.user_path || "/"}</td>
                  <td class="col-price">{formatNumber(row.requests)}</td>
                {/if}
                <td class="col-price">{formatNumber(row.input_tokens)}</td>
                <td class="col-price">{formatNumber(row.output_tokens)}</td>
                {#if kind !== "session"}
                  <td
                    class="col-price"
                    title={row.cached_input_cost != null
                      ? m.usage_current_cached_price({ cost: formatCost(row.cached_input_cost) })
                      : ""}>{formatNumber(row.cached_input_tokens || 0)}</td
                  >
                  <td
                    class="col-price"
                    title={m.usage_local_cached_split({
                      input: formatNumber(row.local_cached_input_tokens || 0),
                      output: formatNumber(row.local_cached_output_tokens || 0),
                    })}
                    >{formatNumber(
                      (row.local_cached_input_tokens || 0) + (row.local_cached_output_tokens || 0),
                    )}</td
                  >
                {/if}
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
    {#if kind === "session"}
      <Pagination
        total={usagePage.sessionUsage.total}
        offset={usagePage.sessionUsage.offset}
        limit={usagePage.sessionUsage.limit}
        onprev={() => usagePage.sessionUsagePrevPage()}
        onnext={() => usagePage.sessionUsageNextPage()}
      />
    {/if}
  </div>
{:else if loading}
  <!-- Loading affordance while the breakdown fetch is in flight. -->
  <div class="model-chart-section usage-breakdown-loading">
    <Spinner size={20} label={m.usage_loading_breakdown({ noun: meta.noun })} />
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
