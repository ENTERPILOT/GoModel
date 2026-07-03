(function(global) {
    function dashboardUsageModule() {
        function dashboardModulePath(path) {
            if (typeof window !== 'undefined' && typeof window.gomodelPath === 'function') {
                return window.gomodelPath(path);
            }
            return path;
        }

        function costSource(entry) {
            return String((entry && entry.cost_source) || '').trim();
        }

        return {
            emptyUsageSummary() {
                return {
                    total_requests: 0,
                    total_input_tokens: 0,
                    total_output_tokens: 0,
                    total_tokens: 0,
                    uncached_input_tokens: 0,
                    cached_input_tokens: 0,
                    cache_write_input_tokens: 0,
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

            summaryTotalTokens() {
                const summary = this.summary || {};
                if (summary.total_tokens !== null && summary.total_tokens !== undefined) {
                    const total = Number(summary.total_tokens);
                    if (Number.isFinite(total)) {
                        return total;
                    }
                }
                const input = Number(summary.total_input_tokens || 0);
                const output = Number(summary.total_output_tokens || 0);
                return (Number.isFinite(input) ? input : 0) + (Number.isFinite(output) ? output : 0);
            },

            // Local response-cache hits over the period. The summary endpoint
            // counts provider (uncached-mode) requests only, so hits live in the
            // cache overview. Zero when cache analytics is off (overview unfetched).
            summaryCacheHits() {
                if (!this.cacheAnalyticsEnabled()) return 0;
                const cacheSummary = this.cacheOverview && this.cacheOverview.summary ? this.cacheOverview.summary : {};
                const hits = Number(cacheSummary.total_hits || 0);
                return Number.isFinite(hits) && hits > 0 ? hits : 0;
            },

            // Total requests including local cache hits (provider requests + hits).
            summaryTotalRequests() {
                const requests = Number((this.summary && this.summary.total_requests) || 0);
                return (Number.isFinite(requests) ? requests : 0) + this.summaryCacheHits();
            },

            summaryTotalRequestsTitle() {
                const hits = this.summaryCacheHits();
                if (hits <= 0) return '';
                const provider = this.summaryTotalRequests() - hits;
                return this.formatNumber(provider) + ' to providers + ' + this.formatNumber(hits) + ' from cache';
            },

            cacheOverviewTotalTokens() {
                const summary = this.cacheOverview && this.cacheOverview.summary ? this.cacheOverview.summary : {};
                const input = Number(summary.total_input_tokens || 0);
                const output = Number(summary.total_output_tokens || 0);
                return (Number.isFinite(input) ? input : 0) + (Number.isFinite(output) ? output : 0);
            },

            cacheAnalyticsEnabled() {
                return typeof this.workflowRuntimeBooleanFlag === 'function'
                    ? this.workflowRuntimeBooleanFlag('CACHE_ENABLED', false)
                    : false;
            },

            // --- Cache meter (overview) ---
            // Splits the selected period's input tokens into three buckets that
            // sum to 100%: not-cached, locally-cached (GoModel response cache),
            // and prompt-cached (provider cache reads). The provider split comes
            // from /admin/usage/summary (uncached/cached/cache-write over provider
            // rows); the local slice from /admin/cache/overview. Both already
            // refresh with the period, so the meter follows the picker for free.
            cacheMeterRawSegments() {
                const positive = (value) => {
                    const n = Number(value || 0);
                    return Number.isFinite(n) && n > 0 ? n : 0;
                };
                const summary = this.summary || {};
                const uncached = positive(summary.uncached_input_tokens);
                const promptCached = positive(summary.cached_input_tokens);
                const cacheWrite = positive(summary.cache_write_input_tokens);
                const cacheSummary = this.cacheOverview && this.cacheOverview.summary ? this.cacheOverview.summary : {};
                const locallyCached = this.cacheAnalyticsEnabled() ? positive(cacheSummary.total_input_tokens) : 0;
                return [
                    {
                        key: 'uncached',
                        label: 'Regular',
                        tokens: uncached + cacheWrite,
                        colorVar: '--cache-meter-uncached',
                        note: cacheWrite > 0 ? 'Includes ' + this.formatNumber(cacheWrite) + ' cache-write tokens' : ''
                    },
                    {
                        key: 'prompt',
                        label: 'Prompt cached',
                        tokens: promptCached,
                        colorVar: '--cache-meter-prompt',
                        note: 'Provider prompt-cache reads'
                    },
                    {
                        key: 'local',
                        label: 'Locally cached',
                        tokens: locallyCached,
                        colorVar: '--cache-meter-local',
                        note: 'Served from GoModel response cache'
                    }
                ];
            },

            cacheMeterTotal() {
                return this.cacheMeterRawSegments().reduce((sum, seg) => sum + seg.tokens, 0);
            },

            cacheMeterVisible() {
                return this.cacheMeterTotal() > 0;
            },

            // The three fixed categories with integer percentages. Largest-
            // remainder rounding keeps the percents summing to exactly 100 when
            // there is data; with no usage every category is 0% so the meter can
            // still render as an empty key. Used by the legend (all three shown)
            // and, filtered to non-zero, by the bar segments.
            cacheMeterCategories() {
                const categories = this.cacheMeterRawSegments();
                const total = categories.reduce((sum, seg) => sum + seg.tokens, 0);
                if (total <= 0) {
                    return categories.map((seg) => Object.assign({}, seg, { pct: 0 }));
                }
                const withPct = categories.map((seg) => {
                    const exact = (seg.tokens / total) * 100;
                    const floor = Math.floor(exact);
                    return Object.assign({}, seg, { pct: floor, remainder: exact - floor });
                });
                let leftover = 100 - withPct.reduce((sum, seg) => sum + seg.pct, 0);
                withPct
                    .map((seg, index) => ({ index, remainder: seg.remainder, tokens: seg.tokens }))
                    .filter((entry) => entry.tokens > 0)
                    .sort((a, b) => b.remainder - a.remainder)
                    .forEach((entry) => {
                        if (leftover > 0) {
                            withPct[entry.index].pct += 1;
                            leftover -= 1;
                        }
                    });
                return withPct;
            },

            // Non-zero categories only, so the bar never renders zero-width
            // slivers. Empty when there is no usage.
            cacheMeterSegments() {
                return this.cacheMeterCategories().filter((seg) => seg.tokens > 0);
            },

            cacheMeterSegmentTitle(segment) {
                const parts = [segment.label + ': ' + this.formatNumber(segment.tokens) + ' input tokens (' + segment.pct + '%)'];
                if (segment.note) parts.push(segment.note);
                return parts.join('\n');
            },

            cacheMeterAriaLabel() {
                const parts = this.cacheMeterSegments().map((seg) => seg.label + ' ' + seg.pct + '%');
                return 'Cache breakdown of input tokens — ' + (parts.length ? parts.join(', ') : 'no data');
            },

            cacheOverviewVisible() {
                return this.page === 'overview' || this.page === 'usage';
            },

            _usageQueryStr() {
                if (this.customStartDate && this.customEndDate) {
                    return 'start_date=' + this._formatDate(this.customStartDate) +
                        '&end_date=' + this._formatDate(this.customEndDate);
                }
                return 'days=' + this.days;
            },

            // Page-level data filters, applied to every usage-page request so
            // charts, cache cards, and the request log describe the same
            // filtered slice of traffic.
            _usageFilterQueryStr() {
                let qs = '';
                if (this.usageFilterModel) qs += '&model=' + encodeURIComponent(this.usageFilterModel);
                if (this.usageFilterProvider) qs += '&provider=' + encodeURIComponent(this.usageFilterProvider);
                if (this.usageFilterLabel) qs += '&label=' + encodeURIComponent(this.usageFilterLabel);
                if (this.usageFilterUserPath) qs += '&user_path=' + encodeURIComponent(this.usageFilterUserPath);
                return qs;
            },

            onUsageFilterChanged() {
                this.fetchUsagePage();
            },

            async fetchCacheOverview() {
                if (!this.cacheOverviewVisible()) {
                    return;
                }
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
                    let queryStr = this._usageQueryStr() + '&interval=' + this.interval;
                    // The usage page filters its cache cards along with the
                    // rest of the page; the overview page stays unfiltered.
                    if (this.page === 'usage') {
                        queryStr += this._usageFilterQueryStr();
                    }

                    const res = await fetch('/admin/cache/overview?' + queryStr, options);
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
                        fetch('/admin/usage/summary?' + queryStr, options),
                        fetch('/admin/usage/daily?' + queryStr, options)
                    ]);

                    const summaryHandled = this.handleFetchResponse(summaryRes, 'usage summary', options);
                    const dailyHandled = this.handleFetchResponse(dailyRes, 'usage daily', options);
                    if ((typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(summaryHandled)) ||
                        (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(dailyHandled))) {
                        return;
                    }
                    if (!summaryHandled || !dailyHandled) {
                        // Reset cache overview too: Total Requests and the cache
                        // meter derive from it, so leaving it stale would show the
                        // previous period's cache hits next to the empty summary.
                        this.summary = this.emptyUsageSummary();
                        this.daily = [];
                        this.cacheOverview = this.emptyCacheOverview();
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
                    // Clear the previous period's cache overview before the first
                    // render so the cache meter and Total Requests don't briefly
                    // mix it with the new summary while it reloads.
                    if (this.cacheOverviewVisible()) {
                        this.cacheOverview = this.emptyCacheOverview();
                    }
                    this.renderChart();
                    if (this.cacheOverviewVisible() && this.cacheAnalyticsEnabled()) {
                        this.fetchCacheOverview();
                    }
                    if (this.page === 'usage') this.fetchUsagePage();
                    if (this.page === 'audit-logs') this.fetchAuditLog(true);
                } catch (e) {
                    if (typeof this._isAbortError === 'function' && this._isAbortError(e)) {
                        return;
                    }
                    console.error('Failed to fetch usage:', e);
                    // Match the handled-failure branch: a fetch()/json() rejection
                    // must not leave the previous period's data rendered.
                    this.summary = this.emptyUsageSummary();
                    this.daily = [];
                    this.cacheOverview = this.emptyCacheOverview();
                    this.renderChart();
                } finally {
                    if (typeof this._clearAbortableRequest === 'function') {
                        this._clearAbortableRequest('_usageFetchController', controller);
                    }
                }
            },

            async fetchUsagePage() {
                const requests = [this.fetchUsagePageSummary(), this.fetchModelUsage(), this.fetchUserPathUsage(), this.fetchLabelUsage(), this.fetchUsageLog(true)];
                if (this.cacheAnalyticsEnabled()) {
                    requests.push(this.fetchCacheOverview());
                }
                await Promise.all(requests);
                this.renderBarChart();
                this.renderUserPathChart();
                this.renderLabelChart();
            },

            // Filtered summary backing the usage-page stat cards. Kept separate
            // from the overview page's unfiltered `summary` state.
            async fetchUsagePageSummary() {
                let controller = null;
                try {
                    controller = typeof this._startAbortableRequest === 'function'
                        ? this._startAbortableRequest('_usagePageSummaryFetchController')
                        : null;
                    const options = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    if (controller) {
                        options.signal = controller.signal;
                    }
                    const res = await fetch('/admin/usage/summary?' + this._usageQueryStr() + this._usageFilterQueryStr(), options);
                    const handled = this.handleFetchResponse(res, 'usage page summary', options);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.usageSummary = this.emptyUsageSummary();
                        return;
                    }
                    const payload = await res.json();
                    if (controller && controller.signal.aborted) {
                        return;
                    }
                    this.usageSummary = payload && typeof payload === 'object' ? payload : this.emptyUsageSummary();
                } catch (e) {
                    if (typeof this._isAbortError === 'function' && this._isAbortError(e)) {
                        return;
                    }
                    console.error('Failed to fetch usage page summary:', e);
                    this.usageSummary = this.emptyUsageSummary();
                } finally {
                    if (typeof this._clearAbortableRequest === 'function') {
                        this._clearAbortableRequest('_usagePageSummaryFetchController', controller);
                    }
                }
            },

            // Requests over the period and filters, including local cache hits
            // (the cache overview follows the same filters on this page), so
            // the count matches the log's default all-mode view.
            usagePageCacheHits() {
                if (!this.cacheAnalyticsEnabled()) return 0;
                const summary = (this.cacheOverview && this.cacheOverview.summary) || {};
                const hits = Number(summary.total_hits || 0);
                return Number.isFinite(hits) && hits > 0 ? hits : 0;
            },

            usagePageTotalRequests() {
                const requests = Number((this.usageSummary && this.usageSummary.total_requests) || 0);
                return (Number.isFinite(requests) ? requests : 0) + this.usagePageCacheHits();
            },

            usagePageRequestsTitle() {
                const hits = this.usagePageCacheHits();
                if (hits <= 0) return '';
                const provider = this.usagePageTotalRequests() - hits;
                return this.formatNumber(provider) + ' to providers + ' + this.formatNumber(hits) + ' from cache';
            },

            usagePageCostTitle() {
                const summary = this.usageSummary || {};
                if (summary.total_input_cost === null || summary.total_input_cost === undefined) return '';
                return this.formatCost(summary.total_input_cost) + ' input + ' + this.formatCost(summary.total_output_cost) + ' output';
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
                    const res = await fetch('/admin/usage/models?' + this._usageQueryStr() + this._usageFilterQueryStr(), options);
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
                    const res = await fetch('/admin/usage/user-paths?' + this._usageQueryStr() + this._usageFilterQueryStr(), options);
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

            async fetchLabelUsage() {
                let controller = null;
                try {
                    controller = typeof this._startAbortableRequest === 'function'
                        ? this._startAbortableRequest('_labelUsageFetchController')
                        : null;
                    const options = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    if (controller) {
                        options.signal = controller.signal;
                    }
                    const res = await fetch('/admin/usage/labels?' + this._usageQueryStr() + this._usageFilterQueryStr(), options);
                    const handled = this.handleFetchResponse(res, 'usage labels', options);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.labelUsage = [];
                        return;
                    }
                    const payload = await res.json();
                    if (controller && controller.signal.aborted) {
                        return;
                    }
                    this.labelUsage = Array.isArray(payload) ? payload : [];
                } catch (e) {
                    if (typeof this._isAbortError === 'function' && this._isAbortError(e)) {
                        return;
                    }
                    console.error('Failed to fetch usage by label:', e);
                    this.labelUsage = [];
                } finally {
                    if (typeof this._clearAbortableRequest === 'function') {
                        this._clearAbortableRequest('_labelUsageFetchController', controller);
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
                    let qs = this._usageQueryStr() + this._usageFilterQueryStr();
                    qs += '&limit=' + this.usageLog.limit + '&offset=' + this.usageLog.offset;
                    qs += '&cache_mode=' + (this.usageLogHideCached ? 'uncached' : 'all');
                    if (this.usageLogSearch) qs += '&search=' + encodeURIComponent(this.usageLogSearch);

                    const res = await fetch('/admin/usage/log?' + qs, options);
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
                    if (typeof this.renderIconsAfterUpdate === 'function') {
                        this.renderIconsAfterUpdate();
                    }
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
                history.pushState(null, '', dashboardModulePath(url));
                this.renderBarChart();
                this.renderUserPathChart();
                this.renderLabelChart();
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

            // Filter options derive from the currently filtered aggregates
            // (faceted drill-down); the active selection stays listed so the
            // select never silently shows "All" while a filter is applied.
            usageFilterModelOptions() {
                const set = new Set();
                this.modelUsage.forEach((m) => { set.add(m.model); });
                if (this.usageFilterModel) set.add(this.usageFilterModel);
                return [...set].sort();
            },

            usageFilterProviderOptions() {
                const set = new Set();
                this.modelUsage.forEach((m) => {
                    const provider = typeof this.providerDisplayValue === 'function'
                        ? this.providerDisplayValue(m)
                        : String((m && (m.provider_name || m.provider)) || '').trim();
                    if (provider) {
                        set.add(provider);
                    }
                });
                if (this.usageFilterProvider) set.add(this.usageFilterProvider);
                return [...set].sort();
            },

            usageFilterLabelOptions() {
                const set = new Set();
                (this.labelUsage || []).forEach((l) => {
                    if (l && l.label) set.add(l.label);
                });
                if (this.usageFilterLabel) set.add(this.usageFilterLabel);
                return [...set].sort();
            },

            entryLabels(entry) {
                return Array.isArray(entry && entry.labels) ? entry.labels : [];
            },

            // The Labels column only appears when labels are in play: the
            // period has by-label aggregates, a label filter is active, or the
            // current log page carries labelled entries (e.g. cached-only
            // labelled traffic, which the uncached-mode aggregates omit).
            usageLogHasLabels() {
                if ((this.labelUsage || []).length > 0 || this.usageFilterLabel) return true;
                const entries = (this.usageLog && this.usageLog.entries) || [];
                return entries.some((entry) => this.entryLabels(entry).length > 0);
            },

            // Chip click: filter the whole page by the label, or clear the
            // filter when the chip's label is already active.
            toggleUsageLabelFilter(label) {
                this.usageFilterLabel = this.usageFilterLabel === label ? '' : label;
                this.onUsageFilterChanged();
            },

            usageLabelChipTitle(label) {
                if (this.usageFilterLabel === label) return 'Clear label filter';
                return 'Filter usage by "' + label + '"';
            },

            usageEntryCacheType(entry) {
                return String((entry && entry.cache_type) || '').trim().toLowerCase();
            },

            usageEntryCached(entry) {
                const type = this.usageEntryCacheType(entry);
                return type === 'exact' || type === 'semantic';
            },

            usageEntryCacheLabel(entry) {
                const type = this.usageEntryCacheType(entry);
                if (type === 'exact') return 'Exact';
                if (type === 'semantic') return 'Semantic';
                return '-';
            },

            cachedCostTitle(entry, baseTitle) {
                const base = baseTitle ? String(baseTitle) : '';
                if (!this.usageEntryCached(entry)) return base;
                const prefix = 'Saved by cache — not charged';
                return base ? prefix + '\n' + base : prefix;
            },

            providerCacheRatio(entry) {
                const ratio = Number(entry && entry.cached_input_ratio);
                if (!Number.isFinite(ratio) || ratio <= 0) return 0;
                return Math.min(1, ratio);
            },

            hasProviderCache(entry) {
                return Number(entry && entry.cached_input_tokens || 0) > 0;
            },

            providerCacheLabel(entry) {
                if (!this.hasProviderCache(entry)) return '';
                const pct = this.providerCacheRatio(entry) * 100;
                return pct.toFixed(1) + '%';
            },

            providerCacheTitle(entry) {
                if (!this.hasProviderCache(entry)) return '';
                const cached = Number(entry.cached_input_tokens || 0);
                const uncached = Number(entry.uncached_input_tokens || 0);
                const write = Number(entry.cache_write_input_tokens || 0);
                const total = cached + uncached + write;
                const parts = [this.formatNumber(cached) + ' cached / ' + this.formatNumber(total) + ' input tokens'];
                if (write > 0) {
                    parts.push(this.formatNumber(write) + ' cache write');
                }
                return parts.join('\n');
            },

            usesOpenRouterCreditPricing(entry) {
                return costSource(entry) === 'openrouter_credits';
            },

            usesResponseCostPricing(entry) {
                const source = costSource(entry);
                return source === 'openrouter_credits' || source === 'xai_cost_in_usd_ticks';
            },

            costSourceTooltip(entry) {
                switch (costSource(entry)) {
                case 'openrouter_credits':
                    return 'Costs from OpenRouter USD-based credits.';
                case 'xai_cost_in_usd_ticks':
                    return 'Costs from xAI usage.cost_in_usd_ticks.';
                default:
                    return '';
                }
            }
        };
    }

    global.dashboardUsageModule = dashboardUsageModule;
})(window);
