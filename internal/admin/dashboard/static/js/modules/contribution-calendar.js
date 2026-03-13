(function(global) {
    function dashboardContributionCalendarModule() {
        return {
            calendarData: [],
            calendarMode: 'tokens',
            calendarLoading: false,
            calendarTooltip: { show: false, x: 0, y: 0, text: '' },

            async fetchCalendarData() {
                this.calendarLoading = true;
                try {
                    const res = await fetch('/admin/api/v1/usage/daily?days=365&interval=daily', { headers: this.headers() });
                    if (!this.handleFetchResponse(res, 'calendar')) {
                        this.calendarData = [];
                        return;
                    }
                    this.calendarData = await res.json();
                } catch (e) {
                    console.error('Failed to fetch calendar data:', e);
                    this.calendarData = [];
                } finally {
                    this.calendarLoading = false;
                }
            },

            buildCalendarGrid() {
                var byDate = {};
                (this.calendarData || []).forEach(function(d) { byDate[d.date] = d; });

                var today = new Date();
                today.setHours(0, 0, 0, 0);

                var start = new Date(today);
                start.setDate(start.getDate() - 364);

                // Align start to Monday (ISO week start)
                var dayOfWeek = start.getDay();
                var diff = dayOfWeek === 0 ? -6 : 1 - dayOfWeek;
                start.setDate(start.getDate() + diff);

                var mode = this.calendarMode;
                var days = [];
                for (var d = new Date(start); d <= today; d.setDate(d.getDate() + 1)) {
                    var key = d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') + '-' + String(d.getDate()).padStart(2, '0');
                    var entry = byDate[key];
                    var value = 0;
                    if (entry) {
                        if (mode === 'costs') {
                            value = entry.total_cost != null ? entry.total_cost : 0;
                        } else {
                            value = entry.total_tokens || 0;
                        }
                    }
                    days.push({ dateStr: key, value: value, level: 0, empty: false });
                }

                // Calculate levels based on non-zero values
                var nonZero = days.filter(function(d) { return d.value > 0; }).map(function(d) { return d.value; });
                nonZero.sort(function(a, b) { return a - b; });
                var max = nonZero.length > 0 ? nonZero[nonZero.length - 1] : 0;

                for (var i = 0; i < days.length; i++) {
                    days[i].level = this.calendarLevel(days[i].value, max, nonZero);
                }

                // Build weeks (columns)
                var weeks = [];
                var week = [];
                for (var j = 0; j < days.length; j++) {
                    week.push(days[j]);
                    if (week.length === 7) {
                        weeks.push(week);
                        week = [];
                    }
                }
                if (week.length > 0) {
                    // Pad remaining week with empty slots
                    while (week.length < 7) {
                        week.push({ dateStr: '', value: 0, level: 0, empty: true });
                    }
                    weeks.push(week);
                }

                return weeks;
            },

            calendarLevel(value, max, sortedNonZero) {
                if (value === 0 || max === 0) return 0;
                if (!sortedNonZero || sortedNonZero.length === 0) return 0;
                var len = sortedNonZero.length;
                var q1 = sortedNonZero[Math.floor(len * 0.25)];
                var q2 = sortedNonZero[Math.floor(len * 0.5)];
                var q3 = sortedNonZero[Math.floor(len * 0.75)];
                if (value <= q1) return 1;
                if (value <= q2) return 2;
                if (value <= q3) return 3;
                return 4;
            },

            toggleCalendarMode(mode) {
                this.calendarMode = mode;
            },

            calendarMonthLabels() {
                var today = new Date();
                today.setHours(0, 0, 0, 0);
                var start = new Date(today);
                start.setDate(start.getDate() - 364);
                var dayOfWeek = start.getDay();
                var diff = dayOfWeek === 0 ? -6 : 1 - dayOfWeek;
                start.setDate(start.getDate() + diff);

                var months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
                var labels = [];
                var lastMonth = -1;
                var weekIdx = 0;

                for (var d = new Date(start); d <= today; ) {
                    var m = d.getMonth();
                    if (m !== lastMonth) {
                        labels.push({ label: months[m], col: weekIdx, key: m + '-' + d.getFullYear() });
                        lastMonth = m;
                    }
                    d.setDate(d.getDate() + 7);
                    weekIdx++;
                }

                return labels;
            },

            calendarSummaryText() {
                var weeks = this.buildCalendarGrid();
                var total = 0;
                for (var i = 0; i < weeks.length; i++) {
                    for (var j = 0; j < weeks[i].length; j++) {
                        if (!weeks[i][j].empty) {
                            total += weeks[i][j].value;
                        }
                    }
                }
                if (this.calendarMode === 'costs') {
                    return '$' + total.toFixed(2) + ' in the last year';
                }
                return total.toLocaleString() + ' tokens in the last year';
            },

            showCalendarTooltip(event, day) {
                if (day.empty) return;
                var label = '';
                if (this.calendarMode === 'costs') {
                    label = '$' + (day.value || 0).toFixed(4) + ' on ' + day.dateStr;
                } else {
                    label = (day.value || 0).toLocaleString() + ' tokens on ' + day.dateStr;
                }
                this.calendarTooltip = {
                    show: true,
                    x: event.clientX,
                    y: event.clientY,
                    text: label
                };
            },

            hideCalendarTooltip() {
                this.calendarTooltip = { show: false, x: 0, y: 0, text: '' };
            }
        };
    }

    global.dashboardContributionCalendarModule = dashboardContributionCalendarModule;
})(window);
