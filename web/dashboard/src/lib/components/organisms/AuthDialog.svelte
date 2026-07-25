<script>
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { auth } from "$lib/stores/auth.svelte.js";
</script>

<Modal
  open={auth.dialogOpen}
  variant="auth"
  onclose={() => auth.closeDialog()}
>
  <div
    class="auth-dialog"
    role="dialog"
    aria-modal="true"
    aria-labelledby="authDialogTitle"
  >
    <div class="auth-dialog-header">
      <div>
        <h2 id="authDialogTitle">
          {auth.needsAuth ? "Dashboard locked" : "Change API key"}
        </h2>
      </div>
      <DialogCloseButton
        label="Close authentication dialog"
        onclick={() => auth.closeDialog()}
        class="auth-dialog-close"
        iconClass=""
      />
    </div>
    <form
      class="auth-dialog-form"
      onsubmit={(event) => {
        event.preventDefault();
        auth.submit();
      }}
    >
      <div class="auth-dialog-input-shell">
        <Icon name="lock-keyhole" class="auth-dialog-input-icon" />
        <input
          id="authDialogApiKey"
          class="auth-dialog-input"
          type="password"
          placeholder="Master key or bearer token"
          aria-label="API key"
          autocomplete="current-password"
          data-modal-autofocus
          bind:value={auth.apiKey}
        />
      </div>
      {#if auth.authError}
        <p class="auth-dialog-error" role="alert">
          {auth.authErrorMessage || "Enter a valid API key to continue."}
        </p>
      {/if}
      <p class="auth-dialog-hint">
        Stored in this browser. Requests use the Authorization bearer header.
      </p>
      <div class="auth-dialog-actions">
        <button
          type="submit"
          class="btn btn-primary btn-with-icon auth-dialog-submit-btn"
        >
          <Icon name="check" class="auth-dialog-submit-icon" />
          <span>{auth.needsAuth ? "Unlock dashboard" : "Save API key"}</span>
        </button>
      </div>
    </form>
  </div>
</Modal>

<style>
/* Styles owned by this component (moved from dashboard.css). */
.auth-dialog-input-shell {
    position: relative;
  }

.auth-dialog-input {
    width: 100%;
    padding: 11px 12px 11px 38px;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    color: var(--text);
    font-size: 14px;
    font-family: inherit;
    outline: none;
  }

.auth-dialog-input:focus {
    border-color: var(--accent);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 18%, transparent);
  }
</style>
