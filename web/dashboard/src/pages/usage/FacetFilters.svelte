<script>
  // Page-level data filters: every widget on the usage page follows them.
  // Each facet dropdown honors every filter except its own (faceted search).
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import { debounced } from "$lib/utils/debounce.js";
  import { usagePage } from "./usage.svelte.js";

  const onUserPathInput = debounced(() => usagePage.onUsageFilterChanged());
  $effect(() => onUserPathInput.cancel);
</script>

<div class="usage-page-filters" role="group" aria-label="Usage data filters">
  <select
    bind:value={usagePage.usageFilterModel}
    onchange={() => usagePage.onUsageFilterChanged()}
    class="usage-log-select"
    aria-label="Filter by model"
  >
    <option value="">All Models</option>
    {#each usagePage.usageFilterModelOptions() as m (m)}
      <option value={m}>{m}</option>
    {/each}
  </select>
  <select
    bind:value={usagePage.usageFilterProvider}
    onchange={() => usagePage.onUsageFilterChanged()}
    class="usage-log-select"
    aria-label="Filter by provider"
  >
    <option value="">All Providers</option>
    {#each usagePage.usageFilterProviderOptions() as p (p)}
      <option value={p}>{p}</option>
    {/each}
  </select>
  {#if usagePage.usageFilterLabelOptions().length > 0}
    <select
      bind:value={usagePage.usageFilterLabel}
      onchange={() => usagePage.onUsageFilterChanged()}
      class="usage-log-select"
      aria-label="Filter by label"
    >
      <option value="">All Labels</option>
      {#each usagePage.usageFilterLabelOptions() as l (l)}
        <option value={l}>{l}</option>
      {/each}
    </select>
  {/if}
  <FilterInput
    class="usage-page-filters-user-path"
    placeholder="User path /team/alpha"
    label="Filter by user path"
    bind:value={usagePage.usageFilterUserPath}
    oninput={onUserPathInput}
  />
</div>

<style>
  /* Styles owned by this component (moved from dashboard.css). */
  /* Page-level filter bar under the Usage Analytics header. Every widget on the
     page (cards, charts, request log) follows these filters. */
  .usage-page-filters {
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
    margin-bottom: 20px;
  }

  .usage-page-filters :global(.usage-log-select) {
    flex: 0 1 auto;
  }

  /* The modifier class rides on FilterInput's own wrapper, so it has to be
     reached through :global from here. */
  .usage-page-filters :global(.usage-page-filters-user-path) {
    flex: 1 1 220px;
    min-width: 180px;
    max-width: 360px;
  }

  @media (max-width: 768px) {
    .usage-page-filters :global(.usage-log-select),
    .usage-page-filters :global(.usage-page-filters-user-path) {
        flex: 1 1 100%;
        max-width: none;
      }
  }
</style>
