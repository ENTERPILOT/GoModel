<script>
  // Audit Logs page: filterable, paginated audit-log list with expandable
  // entries, live-log merging, and the Interactions drawer trigger.
  import NoDataIllustration from "$lib/components/atoms/NoDataIllustration.svelte";
  import AuthBanner from "$lib/components/organisms/AuthBanner.svelte";
  import { untrack } from "svelte";
  import { flip } from "svelte/animate";
  import { liveSlide, motionDuration } from "$lib/utils/motion.js";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { router } from "$lib/stores/router.svelte.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import DatePicker from "$lib/components/molecules/DatePicker.svelte";
  import Pagination from "$lib/components/molecules/Pagination.svelte";
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import AuditFilters from "./AuditFilters.svelte";
  import AuditLiveStatus from "./AuditLiveStatus.svelte";
  import AuditEntryRow from "./AuditEntryRow.svelte";
  import AuditThreadGroup from "./AuditThreadGroup.svelte";
  import { auditList } from "./auditList.svelte.js";
  import { liveLogs } from "./liveLogs.svelte.js";
  import * as m from "$lib/paraglide/messages.js";
  import {
    auditRetentionHighlight,
    auditRetentionPrefix,
    auditRetentionText,
  } from "./audit-logic.js";

  const PAGE = "audit-logs";

  const retentionRaw = $derived(
    runtimeConfig.config && runtimeConfig.config.LOGGING_RETENTION_DAYS,
  );

  // Load on page activation and whenever the API key changes; start the live
  // log stream after the data fetch and stop it on teardown.
  $effect(() => {
    void auth.refreshTick;
    if (router.page !== PAGE) return;
    return untrack(() => activatePage());
  });

  function activatePage() {
    let cancelled = false;
    (async () => {
      try {
        await runtimeConfig.ensureLoaded();
      } finally {
        await auditList.fetchAuditLog(true);
        // ensureLiveLogs is revisit-safe (no restart while the stream is
        // already running); startLiveLogs itself re-checks the runtime gate.
        if (!cancelled && runtimeConfig.liveLogsVisible()) {
          liveLogs.ensureLiveLogs();
        }
      }
    })();
    return () => {
      cancelled = true;
      liveLogs.stopLiveLogs();
    };
  }
</script>

<div class="page-with-sticky-date">
  <div class="page-header date-range-page-header">
    <div class="inline-help-section">
      <h2>{m.audit_title()}</h2>
      {#if auditRetentionText(retentionRaw)}
        <InlineHelpSection
          copyId="audit-retention-help-copy"
          label={m.audit_retention_help_label()}
          text={m.audit_retention_help()}
        >
          {#snippet title()}
            <p class="audit-retention-note">
              {auditRetentionPrefix(retentionRaw)}<span
                class="audit-retention-highlight"
                >{auditRetentionHighlight(retentionRaw)}</span
              >.
            </p>
          {/snippet}
        </InlineHelpSection>
      {/if}
    </div>
  </div>
  <div class="sticky-date-range">
    <DatePicker onchange={() => auditList.fetchAuditLog(true)} />
  </div>

  <AuthBanner />

  {#if runtimeConfig.loaded && !runtimeConfig.auditVisible() && !auth.needsAuth}
    <div class="alert alert-warning" role="status">
      {m.audit_logging_disabled_before_config()}<code
        >LOGGING_ENABLED=true</code
      >{m.audit_logging_disabled_after_config()}
    </div>
  {/if}

  <div class="audit-log-section">
    <AuditFilters />

    <div class="audit-list-toolbar">
      <div class="audit-list-toolbar-left">
        {#if auditList.auditLog.total > 0}
          <p class="audit-log-summary">
            {auditList.auditGroupSessions
              ? m.audit_summary_sessions({
                  start: auditList.auditLog.offset + 1,
                  end: Math.min(
                    auditList.auditLog.offset + auditList.auditLog.limit,
                    auditList.auditLog.total,
                  ),
                  total: auditList.auditLog.total,
                })
              : m.audit_summary_logs({
                  start: auditList.auditLog.offset + 1,
                  end: Math.min(
                    auditList.auditLog.offset + auditList.auditLog.limit,
                    auditList.auditLog.total,
                  ),
                  total: auditList.auditLog.total,
                })}
          </p>
        {/if}
        <!-- View preference, not a filter: Clear leaves it alone. -->
        <label class="audit-group-checkbox">
          <input
            type="checkbox"
            checked={auditList.auditGroupSessions}
            onchange={() => auditList.toggleAuditGroupSessions()}
          />
          <span>{m.audit_group_by_session()}</span>
        </label>
      </div>
      <AuditLiveStatus />
    </div>

    {#if auditList.loading && auditList.auditLog.entries.length === 0}
      <LoadingState label={m.audit_loading()} />
    {:else if auditList.auditLog.entries.length > 0}
      <div class="audit-log-list">
        <!-- Live rows slide in and neighbours shift down via FLIP; fetched
             pages (not _live) render instantly, and neither plays on the
             initial mount (transitions are local). liveSlide skips building
             slide keyframes — and the getComputedStyle they cost — for the
             fetched rows that would animate for 0ms anyway. -->
        {#each auditList.auditLog.entries as entry (entry.id)}
          <div
            animate:flip={{ duration: motionDuration(150) }}
            in:liveSlide={{ live: entry._live }}
          >
            {#if auditList.auditGroupSessions}
              <AuditThreadGroup {entry} />
            {:else}
              <AuditEntryRow {entry} />
            {/if}
          </div>
        {/each}
      </div>
    {/if}

    {#if auditList.auditLog.entries.length === 0 && !auditList.loading && !auth.needsAuth}
      <div class="empty-state">
        <NoDataIllustration />
      </div>
    {/if}

    <Pagination
      total={auditList.auditLog.total}
      offset={auditList.auditLog.offset}
      limit={auditList.auditLog.limit}
      onprev={() => auditList.auditLogPrevPage()}
      onnext={() => auditList.auditLogNextPage()}
    />
  </div>

</div>

<style>
  /* Row above the list: page summary and view preference on the left, the
     live-stream status on the right. */
  .audit-list-toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    flex-wrap: wrap;
    margin-bottom: 10px;
  }

  .audit-list-toolbar-left {
    display: inline-flex;
    align-items: center;
    gap: 16px;
    flex-wrap: wrap;
  }

  .audit-group-checkbox {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    color: var(--text-muted);
    font-size: 13px;
    cursor: pointer;
    user-select: none;
  }

  .audit-group-checkbox input {
    accent-color: var(--accent);
    cursor: pointer;
  }
/* Audit Log Section */
.audit-retention-note {
  color: var(--text-muted);
  font-size: 13px;
}

.audit-retention-highlight {
  color: var(--text);
  font-weight: 600;
}

.audit-log-section {
  background: var(--bg-surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 24px;
}

.audit-log-summary {
  color: var(--text-muted);
  font-size: 13px;
}

.audit-log-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
</style>
