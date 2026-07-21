(function(global) {
    /**
     * Create chart configuration, data-preparation, and rendering helpers for the dashboard.
     * @return {Object} The dashboard chart helper and lifecycle methods.
     */
    function dashboardChartsModule() {
        return {
            // --- Shared overview chart styling, so the line (Daily Token Usage)
            // and bar (Live Token Throughput) charts read as one family. ---
            _chartTickFont() {
                return { size: 11, family: "'SF Mono', Menlo, Consolas, monospace" };
            },

            _chartTooltip(colors, callbacks) {
                return {
                    backgroundColor: colors.tooltipBg,
                    borderColor: colors.tooltipBorder,
                    borderWidth: 1,
                    titleColor: colors.tooltipText,
                    bodyColor: colors.tooltipText,
                    callbacks: callbacks
                };
            },

            // Y-axis ticks that abbreviate token counts (e.g. 1.2K, 3.4M).
            _tokenAxisTicks(colors) {
                return {
                    color: colors.text,
                    font: this._chartTickFont(),
                    callback: (v) => this.formatTokensShort(v)
                };
            },

            _overviewChartConfig(colors, labels, inputData, outputData, promptData, localData) {
                const cacheEnabled = typeof this.cacheAnalyticsEnabled === 'function' && this.cacheAnalyticsEnabled();
                const resolve = (expr) => (typeof this._resolveLiveTokenColor === 'function' ? this._resolveLiveTokenColor(expr) : expr);
                const fade = (expr, pct) => resolve('color-mix(in srgb, ' + expr + ' ' + pct + '%, transparent)');
                // Same palette as the live throughput chart: paid tokens (input,
                // output) are solid browns; cached tokens are dashed blues, the
                // free "Locally Cached" lighter than the almost-free "Prompt cached".
                // Markerless lines (dots on hover only), matching the bar chart's
                // clean look; values stay readable via the tooltip.
                const line = (label, data, color, opts) => Object.assign({
                    label: label,
                    data: data,
                    borderColor: color,
                    backgroundColor: color,
                    fill: false,
                    tension: 0.3,
                    borderWidth: 2,
                    pointRadius: 0,
                    pointHoverRadius: 4
                }, opts || {});
                // Stacked area: each series sits on top of the one below, so the
                // band's top edge is the per-unit total (Input + Output + Prompt
                // cached + Locally cached). The bottom series fills to the axis
                // ('origin'); the rest fill down to the previous dataset ('-1').
                const datasets = [
                    line('Input Tokens', inputData, resolve('var(--token-input)'), { fill: 'origin' }),
                    line('Output Tokens', outputData, resolve('var(--token-output)'), { fill: '-1' }),
                    line('Prompt (Input) Cached', promptData, resolve('var(--token-prompt)'), { fill: '-1', borderDash: [6, 4] })
                ];
                if (cacheEnabled) {
                    datasets.push(
                        line('Locally Cached', localData, fade('var(--info)', 35), { fill: '-1', borderDash: [2, 3] })
                    );
                }
                return {
                    type: 'line',
                    data: {
                        labels: labels,
                        datasets: datasets
                    },
                    options: {
                        responsive: true,
                        maintainAspectRatio: false,
                        animation: { duration: 0 },
                        interaction: { mode: 'index', intersect: false },
                        plugins: {
                            legend: {
                                labels: { color: colors.text, font: { size: 12 } }
                            },
                            tooltip: this._chartTooltip(colors, {
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
                                grid: { color: colors.grid },
                                border: { display: false },
                                ticks: { color: colors.text, font: this._chartTickFont(), maxRotation: 0, autoSkip: true, maxTicksLimit: 10 }
                            },
                            y: {
                                stacked: true,
                                beginAtZero: true,
                                grid: { color: colors.grid },
                                border: { display: false },
                                ticks: this._tokenAxisTicks(colors)
                            }
                        }
                    }
                };
            },

            // Horizontal input/output bars: one row per entity (model, user
            // path, label). In the default diverging view the input-side
            // series (paid input, prompt-cached reads) grow left from zero
            // and the output-side series (output, locally-cached tokens) grow
            // right; in the stacked view everything piles rightward from zero
            // as segments of one bar. Series reuse the overview chart's
            // token palette (paid browns, translucent cache blues) so they
            // read consistently across the dashboard; the legend (and, when
            // diverging, the left/right split) carries identity, not color
            // alone. Cache series appear only when they have data.
            _horizontalUsageChartConfig(colors, labels, series, stacked) {
                const resolve = (expr) => (typeof this._resolveLiveTokenColor === 'function' ? this._resolveLiveTokenColor(expr) : expr);
                const costs = this.usageMode === 'costs';
                const fmtShort = (v) => costs ? '$' + Math.abs(v).toFixed(2) : this.formatTokensShort(Math.abs(v));
                const fmtExact = (v) => costs ? '$' + Math.abs(v).toFixed(4) : Math.abs(v).toLocaleString();
                const inputSide = (values) => values.map((v) => stacked ? Math.abs(v) : -Math.abs(v));
                const bar = (label, data, color) => ({
                    label: label,
                    data: data,
                    backgroundColor: color,
                    borderColor: 'transparent',
                    borderWidth: 0,
                    borderRadius: 4,
                    maxBarThickness: 22
                });
                const hasData = (values) => (values || []).some((v) => Math.abs(v) > 0);
                const datasets = [
                    bar(costs ? 'Input Cost' : 'Input Tokens', inputSide(series.inputs), resolve('var(--token-input)')),
                    bar(costs ? 'Output Cost' : 'Output Tokens', series.outputs, resolve('var(--token-output)'))
                ];
                if (hasData(series.prompts)) {
                    datasets.push(bar(costs ? 'Prompt Cached Cost' : 'Prompt Cached', inputSide(series.prompts), resolve('var(--token-prompt)')));
                }
                // Local cache hits carry both sides, so each joins its own
                // half of the axis (they pile together in the stacked view).
                if (!costs && hasData(series.localIns)) {
                    datasets.push(bar('Locally Cached (Input)', inputSide(series.localIns), resolve('var(--token-local)')));
                }
                if (!costs && hasData(series.localOuts)) {
                    datasets.push(bar('Locally Cached (Output)', series.localOuts, resolve('var(--token-local)')));
                }
                return {
                    type: 'bar',
                    data: {
                        labels: labels,
                        datasets: datasets
                    },
                    options: {
                        indexAxis: 'y',
                        responsive: true,
                        maintainAspectRatio: false,
                        animation: { duration: 0 },
                        layout: { padding: { top: 8 } },
                        scales: {
                            x: {
                                stacked: true,
                                beginAtZero: true,
                                // Emphasize the zero divider between the diverging halves.
                                grid: stacked
                                    ? { color: colors.grid }
                                    : { color: (ctx) => (ctx.tick && ctx.tick.value === 0 ? colors.text : colors.grid) },
                                border: { display: false },
                                ticks: {
                                    color: colors.text,
                                    font: this._chartTickFont(),
                                    callback: (v) => fmtShort(v)
                                }
                            },
                            y: {
                                stacked: true,
                                grid: { display: false },
                                border: { display: false },
                                ticks: {
                                    color: colors.text,
                                    font: this._chartTickFont(),
                                    autoSkip: false
                                }
                            }
                        },
                        plugins: {
                            legend: {
                                labels: { color: colors.text, font: { size: 12 } }
                            },
                            tooltip: this._chartTooltip(colors, {
                                label: (c) => c.dataset.label + ': ' + fmtExact(c.parsed.x),
                                footer: (items) => {
                                    let total = 0;
                                    items.forEach((it) => { total += Math.abs(Number(it.parsed.x)) || 0; });
                                    return 'Total: ' + fmtExact(total);
                                }
                            })
                        }
                    }
                };
            },

            fillMissingDays(daily) {
                if (this.interval !== 'daily') {
                    return daily;
                }

                const byDate = {};
                daily.forEach((d) => { byDate[d.date] = d; });
                const end = this.customEndDate ? new Date(this.customEndDate) : this.todayDate();
                let start = this.customStartDate ? new Date(this.customStartDate) : new Date(end);
                if (!this.customStartDate) {
                    start = this.dateKeyToDate(
                        this.addDaysToDateKey(this.dateToDateKey(end), -(parseInt(this.days, 10) - 1))
                    );
                }
                const result = [];
                for (let d = new Date(start); d <= end; d.setUTCDate(d.getUTCDate() + 1)) {
                    const key = this.dateToDateKey(d);
                    result.push(byDate[key] || { date: key, input_tokens: 0, output_tokens: 0, total_tokens: 0, requests: 0, input_cost: null, output_cost: null, total_cost: null });
                }
                return result;
            },

            // Prompt cache rate: share of the period's provider input tokens
            // that were served from the prompt cache. Denominator is the input
            // "parts" (uncached + prompt-cached + cache writes), matching the
            // cache meter's provider split.
            promptCacheRate() {
                const summary = this.summary || {};
                const uncached = Math.max(0, Number(summary.uncached_input_tokens) || 0);
                const cached = Math.max(0, Number(summary.cached_input_tokens) || 0);
                const cacheWrite = Math.max(0, Number(summary.cache_write_input_tokens) || 0);
                const denom = uncached + cached + cacheWrite;
                return denom > 0 ? (cached / denom) * 100 : 0;
            },

            promptCacheRateHasData() {
                const summary = this.summary || {};
                const denom = (Number(summary.uncached_input_tokens) || 0) +
                    (Number(summary.cached_input_tokens) || 0) +
                    (Number(summary.cache_write_input_tokens) || 0);
                return denom > 0;
            },

            promptCacheRateText() {
                if (!this.promptCacheRateHasData()) return '—';
                return Math.round(this.promptCacheRate()) + '%';
            },

            _promptCacheGaugeConfig(pct, fillColor, trackColor) {
                const value = Math.max(0, Math.min(100, pct));
                return {
                    type: 'doughnut',
                    data: {
                        datasets: [{
                            data: [value, 100 - value],
                            backgroundColor: [fillColor, trackColor],
                            borderWidth: 0,
                            spacing: 0
                        }]
                    },
                    options: {
                        // Half-circle gauge, filling clockwise from the left.
                        rotation: -90,
                        circumference: 180,
                        cutout: '84%',
                        responsive: true,
                        maintainAspectRatio: false,
                        animation: { duration: 0 },
                        layout: { padding: 1 },
                        events: [],
                        plugins: {
                            legend: { display: false },
                            tooltip: { enabled: false }
                        }
                    }
                };
            },

            renderPromptCacheGauge(retries) {
                if (retries === undefined) retries = 3;
                this.$nextTick(() => {
                    if (this.page !== 'overview') {
                        if (this.promptCacheChart) {
                            this.promptCacheChart.destroy();
                            this.promptCacheChart = null;
                        }
                        return;
                    }
                    const canvas = document.getElementById('promptCacheGauge');
                    if (!canvas) {
                        return; // no gauge on this page — nothing to render
                    }
                    if (canvas.offsetWidth === 0) {
                        if (retries > 0) {
                            setTimeout(() => this.renderPromptCacheGauge(retries - 1), 100);
                        }
                        return;
                    }
                    const resolve = (expr) => (typeof this._resolveLiveTokenColor === 'function'
                        ? this._resolveLiveTokenColor(expr)
                        : expr);
                    // Same colour as the "Prompt cached" series in the Tokens meter/chart.
                    const fill = resolve('var(--token-prompt)');
                    const track = resolve('var(--bg-surface-hover)');
                    const config = this._promptCacheGaugeConfig(this.promptCacheRate(), fill, track);

                    if (this.promptCacheChart) {
                        this.promptCacheChart.destroy();
                        this.promptCacheChart = null;
                    }
                    this.promptCacheChart = new Chart(canvas, config);
                });
            },

            renderChart(retries) {
                if (retries === undefined) retries = 3;
                this.renderPromptCacheGauge();
                this.$nextTick(() => {
                    if (this.daily.length === 0 || this.page !== 'overview') {
                        if (this.chart) {
                            this.chart.destroy();
                            this.chart = null;
                        }
                        return;
                    }

                    const canvas = document.getElementById('usageChart');
                    if (!canvas || canvas.offsetWidth === 0) {
                        if (retries > 0) {
                            setTimeout(() => this.renderChart(retries - 1), 100);
                        }
                        return;
                    }

                    const colors = this.chartColors();
                    const filled = this.fillMissingDays(this.daily);
                    const labels = filled.map((d) => d.date);
                    const num = (v) => Number(v) || 0;

                    // Paid input = uncached + cache writes (prompt-cache reads are
                    // their own series). Older rows lack the split, so fall back to
                    // the full input column when no split is present.
                    const inputPaid = filled.map((d) => {
                        const split = num(d.uncached_input_tokens) + num(d.cache_write_input_tokens) + num(d.cached_input_tokens);
                        return split > 0 ? num(d.uncached_input_tokens) + num(d.cache_write_input_tokens) : num(d.input_tokens);
                    });
                    const outputData = filled.map((d) => num(d.output_tokens));
                    const promptData = filled.map((d) => num(d.cached_input_tokens));

                    const cacheByDate = {};
                    const cacheDaily = this.fillMissingDays(this.cacheOverview && Array.isArray(this.cacheOverview.daily) ? this.cacheOverview.daily : []);
                    cacheDaily.forEach((d) => { cacheByDate[d.date] = d; });
                    // Local cache as a single series: input + output served from cache.
                    const localData = labels.map((label) => {
                        const c = cacheByDate[label];
                        return c ? num(c.input_tokens) + num(c.output_tokens) : 0;
                    });

                    const config = this._overviewChartConfig(
                        colors, labels,
                        inputPaid, outputData, promptData, localData
                    );

                    if (this.chart) {
                        this.chart.destroy();
                        this.chart = null;
                    }

                    this.chart = new Chart(canvas, config);
                });
            },

            _barColors() {
                return [
                    '#c2845a', '#7a9e7e', '#d4a574', '#b8a98e', '#8b9e6b',
                    '#7d8a97', '#c47a5a', '#6b8e6b', '#a09486', '#9b7ea4',
                    '#c49a6c'
                ];
            },

            _usageAggregateValue(row) {
                if (this.usageMode === 'costs') return row.total_cost || 0;
                return this.usageRowTotalTokens(row);
            },

            usageRowTotalTokens(row) {
                if (row && typeof row.total_tokens === 'number') return row.total_tokens;
                return ((row && row.input_tokens) || 0) + ((row && row.output_tokens) || 0);
            },

            _usageRowsBySelectedValue(items) {
                return [...(items || [])].sort((a, b) => {
                    if (this.usageMode === 'costs') {
                        return ((b.total_cost || 0) - (a.total_cost || 0));
                    }
                    return this._usageAggregateValue(b) - this._usageAggregateValue(a);
                });
            },

            modelUsageTableRows() {
                return this._usageRowsBySelectedValue(this.modelUsage || []);
            },

            userPathUsageTableRows() {
                return this._usageRowsBySelectedValue(this.userPathUsage || []);
            },

            labelUsageTableRows() {
                return this._usageRowsBySelectedValue(this.labelUsage || []);
            },

            userPathUsageChartVisible() {
                const rows = Array.isArray(this.userPathUsage) ? this.userPathUsage : [];
                if (rows.length === 0) {
                    return false;
                }
                if (rows.length !== 1) {
                    return true;
                }
                const onlyPath = String(rows[0] && rows[0].user_path || '').trim();
                return onlyPath !== '' && onlyPath !== '/';
            },

            // Per-row series split for the diverging charts. Rows keep the
            // selected-metric ordering; past the top 10 they fold into a
            // single "Other" row.
            //
            // Tokens mode mirrors the overview chart's accounting: the Input
            // series is paid input (uncached + cache writes; full input when
            // the split is absent), prompt-cache reads and locally-cached
            // tokens are their own series. Costs mode splits the estimated
            // prompt-cached read cost out of the input cost; local cache hits
            // cost nothing, so they have no cost series.
            _divergingDataFrom(items, labelFor) {
                const sorted = this._usageRowsBySelectedValue(items);
                const costs = this.usageMode === 'costs';
                const num = (v) => Number(v) || 0;
                // The cached cost is an estimate at current prices while
                // input_cost is the recorded charge, so cap the cached
                // segment at the recorded input cost — the two segments then
                // always sum to it and the bar never exceeds recorded totals.
                const promptOf = (row) => {
                    if (!costs) return num(row.cached_input_tokens);
                    return Math.min(num(row.cached_input_cost), num(row.input_cost));
                };
                const inputOf = (row) => {
                    if (costs) return num(row.input_cost) - promptOf(row);
                    const split = num(row.uncached_input_tokens) + num(row.cached_input_tokens) + num(row.cache_write_input_tokens);
                    return split > 0 ? num(row.uncached_input_tokens) + num(row.cache_write_input_tokens) : num(row.input_tokens);
                };
                const outputOf = (row) => num(costs ? row.output_cost : row.output_tokens);
                const localInputOf = (row) => costs ? 0 : num(row.local_cached_input_tokens);
                const localOutputOf = (row) => costs ? 0 : num(row.local_cached_output_tokens);

                const top = sorted.slice(0, 10);
                const rest = sorted.slice(10);

                const labels = top.map(labelFor);
                const inputs = top.map(inputOf);
                const outputs = top.map(outputOf);
                const prompts = top.map(promptOf);
                const localIns = top.map(localInputOf);
                const localOuts = top.map(localOutputOf);

                if (rest.length > 0) {
                    labels.push('Other');
                    const sum = (of) => rest.reduce((total, row) => total + of(row), 0);
                    inputs.push(sum(inputOf));
                    outputs.push(sum(outputOf));
                    prompts.push(sum(promptOf));
                    localIns.push(sum(localInputOf));
                    localOuts.push(sum(localOutputOf));
                }

                return { labels, inputs, outputs, prompts, localIns, localOuts };
            },

            // A view value that renders on the canvas (as opposed to the table).
            _isChartView(view) {
                return (view || 'chart') === 'chart' || view === 'stacked';
            },

            // Shared by the three usage charts: build the diverging or stacked
            // config, grow the wrapper with the row count so horizontal bars
            // stay readable instead of squeezing into a fixed height, and
            // mount the chart.
            _createUsageBarChart(canvas, items, labelFor, view) {
                const series = this._divergingDataFrom(items, labelFor);
                const config = this._horizontalUsageChartConfig(this.chartColors(), series.labels, series, view === 'stacked');

                const wrap = canvas.parentElement;
                if (wrap) {
                    wrap.style.height = Math.max(200, series.labels.length * 32 + 72) + 'px';
                }

                return new Chart(canvas, config);
            },

            // Deterministic label -> palette color so a label keeps one color
            // across the bar chart and every chip on the page.
            labelColor(label) {
                const palette = this._barColors();
                let hash = 5381;
                const text = String(label || '');
                for (let i = 0; i < text.length; i++) {
                    hash = ((hash << 5) + hash + text.charCodeAt(i)) | 0;
                }
                return palette[Math.abs(hash) % palette.length];
            },

            labelChipStyle(label) {
                return { '--label-color': this.labelColor(label) };
            },

            toggleUsageChartView(target, view) {
                if (target === 'model') {
                    this.modelUsageView = view;
                    this.renderBarChart();
                    return;
                }

                if (target === 'userPath') {
                    this.userPathUsageView = view;
                    this.renderUserPathChart();
                    return;
                }

                if (target === 'label') {
                    this.labelUsageView = view;
                    this.renderLabelChart();
                }
            },

            renderBarChart(retries) {
                if (retries === undefined) retries = 3;
                this.$nextTick(() => {
                    if (this.modelUsage.length === 0 || this.page !== 'usage' || !this._isChartView(this.modelUsageView)) {
                        if (this.usageBarChart) {
                            this.usageBarChart.destroy();
                            this.usageBarChart = null;
                        }
                        return;
                    }

                    const canvas = document.getElementById('usageBarChart');
                    if (!canvas || canvas.offsetWidth === 0) {
                        if (retries > 0) {
                            setTimeout(() => this.renderBarChart(retries - 1), 100);
                        }
                        return;
                    }

                    if (this.usageBarChart) {
                        this.usageBarChart.destroy();
                        this.usageBarChart = null;
                    }

                    this.usageBarChart = this._createUsageBarChart(canvas, this.modelUsage, (m) => typeof this.qualifiedModelDisplay === 'function'
                        ? this.qualifiedModelDisplay(m)
                        : m.model, this.modelUsageView);
                });
            },

            renderUserPathChart(retries) {
                if (retries === undefined) retries = 3;
                this.$nextTick(() => {
                    if (!this.userPathUsageChartVisible() || this.page !== 'usage' || !this._isChartView(this.userPathUsageView)) {
                        if (this.usageUserPathChart) {
                            this.usageUserPathChart.destroy();
                            this.usageUserPathChart = null;
                        }
                        return;
                    }

                    const canvas = document.getElementById('usageUserPathChart');
                    if (!canvas || canvas.offsetWidth === 0) {
                        if (retries > 0) {
                            setTimeout(() => this.renderUserPathChart(retries - 1), 100);
                        }
                        return;
                    }

                    if (this.usageUserPathChart) {
                        this.usageUserPathChart.destroy();
                        this.usageUserPathChart = null;
                    }

                    this.usageUserPathChart = this._createUsageBarChart(canvas, this.userPathUsage || [], (u) => u.user_path || '/', this.userPathUsageView);
                });
            },

            renderLabelChart(retries) {
                if (retries === undefined) retries = 3;
                this.$nextTick(() => {
                    if ((this.labelUsage || []).length === 0 || this.page !== 'usage' || !this._isChartView(this.labelUsageView)) {
                        if (this.usageLabelChart) {
                            this.usageLabelChart.destroy();
                            this.usageLabelChart = null;
                        }
                        return;
                    }

                    const canvas = document.getElementById('usageLabelChart');
                    if (!canvas || canvas.offsetWidth === 0) {
                        if (retries > 0) {
                            setTimeout(() => this.renderLabelChart(retries - 1), 100);
                        }
                        return;
                    }

                    if (this.usageLabelChart) {
                        this.usageLabelChart.destroy();
                        this.usageLabelChart = null;
                    }

                    this.usageLabelChart = this._createUsageBarChart(canvas, this.labelUsage || [], (l) => l.label, this.labelUsageView);
                });
            }
        };
    }

    global.dashboardChartsModule = dashboardChartsModule;
})(window);
