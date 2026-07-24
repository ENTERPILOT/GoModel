<script>
  // One expandable audit-log entry row.
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import {
    auditModelDisplay,
    formatTimestampUTC,
    providerDisplayValue,
    qualifiedResolvedModelDisplay,
  } from "$lib/utils/format.js";
  import WorkflowChart from "$pages/workflows/WorkflowChart.svelte";
  import { workflowAuditChart } from "$pages/workflows/workflowChartLogic.js";
  import AuditPane from "./AuditPane.svelte";
  import { auditList } from "./auditList.svelte.js";
  import { auditWorkflows } from "./audit-workflows.svelte.js";
  import { liveLogs } from "./liveLogs.svelte.js";
  import { conversationDrawer } from "./conversationDrawer.svelte.js";
  import { extractRequestPromptTextSegments } from "./conversation-helpers.js";
  import {
    auditAttemptSegmentTitle,
    auditAttemptTrack,
    auditAttemptTrackCount,
    auditAttemptTrackTitle,
    auditEffectiveTab,
    auditEntryLiveInProgress,
    auditHasAttemptTrack,
    auditPanes,
    auditTabKeydownTarget,
    formatDurationNs,
    statusCodeClass,
    workflowFailoverTarget,
  } from "./audit-logic.js";

  let { entry } = $props();

  // Active request/response tab; null falls back to the default tab until the
  // user picks one.
  let activeTab = $state(null);

  const expanded = $derived(auditList.isAuditEntryExpanded(entry));
  const panes = $derived(
    expanded ? auditPanes(entry, extractRequestPromptTextSegments) : [],
  );
  const effectiveTab = $derived(
    expanded ? auditEffectiveTab(activeTab, entry) : null,
  );

  // Workflow pipeline chart; the resolved version comes from the audit-side
  // version cache filled by prefetchAuditWorkflows.
  const workflowChart = $derived(
    expanded
      ? workflowAuditChart(
          entry,
          auditWorkflows.auditEntryWorkflow(entry),
          auditWorkflows.workflowFeatureCaps(),
        )
      : null,
  );

  function onToggle(event) {
    const detailsEl = event && event.currentTarget;
    if (!detailsEl || !detailsEl.open) return;
    auditList.markAuditEntryExpanded(entry);
    if (typeof liveLogs.fetchAuditEntryDetail === "function") {
      liveLogs.fetchAuditEntryDetail(entry);
    }
  }

  function openConversationFromRow(event) {
    event.stopPropagation();
    event.preventDefault();
    conversationDrawer.openConversation(
      entry,
      event.currentTarget.closest("details"),
      true,
      event.currentTarget,
    );
  }

  function onTabKeydown(event, currentId) {
    const ids = panes.map((p) => p.id);
    const next = auditTabKeydownTarget(event.key, ids, currentId);
    if (next == null) return;
    event.preventDefault();
    const tablist =
      event.currentTarget && event.currentTarget.closest
        ? event.currentTarget.closest(".audit-pane-tablist")
        : null;
    if (tablist) {
      const buttons = tablist.querySelectorAll(".audit-pane-tab");
      const target = buttons[ids.indexOf(next)];
      if (target && typeof target.focus === "function") target.focus();
    }
    activeTab = next;
  }
</script>

<details class="audit-entry" ontoggle={onToggle}>
  <summary
    class="audit-entry-summary"
    class:audit-entry-summary-live-in-progress={auditEntryLiveInProgress(entry)}
  >
    <div class="audit-entry-left">
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
      {#if auditHasAttemptTrack(entry)}
        <span
          class="audit-attempt-track"
          role="img"
          title={auditAttemptTrackTitle(entry)}
          aria-label={auditAttemptTrackTitle(entry)}
        >
          <span class="audit-attempt-track-pips">
            {#each auditAttemptTrack(entry) as seg (entry.id + "-pip-" + seg.seq)}
              <span
                class="audit-attempt-pip"
                class:audit-attempt-success={!!(seg && seg.success)}
                class:audit-attempt-error={!(seg && seg.success)}
                title={auditAttemptSegmentTitle(seg)}
              ></span>
            {/each}
          </span>
          <span class="audit-attempt-track-count mono"
            >{auditAttemptTrackCount(entry)}</span
          >
        </span>
      {/if}
      <span
        class="mono font-size-md"
        title={formatTimestampUTC(entry.timestamp)}
        >{timezone.formatTimestamp(entry.timestamp)}</span
      >
      <span class="mono font-size-md">{formatDurationNs(entry.duration_ns)}</span>
      {#if conversationDrawer.canShowConversation(entry)}
        <button
          type="button"
          class="audit-conversation-trigger"
          title="Open interactions"
          aria-label="Open interactions"
          onclick={openConversationFromRow}
        >
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <path d="M9 6l6 6-6 6" />
          </svg>
        </button>
      {/if}
    </div>
  </summary>
  {#if expanded}
    <div class="audit-entry-details">
      {#if workflowChart}
        <WorkflowChart chart={workflowChart} />
      {/if}

      <div class="audit-request-response">
        <div
          class="audit-pane-tablist"
          role="tablist"
          aria-label="Request and response"
        >
          {#each panes as p (p.id)}
            <button
              type="button"
              class="audit-pane-tab"
              class:audit-pane-tab-active={effectiveTab === p.id}
              role="tab"
              aria-selected={effectiveTab === p.id}
              id={"audit-tab-" + entry.id + "-" + p.id}
              aria-controls={"audit-tabpanel-" + entry.id + "-" + p.id}
              tabindex={effectiveTab === p.id ? 0 : -1}
              onkeydown={(event) => onTabKeydown(event, p.id)}
              onclick={() => (activeTab = p.id)}
            >
              <span
                class="audit-pane-icon audit-pane-icon-{p.pane.direction || ''}"
                aria-hidden="true"
              >
                {#if p.pane.direction === "request"}
                  <svg
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="2"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    ><line x1="5" x2="19" y1="12" y2="12" /><polyline
                      points="12 5 19 12 12 19"
                    /></svg
                  >
                {:else if p.pane.direction === "response"}
                  <svg
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="2"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    ><line x1="19" x2="5" y1="12" y2="12" /><polyline
                      points="12 19 5 12 12 5"
                    /></svg
                  >
                {/if}
              </span>
              <span class="audit-pane-tab-label">{p.pane.title}</span>
              {#if p.pane.seq}
                <span class="audit-pane-seq mono">#{p.pane.seq}</span>
              {/if}
              {#if p.pane.kind}
                <span
                  class="provider-badge audit-pane-kind audit-pane-kind-{p.pane
                    .kind || ''}">{p.pane.kind}</span
                >
              {/if}
              {#if p.pane.savingsLabel}
                <span
                  class="audit-savings-pill mono"
                  title="Share of the request body removed by this rewrite"
                  >{p.pane.savingsLabel}</span
                >
              {/if}
              {#if p.pane.statusCode}
                <span
                  class="audit-status-badge {statusCodeClass(p.pane.statusCode)}"
                  >{p.pane.statusCode}</span
                >
              {/if}
            </button>
          {/each}
        </div>
        {#each panes as p (p.id)}
          <!-- Inactive panels stay in the DOM (hidden, not unmounted) so audio
               playback and scroll position survive tab switches. -->
          <div
            class="audit-pane-tabpanel"
            style:display={effectiveTab === p.id ? null : "none"}
            role="tabpanel"
            id={"audit-tabpanel-" + entry.id + "-" + p.id}
            aria-labelledby={"audit-tab-" + entry.id + "-" + p.id}
          >
            <AuditPane pane={p.pane} />
          </div>
        {/each}
      </div>

      <div class="audit-entry-metadata">
        <span class="audit-entry-metadata-label">Metadata:</span>
        <div class="audit-entry-context">
          <span class="provider-badge">{providerDisplayValue(entry) || "-"}</span>
          <span class="provider-badge mono"
            >{entry.requested_model || entry.model || "-"}</span
          >
          {#if entry.user_path}
            <span class="provider-badge mono">{entry.user_path}</span>
          {/if}
          <span class="provider-badge mono"
            >request_id: {entry.request_id || "-"}</span
          >
          {#if entry.client_ip}
            <span class="provider-badge mono">ip: {entry.client_ip}</span>
          {/if}
          {#if entry.auth_key_id}
            <span class="provider-badge mono"
              >auth_key_id: {entry.auth_key_id}</span
            >
          {/if}
          {#if entry.alias_used}
            <span class="provider-badge audit-alias-badge">alias</span>
          {/if}
          {#if entry.alias_used && entry.resolved_model}
            <span class="provider-badge mono"
              >resolved: {qualifiedResolvedModelDisplay(entry)}</span
            >
          {/if}
          {#if workflowFailoverTarget(entry)}
            <span class="provider-badge mono"
              >failover: {workflowFailoverTarget(entry)}</span
            >
          {/if}
          {#if entry.stream}
            <span class="provider-badge">stream</span>
          {/if}
          {#if entry.error_type}
            <span class="provider-badge">{entry.error_type}</span>
          {/if}
        </div>
      </div>
    </div>
  {/if}
</details>

<style>
  /* Styles owned by this component (moved from dashboard.css). */
  .audit-alias-badge {
    background: color-mix(in srgb, var(--accent) 14%, var(--bg));
    border-color: color-mix(in srgb, var(--accent) 28%, var(--border));
    color: var(--accent-strong, var(--accent));
  }

  .audit-entry {
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--bg);
    overflow: hidden;
  }

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

  .audit-entry-summary::-webkit-details-marker {
    display: none;
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

  .audit-conversation-trigger :global(svg) {
    width: 14px;
    height: 14px;
  }

  .audit-path {
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .audit-method-badge {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 46px;
    height: 24px;
    padding: 0 10px;
    border-radius: 999px;
    border: 1px solid var(--border);
    font-size: 12px;
    font-weight: 600;
    letter-spacing: 0.2px;
    background: var(--bg-surface);
  }

  .audit-method-badge {
    min-width: 52px;
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

  .audit-entry-details {
    border-top: 1px solid var(--border);
    padding: 12px;
    background: var(--bg-surface);
    overflow: hidden;
  }

  .audit-request-response {
    margin-top: 4px;
  }

  /* Request / Response(s) are presented as tabs; the tab strip carries the
     direction icon, title, attempt type and status, and the active panel shows
     that pane's content. */
  .audit-pane-tablist {
    display: flex;
    flex-wrap: wrap;
    gap: 2px;
    border-bottom: 1px solid var(--border);
  }

  .audit-pane-tab {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    border: 1px solid var(--border);
    border-bottom-color: transparent;
    border-radius: 6px 6px 0 0;
    margin-bottom: -1px;
    margin-right: 12px;
    background: transparent;
    color: var(--text-muted);
    font-family: inherit;
    font-size: 13px;
    cursor: pointer;
    transition:
      color 0.15s,
      border-color 0.15s,
      background-color 0.15s;
  }

  .audit-pane-tab:not(.audit-pane-tab-active):hover {
    color: var(--text);
    background: color-mix(in srgb, var(--text) 5%, transparent);
  }

  /* Active tab reads as a card: bordered top + sides with slightly rounded top
     corners, and a background-colored bottom edge that blends into the content
     area (covering the tablist underline). */
  .audit-pane-tab-active {
    color: var(--text);
    border-color: var(--border);
    border-bottom-color: var(--bg);
    background: var(--bg);
  }

  .audit-pane-tab-label {
    font-weight: 600;
  }

  .audit-pane-tabpanel {
    min-width: 0;
  }

  /* Direction icon: request (→) vs response (←), distinct hues so the two panes
     are scannable at a glance without relying on success/error color. */
  .audit-pane-icon {
    display: inline-flex;
    align-items: center;
    flex: 0 0 auto;
    color: var(--text-muted);
  }

  .audit-pane-icon :global(svg) {
    width: 16px;
    height: 16px;
  }

  .audit-pane-seq {
    color: var(--text-muted);
    font-size: 12px;
  }

  /* Attempt type rendered as a pill, accented so failover/retry stand out. */
  .audit-pane-kind {
    text-transform: uppercase;
    letter-spacing: 0.04em;
    font-size: 10px;
    font-weight: 600;
  }

  /* Share of the request body removed by a rewrite (e.g. token compression),
     shown on the Rewritten tab next to the rewriter pill. Same blue family as
     the cached tokens in the charts (both derive from --info); the prompt-cache
     variables carry the per-theme tone mix that keeps the text readable. */
  .audit-savings-pill {
    display: inline-flex;
    align-items: center;
    padding: 1px 7px;
    border: 1px solid color-mix(in srgb, var(--prompt-cache-color) 45%, var(--border));
    border-radius: 999px;
    background: var(--prompt-cache-color-bg);
    color: var(--prompt-cache-color);
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.02em;
  }

  .audit-entry-metadata {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-top: 12px;
    padding-top: 12px;
    border-top: 1px solid var(--border);
  }

  .audit-entry-metadata-label {
    flex: 0 0 auto;
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }

  .audit-entry-context {
    display: flex;
    flex: 1 1 auto;
    flex-wrap: wrap;
    gap: 8px;
  }

  /* Collapsed-row attempt indicator: one pip per provider attempt, colored by
     outcome, so failover/retry is visible without expanding the record. */
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

  @media (max-width: 768px) {
    .audit-entry-summary {
        flex-direction: column;
        align-items: flex-start;
      }

    .audit-entry-right {
        width: 100%;
        justify-content: space-between;
      }

    .audit-entry-metadata {
        flex-direction: column;
        align-items: flex-start;
        gap: 8px;
      }
  }
</style>
