<script>
  // One target row of the virtual-model editor. The primary target and the
  // extra {#each} rows share this shape; two or more rows make the redirect
  // a load balancer (weights show only for round-robin). The ☰ handle drags
  // a row onto another row to reorder the list; keyboard users get the same
  // via ArrowUp/ArrowDown on the focused handle.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import SearchSelect from "$lib/components/molecules/SearchSelect.svelte";
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import { virtualModelEditor as vm } from "./virtualModelEditor.svelte.js";
  import { moveFormTarget, vmFormShowWeights } from "./vmForm.js";
  import { GripVertical, Trash2 } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  // provider is the explicit provider a stored target may carry (API- or
  // config-created); it pins the target to a concrete model. The picker shows
  // the qualified name, and choosing another value drops the pin.
  let {
    provider = $bindable(""),
    model = $bindable(""),
    weight = $bindable(),
    id = undefined,
    placeholder = "openai/gpt-4o",
    showRemove = true,
    onremove,
    // index is this row's position in the flattened target list (primary
    // first); draggable turns on the reorder handle.
    index = undefined,
    draggable = false,
  } = $props();

  let moveHandle = $state(null);

  // After a keyboard move the editor asks for focus at the moved row's new
  // index; grab it here so repeated arrows walk the same model, not whatever
  // row now sits at the old index.
  $effect(() => {
    if (draggable && vm.vmFocusHandle === index && moveHandle) {
      moveHandle.focus();
      vm.clearVmFocusHandle();
    }
  });
</script>

<div
  class="vm-target-row"
  role="group"
  class:vm-target-dragging={vm.vmDragIndex === index}
  class:vm-target-drop={vm.vmDropIndex === index && vm.vmDragIndex !== null && vm.vmDragIndex !== index}
  ondragenter={(event) => {
    if (draggable && vm.vmDragIndex !== null) {
      event.preventDefault();
    }
  }}
  ondragover={(event) => {
    if (draggable && vm.vmDragIndex !== null) {
      event.preventDefault();
      event.dataTransfer.dropEffect = "move";
      vm.enterVmTargetDrop(index);
    }
  }}
  ondrop={(event) => {
    if (draggable && vm.vmDragIndex !== null) {
      event.preventDefault();
      vm.dropVmTarget(index);
    }
  }}
  ondragleave={(event) => {
    // Child elements fire dragleave when the cursor moves between them;
    // only clear the highlight when the cursor left the row itself.
    if (!event.currentTarget.contains(event.relatedTarget)) {
      vm.leaveVmTargetDrop(index);
    }
  }}
>
  {#if draggable}
    <span
      bind:this={moveHandle}
      class="vm-target-move"
      role="button"
      tabindex="0"
      aria-label={m.models_move_target()}
      title={m.models_move_target()}
      draggable="true"
      ondragstart={(event) => {
        event.dataTransfer.effectAllowed = "move";
        event.dataTransfer.setData("text/plain", String(index));
        vm.startVmTargetDrag(index);
      }}
      ondragend={() => vm.endVmTargetDrag()}
      onkeydown={(event) => {
        if (event.key === "ArrowUp" && index > 0) {
          event.preventDefault();
          moveFormTarget(vm.vmForm, index, index - 1);
          vm.requestVmFocusHandle(index - 1);
        } else if (
          event.key === "ArrowDown" &&
          index < vm.vmTargetCount() - 1
        ) {
          event.preventDefault();
          moveFormTarget(vm.vmForm, index, index + 1);
          vm.requestVmFocusHandle(index + 1);
        }
      }}
    >
      <Icon icon={GripVertical} class="table-icon-svg" />
    </span>
  {/if}
  <SearchSelect
    {id}
    class="vm-target-model"
    options={vm.vmTargetOptions()}
    bind:value={model}
    onchange={() => {
      provider = "";
    }}
    {placeholder}
    searchPlaceholder={m.models_target_search_placeholder()}
    ariaLabel={m.models_target_model()}
    disabled={vm.vmFormManaged}
    allowCustom
    mono
  />
  {#if vmFormShowWeights(vm.vmForm)}
    <input
      type="number"
      min="1"
      step="1"
      class="mono vm-target-weight"
      placeholder={m.models_weight()}
      title={m.models_weight_help()}
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

<style>
  .vm-target-move {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    cursor: grab;
    padding: 8px 4px 8px 10px;
    width: 24px;
    margin-left: -24px;
    color: var(--muted, var(--text));
    touch-action: none;
    user-select: none;
    -webkit-user-select: none;
  }

  .vm-target-move:active {
    cursor: grabbing;
  }

  .vm-target-row.vm-target-dragging {
    opacity: 0.4;
  }

  .vm-target-row.vm-target-drop {
    outline: 2px dashed var(--accent);
    outline-offset: 2px;
  }
</style>
