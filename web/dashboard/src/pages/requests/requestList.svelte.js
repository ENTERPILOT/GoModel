// Request-log list state: filters, pagination, fetch-token protected loading,
// the expanded-entry map, and session-thread grouping.
//
// The live-logs singleton owns the shared list + filter state (it merges live
// entries into `liveLogs.requestLog` and gates inserts on the filter fields).
// This store delegates those fields to it and layers the
// fetch/pagination/expansion logic on top, and registers the duck-typed
// cross-module hooks (`fetchRequestLog` for stream resets,
// `isRequestEntryExpanded` for detail refetches of expanded rows).
//
// Grouped mode ("Group by session", on by default) fetches thread heads from
// /admin/request-logs/sessions into the same list shape (entries carry
// `session_count`); a thread's older entries are fetched lazily into
// `liveLogs.requestThreadChildren` when the user unfolds it.

import { getJSON } from "$lib/api/client.js";
import { readStoredRenamed, writeStored } from "$lib/utils/storage.js";
import { dateRange } from "$lib/stores/dateRange.svelte.js";
import { liveLogs } from "./liveLogs.svelte.js";
import { normalizeRequestColumns, parseStoredRequestColumns } from "./request-columns.js";
import { normalizeHiddenTypes } from "./request-operations.js";
import { requestWorkflows } from "./request-workflows.svelte.js";
import {
  requestEntryKey,
  groupedRequestLogWithLiveEntries,
  requestLogFromSessions,
  requestLogWithLiveEntries,
  requestSessionId,
  buildRequestLogQuery,
  buildRequestLogSessionQuery,
  mergeRequestThreadChildren,
  toggleExpandedEntry,
  pruneExpandedEntries,
  pruneThreadMap,
  toggleExpandedThread,
} from "./request-log-logic.js";

// A session_id page is capped server-side at 100 entries; bigger threads show
// a "latest 100 of N" note.
const THREAD_CHILDREN_LIMIT = 100;

function emptyRequestLog() {
  return { entries: [], total: 0, limit: 25, offset: 0 };
}

class RequestListStore {
  // Column visibility view preference ({ [column key]: boolean }); persisted
  // like the session-grouping toggle.
  requestColumns = $state(
    parseStoredRequestColumns(readStoredRenamed("gomodel_requests_columns", "gomodel_audit_columns", "{}")),
  );
  requestLogExpandedEntries = $state({});
  requestLogExpandedThreads = $state({});
  loading = $state(false);
  // Fetch-token race protection: a stale response must never clobber the
  // results of a newer request.
  requestLogFetchToken = 0;

  // Shared list + filter state lives on the liveLogs singleton.
  get requestLog() {
    return liveLogs.requestLog;
  }
  set requestLog(value) {
    liveLogs.requestLog = value;
  }
  get requestLogSearch() {
    return liveLogs.requestLogSearch;
  }
  set requestLogSearch(value) {
    liveLogs.requestLogSearch = value;
  }
  get requestLogSearchField() {
    return liveLogs.requestLogSearchField;
  }
  set requestLogSearchField(value) {
    liveLogs.requestLogSearchField = value;
  }
  get requestLogMethod() {
    return liveLogs.requestLogMethod;
  }
  set requestLogMethod(value) {
    liveLogs.requestLogMethod = value;
  }
  get requestLogStatusCode() {
    return liveLogs.requestLogStatusCode;
  }
  set requestLogStatusCode(value) {
    liveLogs.requestLogStatusCode = value;
  }
  get requestLogStream() {
    return liveLogs.requestLogStream;
  }
  set requestLogStream(value) {
    liveLogs.requestLogStream = value;
  }
  get requestLogHiddenTypes() {
    return liveLogs.requestLogHiddenTypes;
  }
  set requestLogHiddenTypes(value) {
    liveLogs.requestLogHiddenTypes = normalizeHiddenTypes(value);
    writeStored("gomodel_requests_hidden_types", JSON.stringify(liveLogs.requestLogHiddenTypes));
  }
  get requestLogGroupSessions() {
    return liveLogs.requestLogGroupSessions;
  }

  // liveFilters snapshots the consolidated filters + custom date range in the
  // shape the pure live-merge helpers expect.
  liveFilters() {
    return {
      search: this.requestLogSearch,
      method: this.requestLogMethod,
      statusCode: this.requestLogStatusCode,
      stream: this.requestLogStream,
      hiddenTypes: this.requestLogHiddenTypes,
      customStartDate: dateRange.customStartDate,
      customEndDate: dateRange.customEndDate,
    };
  }

  // toggleRequestLogGroupSessions flips the persisted view preference. It is a view
  // preference, not a filter: clearRequestFilters leaves it alone.
  // toggleRequestType shows or hides one request type and refetches. Loaded
  // thread children were fetched under the old filter, so they are dropped.
  toggleRequestType(key) {
    const hidden = this.requestLogHiddenTypes;
    this.requestLogHiddenTypes = hidden.includes(key)
      ? hidden.filter((item) => item !== key)
      : [...hidden, key];
    this.requestLogExpandedThreads = {};
    liveLogs.requestThreadChildren = {};
    this.fetchRequestLog(true);
  }

  toggleRequestLogGroupSessions() {
    liveLogs.requestLogGroupSessions = !liveLogs.requestLogGroupSessions;
    writeStored("gomodel_requests_group_sessions", liveLogs.requestLogGroupSessions);
    this.requestLogExpandedThreads = {};
    liveLogs.requestThreadChildren = {};
    this.fetchRequestLog(true);
  }

  // toggleRequestColumn shows or hides one column of the row. Purely visual, so
  // there is nothing to refetch.
  toggleRequestColumn(key) {
    this.requestColumns = normalizeRequestColumns({
      ...this.requestColumns,
      [key]: !this.requestColumns[key],
    });
    writeStored("gomodel_requests_columns", JSON.stringify(this.requestColumns));
  }

  async fetchRequestLog(resetOffset) {
    const requestToken = ++this.requestLogFetchToken;
    this.loading = true;
    try {
      if (resetOffset) this.requestLog.offset = 0;
      const grouped = this.requestLogGroupSessions;
      const qs = buildRequestLogQuery({
        dateQuery: dateRange.queryStr(),
        limit: this.requestLog.limit,
        offset: this.requestLog.offset,
        search: this.requestLogSearch,
        searchField: this.requestLogSearchField,
        method: this.requestLogMethod,
        statusCode: this.requestLogStatusCode,
        stream: this.requestLogStream,
        hiddenTypes: this.requestLogHiddenTypes,
      });

      const path = grouped ? "/admin/request-logs/sessions?" : "/admin/request-logs?";
      const result = await getJSON(path + qs, {
        label: "request log",
      });
      if (result.stale) return;
      if (requestToken !== this.requestLogFetchToken) return;
      if (!result.ok) {
        this.requestLog = emptyRequestLog();
        return;
      }

      const payload = grouped ? requestLogFromSessions(result.data) : result.data;
      const merge = grouped
        ? groupedRequestLogWithLiveEntries
        : requestLogWithLiveEntries;
      const next = merge(
        payload,
        this.requestLog && this.requestLog.entries,
        this.liveFilters(),
      );
      if (!Array.isArray(next.entries)) next.entries = [];
      next.entries = liveLogs.upsertRequestLogRecords(next.entries, "request_log.list");
      this.requestLog = next;
      this.requestLogExpandedThreads = pruneThreadMap(
        this.requestLogExpandedThreads,
        next.entries,
      );
      liveLogs.requestThreadChildren = pruneThreadMap(
        liveLogs.requestThreadChildren,
        next.entries,
      );
      // Expanded child rows must survive a heads refetch, so prune against
      // heads plus every loaded child.
      const visible = [...next.entries, ...this.loadedThreadChildren()];
      this.requestLogExpandedEntries = pruneExpandedEntries(
        this.requestLogExpandedEntries,
        visible,
      );
      // Resolve workflow versions for the page so expanded entries can render
      // the pipeline chart; a prefetch failure must not clobber the payload.
      try {
        await requestWorkflows.prefetchRequestWorkflows(visible);
      } catch (e) {
        console.error("Failed to prefetch request workflows:", e);
      }
    } catch (e) {
      console.error("Failed to fetch request log:", e);
      if (requestToken !== this.requestLogFetchToken) return;
      this.requestLog = emptyRequestLog();
    } finally {
      if (requestToken === this.requestLogFetchToken) this.loading = false;
    }
  }

  loadedThreadChildren() {
    const children = liveLogs.requestThreadChildren || {};
    return Object.keys(children).flatMap((key) =>
      Array.isArray(children[key] && children[key].entries)
        ? children[key].entries
        : [],
    );
  }

  isThreadExpanded(sessionId) {
    return !!(sessionId && this.requestLogExpandedThreads[sessionId]);
  }

  threadChildren(sessionId) {
    return (sessionId && liveLogs.requestThreadChildren[sessionId]) || null;
  }

  async toggleThread(entry) {
    const sessionId = requestSessionId(entry);
    if (!sessionId) return;
    const expandedNow = !this.isThreadExpanded(sessionId);
    this.requestLogExpandedThreads = toggleExpandedThread(
      this.requestLogExpandedThreads,
      sessionId,
    );
    // A partial list (loaded: false) holds only live-displaced entries; the
    // full session page still needs the fetch.
    const list = liveLogs.requestThreadChildren[sessionId];
    if (expandedNow && !(list && (list.loaded || list.loading))) {
      await this.fetchThreadEntries(entry);
    }
  }

  async fetchThreadEntries(head) {
    const sessionId = requestSessionId(head);
    if (!sessionId) return;
    const previous = liveLogs.requestThreadChildren[sessionId];
    liveLogs.requestThreadChildren = {
      ...liveLogs.requestThreadChildren,
      [sessionId]: {
        loading: true,
        loaded: false,
        entries:
          previous && Array.isArray(previous.entries) ? previous.entries : [],
        total: Number((previous && previous.total) || 0),
      },
    };
    // On a failed/stale fetch, keep any live-displaced entries but drop the
    // loading placeholder so the next expand retries (leaving it would render
    // a spinner forever). With nothing to show, also collapse the thread —
    // left expanded, the next click would read as a collapse and push the
    // retry two clicks away.
    const restore = () => {
      const lists = { ...liveLogs.requestThreadChildren };
      const current = lists[sessionId];
      const entries =
        current && Array.isArray(current.entries) ? current.entries : [];
      if (entries.length > 0) {
        lists[sessionId] = {
          loading: false,
          loaded: false,
          entries,
          total: entries.length,
        };
      } else {
        delete lists[sessionId];
        if (this.requestLogExpandedThreads[sessionId]) {
          this.requestLogExpandedThreads = toggleExpandedThread(
            this.requestLogExpandedThreads,
            sessionId,
          );
        }
      }
      liveLogs.requestThreadChildren = lists;
    };
    try {
      const qs = buildRequestLogSessionQuery({
        sessionId,
        limit: THREAD_CHILDREN_LIMIT,
        hiddenTypes: this.requestLogHiddenTypes,
      });
      const result = await getJSON("/admin/request-logs?" + qs, {
        label: "request log session",
      });
      if (result.stale) {
        restore();
        return;
      }
      if (!result.ok) throw new Error("request log session fetch failed");
      // Re-read the slot and the on-screen head: live events during the fetch
      // may have displaced more rows into it or replaced the thread head.
      // Only the CURRENT head is excluded from the children — when the head
      // changed mid-flight, the original head is now a demoted child that the
      // fetched page must keep contributing.
      const current = liveLogs.requestThreadChildren[sessionId];
      const currentHead = this.requestLog.entries.find(
        (entry) => requestSessionId(entry) === sessionId,
      );
      const merged = mergeRequestThreadChildren(
        current,
        result.data.entries,
        currentHead ? [currentHead] : [head],
      );
      merged.entries = liveLogs.upsertRequestLogRecords(
        merged.entries,
        "request_log.thread",
      );
      liveLogs.requestThreadChildren = {
        ...liveLogs.requestThreadChildren,
        [sessionId]: {
          loading: false,
          loaded: true,
          entries: merged.entries,
          total: Number(result.data.total || 0) + merged.preservedCount,
          // When the server read the children, so a later live event from
          // another replica can tell whether total already counts it.
          as_of: result.data.as_of,
        },
      };
    } catch (e) {
      console.error("Failed to fetch request log session entries:", e);
      restore();
    }
  }

  clearRequestFilters() {
    this.requestLogSearch = "";
    this.requestLogSearchField = "";
    this.requestLogMethod = "";
    this.requestLogStatusCode = "";
    this.requestLogStream = "";
    if (this.requestLogHiddenTypes.length > 0) {
      this.requestLogHiddenTypes = [];
      this.requestLogExpandedThreads = {};
      liveLogs.requestThreadChildren = {};
    }
    this.fetchRequestLog(true);
  }

  requestLogNextPage() {
    if (this.requestLog.offset + this.requestLog.limit < this.requestLog.total) {
      this.requestLog.offset += this.requestLog.limit;
      this.fetchRequestLog(false);
    }
  }

  requestLogPrevPage() {
    if (this.requestLog.offset > 0) {
      this.requestLog.offset = Math.max(
        0,
        this.requestLog.offset - this.requestLog.limit,
      );
      this.fetchRequestLog(false);
    }
  }

  isRequestEntryExpanded(entry) {
    const key = requestEntryKey(entry);
    if (!key) return false;
    return !!(this.requestLogExpandedEntries && this.requestLogExpandedEntries[key]);
  }

  // toggleRequestEntryExpanded flips a row's expanded state (the expansion is
  // Svelte-controlled, not the native <details> toggle, so the open and close
  // can animate in every browser). Opening also fetches the persisted detail
  // — list entries are slim.
  toggleRequestEntryExpanded(entry) {
    this.requestLogExpandedEntries = toggleExpandedEntry(
      this.requestLogExpandedEntries,
      entry,
    );
    const expanded = this.isRequestEntryExpanded(entry);
    if (expanded && typeof liveLogs.fetchRequestEntryDetail === "function") {
      void liveLogs.fetchRequestEntryDetail(entry);
    }
    return expanded;
  }

  expandRequestEntry(entry) {
    if (!this.isRequestEntryExpanded(entry)) this.toggleRequestEntryExpanded(entry);
  }
}

export const requestList = new RequestListStore();

// Duck-typed cross-module hooks for the live-logs mixin: stream resets
// refetch the current page, and settled live events refetch detail for rows
// the user has expanded.
liveLogs.fetchRequestLog = (resetOffset) => requestList.fetchRequestLog(resetOffset);
liveLogs.isRequestEntryExpanded = (entry) => requestList.isRequestEntryExpanded(entry);
