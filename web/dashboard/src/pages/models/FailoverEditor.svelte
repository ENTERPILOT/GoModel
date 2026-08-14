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
  import { modelsStore } from "$lib/stores/models.svelte.js";
  import { failover } from "./failover.svelte.js";
  import { qualifiedModelName } from "./failover-logic.js";
  import { Plus, Trash2, WandSparkles } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
</script>

<EditorDialog
  open={failover.failoverFormOpen}
  ariaLabel={m.models_failover_editor()}
  error={failover.failoverError}
  submitting={failover.failoverSaving}
  submitDisabled={failover.failoverGenerating || failover.failoverFormManaged}
  submitLabel={m.models_save()}
  onclose={() => failover.closeFailoverForm()}
  onsubmit={() => failover.submitFailoverForm()}
>
  {#snippet header()}
    <p class="form-kicker">{m.models_failover_mapping()}</p>
    <h3>{failover.failoverForm.source || "Failover"}</h3>
  {/snippet}

  <div data-failover-editor style="display: contents">
    {#if failover.failoverFormManaged}
      <p class="form-hint" role="status">
        {m.models_failover_managed()}
      </p>
    {/if}

    <datalist id="failover-target-options">
      {#each modelsStore.models as m}
        <option value={qualifiedModelName(m)}>{qualifiedModelName(m)}</option>
      {/each}
    </datalist>

    <FormField id="failover-target" label={m.models_fallback_models()}>
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
            aria-label={m.models_fallback_model()}
            data-modal-autofocus
          />
          {#if failover.failoverForm.target_model}
            <TableActionButton
              label={m.models_remove_fallback()}
              class="table-action-btn-danger table-icon-btn vm-target-remove"
              onclick={() => failover.removePrimaryFailoverTarget()}
              disabled={failover.failoverFormManaged}
            >
              <Icon icon={Trash2} class="table-icon-svg" />
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
              aria-label={m.models_fallback_model()}
            />
            <TableActionButton
              label={m.models_remove_fallback()}
              class="table-action-btn-danger table-icon-btn vm-target-remove"
              onclick={() => failover.removeFailoverTarget(index)}
              disabled={failover.failoverFormManaged}
            >
              <Icon icon={Trash2} class="table-icon-svg" />
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
          <Icon icon={Plus} class="form-action-icon" />
          <span>{m.models_add_fallback()}</span>
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
          <Icon icon={WandSparkles} class="form-action-icon" />
          <span>
            {failover.failoverGenerating
              ? m.models_generating()
              : m.models_generate_automatically()}
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
        {m.models_remove_mapping()}
      </button>
    {/if}
  {/snippet}
</EditorDialog>
