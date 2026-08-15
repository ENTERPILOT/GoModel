<script>
  // Generated failover drafts modal: filter, select-all toggle, draft list
  // with per-draft checkboxes, save-selected.
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import { failover } from "./failover.svelte.js";
  import { Check, Save } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
</script>

<Modal
  open={failover.failoverDraftsOpen}
  variant="editor"
  onclose={() => failover.closeFailoverDraftsModal()}
>
  <div
    class="model-editor failover-drafts-editor"
    role="dialog"
    aria-modal="true"
    aria-label={m.models_drafts_label()}
  >
    <div class="editor-header">
      <div>
        <p class="form-kicker">{m.models_drafts_kicker()}</p>
        <h3>{m.models_drafts_title()}</h3>
      </div>
      <div class="failover-draft-header-actions">
        {#if failover.failoverGeneratedRules.length > 0}
          <span class="failover-draft-counter">{failover.failoverDraftCountLabel()}</span>
        {/if}
        <DialogCloseButton
          label={m.models_drafts_close()}
          onclick={() => failover.closeFailoverDraftsModal()}
          disabled={failover.failoverDraftSaving}
        />
      </div>
    </div>

    {#if failover.failoverGenerating}
      <LoadingState label={m.models_drafts_generating()} class="failover-drafts-loading" />
    {/if}

    {#if !failover.failoverGenerating && failover.failoverGeneratedRules.length > 0}
      <div class="failover-draft-toolbar">
        <FilterInput
          placeholder={m.models_drafts_filter_placeholder()}
          label={m.models_drafts_filter_label()}
          bind:value={failover.failoverDraftFilter}
        />
        <button
          type="button"
          class="btn btn-with-icon failover-draft-toggle-all"
          disabled={failover.failoverDraftSaving}
          onclick={() => failover.toggleAllFailoverDrafts()}
        >
          <Icon icon={Check} class="form-action-icon" />
          <span>
            {failover.allFailoverDraftsSelected() ? m.models_drafts_deselect_all() : m.models_drafts_select_all()}
          </span>
        </button>
      </div>
    {/if}

    {#if !failover.failoverGenerating && failover.filteredFailoverDrafts().length > 0}
      <div class="failover-draft-list" role="list">
        {#each failover.filteredFailoverDrafts() as rule ("failover-draft:" + failover.failoverPrimaryModel(rule))}
          <label class="failover-draft-row" role="listitem">
            <input
              type="checkbox"
              checked={failover.failoverDraftSelected(rule)}
              disabled={failover.failoverDraftSaving}
              onchange={(event) =>
                failover.setFailoverDraftSelected(rule, event.currentTarget.checked)}
              aria-label={m.models_drafts_select({ model: failover.failoverPrimaryModel(rule) })}
            />
            <span class="failover-draft-copy">
              <span class="mono failover-draft-source">{failover.failoverPrimaryModel(rule)}</span>
              <span class="mono failover-draft-targets">{failover.failoverTargetLabel(rule)}</span>
            </span>
          </label>
        {/each}
      </div>
    {/if}

    {#if !failover.failoverGenerating && failover.failoverGeneratedRules.length === 0 && !failover.failoverError}
      <p class="form-hint failover-drafts-empty">
        {m.models_drafts_empty()}
      </p>
    {/if}
    {#if !failover.failoverGenerating && failover.failoverGeneratedRules.length > 0 && failover.filteredFailoverDrafts().length === 0}
      <p class="form-hint failover-drafts-empty">
        {m.models_drafts_no_match()}
      </p>
    {/if}
    {#if failover.failoverError}
      <p class="form-error" role="alert" aria-live="assertive">
        {failover.failoverError}
      </p>
    {/if}

    <div class="form-actions failover-draft-actions">
      <button
        type="button"
        class="btn"
        disabled={failover.failoverDraftSaving}
        onclick={() => failover.closeFailoverDraftsModal()}
      >
        {m.common_action_cancel()}
      </button>
      <button
        type="button"
        class="btn btn-primary btn-with-icon"
        disabled={failover.failoverGenerating ||
          failover.failoverDraftSaving ||
          failover.selectedFailoverDraftCount() === 0}
        onclick={() => failover.saveSelectedFailoverDrafts()}
      >
        <Icon icon={Save} class="form-action-icon" />
        <span>{failover.failoverDraftSaving ? m.models_drafts_saving() : m.models_drafts_save_selected()}</span>
      </button>
    </div>
  </div>
</Modal>

<style>
.failover-drafts-editor {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

.failover-draft-header-actions {
    display: flex;
    align-items: center;
    gap: 10px;
    flex: 0 0 auto;
  }

.failover-draft-counter {
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 600;
    white-space: nowrap;
  }

.failover-draft-toolbar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px;
  }

.failover-draft-toolbar :global(.filter-input-wrap) {
    min-width: 0;
  }

.failover-draft-toolbar :global(.filter-input) {
    min-width: 0;
  }

.failover-draft-toggle-all {
    flex: 0 0 auto;
  }

.failover-draft-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
    max-height: min(52vh, 430px);
    overflow-y: auto;
    padding-right: 4px;
  }

.failover-draft-row {
    display: grid;
    grid-template-columns: 18px minmax(0, 1fr);
    gap: 10px;
    align-items: start;
    padding: 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg);
    cursor: pointer;
  }

.failover-draft-row:hover {
    border-color: color-mix(in srgb, var(--accent) 42%, var(--border));
    background: var(--bg-surface-hover);
  }

.failover-draft-row :global(input) {
    width: 16px;
    height: 16px;
    margin-top: 2px;
  }

.failover-draft-copy {
    min-width: 0;
    display: grid;
    gap: 4px;
  }

.failover-draft-source, .failover-draft-targets {
    overflow-wrap: anywhere;
  }

.failover-draft-targets {
    color: var(--text-muted);
    font-size: 12px;
  }

.failover-drafts-empty {
    min-height: 96px;
    display: flex;
    align-items: center;
  }

.failover-draft-actions {
    margin-top: 0;
  }
</style>
