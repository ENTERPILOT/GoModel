<script>
  // Audit Logs page: filterable, paginated audit-log list with expandable
  // entries, live-log merging, and the Interactions drawer trigger.
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
        )} of {auditList.auditLog.total} logs
      </p>
    {/if}

    {#if auditList.loading && auditList.auditLog.entries.length === 0}
      <div class="audit-log-loading">
        <Spinner size={18} label="Loading audit logs" />
      </div>
    {:else if auditList.auditLog.entries.length > 0}
      <div class="audit-log-list">
        {#each auditList.auditLog.entries as entry (entry.id)}
          <AuditEntryRow {entry} />
        {/each}
      </div>
    {/if}

    {#if auditList.auditLog.entries.length === 0 && !auditList.loading && !auth.needsAuth}
      <div class="empty-state">
        <svg
          class="empty-state-icon"
          viewBox="0 0 220 262"
          fill="none"
          role="img"
          aria-label="No data"
          xmlns="http://www.w3.org/2000/svg"
        >
          <path
            d="M110 20 L187.9 65 L187.9 155 L110 200 L32.1 155 L32.1 65 Z"
            stroke="var(--accent)"
            stroke-width="5"
            stroke-linejoin="miter"
            opacity="0.5"
          ></path>
          <path
            d="M60 123 H160"
            stroke="currentColor"
            stroke-width="2"
            stroke-dasharray="1.5 7"
            stroke-linecap="round"
            opacity="0.18"
          ></path>
          <path
            d="M60 93 H160"
            stroke="currentColor"
            stroke-width="2"
            stroke-dasharray="1.5 7"
            stroke-linecap="round"
            opacity="0.18"
          ></path>
          <path
            d="M50 155 H170"
            stroke="currentColor"
            stroke-width="5"
            stroke-linecap="round"
            opacity="0.6"
          ></path>
          <rect
            x="57"
            y="113"
            width="14"
            height="36"
            rx="4"
            stroke="currentColor"
            stroke-width="2.8"
            stroke-dasharray="5 4"
            opacity="0.55"
          ></rect>
          <rect
            x="80"
            y="91"
            width="14"
            height="58"
            rx="4"
            stroke="currentColor"
            stroke-width="2.8"
            stroke-dasharray="5 4"
            opacity="0.55"
          ></rect>
          <rect
            x="103"
            y="75"
            width="14"
            height="74"
            rx="4"
            stroke="currentColor"
            stroke-width="2.8"
            stroke-dasharray="5 4"
            opacity="0.55"
          ></rect>
          <rect
            x="126"
            y="97"
            width="14"
            height="52"
            rx="4"
            stroke="currentColor"
            stroke-width="2.8"
            stroke-dasharray="5 4"
            opacity="0.55"
          ></rect>
          <rect
            x="149"
            y="119"
            width="14"
            height="30"
            rx="4"
            stroke="currentColor"
            stroke-width="2.8"
            stroke-dasharray="5 4"
            opacity="0.55"
          ></rect>
          <text
            x="110"
            y="250"
            text-anchor="middle"
            fill="currentColor"
            font-size="40">No data</text
          >
        </svg>
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
</style>
