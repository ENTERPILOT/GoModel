<script>
  // Request log: paginated per-request usage entries following the page's
  // window and facet filters.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import Spinner from "$lib/components/atoms/Spinner.svelte";
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

  let searchDebounce = null;

  function onSearchInput() {
    clearTimeout(searchDebounce);
    searchDebounce = setTimeout(() => usagePage.fetchUsageLog(true), 300);
  }

  $effect(() => () => clearTimeout(searchDebounce));
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
      <div class="filter-input-wrap">
        <Icon name="search" class="filter-input-icon" />
        <input
          type="text"
          placeholder="Search by request ID, model, provider..."
          aria-label="Search by request ID, model, provider"
          bind:value={usagePage.usageLogSearch}
          oninput={onSearchInput}
          class="filter-input"
        />
      </div>
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
      <svg
        class="empty-state-icon"
        viewBox="0 0 220 262"
        fill="none"
        role="img"
        aria-label="No data"
        xmlns="http://www.w3.org/2000/svg"
      >
        <!-- GoModel hexagon mark: sharp mitered edges like the logotype -->
        <path
          d="M110 20 L187.9 65 L187.9 155 L110 200 L32.1 155 L32.1 65 Z"
          stroke="var(--accent)"
          stroke-width="5"
          stroke-linejoin="miter"
          opacity="0.5"
        ></path>
        <!-- faint chart gridlines -->
        <path d="M60 123 H160" stroke="currentColor" stroke-width="2" stroke-dasharray="1.5 7" stroke-linecap="round" opacity="0.18"></path>
        <path d="M60 93 H160" stroke="currentColor" stroke-width="2" stroke-dasharray="1.5 7" stroke-linecap="round" opacity="0.18"></path>
        <!-- empty chart baseline (the floor), level with the hexagon's lower shoulders -->
        <path d="M50 155 H170" stroke="currentColor" stroke-width="5" stroke-linecap="round" opacity="0.6"></path>
        <!-- ghosted placeholder bars, floating just above the floor -->
        <rect x="57" y="113" width="14" height="36" rx="4" stroke="currentColor" stroke-width="2.8" stroke-dasharray="5 4" opacity="0.55"></rect>
        <rect x="80" y="91" width="14" height="58" rx="4" stroke="currentColor" stroke-width="2.8" stroke-dasharray="5 4" opacity="0.55"></rect>
        <rect x="103" y="75" width="14" height="74" rx="4" stroke="currentColor" stroke-width="2.8" stroke-dasharray="5 4" opacity="0.55"></rect>
        <rect x="126" y="97" width="14" height="52" rx="4" stroke="currentColor" stroke-width="2.8" stroke-dasharray="5 4" opacity="0.55"></rect>
        <rect x="149" y="119" width="14" height="30" rx="4" stroke="currentColor" stroke-width="2.8" stroke-dasharray="5 4" opacity="0.55"></rect>
        <!-- label -->
        <text x="110" y="250" text-anchor="middle" fill="currentColor" font-size="40">No data</text>
      </svg>
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
</style>
