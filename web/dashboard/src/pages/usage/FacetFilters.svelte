<script>
  // Page-level data filters: every widget on the usage page follows them.
  // Each facet dropdown honors every filter except its own (faceted search).
  // Every dropdown picks several values: any of them matches within a
  // filter, and all filters apply together.
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import SearchSelect from "$lib/components/molecules/SearchSelect.svelte";
  import { debounced } from "$lib/utils/debounce.js";
  import { access } from "$lib/stores/access.svelte.js";
  import { usagePage } from "./usage.svelte.js";
  import UserPathTreeSelect from "./UserPathTreeSelect.svelte";
  import * as m from "$lib/paraglide/messages.js";

  const onSessionInput = debounced(() => usagePage.onUsageFilterChanged());
  // Multi-select lists stay open while picking several; refetch once the
  // picks settle rather than on every toggle.
  const onPicksChange = debounced(() => usagePage.onUsageFilterChanged());
  $effect(() => () => {
    onSessionInput.cancel();
    onPicksChange.cancel();
  });
</script>

<div class="usage-page-filters" role="group" aria-label={m.usage_filters_label()}>
  <SearchSelect
    class="usage-page-filters-select"
    options={usagePage.usageFilterModelOptions()}
    multiple
    mono
    bind:values={usagePage.usageFilterModels}
    onchange={onPicksChange}
    placeholder={m.usage_all_models()}
    searchPlaceholder={m.usage_search_models()}
    ariaLabel={m.usage_filter_model()}
  />
  <SearchSelect
    class="usage-page-filters-select"
    options={usagePage.usageFilterProviderOptions()}
    multiple
    bind:values={usagePage.usageFilterProviders}
    onchange={onPicksChange}
    placeholder={m.usage_all_providers()}
    searchPlaceholder={m.usage_search_providers()}
    ariaLabel={m.usage_filter_provider()}
  />
  {#if usagePage.usageFilterLabelOptions().length > 0}
    <SearchSelect
      class="usage-page-filters-select"
      options={usagePage.usageFilterLabelOptions()}
      multiple
      bind:values={usagePage.usageFilterLabels}
      onchange={onPicksChange}
      placeholder={m.usage_all_labels()}
      searchPlaceholder={m.usage_search_labels()}
      ariaLabel={m.usage_filter_label()}
    />
  {/if}
  {#if usagePage.usageFilterEndpointOptions().length > 0}
    <SearchSelect
      class="usage-page-filters-select"
      options={usagePage.usageFilterEndpointOptions()}
      multiple
      mono
      bind:values={usagePage.usageFilterEndpoints}
      onchange={onPicksChange}
      placeholder={m.usage_all_endpoints()}
      searchPlaceholder={m.usage_search_endpoints()}
      ariaLabel={m.usage_filter_endpoint()}
    />
  {/if}
  {#if usagePage.usageFilterAuthKeyOptions().length > 0}
    <SearchSelect
      class="usage-page-filters-select"
      options={usagePage.usageFilterAuthKeyOptions()}
      multiple
      bind:values={usagePage.usageFilterAuthKeys}
      onchange={onPicksChange}
      placeholder={m.usage_all_auth_keys()}
      searchPlaceholder={m.usage_search_auth_keys()}
      ariaLabel={m.usage_filter_auth_key()}
    />
  {/if}
  <!-- A scoped key's server-side default is its scope root: show it as the
       placeholder so an empty filter reads as that subtree. -->
  <div class="usage-page-filters-user-path">
    <UserPathTreeSelect
      paths={usagePage.usageFacetOptions.userPaths}
      bind:values={usagePage.usageFilterUserPaths}
      onchange={onPicksChange}
      placeholder={access.scoped ? access.userPath : m.usage_all_user_paths()}
      scope={access.scoped ? access.userPath : "/"}
      ariaLabel={m.usage_filter_user_path()}
    />
  </div>
  <FilterInput
    class="usage-page-filters-session"
    placeholder={m.usage_filter_session_placeholder()}
    label={m.usage_filter_session()}
    bind:value={usagePage.usageFilterSession}
    oninput={onSessionInput}
    clearLabel={m.usage_clear_session_filter({ session: usagePage.usageFilterSession })}
    onclear={() => {
      onSessionInput.cancel();
      usagePage.onUsageFilterChanged();
    }}
  />
</div>

<style>
  /* Page-level filter bar under the Usage Analytics header. Every widget on the
     page (cards, charts, request log) follows these filters. */
  .usage-page-filters {
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
    margin-bottom: 20px;
  }

  /* The modifier classes ride on the child components' own wrappers, so they
     have to be reached through :global from here. */
  .usage-page-filters :global(.usage-page-filters-select) {
    flex: 0 1 220px;
    min-width: 160px;
  }

  .usage-page-filters-user-path {
    display: flex;
    flex: 1 1 220px;
    min-width: 180px;
    max-width: 360px;
  }

  .usage-page-filters-user-path :global(.tree-select) {
    flex: 1 1 auto;
  }

  .usage-page-filters :global(.usage-page-filters-session) {
    flex: 1 1 220px;
    min-width: 180px;
    max-width: 360px;
  }

  @media (max-width: 768px) {
    .usage-page-filters :global(.usage-page-filters-select),
    .usage-page-filters-user-path,
    .usage-page-filters :global(.usage-page-filters-session) {
      flex: 1 1 100%;
      max-width: none;
    }
  }
</style>
