<script>
  // Models page — grouped model inventory with virtual models (redirects /
  // load balancers / access policies), pricing overrides, failover and rate
  // limit entry points.
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import AuthBanner from "$lib/components/organisms/AuthBanner.svelte";
  import { untrack } from "svelte";
  import { auth } from "$lib/stores/auth.svelte.ts";
  import { router } from "$lib/stores/router.svelte.ts";
  import { modelsStore } from "$lib/stores/models.svelte.ts";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import { virtualModels } from "./virtualModels.svelte.js";
  import { pricingOverrides } from "./pricingOverrides.svelte.js";
  import ModelTable from "./ModelTable.svelte";
  import VirtualModelEditor from "./VirtualModelEditor.svelte";
  import PricingOverrideEditor from "./PricingOverrideEditor.svelte";
  import FailoverEditor from "./FailoverEditor.svelte";
  import FailoverDrafts from "./FailoverDrafts.svelte";
  import { failover } from "./failover.svelte.js";
  import RateLimitEditor from "$pages/rate-limits/RateLimitEditor.svelte";
  import RateLimitInspector from "$pages/rate-limits/RateLimitInspector.svelte";
  import { rateLimits } from "$pages/rate-limits/rateLimits.svelte.js";

  const PAGE = "models";

  // Page data: virtual models, pricing overrides and failover rules load on
  // boot/refresh; rate limit rules feed the gauge buttons, so they are
  // fetched on entering both the rate-limits and models pages.
  $effect(() => {
    void auth.refreshTick;
    if (router.page !== PAGE) return;
    virtualModels.fetchVirtualModels();
    pricingOverrides.fetchModelPricingOverrides();
    failover.fetchFailoverRules();
    rateLimits.fetchRateLimitsPage();
  });

  // Bounded incremental rendering: whenever the visible rows change (model
  // refetch, category switch, filter typing, virtual-model mutations),
  // restart the batched render window so a large catalog paints the loader
  // first and rows stream in. untrack keeps the window bookkeeping
  // (modelRenderLimit, modelsRendering) out of this effect's dependencies —
  // the batch loop writes them, and tracking them would restart forever.
  $effect(() => {
    const total = virtualModels.filteredDisplayModels.length;
    untrack(() => virtualModels.restartModelRendering(total));
    return () => virtualModels.stopModelRendering();
  });

  const authError = $derived(auth.needsAuth);
</script>

<div>
  <div class="page-header">
    <h2>Models</h2>
    {#if virtualModels.displayModels.length > 0}
      <div class="model-count">
        <span>
          {modelsStore.filter
            ? virtualModels.filteredDisplayModels.length + " / " + virtualModels.displayModels.length
            : virtualModels.displayModels.length}
        </span> models
      </div>
    {/if}
  </div>

  <AuthBanner />
  {#if !virtualModels.virtualModelsAvailable && !authError}
    <div class="alert alert-warning">Virtual models feature is unavailable.</div>
  {/if}
  {#if virtualModels.aliasError && !authError}
    <div class="alert alert-warning">{virtualModels.aliasError}</div>
  {/if}
  {#if pricingOverrides.modelPricingOverrideError && !authError && !pricingOverrides.modelPricingOverrideFormOpen}
    <div class="alert alert-warning">{pricingOverrides.modelPricingOverrideError}</div>
  {/if}

  {#if modelsStore.categories.length > 0}
    <div class="category-tabs">
      {#each modelsStore.categories as cat (cat.category)}
        <button
          type="button"
          class="category-tab"
          class:active={modelsStore.activeCategory === cat.category}
          onclick={() => modelsStore.selectCategory(cat.category)}
        >
          <span>{cat.display_name}</span>
          <span class="tab-count">{cat.count}</span>
        </button>
      {/each}
    </div>
  {/if}

  {#if virtualModels.displayModels.length > 0 || modelsStore.filter || virtualModels.virtualModelsAvailable}
    <div class="table-toolbar">
      <div class="table-toolbar-main">
        <FilterInput
          placeholder="Filter by provider, provider/model, alias, or owner..."
          label="Filter models by provider, provider/model, alias, or owner"
          bind:value={modelsStore.filter}
        />
      </div>
      <div class="table-toolbar-actions">
        {#if virtualModels.virtualModelsAvailable}
          <button
            type="button"
            class="btn btn-primary btn-with-icon alias-create-btn"
            aria-label="New virtual model alias"
            title="Alias"
            onclick={() => virtualModels.openVirtualModelCreate()}
          >
            <Icon name="plus" class="alias-create-icon" />
            <span>New&nbsp;virtual&nbsp;model</span>
          </button>
        {/if}
      </div>
    </div>
  {/if}

  {#if virtualModels.modelsBusy() && !authError}
    <LoadingState label={virtualModels.modelLoadingText()} class="models-loading-state" />
  {/if}

  <VirtualModelEditor />
  <PricingOverrideEditor />

  {#if virtualModels.displayModels.length > 0 || modelsStore.filter}
    <ModelTable />
  {/if}

  {#if virtualModels.displayModels.length === 0 && !modelsStore.loading && !authError && !modelsStore.filter && (modelsStore.activeCategory === "all" || !modelsStore.activeCategory)}
    <p class="empty-state">No models registered.</p>
  {/if}
  {#if virtualModels.displayModels.length === 0 && !modelsStore.loading && !authError && !modelsStore.filter && modelsStore.activeCategory && modelsStore.activeCategory !== "all"}
    <p class="empty-state">No models in this category.</p>
  {/if}
  {#if virtualModels.displayModels.length > 0 && virtualModels.filteredDisplayModels.length === 0 && modelsStore.filter}
    <p class="empty-state">No models match your filter.</p>
  {/if}

  <RateLimitInspector />
  <RateLimitEditor />
  <FailoverEditor />
  <FailoverDrafts />
</div>

<style>
.category-tabs {
    display: flex;
    align-items: center;
    gap: 4px;
    margin-bottom: 16px;
    overflow-x: auto;
    -webkit-overflow-scrolling: touch;
    padding-bottom: 2px;
  }

.category-tab {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 6px 14px;
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--text-muted);
    font-size: 13px;
    font-family: inherit;
    font-weight: 500;
    cursor: pointer;
    transition: all 0.15s;
    white-space: nowrap;
    flex-shrink: 0;
  }

.category-tab:hover {
    color: var(--text);
    background: var(--bg-surface-hover);
  }

.category-tab.active {
    background: var(--accent);
    color: #fff;
    border-color: var(--accent);
  }

.category-tab .tab-count {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 20px;
    height: 18px;
    padding: 0 5px;
    background: rgba(255, 255, 255, 0.15);
    border-radius: 9px;
    font-size: 11px;
    font-weight: 600;
    line-height: 1;
  }

.category-tab:not(.active) .tab-count {
    background: var(--bg);
  }

@media (max-width: 768px) {
  /* Category tabs mobile */
  .category-tabs {
          gap: 4px;
        }

  .category-tab {
          padding: 5px 10px;
          font-size: 12px;
        }
}
</style>
