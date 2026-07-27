// Audit-log list state: filters, pagination, fetch-token protected loading,
// the expanded-entry map, and session-thread grouping.
//
// The live-logs singleton owns the shared list + filter state (it merges live
// entries into `liveLogs.auditLog` and gates inserts on the filter fields).
// This store delegates those fields to it and layers the
// fetch/pagination/expansion logic on top, and registers the duck-typed
// cross-module hooks (`fetchAuditLog` for stream resets,
// `isAuditEntryExpanded` for detail refetches of expanded rows).
//
// Grouped mode ("Group by session", on by default) fetches thread heads from
// /admin/audit/sessions into the same list shape (entries carry
// `session_count`); a thread's older entries are fetched lazily into
// `liveLogs.auditThreadChildren` when the user unfolds it.

import { getJSON } from "$lib/api/client.js";
import { writeStored } from "$lib/utils/storage.js";
import { dateRange } from "$lib/stores/dateRange.svelte.js";
import { liveLogs } from "./liveLogs.svelte.js";
import { auditWorkflows } from "./audit-workflows.svelte.js";
import {
  auditEntryKey,
  auditGroupedLogWithLiveEntries,
  auditLogFromSessions,
  auditLogWithLiveEntries,
  auditSessionId,
  auditThreadChildEntries,
  buildAuditLogQuery,
  buildAuditSessionQuery,
  markExpandedEntry,
  pruneExpandedEntries,
  pruneThreadMap,
  toggleExpandedThread,
} from "./audit-logic.js";

// A session_id page is capped server-side at 100 entries; bigger threads show
// a "latest 100 of N" note.
const THREAD_CHILDREN_LIMIT = 100;

function emptyAuditLog() {
  return { entries: [], total: 0, limit: 25, offset: 0 };
}

class AuditListStore {
  auditExpandedEntries = $state({});
  auditExpandedThreads = $state({});
  loading = $state(false);
  // Fetch-token race protection: a stale response must never clobber the
  // results of a newer request.
  auditFetchToken = 0;

  // Shared list + filter state lives on the liveLogs singleton.
  get auditLog() {
    return liveLogs.auditLog;
  }
  set auditLog(value) {
    liveLogs.auditLog = value;
  }
  get auditSearch() {
    return liveLogs.auditSearch;
  }
  set auditSearch(value) {
    liveLogs.auditSearch = value;
  }
  get auditMethod() {
    return liveLogs.auditMethod;
  }
  set auditMethod(value) {
    liveLogs.auditMethod = value;
  }
  get auditStatusCode() {
    return liveLogs.auditStatusCode;
  }
  set auditStatusCode(value) {
    liveLogs.auditStatusCode = value;
  }
  get auditStream() {
    return liveLogs.auditStream;
  }
  set auditStream(value) {
    liveLogs.auditStream = value;
  }
  get auditGroupSessions() {
    return liveLogs.auditGroupSessions;
  }

  // liveFilters snapshots the consolidated filters + custom date range in the
  // shape the pure live-merge helpers expect.
  liveFilters() {
    return {
      search: this.auditSearch,
      method: this.auditMethod,
      statusCode: this.auditStatusCode,
      stream: this.auditStream,
      customStartDate: dateRange.customStartDate,
      customEndDate: dateRange.customEndDate,
    };
  }

  // toggleAuditGroupSessions flips the persisted view preference. It is a view
  // preference, not a filter: clearAuditFilters leaves it alone.
  toggleAuditGroupSessions() {
    liveLogs.auditGroupSessions = !liveLogs.auditGroupSessions;
    writeStored("gomodel_audit_group_sessions", liveLogs.auditGroupSessions);
    this.auditExpandedThreads = {};
    liveLogs.auditThreadChildren = {};
    this.fetchAuditLog(true);
  }

  async fetchAuditLog(resetOffset) {
    const requestToken = ++this.auditFetchToken;
    this.loading = true;
    try {
      if (resetOffset) this.auditLog.offset = 0;
      const grouped = this.auditGroupSessions;
      const qs = buildAuditLogQuery({
        dateQuery: dateRange.queryStr(),
        limit: this.auditLog.limit,
        offset: this.auditLog.offset,
        search: this.auditSearch,
        method: this.auditMethod,
        statusCode: this.auditStatusCode,
        stream: this.auditStream,
      });

      const path = grouped ? "/admin/audit/sessions?" : "/admin/audit/log?";
      const result = await getJSON(path + qs, {
        label: "audit log",
      });
      if (result.stale) return;
      if (requestToken !== this.auditFetchToken) return;
      if (!result.ok) {
        this.auditLog = emptyAuditLog();
        return;
      }

      const payload = grouped ? auditLogFromSessions(result.data) : result.data;
      const merge = grouped
        ? auditGroupedLogWithLiveEntries
        : auditLogWithLiveEntries;
      const next = merge(
        payload,
        this.auditLog && this.auditLog.entries,
        this.liveFilters(),
      );
      if (!Array.isArray(next.entries)) next.entries = [];
      this.auditLog = next;
      this.auditExpandedThreads = pruneThreadMap(
        this.auditExpandedThreads,
        next.entries,
      );
      liveLogs.auditThreadChildren = pruneThreadMap(
        liveLogs.auditThreadChildren,
        next.entries,
      );
      // Expanded child rows must survive a heads refetch, so prune against
      // heads plus every loaded child.
      this.auditExpandedEntries = pruneExpandedEntries(
        this.auditExpandedEntries,
        [...next.entries, ...this.loadedThreadChildren()],
      );
      // Resolve workflow versions for the page so expanded entries can render
      // the pipeline chart; a prefetch failure must not clobber the payload.
      try {
        await auditWorkflows.prefetchAuditWorkflows([
          ...this.auditLog.entries,
          ...this.loadedThreadChildren(),
        ]);
      } catch (e) {
        console.error("Failed to prefetch audit workflows:", e);
      }
    } catch (e) {
      console.error("Failed to fetch audit log:", e);
      if (requestToken !== this.auditFetchToken) return;
      this.auditLog = emptyAuditLog();
    } finally {
      if (requestToken === this.auditFetchToken) this.loading = false;
    }
  }

  loadedThreadChildren() {
    const children = liveLogs.auditThreadChildren || {};
    return Object.keys(children).flatMap((key) =>
      Array.isArray(children[key] && children[key].entries)
        ? children[key].entries
        : [],
    );
  }

  isThreadExpanded(sessionId) {
    return !!(sessionId && this.auditExpandedThreads[sessionId]);
  }

  threadChildren(sessionId) {
    return (sessionId && liveLogs.auditThreadChildren[sessionId]) || null;
  }

  async toggleThread(entry) {
    const sessionId = auditSessionId(entry);
    if (!sessionId) return;
    const expandedNow = !this.isThreadExpanded(sessionId);
    this.auditExpandedThreads = toggleExpandedThread(
      this.auditExpandedThreads,
      sessionId,
    );
    if (expandedNow && !liveLogs.auditThreadChildren[sessionId]) {
      await this.fetchThreadEntries(entry);
    }
  }

  async fetchThreadEntries(head) {
    const sessionId = auditSessionId(head);
    if (!sessionId) return;
    liveLogs.auditThreadChildren = {
      ...liveLogs.auditThreadChildren,
      [sessionId]: { loading: true, entries: [], total: 0 },
    };
    try {
      const qs = buildAuditSessionQuery({
        sessionId,
        limit: THREAD_CHILDREN_LIMIT,
      });
      const result = await getJSON("/admin/audit/log?" + qs, {
        label: "audit session",
      });
      if (result.stale) {
        // Silently drop the loading placeholder so the next toggle retries
        // (leaving it would render a spinner forever).
        const next = { ...liveLogs.auditThreadChildren };
        delete next[sessionId];
        liveLogs.auditThreadChildren = next;
        return;
      }
      if (!result.ok) throw new Error("audit session fetch failed");
      liveLogs.auditThreadChildren = {
        ...liveLogs.auditThreadChildren,
        [sessionId]: {
          loading: false,
          entries: auditThreadChildEntries(result.data.entries, head),
          total: Number(result.data.total || 0),
        },
      };
    } catch (e) {
      console.error("Failed to fetch audit session entries:", e);
      // Drop the placeholder so the next toggle retries the fetch.
      const next = { ...liveLogs.auditThreadChildren };
      delete next[sessionId];
      liveLogs.auditThreadChildren = next;
    }
  }

  clearAuditFilters() {
    this.auditSearch = "";
    this.auditMethod = "";
    this.auditStatusCode = "";
    this.auditStream = "";
    this.fetchAuditLog(true);
  }

  auditLogNextPage() {
    if (this.auditLog.offset + this.auditLog.limit < this.auditLog.total) {
      this.auditLog.offset += this.auditLog.limit;
      this.fetchAuditLog(false);
    }
  }

  auditLogPrevPage() {
    if (this.auditLog.offset > 0) {
      this.auditLog.offset = Math.max(
        0,
        this.auditLog.offset - this.auditLog.limit,
      );
      this.fetchAuditLog(false);
    }
  }

  isAuditEntryExpanded(entry) {
    const key = auditEntryKey(entry);
    if (!key) return false;
    return !!(this.auditExpandedEntries && this.auditExpandedEntries[key]);
  }

  markAuditEntryExpanded(entry) {
    this.auditExpandedEntries = markExpandedEntry(
      this.auditExpandedEntries,
      entry,
    );
  }
}

export const auditList = new AuditListStore();

// Duck-typed cross-module hooks for the live-logs mixin: stream resets
// refetch the current page, and settled live events refetch detail for rows
// the user has expanded.
liveLogs.fetchAuditLog = (resetOffset) => auditList.fetchAuditLog(resetOffset);
liveLogs.isAuditEntryExpanded = (entry) => auditList.isAuditEntryExpanded(entry);
