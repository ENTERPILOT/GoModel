<script>
  // "Authentication required" page banner with the Enter-API-key button.
  // Owns its own visibility (auth.authError), so pages just drop it in.
  import { auth } from "$lib/stores/auth.svelte.js";
  import { i18n } from "$lib/i18n/i18n.svelte.js";
</script>

{#if auth.authError}
  <div class="alert alert-warning auth-banner" role="alert">
    <span>{i18n.t("auth.banner.required")}</span>
    <button
      type="button"
      class="btn btn-primary"
      onclick={() => auth.openDialog()}
    >
      {i18n.t("sidebar.actions.enterApiKey")}
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
