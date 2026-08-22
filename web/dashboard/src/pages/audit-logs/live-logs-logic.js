// Pure live-log stream parsing and merge logic.
//
// The functions are written as a `this`-based method mixin so the Svelte
// singleton (liveLogs.svelte.js) and the node:test suite share the identical
// implementation. The host (`this`) must provide:
//   - state: auditLog {entries,total,limit,offset}, usageLog {…},
//     skippedLiveUsageByRequestId, liveLogsLastSeq, auditGroupSessions,
//     auditThreadChildren ({ [session_id]: {loading, entries, total} })
//   - insert-gate fields: auditSearch, auditMethod, auditStatusCode,
//     auditStream, customStartDate, customEndDate, usageLogSearch,
//     usageFilterModel, usageFilterProvider, usageFilterLabel,
//     usageFilterUserPath, usageFilterSession, usageLogHideCached, page
//   - optional cross-module hooks (guarded with typeof):
//     fetchUsage, fetchAuditLog, isAuditEntryExpanded, cacheAuditRecord,
//     noteLiveTokenUsage, fetchAuditEntryDetail
//
// No Svelte runes here, and the single import is by relative path: node --test
// runs this file directly and cannot resolve the `$lib` alias.

import { consumeEventStream } from "../../lib/api/eventStream.js";
import * as m from "../../lib/paraglide/messages.js";

const LIVE_LOGS_STREAM_PATH = "/admin/live/logs?types=audit,usage";

function matchesLiveAuditKey(entry, id, requestID) {
    return (!!id && String(entry && entry.id || '').trim() === id) ||
        (!!requestID && String(entry && entry.request_id || '').trim() === requestID);
}

// auditLivePauseMessage maps an auditLivePauseReason to the operator-facing
// hint shown by the page's live-status indicator.
export function auditLivePauseMessage(reason) {
    switch (reason) {
        case 'date_range':
            return m.audit_live_pause_date_range();
        case 'filters':
            return m.audit_live_pause_filters();
        case 'pagination':
            return m.audit_live_pause_pagination();
        default:
            return '';
    }
}

// liveLogsStreamPath builds the stream path with the replay cursor:
// '/admin/live/logs?types=audit,usage[&cursor=N]'.
export function liveLogsStreamPath(lastSeq) {
    let url = LIVE_LOGS_STREAM_PATH;
    const seq = Number(lastSeq || 0);
    if (Number.isFinite(seq) && seq > 0) {
        url += "&cursor=" + encodeURIComponent(String(seq));
    }
    return url;
}

export function liveLogsMethods() {
    return {
        async consumeLiveLogsBody(reader) {
            await consumeEventStream(reader, (event) => this.applyLiveLogEvent(event));
        },

        applyLiveLogEvent(event) {
            if (!event || typeof event !== 'object') return;
            const seq = Number(event.seq || 0);
            if (Number.isFinite(seq) && seq > this.liveLogsLastSeq) {
                this.liveLogsLastSeq = seq;
            }
            const type = String(event.type || '').trim();
            if (type === 'heartbeat') return;
            if (type === 'reset') {
                this.reloadLiveLogSources();
                return;
            }
            if (type === 'audit.removed') {
                this.removeLiveAuditEntry(event.data);
                return;
            }
            if (type.indexOf('audit.') === 0) {
                this.mergeLiveAuditEntry(event.data || {}, type);
                return;
            }
            if (type.indexOf('usage.') === 0) {
                this.mergeLiveUsageEntry(event.data || {}, type);
                if (typeof this.noteLiveTokenUsage === 'function') {
                    this.noteLiveTokenUsage(type);
                }
            }
        },

        reloadLiveLogSources() {
            if (typeof this.fetchUsage === 'function') {
                this.fetchUsage();
            }
            if (this.page === 'audit-logs' && typeof this.fetchAuditLog === 'function') {
                this.fetchAuditLog(true);
            }
        },

        // auditLivePauseReason names why live inserts are paused, so the page
        // can tell the operator how to resume them: 'pagination', 'filters',
        // 'date_range', or null while inserts flow.
        auditLivePauseReason() {
            if (!this.auditLog || this.auditLog.offset !== 0) return 'pagination';
            if (this.auditSearch || this.auditFieldValue || this.auditMethod || this.auditStatusCode || this.auditStream) {
                return 'filters';
            }
            // A custom date range pauses live inserts — except a range that
            // ends today (followsToday): an entry arriving now is inside such
            // a window by definition, and since the range persists across
            // sessions it must not silently disable live logs forever.
            if (!this.followsToday && (this.customStartDate || this.customEndDate)) {
                return 'date_range';
            }
            return null;
        },

        auditLiveInsertAllowed() {
            return !this.auditLivePauseReason();
        },

        usageLiveInsertAllowed() {
            return this.usageLog && this.usageLog.offset === 0 &&
                !this.usageLogSearch && !this.usageFilterModel && !this.usageFilterProvider &&
                !this.usageFilterLabel && !this.usageFilterUserPath && !this.usageFilterSession;
        },

        mergeLiveAuditEntry(incoming, eventType) {
            if (!incoming || typeof incoming !== 'object') return;
            const key = String(incoming.id || incoming.request_id || '').trim();
            if (!key) return;
            const requestID = String(incoming.request_id || '').trim();
            const currentEntries = (this.auditLog && Array.isArray(this.auditLog.entries)) ? this.auditLog.entries : [];
            const index = currentEntries.findIndex((entry) => matchesLiveAuditKey(entry, key, requestID));
            const previous = index >= 0 ? currentEntries[index] || {} : {};
            // A detail event IS the fetched detail: re-triggering the detail
            // fetch for it would loop.
            const isDetail = eventType === 'audit.detail';
            const patch = isDetail
                // The detail entry carries the full payload, so the slim-list
                // marker must not survive the merge from the previous row.
                ? { ...incoming, _detail_loaded: true, _response_partial: false, bodies_omitted: false }
                : this.liveAuditPatch(previous, incoming, eventType);
            if (index >= 0) {
                const merged = this.mergeLiveAuditPatch(previous, patch);
                currentEntries.splice(index, 1, merged);
                this.auditLog.entries = [...currentEntries];
                // Regrouping may demote this row to a thread child; the detail
                // and conversation hooks still target the updated row itself.
                this.regroupLiveAuditHead(merged);
                if (!isDetail) this.fetchExpandedAuditDetailIfReady(merged);
                this.cacheMergedAuditRecord(merged, eventType);
                return merged;
            }
            const child = this.mergeLiveAuditChild(incoming, patch);
            if (child) {
                if (!isDetail) this.fetchExpandedAuditDetailIfReady(child);
                this.cacheMergedAuditRecord(child, eventType);
                return child;
            }
            // List filters and pagination gate visual insertion only. The
            // normalized cache still receives every event so an already-open
            // Interactions drawer cannot lose its live continuation.
            if (!this.auditLiveInsertAllowed()) {
                this.cacheMergedAuditRecord(patch, eventType);
                return;
            }
            if (!isDetail && this.auditGroupSessions) {
                const folded = this.foldLiveAuditIntoThread(patch);
                if (folded) {
                    this.fetchExpandedAuditDetailIfReady(folded);
                    this.cacheMergedAuditRecord(folded, eventType);
                    return folded;
                }
            }
            this.auditLog.entries = [this.mergeLiveAuditUsagePatch(patch), ...currentEntries].slice(0, this.auditLog.limit || 25);
            this.auditLog.total = Number(this.auditLog.total || 0) + 1;
            const inserted = this.auditLog.entries[0];
            if (!isDetail) this.fetchExpandedAuditDetailIfReady(inserted);
            this.cacheMergedAuditRecord(inserted, eventType);
            return inserted;
        },

        // liveAuditPatch stamps the live lifecycle state onto an incoming
        // event's data, ratcheting _live_state forward from the previous row.
        liveAuditPatch(previous, incoming, eventType) {
            const liveState = this.liveAuditStateAfter(previous._live_state, eventType);
            const auditFlushed = this.liveAuditEventFlushed(previous._live_state) || this.liveAuditEventFlushed(liveState);
            const patch = {
                ...incoming,
                _live: true,
                _live_state: liveState,
                _audit_flushed: auditFlushed,
                _live_pending: !auditFlushed
            };
            // A stream event's response body is a partial reconstruction of a
            // still-running stream; the flag drops once a settled state
            // delivers the real body. Other events leave the previous flag
            // untouched.
            if (eventType === 'audit.stream') {
                patch._response_partial = true;
            } else if (this.liveAuditStateSettled(eventType)) {
                patch._response_partial = false;
            }
            return patch;
        },

        // --- Session-thread grouping ---------------------------------------
        // With "Group by session" on, list entries are thread heads (carrying
        // session_count) and each unfolded thread keeps its older entries in
        // auditThreadChildren[session_id].

        // mergeLiveAuditChild merges a live patch into an entry living in a
        // loaded thread-children list (a request that was displaced from head
        // position, or an expanded child whose detail arrived).
        mergeLiveAuditChild(incoming, patch) {
            const lists = this.auditThreadChildren;
            if (!lists || typeof lists !== 'object') return null;
            const id = String(incoming.id || '').trim();
            const requestID = String(incoming.request_id || '').trim();
            const sessionIds = Object.keys(lists);
            for (let i = 0; i < sessionIds.length; i++) {
                const list = lists[sessionIds[i]];
                const entries = list && Array.isArray(list.entries) ? list.entries : [];
                const index = entries.findIndex((entry) => matchesLiveAuditKey(entry, id, requestID));
                if (index < 0) continue;
                const merged = this.mergeLiveAuditPatch(entries[index] || {}, patch);
                const nextEntries = [...entries];
                nextEntries.splice(index, 1, merged);
                this.auditThreadChildren = { ...lists, [sessionIds[i]]: { ...list, entries: nextEntries } };
                return merged;
            }
            return null;
        },

        // regroupLiveAuditHead folds a list row into another on-screen head of
        // the same session after an in-place merge. This is how a live row
        // inserted sessionless (audit.started fires before session detection
        // stamps the context) joins its thread once a later event delivers the
        // session id: the NEWEST of the two rows becomes the thread head — the
        // event that happens to complete last is not necessarily the newest
        // request — the other is retained in the thread's children slot, and
        // the two rows collapse into one thread (total shrinks by one).
        regroupLiveAuditHead(entry) {
            if (!this.auditGroupSessions) return null;
            const sessionId = String((entry && entry.session_id) || '').trim();
            if (!sessionId) return null;
            const entries = (this.auditLog && Array.isArray(this.auditLog.entries)) ? this.auditLog.entries : [];
            const id = String(entry.id || '').trim();
            const myIndex = entries.findIndex((candidate) => String(candidate.id || '').trim() === id);
            if (myIndex < 0) return null;
            const otherIndex = entries.findIndex((candidate, index) => {
                return index !== myIndex && String(candidate.session_id || '').trim() === sessionId;
            });
            if (otherIndex < 0) return null;
            const other = entries[otherIndex];
            const otherTime = Date.parse(other && other.timestamp);
            const entryTime = Date.parse(entry && entry.timestamp);
            const otherIsNewer =
                Number.isFinite(otherTime) && Number.isFinite(entryTime) && otherTime > entryTime;
            const head = otherIsNewer ? other : entry;
            const child = otherIsNewer ? entry : other;
            const merged = {
                ...head,
                session_count:
                    Math.max(1, Number(other.session_count || 1)) +
                    Math.max(1, Number(entry.session_count || 1))
            };
            const next = entries.filter((_, index) => index !== myIndex && index !== otherIndex);
            next.unshift(merged);
            this.auditLog.entries = next;
            this.auditLog.total = Math.max(0, Number(this.auditLog.total || 0) - 1);
            this.prependLiveAuditThreadChild(sessionId, child);
            return merged;
        },

        // foldLiveAuditIntoThread makes a fresh live request the new head of
        // its on-screen thread: the old head moves into the loaded children
        // list (or waits for the lazy fetch) and the thread bubbles to the
        // top with its count bumped. Total is unchanged (same thread count).
        foldLiveAuditIntoThread(patch) {
            const sessionId = String((patch && patch.session_id) || '').trim();
            if (!sessionId) return null;
            const entries = (this.auditLog && Array.isArray(this.auditLog.entries)) ? this.auditLog.entries : [];
            const headIndex = entries.findIndex((entry) => String(entry.session_id || '').trim() === sessionId);
            if (headIndex < 0) return null;
            const oldHead = entries[headIndex];
            const oldCount = Number(oldHead.session_count);
            const newHead = this.mergeLiveAuditUsagePatch({
                ...patch,
                session_count: (Number.isFinite(oldCount) && oldCount > 0 ? oldCount : 1) + 1
            });
            const next = [...entries];
            next.splice(headIndex, 1);
            next.unshift(newHead);
            this.auditLog.entries = next;
            this.prependLiveAuditThreadChild(sessionId, oldHead);
            return newHead;
        },

        // prependLiveAuditThreadChild retains a displaced thread member in the
        // session's children slot. When the thread was never expanded it
        // creates a partial list (loaded: false, so expanding still triggers
        // the full fetch) — dropping the entry instead would make its later
        // live events look like brand-new requests and inflate the thread
        // count on every event.
        prependLiveAuditThreadChild(sessionId, entry) {
            const lists = this.auditThreadChildren || {};
            const list = lists[sessionId];
            const child = { ...entry };
            delete child.session_count;
            const next = list && Array.isArray(list.entries)
                ? {
                    ...list,
                    entries: [child, ...list.entries],
                    total: Number(list.total || list.entries.length) + 1
                }
                : { loading: false, loaded: false, entries: [child], total: 1 };
            this.auditThreadChildren = { ...lists, [sessionId]: next };
        },

        removeLiveAuditThreadChild(id, requestID) {
            const lists = this.auditThreadChildren;
            if (!lists || typeof lists !== 'object') return;
            Object.keys(lists).forEach((sessionId) => {
                const list = lists[sessionId];
                const entries = list && Array.isArray(list.entries) ? list.entries : [];
                const next = entries.filter((entry) => !matchesLiveAuditKey(entry, id, requestID));
                const removed = entries.length - next.length;
                if (removed === 0) return;
                this.auditThreadChildren = {
                    ...this.auditThreadChildren,
                    [sessionId]: { ...list, entries: next, total: Math.max(0, Number(list.total || entries.length) - removed) }
                };
                this.decrementLiveAuditThreadCount(sessionId, removed);
            });
        },

        decrementLiveAuditThreadCount(sessionId, count) {
            const entries = (this.auditLog && Array.isArray(this.auditLog.entries)) ? this.auditLog.entries : [];
            const index = entries.findIndex((entry) => String(entry.session_id || '').trim() === sessionId);
            if (index < 0) return;
            const head = entries[index];
            const next = [...entries];
            next.splice(index, 1, {
                ...head,
                session_count: Math.max(1, Number(head.session_count || 1) - count)
            });
            this.auditLog.entries = next;
        },

        mergeLiveAuditPatch(previous, patch) {
            const merged = { ...previous, ...patch };
            if (patch.data === undefined && previous.data !== undefined) {
                merged.data = previous.data;
            } else if (previous.data && patch.data &&
                typeof previous.data === 'object' && typeof patch.data === 'object' &&
                !Array.isArray(previous.data) && !Array.isArray(patch.data)) {
                merged.data = { ...previous.data, ...patch.data };
            }
            return this.mergeLiveAuditUsagePatch(merged);
        },

        mergeLiveAuditUsagePatch(entry) {
            const usageEntry = this.liveUsageEntryForAudit(entry);
            if (!usageEntry) return entry;
            const merged = this.auditEntryWithLiveUsage(entry, usageEntry);
            this.removeSkippedLiveUsage(usageEntry);
            return merged;
        },

        liveUsageEntryForAudit(entry) {
            const requestID = String(entry && entry.request_id || '').trim();
            if (!requestID) return null;
            const entries = this.usageLog && Array.isArray(this.usageLog.entries) ? this.usageLog.entries : [];
            const visible = entries.find((usageEntry) => {
                return String(usageEntry && usageEntry.request_id || '').trim() === requestID;
            });
            if (visible) return visible;
            return this.skippedLiveUsageByRequestId && this.skippedLiveUsageByRequestId[requestID] || null;
        },

        cacheMergedAuditRecord(entry, eventType) {
            if (entry && typeof this.cacheAuditRecord === 'function') {
                return this.cacheAuditRecord(entry, eventType);
            }
            return entry;
        },

        fetchExpandedAuditDetailIfReady(entry) {
            if (!entry || !this.isAuditEntryExpanded || !this.isAuditEntryExpanded(entry)) return;
            const state = String(entry._live_state || '').trim();
            if (state !== 'audit.flushed' && !entry._audit_flushed) return;
            if (typeof this.fetchAuditEntryDetail === 'function') {
                this.fetchAuditEntryDetail(entry);
            }
        },

        liveAuditStateRank(state) {
            switch (String(state || '').trim()) {
            case 'audit.started':
                return 10;
            case 'audit.updated':
            case 'audit.stream':
                return 20;
            case 'audit.completed':
                return 30;
            case 'audit.failed':
            case 'audit.flushed':
            case 'audit.detail':
                return 40;
            default:
                return 0;
            }
        },

        liveAuditStateAfter(previousState, incomingState) {
            const previous = String(previousState || '').trim();
            const incoming = String(incomingState || '').trim();
            return this.liveAuditStateRank(previous) > this.liveAuditStateRank(incoming) ? previous : incoming;
        },

        // liveAuditStateSettled reports whether a live state already carries
        // its final response (audit.completed or later); below that the
        // request is still in flight.
        liveAuditStateSettled(state) {
            return this.liveAuditStateRank(state) >= this.liveAuditStateRank('audit.completed');
        },

        liveAuditEventFlushed(state) {
            const normalized = String(state || '').trim();
            return normalized === 'audit.failed' || normalized === 'audit.flushed' || normalized === 'audit.detail';
        },

        removeLiveAuditEntry(incoming) {
            if (!incoming || !this.auditLog || !Array.isArray(this.auditLog.entries)) return;
            const id = String(incoming.id || '').trim();
            const requestID = String(incoming.request_id || '').trim();
            if (!id && !requestID) return;
            const current = this.auditLog.entries;
            const next = [];
            let removedCount = 0;
            let preservedThreads = 0;
            let reloadGroupedList = false;
            current.forEach((entry) => {
                if (!matchesLiveAuditKey(entry, id, requestID)) {
                    next.push(entry);
                    return;
                }
                removedCount++;
                const sessionId = String(entry.session_id || '').trim();
                const sessionCount = Math.max(1, Number(entry.session_count || 1));
                if (!this.auditGroupSessions || !sessionId || sessionCount <= 1) return;

                // Removing a live thread head does not remove the persisted
                // session behind it. Promote the newest loaded child; if the
                // thread was never expanded, refetch the grouped source.
                preservedThreads++;
                const list = this.auditThreadChildren && this.auditThreadChildren[sessionId];
                const children = list && Array.isArray(list.entries)
                    ? list.entries.filter((child) => !matchesLiveAuditKey(child, id, requestID))
                    : [];
                if (children.length === 0) {
                    reloadGroupedList = true;
                    return;
                }
                const promoted = {
                    ...children[0],
                    session_id: sessionId,
                    session_count: sessionCount - 1
                };
                next.push(promoted);
                this.auditThreadChildren = {
                    ...this.auditThreadChildren,
                    [sessionId]: {
                        ...list,
                        entries: children.slice(1),
                        total: Math.max(0, Number(list.total || sessionCount) - 1)
                    }
                };
            });
            if (removedCount > 0) {
                this.auditLog.entries = next;
                this.auditLog.total = Math.max(
                    0,
                    Number(this.auditLog.total || 0) - removedCount + preservedThreads
                );
            }
            this.removeLiveAuditThreadChild(id, requestID);
            if (reloadGroupedList && typeof this.fetchAuditLog === 'function') {
                this.fetchAuditLog(true);
            }
        },

        mergeLiveUsageEntry(incoming, eventType) {
            if (!incoming || typeof incoming !== 'object') return;
            incoming = { ...incoming, _live_state: eventType || incoming._live_state || 'usage.completed' };
            const id = String(incoming.id || '').trim();
            if (!id) return;
            const currentEntries = (this.usageLog && Array.isArray(this.usageLog.entries)) ? this.usageLog.entries : [];
            const index = currentEntries.findIndex((entry) => String(entry.id || '').trim() === id);
            if (index >= 0) {
                const previous = currentEntries[index] || {};
                const merged = this.mergeLiveUsagePatch(previous, incoming);
                this.applyLiveUsageToAudit(merged);
                if (this.liveUsageShouldSkip(merged)) {
                    currentEntries.splice(index, 1);
                    this.usageLog.entries = [...currentEntries];
                    this.usageLog.total = Math.max(0, Number(this.usageLog.total || 0) - 1);
                    this.storeSkippedLiveUsage(merged);
                    return;
                }
                currentEntries.splice(index, 1, merged);
                this.usageLog.entries = [...currentEntries];
                this.removeSkippedLiveUsage(merged);
                return;
            }
            const liveEntry = this.mergeLiveUsagePatch(this.liveUsageSeedForEntry(incoming), incoming);
            this.applyLiveUsageToAudit(liveEntry);
            if (this.liveUsageShouldSkip(liveEntry)) {
                this.storeSkippedLiveUsage(liveEntry);
                return;
            }
            this.removeSkippedLiveUsage(liveEntry);
            this.usageLog.entries = [liveEntry, ...currentEntries].slice(0, this.usageLog.limit || 50);
            this.usageLog.total = Number(this.usageLog.total || 0) + 1;
        },

        mergeLiveUsagePatch(previous, incoming) {
            previous = previous && typeof previous === 'object' ? previous : {};
            const liveState = this.liveUsageStateAfter(previous._live_state, incoming && incoming._live_state);
            const usageFlushed = this.liveUsageEventFlushed(previous) || this.liveUsageEventFlushed({ ...incoming, _live_state: liveState });
            return {
                ...previous,
                ...incoming,
                _live: true,
                _live_state: liveState || 'usage.completed',
                _live_pending: !usageFlushed,
                _usage_flushed: usageFlushed
            };
        },

        liveUsageShouldSkip(entry) {
            return !!(this.usageLogHideCached && this.liveUsageEntryCached(entry)) || !this.usageLiveInsertAllowed();
        },

        liveUsageSeedForEntry(entry) {
            return this.skippedLiveUsageForEntry(entry) || this.auditLiveUsageForEntry(entry);
        },

        skippedLiveUsageForEntry(entry) {
            const requestID = String(entry && entry.request_id || '').trim();
            return requestID && this.skippedLiveUsageByRequestId ? this.skippedLiveUsageByRequestId[requestID] : null;
        },

        auditLiveUsageForEntry(entry) {
            const requestID = String(entry && entry.request_id || '').trim();
            if (!requestID || !this.auditLog || !Array.isArray(this.auditLog.entries)) return null;
            const auditEntry = this.auditLog.entries.find((candidate) => String(candidate && candidate.request_id || '').trim() === requestID);
            const usage = auditEntry && auditEntry.usage && typeof auditEntry.usage === 'object' && !Array.isArray(auditEntry.usage)
                ? auditEntry.usage
                : null;
            if (!usage) return null;
            return {
                id: entry && entry.id,
                request_id: requestID,
                entries: usage.entries,
                input_tokens: usage.input_tokens,
                uncached_input_tokens: usage.uncached_input_tokens,
                cached_input_tokens: usage.cached_input_tokens,
                cache_write_input_tokens: usage.cache_write_input_tokens,
                output_tokens: usage.output_tokens,
                total_tokens: usage.total_tokens,
                cached_input_ratio: usage.cached_input_ratio,
                estimated_cached_characters: usage.estimated_cached_characters,
                _live_state: auditEntry._usage_live_state,
                _live_pending: auditEntry._usage_live_pending,
                _usage_flushed: auditEntry._usage_flushed
            };
        },

        storeSkippedLiveUsage(entry) {
            const requestID = String(entry && entry.request_id || '').trim();
            if (!requestID) return;
            if (!this.skippedLiveUsageByRequestId || typeof this.skippedLiveUsageByRequestId !== 'object' || Array.isArray(this.skippedLiveUsageByRequestId)) {
                this.skippedLiveUsageByRequestId = {};
            }
            this.skippedLiveUsageByRequestId[requestID] = entry;
        },

        removeSkippedLiveUsage(entry) {
            const requestID = String(entry && entry.request_id || '').trim();
            if (requestID && this.skippedLiveUsageByRequestId) {
                delete this.skippedLiveUsageByRequestId[requestID];
            }
        },

        liveUsageEntryCached(entry) {
            const cacheType = String(entry && entry.cache_type || '').trim().toLowerCase();
            return cacheType === 'exact' || cacheType === 'semantic' || !!(entry && entry.cache_hit);
        },

        liveUsageEventFlushed(entry) {
            const state = String(entry && entry._live_state || '').trim();
            return !!(entry && entry._usage_flushed) || state === 'usage.failed' || state === 'usage.flushed';
        },

        liveUsageStateRank(state) {
            switch (String(state || '').trim()) {
            case 'usage.completed':
                return 10;
            case 'usage.failed':
            case 'usage.flushed':
                return 20;
            default:
                return 0;
            }
        },

        liveUsageStateAfter(previousState, incomingState) {
            const previous = String(previousState || '').trim();
            const incoming = String(incomingState || '').trim();
            return this.liveUsageStateRank(previous) > this.liveUsageStateRank(incoming) ? previous : incoming;
        },

        applyLiveUsageToAudit(usageEntry) {
            const requestID = String(usageEntry && usageEntry.request_id || '').trim();
            if (!requestID || !this.auditLog || !Array.isArray(this.auditLog.entries)) return;
            const index = this.auditLog.entries.findIndex((entry) => String(entry.request_id || '').trim() === requestID);
            if (index >= 0) {
                const entry = this.auditLog.entries[index];
                this.auditLog.entries.splice(index, 1, this.auditEntryWithLiveUsage(entry, usageEntry));
                this.auditLog.entries = [...this.auditLog.entries];
            }

            const lists = this.auditThreadChildren;
            if (!lists || typeof lists !== 'object') return;
            let nextLists = lists;
            let changed = false;
            Object.keys(lists).forEach((sessionId) => {
                const list = nextLists[sessionId];
                const entries = list && Array.isArray(list.entries) ? list.entries : [];
                const childIndex = entries.findIndex((entry) => String(entry.request_id || '').trim() === requestID);
                if (childIndex < 0) return;
                const nextEntries = [...entries];
                nextEntries.splice(
                    childIndex,
                    1,
                    this.auditEntryWithLiveUsage(entries[childIndex], usageEntry)
                );
                nextLists = { ...nextLists, [sessionId]: { ...list, entries: nextEntries } };
                changed = true;
            });
            if (changed) {
                this.auditThreadChildren = nextLists;
            }
        },

        auditEntryWithLiveUsage(entry, usageEntry) {
            const usageLiveState = this.liveUsageStateAfter(entry._usage_live_state, usageEntry._live_state || 'usage.completed');
            const usageFlushed = this.liveUsageEventFlushed({
                _live_state: usageLiveState,
                _usage_flushed: entry._usage_flushed || usageEntry._usage_flushed
            });
            return {
                ...entry,
                usage: this.liveUsageSummary(usageEntry, entry.usage),
                _usage_live_state: usageLiveState || 'usage.completed',
                _usage_live_pending: !usageFlushed,
                _usage_flushed: usageFlushed
            };
        },

        liveUsageSummary(usageEntry, previousUsage) {
            const previous = previousUsage && typeof previousUsage === 'object' && !Array.isArray(previousUsage) ? previousUsage : {};
            const inputTokens = this.liveNumber(usageEntry.input_tokens, this.liveNumber(previous.input_tokens, 0));
            const outputTokens = this.liveNumber(usageEntry.output_tokens, this.liveNumber(previous.output_tokens, 0));
            let uncachedInputTokens = this.liveNumber(usageEntry.uncached_input_tokens, this.liveNumber(previous.uncached_input_tokens, 0));
            const cachedInputTokens = this.liveNumber(usageEntry.cached_input_tokens, this.liveNumber(previous.cached_input_tokens, 0));
            const cacheWriteInputTokens = this.liveNumber(usageEntry.cache_write_input_tokens, this.liveNumber(previous.cache_write_input_tokens, 0));
            if (inputTokens > 0 && uncachedInputTokens + cachedInputTokens + cacheWriteInputTokens === 0) {
                uncachedInputTokens = inputTokens;
            }
            const segmentedInputTokens = uncachedInputTokens + cachedInputTokens + cacheWriteInputTokens;
            const normalizedInputTokens = segmentedInputTokens || inputTokens;
            const computedTotalTokens = normalizedInputTokens + outputTokens;
            const totalTokens = computedTotalTokens || this.liveNumber(
                usageEntry.total_tokens,
                this.liveNumber(previous.total_tokens, 0)
            );
            const cachedInputRatio = this.liveNumber(
                usageEntry.cached_input_ratio,
                this.liveNumber(previous.cached_input_ratio, normalizedInputTokens > 0 ? cachedInputTokens / normalizedInputTokens : 0)
            );
            return {
                entries: Math.max(1, this.liveNumber(usageEntry.entries, this.liveNumber(previous.entries, 1))),
                input_tokens: normalizedInputTokens,
                uncached_input_tokens: uncachedInputTokens,
                cached_input_tokens: cachedInputTokens,
                cache_write_input_tokens: cacheWriteInputTokens,
                output_tokens: outputTokens,
                total_tokens: totalTokens,
                cached_input_ratio: cachedInputRatio,
                estimated_cached_characters: this.liveNumber(
                    usageEntry.estimated_cached_characters,
                    this.liveNumber(previous.estimated_cached_characters, cachedInputTokens * 4)
                )
            };
        },

        liveNumber(value, fallback) {
            const number = Number(value);
            return Number.isFinite(number) ? number : fallback;
        },

        auditEntryShouldFetchDetail(entry) {
            if (!entry || entry._detail_loading || entry._detail_loaded) return false;
            if (this.auditEntryLiveDetailPending(entry)) return false;
            if (this.auditEntryNeedsPersistedLiveDetail(entry)) return true;
            // The list endpoint ships slim entries (bodies stripped
            // server-side); the flag is the explicit signal that the full
            // payload lives behind the detail endpoint.
            if (entry.bodies_omitted) return true;
            return !this.auditEntryHasDetailData(entry);
        },

        auditEntryLiveDetailPending(entry) {
            if (!entry || !entry._live) return false;
            const state = String(entry._live_state || '').trim();
            if (state === 'audit.failed') return true;
            return !entry._audit_flushed && state !== 'audit.flushed' && state !== 'audit.detail';
        },

        auditEntryNeedsPersistedLiveDetail(entry) {
            return !!(entry && entry._live && !entry._detail_loaded);
        },

        auditEntryHasDetailData(entry) {
            const data = entry && entry.data;
            if (!data || typeof data !== 'object') return false;
            return data.request_headers !== undefined ||
                data.response_headers !== undefined ||
                data.request_body !== undefined ||
                data.response_body !== undefined ||
                data.request_body_too_big_to_handle !== undefined ||
                data.response_body_too_big_to_handle !== undefined ||
                data.user_agent !== undefined ||
                data.api_key_hash !== undefined ||
                data.temperature !== undefined ||
                data.max_tokens !== undefined ||
                data.error_message !== undefined ||
                data.error_code !== undefined;
        },

        clearAuditDetailLoading(entry) {
            if (!entry) return;
            const id = String(entry.id || '').trim();
            const requestID = String(entry.request_id || '').trim();
            const entries = this.auditLog && Array.isArray(this.auditLog.entries) ? this.auditLog.entries : [];
            const current = entries.find((candidate) => {
                if (id && String(candidate.id || '').trim() === id) return true;
                return !!(requestID && String(candidate.request_id || '').trim() === requestID);
            });
            const target = current || entry;
            target._detail_loading = false;
            if (current) {
                this.auditLog.entries = [...entries];
            }
        }
    };
}
