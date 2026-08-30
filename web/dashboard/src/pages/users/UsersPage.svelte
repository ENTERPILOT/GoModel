<script>
  // Users page: the user-path tree (groups and users derived from API keys
  // and stored policies) with per-node model allowlists.
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { usersStore as store } from "./users.svelte.js";
  import UserEditor from "./UserEditor.svelte";
  import UserList from "./UserList.svelte";
  import { Plus } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  const PAGE = "users";

  // Re-fetch when the page becomes active or the API key changes.
  $effect(() => {
    void auth.refreshTick;
    if (router.page === PAGE) store.fetchUsers();
  });
</script>

<div>
  <div class="page-header">
    <h2>{m.users_title()}</h2>
    <div class="page-header-controls">
      {#if store.available && !auth.authError}
        <button
          type="button"
          class="btn btn-primary btn-with-icon"
          disabled={store.formSubmitting}
          onclick={() => store.openForm()}
        >
          <Icon icon={Plus} class="table-icon-svg" />
          <span>{m.users_create()}</span>
        </button>
      {/if}
    </div>
  </div>

  {#if !store.available && !auth.authError}
    <div class="alert alert-warning">{m.users_unavailable()}</div>
  {/if}
  {#if store.error && !auth.authError && !store.formOpen}
    <p class="form-error" role="alert" aria-live="assertive">{store.error}</p>
  {/if}

  {#if store.available && !auth.authError}
    <p class="form-hint users-help-notice">{m.users_help()}</p>
  {/if}

  <UserEditor />

  {#if store.loading && store.nodes.length === 0}
    <LoadingState label={m.users_loading()} />
  {/if}

  {#if store.nodes.length > 0 && store.available}
    <div class="table-toolbar">
      <div class="table-toolbar-main">
        <FilterInput
          placeholder={m.users_filter_placeholder()}
          label={m.users_filter_label()}
          bind:value={store.filter}
        />
      </div>
    </div>
  {/if}

  {#if store.visibleNodes.length > 0 && store.available}
    <UserList />
  {/if}

  {#if store.nodes.length > 0 && store.visibleNodes.length === 0 && store.available}
    <p class="empty-state">{m.users_no_match()}</p>
  {/if}

  {#if store.nodes.length === 0 && !store.loading && !auth.authError && !store.error && store.available}
    <p class="empty-state">{m.users_empty()}</p>
  {/if}
</div>

<style>
  .users-help-notice {
    margin-bottom: 20px;
  }
</style>
