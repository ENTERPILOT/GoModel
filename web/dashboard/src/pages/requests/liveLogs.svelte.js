// Live-log stream singleton.
//
// Stream control (startLiveLogs / stopLiveLogs), the stream state
// (liveLogsLastSeq, liveLogsReconnectAttempts, liveLogsReconnectTimer,
// liveLogsController, skippedLiveUsageByRequestId) and every merge helper
// (applyLiveLogEvent, mergeLiveRequestEntry, …) come from the shared mixin in
// ./live-logs-logic.js.
//
// The singleton owns the shared live-preview state (requestLog, usageLog and
// the request log insert-gate filter fields); the Requests/usage pages write their
// fetched pages into `liveLogs.requestLog` / `liveLogs.usageLog`. The usage
// gate asks the usage page's filter state (usageFilters) whether it is empty. Cross-module hooks stay optional and duck-typed: pages
// assign `liveLogs.fetchRequestLog`, `liveLogs.fetchUsage`,
// `liveLogs.isRequestEntryExpanded`, `liveLogs.noteLiveTokenUsage`; the
// the conversation drawer reacts to normalized request-log record changes.
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
import { access } from "$lib/stores/access.svelte.js";
import { readStored } from "$lib/utils/storage.js";
import { normalizeHiddenTypes } from "./request-operations.js";
import { auth } from "$lib/stores/auth.svelte.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import { router } from "$lib/stores/router.svelte.js";
import { dateRange } from "$lib/stores/dateRange.svelte.js";
import { liveLogsMethods, liveLogsStreamPath } from "./live-logs-logic.js";
import { requestLogRecordKey, mergeRequestLogRecord } from "./request-log-records.js";

const MAX_REQUEST_LOG_RECORDS = 200;
const MAX_REQUEST_LOG_RECORD_CHANGES = 512;

class LiveLogsStore {
  // Shared live-preview state the live merge engine writes into. Pages treat
  // these as the live source of truth.
  requestLog = $state({ entries: [], total: 0, limit: 25, offset: 0 });
  usageLog = $state({ entries: [], total: 0, limit: 50, offset: 0 });

  // Normalized source of truth for request log record content. Lists keep their own
  // ordering/pagination, but every consumer reads record bodies and lifecycle
  // state from here so a slim fetch cannot downgrade a richer live preview.
  requestLogRecords = $state({});
  requestLogRecordChanges = $state([]);
  requestLogRecordVersion = 0;
  requestLogRecordPins = new Set();

  // Insert-gate filter fields. The owning pages mirror their filter state
  // here so requestLogLiveInsertAllowed/usageLiveInsertAllowed pause live inserts
  // while filters/pagination are active.
  requestLogSearch = $state("");
  // Which field the search box targets ("" = all fields). A value pauses
  // live inserts through requestLogSearch alone, whatever the field.
  requestLogSearchField = $state("");
  requestLogMethod = $state("");
  requestLogStatusCode = $state("");
  requestLogStream = $state("");
  // Request types hidden from the list (request-operations.js keys). Unlike
  // the filters above it does not pause live inserts: live rows of a hidden
  // type are dropped instead, so hiding MCP traffic keeps the list live.
  requestLogHiddenTypes = $state(readStoredHiddenTypes());

  // Session grouping view preference (default ON) and the lazily-fetched
  // per-thread children lists ({ [session_id]: {loading, entries, total} }).
  // Both live here so the live merge engine can fold displaced heads into a
  // thread's children.
  requestLogGroupSessions = $state(
    readStored("gomodel_audit_group_sessions", "true") !== "false",
  );
  requestThreadChildren = $state({});
  usageLogSearch = $state("");
  // The usage page registers its filter state here (an object with
  // isEmpty()): live usage previews merge only while no filter is set, since
  // a preview cannot be matched against them client side.
  usageFilters = null;
  usageLogHideCached = $state(false);

  // True while the stream body is being consumed; drives the Requests page's
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
  fetchRequestLog = null;
  isRequestEntryExpanded = null;
  noteLiveTokenUsage = null;

  upsertRequestLogRecord(entry, eventType = "") {
    const key = requestLogRecordKey(entry);
    if (!key) return entry;
    const merged = mergeRequestLogRecord(this.requestLogRecords[key], entry);
    this.requestLogRecords = { ...this.requestLogRecords, [key]: merged };
    this.pruneRequestLogRecords();
    this.requestLogRecordVersion++;
    this.requestLogRecordChanges = [...this.requestLogRecordChanges, {
      version: this.requestLogRecordVersion,
      key,
      eventType,
    }].slice(-MAX_REQUEST_LOG_RECORD_CHANGES);
    return merged;
  }

  upsertRequestLogRecords(entries, eventType = "") {
    return (Array.isArray(entries) ? entries : []).map((entry) =>
      this.upsertRequestLogRecord(entry, eventType));
  }

  requestLogRecord(entryOrID) {
    const key = typeof entryOrID === "string"
      ? String(entryOrID).trim()
      : requestLogRecordKey(entryOrID);
    return key && this.requestLogRecords[key] || null;
  }

  cacheRequestLogRecord(entry, eventType) {
    return this.upsertRequestLogRecord(entry, eventType);
  }

  pinRequestLogRecords(ids) {
    this.requestLogRecordPins = new Set((Array.isArray(ids) ? ids : []).map(String));
  }

  pruneRequestLogRecords() {
    const keys = Object.keys(this.requestLogRecords);
    if (keys.length <= MAX_REQUEST_LOG_RECORDS) return;
    const records = { ...this.requestLogRecords };
    let remove = keys.length - MAX_REQUEST_LOG_RECORDS;
    for (const key of keys) {
      if (remove <= 0) break;
      if (this.requestLogRecordPins.has(key)) continue;
      delete records[key];
      remove--;
    }
    this.requestLogRecords = records;
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
  // inserts flowing (see requestLogLiveInsertAllowed).
  get followsToday() {
    return dateRange.followsToday;
  }

  // DASHBOARD_LIVE_LOGS_ENABLED gates the stream (default true), and so does
  // the credential: the stream is gateway-wide, so a key scoped to a user
  // path gets no live entries. Request logging being off does not stop the
  // stream — live entries are exactly what the dashboard shows when
  // persistence is off.
  liveLogsEnabled() {
    return runtimeConfig.liveLogsVisible() && !access.scoped;
  }

  async startLiveLogs() {
    if (typeof fetch !== "function" || typeof ReadableStream === "undefined") {
      return;
    }
    // Ensure the runtime-config flags and the access scope are present
    // before evaluating the gate.
    await Promise.all([runtimeConfig.ensureLoaded(), access.ensureLoaded()]);
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

  async fetchRequestEntryDetail(entry) {
    if (!this.requestEntryShouldFetchDetail(entry)) return;
    const id = String(entry.id || "").trim();
    if (!id) return;
    entry._detail_loading = true;
    let detailEntry = entry;
    try {
      const result = await getJSON(
        "/admin/request-logs/detail?log_id=" + encodeURIComponent(id),
        { label: "request log detail" },
      );
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        return;
      }
      detailEntry = this.mergeLiveRequestEntry(result.data, "audit.detail") || detailEntry;
    } catch (e) {
      console.error("Failed to fetch request log detail:", e);
    } finally {
      this.clearRequestLogDetailLoading(detailEntry);
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

function readStoredHiddenTypes() {
  try {
    return normalizeHiddenTypes(JSON.parse(readStored("gomodel_audit_hidden_types", "[]")));
  } catch {
    return [];
  }
}
