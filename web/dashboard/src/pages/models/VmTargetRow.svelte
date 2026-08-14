<script>
  // One target row of the virtual-model editor. The primary target and the
  // extra {#each} rows share this shape; two or more rows make the redirect
  // a load balancer (weights show only for round-robin).
  import Icon from "$lib/components/atoms/Icon.svelte";
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import { virtualModels as vm } from "./virtualModels.svelte.js";
  import { Trash2 } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  let {
    model = $bindable(""),
    weight = $bindable(),
    id = undefined,
    placeholder = "openai/gpt-4o",
    showRemove = true,
    onremove,
  } = $props();
</script>

<div class="vm-target-row">
  <input
    {id}
    type="text"
    list="virtual-model-target-options"
    class="mono vm-target-model"
    {placeholder}
    bind:value={model}
    disabled={vm.vmFormManaged}
    aria-label={m.models_target_model()}
  />
  {#if vm.vmFormShowWeights()}
    <input
      type="number"
      min="1"
      step="1"
      class="mono vm-target-weight"
      placeholder={m.models_weight()}
      bind:value={weight}
      disabled={vm.vmFormManaged}
      required
      aria-label={m.models_target_weight()}
    />
  {/if}
  {#if showRemove}
    <TableActionButton
      label={m.models_remove_target()}
      class="table-action-btn-danger table-icon-btn vm-target-remove"
      onclick={onremove}
      disabled={vm.vmFormManaged}
    >
      <Icon icon={Trash2} class="table-icon-svg" />
    </TableActionButton>
  {/if}
</div>
