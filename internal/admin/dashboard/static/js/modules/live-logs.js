(function(global) {
    function dashboardLiveLogsModule() {
        function liveLogsPath(path) {
            if (typeof window !== 'undefined' && typeof window.gomodelPath === 'function') {
                return window.gomodelPath(path);
            }
            return path;
        }

        return {
            liveLogsLastSeq: 0,
            liveLogsReconnectAttempts: 0,
            liveLogsReconnectTimer: null,
            liveLogsController: null,

            liveLogsEnabled() {
                return typeof this.workflowRuntimeBooleanFlag === 'function'
                    ? this.workflowRuntimeBooleanFlag('DASHBOARD_LIVE_LOGS_ENABLED', true)
                    : true;
            },

            startLiveLogs() {
                if (!this.liveLogsEnabled() || typeof fetch !== 'function' || typeof ReadableStream === 'undefined') {
                    return;
                }
                this.stopLiveLogs();
                this.liveLogsController = typeof AbortController === 'function' ? new AbortController() : null;
                this.readLiveLogsStream(this.liveLogsController);
            },

            stopLiveLogs() {
                if (this.liveLogsReconnectTimer) {
                    clearTimeout(this.liveLogsReconnectTimer);
                    this.liveLogsReconnectTimer = null;
                }
                if (this.liveLogsController && typeof this.liveLogsController.abort === 'function') {
                    this.liveLogsController.abort();
                }
                this.liveLogsController = null;
            },

            async readLiveLogsStream(controller) {
                const request = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                if (controller) {
                    request.signal = controller.signal;
                }
                let url = liveLogsPath('/admin/live/logs?types=audit,usage');
                if (this.liveLogsLastSeq > 0) {
                    url += '&cursor=' + encodeURIComponent(String(this.liveLogsLastSeq));
                }

                try {
                    const res = await fetch(url, request);
                    const handled = this.handleFetchResponse(res, 'live logs', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled || !res.body || typeof res.body.getReader !== 'function') {
                        this.scheduleLiveLogsReconnect();
                        return;
                    }
                    this.liveLogsReconnectAttempts = 0;
                    await this.consumeLiveLogsBody(res.body.getReader());
                    this.scheduleLiveLogsReconnect();
                } catch (e) {
                    if (typeof this._isAbortError === 'function' && this._isAbortError(e)) {
                        return;
                    }
                    console.error('Live logs stream failed:', e);
                    this.scheduleLiveLogsReconnect();
                }
            },

            async consumeLiveLogsBody(reader) {
                const decoder = new TextDecoder();
                let buffer = '';
                while (true) {
                    const chunk = await reader.read();
                    if (chunk.done) break;
                    buffer += decoder.decode(chunk.value, { stream: true });
                    let delimiter;
                    while ((delimiter = buffer.match(/\r?\n\r?\n/))) {
                        const splitAt = delimiter.index;
                        const frame = buffer.slice(0, splitAt);
                        buffer = buffer.slice(splitAt + delimiter[0].length);
                        this.handleLiveLogsFrame(frame);
                    }
                }
                buffer += decoder.decode();
                if (buffer.trim()) {
                    this.handleLiveLogsFrame(buffer);
                }
            },

            handleLiveLogsFrame(frame) {
                const lines = String(frame || '').split(/\r?\n/);
                const data = [];
                for (const line of lines) {
                    if (line.indexOf('data:') === 0) {
                        data.push(line.slice(5).trimStart());
                    }
                }
                if (data.length === 0) return;
                let event;
                try {
                    event = JSON.parse(data.join('\n'));
                } catch (_) {
                    return;
                }
                this.applyLiveLogEvent(event);
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
                }
            },

            scheduleLiveLogsReconnect() {
                if (!this.liveLogsEnabled()) return;
                if (this.liveLogsReconnectTimer) return;
                const attempt = Math.min(this.liveLogsReconnectAttempts + 1, 6);
                this.liveLogsReconnectAttempts = attempt;
                const delay = Math.min(30000, 500 * Math.pow(2, attempt - 1));
                this.liveLogsReconnectTimer = setTimeout(() => {
                    this.liveLogsReconnectTimer = null;
                    this.startLiveLogs();
                }, delay);
            },

            reloadLiveLogSources() {
                if (typeof this.fetchUsage === 'function') {
                    this.fetchUsage();
                }
                if (this.page === 'audit-logs' && typeof this.fetchAuditLog === 'function') {
                    this.fetchAuditLog(true);
                }
            },

            auditLiveInsertAllowed() {
                return this.auditLog && this.auditLog.offset === 0 &&
                    !this.auditSearch && !this.auditMethod && !this.auditStatusCode && !this.auditStream &&
                    !this.customStartDate && !this.customEndDate;
            },

            usageLiveInsertAllowed() {
                return this.usageLog && this.usageLog.offset === 0 &&
                    !this.usageLogSearch && !this.usageLogModel && !this.usageLogProvider && !this.usageLogUserPath;
            },

            mergeLiveAuditEntry(incoming, eventType) {
                if (!incoming || typeof incoming !== 'object') return;
                const key = String(incoming.id || incoming.request_id || '').trim();
                if (!key) return;
                const currentEntries = (this.auditLog && Array.isArray(this.auditLog.entries)) ? this.auditLog.entries : [];
                const index = currentEntries.findIndex((entry) => {
                    return String(entry.id || '').trim() === key ||
                        (incoming.request_id && String(entry.request_id || '').trim() === String(incoming.request_id).trim());
                });
                const previous = index >= 0 ? currentEntries[index] || {} : {};
                if (eventType === 'audit.detail') {
                    const patch = { ...incoming, _detail_loaded: true };
                    if (index >= 0) {
                        const merged = this.mergeLiveAuditPatch(previous, patch);
                        currentEntries.splice(index, 1, merged);
                        this.auditLog.entries = [...currentEntries];
                        return merged;
                    }
                    if (!this.auditLiveInsertAllowed()) return;
                    this.auditLog.entries = [patch, ...currentEntries].slice(0, this.auditLog.limit || 25);
                    this.auditLog.total = Number(this.auditLog.total || 0) + 1;
                    return this.auditLog.entries[0];
                }
                const liveState = this.liveAuditStateAfter(previous._live_state, eventType);
                const auditFlushed = this.liveAuditEventFlushed(previous._live_state) || this.liveAuditEventFlushed(liveState);
                const patch = { ...incoming, _live: true, _live_state: liveState, _audit_flushed: auditFlushed };
                if (!auditFlushed) {
                    patch._live_pending = true;
                } else {
                    patch._live_pending = false;
                }
                if (index >= 0) {
                    const merged = this.mergeLiveAuditPatch(previous, patch);
                    currentEntries.splice(index, 1, merged);
                    this.auditLog.entries = [...currentEntries];
                    this.fetchExpandedAuditDetailIfReady(merged);
                    return merged;
                }
                if (!this.auditLiveInsertAllowed()) return;
                this.auditLog.entries = [patch, ...currentEntries].slice(0, this.auditLog.limit || 25);
                this.auditLog.total = Number(this.auditLog.total || 0) + 1;
                const inserted = this.auditLog.entries[0];
                this.fetchExpandedAuditDetailIfReady(inserted);
                return inserted;
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
                return merged;
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

            liveAuditEventFlushed(state) {
                const normalized = String(state || '').trim();
                return normalized === 'audit.failed' || normalized === 'audit.flushed' || normalized === 'audit.detail';
            },

            removeLiveAuditEntry(incoming) {
                if (!incoming || !this.auditLog || !Array.isArray(this.auditLog.entries)) return;
                const id = String(incoming.id || '').trim();
                const requestID = String(incoming.request_id || '').trim();
                if (!id && !requestID) return;
                const next = this.auditLog.entries.filter((entry) => {
                    if (id && String(entry.id || '').trim() === id) return false;
                    if (requestID && String(entry.request_id || '').trim() === requestID) return false;
                    return true;
                });
                const removedCount = this.auditLog.entries.length - next.length;
                if (removedCount > 0) {
                    this.auditLog.entries = next;
                    this.auditLog.total = Math.max(0, Number(this.auditLog.total || 0) - removedCount);
                }
            },

            mergeLiveUsageEntry(incoming, eventType) {
                if (!incoming || typeof incoming !== 'object') return;
                incoming = { ...incoming, _live_state: eventType || incoming._live_state || 'usage.completed' };
                const id = String(incoming.id || '').trim();
                if (!id) return;
                this.applyLiveUsageToAudit(incoming);
                const currentEntries = (this.usageLog && Array.isArray(this.usageLog.entries)) ? this.usageLog.entries : [];
                const index = currentEntries.findIndex((entry) => String(entry.id || '').trim() === id);
                if (index >= 0) {
                    const previous = currentEntries[index] || {};
                    const liveState = this.liveUsageStateAfter(previous._live_state, incoming._live_state);
                    const usageFlushed = this.liveUsageEventFlushed(previous) || this.liveUsageEventFlushed({ ...incoming, _live_state: liveState });
                    currentEntries.splice(index, 1, {
                        ...previous,
                        ...incoming,
                        _live: true,
                        _live_state: liveState || 'usage.completed',
                        _live_pending: !usageFlushed,
                        _usage_flushed: usageFlushed
                    });
                    this.usageLog.entries = [...currentEntries];
                    return;
                }
                if (!this.usageLiveInsertAllowed()) return;
                const liveState = this.liveUsageStateAfter('', incoming._live_state);
                const usageFlushed = this.liveUsageEventFlushed({ ...incoming, _live_state: liveState });
                this.usageLog.entries = [{
                    ...incoming,
                    _live: true,
                    _live_state: liveState || 'usage.completed',
                    _live_pending: !usageFlushed,
                    _usage_flushed: usageFlushed
                }, ...currentEntries].slice(0, this.usageLog.limit || 50);
                this.usageLog.total = Number(this.usageLog.total || 0) + 1;
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
                if (this.liveUsageEntryCached(usageEntry)) return;
                const requestID = String(usageEntry && usageEntry.request_id || '').trim();
                if (!requestID || !this.auditLog || !Array.isArray(this.auditLog.entries)) return;
                const index = this.auditLog.entries.findIndex((entry) => String(entry.request_id || '').trim() === requestID);
                if (index < 0) return;
                const entry = this.auditLog.entries[index];
                const usageLiveState = this.liveUsageStateAfter(entry._usage_live_state, usageEntry._live_state || 'usage.completed');
                const usageFlushed = this.liveUsageEventFlushed({
                    _live_state: usageLiveState,
                    _usage_flushed: entry._usage_flushed || usageEntry._usage_flushed
                });
                const usage = {
                    entries: 1,
                    input_tokens: Number(usageEntry.input_tokens || 0),
                    uncached_input_tokens: Number(usageEntry.input_tokens || 0),
                    cached_input_tokens: 0,
                    cache_write_input_tokens: 0,
                    output_tokens: Number(usageEntry.output_tokens || 0),
                    total_tokens: Number(usageEntry.total_tokens || 0),
                    cached_input_ratio: 0,
                    estimated_cached_characters: 0
                };
                this.auditLog.entries.splice(index, 1, {
                    ...entry,
                    usage,
                    _usage_live_state: usageLiveState || 'usage.completed',
                    _usage_live_pending: !usageFlushed,
                    _usage_flushed: usageFlushed
                });
                this.auditLog.entries = [...this.auditLog.entries];
            },

            async fetchAuditEntryDetail(entry) {
                if (!entry || entry._detail_loading || entry._detail_loaded || this.auditEntryHasDetailData(entry)) return;
                const id = String(entry.id || '').trim();
                if (!id) return;
                entry._detail_loading = true;
                let detailEntry = entry;
                try {
                    const request = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    const res = await fetch(liveLogsPath('/admin/audit/detail?log_id=' + encodeURIComponent(id)), request);
                    const handled = this.handleFetchResponse(res, 'audit detail', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) return;
                    const payload = await res.json();
                    detailEntry = this.mergeLiveAuditEntry(payload, 'audit.detail') || detailEntry;
                } catch (e) {
                    console.error('Failed to fetch audit detail:', e);
                } finally {
                    this.clearAuditDetailLoading(detailEntry);
                }
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

    global.dashboardLiveLogsModule = dashboardLiveLogsModule;
})(window);
