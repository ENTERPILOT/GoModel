(function(global) {
    const PROVIDER_STATUS_DETAILS_STORAGE_KEY = 'gomodel_provider_status_details_expanded';
    const PROVIDER_CARD_OVERRIDES_STORAGE_KEY = 'gomodel_provider_card_expanded_overrides';
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
        'bedrock-mantle': 'bedrock-mantle',
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
            // Per-card expand/collapse overrides keyed by provider name; cards
            // without an entry follow providerStatusDetailsExpanded.
            providerCardOverrides: {},
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
                this.providerCardOverrides = {};
                try {
                    const storage = browserStorage();
                    if (storage) {
                        const stored = storage.getItem(PROVIDER_STATUS_DETAILS_STORAGE_KEY);
                        if (stored === 'true' || stored === 'false') {
                            this.providerStatusDetailsExpanded = stored === 'true';
                        } else {
                            storage.setItem(PROVIDER_STATUS_DETAILS_STORAGE_KEY, 'false');
                        }
                        const overrides = JSON.parse(storage.getItem(PROVIDER_CARD_OVERRIDES_STORAGE_KEY) || '{}');
                        if (overrides && typeof overrides === 'object' && !Array.isArray(overrides)) {
                            this.providerCardOverrides = overrides;
                        }
                    }
                } catch (_) {
                    // Ignore storage failures; details still start collapsed.
                }
            },

            saveProviderCardOverrides() {
                const storage = browserStorage();
                if (!storage) {
                    return;
                }
                try {
                    storage.setItem(PROVIDER_CARD_OVERRIDES_STORAGE_KEY, JSON.stringify(this.providerCardOverrides));
                } catch (_) {
                    // Ignore storage failures and keep the in-memory state active.
                }
            },

            providerCardExpanded(provider) {
                const name = provider && provider.name ? String(provider.name) : '';
                if (name && Object.prototype.hasOwnProperty.call(this.providerCardOverrides, name)) {
                    return this.providerCardOverrides[name] === true;
                }
                return this.providerStatusDetailsExpanded;
            },

            toggleProviderCard(provider) {
                const name = provider && provider.name ? String(provider.name) : '';
                if (!name) {
                    return;
                }
                const overrides = Object.assign({}, this.providerCardOverrides);
                overrides[name] = !this.providerCardExpanded(provider);
                this.providerCardOverrides = overrides;
                this.saveProviderCardOverrides();
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
                // The section-wide switch acts as a master control: drop any
                // per-card overrides so every card follows it again.
                this.providerCardOverrides = {};
                this.saveProviderStatusDetailsPreference();
                this.saveProviderCardOverrides();
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
                const fetchAt = provider.runtime.last_model_fetch_at || '';
                const availabilityAt = provider.runtime.last_availability_check_at || '';
                if (!fetchAt) return availabilityAt;
                if (!availabilityAt) return fetchAt;
                // Whichever check ran most recently; an unparsable timestamp
                // falls back to the model fetch side.
                return Date.parse(availabilityAt) > Date.parse(fetchAt) ? availabilityAt : fetchAt;
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
            },

            // Tooltip for the status pill: the classification reason plus the
            // last error, so the collapsed card already explains its state.
            providerStatusPillTitle(provider) {
                if (!provider) return '';
                const parts = [];
                if (provider.status_reason) {
                    parts.push(String(provider.status_reason));
                }
                if (provider.last_error) {
                    parts.push('Last error: ' + String(provider.last_error));
                }
                return parts.join('\n\n');
            },

            providerRequestHealth(provider) {
                const requestHealth = provider && provider.request_health;
                return requestHealth && typeof requestHealth === 'object' ? requestHealth : null;
            },

            providerBreakerState(provider) {
                const requestHealth = this.providerRequestHealth(provider);
                return requestHealth ? String(requestHealth.circuit_state || '').trim() : '';
            },

            providerBreakerStateLabel(provider) {
                const state = this.providerBreakerState(provider);
                if (!state) return '';
                return state.charAt(0).toUpperCase() + state.slice(1);
            },

            providerBreakerStateClass(provider) {
                const state = this.providerBreakerState(provider);
                if (state === 'open') return 'is-unhealthy';
                if (state === 'half-open') return 'is-degraded';
                return 'is-healthy';
            },

            providerRecentTrafficSummary(provider) {
                const requestHealth = this.providerRequestHealth(provider);
                if (!requestHealth) return '';
                const requests = Number(requestHealth.requests || 0);
                const errors = Number(requestHealth.errors || 0);
                const minutes = Math.round(Number(requestHealth.window_seconds || 0) / 60);
                const windowText = minutes > 0 ? 'last ' + minutes + ' min' : 'recent';
                return String(requests) + ' request' + (requests === 1 ? '' : 's') +
                    ' · ' + String(errors) + ' error' + (errors === 1 ? '' : 's') +
                    ' (' + windowText + ')';
            },

            providerHealthModels(provider) {
                const requestHealth = this.providerRequestHealth(provider);
                return requestHealth && Array.isArray(requestHealth.models) ? requestHealth.models : [];
            },

            providerHealthModelStats(model) {
                if (!model) return '';
                return String(Number(model.errors || 0)) + '/' + String(Number(model.requests || 0)) + ' failed';
            },

            // Tooltip with the model's most recent failure; empty when the
            // model has no windowed errors.
            providerHealthModelTitle(model) {
                const lastError = model && model.last_error;
                if (!lastError || !lastError.message) return '';
                const prefix = lastError.status_code ? 'HTTP ' + String(lastError.status_code) + ': ' : '';
                return prefix + lastError.message;
            }
        };
    }

    global.dashboardProvidersModule = dashboardProvidersModule;
})(typeof window !== 'undefined' ? window : globalThis);
