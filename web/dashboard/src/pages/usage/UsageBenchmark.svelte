<script>
  // Cost per active user: for a group (each direct child of it is one user,
  // its subtree summed), the monthly active users and the spread of their
  // cost, as a chart (median and p90 lines over active-user bars) or a table
  // of the monthly statistics. No per-user rows.
  import SegmentedControl from "$lib/components/atoms/SegmentedControl.svelte";
  import ChartCanvas from "$lib/components/molecules/ChartCanvas.svelte";
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import { access } from "$lib/stores/access.svelte.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import { chartColors } from "$lib/utils/chartTheme.js";
  import { formatCost } from "$lib/utils/format.js";
  import { usageFacets } from "./usageFacets.svelte.js";
  import { usageBenchmark } from "./usageBenchmark.svelte.js";
  import UserPathTreeSelect from "./UserPathTreeSelect.svelte";
  import {
    BENCHMARK_MONTH_OPTIONS,
    benchmarkChangeLabel,
    benchmarkChartConfig,
    benchmarkCostBasisOptions,
    benchmarkHasPricingGaps,
    benchmarkHasUsage,
    benchmarkMonthLabel,
    benchmarkMonths,
    benchmarkPricingGaps,
    benchmarkStatus,
    benchmarkUnattributedCost,
  } from "./benchmark-helpers.js";
  import * as m from "$lib/paraglide/messages.js";

  const scope = $derived(access.scoped ? access.userPath : "/");
  const data = $derived(usageBenchmark.data);
  const months = $derived(benchmarkMonths(data));
  const status = $derived(benchmarkStatus({ data, failed: usageBenchmark.failed }));
  const hasUsers = $derived(benchmarkHasUsage(data));
  const pricingGaps = $derived(benchmarkHasPricingGaps(data));
  const unattributed = $derived(benchmarkUnattributedCost(data));
  const basisOptions = $derived(benchmarkCostBasisOptions(runtimeConfig));

  // The tree picker selects several paths; a group is one, so the last pick
  // replaces the previous one and clearing falls back to the default group.
  let picked = $state(usageBenchmark.parent ? [usageBenchmark.parent] : []);
  function pickGroup(values) {
    const parent = values.length ? values[values.length - 1] : "";
    picked = parent ? [parent] : [];
    if (parent !== usageBenchmark.parent) usageBenchmark.set("parent", parent);
  }

  function buildChart() {
    if (!hasUsers) return null;
    return benchmarkChartConfig(chartColors(), data);
  }
</script>

<section class="model-chart-section usage-benchmark" aria-label={m.usage_benchmark_title()}>
  <div class="model-chart-header">
    <h3>{m.usage_benchmark_title()}</h3>
    {#if usageBenchmark.loading}<Spinner size={14} label={m.usage_benchmark_loading()} />{/if}
    <div class="usage-benchmark-actions">
      {#if basisOptions}
        <SegmentedControl
          ariaLabel={m.usage_benchmark_cost_basis()}
          options={basisOptions}
          value={usageBenchmark.costBasis}
          onchange={(basis) => usageBenchmark.set("costBasis", basis)}
        />
      {/if}
      <SegmentedControl
        ariaLabel={m.usage_benchmark_view()}
        options={[
          { value: "chart", label: m.usage_chart() },
          { value: "table", label: m.usage_table() },
        ]}
        value={usageBenchmark.view}
        onchange={(view) => (usageBenchmark.view = view)}
      />
    </div>
  </div>

  <div class="usage-benchmark-controls">
    <!-- A div, not a label: the picker holds buttons of its own. -->
    <div class="usage-benchmark-field usage-benchmark-group">
      <span>{m.usage_benchmark_group()}</span>
      <UserPathTreeSelect
        paths={usageFacets.options.userPaths}
        values={picked}
        onchange={pickGroup}
        placeholder={scope}
        {scope}
        ariaLabel={m.usage_benchmark_group()}
      />
    </div>
    <label>
      <span>{m.usage_benchmark_months()}</span>
      <select
        class="usage-log-select"
        value={String(usageBenchmark.months)}
        onchange={(e) => usageBenchmark.set("months", Number(e.currentTarget.value))}
      >
        {#each BENCHMARK_MONTH_OPTIONS as count (count)}
          <option value={String(count)}>{m.usage_benchmark_months_option({ count })}</option>
        {/each}
      </select>
    </label>
  </div>

  {#if status !== "ready"}
    <p class="usage-benchmark-empty">{status === "failed" ? m.usage_benchmark_failed() : m.usage_benchmark_empty()}</p>
  {:else}
    {#if pricingGaps}
      <p class="usage-benchmark-warning">{m.usage_benchmark_pricing_gaps_note()}</p>
    {/if}
    {#if unattributed > 0}
      <p class="usage-benchmark-warning">
        {m.usage_benchmark_unattributed_note({ cost: formatCost(unattributed), parent: data.parent })}
      </p>
    {/if}
    {#if !hasUsers}
      <p class="usage-benchmark-empty">{m.usage_benchmark_empty()}</p>
    {:else if usageBenchmark.view === "chart"}
      <div class="usage-benchmark-chart">
        <ChartCanvas build={buildChart} ariaLabel={m.usage_benchmark_title()} />
      </div>
    {:else}
      <div class="table-wrapper">
        <table class="data-table usage-benchmark-table">
          <thead>
            <tr>
              <th>{m.usage_benchmark_month()}</th>
              <th class="col-price">{m.usage_benchmark_active_users()}</th>
              <th class="col-price">{m.usage_benchmark_total()}</th>
              <th class="col-price">{m.usage_benchmark_mean()}</th>
              <th class="col-price">{m.usage_benchmark_median()}</th>
              <th class="col-price">{m.usage_benchmark_p90()}</th>
              <th class="col-price">{m.usage_benchmark_max()}</th>
              <th class="col-price">{m.usage_benchmark_median_change()}</th>
              <th class="col-price">{m.usage_benchmark_pricing_gaps()}</th>
              {#if unattributed > 0}<th class="col-price">{m.usage_benchmark_unattributed()}</th>{/if}
            </tr>
          </thead>
          <tbody>
            {#each months as month (month.month)}
              <tr class:usage-benchmark-partial={month.partial}>
                <td class="mono font-size-md">{benchmarkMonthLabel(month)}</td>
                <td class="col-price">{Number(month.active_users || 0).toLocaleString()}</td>
                <td class="col-price">{formatCost(month.total_cost)}</td>
                <td class="col-price">{month.mean == null ? "–" : formatCost(month.mean)}</td>
                <td class="col-price">{month.p50 == null ? "–" : formatCost(month.p50)}</td>
                <td class="col-price">{month.p90 == null ? "–" : formatCost(month.p90)}</td>
                <td class="col-price">{month.max == null ? "–" : formatCost(month.max)}</td>
                <td class="col-price">{benchmarkChangeLabel(month)}</td>
                <td class="col-price">{benchmarkPricingGaps(month)}</td>
                {#if unattributed > 0}<td class="col-price">{formatCost(month.unattributed_cost)}</td>{/if}
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {/if}
  <p class="usage-benchmark-note">{m.usage_benchmark_note()}</p>
</section>

<style>
  .usage-benchmark {
    margin-bottom: var(--space-stack);
  }

  .usage-benchmark-actions {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px;
    margin-left: auto;
  }

  .usage-benchmark-controls {
    display: flex;
    flex-wrap: wrap;
    gap: 12px;
    margin-bottom: 16px;
  }

  .usage-benchmark-controls label,
  .usage-benchmark-field {
    display: flex;
    flex-direction: column;
    gap: 4px;
    color: var(--text-muted);
    font-size: 11px;
  }

  .usage-benchmark-group {
    min-width: 220px;
  }

  .usage-benchmark-chart {
    position: relative;
    height: 320px;
  }

  .usage-benchmark-table :global(th),
  .usage-benchmark-table :global(td) {
    white-space: nowrap;
  }

  .usage-benchmark-partial {
    color: var(--text-muted);
    font-style: italic;
  }

  .usage-benchmark-warning {
    margin: 0 0 8px;
    color: var(--text);
    font-size: 12px;
  }

  .usage-benchmark-empty,
  .usage-benchmark-note {
    margin: 8px 0 0;
    color: var(--text-muted);
    font-size: 12px;
  }

  @media (max-width: 768px) {
    .usage-benchmark-controls label,
    .usage-benchmark-field {
      flex: 1 1 140px;
    }
  }
</style>
