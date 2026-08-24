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
// the conversation drawer reacts to normalized audit-record changes.
//
// Transport: the stream is fetch + ReadableStream (NOT EventSource) so the
// Authorization bearer header can be sent; apiFetch preserves that. The
// stream uses SSE framing (data: lines, CRLF frames), a replay cursor
// (&cursor=lastSeq), exponential reconnect backoff (500ms * 2^n, attempt
// counter capped at 6 so the delay tops out at 16s; reconnects continue
// indefinitely) and heartbeat handling. Framing and backoff are shared with
// the overview's usage-signal stream via $lib/api/eventStream.js.

import { untrack } from "svelte";
import { apiFetch, getJSON, isAbortError } from "$lib/api/client.js";
import { nextReconnect } from "$lib/api/eventStream.js";
import { readStored } from "$lib/utils/storage.js";
import { auth } from "$lib/stores/auth.svelte.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import { router } from "$lib/stores/router.svelte.js";
import { dateRange } from "$lib/stores/dateRange.svelte.js";
import { liveLogsMethods, liveLogsStreamPath } from "./live-logs-logic.js";
import { auditRecordKey, mergeAuditRecord } from "./audit-records.js";

const MAX_AUDIT_RECORDS = 200;
const MAX_AUDIT_RECORD_CHANGES = 512;

class LiveLogsStore {
  // Shared live-preview state the live merge engine writes into. Pages treat
  // these as the live source of truth.
  auditLog = $state({ entries: [], total: 0, limit: 25, offset: 0 });
  usageLog = $state({ entries: [], total: 0, limit: 50, offset: 0 });

  // Normalized source of truth for audit record content. Lists keep their own
  // ordering/pagination, but every consumer reads record bodies and lifecycle
  // state from here so a slim fetch cannot downgrade a richer live preview.
  auditRecords = $state({});
  auditRecordChanges = $state([]);
  auditRecordVersion = 0;
  auditRecordPins = new Set();

  // Insert-gate filter fields. The owning pages mirror their filter state
  // here so auditLiveInsertAllowed/usageLiveInsertAllowed pause live inserts
  // while filters/pagination are active.
  auditSearch = $state("");
  auditMethod = $state("");
  auditStatusCode = $state("");
  auditStream = $state("");
  auditField = $state("search");
  auditFieldValue = $state("");

  // Session grouping view preference (default ON) and the lazily-fetched
  // per-thread children lists ({ [session_id]: {loading, entries, total} }).
  // Both live here so the live merge engine can fold displaced heads into a
  // thread's children.
  auditGroupSessions = $state(
    readStored("gomodel_audit_group_sessions", "true") !== "false",
  );
  auditThreadChildren = $state({});
  usageLogSearch = $state("");
  usageFilterModel = $state("");
  usageFilterProvider = $state("");
  usageFilterLabel = $state("");
  usageFilterUserPath = $state("");
  usageFilterSession = $state("");
  usageLogHideCached = $state(false);

  // True while the stream body is being consumed; drives the audit page's
  // live-status indicator.
  liveLogsStreaming = $state(false);

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
  noteLiveTokenUsage = null;

  upsertAuditRecord(entry, eventType = "") {
    const key = auditRecordKey(entry);
    if (!key) return entry;
    const merged = mergeAuditRecord(this.auditRecords[key], entry);
    this.auditRecords = { ...this.auditRecords, [key]: merged };
    this.pruneAuditRecords();
    this.auditRecordVersion++;
    this.auditRecordChanges = [...this.auditRecordChanges, {
      version: this.auditRecordVersion,
      key,
      eventType,
    }].slice(-MAX_AUDIT_RECORD_CHANGES);
    return merged;
  }

  upsertAuditRecords(entries, eventType = "") {
    return (Array.isArray(entries) ? entries : []).map((entry) =>
      this.upsertAuditRecord(entry, eventType));
  }

  auditRecord(entryOrID) {
    const key = typeof entryOrID === "string"
      ? String(entryOrID).trim()
      : auditRecordKey(entryOrID);
    return key && this.auditRecords[key] || null;
  }

  cacheAuditRecord(entry, eventType) {
    return this.upsertAuditRecord(entry, eventType);
  }

  pinAuditRecords(ids) {
    this.auditRecordPins = new Set((Array.isArray(ids) ? ids : []).map(String));
  }

  pruneAuditRecords() {
    const keys = Object.keys(this.auditRecords);
    if (keys.length <= MAX_AUDIT_RECORDS) return;
    const records = { ...this.auditRecords };
    let remove = keys.length - MAX_AUDIT_RECORDS;
    for (const key of keys) {
      if (remove <= 0) break;
      if (this.auditRecordPins.has(key)) continue;
      delete records[key];
      remove--;
    }
    this.auditRecords = records;
  }

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

  // True while the custom window ends on today; such a window keeps live
  // inserts flowing (see auditLiveInsertAllowed).
  get followsToday() {
    return dateRange.followsToday;
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
    this.liveLogsStreaming = false;
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
      this.liveLogsStreaming = true;
      await this.consumeLiveLogsBody(res.body.getReader());
      // A reader that ends normally after the stream was deliberately
      // stopped (page teardown, pagehide, restart) must not resurrect it —
      // reconnecting here would open a stream nothing owns.
      if (controller && (controller.signal.aborted || this.liveLogsController !== controller)) {
        return;
      }
      this.scheduleLiveLogsReconnect();
    } catch (e) {
      // Firefox rejects deliberately-aborted stream reads with plain
      // TypeErrors ("Error in input stream", "NetworkError when attempting
      // to fetch resource") instead of an AbortError, so trust our own
      // controller state over the error's name.
      if (isAbortError(e) || (controller && controller.signal && controller.signal.aborted)) {
        return;
      }
      console.error("Live logs stream failed:", e);
      this.scheduleLiveLogsReconnect();
    }
  }

  scheduleLiveLogsReconnect() {
    this.liveLogsStreaming = false;
    if (!this.liveLogsEnabled()) return;
    if (this.liveLogsReconnectTimer) return;
    const { attempt, delay } = nextReconnect(this.liveLogsReconnectAttempts);
    this.liveLogsReconnectAttempts = attempt;
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

// A page refresh or navigation kills the in-flight stream at the network
// layer; Firefox surfaces that as a TypeError, which used to log one
// "stream failed" console error per refresh. Abort the stream ourselves
// before the page goes away — teardown becomes a real abort the reader
// suppresses — and bring it back after a back/forward-cache restore.
if (typeof window !== "undefined") {
  let streamingBeforePagehide = false;
  window.addEventListener("pagehide", () => {
    streamingBeforePagehide = Boolean(liveLogs.liveLogsController);
    liveLogs.stopLiveLogs();
  });
  window.addEventListener("pageshow", (event) => {
    if (event.persisted && streamingBeforePagehide) {
      liveLogs.ensureLiveLogs();
    }
  });
}

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
