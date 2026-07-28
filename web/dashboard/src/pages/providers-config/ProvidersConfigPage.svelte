<script>
  // Providers page: dashboard-managed provider credentials
  // (/admin/provider-credentials).
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { providersConfig } from "./providersConfig.svelte.js";
  import ProviderCredentialList from "./ProviderCredentialList.svelte";
  import ProviderCredentialEditor from "./ProviderCredentialEditor.svelte";

  const PAGE = "providers-config";

  // Re-fetch when the page becomes active or the API key changes.
  $effect(() => {
    void auth.refreshTick;
    if (router.page === PAGE) providersConfig.fetchPage();
  });
</script>

<div>
  <div class="page-header">
    <div>
      <InlineHelpSection copyId="providers-config-help-copy" label="model providers help">
        {#snippet title()}<h2>Providers</h2>{/snippet}
        {#snippet help()}
          Configure LLM provider credentials here instead of setting API keys as
          environment variables. Providers declared in config.yaml or env vars
          are read-only (Config badge) and cannot be edited or deleted from the
          dashboard. Keys are masked after saving.
        {/snippet}
      </InlineHelpSection>
    </div>
    <div class="page-header-controls">
      {#if providersConfig.available && !auth.needsAuth}
        <button
          type="button"
          class="btn btn-primary btn-with-icon"
          disabled={providersConfig.formSubmitting}
          onclick={() => providersConfig.openCreate()}
        >
          <Icon name="plus" class="form-action-icon" />
          <span>Add Provider</span>
        </button>
      {/if}
    </div>
  </div>

  {#if !providersConfig.available && !auth.needsAuth}
    <div class="alert alert-warning">Provider credential management is unavailable.</div>
  {/if}
  {#if providersConfig.error && !auth.needsAuth && !providersConfig.formOpen}
    <p class="form-error" role="alert" aria-live="assertive">{providersConfig.error}</p>
  {/if}
  {#if providersConfig.loading && !auth.needsAuth}
    <LoadingState label="Loading providers..." />
  {/if}

  {#if (providersConfig.rows.length > 0 || providersConfig.filter) && providersConfig.available && !auth.needsAuth}
    <div class="table-toolbar">
      <div class="table-toolbar-main">
        <FilterInput
          id="provider-credential-filter"
          placeholder="Filter by name, type, or base URL..."
          label="Filter providers by name, type, or base URL"
          bind:value={providersConfig.filter}
        />
      </div>
    </div>
  {/if}

  <ProviderCredentialEditor />

  {#if providersConfig.filteredRows.length > 0 && providersConfig.available && !auth.needsAuth}
    <ProviderCredentialList />
  {/if}

  {#if providersConfig.rows.length === 0 && !providersConfig.filter && !providersConfig.loading && !auth.needsAuth && !providersConfig.error && providersConfig.available}
    <p class="empty-state">
      No dashboard-managed providers yet. Add one here, or declare providers in <code>config.yaml</code> / environment variables.
    </p>
  {/if}
  {#if providersConfig.rows.length > 0 && providersConfig.filteredRows.length === 0 && providersConfig.filter && !providersConfig.loading && !auth.needsAuth && providersConfig.available}
    <p class="empty-state">No providers match your filter.</p>
  {/if}
</div>
