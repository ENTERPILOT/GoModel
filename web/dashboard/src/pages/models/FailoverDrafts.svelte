<script>
  // Generated failover drafts modal: filter, select-all toggle, draft list
  // with per-draft checkboxes, save-selected.
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { failover } from "./failover.svelte.js";
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
    aria-label="Generated failover drafts"
  >
    <div class="editor-header">
      <div>
        <p class="form-kicker">Generated drafts</p>
        <h3>Failover models</h3>
      </div>
      <div class="failover-draft-header-actions">
        {#if failover.failoverGeneratedRules.length > 0}
          <span class="failover-draft-counter">{failover.failoverDraftCountLabel()}</span>
        {/if}
        <DialogCloseButton
          label="Close failover drafts"
          onclick={() => failover.closeFailoverDraftsModal()}
          disabled={failover.failoverDraftSaving}
        />
      </div>
    </div>

    {#if failover.failoverGenerating}
      <LoadingState label="Generating failover drafts..." class="failover-drafts-loading" />
    {/if}

    {#if !failover.failoverGenerating && failover.failoverNotice && !failover.failoverError}
      <div
        class="alert alert-success failover-draft-alert"
        role="status"
        aria-live="polite"
        aria-atomic="true"
      >
        {failover.failoverNotice}
      </div>
    {/if}

    {#if !failover.failoverGenerating && failover.failoverGeneratedRules.length > 0}
      <div class="failover-draft-toolbar">
        <div class="filter-input-wrap">
          <Icon name="search" class="filter-input-icon" />
          <input
            type="text"
            class="filter-input"
            placeholder="Filter failover drafts..."
            aria-label="Filter failover drafts"
            bind:value={failover.failoverDraftFilter}
          />
        </div>
        <button
          type="button"
          class="pagination-btn pagination-btn-with-icon failover-draft-toggle-all"
          disabled={failover.failoverDraftSaving}
          onclick={() => failover.toggleAllFailoverDrafts()}
        >
          <Icon name="check" class="form-action-icon" />
          <span>
            {failover.allFailoverDraftsSelected() ? "Deselect all" : "Select all"}
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
              aria-label={"Select failover draft for " +
                failover.failoverPrimaryModel(rule)}
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
        No failover suggestions were generated.
      </p>
    {/if}
    {#if !failover.failoverGenerating && failover.failoverGeneratedRules.length > 0 && failover.filteredFailoverDrafts().length === 0}
      <p class="form-hint failover-drafts-empty">
        No failover drafts match the filter.
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
        class="pagination-btn"
        disabled={failover.failoverDraftSaving}
        onclick={() => failover.closeFailoverDraftsModal()}
      >
        Cancel
      </button>
      <button
        type="button"
        class="pagination-btn pagination-btn-primary pagination-btn-with-icon"
        disabled={failover.failoverGenerating ||
          failover.failoverDraftSaving ||
          failover.selectedFailoverDraftCount() === 0}
        onclick={() => failover.saveSelectedFailoverDrafts()}
      >
        <Icon name="save" class="form-action-icon" />
        <span>{failover.failoverDraftSaving ? "Saving..." : "Save selected"}</span>
      </button>
    </div>
  </div>
</Modal>
