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
