<script>
  // API Keys page: managed gateway API keys (create with one-time secret
  // reveal, label editing, permanent deactivation).
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { authKeysStore as store } from "./authKeys.svelte.js";
  import AuthKeyEditor from "./AuthKeyEditor.svelte";
  import AuthKeyLabelsEditor from "./AuthKeyLabelsEditor.svelte";
  import AuthKeyList from "./AuthKeyList.svelte";

  const PAGE = "auth-keys";

  // Re-fetch when the page becomes active or the API key / timezone changes.
  $effect(() => {
    void auth.refreshTick;
    if (router.page === PAGE) store.fetchKeys();
  });
</script>

<div>
  <div class="page-header">
    <h2>API Keys</h2>
    <div class="page-header-controls">
      {#if store.available && !auth.authError}
        <button
          type="button"
          class="pagination-btn pagination-btn-primary pagination-btn-with-icon"
          disabled={store.formSubmitting}
          onclick={() => {
            if (!store.formSubmitting) store.openForm();
          }}
        >
          <Icon name="plus" class="table-icon-svg" />
          <span>Create API Key</span>
        </button>
      {/if}
    </div>
  </div>

  {#if !store.available && !auth.authError}
    <div class="alert alert-warning">API key management is unavailable.</div>
  {/if}
  {#if store.error && !auth.authError && !store.formOpen}
    <p class="form-error" role="alert" aria-live="assertive">{store.error}</p>
  {/if}

  {#if store.available && !auth.authError}
    <p class="form-hint auth-keys-help-notice">
      Managed API keys authenticate requests to the gateway. Deactivation is
      permanent &mdash; create a new key if access needs to be restored.
    </p>
  {/if}

  <AuthKeyEditor />
  <AuthKeyLabelsEditor />

  {#if store.loading && store.keys.length === 0}
    <div class="auth-keys-loading">
      <Spinner size={18} label="Loading API keys" />
    </div>
  {/if}

  {#if store.keys.length > 0 && store.available}
    <AuthKeyList />
  {/if}

  {#if store.keys.length === 0 && !store.loading && !auth.authError && !store.error && store.available}
    <p class="empty-state">No API keys yet. Issue a key to get started.</p>
  {/if}
</div>

<style>
  .auth-keys-loading {
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 32px 0;
  }
/* --- API Keys page --- */
.auth-keys-help-notice {
  margin-bottom: 20px;
}
</style>
