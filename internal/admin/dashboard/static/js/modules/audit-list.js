(function(global) {
    function tryParseJSON(value) {
        try {
            return JSON.parse(value);
        } catch (_) {
            return null;
        }
    }

    function normalizeAuditErrorText(value, depth) {
        const text = String(value || '').trim();
        if (!text) return '';
        if (depth > 6) return text;

        const parsed = tryParseJSON(text);
        if (parsed == null) return text;
        return findNestedAuditErrorMessage(parsed, depth + 1) || text;
    }

    function auditErrorMessageFromField(value) {
        if (value == null) return '';
        if (typeof value === 'string') return normalizeAuditErrorText(value, 0);
        return findNestedAuditErrorMessage(value, 0);
    }

    function findNestedAuditErrorMessage(value, depth) {
        if (value == null || depth > 6) return '';

        if (typeof value === 'string') {
            const parsed = tryParseJSON(value.trim());
            return parsed == null ? '' : findNestedAuditErrorMessage(parsed, depth + 1);
        }

        if (Array.isArray(value)) {
            for (let i = 0; i < value.length; i++) {
                const message = findNestedAuditErrorMessage(value[i], depth + 1);
                if (message) return message;
            }
            return '';
        }

        if (typeof value !== 'object') return '';

        if (value.error !== undefined) {
            if (typeof value.error === 'string') {
                return normalizeAuditErrorText(value.error, depth + 1);
            }
            if (value.error && typeof value.error.message === 'string' && value.error.message.trim()) {
                return normalizeAuditErrorText(value.error.message, depth + 1);
            }
            const nestedError = findNestedAuditErrorMessage(value.error, depth + 1);
            if (nestedError) return nestedError;
        }

        if (
            typeof value.message === 'string' &&
            value.message.trim() &&
            (value.error !== undefined || value.code !== undefined || value.status !== undefined || value.type !== undefined)
        ) {
            return normalizeAuditErrorText(value.message, depth + 1);
        }

        const keys = Object.keys(value);
        for (let i = 0; i < keys.length; i++) {
            if (keys[i] === 'error') continue;
            const message = findNestedAuditErrorMessage(value[keys[i]], depth + 1);
            if (message) return message;
        }
        return '';
    }

    function auditEntryStatusCode(entry, data) {
        const candidates = [
            entry && entry.status_code,
            entry && entry.status,
            data && data.status_code,
            data.status
        ];

        for (let i = 0; i < candidates.length; i++) {
            const parsed = Number(candidates[i]);
            if (Number.isFinite(parsed)) return parsed;
        }

        return null;
    }

    function hasTopLevelAuditErrorShape(value) {
        if (value == null) return false;

        let candidate = value;
        if (typeof candidate === 'string') {
            const parsed = tryParseJSON(candidate.trim());
            if (parsed == null) return false;
            candidate = parsed;
        }

        if (Array.isArray(candidate) || typeof candidate !== 'object') return false;
        if (candidate.error !== undefined) return true;
        if (typeof candidate.message === 'string' && candidate.message.trim()) return true;

        const topLevelErrorFields = ['detail', 'error_message', 'error_msg', 'title'];
        for (let i = 0; i < topLevelErrorFields.length; i++) {
            const field = topLevelErrorFields[i];
            if (typeof candidate[field] === 'string' && candidate[field].trim()) return true;
        }

        return false;
    }

    function shouldInspectAuditResponseBody(entry, data) {
        const statusCode = auditEntryStatusCode(entry, data);
        if (statusCode !== null && statusCode >= 400) return true;
        return hasTopLevelAuditErrorShape(data && data.response_body);
    }

    function dashboardAuditListModule() {
        const clipboardModuleFactory = typeof global.dashboardClipboardModule === 'function'
            ? global.dashboardClipboardModule
            : null;
        const clipboard = clipboardModuleFactory
            ? clipboardModuleFactory()
            : null;

        return {
            _auditQueryStr() {
                if (this.customStartDate && this.customEndDate) {
                    return 'start_date=' + this._formatDate(this.customStartDate) +
                        '&end_date=' + this._formatDate(this.customEndDate);
                }
                return 'days=' + this.days;
            },

            async fetchAuditLog(resetOffset) {
                const requestToken = ++this.auditFetchToken;
                try {
                    if (resetOffset) this.auditLog.offset = 0;
                    let qs = this._auditQueryStr();
                    qs += '&limit=' + this.auditLog.limit + '&offset=' + this.auditLog.offset;
                    if (this.auditSearch) qs += '&search=' + encodeURIComponent(this.auditSearch);
                    if (this.auditMethod) qs += '&method=' + encodeURIComponent(this.auditMethod);
                    if (this.auditStatusCode) qs += '&status_code=' + encodeURIComponent(this.auditStatusCode);
                    if (this.auditStream) qs += '&stream=' + encodeURIComponent(this.auditStream);

                    const request = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    const res = await fetch('/admin/audit/log?' + qs, request);
                    const handled = this.handleFetchResponse(res, 'audit log', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        if (requestToken !== this.auditFetchToken) return;
                        this.auditLog = { entries: [], total: 0, limit: 25, offset: 0 };
                        return;
                    }
                    const payload = await res.json();
                    if (requestToken !== this.auditFetchToken) return;
                    this.auditLog = this.auditLogWithLiveEntries(payload, this.auditLog && this.auditLog.entries);
                    if (!this.auditLog.entries) this.auditLog.entries = [];
                    this.pruneAuditExpandedEntries(this.auditLog.entries);
                    if (typeof this.prefetchAuditWorkflows === 'function') {
                        try {
                            await this.prefetchAuditWorkflows(this.auditLog.entries);
                        } catch (e) {
                            console.error('Failed to prefetch audit workflows:', e);
                        }
                    }
                } catch (e) {
                    console.error('Failed to fetch audit log:', e);
                    if (requestToken !== this.auditFetchToken) return;
                    this.auditLog = { entries: [], total: 0, limit: 25, offset: 0 };
                }
            },

            auditLogWithLiveEntries(payload, currentEntries) {
                const next = payload && typeof payload === 'object'
                    ? { ...payload }
                    : { entries: [], total: 0, limit: 25, offset: 0 };
                const entries = Array.isArray(next.entries) ? next.entries : [];
                next.entries = entries;
                if (!this.auditLogAllowsLiveEntries(next)) return next;

                const liveEntries = (Array.isArray(currentEntries) ? currentEntries : [])
                    .filter((entry) => this.auditEntryLivePreviewPending(entry));
                if (liveEntries.length === 0) return next;

                const persistedKeys = new Set(entries.flatMap((entry) => this.auditEntryIdentityKeys(entry)));
                const prepend = [];
                liveEntries.forEach((entry) => {
                    const keys = this.auditEntryIdentityKeys(entry);
                    if (keys.length === 0) return;
                    if (keys.some((key) => persistedKeys.has(key))) return;
                    keys.forEach((key) => persistedKeys.add(key));
                    prepend.push(entry);
                });
                if (prepend.length === 0) return next;

                next.entries = [...prepend, ...entries].slice(0, next.limit || 25);
                next.total = Number(next.total || 0) + prepend.length;
                return next;
            },

            auditLogAllowsLiveEntries(payload) {
                return payload && Number(payload.offset || 0) === 0 &&
                    !this.auditSearch && !this.auditMethod && !this.auditStatusCode && !this.auditStream &&
                    this.auditLiveDateRangeAllowsNow();
            },

            auditLiveDateRangeAllowsNow() {
                if (!this.customStartDate && !this.customEndDate) return true;
                const now = new Date();
                if (this.customStartDate) {
                    const start = new Date(this.customStartDate);
                    start.setHours(0, 0, 0, 0);
                    if (Number.isFinite(start.getTime()) && now < start) return false;
                }
                if (this.customEndDate) {
                    const end = new Date(this.customEndDate);
                    end.setHours(23, 59, 59, 999);
                    if (Number.isFinite(end.getTime()) && now > end) return false;
                }
                return true;
            },

            auditEntryLivePreviewPending(entry) {
                return !!(entry && entry._live && entry._live_pending && !entry._audit_flushed);
            },

            auditEntryIdentityKeys(entry) {
                if (!entry) return [];
                const keys = [];
                const id = String(entry.id || '').trim();
                const requestID = String(entry.request_id || '').trim();
                if (id) keys.push('id:' + id);
                if (requestID) keys.push('request:' + requestID);
                return keys;
            },

            auditEntryKey(entry) {
                return String(entry && entry.id || '').trim();
            },

            isAuditEntryExpanded(entry) {
                const key = this.auditEntryKey(entry);
                if (!key) return false;
                return !!(this.auditExpandedEntries && this.auditExpandedEntries[key]);
            },

            markAuditEntryExpanded(entry) {
                const key = this.auditEntryKey(entry);
                if (!key || this.isAuditEntryExpanded(entry)) return;

                this.auditExpandedEntries = {
                    ...(this.auditExpandedEntries || {}),
                    [key]: true
                };
            },

            pruneAuditExpandedEntries(entries) {
                const expanded = this.auditExpandedEntries || {};
                const keys = new Set((Array.isArray(entries) ? entries : [])
                    .map((entry) => this.auditEntryKey(entry))
                    .filter(Boolean));
                const next = {};
                let changed = false;

                Object.keys(expanded).forEach((key) => {
                    if (keys.has(key)) {
                        next[key] = true;
                        return;
                    }
                    changed = true;
                });

                if (changed) {
                    this.auditExpandedEntries = next;
                }
            },

            clearAuditFilters() {
                this.auditSearch = '';
                this.auditMethod = '';
                this.auditStatusCode = '';
                this.auditStream = '';
                this.fetchAuditLog(true);
            },

            auditLogNextPage() {
                if (this.auditLog.offset + this.auditLog.limit < this.auditLog.total) {
                    this.auditLog.offset += this.auditLog.limit;
                    this.fetchAuditLog(false);
                }
            },

            auditLogPrevPage() {
                if (this.auditLog.offset > 0) {
                    this.auditLog.offset = Math.max(0, this.auditLog.offset - this.auditLog.limit);
                    this.fetchAuditLog(false);
                }
            },

            formatDurationNs(ns) {
                if (ns == null) return '-';
                const v = Number(ns);
                if (!Number.isFinite(v)) return '-';
                if (v <= 0) return 'pending';
                if (v < 1000000) return Math.round(v / 1000) + ' \u00b5s';
                if (v < 1000000000) return (v / 1000000).toFixed(2) + ' ms';
                return (v / 1000000000).toFixed(2) + ' s';
            },

            auditEntrySummaryClass(entry) {
                return {
                    'audit-entry-summary-live-in-progress': this.auditEntryLiveInProgress(entry)
                };
            },

            auditEntryLiveInProgress(entry) {
                if (!entry || !entry._live || !entry._live_pending) return false;
                const liveState = String(entry._live_state || '').trim();
                if (liveState === 'audit.completed' || liveState === 'audit.flushed' || liveState === 'audit.detail') {
                    return false;
                }
                if (entry.status_code !== null && entry.status_code !== undefined && entry.status_code !== '') return false;
                if (Number(entry.duration_ns || 0) > 0) return false;
                if (entry.error_type || entry.error_message) return false;

                const data = entry.data || {};
                return !(data.response_headers || data.response_body || data.error_message);
            },

            handleAuditEntryToggle(event, entry) {
                const detailsEl = event && event.currentTarget;
                if (!detailsEl) return;

                if (detailsEl.open) {
                    this.markAuditEntryExpanded(entry);
                    if (typeof this.fetchAuditEntryDetail === 'function') {
                        this.fetchAuditEntryDetail(entry);
                    }
                }
            },

            statusCodeClass(statusCode) {
                if (statusCode === null || statusCode === undefined || statusCode === '') return 'status-unknown';
                const parsedStatus = Number(statusCode);
                if (!Number.isFinite(parsedStatus)) return 'status-unknown';
                if (parsedStatus >= 500) return 'status-error';
                if (parsedStatus >= 400) return 'status-warning';
                if (parsedStatus >= 300) return 'status-neutral';
                return 'status-success';
            },

            auditEntryErrorMessage(entry) {
                const data = entry && entry.data ? entry.data : null;
                if (!data) return '';
                const fieldMessage = auditErrorMessageFromField(data.error_message);
                if (fieldMessage) return fieldMessage;
                if (!shouldInspectAuditResponseBody(entry, data)) return '';
                return findNestedAuditErrorMessage(data.response_body, 0);
            },

            auditAttempts(entry) {
                const attempts = entry && entry.data && Array.isArray(entry.data.attempts)
                    ? entry.data.attempts
                    : [];
                return attempts
                    .map((attempt, index) => ({
                        ...attempt,
                        seq: Number(attempt && attempt.seq || index + 1)
                    }))
                    .sort((a, b) => a.seq - b.seq);
            },

            // auditUsesPerAttemptResponses reports whether responses should be
            // split into one tab per attempt — when a failover/retry happened
            // (more than one attempt) or any attempt failed (e.g. a failed
            // primary while failover is still in flight) — instead of a single
            // combined Response tab.
            auditUsesPerAttemptResponses(entry) {
                const attempts = this.auditAttempts(entry);
                return attempts.length > 1 || attempts.some((attempt) => !(attempt && attempt.success));
            },

            auditAttemptClass(attempt) {
                return {
                    'audit-attempt-success': !!(attempt && attempt.success),
                    'audit-attempt-error': !(attempt && attempt.success)
                };
            },

            auditAttemptStatus(attempt) {
                if (!attempt) return '-';
                const status = attempt.status_code || attempt.status;
                if (status) return String(status);
                return attempt.success ? 'ok' : 'error';
            },

            auditAttemptKind(attempt) {
                const kind = String(attempt && attempt.kind || '').trim();
                return kind || 'attempt';
            },

            auditAttemptProvider(attempt) {
                if (!attempt) return '-';
                const name = String(attempt.provider_name || '').trim();
                const type = String(attempt.provider_type || attempt.provider || '').trim();
                if (name && type && name !== type) return name + ' (' + type + ')';
                return name || type || '-';
            },

            auditAttemptModel(attempt) {
                return String(attempt && attempt.model || '').trim() || '-';
            },

            // auditAttemptTrack returns the attempt list when it is worth
            // surfacing on the collapsed summary row: a retry/failover happened
            // (more than one attempt) or any single attempt failed. A lone
            // successful attempt is the common case and needs no indicator.
            auditAttemptTrack(entry) {
                const attempts = this.auditAttempts(entry);
                if (attempts.length > 1) return attempts;
                if (attempts.some((attempt) => !(attempt && attempt.success))) return attempts;
                return [];
            },

            auditHasAttemptTrack(entry) {
                return this.auditAttemptTrack(entry).length > 0;
            },

            auditAttemptTrackCount(entry) {
                return this.auditAttempts(entry).length + '×';
            },

            auditAttemptTrackTitle(entry) {
                const attempts = this.auditAttempts(entry);
                const failed = attempts.filter((attempt) => !(attempt && attempt.success)).length;
                const noun = attempts.length === 1 ? 'attempt' : 'attempts';
                const base = attempts.length + ' provider ' + noun;
                return failed > 0 ? base + ' · ' + failed + ' failed' : base;
            },

            auditAttemptSegmentTitle(attempt) {
                if (!attempt) return '';
                const parts = ['#' + Number(attempt.seq || 0)];
                const kind = this.auditAttemptKind(attempt);
                if (kind && kind !== 'attempt') parts.push(kind);
                parts.push(this.auditAttemptStatus(attempt));
                const provider = this.auditAttemptProvider(attempt);
                if (provider && provider !== '-') parts.push(provider);
                const model = this.auditAttemptModel(attempt);
                if (model && model !== '-') parts.push(model);
                parts.push(attempt.success ? 'succeeded' : 'failed');
                return parts.join(' · ');
            },

            // auditAttemptBody resolves the response payload for one attempt.
            // A failed attempt carries the upstream error body in error_message;
            // the successful attempt's body is the final captured response_body.
            // auditAttemptBody is the captured raw upstream response body only:
            // the final response for the successful attempt, or the provider's
            // raw error body for a failed one. The normalized error message is
            // surfaced separately (see auditAttemptErrorMessage), not as a body.
            auditAttemptBody(entry, attempt) {
                if (!attempt) return null;
                if (attempt.success) {
                    const data = entry && entry.data ? entry.data : null;
                    return data && data.response_body != null ? data.response_body : null;
                }
                return attempt.response_body != null && attempt.response_body !== '' ? attempt.response_body : null;
            },

            auditAttemptErrorMessage(attempt) {
                if (!attempt || attempt.success) return '';
                const message = String(attempt.error_message || '').trim();
                const code = String(attempt.error_code || '').trim();
                const type = String(attempt.error_type || '').trim();
                if (message && code) return code + ': ' + message;
                return message || code || type || 'Provider attempt failed';
            },

            auditAttemptHeaders(entry, attempt) {
                if (!attempt) return null;
                if (attempt.success) {
                    const data = entry && entry.data ? entry.data : null;
                    return data ? data.response_headers : null;
                }
                return attempt.response_headers || null;
            },

            auditAttemptStatusCode(attempt) {
                const code = Number(attempt && attempt.status_code);
                return Number.isFinite(code) && code > 0 ? code : null;
            },

            auditAttemptResponsePane(entry, attempt) {
                const success = !!(attempt && attempt.success);
                const data = entry && entry.data ? entry.data : null;
                const body = this.auditAttemptBody(entry, attempt);
                const headers = this.auditAttemptHeaders(entry, attempt);
                const errorMessage = this.auditAttemptErrorMessage(attempt);
                const hasBody = body != null && body !== '';
                const kind = this.auditAttemptKind(attempt);
                // With only one response tab the seq/type/status chips are just
                // noise (it's the whole response); show them only to tell apart
                // multiple attempt tabs.
                const single = this.auditAttempts(entry).length <= 1;

                return {
                    title: 'Response',
                    direction: 'response',
                    seq: single ? 0 : Number(attempt && attempt.seq || 0),
                    kind: single ? '' : (kind === 'attempt' ? '' : kind),
                    statusCode: single ? null : this.auditAttemptStatusCode(attempt),
                    layout: 'split',
                    entry,
                    copyHeaders: headers,
                    copyBody: body,
                    showErrorMessage: !!errorMessage,
                    errorMessage,
                    showHeaders: !!headers,
                    headers,
                    showBody: hasBody,
                    body,
                    showEmpty: !errorMessage && !hasBody && !headers,
                    emptyMessage: 'No response was captured for this attempt.',
                    showTooLarge: !!(success && data && data.response_body_too_big_to_handle),
                    tooLargeMessage: 'Response body was too large to capture.'
                };
            },

            // auditPanes returns the ordered Request/Response panes that back the
            // tab strip: the request, then either the single response or one pane
            // per provider attempt (failover/failed). Each entry pairs a stable
            // tab id with the pane object the audit-pane template renders.
            auditPanes(entry) {
                const panes = [{ id: 'request', pane: this.auditRequestPane(entry) }];
                if (this.auditUsesPerAttemptResponses(entry)) {
                    this.auditAttempts(entry).forEach((attempt) => {
                        panes.push({
                            id: 'response-' + Number(attempt && attempt.seq || 0),
                            pane: this.auditAttemptResponsePane(entry, attempt)
                        });
                    });
                } else {
                    panes.push({ id: 'response', pane: this.auditResponsePane(entry) });
                }
                return panes;
            },

            // auditDefaultPaneTab selects the tab shown first: the last valid
            // (successful) response, falling back to the last attempt when none
            // succeeded, and to the single response otherwise.
            auditDefaultPaneTab(entry) {
                if (!this.auditUsesPerAttemptResponses(entry)) return 'response';
                const attempts = this.auditAttempts(entry);
                let target = null;
                attempts.forEach((attempt) => {
                    if (attempt && attempt.success) target = attempt;
                });
                if (!target) target = attempts[attempts.length - 1];
                return target ? 'response-' + Number(target.seq || 0) : 'request';
            },

            // auditEffectiveTab resolves the active tab id, falling back to the
            // default when nothing is selected yet or the selection no longer
            // exists (e.g. a live entry gained attempts after the first render).
            auditEffectiveTab(active, entry) {
                if (active && this.auditPanes(entry).some((p) => p.id === active)) {
                    return active;
                }
                return this.auditDefaultPaneTab(entry);
            },

            // auditTabKeydown implements roving-tabindex keyboard navigation for the
            // request/response tablist: Left/Up select the previous tab, Right/Down
            // the next (wrapping), Home/End jump to the ends. It returns the tab id
            // to activate (and moves DOM focus there), or null for unhandled keys so
            // the caller can keep the current selection.
            auditTabKeydown(event, entry, currentId) {
                const ids = this.auditPanes(entry).map((p) => p.id);
                if (!ids.length) return null;
                let idx = ids.indexOf(currentId);
                if (idx < 0) idx = 0;
                let next;
                switch (event.key) {
                    case 'ArrowRight':
                    case 'ArrowDown':
                        next = (idx + 1) % ids.length;
                        break;
                    case 'ArrowLeft':
                    case 'ArrowUp':
                        next = (idx - 1 + ids.length) % ids.length;
                        break;
                    case 'Home':
                        next = 0;
                        break;
                    case 'End':
                        next = ids.length - 1;
                        break;
                    default:
                        return null;
                }
                event.preventDefault();
                const tablist = event.currentTarget && event.currentTarget.closest
                    ? event.currentTarget.closest('.audit-pane-tablist')
                    : null;
                if (tablist) {
                    const buttons = tablist.querySelectorAll('.audit-pane-tab');
                    if (buttons[next] && typeof buttons[next].focus === 'function') {
                        buttons[next].focus();
                    }
                }
                return ids[next];
            },

            formatJSON(v) {
                if (v == null || v === undefined || v === '') return 'Not captured';

                if (typeof v === 'string') {
                    const trimmed = v.trim();
                    if ((trimmed.startsWith('{') && trimmed.endsWith('}')) || (trimmed.startsWith('[') && trimmed.endsWith(']'))) {
                        try {
                            return JSON.stringify(JSON.parse(trimmed), null, 2);
                        } catch (_) {
                            return v;
                        }
                    }
                    return v;
                }

                try {
                    return JSON.stringify(v, null, 2);
                } catch (_) {
                    return String(v);
                }
            },

            auditRequestPane(entry) {
                const data = entry && entry.data ? entry.data : null;

                return {
                    title: 'Request',
                    direction: 'request',
                    layout: 'split',
                    entry,
                    copyHeaders: data && data.request_headers,
                    copyBody: data && data.request_body,
                    showErrorMessage: false,
                    errorMessage: null,
                    showHeaders: !!(data && data.request_headers),
                    headers: data && data.request_headers,
                    showBody: !!(data && data.request_body),
                    body: data && data.request_body,
                    bodyCacheRatioLabel: this.auditCacheRatioPillLabel(entry),
                    promptCacheHighlight: this.auditPromptCacheHighlight(entry),
                    showEmpty: !data || (!data.request_headers && !data.request_body),
                    emptyMessage: 'Request details were not captured.',
                    showTooLarge: !!(data && data.request_body_too_big_to_handle),
                    tooLargeMessage: 'Request body was too large to capture.'
                };
            },

            auditResponsePane(entry) {
                const data = entry && entry.data ? entry.data : null;
                const errorMessage = this.auditEntryErrorMessage(entry);

                return {
                    title: 'Response',
                    direction: 'response',
                    layout: 'split',
                    entry,
                    copyHeaders: data && data.response_headers,
                    copyBody: data && data.response_body,
                    showErrorMessage: !!errorMessage,
                    errorMessage,
                    showHeaders: !!(data && data.response_headers),
                    headers: data && data.response_headers,
                    showBody: !!(data && data.response_body),
                    body: data && data.response_body,
                    showEmpty: !data || (!errorMessage && !data.response_headers && !data.response_body),
                    emptyMessage: 'Response details were not captured.',
                    showTooLarge: !!(data && data.response_body_too_big_to_handle),
                    tooLargeMessage: 'Response body was too large to capture.'
                };
            },

            auditUsage(entry) {
                const usage = entry && entry.usage;
                if (!usage || typeof usage !== 'object') return null;
                return usage;
            },

            auditHasCachedTokens(entry) {
                const usage = this.auditUsage(entry);
                return Number(usage && usage.cached_input_tokens || 0) > 0;
            },

            auditCacheSharePercent(entry) {
                const usage = this.auditUsage(entry);
                const inputTokens = Number(usage && usage.input_tokens || 0);
                const cachedTokens = Number(usage && usage.cached_input_tokens || 0);
                if (!Number.isFinite(inputTokens) || inputTokens <= 0 || !Number.isFinite(cachedTokens) || cachedTokens <= 0) {
                    return 0;
                }
                return Math.max(0, Math.min(100, (cachedTokens / inputTokens) * 100));
            },

            auditCacheRatioLabel(entry) {
                const usage = this.auditUsage(entry);
                if (!usage) return '';
                const inputTokens = Number(usage.input_tokens || 0);
                const cachedTokens = Number(usage.cached_input_tokens || 0);
                if (inputTokens <= 0) {
                    return this.formatNumber(cachedTokens) + ' cached';
                }
                return this.auditCacheSharePercent(entry).toFixed(1) + '% cached';
            },

            auditCacheRatioPillLabel(entry) {
                if (!this.auditHasCachedTokens(entry)) return '';
                return this.auditCacheRatioLabel(entry);
            },

            auditPromptCacheHighlight(entry) {
                const usage = this.auditUsage(entry);
                if (!usage || !entry || !entry.data || !entry.data.request_body) return null;

                const estimatedChars = Number(usage.estimated_cached_characters || 0);
                if (!Number.isFinite(estimatedChars) || estimatedChars <= 0) {
                    return null;
                }

                const helper = global.DashboardConversationHelpers;
                if (!helper || typeof helper.extractRequestPromptTextSegments !== 'function') {
                    return null;
                }

                const segments = helper.extractRequestPromptTextSegments(entry.data.request_body);
                if (!Array.isArray(segments) || segments.length === 0) {
                    return null;
                }

                return {
                    characters: estimatedChars,
                    segments
                };
            },

            auditPaneState(pane) {
                const formatJSON = this.formatJSON.bind(this);
                const renderBody = typeof this.renderBodyWithConversationHighlights === 'function'
                    ? this.renderBodyWithConversationHighlights.bind(this)
                    : (_entry, body) => formatJSON(body);
                const createCopyState = () => clipboard
                    ? clipboard.createClipboardButtonState({
                        logPrefix: 'Failed to copy audit payload:'
                    })
                    : {
                        copied: false,
                        error: false,
                        copy() {
                            return Promise.resolve();
                        }
                    };
                const copyBodyState = createCopyState();
                const copyHeadersState = createCopyState();

                const helpers = global.DashboardConversationHelpers;
                const computeRenderedBody = (p) => {
                    if (!p || !p.showBody) return '';
                    if (helpers && typeof helpers.isAudioBody === 'function' && helpers.isAudioBody(p.body)) {
                        return helpers.renderAudioBody(p.body);
                    }
                    return renderBody(p.entry, p.body, { promptCacheHighlight: p.promptCacheHighlight });
                };

                return {
                    pane,
                    formattedHeaders: pane && pane.showHeaders ? formatJSON(pane.headers) : '',
                    renderedBody: computeRenderedBody(pane),
                    copyBodyState,
                    copyHeadersState,
                    copyState: copyBodyState,

                    copyBody() {
                        return this.copyBodyState.copy(this.pane.copyBody, formatJSON);
                    },

                    copyHeaders() {
                        return this.copyHeadersState.copy(this.pane.copyHeaders, formatJSON);
                    },

                    syncPane(nextPane) {
                        this.pane = nextPane;
                        this.formattedHeaders = nextPane && nextPane.showHeaders ? formatJSON(nextPane.headers) : '';
                        this.renderedBody = computeRenderedBody(nextPane);
                    }
                };
            }
        };
    }

    global.dashboardAuditListModule = dashboardAuditListModule;
})(window);
