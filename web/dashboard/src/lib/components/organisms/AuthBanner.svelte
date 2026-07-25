<script>
  // "Authentication required" page banner with the Enter-API-key button.
  // Owns its own visibility (auth.authError), so pages just drop it in.
  import { auth } from "$lib/stores/auth.svelte.js";
</script>

{#if auth.authError}
  <div class="alert alert-warning auth-banner" role="alert">
    <span>Authentication required for dashboard data.</span>
    <button
      type="button"
      class="btn btn-primary"
      onclick={() => auth.openDialog()}
    >
      Enter API key
    </button>
  </div>
{/if}

<style>
  /* Styles owned by this component (moved from dashboard.css). */
  .auth-banner {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
  }
</style>
