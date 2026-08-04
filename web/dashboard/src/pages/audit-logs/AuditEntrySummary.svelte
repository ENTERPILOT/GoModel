<script>
  // The collapsed row of one audit entry: status/method/model/path on the
  // left, attempt pips, timing and the interactions trigger on the right.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { auditModelDisplay, formatTimestampUTC } from "$lib/utils/format.js";
  import AuditAttemptTrack from "./AuditAttemptTrack.svelte";
  import { auditList } from "./auditList.svelte.js";
  import { conversationDrawer } from "./conversationDrawer.svelte.js";
  import {
    auditEntryLiveInProgress,
    formatDurationNs,
    statusCodeClass,
  } from "./audit-logic.js";
  import { ChevronDown, ChevronRight } from "lucide";

  // `thread` marks a session-thread head row (grouped mode):
  // { count, expanded, ontoggle }. `expanded`/`onactivate` wire the
  // Svelte-controlled row expansion: the click is intercepted (the parent
  // <details> stays open so the close can animate) and the state lives in
  // auditList.
  let { entry, thread = null, expanded = false, onactivate = null } = $props();

  function onSummaryClick(event) {
    event.preventDefault();
    if (onactivate) onactivate();
  }

  // The expander is a button nested in <summary>, so it must not toggle the
  // entry's own expansion — same treatment as openConversation.
  function toggleThread(event) {
    event.stopPropagation();
    event.preventDefault();
    thread.ontoggle();
  }

  function openConversation(event) {
    event.stopPropagation();
    event.preventDefault();
    auditList.expandAuditEntry(entry);
    conversationDrawer.openConversation(entry, event.currentTarget);
  }
</script>

<summary
  class="audit-entry-summary"
  class:audit-entry-summary-live-in-progress={auditEntryLiveInProgress(entry)}
  aria-expanded={expanded}
  onclick={onSummaryClick}
>
  <div class="audit-entry-left">
    {#if thread}
      <button
        type="button"
        class="audit-thread-expander"
        aria-expanded={thread.expanded}
        title={"Session with " + thread.count + " requests"}
        aria-label={"Session with " +
          thread.count +
          " requests, " +
          (thread.expanded ? "collapse" : "expand")}
        onclick={toggleThread}
      >
        <Icon
          icon={thread.expanded ? ChevronDown : ChevronRight}
          class="audit-thread-expander-svg"
        />
        <span class="audit-thread-count mono">{thread.count}</span>
      </button>
    {/if}
    <span class="audit-status-badge {statusCodeClass(entry.status_code)}"
      >{entry.status_code || "-"}</span
    >
    <span class="audit-method-badge">{entry.method || "-"}</span>
    {#if entry.requested_model || entry.model}
      <span class="audit-provider-model mono">{auditModelDisplay(entry)}</span>
    {/if}
    <span class="audit-path mono">{entry.path || "-"}</span>
  </div>
  <div class="audit-entry-right">
    <AuditAttemptTrack {entry} />
    <span class="mono font-size-md" title={formatTimestampUTC(entry.timestamp)}
      >{timezone.formatTimestamp(entry.timestamp)}</span
    >
    <span class="mono font-size-md">{formatDurationNs(entry.duration_ns)}</span>
    {#if conversationDrawer.canShowConversation(entry)}
      <button
        type="button"
        class="audit-conversation-trigger"
        title="Open interactions"
        aria-label="Open interactions"
        onclick={openConversation}
      >
        <Icon icon={ChevronRight} class="audit-conversation-trigger-svg" />
      </button>
    {/if}
  </div>
</summary>

<style>
  .audit-entry-summary {
    list-style: none;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 5px 14px;
    position: relative;
    cursor: pointer;
  }

  .audit-entry-summary::-webkit-details-marker {
    display: none;
  }

  .audit-entry-summary-live-in-progress {
    background: color-mix(in srgb, var(--info) 7%, var(--bg));
  }

  .audit-entry-summary-live-in-progress::before {
    content: "";
    position: absolute;
    inset: 0 auto 0 0;
    width: 3px;
    background: var(--info);
    pointer-events: none;
    animation: audit-live-summary-stripe-blink 1.2s ease-in-out infinite;
  }

  @media (prefers-reduced-motion: reduce) {
    .audit-entry-summary-live-in-progress::before {
        animation: none;
        opacity: 0.78;
      }
  }

  .audit-entry-left {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }

  .audit-entry-right {
    display: inline-flex;
    align-items: center;
    gap: 10px;
    color: var(--text-muted);
    flex-shrink: 0;
    min-height: 28px;
  }

  /* Thread expander: chevron + entry count, sized like the interactions
     trigger for a consistent hit target. */
  .audit-thread-expander {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 3px;
    height: 28px;
    min-width: 28px;
    padding: 0 7px 0 4px;
    border-radius: 6px;
    border: 1px solid var(--border);
    background: var(--bg-surface);
    color: var(--text-muted);
    cursor: pointer;
    transition:
      background 0.1s ease-out,
      color 0.1s ease-out;
  }

  .audit-thread-expander:hover {
    background: color-mix(in srgb, var(--accent) 12%, var(--bg));
    color: var(--accent);
  }

  .audit-thread-expander :global(.audit-thread-expander-svg) {
    width: 14px;
    height: 14px;
  }

  .audit-thread-count {
    font-size: 11px;
    font-weight: 600;
  }

  .audit-conversation-trigger {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    margin-right: -9px;
    border-radius: 6px;
    border: 1px solid color-mix(in srgb, var(--accent) 55%, var(--border));
    background: color-mix(in srgb, var(--accent) 12%, var(--bg));
    color: var(--accent);
    cursor: pointer;
    transition:
      transform 0.1s ease-out,
      background 0.1s ease-out,
      color 0.1s ease-out;
  }

  .audit-conversation-trigger:hover {
    background: color-mix(in srgb, var(--accent) 20%, var(--bg));
    transform: translateX(1px);
  }

  /* The Icon atom hard-codes lucide's round caps and 24px attribute size;
     these CSS declarations win over presentation attributes, keeping the
     chevron rendered exactly like the previous hand-rolled SVG (butt caps,
     miter joins, 14px). */
  .audit-conversation-trigger :global(.audit-conversation-trigger-svg) {
    width: 14px;
    height: 14px;
    stroke-linecap: butt;
    stroke-linejoin: miter;
  }

  .audit-path {
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* Same pill shape as .audit-status-badge, in the muted palette. */
  .audit-method-badge {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 52px;
    height: 24px;
    padding: 0 10px;
    border-radius: 999px;
    border: 1px solid var(--border);
    font-size: 12px;
    font-weight: 600;
    letter-spacing: 0.2px;
    background: var(--bg-surface);
    color: var(--text-muted);
  }

  .audit-provider-model {
    height: 24px;
    padding: 0 10px;
    display: inline-flex;
    align-items: center;
    border-radius: 999px;
    border: 1px solid var(--border);
    font-size: 12px;
    background: var(--bg-surface);
    color: var(--text-muted);
    white-space: nowrap;
    max-width: 420px;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  @media (max-width: 768px) {
    .audit-entry-summary {
        flex-direction: column;
        align-items: flex-start;
      }

    .audit-entry-right {
        width: 100%;
        justify-content: space-between;
      }
  }
</style>
