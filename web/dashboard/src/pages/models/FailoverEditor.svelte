<script>
  // Failover mapping editor (EditorDialog shell). Self-contained:
  // reads/writes the shared failover store; the datalist comes from the
  // shared model inventory. The [data-failover-editor] wrapper is the
  // store's focusFailoverEditor() query anchor.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import EditorDialog from "$lib/components/organisms/EditorDialog.svelte";
  import EnabledToggle from "$lib/components/atoms/EnabledToggle.svelte";
  import FormField from "$lib/components/molecules/FormField.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { modelsStore } from "$lib/stores/models.svelte.ts";
  import { failover } from "./failover.svelte.js";
  import { qualifiedModelName } from "./failover-logic.js";
</script>

<EditorDialog
  open={failover.failoverFormOpen}
  ariaLabel="Failover editor"
  error={failover.failoverError}
  submitting={failover.failoverSaving ||
    failover.failoverGenerating ||
    failover.failoverFormManaged}
  submitLabel="Save"
  submittingLabel={failover.failoverSaving ? "Saving..." : "Save"}
  onclose={() => failover.closeFailoverForm()}
  onsubmit={() => failover.submitFailoverForm()}
>
  {#snippet header()}
    <p class="form-kicker">Failover mapping</p>
    <h3>{failover.failoverForm.source || "Failover"}</h3>
  {/snippet}

  <div data-failover-editor style="display: contents">
    {#if failover.failoverFormManaged}
      <p class="form-hint" role="status">
        This failover mapping is defined in configuration and is read-only here.
      </p>
    {/if}

    <datalist id="failover-target-options">
      {#each modelsStore.models as m}
        <option value={qualifiedModelName(m)}>{qualifiedModelName(m)}</option>
      {/each}
    </datalist>

    <FormField id="failover-target" label="Fallback models">
      <div class="vm-target-list">
        <div class="vm-target-row">
          <input
            id="failover-target"
            type="text"
            list="failover-target-options"
            class="mono vm-target-model"
            placeholder="azure/gpt-4o"
            bind:value={failover.failoverForm.target_model}
            disabled={failover.failoverFormManaged}
            aria-label="Fallback model"
            data-modal-autofocus
          />
          {#if failover.failoverForm.target_model}
            <TableActionButton
              label="Remove fallback model"
              class="table-action-btn-danger table-icon-btn vm-target-remove"
              onclick={() => failover.removePrimaryFailoverTarget()}
              disabled={failover.failoverFormManaged}
            >
              <Icon name="trash-2" class="table-icon-svg" />
            </TableActionButton>
          {/if}
        </div>
        {#each failover.failoverForm.targets as target, index (index)}
          <div class="vm-target-row">
            <input
              type="text"
              list="failover-target-options"
              class="mono vm-target-model"
              placeholder="gemini/gemini-2.5-pro"
              bind:value={target.model}
              disabled={failover.failoverFormManaged}
              aria-label="Fallback model"
            />
            <TableActionButton
              label="Remove fallback model"
              class="table-action-btn-danger table-icon-btn vm-target-remove"
              onclick={() => failover.removeFailoverTarget(index)}
              disabled={failover.failoverFormManaged}
            >
              <Icon name="trash-2" class="table-icon-svg" />
            </TableActionButton>
          </div>
        {/each}
      </div>
      <div class="failover-target-actions">
        <button
          type="button"
          class="btn btn-with-icon"
          disabled={failover.failoverFormManaged ||
            failover.failoverGenerating ||
            failover.failoverSaving}
          onclick={() => failover.addFailoverTarget()}
        >
          <Icon name="plus" class="form-action-icon" />
          <span>Add fallback model</span>
        </button>
        <button
          type="button"
          class="btn btn-with-icon"
          disabled={failover.failoverFormManaged ||
            failover.failoverGenerating ||
            failover.failoverSaving ||
            !failover.failoverEnabled()}
          onclick={() => failover.generateFailoverForForm()}
        >
          <Icon name="wand-sparkles" class="form-action-icon" />
          <span>
            {failover.failoverGenerating
              ? "Generating..."
              : "Generate automatically"}
          </span>
        </button>
      </div>
    </FormField>

    <div class="vm-status-row">
      <div class="vm-status-toggle">
        <EnabledToggle
          enabled={failover.failoverForm.enabled}
          label="failover mapping"
          disabled={failover.failoverFormManaged}
          onclick={() => {
            if (!failover.failoverFormManaged) {
              failover.failoverForm.enabled = !failover.failoverForm.enabled;
            }
          }}
        />
      </div>
    </div>
  </div>

  {#snippet extraActions()}
    {#if failover.failoverFormMode === "edit" && !failover.failoverFormManaged}
      <button
        type="button"
        class="btn btn-danger-outline"
        disabled={failover.failoverSaving || failover.failoverGenerating}
        onclick={() => failover.deleteFailoverRule()}
      >
        Remove
      </button>
    {/if}
  {/snippet}
</EditorDialog>
