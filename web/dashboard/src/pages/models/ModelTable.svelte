<script>
  // Grouped models table for the active category — one component that
  // switches its columns per category.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import { modelsStore } from "$lib/stores/models.svelte.js";
  import { virtualModels } from "./virtualModels.svelte.js";
  import { pricingOverrides } from "./pricingOverrides.svelte.js";
  import { rateLimits } from "$pages/rate-limits/rateLimits.svelte.js";
  import {
    hasAccessOverride,
    modelOverrideEditButtonClass,
    modelOverrideEditButtonLabel,
  } from "./virtualModelsLogic.js";
  import AccessToggle from "./AccessToggle.svelte";
  import TableIcon from "./TableIcon.svelte";
  import ModelGlobalActions from "./ModelGlobalActions.svelte";
  import ModelRow from "./ModelRow.svelte";
  import { categoryColumns, categoryColspan } from "./categoryColumns.js";

  const category = $derived(modelsStore.activeCategory || "all");
  const columns = $derived(categoryColumns(category));
  const groupColspan = $derived(categoryColspan(category));
</script>

<div class="table-wrapper">
  <table class="data-table">
    <thead>
      <tr>
        <th>Model</th>
        {#each columns as col, i (i)}
          <th class={col.class}>
            {#each col.headerLines as line, li (li)}
              {#if li > 0}<br />{/if}{line}
            {/each}
          </th>
        {/each}
        <th class="model-actions-header"><ModelGlobalActions /></th>
      </tr>
    </thead>
    {#each virtualModels.filteredDisplayModelGroups as group (group.key)}
      <tbody>
        <tr class="provider-group-row">
          <td colspan={groupColspan}>
            <div class="provider-group-header">
              <div class="provider-group-meta">
                <div class="provider-group-title">
                  <span class="mono font-size-md">{group.display_name}</span>
                  {#if group.type_label}
                    <span class="provider-group-type">{"(" + group.type_label + ")"}</span>
                  {/if}
                  {#if group.item_count_label}
                    <span class="provider-group-count">{group.item_count_label}</span>
                  {/if}
                </div>
                {#if group.access_summary}
                  <div class="provider-group-summary">{group.access_summary}</div>
                {/if}
              </div>
              <div class="alias-actions-cell model-list-actions">
                {#if group.access.selector}
                  <AccessToggle row={group} />
                {/if}
                {#if pricingOverrides.modelPricingOverridesAvailable && group.provider_name}
                  <TableActionButton
                    label={pricingOverrides.modelPricingButtonLabel( "provider pricing for " + group.display_name, pricingOverrides.hasProviderPricingOverride(group), )}
                    class="table-icon-btn {pricingOverrides.modelPricingButtonClass(pricingOverrides.hasProviderPricingOverride(group))}"
                    onclick={() => pricingOverrides.openProviderPricingOverrideEdit(group)}
                  >
                    <TableIcon name="dollar" />
                  </TableActionButton>
                {/if}
                {#if rateLimits.rateLimitsEnabled() && group.provider_name}
                  <TableActionButton
                    label={rateLimits.rateLimitGaugeTitle( "provider " + group.display_name, rateLimits.rateLimitGaugeClassForProvider(group), )}
                    class="table-icon-btn {rateLimits.rateLimitGaugeClassForProvider(group)}"
                    onclick={() => rateLimits.openRateLimitInspectorForProvider(group)}
                  >
                    <TableIcon name="gauge" />
                  </TableActionButton>
                {/if}
                {#if virtualModels.virtualModelsAvailable && group.access.selector}
                  <TableActionButton
                    label={modelOverrideEditButtonLabel( "provider access for " + group.display_name, hasAccessOverride(group.access), )}
                    class="table-icon-btn {modelOverrideEditButtonClass(hasAccessOverride(group.access))}"
                    onclick={() => virtualModels.openProviderOverrideEdit(group)}
                  >
                    <TableIcon name="edit" />
                  </TableActionButton>
                {/if}
              </div>
            </div>
          </td>
        </tr>
        {#each group.rows as row (row.key)}
          <ModelRow {row} {columns} />
        {/each}
      </tbody>
    {/each}
  </table>
</div>
