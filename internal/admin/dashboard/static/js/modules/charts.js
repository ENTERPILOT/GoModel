(function(global) {
    function dashboardChartsModule() {
        return {
            _overviewChartConfig(colors, labels, inputData, outputData, cacheInputData, cacheOutputData) {
                const cacheEnabled = typeof this.cacheAnalyticsEnabled === 'function' && this.cacheAnalyticsEnabled();
                const datasets = [
                    {
                        label: 'Input Tokens',
                        data: inputData,
                        borderColor: '#c2845a',
                        backgroundColor: 'rgba(194, 132, 90, 0.1)',
                        fill: true,
                        tension: 0.3,
                        pointRadius: 3,
                        pointHoverRadius: 5
                    },
                    {
                        label: 'Output Tokens',
                        data: outputData,
                        borderColor: '#7a9e7e',
                        backgroundColor: 'rgba(122, 158, 126, 0.1)',
                        fill: true,
                        tension: 0.3,
                        pointRadius: 3,
                        pointHoverRadius: 5
                    }
                ];
                if (cacheEnabled) {
                    datasets.push(
                        {
                            label: 'Local Cache Input Tokens',
                            data: cacheInputData,
                            borderColor: '#5f7dcf',
                            backgroundColor: 'rgba(95, 125, 207, 0.08)',
                            fill: false,
                            tension: 0.3,
                            pointRadius: 2,
                            pointHoverRadius: 4,
                            borderDash: [6, 4]
                        },
                        {
                            label: 'Local Cache Output Tokens',
                            data: cacheOutputData,
                            borderColor: '#b0677b',
                            backgroundColor: 'rgba(176, 103, 123, 0.08)',
                            fill: false,
                            tension: 0.3,
                            pointRadius: 2,
                            pointHoverRadius: 4,
                            borderDash: [6, 4]
                        }
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
                            tooltip: {
                                backgroundColor: colors.tooltipBg,
                                borderColor: colors.tooltipBorder,
                                borderWidth: 1,
                                titleColor: colors.tooltipText,
                                bodyColor: colors.tooltipText,
                                callbacks: {
                                    label: function(c) {
                                        return c.dataset.label + ': ' + c.parsed.y.toLocaleString();
                                    }
                                }
                            }
                        },
                        scales: {
                            x: {
                                grid: { color: colors.grid },
                                ticks: { color: colors.text, font: { size: 11 }, maxTicksLimit: 10 }
                            },
                            y: {
                                beginAtZero: true,
                                grid: { color: colors.grid },
                                ticks: {
                                    color: colors.text,
                                    font: { size: 11 },
                                    callback: function(value) {
                                        if (value >= 1000000) return (value / 1000000).toFixed(1) + 'M';
                                        if (value >= 1000) return (value / 1000).toFixed(1) + 'K';
                                        return value;
                                    }
                                }
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

            renderChart(retries) {
                if (retries === undefined) retries = 3;
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
                    const inputData = filled.map((d) => d.input_tokens);
                    const outputData = filled.map((d) => d.output_tokens);
                    const cacheByDate = {};
                    const cacheDaily = this.fillMissingDays(this.cacheOverview && Array.isArray(this.cacheOverview.daily) ? this.cacheOverview.daily : []);
                    cacheDaily.forEach((d) => { cacheByDate[d.date] = d; });
                    const cacheInputData = labels.map((label) => (cacheByDate[label] && cacheByDate[label].input_tokens) || 0);
                    const cacheOutputData = labels.map((label) => (cacheByDate[label] && cacheByDate[label].output_tokens) || 0);
                    const config = this._overviewChartConfig(colors, labels, inputData, outputData, cacheInputData, cacheOutputData);

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
            }
        };
    }

    global.dashboardChartsModule = dashboardChartsModule;
})(window);
