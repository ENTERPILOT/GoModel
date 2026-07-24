<script>
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { confirmDialog } from "$lib/stores/confirm.svelte.js";

  const dialog = $derived(confirmDialog.state);
</script>

<Modal open={dialog.open} variant="auth" onclose={() => confirmDialog.close()}>
  <div
    class="auth-dialog {dialog.dialogClass}"
    role="dialog"
    aria-modal="true"
    aria-labelledby={dialog.titleId}
  >
    <div class="auth-dialog-header">
      <h2 id={dialog.titleId}>{dialog.title}</h2>
      <DialogCloseButton
        label="Close confirmation dialog"
        onclick={() => confirmDialog.close()}
        class="auth-dialog-close"
        iconClass=""
      />
    </div>
    <form
      class="auth-dialog-form"
      onsubmit={(event) => {
        event.preventDefault();
        confirmDialog.submit();
      }}
    >
      {#if dialog.message}
        <p class="auth-dialog-hint">{dialog.message}</p>
      {/if}
      <div class="form-field">
        <label class="form-field-label" for={dialog.inputId}>
          {confirmDialog.inputLabel()}
        </label>
        <input
          id={dialog.inputId}
          class="form-input"
          type="text"
          autocomplete="off"
          data-modal-autofocus
          bind:value={confirmDialog.state.value}
        />
      </div>
      {#if confirmDialog.error}
        <p class="auth-dialog-error" role="alert">{confirmDialog.error}</p>
      {/if}
      <div class="auth-dialog-actions">
        <button
          type="button"
          class="pagination-btn"
          onclick={() => confirmDialog.close()}>Cancel</button
        >
        <button
          type="submit"
          class="pagination-btn pagination-btn-danger pagination-btn-with-icon"
          disabled={dialog.loading || !confirmDialog.ready()}
        >
          <Icon name={dialog.icon} class="form-action-icon" />
          <span>{dialog.confirmLabel}</span>
        </button>
      </div>
    </form>
  </div>
</Modal>
