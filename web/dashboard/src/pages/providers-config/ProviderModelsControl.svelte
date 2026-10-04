<script>
  // The provider's custom models, edited one per row like the virtual-model
  // targets, or as text for pasting a long list (commas or newlines). Both
  // views edit the same form.models rows; blank and repeated entries drop out
  // on save, and no models at all still means auto-discover.
  import { tick } from "svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import SegmentedControl from "$lib/components/atoms/SegmentedControl.svelte";
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import { providersConfig } from "./providersConfig.svelte.js";
  import { Plus, Trash2 } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  let { id, invalid = false, describedBy = undefined } = $props();

  const rows = $derived(providersConfig.form.models);
  const viewOptions = $derived([
    { value: "list", label: m.providers_models_view_list() },
    { value: "text", label: m.providers_models_view_text() },
  ]);

  function rowId(index) {
    return index === 0 ? id : id + "-" + index;
  }

  async function addRow(at) {
    const index = providersConfig.addModelRow(at);
    await tick();
    document.getElementById(rowId(index))?.focus();
  }

  // Enter starts the next model instead of submitting the whole dialog.
  function onRowKeydown(event, index) {
    if (event.key === "Enter" && !event.isComposing) {
      event.preventDefault();
      addRow(index + 1);
    }
  }
</script>

{#if providersConfig.modelsView === "text"}
  <textarea
    {id}
    rows="6"
    class="mono"
    placeholder={"gpt-4o\ngpt-4o-mini"}
    aria-invalid={invalid ? "true" : undefined}
    aria-describedby={describedBy}
    value={providersConfig.modelsText}
    oninput={(event) => providersConfig.setModelsText(event.currentTarget.value)}
  ></textarea>
{:else if rows.length > 0}
  <div class="vm-target-list provider-models-list">
    {#each providersConfig.form.models as row, index (index)}
      <div class="vm-target-row">
        <input
          id={rowId(index)}
          type="text"
          class="mono vm-target-model"
          placeholder="gpt-4o"
          aria-label={m.providers_model_entry({ number: index + 1 })}
          aria-invalid={invalid ? "true" : undefined}
          aria-describedby={index === 0 ? describedBy : undefined}
          bind:value={row.value}
          oninput={() => providersConfig.clearFieldError("models")}
          onkeydown={(event) => onRowKeydown(event, index)}
        />
        <TableActionButton
          label={m.providers_remove_model({ number: index + 1 })}
          class="table-action-btn-danger table-icon-btn vm-target-remove"
          onclick={() => providersConfig.removeModelRow(index)}
        >
          <Icon icon={Trash2} class="table-icon-svg" />
        </TableActionButton>
      </div>
    {/each}
  </div>
{/if}
<div class="failover-target-actions provider-models-actions">
  {#if providersConfig.modelsView === "list"}
    <button
      type="button"
      id={rows.length === 0 ? id : undefined}
      class="btn btn-with-icon"
      onclick={() => addRow()}
    >
      <Icon icon={Plus} class="form-action-icon" />
      <span>{m.providers_add_model()}</span>
    </button>
  {/if}
  <SegmentedControl
    class="provider-models-view"
    options={viewOptions}
    value={providersConfig.modelsView}
    ariaLabel={m.providers_models_view()}
    onchange={(view) => providersConfig.setModelsView(view)}
  />
</div>

<style>
  .provider-models-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .provider-models-actions {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .provider-models-actions :global(.provider-models-view) {
    margin-left: auto;
  }
</style>
