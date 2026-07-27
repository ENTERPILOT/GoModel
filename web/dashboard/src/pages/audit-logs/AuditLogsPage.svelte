<script>
  // Audit Logs page: filterable, paginated audit-log list with expandable
  // entries, live-log merging, and the Interactions drawer trigger.
  import NoDataIllustration from "$lib/components/atoms/NoDataIllustration.svelte";
  import AuthBanner from "$lib/components/organisms/AuthBanner.svelte";
  import { untrack } from "svelte";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { router } from "$lib/stores/router.svelte.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import DatePicker from "$lib/components/molecules/DatePicker.svelte";
  import Pagination from "$lib/components/molecules/Pagination.svelte";
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import AuditFilters from "./AuditFilters.svelte";
  import AuditEntryRow from "./AuditEntryRow.svelte";
  import AuditThreadGroup from "./AuditThreadGroup.svelte";
  import ConversationDrawer from "./ConversationDrawer.svelte";
  import { auditList } from "./auditList.svelte.js";
  import { liveLogs } from "./liveLogs.svelte.js";
  import {
    auditRetentionHighlight,
    auditRetentionPrefix,
    auditRetentionText,
  } from "./audit-logic.js";

  const PAGE = "audit-logs";

  const RETENTION_HELP =
    "If you want to change the retention period, set LOGGING_RETENTION_DAYS (env var) or logging.retention_days (config.yaml) and restart the gateway. Default is 30 days; 0 keeps audit logs forever.";


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
      <h2>Audit Logs</h2>
      {#if auditRetentionText(retentionRaw)}
        <InlineHelpSection
          copyId="audit-retention-help-copy"
          label="retention help"
          text={RETENTION_HELP}
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
      Audit logging is off. Live entries are temporary and disappear after
      refresh. Set <code>LOGGING_ENABLED=true</code> to persist them.
    </div>
  {/if}

  <div class="audit-log-section">
    <AuditFilters />

    {#if auditList.auditLog.total > 0}
      <p class="audit-log-summary">
        Showing {auditList.auditLog.offset + 1}-{Math.min(
          auditList.auditLog.offset + auditList.auditLog.limit,
          auditList.auditLog.total,
        )} of {auditList.auditLog.total}
        {auditList.auditGroupSessions ? "sessions" : "logs"}
      </p>
    {/if}

    {#if auditList.loading && auditList.auditLog.entries.length === 0}
      <div class="audit-log-loading">
        <Spinner size={18} label="Loading audit logs" />
      </div>
    {:else if auditList.auditLog.entries.length > 0}
      <div class="audit-log-list">
        {#each auditList.auditLog.entries as entry (entry.id)}
          {#if auditList.auditGroupSessions}
            <AuditThreadGroup {entry} />
          {:else}
            <AuditEntryRow {entry} />
          {/if}
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

  <ConversationDrawer />
</div>

<style>
  /* Loading affordance while the list fetch is in flight. */
  .audit-log-loading {
    display: flex;
    justify-content: center;
    padding: 2rem 0;
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
  margin-bottom: 12px;
}

.audit-log-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
</style>
