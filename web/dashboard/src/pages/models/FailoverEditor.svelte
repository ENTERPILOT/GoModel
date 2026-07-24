<script>
  // Failover mapping editor modal. Self-contained:
  // reads/writes the shared failover store; the datalist comes from the
  // shared model inventory.
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { modelsStore } from "$lib/stores/models.svelte.js";
  import { failover } from "./failover.svelte.js";
  import { qualifiedModelName } from "./failover-logic.js";
</script>

<Modal
  open={failover.failoverFormOpen}
  variant="editor"
  onclose={() => failover.closeFailoverForm()}
>
  <div
    class="model-editor"
    data-failover-editor
    role="dialog"
    aria-modal="true"
    aria-label="Failover editor"
  >
    <form
      class="form"
      onsubmit={(event) => {
        event.preventDefault();
        failover.submitFailoverForm();
      }}
    >
      <div class="editor-header">
        <div>
          <p class="form-kicker">Failover mapping</p>
          <h3>{failover.failoverForm.source || "Failover"}</h3>
        </div>
        <DialogCloseButton
          label="Close failover editor"
          onclick={() => failover.closeFailoverForm()}
        />
      </div>

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

      <div class="form-field">
        <label class="form-field-label" for="failover-target">Fallback models</label>
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
            class="pagination-btn pagination-btn-with-icon"
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
            class="pagination-btn pagination-btn-with-icon"
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
      </div>

      <div class="vm-status-row">
        <div class="vm-status-toggle">
          <button
            type="button"
            class="alias-toggle"
            class:enabled={failover.failoverForm.enabled}
            disabled={failover.failoverFormManaged}
            aria-label={(failover.failoverForm.enabled ? "Disable" : "Enable") +
              " failover mapping"}
            onclick={() => {
              if (!failover.failoverFormManaged) {
                failover.failoverForm.enabled = !failover.failoverForm.enabled;
              }
            }}
          >
            <span class="alias-toggle-track"><span class="alias-toggle-thumb"></span></span>
            <span>{failover.failoverForm.enabled ? "Enabled" : "Disabled"}</span>
          </button>
        </div>
      </div>

      {#if failover.failoverError}
        <p class="form-error" role="alert" aria-live="assertive">
          {failover.failoverError}
        </p>
      {/if}

      <div class="form-actions">
        <button
          type="button"
          class="pagination-btn"
          onclick={() => failover.closeFailoverForm()}
        >
          Cancel
        </button>
        {#if failover.failoverFormMode === "edit" && !failover.failoverFormManaged}
          <button
            type="button"
            class="pagination-btn pagination-btn-danger-outline"
            disabled={failover.failoverSaving || failover.failoverGenerating}
            onclick={() => failover.deleteFailoverRule()}
          >
            Remove
          </button>
        {/if}
        {#if !failover.failoverFormManaged}
          <button
            type="submit"
            class="pagination-btn pagination-btn-primary pagination-btn-with-icon"
            disabled={failover.failoverSaving || failover.failoverGenerating}
          >
            <Icon name="save" class="form-action-icon" />
            <span>{failover.failoverSaving ? "Saving..." : "Save"}</span>
          </button>
        {/if}
      </div>
    </form>
  </div>
</Modal>
