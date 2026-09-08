<script>
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { confirmDialog } from "$lib/stores/confirm.svelte.js";
  import * as m from "$lib/paraglide/messages.js";

  const dialog = $derived(confirmDialog.state);
</script>

<Modal
  open={dialog.open}
  variant="auth"
  stacked={dialog.stacked}
  onclose={() => confirmDialog.close()}
>
  <div
    class="auth-dialog {dialog.dialogClass}"
    role="dialog"
    aria-modal="true"
    aria-labelledby={dialog.titleId}
  >
    <div class="auth-dialog-header">
      <h2 id={dialog.titleId}>{dialog.title}</h2>
      <DialogCloseButton
        label={m.confirmation_close()}
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
      <!-- Typed flows require typing the exact text; simple confirmations
           (e.g. the editor's discard-changes prompt) omit requiredText and
           confirm with one click. -->
      {#if dialog.requiredText}
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
      {/if}
      {#if confirmDialog.error}
        <p class="auth-dialog-error" role="alert">{confirmDialog.error}</p>
      {/if}
      <div class="auth-dialog-actions">
        <!-- Simple confirmations (no requiredText) have no input to
             autofocus; the Cancel button is the fallback target. The typed
             input sits earlier in the DOM, so it still wins when present. -->
        <button
          type="button"
          class="btn"
          data-modal-autofocus
          onclick={() => confirmDialog.close()}>{m.common_action_cancel()}</button
        >
        <button
          type="submit"
          class="btn btn-danger btn-with-icon"
          disabled={dialog.loading || !confirmDialog.ready()}
        >
          <Icon icon={dialog.icon} class="form-action-icon" />
          <span>{dialog.confirmLabel}</span>
        </button>
      </div>
    </form>
  </div>
</Modal>
