<script>
  // Collapsed-row attempt indicator: one pip per provider attempt, colored by
  // outcome, so failover/retry is visible without expanding the record.
  // Renders nothing when the entry has no attempt track.
  import {
    auditAttemptSegmentTitle,
    auditAttemptTrack,
    auditAttemptTrackCount,
    auditAttemptTrackTitle,
  } from "./audit-logic.js";

  let { entry } = $props();

  // Each of these rebuilds (and re-sorts) the attempt list, so they are derived
  // once per entry instead of once per template read.
  const track = $derived(auditAttemptTrack(entry));
  const title = $derived(track.length > 0 ? auditAttemptTrackTitle(entry) : "");
  const count = $derived(track.length > 0 ? auditAttemptTrackCount(entry) : "");
</script>

{#if track.length > 0}
  <span class="audit-attempt-track" role="img" {title} aria-label={title}>
    <span class="audit-attempt-track-pips">
      {#each track as seg (entry.id + "-pip-" + seg.seq)}
        <span
          class="audit-attempt-pip"
          class:audit-attempt-success={!!(seg && seg.success)}
          class:audit-attempt-error={!(seg && seg.success)}
          title={auditAttemptSegmentTitle(seg)}
        ></span>
      {/each}
    </span>
    <span class="audit-attempt-track-count mono">{count}</span>
  </span>
{/if}

<style>
  .audit-attempt-track {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    cursor: pointer;
  }

  .audit-attempt-track-pips {
    display: inline-flex;
    align-items: center;
    gap: 3px;
  }

  .audit-attempt-pip {
    width: 8px;
    height: 8px;
    border-radius: 2px;
    background: var(--text-muted);
  }

  .audit-attempt-pip.audit-attempt-success {
    background: var(--success);
  }

  .audit-attempt-pip.audit-attempt-error {
    background: var(--danger);
  }

  .audit-attempt-track-count {
    font-size: 11px;
    color: var(--text-muted);
  }
</style>
