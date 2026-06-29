(function(global) {
    // Live token throughput chart (overview page).
    //
    // A rolling, stacked bar chart of token volume bucketed by a selectable
    // granularity (seconds/minutes/hours/days). The buckets come from the
    // backend aggregation endpoint (GET /admin/usage/throughput) — the database
    // is the single source of truth — so history is present on load and survives
    // refreshes/restarts. The live usage SSE stream is used only as a signal to
    // refetch (debounced) when new usage is persisted; a steady timer refetches
    // to keep the window scrolling.
    //
    // Each bucket carries four stacked series, mirroring the overview Cache Meter:
    //   - Input Tokens          (regular, non-cached prompt input + cache writes)
    //   - Output Tokens         (generated tokens)
    //   - Prompt (Input) Cached (provider prompt-cache reads)
    //   - Locally Cached        (responses served from GoModel's local cache)
    function dashboardLiveTokensModule() {
        function liveTokensPath(path) {
            if (typeof window !== 'undefined' && typeof window.gomodelPath === 'function') {
                return window.gomodelPath(path);
            }
            return path;
        }

        // apiName: backend granularity; refreshMs: how often to refetch to scroll
        // the window (matched to the bucket width, capped for coarse views).
        const GRANULARITIES = {
            seconds: { apiName: 'second', windowLabel: 'Last 60 seconds', refreshMs: 2000 },
            minutes: { apiName: 'minute', windowLabel: 'Last 60 minutes', refreshMs: 5000 },
            hours:   { apiName: 'hour', windowLabel: 'Last 24 hours', refreshMs: 20000 },
            days:    { apiName: 'day', windowLabel: 'Last 30 days', refreshMs: 60000 }
        };

        const GRANULARITY_OPTIONS = [
            { value: 'seconds', label: 'Seconds' },
            { value: 'minutes', label: 'Minutes' },
            { value: 'hours', label: 'Hours' },
            { value: 'days', label: 'Days' }
        ];

        // Engine state lives in this closure, deliberately OUTSIDE Alpine's
        // reactive proxy: a Chart.js instance is deeply self-referential, so
        // operating on a reactive-wrapped chart recurses forever. This also
        // cleanly separates the engine from the reactive view state below.
        let chart = null;
        let chartGran = null;
        let scrollTimer = null;
        let debounceTimer = null;
        let inFlight = false;
        let buckets = [];
        // Bucket-start timestamps for the current chart, kept here (not on the
        // Chart instance) so they're available to the very first synchronous draw
        // inside `new Chart()` — otherwise the day-marks plugin misses them on
        // creation and the separator only appears on the next redraw (e.g. hover).
        let liveStamps = [];

        function zeroMetrics() {
            return { input: 0, output: 0, prompt: 0, local: 0 };
        }

        function pad2(n) {
            return String(n).padStart(2, '0');
        }

        function num(value) {
            const n = Number(value);
            return Number.isFinite(n) && n > 0 ? n : 0;
        }

        // Bucket-start label, formatted per granularity in local time.
        function formatBucketLabel(granularity, startMs) {
            if (!Number.isFinite(startMs)) {
                return '';
            }
            const d = new Date(startMs);
            switch (granularity) {
            case 'seconds':
                return pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds());
            case 'minutes':
                return pad2(d.getHours()) + ':' + pad2(d.getMinutes());
            case 'hours':
                return pad2(d.getHours()) + ':00';
            case 'days':
            default:
                return pad2(d.getMonth() + 1) + '-' + pad2(d.getDate());
            }
        }

        return {
            // Reactive view state (bound in the template).
            liveTokensGranularity: 'minutes',
            liveTokensSummary: zeroMetrics(),
            liveTokensActive: false,

            liveTokensGranularityOptions() {
                return GRANULARITY_OPTIONS;
            },

            liveTokensWindowLabel() {
                return (GRANULARITIES[this.liveTokensGranularity] || GRANULARITIES.minutes).windowLabel;
            },

            liveTokensHasData() {
                const s = this.liveTokensSummary;
                return (s.input + s.output + s.prompt + s.local) > 0;
            },

            liveTokensLegendValue(metric) {
                return this.formatTokensShort(Math.max(0, Math.round(this.liveTokensSummary[metric] || 0)));
            },

            // Text equivalent of the chart for screen readers (the canvas itself
            // is opaque to assistive tech).
            liveTokensChartAriaLabel() {
                return 'Live token throughput, ' + this.liveTokensWindowLabel().toLowerCase() + '. ' +
                    'Input ' + this.liveTokensLegendValue('input') + ', ' +
                    'output ' + this.liveTokensLegendValue('output') + ', ' +
                    'prompt cached ' + this.liveTokensLegendValue('prompt') + ', ' +
                    'locally cached ' + this.liveTokensLegendValue('local') + ' tokens.';
            },

            setLiveTokensGranularity(value) {
                if (!GRANULARITIES[value] || value === this.liveTokensGranularity) {
                    return;
                }
                this.liveTokensGranularity = value;
                chartGran = null; // force a rebuild so the x-axis re-buckets
                buckets = [];
                this.renderLiveTokensChart();
                this._restartLiveTokensScroll();
                this.fetchLiveTokens();
            },

            startLiveTokens() {
                this.liveTokensActive = true;
                this.fetchLiveTokens();
                this._restartLiveTokensScroll();
            },

            stopLiveTokens() {
                this.liveTokensActive = false;
                if (scrollTimer) {
                    clearInterval(scrollTimer);
                    scrollTimer = null;
                }
                if (debounceTimer) {
                    clearTimeout(debounceTimer);
                    debounceTimer = null;
                }
                if (chart) {
                    chart.destroy();
                    chart = null;
                }
                chartGran = null;
                buckets = [];
                this.liveTokensSummary = zeroMetrics();
            },

            // Force a fresh chart (e.g. on theme change, to re-resolve colors).
            redrawLiveTokensChart() {
                chartGran = null;
                this.renderLiveTokensChart();
            },

            _restartLiveTokensScroll() {
                if (scrollTimer) {
                    clearInterval(scrollTimer);
                    scrollTimer = null;
                }
                if (typeof setInterval !== 'function') {
                    return;
                }
                const cfg = GRANULARITIES[this.liveTokensGranularity] || GRANULARITIES.minutes;
                scrollTimer = setInterval(() => {
                    if (!this.liveTokensActive || this.page !== 'overview') {
                        return;
                    }
                    this.fetchLiveTokens();
                }, cfg.refreshMs);
            },

            // Called from the live-logs SSE consumer. A `usage.flushed` event
            // means a row was just persisted, so the aggregate has changed —
            // refetch (debounced to coalesce bursts). Other usage events are the
            // not-yet-persisted echoes the periodic scroll already covers.
            noteLiveTokenUsage(eventType) {
                if (!this.liveTokensActive || eventType !== 'usage.flushed') {
                    return;
                }
                if (debounceTimer || typeof setTimeout !== 'function') {
                    return;
                }
                debounceTimer = setTimeout(() => {
                    debounceTimer = null;
                    this.fetchLiveTokens();
                }, 900);
            },

            async fetchLiveTokens() {
                if (!this.liveTokensActive || this.page !== 'overview' || inFlight) {
                    return;
                }
                if (typeof fetch !== 'function') {
                    return;
                }
                inFlight = true;
                const requested = this.liveTokensGranularity;
                try {
                    const cfg = GRANULARITIES[requested] || GRANULARITIES.minutes;
                    const options = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    const res = await fetch(liveTokensPath('/admin/usage/throughput?granularity=' + cfg.apiName), options);
                    const handled = this.handleFetchResponse(res, 'token throughput', options);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        return;
                    }
                    const payload = await res.json();
                    // The granularity changed while this request was in flight, so
                    // its buckets no longer match the selected window — discard them
                    // (a fresh fetch is issued in finally).
                    if (this.liveTokensGranularity !== requested) {
                        return;
                    }
                    buckets = payload && Array.isArray(payload.buckets) ? payload.buckets : [];
                    this.renderLiveTokensChart();
                } catch (e) {
                    if (typeof this._isAbortError === 'function' && this._isAbortError(e)) {
                        return;
                    }
                    console.error('Failed to fetch token throughput:', e);
                } finally {
                    inFlight = false;
                    // A granularity change during the fetch was swallowed by the
                    // in-flight guard; fetch the now-selected granularity.
                    if (this.liveTokensActive && this.page === 'overview' && this.liveTokensGranularity !== requested) {
                        this.fetchLiveTokens();
                    }
                }
            },

            // Resolve a CSS color expression to a canvas-safe rgb() string by
            // letting the browser compute it on a throwaway element (handles
            // var()/color-mix(), which canvas fillStyle may not accept directly).
            _resolveLiveTokenColor(expr) {
                if (typeof document === 'undefined' || !document.body) {
                    return expr;
                }
                const probe = document.createElement('span');
                probe.style.display = 'none';
                probe.style.color = expr;
                document.body.appendChild(probe);
                const resolved = getComputedStyle(probe).color;
                document.body.removeChild(probe);
                return resolved || expr;
            },

            liveTokensColors() {
                return {
                    input: this._resolveLiveTokenColor('var(--token-input)'),
                    output: this._resolveLiveTokenColor('var(--token-output)'),
                    prompt: this._resolveLiveTokenColor('var(--token-prompt)'),
                    local: this._resolveLiveTokenColor('var(--token-local)')
                };
            },

            _liveTokensChartConfig(colors, seriesColors, labels, cols) {
                const formatValue = (v) => this.formatTokensShort(Math.max(0, Math.round(v)));
                const granularity = this.liveTokensGranularity;
                const bar = (label, data, color) => ({
                    label: label,
                    data: data,
                    backgroundColor: color,
                    borderWidth: 0,
                    borderRadius: 0,
                    categoryPercentage: 1.0, // touching bars: fill the category, no inter-bar gap
                    barPercentage: 1.0,
                    stack: 'tokens'
                });
                // Mark where the local calendar day changes between buckets with a
                // dashed separator + date label. Chart.js has no built-in for this
                // on a category axis (a time scale could, but needs a date-adapter
                // lib we don't bundle), so we draw it directly. Skipped for the
                // Days view, where every bar is already a separate day.
                const dayMarks = {
                    id: 'liveTokensDayMarks',
                    afterDatasetsDraw: (chart) => {
                        if (granularity === 'days') return;
                        const stamps = liveStamps;
                        const meta = chart.getDatasetMeta(0);
                        const area = chart.chartArea;
                        if (!meta || !meta.data || !area) return;
                        const ctx = chart.ctx;
                        ctx.save();
                        ctx.font = "10px 'SF Mono', Menlo, Consolas, monospace";
                        let prevKey = null;
                        for (let i = 0; i < stamps.length && i < meta.data.length; i++) {
                            const ms = stamps[i];
                            if (!Number.isFinite(ms)) { prevKey = null; continue; }
                            const d = new Date(ms);
                            const key = d.getFullYear() + '-' + d.getMonth() + '-' + d.getDate();
                            if (prevKey !== null && key !== prevKey && meta.data[i - 1]) {
                                const markerColor = colors.dayMarker || colors.text;
                                const x = Math.round((meta.data[i - 1].x + meta.data[i].x) / 2) + 0.5;
                                ctx.globalAlpha = 0.6;
                                ctx.strokeStyle = markerColor;
                                ctx.setLineDash([3, 3]);
                                ctx.lineWidth = 1;
                                ctx.beginPath();
                                ctx.moveTo(x, area.top);
                                ctx.lineTo(x, area.bottom);
                                ctx.stroke();
                                ctx.setLineDash([]);
                                // Full opacity + high-contrast color so the date label stays legible.
                                ctx.globalAlpha = 1;
                                ctx.fillStyle = markerColor;
                                ctx.textAlign = 'left';
                                ctx.textBaseline = 'bottom';
                                ctx.fillText(d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }), x + 3, area.bottom - 2);
                            }
                            prevKey = key;
                        }
                        ctx.restore();
                    }
                };
                return {
                    type: 'bar',
                    plugins: [dayMarks],
                    data: {
                        labels: labels,
                        datasets: [
                            bar('Input Tokens', cols.input, seriesColors.input),
                            bar('Output Tokens', cols.output, seriesColors.output),
                            bar('Prompt (Input) Cached', cols.prompt, seriesColors.prompt),
                            bar('Locally Cached', cols.local, seriesColors.local)
                        ]
                    },
                    options: {
                        responsive: true,
                        maintainAspectRatio: false,
                        animation: { duration: 0 },
                        interaction: { mode: 'index', intersect: false },
                        scales: {
                            x: {
                                stacked: true,
                                grid: { color: colors.grid },
                                border: { display: false },
                                ticks: {
                                    color: colors.text,
                                    font: this._chartTickFont(),
                                    maxRotation: 0,
                                    autoSkip: true,
                                    maxTicksLimit: 8
                                }
                            },
                            y: {
                                stacked: true,
                                beginAtZero: true,
                                grid: { color: colors.grid },
                                border: { display: false },
                                ticks: this._tokenAxisTicks(colors)
                            }
                        },
                        plugins: {
                            legend: { display: false },
                            tooltip: this._chartTooltip(colors, {
                                title: (items) => {
                                    if (!items.length) {
                                        return '';
                                    }
                                    const ms = liveStamps[items[0].dataIndex];
                                    if (!ms) {
                                        return items[0].label;
                                    }
                                    const date = new Date(ms);
                                    // Day buckets start at local midnight, so the time is always
                                    // 00:00:00 — show the date alone on the Days view.
                                    return granularity === 'days' ? date.toLocaleDateString() : date.toLocaleString();
                                },
                                label: (c) => c.dataset.label + ': ' + formatValue(c.parsed.y),
                                footer: (items) => {
                                    let total = 0;
                                    items.forEach((it) => { total += Number(it.parsed.y) || 0; });
                                    return 'Total: ' + formatValue(total);
                                }
                            })
                        }
                    }
                };
            },

            renderLiveTokensChart(retries) {
                if (retries === undefined) {
                    retries = 3;
                }
                this.$nextTick(() => {
                    if (!this.liveTokensActive || this.page !== 'overview') {
                        if (chart) {
                            chart.destroy();
                            chart = null;
                            chartGran = null;
                        }
                        return;
                    }

                    const canvas = document.getElementById('liveTokensChart');
                    if (!canvas || canvas.offsetWidth === 0) {
                        if (retries > 0) {
                            setTimeout(() => this.renderLiveTokensChart(retries - 1), 100);
                        }
                        return;
                    }

                    const labels = [];
                    const stamps = [];
                    const cols = { input: [], output: [], prompt: [], local: [] };
                    const totals = zeroMetrics();
                    for (const bucket of buckets) {
                        const startMs = Date.parse(bucket && bucket.start);
                        const input = num(bucket && bucket.input_tokens);
                        const output = num(bucket && bucket.output_tokens);
                        const prompt = num(bucket && bucket.prompt_cached_tokens);
                        const local = num(bucket && bucket.locally_cached_tokens);
                        labels.push(formatBucketLabel(this.liveTokensGranularity, startMs));
                        stamps.push(Number.isFinite(startMs) ? startMs : null);
                        cols.input.push(input);
                        cols.output.push(output);
                        cols.prompt.push(prompt);
                        cols.local.push(local);
                        totals.input += input;
                        totals.output += output;
                        totals.prompt += prompt;
                        totals.local += local;
                    }
                    this.liveTokensSummary = totals;
                    liveStamps = stamps;

                    if (chart && chartGran === this.liveTokensGranularity) {
                        chart.data.labels = labels;
                        chart.data.datasets[0].data = cols.input;
                        chart.data.datasets[1].data = cols.output;
                        chart.data.datasets[2].data = cols.prompt;
                        chart.data.datasets[3].data = cols.local;
                        chart.update('none');
                        return;
                    }

                    if (chart) {
                        chart.destroy();
                    }
                    const config = this._liveTokensChartConfig(this.chartColors(), this.liveTokensColors(), labels, cols);
                    chart = new Chart(canvas, config);
                    chartGran = this.liveTokensGranularity;
                });
            }
        };
    }

    global.dashboardLiveTokensModule = dashboardLiveTokensModule;
})(window);
