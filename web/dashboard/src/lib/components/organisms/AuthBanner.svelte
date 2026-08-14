<script>
  // "Authentication required" page banner with the Enter-API-key button.
  // Owns its own visibility (auth.authError), so pages just drop it in.
  import { auth } from "$lib/stores/auth.svelte.js";
  import * as m from "$lib/paraglide/messages.js";
</script>

{#if auth.authError}
  <div class="alert alert-warning auth-banner" role="alert">
    <span>{m.auth_banner_required()}</span>
    <button
      type="button"
      class="btn btn-primary"
      onclick={() => auth.openDialog()}
    >
      {m.sidebar_action_enter_api_key()}
    </button>
  </div>
{/if}

<style>
  .auth-banner {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
  }
</style>
