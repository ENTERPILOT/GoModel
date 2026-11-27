<script>
  // The collapsed row of one request log entry: status/method/model/path on the
  // left, attempt pips, timing and the interactions trigger on the right.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { requestModelDisplay, formatTimestampUTC } from "$lib/utils/format.js";
  import RequestAttemptTrack from "./RequestAttemptTrack.svelte";
  import { requestList } from "./requestList.svelte.js";
  import { requestEntryLabels } from "./request-columns.js";
  import { conversationDrawer } from "./conversationDrawer.svelte.js";
  import {
    requestEntryLiveInProgress,
    requestOutputTokensPerSecond,
    formatDurationNs,
    formatTokensPerSecond,
    statusCodeClass,
  } from "./request-log-logic.js";
  import { labelChipStyle } from "$lib/utils/chartTheme.js";
  import { ChartNoAxesColumnIncreasing, ChevronDown, ChevronLeft, ChevronRight } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  // `thread` marks a session-thread head row (grouped mode):
  // { count, expanded, ontoggle }. `expanded`/`onactivate` wire the
  // Svelte-controlled row expansion: the click is intercepted (the parent
  // <details> stays open so the close can animate) and the state lives in
  // requestList.
  let {
    entry,
    thread = null,
    expanded = false,
    onactivate = null,
    onusage = null,
    hidePath = false,
  } = $props();

  const columns = $derived(requestList.requestColumns);
  const labels = $derived(requestEntryLabels(entry));
  const tokensPerSecond = $derived(
    formatTokensPerSecond(requestOutputTokensPerSecond(entry)),
  );
  const timestamp = $derived(timezone.formatTimestamp(entry.timestamp));
  const timestampTime = $derived(
    timestamp === "-" ? timestamp : timestamp.slice(timestamp.lastIndexOf(" ") + 1),
  );
  const interactionsOpen = $derived(
    conversationDrawer.conversationOpen &&
      String(conversationDrawer.conversationAnchorID || "") ===
        String(entry.id || ""),
  );

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

  function openSessionUsage(event) {
    event.stopPropagation();
    event.preventDefault();
    onusage?.();
  }

  function threadDescription() {
    return m.requests_session_description({ count: Number(thread.count || 1) });
  }

  function openConversation(event) {
    event.stopPropagation();
    event.preventDefault();
    if (interactionsOpen) {
      conversationDrawer.closeConversation();
      return;
    }
    conversationDrawer.openConversation(entry, event.currentTarget);
  }
</script>

<summary
  class="request-log-entry-summary"
  class:request-log-entry-summary-live-in-progress={requestEntryLiveInProgress(entry)}
  class:request-log-entry-summary-has-interactions={conversationDrawer.canShowConversation(
    entry,
  )}
  aria-expanded={expanded}
  onclick={onSummaryClick}
>
  <div class="request-log-entry-left">
    {#if thread}
      <button
        type="button"
        class="request-log-thread-expander"
        aria-expanded={thread.expanded}
        title={threadDescription()}
        aria-label={threadDescription() +
          ", " +
          (thread.expanded
            ? m.common_action_collapse()
            : m.common_action_expand())}
        onclick={toggleThread}
      >
        <Icon
          icon={thread.expanded ? ChevronDown : ChevronRight}
          class="request-log-thread-expander-svg"
        />
        <span class="request-log-thread-count mono">{thread.count}</span>
      </button>
    {/if}
    {#if onusage}
      <button
        type="button"
        class="request-log-session-usage"
        title={m.requests_view_session_usage()}
        aria-label={m.requests_view_session_usage()}
        onclick={openSessionUsage}
      >
        <Icon icon={ChartNoAxesColumnIncreasing} class="request-log-session-usage-svg" />
      </button>
    {/if}
    {#if columns.status}
      <span
        class="request-log-status-badge request-log-request-badge {statusCodeClass(
          entry.status_code,
        )}"
        title={m.requests_status({
          status: entry.status_code || "-",
          method: entry.method || "-",
        })}
        aria-label={(entry.status_code || "-") + " " + (entry.method || "-")}
      >
        <span class="request-log-request-status" aria-hidden="true"
          >{entry.status_code || "-"}</span
        >
        <span aria-hidden="true">{entry.method || "-"}</span>
      </span>
    {/if}
    {#if columns.model && (entry.requested_model || entry.model)}
      <span class="request-log-provider-model mono">{requestModelDisplay(entry)}</span>
    {/if}
    {#if columns.path && !hidePath}
      <span class="request-log-path mono">{entry.path || "-"}</span>
    {/if}
    {#if columns.labels && labels.length > 0}
      <span class="usage-label-chips">
        {#each labels as label (label)}
          <span class="usage-label-chip request-log-label-chip" style={labelChipStyle(label)}
            >{label}</span
          >
        {/each}
      </span>
    {/if}
  </div>
  <div class="request-log-entry-right">
    {#if columns.attempts}
      <RequestAttemptTrack {entry} />
    {/if}
    {#if columns.time}
      <span
        class="request-log-timestamp mono font-size-md"
        title={formatTimestampUTC(entry.timestamp)}
        aria-label={timestamp}
      >
        <span class="request-log-timestamp-full" aria-hidden="true">{timestamp}</span>
        <span class="request-log-timestamp-time" aria-hidden="true">{timestampTime}</span>
      </span>
    {/if}
    {#if columns.duration}
      <span class="request-log-timing mono font-size-md">{formatDurationNs(entry.duration_ns)}</span>
    {/if}
    {#if columns.tokens && tokensPerSecond}
      <span class="request-log-token-rate mono" title={m.requests_tokens_per_second_title()}
        >{tokensPerSecond}</span
      >
    {/if}
    {#if conversationDrawer.canShowConversation(entry)}
      <button
        type="button"
        class="request-log-conversation-trigger"
        class:request-log-conversation-trigger-active={interactionsOpen}
        title={interactionsOpen
          ? m.requests_close_interactions()
          : m.requests_open_interactions()}
        aria-label={interactionsOpen
          ? m.requests_close_interactions()
          : m.requests_open_interactions()}
        aria-pressed={interactionsOpen}
        onclick={openConversation}
      >
        <Icon
          icon={interactionsOpen ? ChevronLeft : ChevronRight}
          class="request-log-conversation-trigger-svg"

        />
      </button>
    {/if}
  </div>
</summary>

<style>
  .request-log-entry-summary {
    list-style: none;
    display: flex;
    align-items: center;

    justify-content: space-between;
    gap: 12px;
    padding: 4px 16px;

    position: relative;
    cursor: pointer;
  }


  .request-log-entry-summary::-webkit-details-marker {
    display: none;
  }

  .request-log-entry-summary-live-in-progress {
    background: color-mix(in srgb, var(--primary) 7%, var(--bg));
  }

  .request-log-entry-summary-has-interactions {
    padding-right: 44px;
  }


  .request-log-entry-summary-live-in-progress::before {
    content: "";
    position: absolute;
    inset: 0 auto 0 0;
    width: 3px;

    background: var(--primary);
    pointer-events: none;
    animation: request-log-live-summary-stripe-blink 1.2s ease-in-out infinite;
  }

  @media (prefers-reduced-motion: reduce) {
    .request-log-entry-summary-live-in-progress::before {
        animation: none;
        opacity: 0.78;
      }
  }

  .request-log-entry-left {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }

  .request-log-entry-right {
    display: inline-flex;
    align-items: center;
    gap: 12px;
    color: var(--text-muted);
    flex-shrink: 0;
    min-height: 28px;
  }

  /* Thread expander: chevron + entry count, sized like the interactions
     trigger for a consistent hit target. */
  .request-log-thread-expander {
    display: inline-flex;
    align-items: center;
    justify-content: center;

    gap: 4px;
    flex-shrink: 0;
    height: 28px;
    min-width: 28px;

    padding: 0 8px 0 4px;
    border-radius: var(--radius-control);
    border: 1px solid var(--border);
    background: var(--bg-surface);

    color: var(--text-muted);
    cursor: pointer;
    transition:
      background 0.1s ease-out,

      color 0.1s ease-out;
  }

  .request-log-thread-expander:hover {
    background: color-mix(in srgb, var(--accent) 12%, var(--bg));
    color: var(--accent);
  }

  .request-log-thread-expander :global(.request-log-thread-expander-svg) {
    width: 14px;
    height: 14px;
  }

  .request-log-thread-count {
    font-size: 11px;
    font-weight: 600;
  }


  .request-log-session-usage {
    display: inline-flex;
    align-items: center;
    justify-content: center;

    flex-shrink: 0;
    width: 28px;
    height: 28px;
    padding: 0;
    border: 1px solid color-mix(in srgb, var(--accent) 35%, var(--border));
    border-radius: var(--radius-control);
    background: color-mix(in srgb, var(--accent) 10%, var(--bg));
    color: var(--accent-strong, var(--accent));
    cursor: pointer;
  }

  .request-log-session-usage:hover {
    background: color-mix(in srgb, var(--accent) 20%, var(--bg));
    border-color: var(--accent);
  }

  .request-log-session-usage :global(.request-log-session-usage-svg) {
    width: 14px;
    height: 14px;
  }

  .request-log-conversation-trigger {
    position: absolute;
    inset: 0 0 0 auto;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 100%;
    margin: 0;
    border: 0;
    border-left: 1px solid var(--border);
    border-radius: 0;
    background: color-mix(in srgb, var(--accent) 12%, var(--bg));
    color: var(--accent);
    cursor: pointer;
    transition:
      background 0.1s ease-out,
      color 0.1s ease-out;
  }


  .request-log-conversation-trigger:hover {
    background: color-mix(in srgb, var(--accent) 20%, var(--bg));
  }

  .request-log-conversation-trigger-active {
    border-left: 0;
    color: var(--accent-strong, var(--accent));
    background: color-mix(in srgb, var(--accent) 28%, var(--bg));
  }

  .request-log-conversation-trigger-active:hover {
    background: color-mix(in srgb, var(--accent) 34%, var(--bg));
  }


  /* The Icon atom hard-codes lucide's round caps and 24px attribute size;
     these CSS declarations win over presentation attributes, keeping the
     chevron rendered exactly like the previous hand-rolled SVG (butt caps,
     miter joins, 14px). */
  .request-log-conversation-trigger :global(.request-log-conversation-trigger-svg) {
    width: 18px;
    height: 18px;
    stroke-linecap: butt;
    stroke-linejoin: miter;
  }

  .request-log-path {
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .request-log-request-badge {
    flex-shrink: 0;
    gap: 8px;
    min-width: 0;
    white-space: nowrap;

  }

  .request-log-timestamp-time {
    display: none;
  }


  /* Request labels are informational here; the shared chip is a filter
     button on the usage page. */
  .request-log-label-chip {
    cursor: inherit;
  }

  .request-log-timing {
    white-space: nowrap;
  }

  /* The token rate shares the provider/model pill so it does not read as
     part of the plain timestamp and duration text next to it. */
  .request-log-provider-model,

  .request-log-token-rate {
    height: 24px;
    padding: 0 12px;
    display: inline-flex;

    align-items: center;
    border-radius: var(--radius-pill);
    border: 1px solid var(--border);
    font-size: 12px;
    background: var(--bg-surface);

    color: var(--text-muted);
    white-space: nowrap;
  }


  .request-log-provider-model {
    max-width: 420px;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  @container dashboard-content (max-width: 699px) {
    .request-log-entry-summary {
        flex-direction: column;
        align-items: flex-start;
      }

    .request-log-entry-left {
        flex-wrap: wrap;
        max-width: 100%;
      }

    .request-log-provider-model {
        max-width: 100%;
        overflow: hidden;
        text-overflow: ellipsis;
      }

    .request-log-path {
        flex: 1 1 100%;
        min-width: 0;
      }

    .request-log-entry-right {
        width: 100%;
        flex-wrap: wrap;
        justify-content: space-between;
      }

    .request-log-request-status, .request-log-timestamp-full {
        display: none;
      }

    .request-log-timestamp-time {
        display: inline;
      }
  }
</style>
