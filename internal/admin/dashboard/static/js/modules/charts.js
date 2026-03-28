(function(global) {
    function dashboardChartsModule() {
        return {
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
                    if (this.chart) {
                        this.chart.destroy();
                        this.chart = null;
                    }

                    if (this.daily.length === 0) return;
                    if (this.page !== 'overview') return;

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

                    this.chart = new Chart(canvas, {
                        type: 'line',
                        data: {
                            labels: labels,
                            datasets: [
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
                            ]
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
                    });
                });
            },

            _barColors() {
                return [
                    '#c2845a', '#7a9e7e', '#d4a574', '#b8a98e', '#8b9e6b',
                    '#7d8a97', '#c47a5a', '#6b8e6b', '#a09486', '#9b7ea4',
                    '#c49a6c'
                ];
            },

            _barData() {
                const sorted = [...this.modelUsage].sort((a, b) => {
                    if (this.usageMode === 'costs') {
                        return ((b.total_cost || 0) - (a.total_cost || 0));
                    }
                    return ((b.input_tokens + b.output_tokens) - (a.input_tokens + a.output_tokens));
                });

                const top = sorted.slice(0, 10);
                const rest = sorted.slice(10);

                const labels = top.map((m) => m.model);
                const values = top.map((m) => {
                    if (this.usageMode === 'costs') return m.total_cost || 0;
                    return m.input_tokens + m.output_tokens;
                });

                if (rest.length > 0) {
                    labels.push('Other');
                    let otherVal = 0;
                    rest.forEach((m) => {
                        if (this.usageMode === 'costs') otherVal += (m.total_cost || 0);
                        else otherVal += m.input_tokens + m.output_tokens;
                    });
                    values.push(otherVal);
                }

                return { labels, values };
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

            renderBarChart(retries) {
                if (retries === undefined) retries = 3;
                this.$nextTick(() => {
                    if (this.usageBarChart) {
                        this.usageBarChart.destroy();
                        this.usageBarChart = null;
                    }

                    if (this.modelUsage.length === 0) return;
                    if (this.page !== 'usage') return;

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

                    this.usageBarChart = new Chart(canvas, {
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
                    });
                });
            }
        };
    }

    global.dashboardChartsModule = dashboardChartsModule;
})(window);
