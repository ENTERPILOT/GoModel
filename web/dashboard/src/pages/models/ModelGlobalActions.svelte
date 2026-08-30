<script>
  // Global-scope action buttons shown in every table's actions header.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import { virtualModels } from "./virtualModels.svelte.js";
  import { virtualModelEditor } from "./virtualModelEditor.svelte.js";
  import { pricingOverrides } from "./pricingOverrides.svelte.js";
  import {
  modelOverrideEditButtonClass,
  modelOverrideEditButtonLabel,
} from "./displayRows.js";
  import AccessToggle from "./AccessToggle.svelte";
  import { CircleDollarSign, Pencil } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
</script>

<div class="alias-actions-cell model-list-actions">
  {#if virtualModels.virtualModelsAvailable}
    <AccessToggle row={virtualModels.globalScopeRow} />
  {/if}
  {#if pricingOverrides.modelPricingOverridesAvailable}
    <TableActionButton
      label={pricingOverrides.modelPricingButtonLabel(m.models_global_pricing(), pricingOverrides.hasGlobalPricingOverride())}
      class="table-icon-btn {pricingOverrides.modelPricingButtonClass(pricingOverrides.hasGlobalPricingOverride())}"
      onclick={() => pricingOverrides.openGlobalPricingOverrideEdit()}
    >
      <Icon icon={CircleDollarSign} class="table-icon-svg" />
    </TableActionButton>
  {/if}
  {#if virtualModels.virtualModelsAvailable}
    <TableActionButton
      label={modelOverrideEditButtonLabel(m.models_global_access(), virtualModels.hasGlobalModelOverride())}
      class="table-icon-btn {modelOverrideEditButtonClass(virtualModels.hasGlobalModelOverride())}"
      onclick={() => virtualModelEditor.openGlobalModelOverrideEdit()}
    >
      <Icon icon={Pencil} class="table-icon-svg" />
    </TableActionButton>
  {/if}
</div>
