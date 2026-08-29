<script>
  // Users page: the group tree with member users and API key counts, plus
  // the per-user / per-group model access editor. User paths are derived
  // from the tree.
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import FilterInput from "$lib/components/molecules/FilterInput.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { usersStore as store } from "./users.svelte.js";
  import UserList from "./UserList.svelte";
  import UserEditor from "./UserEditor.svelte";
  import GroupEditor from "./GroupEditor.svelte";
  import AccessEditor from "./AccessEditor.svelte";
  import { Plus, UsersRound } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  const PAGE = "users";

  // Re-fetch when the page becomes active or the API key changes.
  $effect(() => {
    void auth.refreshTick;
    if (router.page === PAGE) store.fetchAll();
  });
</script>

<div>
  <div class="page-header">
    <h2>{m.users_title()}</h2>
    <div class="page-header-controls">
      {#if store.available && !auth.authError}
        <button
          type="button"
          class="btn btn-secondary btn-with-icon"
          onclick={() => store.openGroupEditor()}
        >
          <Icon icon={UsersRound} class="table-icon-svg" />
          <span>{m.groups_create()}</span>
        </button>
        <button
          type="button"
          class="btn btn-primary btn-with-icon"
          onclick={() => store.openUserEditor()}
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
  {#if store.error && !auth.authError}
    <p class="form-error" role="alert" aria-live="assertive">{store.error}</p>
  {/if}

  {#if store.available && !auth.authError}
    <p class="form-hint users-help-notice">{m.users_help()}</p>
  {/if}

  <UserEditor />
  <GroupEditor />
  <AccessEditor />

  {#if store.loading && store.users.length === 0 && store.groups.length === 0}
    <LoadingState label={m.users_loading()} />
  {/if}

  {#if store.available && !auth.authError}
    {#if store.users.length > 0 || store.groups.length > 0}
      <div class="table-toolbar">
        <div class="table-toolbar-main">
          <FilterInput
            placeholder={m.users_filter_placeholder()}
            label={m.users_filter_label()}
            bind:value={store.filter}
          />
        </div>
      </div>
      <UserList />
    {:else if !store.loading && !store.error}
      <p class="empty-state">{m.users_empty()}</p>
    {/if}
  {/if}
</div>

<style>
  .users-help-notice {
    margin-bottom: 20px;
  }
</style>
