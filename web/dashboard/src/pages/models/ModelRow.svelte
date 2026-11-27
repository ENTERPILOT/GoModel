<script>
  // One model/alias table row.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import HighlightText from "$lib/components/atoms/HighlightText.svelte";
  import { modelsStore } from "$lib/stores/models.svelte.js";
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
  rowSlowdown,
  rowUpstreamTimeouts,
  slowdownLabel,
  upstreamTimeoutsLabel,
} from "./displayRows.js";
import {
  aliasRouting,
  isAutoStrategy,
  maskingFailsOver,
  maskingRoutingKind,
  maskingRoutingLabel,
} from "./routing.js";
  import AccessToggle from "./AccessToggle.svelte";
  import ModelDetails from "./ModelDetails.svelte";
  import { modelDetailsState } from "./modelDetails.svelte.js";
  import { rowHasModelDetails } from "./modelDetails.js";
  import {
    Activity,
    ArrowRight,
    ChevronRight,
    CircleDollarSign,
    Gauge,
    Layers,
    Pencil,
    PiggyBank,
    Puzzle,
    Repeat,
    ShieldCheck,
    Split,
    Trash2,
  } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  // columns: the active category's column spec from categoryColumns.js
  // (ModelTable renders the matching <thead> from the same spec).
  // colspan: the full table width, spanned by the details row.
  let { row, columns, colspan } = $props();

  // The model name doubles as the details accordion toggle whenever the row
  // has a real model behind it; the details render as a second table row.
  const detailsAvailable = $derived(rowHasModelDetails(row));
  const expanded = $derived(detailsAvailable && modelDetailsState.isExpanded(row));
  const detailsID = $derived("model-details-" + encodeURIComponent(String(row.key || "")));

  // A virtual model reads on one line: its name, how it picks a target, and
  // the targets. Each strategy has its own icon so a list of them scans fast.
  const STRATEGY_ICONS = {
    round_robin: Repeat,
    cost: PiggyBank,
    failover: ShieldCheck,
    adaptive: Activity,
    auto: Layers,
    plugin: Puzzle,
  };
  const aliasRoute = $derived(row.is_alias ? aliasRouting(row.alias) : null);

  const pricing = $derived(pricingOverrides.modelRowPricing(row));
  // What the virtual model over this real model does with its requests: the
  // icon, the secondary line, and the edit/remove labels all follow it.
  const globalFailover = $derived(runtimeConfig.booleanFlag("FAILOVER_ENABLED", true));
  const routingKind = $derived(
    row.masking_alias ? maskingRoutingKind(row.masking_alias, globalFailover) : "",
  );
  // An auto route with the model among its targets picks by level rather
  // than spreading requests, so it gets its own words.
  const routedByLevel = $derived(
    routingKind === "balanced" && isAutoStrategy(row.masking_alias.strategy),
  );
  const routingPrefix = $derived(
    routingKind === "failover"
      ? m.models_falls_back_to()
      : routedByLevel
        ? m.models_routed_by_level()
        : routingKind === "balanced"
          ? m.models_balanced_with()
          : m.models_redirects_to(),
  );
  const routingTitle = $derived.by(() => {
    if (routingKind === "failover") return m.models_failover_title();
    if (routedByLevel) return m.models_auto_title();
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

  const slowdown = $derived(slowdownLabel(rowSlowdown(row)));
  const timeoutsLabel = $derived(upstreamTimeoutsLabel(rowUpstreamTimeouts(row)));
</script>

<tr id={rowAnchorID(row) || undefined} class={displayRowClass(row)}>
  <td class="model-name-col">
    <div class="model-name-cell">
      <div class="model-name-primary">
        {#if detailsAvailable}
          <button
            type="button"
            class="model-details-toggle"
            class:is-expanded={expanded}
            aria-expanded={expanded}
            aria-controls={expanded ? detailsID : undefined}
            title={expanded
              ? m.models_hide_details({ name: row.display_name })
              : m.models_show_details({ name: row.display_name })}
            onclick={() => modelDetailsState.toggle(row)}
          >
            <Icon icon={ChevronRight} class="model-details-toggle-icon" />
            <span class="mono font-size-md"><HighlightText text={row.display_name} needle={modelsStore.filter} /></span>
          </button>
        {:else}
          <span class="mono font-size-md"><HighlightText text={row.display_name} needle={modelsStore.filter} /></span>
        {/if}
        {#if !row.is_alias && row.masking_alias}
          <span class="model-kind-icon" role="img" aria-label={routingTitle} title={routingTitle}>
            {#if routingKind === "failover"}
              <Icon icon={ShieldCheck} class="model-kind-icon-svg" />
            {:else if routedByLevel}
              <Icon icon={Layers} class="model-kind-icon-svg" />
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
        {#if aliasRoute}
          {#if aliasRoute.kind}
            <span class="alias-strategy">
              <Icon icon={STRATEGY_ICONS[aliasRoute.kind] || Split} class="alias-route-icon" />
              {aliasRoute.label}
            </span>
          {:else}
            <span class="alias-strategy" role="img" aria-label={m.models_virtual_model()}>
              <Icon icon={ArrowRight} class="alias-route-icon" />
            </span>
          {/if}
          <span class="alias-targets mono" title={aliasRoute.all}>
            {#if aliasRoute.levels}
              <!-- An auto route: each target the row names carries its level. -->
              <span aria-hidden="true">
                {#each aliasRoute.levels as target, i (i)}
                  {i > 0 ? ", " : ""}<HighlightText text={target.name} needle={modelsStore.filter} />
                  {#if target.level}
                    <span class="alias-target-level">{target.level}</span>
                  {/if}
                {/each}
                {aliasRoute.more > 0 ? " +" + aliasRoute.more : ""}
              </span>
            {:else}
              <span aria-hidden="true"><HighlightText text={aliasRoute.targets} needle={modelsStore.filter} /></span>
            {/if}
            <span class="alias-targets-full">{m.models_targets()}: {aliasRoute.all}</span>
          </span>
        {/if}
      </div>
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
      {#if slowdown}
        <div class="model-name-secondary">
          {m.models_slowdown()} <span class="mono font-size-md">{slowdown}</span>
        </div>
      {/if}
      {#if timeoutsLabel}
        <div class="model-name-secondary">
          {m.models_upstream_timeouts()} <span class="mono font-size-md">{timeoutsLabel}</span>
        </div>
      {/if}
    </div>
  </td>
  {#each columns as col, i (i)}
    {@const hint = col.hint ? col.hint(row, pricing) : ""}
    <td class={col.class} title={hint || undefined}>
      {col.value(row, pricing)}{#if hint}<span class="price-hint" aria-hidden="true">*</span><span class="price-hint-text">{hint}</span>{/if}
    </td>
  {/each}
  <td class="model-row-actions col-actions">
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
{#if expanded}
  <ModelDetails {row} {colspan} id={detailsID} />
{/if}

<style>
  /* Rows sit indented under their provider header so groups and their
     models read apart at a glance. */
  .model-name-col {
    padding-left: calc(var(--space-cell) + 22px);
  }


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

  .alias-strategy {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 12px;
    color: var(--text-muted);
    white-space: nowrap;
  }

  /* The full target list, visually hidden: assistive tech reads it in place
     of the shortened text, which a pointer user expands with the title. */
  .alias-targets-full {
    position: absolute;
    width: 1px;
    height: 1px;
    margin: -1px;
    padding: 0;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
    border: 0;
  }

  /* The targets take what is left of the line and cut off with an ellipsis
     rather than wrap; the title carries the full list. */
  .alias-targets {
    flex: 1 1 0;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 12px;
    color: var(--text-muted);
  }

  /* The level of one target of an auto route: a small pill after its name. */
  .alias-target-level {
    padding: 0 6px;
    border: 1px solid var(--border);
    border-radius: 999px;
    font-size: 10px;
    font-weight: 600;
    letter-spacing: 0.2px;
    text-transform: uppercase;
  }

  /* :global so the rule reaches the SVG rendered by the Icon component. */
  .model-name-primary :global(.alias-route-icon) {
    flex: 0 0 auto;
    width: 14px;
    height: 14px;
    color: var(--accent);
  }

  .model-details-toggle {
    appearance: none;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    margin: 0;
    padding: 0;
    border: 0;
    background: none;
    color: inherit;
    font: inherit;
    text-align: left;
    cursor: pointer;
  }

  .model-details-toggle:hover {
    color: var(--accent);
  }

  .model-details-toggle:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--accent) 32%, transparent);
    outline-offset: 2px;
    border-radius: 4px;
  }

  /* The class rides on the Icon child's own <svg>, so it needs :global. */
  .model-details-toggle :global(.model-details-toggle-icon) {
    flex: 0 0 auto;
    width: 14px;
    height: 14px;
    color: var(--text-muted);
    transition: transform 0.2s ease;
  }

  .model-details-toggle.is-expanded :global(.model-details-toggle-icon) {
    transform: rotate(90deg);
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
    .model-name-col {
        padding-left: calc(var(--space-cell) + 16px);
      }


    .model-name-primary {
        flex-direction: column;
        align-items: flex-start;
      }

    /* Stacked, the targets get a line of their own. */
    .alias-targets {
        flex: none;
        max-width: 100%;
      }

    .model-row-actions {
        width: auto;
      }
  }
</style>
