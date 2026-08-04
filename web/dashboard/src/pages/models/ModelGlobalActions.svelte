<script>
  // Global-scope action buttons shown in every table's actions header.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import { virtualModels } from "./virtualModels.svelte.js";
  import { pricingOverrides } from "./pricingOverrides.svelte.js";
  import {
    modelOverrideEditButtonClass,
    modelOverrideEditButtonLabel,
  } from "./virtualModelsLogic.js";
  import AccessToggle from "./AccessToggle.svelte";
  import { CircleDollarSign, Pencil } from "lucide";
</script>

<div class="alias-actions-cell model-list-actions">
  {#if virtualModels.virtualModelsAvailable}
    <AccessToggle row={virtualModels.globalScopeRow} />
  {/if}
  {#if pricingOverrides.modelPricingOverridesAvailable}
    <TableActionButton
      label={pricingOverrides.modelPricingButtonLabel("global model pricing", pricingOverrides.hasGlobalPricingOverride())}
      class="table-icon-btn {pricingOverrides.modelPricingButtonClass(pricingOverrides.hasGlobalPricingOverride())}"
      onclick={() => pricingOverrides.openGlobalPricingOverrideEdit()}
    >
      <Icon icon={CircleDollarSign} class="table-icon-svg" />
    </TableActionButton>
  {/if}
  {#if virtualModels.virtualModelsAvailable}
    <TableActionButton
      label={modelOverrideEditButtonLabel("global model access", virtualModels.hasGlobalModelOverride())}
      class="table-icon-btn {modelOverrideEditButtonClass(virtualModels.hasGlobalModelOverride())}"
      onclick={() => virtualModels.openGlobalModelOverrideEdit()}
    >
      <Icon icon={Pencil} class="table-icon-svg" />
    </TableActionButton>
  {/if}
</div>
