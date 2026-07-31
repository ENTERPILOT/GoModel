<script>
  // Live-stream status chip for the audit list: a pulsing dot while live
  // inserts flow, a muted dot plus a how-to-resume hint while they are
  // paused (filters, pagination, or a date range detached from today), and
  // a connecting note while the stream is down. Hidden entirely when the
  // deployment disables live logs.
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import { liveLogs } from "./liveLogs.svelte.js";
  import { auditLivePauseMessage } from "./live-logs-logic.js";

  const pauseReason = $derived(liveLogs.auditLivePauseReason());
  const live = $derived(liveLogs.liveLogsStreaming && !pauseReason);
</script>

{#if runtimeConfig.liveLogsVisible()}
  <span class="audit-live-status" role="status">
    <span class="live-dot" class:is-streaming={live} aria-hidden="true"></span>
    {#if !liveLogs.liveLogsStreaming}
      <span class="audit-live-status-text">Live stream connecting…</span>
    {:else if pauseReason}
      <span class="audit-live-status-text">{auditLivePauseMessage(pauseReason)}</span>
    {:else}
      <span class="audit-live-status-text is-live">Live</span>
    {/if}
  </span>
{/if}

<style>
  .audit-live-status {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    font-size: 12px;
    color: var(--text-muted);
    /* The pulsing ring expands past the dot; keep it from clipping. */
    padding-left: 4px;
  }

  .audit-live-status-text.is-live {
    color: var(--success);
    font-weight: 600;
  }
</style>
