(function(global) {
    function dashboardContributionCalendarModule() {
        return {
            calendarData: [],
            calendarMode: 'tokens',
            calendarLoading: false,
            calendarTooltip: { show: false, x: 0, y: 0, text: '' },

            async fetchCalendarData() {
                const controller = typeof this._startAbortableRequest === 'function'
                    ? this._startAbortableRequest('_calendarFetchController')
                    : null;
                const isCurrentRequest = () => {
                    if (!controller) {
                        return true;
                    }
                    if (typeof this._isCurrentAbortableRequest === 'function') {
                        return this._isCurrentAbortableRequest('_calendarFetchController', controller);
                    }
                    return this._calendarFetchController === controller && !controller.signal.aborted;
                };
                this.calendarLoading = true;
                try {
                    const options = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    if (controller) {
                        options.signal = controller.signal;
                    }
                    const res = await fetch('/admin/api/v1/usage/daily?days=365&interval=daily', options);
                    if (!isCurrentRequest()) {
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'calendar', options);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        if (!isCurrentRequest()) {
                            return;
                        }
                        this.calendarData = [];
                        return;
                    }
                    const payload = await res.json();
                    if (!isCurrentRequest()) {
                        return;
                    }
                    this.calendarData = payload;
                } catch (e) {
                    if (typeof this._isAbortError === 'function' && this._isAbortError(e)) {
                        return;
                    }
                    if (!isCurrentRequest()) {
                        return;
                    }
                    console.error('Failed to fetch calendar data:', e);
                    this.calendarData = [];
                } finally {
                    const currentRequest = isCurrentRequest();
                    if (currentRequest && typeof this._clearAbortableRequest === 'function') {
                        this._clearAbortableRequest('_calendarFetchController', controller);
                    }
                    if (currentRequest) {
                        this.calendarLoading = false;
                    }
                }
            },

            buildCalendarGrid() {
                var byDate = {};
                (this.calendarData || []).forEach(function(d) { byDate[d.date] = d; });

                var todayKey = this.currentDateKey();
                var start = this.dateKeyToDate(this.addDaysToDateKey(todayKey, -364));

                // Align start to Monday (ISO week start)
                var dayOfWeek = start.getUTCDay();
                var diff = dayOfWeek === 0 ? -6 : 1 - dayOfWeek;
                start.setUTCDate(start.getUTCDate() + diff);

                var mode = this.calendarMode;
                var days = [];
                for (var d = new Date(start); this.dateToDateKey(d) <= todayKey; d.setUTCDate(d.getUTCDate() + 1)) {
                    var key = this.dateToDateKey(d);
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
                var todayKey = this.currentDateKey();
                var today = this.dateKeyToDate(todayKey);
                var start = this.dateKeyToDate(this.addDaysToDateKey(todayKey, -364));
                var dayOfWeek = start.getUTCDay();
                var diff = dayOfWeek === 0 ? -6 : 1 - dayOfWeek;
                start.setUTCDate(start.getUTCDate() + diff);

                var months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
                var labels = [];
                var seenMonths = {};
                var totalWeeks = 0;

                for (var weekStart = new Date(start); this.dateToDateKey(weekStart) <= todayKey; weekStart.setUTCDate(weekStart.getUTCDate() + 7), totalWeeks++) {
                    var labelDay = null;

                    if (totalWeeks === 0) {
                        labelDay = new Date(weekStart);
                    } else {
                        for (var offset = 0; offset < 7; offset++) {
                            var d = new Date(weekStart);
                            d.setUTCDate(weekStart.getUTCDate() + offset);
                            if (this.dateToDateKey(d) > todayKey) {
                                break;
                            }
                            if (d.getUTCDate() === 1) {
                                labelDay = d;
                                break;
                            }
                        }
                    }

                    if (!labelDay) {
                        continue;
                    }

                    var monthKey = labelDay.getUTCFullYear() + '-' + labelDay.getUTCMonth();
                    if (seenMonths[monthKey]) {
                        continue;
                    }

                    labels.push({
                        label: months[labelDay.getUTCMonth()],
                        col: totalWeeks,
                        key: monthKey
                    });
                    seenMonths[monthKey] = true;
                }

                for (var i = 0; i < labels.length; i++) {
                    var nextCol = i + 1 < labels.length ? labels[i + 1].col : totalWeeks;
                    labels[i].span = Math.max(1, nextCol - labels[i].col);
                }

                return labels;
            },

            calendarSummaryText() {
                var total = 0;
                var data = this.calendarData || [];
                for (var i = 0; i < data.length; i++) {
                    var entry = data[i];
                    if (!entry) {
                        continue;
                    }
                    if (this.calendarMode === 'costs') {
                        if (entry.total_cost) {
                            total += entry.total_cost;
                        }
                        continue;
                    }
                    if (entry.total_tokens) {
                        total += entry.total_tokens;
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
