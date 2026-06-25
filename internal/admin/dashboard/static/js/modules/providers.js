(function(global) {
    const PROVIDER_STATUS_DETAILS_STORAGE_KEY = 'gomodel_provider_status_details_expanded';
    const PROVIDER_STATUS_POLL_MS = 3000;

    // Provider types that have a dedicated docs page at
    // gomodel.enterpilot.io/docs/providers/. Keyed by provider type; the value
    // is the docs slug (identical to the type except opencode_go → opencode-go).
    // Types absent here get no help icon — that is the "doc exists" gate.
    const PROVIDER_DOCS_BASE_URL = 'https://gomodel.enterpilot.io/docs/providers/';
    const PROVIDER_DOC_SLUGS = {
        anthropic: 'anthropic',
        azure: 'azure',
        bailian: 'bailian',
        bedrock: 'bedrock',
        deepseek: 'deepseek',
        gemini: 'gemini',
        opencode_go: 'opencode-go',
        oracle: 'oracle',
        vertex: 'vertex',
        vllm: 'vllm',
        xiaomi: 'xiaomi'
    };

    function browserStorage() {
        try {
            return global.localStorage || null;
        } catch (_) {
            return null;
        }
    }

    function dashboardProvidersModule() {
        return {
            providerStatusDetailsExpanded: false,
            providerStatusPollTimer: null,

            emptyProviderStatus() {
                return {
                    summary: {
                        total: 0,
                        healthy: 0,
                        degraded: 0,
                        unhealthy: 0,
                        overall_status: 'degraded'
                    },
                    providers: []
                };
            },

            initProviderStatusPreferences() {
                this.providerStatusDetailsExpanded = false;
                try {
                    const storage = browserStorage();
                    if (storage) {
                        const stored = storage.getItem(PROVIDER_STATUS_DETAILS_STORAGE_KEY);
                        if (stored === 'true' || stored === 'false') {
                            this.providerStatusDetailsExpanded = stored === 'true';
                        } else {
                            storage.setItem(PROVIDER_STATUS_DETAILS_STORAGE_KEY, 'false');
                        }
                    }
                } catch (_) {
                    // Ignore storage failures; details still start collapsed.
                }
            },

            saveProviderStatusDetailsPreference() {
                const storage = browserStorage();
                if (!storage) {
                    return;
                }
                try {
                    storage.setItem(PROVIDER_STATUS_DETAILS_STORAGE_KEY, this.providerStatusDetailsExpanded ? 'true' : 'false');
                } catch (_) {
                    // Ignore storage failures and keep the in-memory preference active.
                }
            },

            toggleProviderStatusDetails() {
                this.providerStatusDetailsExpanded = !this.providerStatusDetailsExpanded;
                this.saveProviderStatusDetailsPreference();
            },

            providerStatusDetailsToggleLabel() {
                return this.providerStatusDetailsExpanded ? 'Show Details' : 'Hide Details';
            },

            providerStatusNeedsPolling() {
                const providers = this.providerStatus && Array.isArray(this.providerStatus.providers)
                    ? this.providerStatus.providers
                    : [];
                return providers.some((provider) => {
                    const label = String(provider && provider.status_label || '').trim().toLowerCase();
                    return label === 'starting';
                });
            },

            clearProviderStatusRefresh() {
                if (!this.providerStatusPollTimer) {
                    return;
                }
                const clearTimer = global.clearTimeout || (typeof clearTimeout === 'function' ? clearTimeout : null);
                if (typeof clearTimer === 'function') {
                    clearTimer(this.providerStatusPollTimer);
                }
                this.providerStatusPollTimer = null;
            },

            scheduleProviderStatusRefresh() {
                this.clearProviderStatusRefresh();
                if (!this.providerStatusNeedsPolling()) {
                    return;
                }
                const setTimer = global.setTimeout || (typeof setTimeout === 'function' ? setTimeout : null);
                if (typeof setTimer !== 'function') {
                    return;
                }
                this.providerStatusPollTimer = setTimer(() => {
                    this.providerStatusPollTimer = null;
                    if (typeof this.fetchProviderStatus === 'function') {
                        return this.fetchProviderStatus();
                    }
                    return null;
                }, PROVIDER_STATUS_POLL_MS);
            },

            async fetchProviderStatus() {
                let controller = null;
                try {
                    controller = typeof this._startAbortableRequest === 'function'
                        ? this._startAbortableRequest('_providerStatusFetchController')
                        : null;
                    const options = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    if (controller) {
                        options.signal = controller.signal;
                    }
                    const res = await fetch('/admin/providers/status', options);
                    if (options.signal && options.signal.aborted) {
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'provider status', options);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.providerStatus = this.emptyProviderStatus();
                        this.clearProviderStatusRefresh();
                        return;
                    }
                    const payload = await res.json();
                    if (controller && controller.signal.aborted) {
                        return;
                    }
                    this.providerStatus = payload && typeof payload === 'object'
                        ? payload
                        : this.emptyProviderStatus();
                    if (!this.providerStatus.summary) {
                        this.providerStatus.summary = this.emptyProviderStatus().summary;
                    }
                    if (!Array.isArray(this.providerStatus.providers)) {
                        this.providerStatus.providers = [];
                    }
                    this.scheduleProviderStatusRefresh();
                } catch (e) {
                    if (typeof this._isAbortError === 'function' && this._isAbortError(e)) {
                        return;
                    }
                    console.error('Failed to fetch provider status:', e);
                    this.providerStatus = this.emptyProviderStatus();
                    this.clearProviderStatusRefresh();
                } finally {
                    if (typeof this._clearAbortableRequest === 'function') {
                        this._clearAbortableRequest('_providerStatusFetchController', controller);
                    }
                }
            },

            providerStatusSummaryClass() {
                const status = String(this.providerStatus && this.providerStatus.summary && this.providerStatus.summary.overall_status || 'degraded').trim();
                return 'is-' + (status || 'degraded');
            },

            providerStatusBadgeClass(status) {
                const normalized = String(status || 'degraded').trim() || 'degraded';
                return 'is-' + normalized;
            },

            providerStatusRatioText() {
                const summary = this.providerStatus && this.providerStatus.summary ? this.providerStatus.summary : {};
                return String(summary.healthy || 0) + '/' + String(summary.total || 0);
            },

            providerStatusHasIssues() {
                const summary = this.providerStatus && this.providerStatus.summary ? this.providerStatus.summary : {};
                const total = Number(summary.total || 0);
                const healthy = Number(summary.healthy || 0);
                return total > 0 && healthy < total;
            },

            providerStatusSummaryText() {
                const summary = this.providerStatus && this.providerStatus.summary ? this.providerStatus.summary : {};
                const total = Number(summary.total || 0);
                const healthy = Number(summary.healthy || 0);
                if (total === 0) return 'No configured providers';
                if (healthy === total) return 'All configured providers are healthy';
                if (healthy === 0) return 'No configured providers are healthy';
                return String(total - healthy) + ' provider' + (total - healthy === 1 ? '' : 's') +
                    ' need' + (total - healthy === 1 ? 's' : '') + ' attention';
            },

            scrollToProviderStatusSection() {
                const doc = global.document;
                if (!doc || typeof doc.getElementById !== 'function') {
                    return;
                }
                const section = doc.getElementById('provider-status-section');
                if (!section) {
                    return;
                }
                if (typeof section.scrollIntoView === 'function') {
                    section.scrollIntoView({ behavior: 'smooth', block: 'start' });
                }
                if (typeof section.focus === 'function') {
                    section.focus({ preventScroll: true });
                }
            },

            providerLastChecked(provider) {
                if (!provider || !provider.runtime) return '';
                return provider.runtime.last_model_fetch_at ||
                    provider.runtime.last_availability_check_at ||
                    '';
            },

            providerLastCheckedTime(provider) {
                const timestamp = this.providerLastChecked(provider);
                if (!timestamp || typeof this.formatTimestamp !== 'function') {
                    return '-';
                }
                const formatted = this.formatTimestamp(timestamp);
                if (!formatted || formatted === '-') {
                    return '-';
                }
                const parts = String(formatted).split(' ');
                return parts.length > 1 ? parts.slice(1).join(' ') : formatted;
            },

            providerLastCheckedTitle(provider) {
                const timestamp = this.providerLastChecked(provider);
                if (!timestamp) {
                    return '';
                }
                if (typeof this.formatTimestamp === 'function') {
                    return this.formatTimestamp(timestamp);
                }
                return String(timestamp);
            },

            providerTypeLabel(provider) {
                if (!provider) {
                    return '';
                }
                const name = String(provider.name || '').trim();
                const type = String(provider.type || (provider.config && provider.config.type) || '').trim();
                if (!type || type === name) {
                    return '';
                }
                return type;
            },

            // Docs URL for a provider's type, or '' when no provider-specific
            // page exists (the help icon is shown only when this is non-empty).
            // Uses the raw type so it still resolves when the (type) label is
            // hidden because the provider name equals its type.
            providerDocUrl(provider) {
                const type = String(
                    (provider && (provider.type || (provider.config && provider.config.type))) || ''
                ).trim().toLowerCase();
                const slug = type ? PROVIDER_DOC_SLUGS[type] : '';
                return slug ? PROVIDER_DOCS_BASE_URL + slug : '';
            },

            providerRetrySummary(provider) {
                const retry = provider && provider.config && provider.config.resilience
                    ? provider.config.resilience.retry
                    : null;
                if (!retry) return '-';
                return String(retry.max_retries) + ' retries, ' +
                    retry.initial_backoff + ' initial, ' +
                    retry.max_backoff + ' max, factor ' +
                    retry.backoff_factor + ', jitter ' +
                    retry.jitter_factor;
            },

            providerCircuitBreakerSummary(provider) {
                const breaker = provider && provider.config && provider.config.resilience
                    ? provider.config.resilience.circuit_breaker
                    : null;
                if (!breaker) return '-';
                return String(breaker.failure_threshold) + ' fail, ' +
                    String(breaker.success_threshold) + ' success, ' +
                    breaker.timeout + ' timeout';
            },

            providerModelsSummary(provider) {
                const models = provider && provider.config && Array.isArray(provider.config.models)
                    ? provider.config.models.filter(Boolean)
                    : [];
                if (models.length === 0) return 'Automatic';
                return models.join(', ');
            }
        };
    }

    global.dashboardProvidersModule = dashboardProvidersModule;
})(typeof window !== 'undefined' ? window : globalThis);
