(function(global) {
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

            _barChartConfig(colors, labels, values, palette) {
                return {
                    type: 'bar',
                    data: {
                        labels: labels,
                        datasets: [{
                            data: values,
                            backgroundColor: labels.map((_, i) => palette[i % palette.length]),
                            borderColor: 'transparent',
                            borderWidth: 0,
                            borderRadius: 4
                        }]
                    },
                    options: {
                        responsive: true,
                        maintainAspectRatio: false,
                        animation: { duration: 0 },
                        layout: { padding: { top: 8 } },
                        scales: {
                            x: {
                                grid: { display: false },
                                ticks: {
                                    color: colors.text,
                                    font: { size: 11, family: "'SF Mono', Menlo, Consolas, monospace" },
                                    maxRotation: 45,
                                    minRotation: 0
                                }
                            },
                            y: {
                                grid: { color: colors.grid },
                                border: { display: false },
                                ticks: {
                                    color: colors.text,
                                    font: { size: 11, family: "'SF Mono', Menlo, Consolas, monospace" },
                                    callback: (v) => {
                                        if (this.usageMode === 'costs') return '$' + v.toFixed(2);
                                        return this.formatTokensShort(v);
                                    }
                                }
                            }
                        },
                        plugins: {
                            legend: { display: false },
                            tooltip: {
                                backgroundColor: colors.tooltipBg,
                                borderColor: colors.tooltipBorder,
                                borderWidth: 1,
                                titleColor: colors.tooltipText,
                                bodyColor: colors.tooltipText,
                                callbacks: {
                                    label: (c) => {
                                        const val = c.parsed.y;
                                        if (this.usageMode === 'costs') return '$' + val.toFixed(4);
                                        return this.formatTokensShort(val);
                                    }
                                }
                            }
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

            _barDataFrom(items, labelFor) {
                const sorted = this._usageRowsBySelectedValue(items);

                const top = sorted.slice(0, 10);
                const rest = sorted.slice(10);

                const labels = top.map(labelFor);
                const values = top.map((row) => this._usageAggregateValue(row));

                if (rest.length > 0) {
                    labels.push('Other');
                    let otherVal = 0;
                    rest.forEach((row) => {
                        otherVal += this._usageAggregateValue(row);
                    });
                    values.push(otherVal);
                }

                return { labels, values };
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

            _barData() {
                return this._barDataFrom(this.modelUsage, (m) => typeof this.qualifiedModelDisplay === 'function'
                    ? this.qualifiedModelDisplay(m)
                    : m.model);
            },

            _userPathBarData() {
                return this._barDataFrom(this.userPathUsage || [], (u) => u.user_path || '/');
            },

            _labelBarData() {
                return this._barDataFrom(this.labelUsage || [], (l) => l.label);
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

            // The synthetic "Other" bar gets a neutral tone instead of a
            // hashed identity color.
            _labelBarPalette(labels) {
                return labels.map((label) => label === 'Other' ? '#a8a29a' : this.labelColor(label));
            },

            barLegendItems() {
                const { labels, values } = this._barData();
                const colors = this._barColors();
                return labels.map((label, i) => ({
                    label,
                    color: colors[i % colors.length],
                    value: this.usageMode === 'costs' ? '$' + values[i].toFixed(4) : this.formatTokensShort(values[i])
                }));
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
                    if (this.modelUsage.length === 0 || this.page !== 'usage' || (this.modelUsageView || 'chart') !== 'chart') {
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

                    const colors = this.chartColors();
                    const { labels, values } = this._barData();
                    const palette = this._barColors();
                    const config = this._barChartConfig(colors, labels, values, palette);

                    if (this.usageBarChart) {
                        this.usageBarChart.destroy();
                        this.usageBarChart = null;
                    }

                    this.usageBarChart = new Chart(canvas, config);
                });
            },

            renderUserPathChart(retries) {
                if (retries === undefined) retries = 3;
                this.$nextTick(() => {
                    if (!this.userPathUsageChartVisible() || this.page !== 'usage' || (this.userPathUsageView || 'chart') !== 'chart') {
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

                    const colors = this.chartColors();
                    const { labels, values } = this._userPathBarData();
                    const palette = this._barColors();
                    const config = this._barChartConfig(colors, labels, values, palette);

                    if (this.usageUserPathChart) {
                        this.usageUserPathChart.destroy();
                        this.usageUserPathChart = null;
                    }

                    this.usageUserPathChart = new Chart(canvas, config);
                });
            },

            renderLabelChart(retries) {
                if (retries === undefined) retries = 3;
                this.$nextTick(() => {
                    if ((this.labelUsage || []).length === 0 || this.page !== 'usage' || (this.labelUsageView || 'chart') !== 'chart') {
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

                    const colors = this.chartColors();
                    const { labels, values } = this._labelBarData();
                    const palette = this._labelBarPalette(labels);
                    const config = this._barChartConfig(colors, labels, values, palette);

                    if (this.usageLabelChart) {
                        this.usageLabelChart.destroy();
                        this.usageLabelChart = null;
                    }

                    this.usageLabelChart = new Chart(canvas, config);
                });
            }
        };
    }

    global.dashboardChartsModule = dashboardChartsModule;
})(window);
