(function(global) {
    function dashboardUsageModule() {
        return {
            emptyUsageSummary() {
                return {
                    total_requests: 0,
                    total_input_tokens: 0,
                    total_output_tokens: 0,
                    total_tokens: 0,
                    total_input_cost: null,
                    total_output_cost: null,
                    total_cost: null
                };
            },

            emptyCacheOverview() {
                return {
                    summary: {
                        total_hits: 0,
                        exact_hits: 0,
                        semantic_hits: 0,
                        total_input_tokens: 0,
                        total_output_tokens: 0,
                        total_tokens: 0,
                        total_saved_cost: null
                    },
                    daily: []
                };
            },

            cacheAnalyticsEnabled() {
                return typeof this.workflowRuntimeBooleanFlag === 'function'
                    ? this.workflowRuntimeBooleanFlag('CACHE_ENABLED', false)
                    : false;
            },

            _usageQueryStr() {
                if (this.customStartDate && this.customEndDate) {
                    return 'start_date=' + this._formatDate(this.customStartDate) +
                        '&end_date=' + this._formatDate(this.customEndDate);
                }
                return 'days=' + this.days;
            },

            async fetchCacheOverview() {
                if (!this.cacheAnalyticsEnabled()) {
                    this.cacheOverview = this.emptyCacheOverview();
                    if (this.page === 'overview') this.renderChart();
                    return;
                }

                let controller = null;
                try {
                    controller = typeof this._startAbortableRequest === 'function'
                        ? this._startAbortableRequest('_cacheOverviewFetchController')
                        : null;
                    const options = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    if (controller) {
                        options.signal = controller.signal;
                    }
                    let queryStr;
                    if (this.customStartDate && this.customEndDate) {
                        queryStr = 'start_date=' + this._formatDate(this.customStartDate) +
                            '&end_date=' + this._formatDate(this.customEndDate);
                    } else {
                        queryStr = 'days=' + this.days;
                    }
                    queryStr += '&interval=' + this.interval;

                    const res = await fetch('/admin/api/v1/cache/overview?' + queryStr, options);
                    const handled = this.handleFetchResponse(res, 'cache overview', options);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.cacheOverview = this.emptyCacheOverview();
                        return;
                    }
                    const payload = await res.json();
                    if (controller && controller.signal.aborted) {
                        return;
                    }
                    this.cacheOverview = payload && typeof payload === 'object' ? payload : this.emptyCacheOverview();
                    if (!this.cacheOverview.summary) {
                        this.cacheOverview.summary = this.emptyCacheOverview().summary;
                    }
                    if (!Array.isArray(this.cacheOverview.daily)) {
                        this.cacheOverview.daily = [];
                    }
                    if (this.page === 'overview') this.renderChart();
                } catch (e) {
                    if (typeof this._isAbortError === 'function' && this._isAbortError(e)) {
                        return;
                    }
                    console.error('Failed to fetch cache overview:', e);
                    this.cacheOverview = this.emptyCacheOverview();
                } finally {
                    if (typeof this._clearAbortableRequest === 'function') {
                        this._clearAbortableRequest('_cacheOverviewFetchController', controller);
                    }
                }
            },

            async fetchUsage() {
                let controller = null;
                try {
                    controller = typeof this._startAbortableRequest === 'function'
                        ? this._startAbortableRequest('_usageFetchController')
                        : null;
                    const options = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    if (controller) {
                        options.signal = controller.signal;
                    }
                    let queryStr;
                    if (this.customStartDate && this.customEndDate) {
                        queryStr = 'start_date=' + this._formatDate(this.customStartDate) +
                            '&end_date=' + this._formatDate(this.customEndDate);
                    } else {
                        queryStr = 'days=' + this.days;
                    }
                    queryStr += '&interval=' + this.interval;

                    const [summaryRes, dailyRes] = await Promise.all([
                        fetch('/admin/api/v1/usage/summary?' + queryStr, options),
                        fetch('/admin/api/v1/usage/daily?' + queryStr, options)
                    ]);

                    const summaryHandled = this.handleFetchResponse(summaryRes, 'usage summary', options);
                    const dailyHandled = this.handleFetchResponse(dailyRes, 'usage daily', options);
                    if ((typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(summaryHandled)) ||
                        (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(dailyHandled))) {
                        return;
                    }
                    if (!summaryHandled || !dailyHandled) {
                        this.summary = this.emptyUsageSummary();
                        this.daily = [];
                        this.renderChart();
                        return;
                    }

                    const [summary, daily] = await Promise.all([
                        summaryRes.json(),
                        dailyRes.json()
                    ]);
                    if (controller && controller.signal.aborted) {
                        return;
                    }
                    this.summary = summary;
                    this.daily = daily;
                    this.renderChart();
                    if (this.cacheAnalyticsEnabled()) {
                        this.fetchCacheOverview();
                    } else {
                        this.cacheOverview = this.emptyCacheOverview();
                    }
                    if (this.page === 'usage') this.fetchUsagePage();
                    if (this.page === 'audit-logs') this.fetchAuditLog(true);
                } catch (e) {
                    if (typeof this._isAbortError === 'function' && this._isAbortError(e)) {
                        return;
                    }
                    console.error('Failed to fetch usage:', e);
                } finally {
                    if (typeof this._clearAbortableRequest === 'function') {
                        this._clearAbortableRequest('_usageFetchController', controller);
                    }
                }
            },

            async fetchUsagePage() {
                const requests = [this.fetchModelUsage(), this.fetchUserPathUsage(), this.fetchUsageLog(true)];
                if (this.cacheAnalyticsEnabled()) {
                    requests.push(this.fetchCacheOverview());
                }
                await Promise.all(requests);
                this.renderBarChart();
                this.renderUserPathChart();
            },

            async fetchModelUsage() {
                let controller = null;
                try {
                    controller = typeof this._startAbortableRequest === 'function'
                        ? this._startAbortableRequest('_modelUsageFetchController')
                        : null;
                    const options = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    if (controller) {
                        options.signal = controller.signal;
                    }
                    const res = await fetch('/admin/api/v1/usage/models?' + this._usageQueryStr(), options);
                    const handled = this.handleFetchResponse(res, 'usage models', options);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.modelUsage = [];
                        return;
                    }
                    const payload = await res.json();
                    if (controller && controller.signal.aborted) {
                        return;
                    }
                    this.modelUsage = payload;
                } catch (e) {
                    if (typeof this._isAbortError === 'function' && this._isAbortError(e)) {
                        return;
                    }
                    console.error('Failed to fetch model usage:', e);
                    this.modelUsage = [];
                } finally {
                    if (typeof this._clearAbortableRequest === 'function') {
                        this._clearAbortableRequest('_modelUsageFetchController', controller);
                    }
                }
            },

            async fetchUserPathUsage() {
                let controller = null;
                try {
                    controller = typeof this._startAbortableRequest === 'function'
                        ? this._startAbortableRequest('_userPathUsageFetchController')
                        : null;
                    const options = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    if (controller) {
                        options.signal = controller.signal;
                    }
                    const res = await fetch('/admin/api/v1/usage/user-paths?' + this._usageQueryStr(), options);
                    const handled = this.handleFetchResponse(res, 'usage user paths', options);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.userPathUsage = [];
                        return;
                    }
                    const payload = await res.json();
                    if (controller && controller.signal.aborted) {
                        return;
                    }
                    this.userPathUsage = Array.isArray(payload) ? payload : [];
                } catch (e) {
                    if (typeof this._isAbortError === 'function' && this._isAbortError(e)) {
                        return;
                    }
                    console.error('Failed to fetch usage by user path:', e);
                    this.userPathUsage = [];
                } finally {
                    if (typeof this._clearAbortableRequest === 'function') {
                        this._clearAbortableRequest('_userPathUsageFetchController', controller);
                    }
                }
            },

            async fetchUsageLog(resetOffset) {
                let controller = null;
                try {
                    controller = typeof this._startAbortableRequest === 'function'
                        ? this._startAbortableRequest('_usageLogFetchController')
                        : null;
                    const options = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    if (controller) {
                        options.signal = controller.signal;
                    }
                    if (resetOffset) this.usageLog.offset = 0;
                    let qs = this._usageQueryStr();
                    qs += '&limit=' + this.usageLog.limit + '&offset=' + this.usageLog.offset;
                    if (this.usageLogSearch) qs += '&search=' + encodeURIComponent(this.usageLogSearch);
                    if (this.usageLogModel) qs += '&model=' + encodeURIComponent(this.usageLogModel);
                    if (this.usageLogProvider) qs += '&provider=' + encodeURIComponent(this.usageLogProvider);
                    if (this.usageLogUserPath) qs += '&user_path=' + encodeURIComponent(this.usageLogUserPath);

                    const res = await fetch('/admin/api/v1/usage/log?' + qs, options);
                    const handled = this.handleFetchResponse(res, 'usage log', options);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.usageLog = { entries: [], total: 0, limit: 50, offset: 0 };
                        return;
                    }
                    const payload = await res.json();
                    if (controller && controller.signal.aborted) {
                        return;
                    }
                    this.usageLog = payload;
                    if (!this.usageLog.entries) this.usageLog.entries = [];
                } catch (e) {
                    if (typeof this._isAbortError === 'function' && this._isAbortError(e)) {
                        return;
                    }
                    console.error('Failed to fetch usage log:', e);
                    this.usageLog = { entries: [], total: 0, limit: 50, offset: 0 };
                } finally {
                    if (typeof this._clearAbortableRequest === 'function') {
                        this._clearAbortableRequest('_usageLogFetchController', controller);
                    }
                }
            },

            toggleUsageMode(mode) {
                this.usageMode = mode;
                const url = mode === 'costs' ? '/admin/dashboard/usage/costs' : '/admin/dashboard/usage';
                history.pushState(null, '', url);
                this.renderBarChart();
                this.renderUserPathChart();
            },

            usageLogNextPage() {
                if (this.usageLog.offset + this.usageLog.limit < this.usageLog.total) {
                    this.usageLog.offset += this.usageLog.limit;
                    this.fetchUsageLog(false);
                }
            },

            usageLogPrevPage() {
                if (this.usageLog.offset > 0) {
                    this.usageLog.offset = Math.max(0, this.usageLog.offset - this.usageLog.limit);
                    this.fetchUsageLog(false);
                }
            },

            usageLogModelOptions() {
                const set = new Set();
                this.modelUsage.forEach((m) => { set.add(m.model); });
                return [...set].sort();
            },

            usageLogProviderOptions() {
                const set = new Set();
                this.modelUsage.forEach((m) => {
                    const provider = typeof this.providerDisplayValue === 'function'
                        ? this.providerDisplayValue(m)
                        : String((m && (m.provider_name || m.provider)) || '').trim();
                    if (provider) {
                        set.add(provider);
                    }
                });
                return [...set].sort();
            }
        };
    }

    global.dashboardUsageModule = dashboardUsageModule;
})(window);
