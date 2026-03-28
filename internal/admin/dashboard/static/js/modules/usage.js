(function(global) {
    function dashboardUsageModule() {
        return {
            _usageQueryStr() {
                if (this.customStartDate && this.customEndDate) {
                    return 'start_date=' + this._formatDate(this.customStartDate) +
                        '&end_date=' + this._formatDate(this.customEndDate);
                }
                return 'days=' + this.days;
            },

            async fetchUsage() {
                try {
                    let queryStr;
                    if (this.customStartDate && this.customEndDate) {
                        queryStr = 'start_date=' + this._formatDate(this.customStartDate) +
                            '&end_date=' + this._formatDate(this.customEndDate);
                    } else {
                        queryStr = 'days=' + this.days;
                    }
                    queryStr += '&interval=' + this.interval;

                    const [summaryRes, dailyRes] = await Promise.all([
                        fetch('/admin/api/v1/usage/summary?' + queryStr, { headers: this.headers() }),
                        fetch('/admin/api/v1/usage/daily?' + queryStr, { headers: this.headers() })
                    ]);

                    if (!this.handleFetchResponse(summaryRes, 'usage summary') ||
                        !this.handleFetchResponse(dailyRes, 'usage daily')) {
                        return;
                    }

                    this.summary = await summaryRes.json();
                    this.daily = await dailyRes.json();
                    this.renderChart();
                    if (this.page === 'usage') this.fetchUsagePage();
                    if (this.page === 'audit-logs') this.fetchAuditLog(true);
                } catch (e) {
                    console.error('Failed to fetch usage:', e);
                }
            },

            async fetchUsagePage() {
                await Promise.all([this.fetchModelUsage(), this.fetchUsageLog(true)]);
                this.renderBarChart();
            },

            async fetchModelUsage() {
                try {
                    const res = await fetch('/admin/api/v1/usage/models?' + this._usageQueryStr(), { headers: this.headers() });
                    if (!this.handleFetchResponse(res, 'usage models')) {
                        this.modelUsage = [];
                        return;
                    }
                    this.modelUsage = await res.json();
                } catch (e) {
                    console.error('Failed to fetch model usage:', e);
                    this.modelUsage = [];
                }
            },

            async fetchUsageLog(resetOffset) {
                try {
                    if (resetOffset) this.usageLog.offset = 0;
                    let qs = this._usageQueryStr();
                    qs += '&limit=' + this.usageLog.limit + '&offset=' + this.usageLog.offset;
                    if (this.usageLogSearch) qs += '&search=' + encodeURIComponent(this.usageLogSearch);
                    if (this.usageLogModel) qs += '&model=' + encodeURIComponent(this.usageLogModel);
                    if (this.usageLogProvider) qs += '&provider=' + encodeURIComponent(this.usageLogProvider);

                    const res = await fetch('/admin/api/v1/usage/log?' + qs, { headers: this.headers() });
                    if (!this.handleFetchResponse(res, 'usage log')) {
                        this.usageLog = { entries: [], total: 0, limit: 50, offset: 0 };
                        return;
                    }
                    this.usageLog = await res.json();
                    if (!this.usageLog.entries) this.usageLog.entries = [];
                } catch (e) {
                    console.error('Failed to fetch usage log:', e);
                    this.usageLog = { entries: [], total: 0, limit: 50, offset: 0 };
                }
            },

            toggleUsageMode(mode) {
                this.usageMode = mode;
                const url = mode === 'costs' ? '/admin/dashboard/usage/costs' : '/admin/dashboard/usage';
                history.pushState(null, '', url);
                this.renderBarChart();
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
                this.modelUsage.forEach((m) => { set.add(m.provider); });
                return [...set].sort();
            }
        };
    }

    global.dashboardUsageModule = dashboardUsageModule;
})(window);
