<script>
  // Collapsed-row attempt indicator: one pip per provider attempt, colored by
  // outcome, so failover/retry is visible without expanding the record.
  // Renders nothing when the entry has no attempt track.
  import {
    requestAttemptSegmentTitle,
    requestAttemptTrack,
    requestAttemptTrackCount,
    requestAttemptTrackTitle,
  } from "./request-log-logic.js";

  let { entry } = $props();

  // Each of these rebuilds (and re-sorts) the attempt list, so they are derived
  // once per entry instead of once per template read.
  const track = $derived(requestAttemptTrack(entry));
  const title = $derived(track.length > 0 ? requestAttemptTrackTitle(entry) : "");
  const count = $derived(track.length > 0 ? requestAttemptTrackCount(entry) : "");
</script>

{#if track.length > 0}
  <span class="request-log-attempt-track" role="img" {title} aria-label={title}>
    <span class="request-log-attempt-track-pips">
      {#each track as seg (entry.id + "-pip-" + seg.seq)}
        <span
          class="request-log-attempt-pip"
          class:request-log-attempt-success={!!(seg && seg.success)}
          class:request-log-attempt-error={!(seg && seg.success)}
          title={requestAttemptSegmentTitle(seg)}
        ></span>
      {/each}
    </span>
    <span class="request-log-attempt-track-count mono">{count}</span>
  </span>
{/if}

<style>
  .request-log-attempt-track {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    cursor: pointer;
  }

  .request-log-attempt-track-pips {
    display: inline-flex;
    align-items: center;
    gap: 3px;
  }

  .request-log-attempt-pip {
    width: 8px;
    height: 8px;
    border-radius: 2px;
    background: var(--text-muted);
  }

  .request-log-attempt-pip.request-log-attempt-success {
    background: var(--success);
  }

  .request-log-attempt-pip.request-log-attempt-error {
    background: var(--danger);
  }

  .request-log-attempt-track-count {
    font-size: 11px;
    color: var(--text-muted);
  }
</style>
