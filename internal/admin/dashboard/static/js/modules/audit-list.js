(function(global) {
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

                return {
                    title: 'Response',
                    entry,
                    copyBody: data && data.response_body,
                    showErrorMessage: !!(data && data.error_message),
                    errorMessage: data && data.error_message,
                    showHeaders: !!(data && data.response_headers),
                    headers: data && data.response_headers,
                    showBody: !!(data && data.response_body),
                    body: data && data.response_body,
                    showEmpty: !data || (!data.error_message && !data.response_headers && !data.response_body),
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

                return {
                    pane,
                    formattedHeaders: pane && pane.showHeaders ? formatJSON(pane.headers) : '',
                    renderedBody: pane && pane.showBody ? renderBody(pane.entry, pane.body) : '',
                    copyState: clipboard
                        ? clipboard.createClipboardButtonState({
                            logPrefix: 'Failed to copy audit payload:'
                        })
                        : {
                            copied: false,
                            error: false,
                            copy() {
                                return Promise.resolve();
                            }
                        },

                    copyBody() {
                        return this.copyState.copy(this.pane.copyBody, formatJSON);
                    }
                };
            }
        };
    }

    global.dashboardAuditListModule = dashboardAuditListModule;
})(window);
