(function(global) {
    // Number of active intensity levels (1..CALENDAR_LEVELS); level 0 means no
    // activity. Keep in sync with the --cal-level-* ramp and .level-N rules in
    // dashboard.css.
    var CALENDAR_LEVELS = 10;
    // Exponent (<1) for the intensity scale; see calendarLevel() for rationale.
    var CALENDAR_SCALE_EXPONENT = 0.7;

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
                    const res = await fetch('/admin/usage/daily?days=365&interval=daily', options);
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

                // Align start to Sunday so each week column reads top-to-bottom
                // Sun, Mon, ..., Sat — matching the Mon/Wed/Fri day labels (GitHub style).
                var dayOfWeek = start.getUTCDay(); // 0 = Sunday
                start.setUTCDate(start.getUTCDate() - dayOfWeek);

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

                // Calculate levels relative to the busiest day so intensity
                // reflects magnitude (a 10K day reads much lighter than a 1M day).
                var max = 0;
                for (var i = 0; i < days.length; i++) {
                    if (days[i].value > max) max = days[i].value;
                }

                for (var i = 0; i < days.length; i++) {
                    days[i].level = this.calendarLevel(days[i].value, max);
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

            calendarLevel(value, max) {
                if (value <= 0 || max <= 0) return 0;
                // Power scale (exponent < 1) against the busiest day. Token
                // counts span orders of magnitude. A linear ratio collapses every
                // quiet day to the lightest shade, while a pure log scale flattens
                // the busy end so a 1M and a 1.5M day land on the same shade. A
                // ~0.7 exponent sits between the two: it keeps high-volume days
                // separated (so big days stay distinguishable) while still
                // lifting quiet days off the lightest level. Spreading the result
                // across CALENDAR_LEVELS buckets gives a gradual ramp instead of
                // a handful of coarse steps.
                var ratio = Math.pow(value / max, CALENDAR_SCALE_EXPONENT);
                var level = Math.ceil(ratio * CALENDAR_LEVELS);
                if (level < 1) return 1;
                if (level > CALENDAR_LEVELS) return CALENDAR_LEVELS;
                return level;
            },

            calendarLegendLevels() {
                // 0 (empty) .. CALENDAR_LEVELS, matching calendarLevel()'s range
                // and the --cal-level-* ramp so the legend always mirrors the grid.
                var levels = [];
                for (var i = 0; i <= CALENDAR_LEVELS; i++) {
                    levels.push(i);
                }
                return levels;
            },

            toggleCalendarMode(mode) {
                this.calendarMode = mode;
            },

            calendarMonthLabels() {
                var todayKey = this.currentDateKey();
                var today = this.dateKeyToDate(todayKey);
                var start = this.dateKeyToDate(this.addDaysToDateKey(todayKey, -364));
                // Sunday-align to match buildCalendarGrid's week columns.
                var dayOfWeek = start.getUTCDay(); // 0 = Sunday
                start.setUTCDate(start.getUTCDate() - dayOfWeek);

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
