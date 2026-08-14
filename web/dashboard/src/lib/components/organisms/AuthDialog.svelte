<script>
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { authenticationLoginURL } from "$lib/stores/external-auth.js";
  import { gomodelPath } from "$lib/api/paths.js";
  import { i18n } from "$lib/i18n/i18n.svelte.js";
  import { Check, KeyRound, LockKeyhole } from "lucide";
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
          {auth.needsAuth
            ? i18n.t("auth.dialog.lockedTitle")
            : i18n.t("auth.dialog.changeKeyTitle")}
        </h2>
      </div>
      <DialogCloseButton
        label={i18n.t("auth.dialog.close")}
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
      {#if auth.externalLoginURL}
        <a
          class="btn btn-primary btn-with-icon external-login-btn"
          href={authenticationLoginURL(gomodelPath(auth.externalLoginURL))}
          onclick={() => auth.selectExternalAuthentication()}
        >
          <Icon icon={KeyRound} />
          <span>{i18n.t("auth.dialog.signInWithSso")}</span>
        </a>
        <div class="auth-dialog-separator"><span>{i18n.t("auth.dialog.orUseApiKey")}</span></div>
      {/if}
      <div class="auth-dialog-input-shell">
        <Icon icon={LockKeyhole} class="auth-dialog-input-icon" />
        <input
          id="authDialogApiKey"
          class="auth-dialog-input"
          type="password"
          placeholder={i18n.t("auth.apiKey.placeholder")}
          aria-label={i18n.t("auth.apiKey.label")}
          autocomplete="current-password"
          data-modal-autofocus
          bind:value={auth.apiKey}
        />
      </div>
      {#if auth.authError}
        <p class="auth-dialog-error" role="alert">
          {auth.authErrorMessage || i18n.t("auth.apiKey.invalid")}
        </p>
      {/if}
      <p class="auth-dialog-hint">
        {i18n.t("auth.apiKey.storageHint")}
      </p>
      <div class="auth-dialog-actions">
        <button
          type="submit"
          class="btn btn-primary btn-with-icon auth-dialog-submit-btn"
        >
          <Icon icon={Check} class="auth-dialog-submit-icon" />
          <span>{auth.needsAuth
              ? i18n.t("auth.actions.unlockDashboard")
              : i18n.t("auth.actions.saveApiKey")}</span>
        </button>
      </div>
    </form>
  </div>
</Modal>

<style>
.external-login-btn {
    width: 100%;
    justify-content: center;
    text-decoration: none;
}

.auth-dialog-separator {
    display: flex;
    align-items: center;
    gap: 10px;
    color: var(--text-muted);
    font-size: 12px;
}

.auth-dialog-separator::before,
.auth-dialog-separator::after {
    content: "";
    flex: 1;
    border-top: 1px solid var(--border);
}

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
