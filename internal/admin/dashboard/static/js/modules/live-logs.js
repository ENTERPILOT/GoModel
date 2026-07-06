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
            skippedLiveUsageByRequestId: null,

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
                    if (typeof this.noteLiveTokenUsage === 'function') {
                        this.noteLiveTokenUsage(type);
                    }
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
                    !this.usageLogSearch && !this.usageFilterModel && !this.usageFilterProvider &&
                    !this.usageFilterLabel && !this.usageFilterUserPath;
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
                    const patch = { ...incoming, _detail_loaded: true, _response_partial: false };
                    if (index >= 0) {
                        const merged = this.mergeLiveAuditPatch(previous, patch);
                        currentEntries.splice(index, 1, merged);
                        this.auditLog.entries = [...currentEntries];
                        this.notifyLiveConversation(merged);
                        return merged;
                    }
                    if (!this.auditLiveInsertAllowed()) return;
                    this.auditLog.entries = [this.mergeLiveAuditUsagePatch(patch), ...currentEntries].slice(0, this.auditLog.limit || 25);
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
                // A stream event's response body is a partial reconstruction of a
                // still-running stream; the flag drops once a settled state
                // delivers the real body. Other events leave the previous flag
                // untouched.
                if (eventType === 'audit.stream') {
                    patch._response_partial = true;
                } else if (this.liveAuditStateSettled(eventType)) {
                    patch._response_partial = false;
                }
                if (index >= 0) {
                    const merged = this.mergeLiveAuditPatch(previous, patch);
                    currentEntries.splice(index, 1, merged);
                    this.auditLog.entries = [...currentEntries];
                    this.fetchExpandedAuditDetailIfReady(merged);
                    this.notifyLiveConversation(merged);
                    return merged;
                }
                if (!this.auditLiveInsertAllowed()) return;
                this.auditLog.entries = [this.mergeLiveAuditUsagePatch(patch), ...currentEntries].slice(0, this.auditLog.limit || 25);
                this.auditLog.total = Number(this.auditLog.total || 0) + 1;
                const inserted = this.auditLog.entries[0];
                this.fetchExpandedAuditDetailIfReady(inserted);
                this.notifyLiveConversation(inserted);
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

            // notifyLiveConversation forwards merged live entries to the
            // Interactions drawer (when its module is mixed in) so an open
            // live conversation re-renders as stream chunks arrive.
            notifyLiveConversation(entry) {
                if (entry && typeof this.refreshLiveConversation === 'function') {
                    this.refreshLiveConversation(entry);
                }
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

            // liveAuditStateSettled reports whether a live state already
            // carries its final response (audit.completed or later); below
            // that the request is still in flight.
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
                if (index < 0) return;
                const entry = this.auditLog.entries[index];
                this.auditLog.entries.splice(index, 1, this.auditEntryWithLiveUsage(entry, usageEntry));
                this.auditLog.entries = [...this.auditLog.entries];
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

            async fetchAuditEntryDetail(entry) {
                if (!this.auditEntryShouldFetchDetail(entry)) return;
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

            auditEntryShouldFetchDetail(entry) {
                if (!entry || entry._detail_loading || entry._detail_loaded) return false;
                if (this.auditEntryLiveDetailPending(entry)) return false;
                if (this.auditEntryNeedsPersistedLiveDetail(entry)) return true;
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

    global.dashboardLiveLogsModule = dashboardLiveLogsModule;
})(window);
