<script>
  // Budgets page (list, editor, per-budget reset, delete flows).
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import AuthBanner from "$lib/components/organisms/AuthBanner.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { auth } from "$lib/stores/auth.svelte.js";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import BudgetList from "./BudgetList.svelte";
  import BudgetEditor from "./BudgetEditor.svelte";
  import { budgetsStore as store } from "./budgets.svelte.js";
  import { Plus } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  const PAGE = "budgets";

  // Re-fetch when the page becomes active or the API key changes.
  $effect(() => {
    void auth.refreshTick;
    if (router.page === PAGE) store.fetchBudgetsPage();
  });

  const filtered = $derived(store.filteredBudgets());
</script>

<div>
  <div class="page-header">
    <div>
      <InlineHelpSection copyId="budgets-help-copy" label={m.budgets_help_label()}>
        {#snippet title()}<h2>{m.budgets_title()}</h2>{/snippet}
        {#snippet help()}
          {m.budgets_help()}
        {/snippet}
      </InlineHelpSection>
    </div>
    <div class="page-header-controls">
      {#if store.managementEnabled() && store.budgetsAvailable && !auth.authError}
        <button
          type="button"
          class="btn btn-primary btn-with-icon"
          disabled={store.formSubmitting}
          onclick={() => store.openForm()}
        >
          <Icon icon={Plus} class="form-action-icon" />
          <span>{m.budgets_create()}</span>
        </button>
      {/if}
    </div>
  </div>

  <AuthBanner />

  {#if (!store.managementEnabled() || !store.budgetsAvailable) && !auth.authError}
    <div class="alert alert-warning">{m.budgets_unavailable()}</div>
  {/if}
  {#if store.error && !auth.authError}
    <p class="form-error" role="alert" aria-live="assertive">{store.error}</p>
  {/if}
  {#if store.loading && !auth.authError}
    <LoadingState label={m.budgets_loading()} />
  {/if}

  {#if (store.budgets.length > 0 || store.filter) && store.budgetsAvailable && !auth.authError && !store.formOpen}
    <div class="table-toolbar">
      <div class="table-toolbar-main">
        <FilterInput
          id="budget-filter"
          placeholder={m.budgets_filter_placeholder()}
          label={m.budgets_filter_label()}
          bind:value={store.filter}
        />
      </div>
      <div class="table-toolbar-actions budget-sort-control">
        <label for="budget-sort-by">{m.budgets_sort_by()}</label>
        <select
          id="budget-sort-by"
          class="usage-log-select budget-sort-select"
          aria-label={m.budgets_sort_label()}
          bind:value={store.sortBy}
        >
          <option value="subject">{m.budgets_sort_subject()}</option>
          <option value="period">{m.budgets_period()}</option>
        </select>
      </div>
    </div>
  {/if}

  <BudgetEditor />

  {#if filtered.length > 0 && store.budgetsAvailable && !auth.authError}
    <BudgetList budgets={filtered} />
  {/if}

  {#if store.budgets.length === 0 && !store.filter && !store.loading && !auth.authError && !store.error && store.budgetsAvailable && store.managementEnabled()}
    <p class="empty-state">{m.budgets_empty()}</p>
  {/if}
  {#if store.budgets.length > 0 && filtered.length === 0 && store.filter && !store.loading && !auth.authError && !store.error && store.budgetsAvailable && store.managementEnabled()}
    <p class="empty-state">{m.budgets_no_match()}</p>
  {/if}
</div>

<style>
  .budget-sort-control {
    align-items: center;
    gap: 8px;
  }

  .budget-sort-control :global(label) {
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 600;
    white-space: nowrap;
  }

  .budget-sort-select {
    background-color: var(--bg-surface);
    min-width: 132px;
  }

  .budget-sort-select:hover {
    background-color: var(--bg-surface-hover);
  }
</style>
