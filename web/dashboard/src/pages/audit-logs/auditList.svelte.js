// Audit-log list state: filters, pagination, fetch-token protected loading,
// and the expanded-entry map.
//
// The live-logs singleton owns the shared list + filter state (it merges live
// entries into `liveLogs.auditLog` and gates inserts on the filter fields).
// This store delegates those fields to it and layers the
// fetch/pagination/expansion logic on top, and registers the duck-typed
// cross-module hooks (`fetchAuditLog` for stream resets,
// `isAuditEntryExpanded` for detail refetches of expanded rows).

import { getJSON } from "$lib/api/client.js";
import { dateRange } from "$lib/stores/dateRange.svelte.js";
import { liveLogs } from "./liveLogs.svelte.js";
import { auditWorkflows } from "./audit-workflows.svelte.js";
import {
  auditEntryKey,
  auditLogWithLiveEntries,
  buildAuditLogQuery,
  markExpandedEntry,
  pruneExpandedEntries,
} from "./audit-logic.js";

function emptyAuditLog() {
  return { entries: [], total: 0, limit: 25, offset: 0 };
}

class AuditListStore {
  auditExpandedEntries = $state({});
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

  async fetchAuditLog(resetOffset) {
    const requestToken = ++this.auditFetchToken;
    this.loading = true;
    try {
      if (resetOffset) this.auditLog.offset = 0;
      const qs = buildAuditLogQuery({
        dateQuery: dateRange.queryStr(),
        limit: this.auditLog.limit,
        offset: this.auditLog.offset,
        search: this.auditSearch,
        method: this.auditMethod,
        statusCode: this.auditStatusCode,
        stream: this.auditStream,
      });

      const result = await getJSON("/admin/audit/log?" + qs, {
        label: "audit log",
      });
      if (result.stale) return;
      if (requestToken !== this.auditFetchToken) return;
      if (!result.ok) {
        this.auditLog = emptyAuditLog();
        return;
      }

      const next = auditLogWithLiveEntries(
        result.data,
        this.auditLog && this.auditLog.entries,
        this.liveFilters(),
      );
      if (!Array.isArray(next.entries)) next.entries = [];
      this.auditLog = next;
      this.auditExpandedEntries = pruneExpandedEntries(
        this.auditExpandedEntries,
        next.entries,
      );
      // Resolve workflow versions for the page so expanded entries can render
      // the pipeline chart; a prefetch failure must not clobber the payload.
      try {
        await auditWorkflows.prefetchAuditWorkflows(this.auditLog.entries);
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
