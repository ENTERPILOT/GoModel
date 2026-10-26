<script>
  // Breakdown section: pivot the filtered usage by a row and a column
  // dimension (e.g. cost per month by user path), as a stacked bar chart or a
  // table with row and column totals, with a CSV download of the table.
  import SegmentedControl from "$lib/components/atoms/SegmentedControl.svelte";
  import ChartCanvas from "$lib/components/molecules/ChartCanvas.svelte";
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { Download } from "lucide";
  import { chartColors } from "$lib/utils/chartTheme.js";
  import { usagePage } from "./usage.svelte.js";
  import { usagePivot } from "./usagePivot.svelte.js";
  import { PIVOT_DIMENSIONS, PIVOT_INTERVALS } from "./usage-query.js";
  import {
    formatPivotValue,
    pivotChartConfig,
    pivotCsv,
    pivotKeyLabel,
    pivotTable,
  } from "./pivot-helpers.js";
  import * as m from "$lib/paraglide/messages.js";

  const DIMENSION_LABELS = {
    model: m.usage_column_model(),
    provider: m.usage_column_provider(),
    user_path: m.usage_column_user_path(),
    label: m.usage_column_label(),
    endpoint: m.usage_column_endpoint(),
    auth_key: m.usage_column_auth_key(),
    period: m.usage_pivot_period(),
  };
  const INTERVAL_LABELS = {
    daily: m.usage_interval_daily(),
    weekly: m.usage_interval_weekly(),
    monthly: m.usage_interval_monthly(),
    yearly: m.usage_interval_yearly(),
  };
  const DEPTHS = [0, 1, 2, 3, 4];

  const data = $derived(usagePivot.data);
  const metric = $derived(data ? data.metric : usagePivot.metric);
  const table = $derived(pivotTable(data, metric));
  const rowLabels = $derived(table.rows.map((key) => pivotKeyLabel(data.row_dimension, key, usagePage.authKeyNames)));
  const columnLabels = $derived(
    table.columns.map((key) => pivotKeyLabel(data.column_dimension, key, usagePage.authKeyNames)),
  );
  const hasColumns = $derived(Boolean(data && data.column_dimension));
  const empty = $derived(table.rows.length === 0);

  function change(field, value) {
    usagePage.onPivotChanged(field, value);
  }

  function buildChart() {
    if (empty) return null;
    return pivotChartConfig(chartColors(), table, { rowLabels, columnLabels, metric, hasColumns });
  }

  function downloadCsv() {
    const csv = pivotCsv(table, {
      cornerLabel: DIMENSION_LABELS[data.row_dimension],
      rowLabels,
      columnLabels,
      hasColumns,
    });
    const url = URL.createObjectURL(new Blob([csv], { type: "text/csv;charset=utf-8" }));
    const link = document.createElement("a");
    link.href = url;
    link.download = ["usage", data.row_dimension, data.column_dimension, metric].filter(Boolean).join("-") + ".csv";
    link.click();
    URL.revokeObjectURL(url);
  }
</script>

<section class="model-chart-section usage-pivot" aria-label={m.usage_pivot_title()}>
  <div class="model-chart-header">
    <h3>{m.usage_pivot_title()}</h3>
    {#if usagePivot.loading}<Spinner size={14} label={m.usage_pivot_loading()} />{/if}
    <div class="usage-pivot-actions">
      <SegmentedControl
        ariaLabel={m.usage_pivot_view()}
        options={[
          { value: "chart", label: m.usage_chart() },
          { value: "table", label: m.usage_table() },
        ]}
        value={usagePivot.view}
        onchange={(view) => (usagePivot.view = view)}
      />
      <button
        type="button"
        class="btn btn-with-icon"
        aria-label={m.usage_pivot_download_csv()}
        title={m.usage_pivot_download_csv()}
        disabled={empty}
        onclick={downloadCsv}
      >
        <Icon icon={Download} class="form-action-icon" />
        <span>CSV</span>
      </button>
    </div>
  </div>

  <div class="usage-pivot-controls">
    <label>
      <span>{m.usage_pivot_rows()}</span>
      <select class="usage-log-select" value={usagePivot.rows} onchange={(e) => change("rows", e.currentTarget.value)}>
        {#each PIVOT_DIMENSIONS as dimension (dimension)}
          <option value={dimension}>{DIMENSION_LABELS[dimension]}</option>
        {/each}
      </select>
    </label>
    <label>
      <span>{m.usage_pivot_columns()}</span>
      <select class="usage-log-select" value={usagePivot.columns} onchange={(e) => change("columns", e.currentTarget.value)}>
        <option value="">{m.usage_pivot_no_columns()}</option>
        {#each PIVOT_DIMENSIONS as dimension (dimension)}
          <option value={dimension}>{DIMENSION_LABELS[dimension]}</option>
        {/each}
      </select>
    </label>
    <label>
      <span>{m.usage_pivot_metric()}</span>
      <select class="usage-log-select" value={usagePivot.metric} onchange={(e) => change("metric", e.currentTarget.value)}>
        <option value="cost">{m.usage_column_cost()}</option>
        <option value="tokens">{m.usage_column_total_tokens()}</option>
        <option value="requests">{m.usage_column_requests()}</option>
      </select>
    </label>
    {#if usagePivot.uses("period")}
      <label>
        <span>{m.usage_pivot_interval()}</span>
        <select class="usage-log-select" value={usagePivot.interval} onchange={(e) => change("interval", e.currentTarget.value)}>
          {#each PIVOT_INTERVALS as interval (interval)}
            <option value={interval}>{INTERVAL_LABELS[interval]}</option>
          {/each}
        </select>
      </label>
    {/if}
    {#if usagePivot.uses("user_path")}
      <label>
        <span>{m.usage_pivot_depth()}</span>
        <select
          class="usage-log-select"
          value={String(usagePivot.depth)}
          onchange={(e) => change("depth", Number(e.currentTarget.value))}
        >
          {#each DEPTHS as depth (depth)}
            <option value={String(depth)}>{depth ? m.usage_pivot_depth_levels({ count: depth }) : m.usage_pivot_depth_full()}</option>
          {/each}
        </select>
      </label>
    {/if}
  </div>

  {#if empty}
    <p class="usage-pivot-empty">{usagePivot.failed ? m.usage_pivot_failed() : m.usage_pivot_empty()}</p>
  {:else if usagePivot.view === "chart"}
    <div class="usage-pivot-chart">
      <ChartCanvas build={buildChart} ariaLabel={m.usage_pivot_title()} />
    </div>
  {:else}
    <div class="table-wrapper usage-pivot-table-wrapper">
      <table class="data-table usage-pivot-table">
        <thead>
          <tr>
            <th>{DIMENSION_LABELS[data.row_dimension]}</th>
            {#if hasColumns}
              {#each columnLabels as label, i (i)}
                <th class="col-price">{label}</th>
              {/each}
            {/if}
            <th class="col-price">{m.usage_column_total()}</th>
          </tr>
        </thead>
        <tbody>
          {#each table.grid as cells, i (i)}
            <tr>
              <td class="mono font-size-md" class:usage-pivot-other={table.rows[i].other}>{rowLabels[i]}</td>
              {#if hasColumns}
                {#each cells as value, j (j)}
                  <td class="col-price">{formatPivotValue(value, metric)}</td>
                {/each}
              {/if}
              <td class="col-price usage-pivot-total">{formatPivotValue(table.rowTotals[i], metric)}</td>
            </tr>
          {/each}
        </tbody>
        <tfoot>
          <tr>
            <td class="usage-pivot-total">{m.usage_column_total()}</td>
            {#if hasColumns}
              {#each table.columnTotals as value, j (j)}
                <td class="col-price usage-pivot-total">{formatPivotValue(value, metric)}</td>
              {/each}
            {/if}
            <td class="col-price usage-pivot-total">{formatPivotValue(table.total, metric)}</td>
          </tr>
        </tfoot>
      </table>
    </div>
  {/if}
  {#if data && (data.row_dimension === "label" || data.column_dimension === "label")}
    <p class="usage-pivot-note">{m.usage_pivot_label_note()}</p>
  {/if}
</section>

<style>
  .usage-pivot {
    margin-bottom: var(--space-stack);
  }

  .usage-pivot-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-left: auto;
  }

  .usage-pivot-controls {
    display: flex;
    flex-wrap: wrap;
    gap: 12px;
    margin-bottom: 16px;
  }

  .usage-pivot-controls label {
    display: flex;
    flex-direction: column;
    gap: 4px;
    color: var(--text-muted);
    font-size: 11px;
  }

  .usage-pivot-chart {
    position: relative;
    height: 320px;
  }

  .usage-pivot-table {
    min-width: max-content;
  }

  .usage-pivot-table :global(th),
  .usage-pivot-table :global(td) {
    white-space: nowrap;
  }

  .usage-pivot-total {
    font-weight: 600;
  }

  .usage-pivot-other {
    color: var(--text-muted);
    font-style: italic;
  }

  .usage-pivot-empty,
  .usage-pivot-note {
    margin: 8px 0 0;
    color: var(--text-muted);
    font-size: 12px;
  }

  @media (max-width: 768px) {
    .usage-pivot-controls label {
      flex: 1 1 140px;
    }
  }
</style>
