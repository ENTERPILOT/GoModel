// Live-log stream singleton.
//
// Stream control (startLiveLogs / stopLiveLogs), the stream state
// (liveLogsLastSeq, liveLogsReconnectAttempts, liveLogsReconnectTimer,
// liveLogsController, skippedLiveUsageByRequestId) and every merge helper
// (applyLiveLogEvent, mergeLiveAuditEntry, …) come from the shared mixin in
// ./live-logs-logic.js.
//
// The singleton owns the shared live-preview state (auditLog, usageLog and
// the insert-gate filter fields); the audit/usage pages write their fetched
// pages into `liveLogs.auditLog` / `liveLogs.usageLog` and keep the filter
// fields in sync. Cross-module hooks stay optional and duck-typed: pages
// assign `liveLogs.fetchAuditLog`, `liveLogs.fetchUsage`,
// `liveLogs.isAuditEntryExpanded`, `liveLogs.noteLiveTokenUsage`; the
// conversation drawer registers `liveLogs.refreshLiveConversation` itself.
//
// Transport: the stream is fetch + ReadableStream (NOT EventSource) so the
// Authorization bearer header can be sent; apiFetch preserves that. The
// stream uses SSE framing (data: lines, CRLF frames), a replay cursor
// (&cursor=lastSeq), exponential reconnect backoff (500ms * 2^n, capped at
// 30s, attempt cap 6) and heartbeat handling.

import { untrack } from "svelte";
import { apiFetch, getJSON, isAbortError } from "$lib/api/client.js";
import { auth } from "$lib/stores/auth.svelte.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import { router } from "$lib/stores/router.svelte.js";
import { dateRange } from "$lib/stores/dateRange.svelte.js";
import { liveLogsMethods, liveLogsStreamPath } from "./live-logs-logic.js";

class LiveLogsStore {
  // Shared live-preview state the live merge engine writes into. Pages treat
  // these as the live source of truth.
  auditLog = $state({ entries: [], total: 0, limit: 25, offset: 0 });
  usageLog = $state({ entries: [], total: 0, limit: 50, offset: 0 });

  // Insert-gate filter fields. The owning pages mirror their filter state
  // here so auditLiveInsertAllowed/usageLiveInsertAllowed pause live inserts
  // while filters/pagination are active.
  auditSearch = $state("");
  auditMethod = $state("");
  auditStatusCode = $state("");
  auditStream = $state("");
  usageLogSearch = $state("");
  usageFilterModel = $state("");
  usageFilterProvider = $state("");
  usageFilterLabel = $state("");
  usageFilterUserPath = $state("");
  usageLogHideCached = $state(false);

  // Stream bookkeeping (not read by templates).
  liveLogsLastSeq = 0;
  liveLogsReconnectAttempts = 0;
  liveLogsReconnectTimer = null;
  liveLogsController = null;
  skippedLiveUsageByRequestId = null;

  // Optional cross-module hooks, duck-typed (guarded with typeof).
  fetchUsage = null;
  fetchAuditLog = null;
  isAuditEntryExpanded = null;
  refreshLiveConversation = null;
  noteLiveTokenUsage = null;

  // The mixin reads `this.page` (route id) and the date-picker's custom range.
  get page() {
    return router.page;
  }

  get customStartDate() {
    return dateRange.customStartDate;
  }

  get customEndDate() {
    return dateRange.customEndDate;
  }

  // Only DASHBOARD_LIVE_LOGS_ENABLED gates the stream (default true). Audit
  // logging being off does not stop the stream — live entries are exactly
  // what the dashboard shows when persistence is off.
  liveLogsEnabled() {
    return runtimeConfig.liveLogsVisible();
  }

  async startLiveLogs() {
    if (typeof fetch !== "function" || typeof ReadableStream === "undefined") {
      return;
    }
    // Ensure the runtime-config flags are present before evaluating the gate.
    await runtimeConfig.ensureLoaded();
    if (!this.liveLogsEnabled()) {
      return;
    }
    this.stopLiveLogs();
    this.liveLogsController =
      typeof AbortController === "function" ? new AbortController() : null;
    void this.readLiveLogsStream(this.liveLogsController);
  }

  stopLiveLogs() {
    if (this.liveLogsReconnectTimer) {
      clearTimeout(this.liveLogsReconnectTimer);
      this.liveLogsReconnectTimer = null;
    }
    if (this.liveLogsController && typeof this.liveLogsController.abort === "function") {
      this.liveLogsController.abort();
    }
    this.liveLogsController = null;
  }

  // ensureLiveLogs starts the stream only when it is not already running or
  // scheduled to reconnect — for page $effects that run on every visit
  // (revisits must not restart a running stream).
  ensureLiveLogs() {
    if (this.liveLogsController || this.liveLogsReconnectTimer) {
      return;
    }
    void this.startLiveLogs();
  }

  async readLiveLogsStream(controller) {
    const options = {};
    if (controller) {
      options.signal = controller.signal;
    }
    const url = liveLogsStreamPath(this.liveLogsLastSeq);
    const generation = auth.generation;

    try {
      const res = await apiFetch(url, options);
      if (res.status === 401) {
        // Stale-auth (older key): drop silently; the refreshTick restart
        // reconnects under the new key. Current-key 401 opens the auth
        // dialog and keeps the backoff loop alive.
        auth.handleUnauthorized(generation);
        if (generation < auth.generation) {
          return;
        }
        this.scheduleLiveLogsReconnect();
        return;
      }
      if (!res.ok) {
        console.error(`Failed to fetch live logs: ${res.status} ${res.statusText}`);
        this.scheduleLiveLogsReconnect();
        return;
      }
      if (!res.body || typeof res.body.getReader !== "function") {
        this.scheduleLiveLogsReconnect();
        return;
      }
      this.liveLogsReconnectAttempts = 0;
      await this.consumeLiveLogsBody(res.body.getReader());
      this.scheduleLiveLogsReconnect();
    } catch (e) {
      if (isAbortError(e)) {
        return;
      }
      console.error("Live logs stream failed:", e);
      this.scheduleLiveLogsReconnect();
    }
  }

  scheduleLiveLogsReconnect() {
    if (!this.liveLogsEnabled()) return;
    if (this.liveLogsReconnectTimer) return;
    const attempt = Math.min(this.liveLogsReconnectAttempts + 1, 6);
    this.liveLogsReconnectAttempts = attempt;
    const delay = Math.min(30000, 500 * Math.pow(2, attempt - 1));
    this.liveLogsReconnectTimer = setTimeout(() => {
      this.liveLogsReconnectTimer = null;
      void this.startLiveLogs();
    }, delay);
  }

  async fetchAuditEntryDetail(entry) {
    if (!this.auditEntryShouldFetchDetail(entry)) return;
    const id = String(entry.id || "").trim();
    if (!id) return;
    entry._detail_loading = true;
    let detailEntry = entry;
    try {
      const result = await getJSON(
        "/admin/audit/detail?log_id=" + encodeURIComponent(id),
        { label: "audit detail" },
      );
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        return;
      }
      detailEntry = this.mergeLiveAuditEntry(result.data, "audit.detail") || detailEntry;
    } catch (e) {
      console.error("Failed to fetch audit detail:", e);
    } finally {
      this.clearAuditDetailLoading(detailEntry);
    }
  }
}

Object.assign(LiveLogsStore.prototype, liveLogsMethods());

export const liveLogs = new LiveLogsStore();

// Reconnect the stream whenever the API key changes. The restart is untracked so
// runtime-config reads inside startLiveLogs never become effect dependencies.
let seenRefreshTick = null;
$effect.root(() => {
  $effect(() => {
    const tick = auth.refreshTick;
    if (seenRefreshTick === null) {
      seenRefreshTick = tick;
      return;
    }
    if (tick === seenRefreshTick) {
      return;
    }
    seenRefreshTick = tick;
    untrack(() => {
      liveLogs.stopLiveLogs();
      void liveLogs.startLiveLogs();
    });
  });
});
