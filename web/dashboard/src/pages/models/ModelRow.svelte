<script>
  // One model/alias table row.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import { virtualModels } from "./virtualModels.svelte.js";
  import { pricingOverrides } from "./pricingOverrides.svelte.js";
  import { failover } from "./failover.svelte.js";
  import { rateLimits } from "$pages/rate-limits/rateLimits.svelte.js";
  import {
    aliasRowCanRemove,
    aliasTargetLabel,
    displayRowClass,
    hasAccessOverride,
    modelOverrideEditButtonClass,
    modelOverrideEditButtonLabel,
    rowAnchorID,
    rowIsManaged,
    rowRedirectCanRemove,
  } from "./virtualModelsLogic.js";
  import AccessToggle from "./AccessToggle.svelte";

  // columns: the active category's column spec from categoryColumns.js
  // (ModelTable renders the matching <thead> from the same spec).
  let { row, columns } = $props();

  const pricing = $derived(pricingOverrides.modelRowPricing(row));
</script>

<tr id={rowAnchorID(row) || undefined} class={displayRowClass(row)}>
  <td>
    <div class="model-name-cell">
      <div class="model-name-primary">
        <span class="mono font-size-md">{row.display_name}</span>
        {#if row.is_alias}
          <span class="model-kind-icon" role="img" aria-label="Virtual model" title="Virtual model">
            <svg class="model-kind-icon-svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <path d="m12 3-1.9 5.8a2 2 0 0 1-1.3 1.3L3 12l5.8 1.9a2 2 0 0 1 1.3 1.3L12 21l1.9-5.8a2 2 0 0 1 1.3-1.3L21 12l-5.8-1.9a2 2 0 0 1-1.3-1.3L12 3Z"></path>
            </svg>
          </span>
        {/if}
        {#if !row.is_alias && row.masking_alias}
          <span class="model-kind-icon" role="img" aria-label="Redirect" title="Redirect">
            <svg class="model-kind-icon-svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <circle cx="6" cy="19" r="3"></circle>
              <path d="M9 19h8.5a3.5 3.5 0 0 0 0-7h-11a3.5 3.5 0 0 1 0-7H15"></path>
              <circle cx="18" cy="5" r="3"></circle>
            </svg>
          </span>
        {/if}
        {#if rowIsManaged(row)}
          <span class="alias-kind-badge" title="Managed by config.yaml / VIRTUAL_MODELS">Config</span>
        {/if}
      </div>
      {#if row.is_alias}
        <div class="model-name-secondary">
          Targets <span class="mono font-size-md">{row.secondary_name}</span>
        </div>
      {/if}
      {#if !row.is_alias && row.masking_alias}
        <div class="model-name-secondary">
          Redirects to <span class="mono font-size-md">{aliasTargetLabel(row.masking_alias)}</span>
          {#if virtualModels.virtualModelsAvailable && rowRedirectCanRemove(row)}
            <button
              type="button"
              class="model-redirect-remove-btn mono"
              aria-label={virtualModels.rowDeletingKey === row.key
                ? "Removing redirect for " + row.display_name
                : "Remove redirect for " + row.display_name}
              title={virtualModels.rowDeletingKey === row.key
                ? "Removing redirect for " + row.display_name
                : "Remove redirect for " + row.display_name}
              disabled={Boolean(virtualModels.rowDeletingKey)}
              onclick={() => virtualModels.removeRedirectRow(row)}
            >[remove]</button>
          {/if}
        </div>
      {/if}
    </div>
  </td>
  {#each columns as col, i (i)}
    <td class={col.class}>{col.value(row, pricing)}</td>
  {/each}
  <td class="model-row-actions">
    {#if row.is_alias}
      <div class="alias-actions-cell model-list-actions">
        <AccessToggle {row} />
        {#if virtualModels.virtualModelsAvailable && aliasRowCanRemove(row)}
          <TableActionButton
            label={virtualModels.rowDeletingKey === row.key ? "Removing alias " + row.alias.name : "Remove alias " + row.alias.name}
            class="table-action-btn-danger table-icon-btn"
            onclick={() => virtualModels.removeAliasRow(row)}
            disabled={Boolean(virtualModels.rowDeletingKey)}
          >
            <Icon name="trash-2" class="table-icon-svg" />
          </TableActionButton>
        {/if}
        {#if virtualModels.virtualModelsAvailable}
          <TableActionButton
            label={"Edit alias " + row.alias.name}
            class="table-icon-btn table-action-btn-active"
            onclick={() => virtualModels.openVirtualModelEditAlias(row.alias)}
          >
            <Icon name="pencil" class="table-icon-svg" />
          </TableActionButton>
        {/if}
      </div>
    {:else}
      <div class="alias-actions-cell model-list-actions">
        <AccessToggle {row} />
        {#if pricingOverrides.modelPricingOverridesAvailable}
          <TableActionButton
            label={pricingOverrides.modelPricingButtonLabel( "model pricing for " + row.display_name, pricingOverrides.hasModelPricingOverride(row), )}
            class="table-icon-btn {pricingOverrides.modelPricingButtonClass(pricingOverrides.hasModelPricingOverride(row))}"
            onclick={() => pricingOverrides.openModelPricingOverrideEdit(row)}
          >
            <Icon name="circle-dollar-sign" class="table-icon-svg" />
          </TableActionButton>
        {/if}
        {#if failover.failoverAvailable && failover.failoverEnabled()}
          <TableActionButton
            label={failover.failoverButtonLabel(row)}
            class="table-icon-btn {failover.failoverButtonClass(row)}"
            onclick={() => failover.openFailoverForModel(row)}
          >
            <Icon name="shuffle" class="table-icon-svg" />
          </TableActionButton>
        {/if}
        {#if rateLimits.rateLimitsEnabled() && rateLimits.rateLimitInspectorModelID(row)}
          <TableActionButton
            label={rateLimits.rateLimitGaugeTitle(row.display_name, rateLimits.rateLimitGaugeClassForModel(row))}
            class="table-icon-btn {rateLimits.rateLimitGaugeClassForModel(row)}"
            onclick={() => rateLimits.openRateLimitInspectorForModel(row)}
          >
            <Icon name="gauge" class="table-icon-svg" />
          </TableActionButton>
        {/if}
        {#if virtualModels.virtualModelsAvailable && row.masking_alias && row.masking_alias.name}
          <TableActionButton
            label={"Edit redirect for " + row.display_name}
            class="table-icon-btn table-action-btn-active"
            onclick={() => virtualModels.openVirtualModelEditAlias(row.masking_alias)}
          >
            <Icon name="pencil" class="table-icon-svg" />
          </TableActionButton>
        {/if}
        {#if virtualModels.virtualModelsAvailable && !row.masking_alias}
          <TableActionButton
            label={modelOverrideEditButtonLabel("model access for " + row.display_name, hasAccessOverride(row.access))}
            class="table-icon-btn {modelOverrideEditButtonClass(hasAccessOverride(row.access))}"
            onclick={() => virtualModels.openVirtualModelEditModel(row)}
          >
            <Icon name="pencil" class="table-icon-svg" />
          </TableActionButton>
        {/if}
      </div>
    {/if}
  </td>
</tr>

<style>
  /* Styles owned by this component (moved from dashboard.css). */
  .model-name-cell {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .model-name-primary {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
  }

  .model-name-secondary {
    font-size: 12px;
    color: var(--text-muted);
  }

  .model-redirect-remove-btn {
    appearance: none;
    margin-left: 4px;
    padding: 0;
    border: 0;
    background: none;
    color: var(--danger);
    font-size: 11px;
    cursor: pointer;
  }

  .model-redirect-remove-btn:hover:not(:disabled) {
    text-decoration: underline;
  }

  .model-redirect-remove-btn:disabled {
    opacity: 0.45;
    cursor: default;
  }

  .model-kind-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    flex: 0 0 24px;
    border: 1px solid color-mix(in srgb, var(--accent) 55%, var(--border));
    border-radius: 999px;
    background: var(--bg);
    color: var(--accent);
  }

  .model-kind-icon-svg {
    width: 14px;
    height: 14px;
  }

  .model-row-actions {
    text-align: right;
    width: 170px;
  }

  @media (max-width: 768px) {
    .model-name-primary {
        flex-direction: column;
        align-items: flex-start;
      }
  }
</style>
