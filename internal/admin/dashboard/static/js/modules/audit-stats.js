(function(global) {
    function dashboardAuditStatsModule() {
        return {
            _auditStatsQueryStr() {
                if (this.customStartDate && this.customEndDate) {
                    return 'start_date=' + this._formatDate(this.customStartDate) +
                        '&end_date=' + this._formatDate(this.customEndDate);
                }
                return 'days=' + this.days;
            },

            async fetchAuditStats() {
                const requestToken = ++this.auditStatsFetchToken;
                try {
                    const request = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    const res = await fetch('/admin/audit/stats?' + this._auditStatsQueryStr(), request);
                    const handled = this.handleFetchResponse(res, 'audit stats', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        if (requestToken !== this.auditStatsFetchToken) return;
                        this.auditStats = this.emptyAuditStats();
                        this.renderAuditStatsCharts();
                        return;
                    }
                    const payload = await res.json();
                    if (requestToken !== this.auditStatsFetchToken) return;
                    this.auditStats = this.normalizeAuditStats(payload);
                    this.renderAuditStatsCharts();
                } catch (e) {
                    console.error('Failed to fetch audit stats:', e);
                    if (requestToken !== this.auditStatsFetchToken) return;
                    this.auditStats = this.emptyAuditStats();
                    this.renderAuditStatsCharts();
                }
            },

            emptyAuditStats() {
                return { interval: 'day', buckets: [], summary: { requests: 0 }, provider_latency: [] };
            },

            normalizeAuditStats(payload) {
                const stats = payload && typeof payload === 'object' ? payload : {};
                return {
                    interval: stats.interval === 'hour' ? 'hour' : 'day',
                    buckets: Array.isArray(stats.buckets) ? stats.buckets : [],
                    summary: stats.summary && typeof stats.summary === 'object' ? stats.summary : { requests: 0 },
                    provider_latency: Array.isArray(stats.provider_latency) ? stats.provider_latency : []
                };
            },

            auditStatsHasData() {
                return Number(this.auditStats && this.auditStats.summary && this.auditStats.summary.requests || 0) > 0;
            },

            auditLatencyHasData() {
                return (this.auditStats && Array.isArray(this.auditStats.provider_latency) ? this.auditStats.provider_latency : []).length > 0;
            },

            auditStatsSuccessRateText() {
                const rate = this.auditStats && this.auditStats.summary ? this.auditStats.summary.success_rate : null;
                if (rate === null || rate === undefined) return '—';
                return (Math.round(Number(rate) * 1000) / 10).toFixed(1) + '%';
            },

            auditStatsSummaryCount(key) {
                return this.formatNumber(Number(this.auditStats && this.auditStats.summary && this.auditStats.summary[key] || 0));
            },

            auditStatsAvgLatencyText() {
                const avg = this.auditStats && this.auditStats.summary ? this.auditStats.summary.avg_duration_ms : null;
                if (avg === null || avg === undefined) return '—';
                return this.formatDurationMs(Number(avg));
            },

            formatDurationMs(ms) {
                const v = Number(ms);
                if (!Number.isFinite(v)) return '-';
                if (v >= 60000) return (v / 60000).toFixed(1) + ' min';
                if (v >= 1000) return (v / 1000).toFixed(2) + ' s';
                return Math.round(v) + ' ms';
            },

            // Date parts in the dashboard's effective timezone: the server
            // buckets by the same X-GoModel-Timezone the dashboard sends, so
            // labels must not drift to the browser's locale when the two
            // timezones differ.
            _auditStatsDateParts(d) {
                const zone = typeof this.effectiveTimezone === 'function' ? this.effectiveTimezone() : undefined;
                try {
                    const byType = {};
                    new Intl.DateTimeFormat('en-US', {
                        timeZone: zone,
                        year: 'numeric',
                        month: 'short',
                        day: 'numeric',
                        hour: '2-digit',
                        hourCycle: 'h23'
                    }).formatToParts(d).forEach((part) => { byType[part.type] = part.value; });
                    return { year: byType.year, month: byType.month, day: byType.day, hour: Number(byType.hour) };
                } catch (e) {
                    const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
                    return { year: String(d.getFullYear()), month: months[d.getMonth()], day: String(d.getDate()), hour: d.getHours() };
                }
            },

            // Axis labels: hourly buckets show the hour and mark midnight with
            // the short date; daily buckets show the short date.
            _auditStatsBucketLabel(bucket) {
                const d = new Date(bucket.start);
                if (Number.isNaN(d.getTime())) return String(bucket.start || '');
                const parts = this._auditStatsDateParts(d);
                const day = parts.month + ' ' + parts.day;
                if (this.auditStats.interval !== 'hour') return day;
                if (parts.hour === 0) return day;
                return String(parts.hour).padStart(2, '0') + ':00';
            },

            _auditStatsTooltipTitle(bucket) {
                const d = new Date(bucket.start);
                if (Number.isNaN(d.getTime())) return String(bucket.start || '');
                if (this.auditStats.interval === 'hour') return this.formatTimestamp(bucket.start);
                const parts = this._auditStatsDateParts(d);
                return parts.month + ' ' + parts.day + ', ' + parts.year;
            },

            // Charts resolve the dashboard's status tokens (success/warning/
            // danger) so the stacked bars match the status badges in the log
            // list below, in both themes.
            _auditStatsColors() {
                const resolve = (expr) => (typeof this._resolveLiveTokenColor === 'function' ? this._resolveLiveTokenColor(expr) : expr);
                return {
                    ok: resolve('var(--success)'),
                    clientError: resolve('var(--warning)'),
                    serverError: resolve('var(--danger)'),
                    other: resolve('color-mix(in srgb, var(--text-muted) 55%, transparent)')
                };
            },

            _auditStatusChartConfig(colors, buckets) {
                const labels = buckets.map((b) => this._auditStatsBucketLabel(b));
                const statusColors = this._auditStatsColors();
                const surface = typeof this._resolveLiveTokenColor === 'function'
                    ? this._resolveLiveTokenColor('var(--bg-surface)')
                    : 'transparent';
                const num = (v) => Number(v) || 0;
                // Surface-colored borders read as gaps where stacked segments
                // touch, keeping the status classes separable without relying
                // on hue alone.
                const bar = (label, data, color) => ({
                    label: label,
                    data: data,
                    backgroundColor: color,
                    borderColor: surface,
                    borderWidth: 1,
                    borderSkipped: false,
                    borderRadius: 2,
                    maxBarThickness: 28
                });
                const datasets = [
                    bar('2xx', buckets.map((b) => num(b.status_2xx)), statusColors.ok),
                    bar('4xx', buckets.map((b) => num(b.status_4xx)), statusColors.clientError),
                    bar('5xx', buckets.map((b) => num(b.status_5xx)), statusColors.serverError)
                ];
                if (buckets.some((b) => num(b.status_other) > 0)) {
                    datasets.push(bar('Other', buckets.map((b) => num(b.status_other)), statusColors.other));
                }
                return {
                    type: 'bar',
                    data: { labels: labels, datasets: datasets },
                    options: {
                        responsive: true,
                        maintainAspectRatio: false,
                        animation: { duration: 0 },
                        interaction: { mode: 'index', intersect: false },
                        plugins: {
                            legend: { labels: { color: colors.text, font: { size: 12 } } },
                            tooltip: this._chartTooltip(colors, {
                                title: (items) => items.length ? this._auditStatsTooltipTitle(buckets[items[0].dataIndex]) : '',
                                label: (c) => c.dataset.label + ': ' + c.parsed.y.toLocaleString(),
                                footer: (items) => {
                                    let total = 0;
                                    items.forEach((it) => { total += Number(it.parsed.y) || 0; });
                                    return 'Total: ' + total.toLocaleString();
                                }
                            })
                        },
                        scales: {
                            x: {
                                stacked: true,
                                grid: { display: false },
                                border: { display: false },
                                ticks: { color: colors.text, font: this._chartTickFont(), maxRotation: 0, autoSkip: true, maxTicksLimit: 12 }
                            },
                            y: {
                                stacked: true,
                                beginAtZero: true,
                                grid: { color: colors.grid },
                                border: { display: false },
                                ticks: { color: colors.text, font: this._chartTickFont(), precision: 0, callback: (v) => this.formatTokensShort(v) }
                            }
                        }
                    }
                };
            },

            // Distinct categorical colors handed out in first-seen order, so a
            // provider keeps its color while the dashboard stays open and
            // near-identical hues (which hashing can produce) never end up as
            // neighboring lines.
            auditProviderColor(provider) {
                if (!this._auditProviderColors) this._auditProviderColors = {};
                if (!(provider in this._auditProviderColors)) {
                    const palette = typeof this._barColors === 'function' ? this._barColors() : ['#c2845a'];
                    this._auditProviderColors[provider] = palette[Object.keys(this._auditProviderColors).length % palette.length];
                }
                return this._auditProviderColors[provider];
            },

            _auditLatencyChartConfig(colors, buckets, series) {
                const labels = buckets.map((b) => this._auditStatsBucketLabel(b));
                const datasets = series.map((s) => ({
                    label: s.provider,
                    data: (s.avg_duration_ms || []).map((v) => (v === null || v === undefined ? null : Number(v))),
                    borderColor: this.auditProviderColor(s.provider),
                    backgroundColor: this.auditProviderColor(s.provider),
                    fill: false,
                    tension: 0.3,
                    borderWidth: 2,
                    pointRadius: 0,
                    pointHoverRadius: 4,
                    // Bridge a single quiet bucket so one idle hour doesn't cut
                    // the line, but keep longer outages visible as gaps. The
                    // category axis measures gaps in bucket indices.
                    spanGaps: this.auditStats.interval === 'hour' ? 2 : false
                }));
                return {
                    type: 'line',
                    data: { labels: labels, datasets: datasets },
                    options: {
                        responsive: true,
                        maintainAspectRatio: false,
                        animation: { duration: 0 },
                        interaction: { mode: 'index', intersect: false },
                        plugins: {
                            legend: { labels: { color: colors.text, font: { size: 12 } } },
                            tooltip: this._chartTooltip(colors, {
                                title: (items) => items.length ? this._auditStatsTooltipTitle(buckets[items[0].dataIndex]) : '',
                                label: (c) => {
                                    const requests = (series[c.datasetIndex] && series[c.datasetIndex].requests || [])[c.dataIndex];
                                    const count = Number(requests) || 0;
                                    return c.dataset.label + ': ' + this.formatDurationMs(c.parsed.y) +
                                        (count > 0 ? ' (' + count.toLocaleString() + ' req)' : '');
                                }
                            })
                        },
                        scales: {
                            x: {
                                grid: { color: colors.grid },
                                border: { display: false },
                                ticks: { color: colors.text, font: this._chartTickFont(), maxRotation: 0, autoSkip: true, maxTicksLimit: 12 }
                            },
                            y: {
                                beginAtZero: true,
                                grid: { color: colors.grid },
                                border: { display: false },
                                ticks: { color: colors.text, font: this._chartTickFont(), callback: (v) => this.formatDurationMs(v) }
                            }
                        }
                    }
                };
            },

            renderAuditStatsCharts() {
                this.renderAuditStatusChart();
                this.renderAuditLatencyChart();
            },

            _renderAuditChart(chartKey, canvasId, visible, buildConfig, retries) {
                if (retries === undefined) retries = 3;
                this.$nextTick(() => {
                    if (this.page !== 'overview' || !visible()) {
                        if (this[chartKey]) {
                            this[chartKey].destroy();
                            this[chartKey] = null;
                        }
                        return;
                    }
                    const canvas = document.getElementById(canvasId);
                    if (!canvas || canvas.offsetWidth === 0) {
                        if (retries > 0) {
                            setTimeout(() => this._renderAuditChart(chartKey, canvasId, visible, buildConfig, retries - 1), 100);
                        }
                        return;
                    }
                    if (this[chartKey]) {
                        this[chartKey].destroy();
                        this[chartKey] = null;
                    }
                    this[chartKey] = new Chart(canvas, buildConfig(this.chartColors()));
                });
            },

            renderAuditStatusChart(retries) {
                this._renderAuditChart(
                    'auditStatusChart',
                    'auditStatusChartCanvas',
                    () => this.auditStatsHasData(),
                    (colors) => this._auditStatusChartConfig(colors, this.auditStats.buckets),
                    retries
                );
            },

            renderAuditLatencyChart(retries) {
                this._renderAuditChart(
                    'auditLatencyChart',
                    'auditLatencyChartCanvas',
                    () => this.auditStatsHasData() && this.auditLatencyHasData(),
                    (colors) => this._auditLatencyChartConfig(colors, this.auditStats.buckets, this.auditStats.provider_latency),
                    retries
                );
            }
        };
    }

    global.dashboardAuditStatsModule = dashboardAuditStatsModule;
})(window);
