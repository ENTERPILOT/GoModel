<script>
  // Settings → Failover: bulk-generate failover drafts and reset all
  // mappings. Drives the shared failover store (models page); the drafts
  // modal is mounted here too so the generate flow works from Settings.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { failover } from "$pages/models/failover.svelte.js";
  import FailoverDrafts from "$pages/models/FailoverDrafts.svelte";

  const busy = $derived(
    failover.failoverSaving ||
      failover.failoverGenerating ||
      failover.failoverDraftSaving ||
      !failover.failoverAvailable ||
      !failover.failoverEnabled(),
  );
</script>

<div class="settings-refresh-section">
  <div>
    <h3>Failover</h3>
  </div>
  <div class="settings-refresh-actions">
    <button
      type="button"
      class="pagination-btn pagination-btn-primary pagination-btn-with-icon"
      disabled={busy}
      onclick={() => failover.generateFailoverRules()}
    >
      <Icon name="wand-sparkles" class="form-action-icon" />
      <span>
        {failover.failoverGenerating
          ? "Generating..."
          : "Generate failover models automatically"}
      </span>
    </button>
    <button
      type="button"
      class="pagination-btn pagination-btn-danger-outline pagination-btn-with-icon"
      disabled={busy}
      onclick={() => failover.openFailoverResetDialog()}
    >
      <Icon name="trash-2" class="form-action-icon" />
      <span>Remove all the failover models</span>
    </button>
  </div>
</div>
<div>
  {#if failover.failoverError}
    <div
      class="alert alert-warning settings-refresh-alert"
      role="alert"
      aria-live="assertive"
      aria-atomic="true"
    >
      {failover.failoverError}
    </div>
  {/if}
</div>

<FailoverDrafts />
