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
                    const res = await fetch('/admin/api/v1/audit/log?' + qs, request);
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
                    this.auditLog = payload;
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
                if (ns < 1000000) return Math.round(ns / 1000) + ' \u00b5s';
                if (ns < 1000000000) return (ns / 1000000).toFixed(2) + ' ms';
                return (ns / 1000000000).toFixed(2) + ' s';
            },

            handleAuditEntryToggle(event, entry) {
                const detailsEl = event && event.currentTarget;
                if (!detailsEl) return;

                if (detailsEl.open) {
                    this.markAuditEntryExpanded(entry);
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
                    entry,
                    copyHeaders: data && data.request_headers,
                    copyBody: data && data.request_body,
                    showErrorMessage: false,
                    errorMessage: null,
                    showHeaders: !!(data && data.request_headers),
                    headers: data && data.request_headers,
                    showBody: !!(data && data.request_body),
                    body: data && data.request_body,
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

                return {
                    pane,
                    formattedHeaders: pane && pane.showHeaders ? formatJSON(pane.headers) : '',
                    renderedBody: pane && pane.showBody ? renderBody(pane.entry, pane.body) : '',
                    copyBodyState,
                    copyHeadersState,
                    copyState: copyBodyState,

                    copyBody() {
                        return this.copyBodyState.copy(this.pane.copyBody, formatJSON);
                    },

                    copyHeaders() {
                        return this.copyHeadersState.copy(this.pane.copyHeaders, formatJSON);
                    }
                };
            }
        };
    }

    global.dashboardAuditListModule = dashboardAuditListModule;
})(window);
