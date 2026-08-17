<script>
  // API Keys page: managed gateway API keys (create with one-time secret
  // reveal, label editing, permanent deactivation).
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { authKeysStore as store } from "./authKeys.svelte.js";
  import AuthKeyEditor from "./AuthKeyEditor.svelte";
  import AuthKeyLabelsEditor from "./AuthKeyLabelsEditor.svelte";
  import AuthKeyList from "./AuthKeyList.svelte";
  import { Plus } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  const PAGE = "auth-keys";

  // Re-fetch when the page becomes active or the API key / timezone changes.
  $effect(() => {
    void auth.refreshTick;
    if (router.page === PAGE) store.fetchKeys();
  });
</script>

<div>
  <div class="page-header">
    <h2>{m.api_keys_title()}</h2>
    <div class="page-header-controls">
      {#if store.available && !auth.authError}
        <button
          type="button"
          class="btn btn-primary btn-with-icon"
          disabled={store.formSubmitting}
          onclick={() => {
            if (!store.formSubmitting) store.openForm();
          }}
        >
          <Icon icon={Plus} class="table-icon-svg" />
          <span>{m.api_keys_create()}</span>
        </button>
      {/if}
    </div>
  </div>

  {#if !store.available && !auth.authError}
    <div class="alert alert-warning">{m.api_keys_unavailable()}</div>
  {/if}
  {#if store.error && !auth.authError && !store.formOpen}
    <p class="form-error" role="alert" aria-live="assertive">{store.error}</p>
  {/if}

  {#if store.available && !auth.authError}
    <p class="form-hint auth-keys-help-notice">
      {m.api_keys_help()}
    </p>
  {/if}

  <AuthKeyEditor />
  <AuthKeyLabelsEditor />

  {#if store.loading && store.keys.length === 0}
    <LoadingState label={m.api_keys_loading()} />
  {/if}

  {#if store.keys.length > 0 && store.available}
    <div class="table-toolbar">
      <div class="table-toolbar-main">
        <FilterInput
          placeholder={m.api_keys_filter_placeholder()}
          label={m.api_keys_filter_label()}
          bind:value={store.filter}
        />
      </div>
      <div class="table-toolbar-actions">
        <label class="auth-keys-inactive-toggle">
          <input type="checkbox" bind:checked={store.showInactive} />
          <span>
            {m.api_keys_show_inactive()}
            {#if store.inactiveCount > 0}({store.inactiveCount}){/if}
          </span>
        </label>
      </div>
    </div>
  {/if}

  {#if store.visibleKeys.length > 0 && store.available}
    <AuthKeyList />
  {/if}

  {#if store.keys.length > 0 && store.visibleKeys.length === 0 && store.available}
    <p class="empty-state">
      {m.api_keys_no_match()}{store.inactiveCount > 0 && !store.showInactive
        ? " " + m.api_keys_hidden({ count: store.inactiveCount })
        : ""}
    </p>
  {/if}

  {#if store.keys.length === 0 && !store.loading && !auth.authError && !store.error && store.available}
    <p class="empty-state">{m.api_keys_empty()}</p>
  {/if}
</div>

<style>
/* --- API Keys page --- */
.auth-keys-help-notice {
  margin-bottom: 20px;
}

.auth-keys-inactive-toggle {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--text);
  cursor: pointer;
  user-select: none;
  white-space: nowrap;
}

.auth-keys-inactive-toggle input {
  width: 16px;
  height: 16px;
  accent-color: var(--accent);
  cursor: pointer;
}
</style>
