<script>
  // Requests page: filterable, paginated request-log list with expandable
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
  import RequestColumnsMenu from "./RequestColumnsMenu.svelte";
  import RequestFilters from "./RequestFilters.svelte";
  import RequestLiveStatus from "./RequestLiveStatus.svelte";
  import RequestEntryRow from "./RequestEntryRow.svelte";
  import RequestThreadGroup from "./RequestThreadGroup.svelte";
  import { requestList } from "./requestList.svelte.js";
  import { liveLogs } from "./liveLogs.svelte.js";
  import * as m from "$lib/paraglide/messages.js";
  import {
    requestLogRetentionHighlight,
    requestLogRetentionPrefix,
    requestLogRetentionText,
  } from "./request-log-logic.js";

  const PAGE = "requests";

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
        await requestList.fetchRequestLog(true);
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
      <h2>{m.requests_title()}</h2>
      {#if requestLogRetentionText(retentionRaw)}
        <InlineHelpSection
          copyId="request-log-retention-help-copy"
          label={m.requests_retention_help_label()}
          text={m.requests_retention_help()}
        >
          {#snippet title()}
            <p class="request-log-retention-note">
              {requestLogRetentionPrefix(retentionRaw)}<span
                class="request-log-retention-highlight"
                >{requestLogRetentionHighlight(retentionRaw)}</span
              >.
            </p>
          {/snippet}
        </InlineHelpSection>
      {/if}
    </div>
  </div>
  <div class="sticky-date-range">
    <DatePicker onchange={() => requestList.fetchRequestLog(true)} />

  </div>


  <AuthBanner />

  {#if runtimeConfig.loaded && !runtimeConfig.requestLogsVisible() && !auth.needsAuth}
    <div class="alert alert-warning" role="status">
      {m.requests_logging_disabled_before_config()}<code
        >LOGGING_ENABLED=true</code

      >{m.requests_logging_disabled_after_config()}
    </div>

  {/if}

  <div class="request-log-section">
    <RequestFilters />

    <div class="request-log-list-toolbar">
      <div class="request-log-list-toolbar-left">
        {#if requestList.requestLog.total > 0}
          <p class="request-log-summary">
            {requestList.requestLogGroupSessions
              ? m.requests_summary_sessions({
                  start: requestList.requestLog.offset + 1,
                  end: Math.min(
                    requestList.requestLog.offset + requestList.requestLog.limit,
                    requestList.requestLog.total,
                  ),
                  total: requestList.requestLog.total,
                })
              : m.requests_summary_logs({
                  start: requestList.requestLog.offset + 1,
                  end: Math.min(
                    requestList.requestLog.offset + requestList.requestLog.limit,
                    requestList.requestLog.total,
                  ),
                  total: requestList.requestLog.total,
                })}
          </p>
        {/if}
        <!-- View preference, not a filter: Clear leaves it alone. -->
        <label class="request-log-group-checkbox">
          <input
            type="checkbox"
            checked={requestList.requestLogGroupSessions}
            onchange={() => requestList.toggleRequestLogGroupSessions()}
          />
          <span>{m.requests_group_by_session()}</span>

        </label>
        <RequestColumnsMenu />
      </div>
      <RequestLiveStatus />
    </div>

    {#if requestList.loading && requestList.requestLog.entries.length === 0}
      <LoadingState label={m.requests_loading()} />
    {:else if requestList.requestLog.entries.length > 0}
      <div class="request-log-list">
        <!-- Live rows slide in and neighbours shift down via FLIP; fetched
             pages (not _live) render instantly, and neither plays on the
             initial mount (transitions are local). liveSlide skips building
             slide keyframes — and the getComputedStyle they cost — for the
             fetched rows that would animate for 0ms anyway. -->
        {#each requestList.requestLog.entries as entry (entry.id)}
          <div
            animate:flip={{ duration: motionDuration(150) }}
            in:liveSlide={{ live: entry._live }}
          >
            {#if requestList.requestLogGroupSessions}
              <RequestThreadGroup {entry} />
            {:else}

              <RequestEntryRow {entry} />
            {/if}
          </div>
        {/each}
      </div>

    {/if}

    {#if requestList.requestLog.entries.length === 0 && !requestList.loading && !auth.needsAuth}
      <div class="empty-state">
        <NoDataIllustration />
      </div>
    {/if}


    <Pagination

      total={requestList.requestLog.total}
      offset={requestList.requestLog.offset}
      limit={requestList.requestLog.limit}
      onprev={() => requestList.requestLogPrevPage()}
      onnext={() => requestList.requestLogNextPage()}
    />
  </div>

</div>

<style>

  /* Row above the list: page summary and view preference on the left, the
     live-stream status on the right. */
  .request-log-list-toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;

    gap: 12px;
    flex-wrap: wrap;
    margin-bottom: 12px;
  }

  .request-log-list-toolbar-left {
    display: inline-flex;
    align-items: center;
    gap: 16px;
    flex-wrap: wrap;
  }

  .request-log-group-checkbox {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    color: var(--text-muted);
    font-size: 13px;

    cursor: pointer;
    user-select: none;
  }


  .request-log-group-checkbox input {
    accent-color: var(--accent);
    cursor: pointer;
  }
/* Request Log Section */
.request-log-retention-note {
  color: var(--text-muted);
  font-size: 13px;
}


.request-log-retention-highlight {
  color: var(--text);
  font-weight: 600;
}

.request-log-section {
  background: var(--bg-surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-panel);
  padding: var(--space-block);
}

.request-log-summary {
  color: var(--text-muted);
  font-size: 13px;
}

.request-log-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
</style>
