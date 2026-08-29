<script>
  // One model/alias table row.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import { virtualModels } from "./virtualModels.svelte.js";
  import { virtualModelEditor } from "./virtualModelEditor.svelte.js";
  import { pricingOverrides } from "./pricingOverrides.svelte.js";
  import { rateLimits } from "$pages/rate-limits/rateLimits.svelte.js";
  import {
  aliasRowCanRemove,
  displayRowClass,
  hasAccessOverride,
  modelOverrideEditButtonClass,
  modelOverrideEditButtonLabel,
  rowAnchorID,
  rowIsManaged,
  rowRedirectCanRemove,
} from "./displayRows.js";
import {
  maskingFailsOver,
  maskingRoutingKind,
  maskingRoutingLabel,
} from "./routing.js";
  import AccessToggle from "./AccessToggle.svelte";
  import { CircleDollarSign, Gauge, Pencil, ShieldCheck, Split, Trash2 } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  // columns: the active category's column spec from categoryColumns.js
  // (ModelTable renders the matching <thead> from the same spec).
  let { row, columns } = $props();

  const pricing = $derived(pricingOverrides.modelRowPricing(row));
  const configuredSlowdown = $derived(
    row.is_alias
      ? row.alias && row.alias.slowdown
      : row.masking_alias && row.masking_alias.slowdown != null
        ? row.masking_alias.slowdown
        : row.access && row.access.override && row.access.override.slowdown,
  );
  // What the virtual model over this real model does with its requests: the
  // icon, the secondary line, and the edit/remove labels all follow it.
  const globalFailover = $derived(runtimeConfig.booleanFlag("FAILOVER_ENABLED", true));
  const routingKind = $derived(
    row.masking_alias ? maskingRoutingKind(row.masking_alias, globalFailover) : "",
  );
  const routingPrefix = $derived(
    routingKind === "failover"
      ? m.models_falls_back_to()
      : routingKind === "balanced"
        ? m.models_balanced_with()
        : m.models_redirects_to(),
  );
  const routingTitle = $derived.by(() => {
    if (routingKind === "failover") return m.models_failover_title();
    if (routingKind === "balanced") {
      if (!globalFailover) return m.models_balanced_title_failover_global_off();
      return maskingFailsOver(row.masking_alias, globalFailover)
        ? m.models_balanced_title()
        : m.models_balanced_title_failover_off();
    }
    return m.models_redirect();
  });
  const routingEditLabel = $derived(
    routingKind === "failover"
      ? m.models_edit_failover({ name: row.display_name })
      : routingKind === "balanced"
        ? m.models_edit_balancing({ name: row.display_name })
        : m.models_edit_redirect({ name: row.display_name }),
  );
  const routingRemoveLabel = $derived(
    routingKind === "failover"
      ? m.models_remove_failover({ model: row.display_name })
      : routingKind === "balanced"
        ? m.models_remove_balancing({ model: row.display_name })
        : m.models_remove_redirect({ model: row.display_name }),
  );
  const routingRemovingLabel = $derived(
    routingKind === "failover"
      ? m.models_removing_failover({ model: row.display_name })
      : routingKind === "balanced"
        ? m.models_removing_balancing({ model: row.display_name })
        : m.models_removing_redirect({ model: row.display_name }),
  );

  const slowdown = $derived(
    Number(configuredSlowdown == null ? 0 : configuredSlowdown),
  );
</script>

<tr id={rowAnchorID(row) || undefined} class={displayRowClass(row)}>
  <td>
    <div class="model-name-cell">
      <div class="model-name-primary">
        <span class="mono font-size-md">{row.display_name}</span>
        {#if row.is_alias}
          <span class="model-kind-icon" role="img" aria-label={m.models_virtual_model()} title={m.models_virtual_model()}>
            <svg class="model-kind-icon-svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <path d="m12 3-1.9 5.8a2 2 0 0 1-1.3 1.3L3 12l5.8 1.9a2 2 0 0 1 1.3 1.3L12 21l1.9-5.8a2 2 0 0 1 1.3-1.3L21 12l-5.8-1.9a2 2 0 0 1-1.3-1.3L12 3Z"></path>
            </svg>
          </span>
        {/if}
        {#if !row.is_alias && row.masking_alias}
          <span class="model-kind-icon" role="img" aria-label={routingTitle} title={routingTitle}>
            {#if routingKind === "failover"}
              <Icon icon={ShieldCheck} class="model-kind-icon-svg" />
            {:else if routingKind === "balanced"}
              <Icon icon={Split} class="model-kind-icon-svg" />
            {:else}
              <svg class="model-kind-icon-svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <circle cx="6" cy="19" r="3"></circle>
                <path d="M9 19h8.5a3.5 3.5 0 0 0 0-7h-11a3.5 3.5 0 0 1 0-7H15"></path>
                <circle cx="18" cy="5" r="3"></circle>
              </svg>
            {/if}
          </span>
        {/if}
        {#if rowIsManaged(row)}
          <span class="alias-kind-badge" title={m.models_managed_config()}>{m.models_config()}</span>
        {/if}
      </div>
      {#if row.is_alias}
        <div class="model-name-secondary">
          {m.models_targets()} <span class="mono font-size-md">{row.secondary_name}</span>
        </div>
      {/if}
      {#if !row.is_alias && row.masking_alias}
        <div class="model-name-secondary">
          {routingPrefix} <span class="mono font-size-md">{maskingRoutingLabel(row.masking_alias, routingKind)}</span>
          {#if virtualModels.virtualModelsAvailable && rowRedirectCanRemove(row)}
            <button
              type="button"
              class="model-redirect-remove-btn mono"
              aria-label={virtualModels.rowDeletingKey === row.key ? routingRemovingLabel : routingRemoveLabel}
              title={virtualModels.rowDeletingKey === row.key ? routingRemovingLabel : routingRemoveLabel}
              disabled={Boolean(virtualModels.rowDeletingKey)}
              onclick={() => virtualModels.removeRedirectRow(row)}
            >[{m.models_remove()}]</button>
          {/if}
        </div>
      {/if}
      {#if slowdown > 0}
        <div class="model-name-secondary">
          {m.models_slowdown()} <span class="mono font-size-md">{m.models_inference_time({ value: slowdown })}</span>
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
            label={virtualModels.rowDeletingKey === row.key
              ? m.models_removing_alias({ name: row.alias.name })
              : m.models_remove_alias({ name: row.alias.name })}
            class="table-action-btn-danger table-icon-btn"
            onclick={() => virtualModels.removeAliasRow(row)}
            disabled={Boolean(virtualModels.rowDeletingKey)}
          >
            <Icon icon={Trash2} class="table-icon-svg" />
          </TableActionButton>
        {/if}
        {#if virtualModels.virtualModelsAvailable}
          <TableActionButton
            label={m.models_edit_alias({ name: row.alias.name })}
            class="table-icon-btn table-action-btn-active"
            onclick={() => virtualModelEditor.openVirtualModelEditAlias(row.alias)}
          >
            <Icon icon={Pencil} class="table-icon-svg" />
          </TableActionButton>
        {/if}
      </div>
    {:else}
      <div class="alias-actions-cell model-list-actions">
        <AccessToggle {row} />
        {#if pricingOverrides.modelPricingOverridesAvailable}
          <TableActionButton
            label={pricingOverrides.modelPricingButtonLabel(m.models_model_pricing_for({ name: row.display_name }), pricingOverrides.hasModelPricingOverride(row))}
            class="table-icon-btn {pricingOverrides.modelPricingButtonClass(pricingOverrides.hasModelPricingOverride(row))}"
            onclick={() => pricingOverrides.openModelPricingOverrideEdit(row)}
          >
            <Icon icon={CircleDollarSign} class="table-icon-svg" />
          </TableActionButton>
        {/if}
        {#if rateLimits.rateLimitsEnabled() && rateLimits.rateLimitInspectorModelID(row)}
          <TableActionButton
            label={rateLimits.rateLimitGaugeTitle(row.display_name, rateLimits.rateLimitGaugeClassForModel(row))}
            class="table-icon-btn {rateLimits.rateLimitGaugeClassForModel(row)}"
            onclick={() => rateLimits.openRateLimitInspectorForModel(row)}
          >
            <Icon icon={Gauge} class="table-icon-svg" />
          </TableActionButton>
        {/if}
        {#if virtualModels.virtualModelsAvailable && row.masking_alias && row.masking_alias.name}
          <TableActionButton
            label={routingEditLabel}
            class="table-icon-btn table-action-btn-active"
            onclick={() => virtualModelEditor.openVirtualModelEditAlias(row.masking_alias)}
          >
            <Icon icon={Pencil} class="table-icon-svg" />
          </TableActionButton>
        {/if}
        {#if virtualModels.virtualModelsAvailable && !row.masking_alias}
          <TableActionButton
            label={modelOverrideEditButtonLabel(m.models_model_settings_for({ name: row.display_name }), hasAccessOverride(row.access))}
            class="table-icon-btn {modelOverrideEditButtonClass(hasAccessOverride(row.access))}"
            onclick={() => virtualModelEditor.openVirtualModelEditModel(row)}
          >
            <Icon icon={Pencil} class="table-icon-svg" />
          </TableActionButton>
        {/if}
      </div>
    {/if}
  </td>
</tr>

<style>
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

  /* :global so the rule also reaches the SVG rendered by the Icon component. */
  .model-kind-icon :global(.model-kind-icon-svg) {
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
