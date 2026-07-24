<script>
  // Edit-labels modal for an existing API key.
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import { authKeysStore as store } from "./authKeys.svelte.js";

  function onsubmit(event) {
    event.preventDefault();
    store.submitLabelsEditor();
  }
</script>

<Modal open={store.labelsEditor.open} variant="editor" onclose={() => store.closeLabelsEditor()}>
  <div
    class="model-editor auth-key-editor"
    role="dialog"
    aria-modal="true"
    aria-label="API key labels editor"
  >
    <form class="form" {onsubmit}>
      <div class="editor-header">
        <div>
          <h3>Edit Labels</h3>
          <p class="form-hint">{store.labelsEditor.name}</p>
        </div>
        <DialogCloseButton label="Close" onclick={() => store.closeLabelsEditor()} />
      </div>
      <div class="form-field">
        <label class="form-field-label" for="auth-key-labels-edit">Labels (comma-separated)</label>
        <input
          id="auth-key-labels-edit"
          type="text"
          placeholder="ex. team-a, batch-jobs"
          autocomplete="off"
          data-modal-autofocus
          bind:value={store.labelsEditor.value}
        />
        <p class="form-hint">
          Applies to new requests made with this key. Leave empty to remove all labels.
        </p>
      </div>
      {#if store.labelsEditor.error}
        <p class="form-error" role="alert" aria-live="assertive">{store.labelsEditor.error}</p>
      {/if}
      <div class="form-actions">
        <button
          type="submit"
          class="pagination-btn pagination-btn-primary"
          disabled={store.labelsEditor.submitting}
        >{store.labelsEditor.submitting ? "Saving..." : "Save Labels"}</button>
      </div>
    </form>
  </div>
</Modal>
