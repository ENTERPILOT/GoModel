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
      <InlineHelpSection copyId="budgets-help-copy" label="budgets help">
        {#snippet title()}<h2>Budgets</h2>{/snippet}
        {#snippet help()}
          Budgets are evaluated from tracked usage cost records for each user
          path subtree. Enforcement runs only when Budget is enabled for the
          active workflow.
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
          <Icon name="plus" class="form-action-icon" />
          <span>Create Budget</span>
        </button>
      {/if}
    </div>
  </div>

  <AuthBanner />

  {#if (!store.managementEnabled() || !store.budgetsAvailable) && !auth.authError}
    <div class="alert alert-warning">Budget management is unavailable.</div>
  {/if}
  {#if store.error && !auth.authError}
    <p class="form-error" role="alert" aria-live="assertive">{store.error}</p>
  {/if}
  {#if store.loading && !auth.authError}
    <LoadingState label="Loading budgets..." />
  {/if}

  {#if (store.budgets.length > 0 || store.filter) && store.budgetsAvailable && !auth.authError && !store.formOpen}
    <div class="table-toolbar">
      <div class="table-toolbar-main">
        <FilterInput
          id="budget-filter"
          placeholder="Filter by user path, label, or period..."
          label="Filter budgets by user path or period"
          bind:value={store.filter}
        />
      </div>
      <div class="table-toolbar-actions budget-sort-control">
        <label for="budget-sort-by">Sort by</label>
        <select
          id="budget-sort-by"
          class="usage-log-select budget-sort-select"
          aria-label="Sort budgets by"
          bind:value={store.sortBy}
        >
          <option value="subject">Scope &amp; Subject</option>
          <option value="period">Period</option>
        </select>
      </div>
    </div>
  {/if}

  <BudgetEditor />

  {#if filtered.length > 0 && store.budgetsAvailable && !auth.authError}
    <BudgetList budgets={filtered} />
  {/if}

  {#if store.budgets.length === 0 && !store.filter && !store.loading && !auth.authError && !store.error && store.budgetsAvailable && store.managementEnabled()}
    <p class="empty-state">No budgets configured yet.</p>
  {/if}
  {#if store.budgets.length > 0 && filtered.length === 0 && store.filter && !store.loading && !auth.authError && !store.error && store.budgetsAvailable && store.managementEnabled()}
    <p class="empty-state">No budgets match your filter.</p>
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
