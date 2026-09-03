<script>
  // Page-level data filters: every widget on the usage page follows them.
  // Each facet dropdown honors every filter except its own (faceted search).
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import { debounced } from "$lib/utils/debounce.js";
  import { access } from "$lib/stores/access.svelte.js";
  import { usagePage } from "./usage.svelte.js";
  import * as m from "$lib/paraglide/messages.js";

  const onUserPathInput = debounced(() => usagePage.onUsageFilterChanged());
  const onSessionInput = debounced(() => usagePage.onUsageFilterChanged());
  $effect(() => () => {
    onUserPathInput.cancel();
    onSessionInput.cancel();
  });
</script>

<div class="usage-page-filters" role="group" aria-label={m.usage_filters_label()}>
  <select
    bind:value={usagePage.usageFilterModel}
    onchange={() => usagePage.onUsageFilterChanged()}
    class="usage-log-select"
    aria-label={m.usage_filter_model()}
  >
    <option value="">{m.usage_all_models()}</option>
    {#each usagePage.usageFilterModelOptions() as m (m)}
      <option value={m}>{m}</option>
    {/each}
  </select>
  <select
    bind:value={usagePage.usageFilterProvider}
    onchange={() => usagePage.onUsageFilterChanged()}
    class="usage-log-select"
    aria-label={m.usage_filter_provider()}
  >
    <option value="">{m.usage_all_providers()}</option>
    {#each usagePage.usageFilterProviderOptions() as p (p)}
      <option value={p}>{p}</option>
    {/each}
  </select>
  {#if usagePage.usageFilterLabelOptions().length > 0}
    <select
      bind:value={usagePage.usageFilterLabel}
      onchange={() => usagePage.onUsageFilterChanged()}
      class="usage-log-select"
      aria-label={m.usage_filter_label()}
    >
      <option value="">{m.usage_all_labels()}</option>
      {#each usagePage.usageFilterLabelOptions() as l (l)}
        <option value={l}>{l}</option>
      {/each}
    </select>
  {/if}
  <!-- A scoped key's server-side default is its scope root: show it as the
       placeholder so an empty filter reads as that subtree. -->
  <FilterInput
    class="usage-page-filters-user-path"
    placeholder={access.scoped ? access.userPath : m.usage_filter_user_path_placeholder()}
    label={m.usage_filter_user_path()}
    bind:value={usagePage.usageFilterUserPath}
    oninput={onUserPathInput}
  />
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

  .usage-page-filters :global(.usage-page-filters-session) {
    flex: 1 1 220px;
    min-width: 180px;
    max-width: 360px;
  }

  @media (max-width: 768px) {
    .usage-page-filters :global(.usage-log-select),
    .usage-page-filters :global(.usage-page-filters-user-path),
    .usage-page-filters :global(.usage-page-filters-session) {
        flex: 1 1 100%;
        max-width: none;
      }
  }
</style>
