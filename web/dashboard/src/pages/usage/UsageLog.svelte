<script>
  // Request log: paginated per-request usage entries following the page's
  // window and facet filters.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import NoDataIllustration from "$lib/components/atoms/NoDataIllustration.svelte";
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import { debounced } from "$lib/utils/debounce.js";
  import Pagination from "$lib/components/molecules/Pagination.svelte";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import {
    formatCost,
    formatNumber,
    formatTimestampUTC,
    providerDisplayValue,
  } from "$lib/utils/format.js";
  import { usagePage } from "./usage.svelte.js";
  import {
    cachedCostTitle,
    costSourceTooltip,
    entryLabels,
    formatCostTooltip,
    hasProviderCache,
    labelColor,
    providerCacheLabel,
    providerCacheTitle,
    usageEntryCacheLabel,
    usageEntryCached,
    usageLogHasLabels,
    usesResponseCostPricing,
  } from "./usage-helpers.js";

  const costsMode = $derived(usagePage.usageMode === "costs");
  const hasLabels = $derived(
    usageLogHasLabels(
      usagePage.labelUsage,
      usagePage.usageFilterLabel,
      usagePage.usageLog.entries,
    ),
  );

  const onSearchInput = debounced(() => usagePage.fetchUsageLog(true));
  $effect(() => onSearchInput.cancel);
</script>

{#snippet labelChips(entry)}
  {#if entryLabels(entry).length > 0}
    <div class="usage-label-chips">
      {#each entryLabels(entry) as label (label)}
        <button
          type="button"
          class="usage-label-chip"
          class:active={usagePage.usageFilterLabel === label}
          style="--label-color: {labelColor(label)}"
          title={usagePage.usageLabelChipTitle(label)}
          onclick={() => usagePage.toggleUsageLabelFilter(label)}
        >{label}</button>
      {/each}
    </div>
  {:else}
    <span>-</span>
  {/if}
{/snippet}

<div class="usage-log-section">
  <h3>Request Log</h3>
  <div class="usage-log-toolbar">
    <div class="usage-filter-row usage-filter-row-search">
      <FilterInput
        placeholder="Search by request ID, model, provider..."
        label="Search by request ID, model, provider"
        bind:value={usagePage.usageLogSearch}
        oninput={onSearchInput}
      />
    </div>
    <div class="usage-filter-row usage-filter-row-options">
      <label class="usage-log-checkbox">
        <input
          type="checkbox"
          bind:checked={usagePage.usageLogHideCached}
          onchange={() => usagePage.fetchUsageLog(true)}
        />
        <span>Hide cached requests</span>
      </label>
    </div>
  </div>
  {#if usagePage.usageLog.entries.length > 0}
    <div class="table-wrapper">
      <table class="data-table usage-log-table">
        <thead>
          <tr>
            <th>Timestamp</th>
            <th>Provider</th>
            <th>Model</th>
            <th>User Path</th>
            {#if hasLabels}
              <th>Labels</th>
            {/if}
            <th>Cache</th>
            <th title="Share of input tokens served from the provider's prompt cache">Provider Cache</th>
            <th class="col-price">{costsMode ? "Input Cost" : "Input"}</th>
            <th class="col-price">{costsMode ? "Output Cost" : "Output"}</th>
            <th class="col-price">{costsMode ? "Total Cost" : "Total"}</th>
            {#if !costsMode}
              <th class="col-price">Cost</th>
            {/if}
          </tr>
        </thead>
        <tbody>
          {#each usagePage.usageLog.entries as entry (entry.id)}
            <tr class:usage-log-row-cached={usageEntryCached(entry)}>
              <td class="mono usage-ts" title={formatTimestampUTC(entry.timestamp)}
                >{timezone.formatTimestamp(entry.timestamp)}</td
              >
              <td>
                <span class="provider-badge">{providerDisplayValue(entry) || "-"}</span>
              </td>
              <td class="mono font-size-md">{entry.model}</td>
              <td class="mono font-size-md">{entry.user_path || "-"}</td>
              {#if hasLabels}
                <td class="usage-log-labels-cell">
                  {@render labelChips(entry)}
                </td>
              {/if}
              <td class="usage-log-cache-cell">{usageEntryCacheLabel(entry)}</td>
              <td class="usage-log-provider-cache-cell" title={providerCacheTitle(entry)}>
                {#if hasProviderCache(entry)}
                  <span class="audit-prompt-cache-pill mono">{providerCacheLabel(entry)}</span>
                {:else}
                  <span>-</span>
                {/if}
              </td>
              <td
                class="col-price"
                title={costsMode ? formatNumber(entry.input_tokens) + " tokens" : ""}
                >{costsMode ? formatCost(entry.input_cost) : formatNumber(entry.input_tokens)}</td
              >
              <td
                class="col-price"
                title={costsMode ? formatNumber(entry.output_tokens) + " tokens" : ""}
                >{costsMode ? formatCost(entry.output_cost) : formatNumber(entry.output_tokens)}</td
              >
              <td class="col-price" title={costsMode ? cachedCostTitle(entry, "") : ""}>
                <span
                  title={costsMode
                    ? cachedCostTitle(
                        entry,
                        formatNumber(entry.total_tokens) + " tokens\n" + formatCostTooltip(entry),
                      )
                    : ""}
                  >{costsMode
                    ? formatCost(entry.total_cost)
                    : formatNumber(entry.total_tokens)}</span
                >
                {#if costsMode && usesResponseCostPricing(entry)}
                  <Icon
                    name="circle-dollar-sign"
                    class="cost-source-icon"
                    title={costSourceTooltip(entry)}
                  />
                {/if}
                {#if costsMode && usageEntryCached(entry)}
                  <Icon name="database-zap" class="cache-savings-icon" />
                {/if}
                {#if costsMode && entry.costs_calculation_caveat}
                  <span class="caveat-icon" title={entry.costs_calculation_caveat}>&#9888;</span>
                {/if}
              </td>
              {#if !costsMode}
                <td class="col-price" title={cachedCostTitle(entry, formatCostTooltip(entry))}>
                  <span>{formatCost(entry.total_cost)}</span>
                  {#if usesResponseCostPricing(entry)}
                    <Icon
                      name="circle-dollar-sign"
                      class="cost-source-icon"
                      title={costSourceTooltip(entry)}
                    />
                  {/if}
                  {#if usageEntryCached(entry)}
                    <Icon name="database-zap" class="cache-savings-icon" />
                  {/if}
                  {#if entry.costs_calculation_caveat}
                    <span class="caveat-icon" title={entry.costs_calculation_caveat}>&#9888;</span>
                  {/if}
                </td>
              {/if}
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else if usagePage.usageLogLoading}
    <!-- Loading affordance while the log fetch is in flight. -->
    <div class="empty-state usage-log-loading">
      <Spinner size={20} label="Loading request log" />
    </div>
  {:else}
    <div class="empty-state">
      <NoDataIllustration />
    </div>
  {/if}
  <Pagination
    total={usagePage.usageLog.total}
    offset={usagePage.usageLog.offset}
    limit={usagePage.usageLog.limit}
    onprev={() => usagePage.usageLogPrevPage()}
    onnext={() => usagePage.usageLogNextPage()}
  />
</div>

<style>
.usage-log-loading {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 120px;
  }

.usage-log-section {
  background: var(--bg-surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 24px;
}

.usage-log-section :global(h3) {
  font-size: 16px;
  font-weight: 600;
  margin-bottom: 16px;
}

/* The request log carries many columns (labels included); scroll sideways on
   narrow windows instead of clipping the trailing cost columns. */
.usage-log-section :global(.table-wrapper) {
  overflow-x: auto;
}

.usage-log-toolbar {
  display: grid;
  gap: 12px;
  margin-bottom: 16px;
}

.usage-filter-row {
  display: grid;
  grid-template-columns: repeat(12, minmax(0, 1fr));
  gap: 12px;
  align-items: center;
}

.usage-filter-row-search :global(.filter-input-wrap) {
  grid-column: 1 / -1;
}

.usage-filter-row-options {
  grid-template-columns: 1fr;
}

.usage-log-checkbox {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--text);
  cursor: pointer;
  user-select: none;
}

.usage-log-checkbox :global(input) {
  width: 16px;
  height: 16px;
  cursor: pointer;
}

.usage-ts {
  white-space: nowrap;
  font-size: 12px;
}

.usage-log-row-cached :global(td) {
  opacity: 0.75;
  font-style: italic;
}

.usage-log-row-cached .usage-log-cache-cell {
  font-weight: 700;
}

/* Caveat warning icon */
.caveat-icon {
  color: var(--warning);
  cursor: help;
  font-size: 14px;
  margin-left: 4px;
}

@media (max-width: 768px) {
  /* Usage page mobile */
  .usage-log-toolbar {
        gap: 10px;
      }

  .usage-filter-row {
        grid-template-columns: 1fr;
      }

  .usage-filter-row-search :global(.filter-input-wrap) {
        grid-column: 1;
      }

  .usage-log-table {
        display: block;
        overflow-x: auto;
      }
}
</style>
