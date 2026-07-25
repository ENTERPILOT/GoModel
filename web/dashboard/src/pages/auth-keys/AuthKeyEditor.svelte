<script>
  // Create-API-key modal: form fields plus the one-time issued-secret banner
  // with clipboard copy.
  import CopyButton from "$lib/components/atoms/CopyButton.svelte";
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Modal from "$lib/components/atoms/Modal.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { authKeysStore as store } from "./authKeys.svelte.js";

  function onclose() {
    // Escape is ignored while the auth dialog is on top (stacked dialogs).
    if (!auth.dialogOpen) {
      store.closeForm();
    }
  }

  function onsubmit(event) {
    event.preventDefault();
    store.submitForm();
  }
</script>

<Modal open={store.formOpen} variant="editor" {onclose}>
  <div
    class="model-editor auth-key-editor"
    role="dialog"
    aria-modal="true"
    aria-label="API key editor"
  >
    <form class="form" {onsubmit}>
      <div class="editor-header">
        <div>
          <h3>Create API Key</h3>
        </div>
        <DialogCloseButton label="Close" onclick={() => store.closeForm()} />
      </div>

      {#if store.issuedValue}
        <div class="auth-key-issued-banner">
          <p class="auth-key-issued-warning">
            Store this key securely &mdash; it won&rsquo;t be shown again.
          </p>
          <div class="auth-key-issued-value-row">
            <code class="auth-key-issued-token">{store.issuedValue}</code>
            <CopyButton
              state={store.copyState}
              onclick={() => store.copyIssuedValue()}
            />
          </div>
          {#if store.copyState.error}
            <p class="form-error" role="alert" aria-live="assertive">
              Unable to copy the key automatically. Copy it manually.
            </p>
          {/if}
          <div class="form-actions">
            <button
              type="button"
              class="btn btn-primary"
              onclick={() => store.dismissIssuedKey()}
            >Done, I&rsquo;ve stored it</button>
          </div>
        </div>
      {:else}
        <div class="auth-key-form-fields">
          <div class="form-grid">
            <div class="form-field">
              <label class="form-field-label" for="auth-key-name">
                Name <span class="form-hint">(required)</span>
              </label>
              <input
                id="auth-key-name"
                type="text"
                placeholder="e.g. ci-deploy"
                autocomplete="off"
                data-modal-autofocus
                bind:value={store.form.name}
              />
            </div>
            <div class="form-field">
              <label class="form-field-label" for="auth-key-expires">
                Expires <span class="form-hint">(optional, valid through the selected date)</span>
              </label>
              <input id="auth-key-expires" type="date" bind:value={store.form.expires_at} />
            </div>
          </div>
          <div class="form-field">
            <InlineHelpSection copyId="auth-key-user-path-help-copy" label="API key user path help">
              {#snippet title()}
                <label class="form-field-label" for="auth-key-user-path">User Path (optional)</label>
              {/snippet}
              {#snippet help()}
                When set, this key overrides the configured user path request
                header for audit logging and downstream request context.
              {/snippet}
            </InlineHelpSection>
            <input
              id="auth-key-user-path"
              type="text"
              placeholder="ex. /department1/team-a"
              aria-describedby="auth-key-user-path-help-copy"
              bind:value={store.form.user_path}
            />
          </div>
          <div class="form-field">
            <InlineHelpSection copyId="auth-key-labels-help-copy" label="API key labels help">
              {#snippet title()}
                <label class="form-field-label" for="auth-key-labels">
                  Labels (optional, comma-separated)
                </label>
              {/snippet}
              {#snippet help()}
                Every request authenticated with this key gets these labels, in
                addition to any labels from tagging headers. Labels show up in
                usage analytics, the request log, and audit logs.
              {/snippet}
            </InlineHelpSection>
            <input
              id="auth-key-labels"
              type="text"
              placeholder="ex. team-a, batch-jobs"
              aria-describedby="auth-key-labels-help-copy"
              bind:value={store.form.labels}
            />
          </div>
          <div class="form-field">
            <InlineHelpSection copyId="auth-key-dashboard-access-help-copy" label="API key dashboard access help">
              {#snippet title()}
                <label class="form-field-label" for="auth-key-dashboard-access">Dashboard access</label>
              {/snippet}
              {#snippet help()}
                When off, this key is denied the dashboard and every /admin API
                endpoint. Model endpoints and GET /v1/usage stay available to
                the key. The master key always has dashboard access.
              {/snippet}
            </InlineHelpSection>
            <label class="auth-key-dashboard-toggle">
              <input
                id="auth-key-dashboard-access"
                type="checkbox"
                aria-describedby="auth-key-dashboard-access-help-copy"
                bind:checked={store.form.dashboard_access}
              />
              <span>Allow this key to use the dashboard and /admin API</span>
            </label>
          </div>
          <div class="form-field">
            <label class="form-field-label" for="auth-key-description">Description (optional)</label>
            <textarea
              id="auth-key-description"
              rows="2"
              placeholder="What is this key used for?"
              bind:value={store.form.description}
            ></textarea>
          </div>
          {#if store.error}
            <p class="form-error" role="alert" aria-live="assertive">{store.error}</p>
          {/if}
          <div class="form-actions">
            <button
              type="submit"
              class="btn btn-primary btn-with-icon"
              disabled={store.formSubmitting}
            >
              {#if !store.formSubmitting}
                <span aria-hidden="true"><Icon name="plus" class="table-icon-svg" /></span>
              {/if}
              <span>{store.formSubmitting ? "Creating..." : "Create API Key"}</span>
            </button>
          </div>
        </div>
      {/if}
    </form>
  </div>
</Modal>

<style>
  .auth-key-form-fields > :global(.form-field) {
    margin-bottom: 4px;
  }

  .auth-key-dashboard-toggle {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    cursor: pointer;
  }

  .auth-key-issued-banner {
    background: color-mix(in srgb, var(--success) 8%, var(--bg-surface));
    border: 1px solid color-mix(in srgb, var(--success) 30%, var(--border));
    border-radius: var(--radius);
    padding: 16px;
    margin-bottom: 20px;
  }

  .auth-key-issued-warning {
    font-size: 13px;
    font-weight: 600;
    margin-bottom: 12px;
    color: color-mix(in srgb, var(--success) 80%, var(--text));
  }

  .auth-key-issued-value-row {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-bottom: 12px;
    flex-wrap: wrap;
  }

  .auth-key-issued-token {
    flex: 1;
    min-width: 0;
    overflow-x: auto;
    padding: 8px 12px;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    font-size: 13px;
    word-break: break-all;
  }
</style>
