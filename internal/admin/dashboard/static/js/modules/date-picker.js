(function(global) {
    function dashboardDatePickerModule() {
        return {
            toggleDatePicker() {
                this.datePickerOpen = !this.datePickerOpen;
                if (this.datePickerOpen) {
                    this.calendarMonth = this.startOfMonthDate(this.customEndDate || this.todayDate());
                    this.selectingDate = 'start';
                }
            },

            closeDatePicker() {
                this.datePickerOpen = false;
                this.cursorHint.show = false;
            },

            onCalendarMouseMove(e) {
                this.cursorHint = { show: true, x: e.clientX, y: e.clientY };
            },

            onCalendarMouseLeave() {
                this.cursorHint.show = false;
            },

            selectPreset(days) {
                this.selectedPreset = days;
                this.customStartDate = null;
                this.customEndDate = null;
                this.selectingDate = 'start';
                this.days = days;
                this.fetchUsage();
                this.closeDatePicker();
            },

            selectionHint() {
                return this.selectingDate === 'end' ? 'Select end date' : 'Select start date';
            },

            dateRangeLabel() {
                if (this.selectedPreset) return 'Last ' + this.selectedPreset + ' days';
                if (this.customStartDate && this.customEndDate) {
                    return this.formatDateShort(this.customStartDate) + ' \u2013 ' + this.formatDateShort(this.customEndDate);
                }
                if (this.customStartDate) {
                    return this.formatDateShort(this.customStartDate) + ' \u2013 ...';
                }
                return 'Last 30 days';
            },

            formatDateShort(date) {
                const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
                return months[date.getUTCMonth()] + ' ' + date.getUTCDate() + ', ' + date.getUTCFullYear();
            },

            calendarTitle(offset) {
                const d = new Date(Date.UTC(
                    this.calendarMonth.getUTCFullYear(),
                    this.calendarMonth.getUTCMonth() + offset,
                    1
                ));
                const months = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'];
                return months[d.getUTCMonth()] + ' ' + d.getUTCFullYear();
            },

            calendarDays(offset) {
                const year = this.calendarMonth.getUTCFullYear();
                const month = this.calendarMonth.getUTCMonth() + offset;
                const first = new Date(Date.UTC(year, month, 1));
                const last = new Date(Date.UTC(year, month + 1, 0));
                let startDay = (first.getUTCDay() + 6) % 7;
                const days = [];

                const prevLast = new Date(Date.UTC(year, month, 0));
                for (let i = startDay - 1; i >= 0; i--) {
                    const d = prevLast.getUTCDate() - i;
                    const date = new Date(Date.UTC(year, month - 1, d));
                    days.push({ day: d, date, current: false, key: 'p-' + this.dateToDateKey(date) });
                }

                for (let d = 1; d <= last.getUTCDate(); d++) {
                    const date = new Date(Date.UTC(year, month, d));
                    days.push({ day: d, date, current: true, key: 'c-' + this.dateToDateKey(date) });
                }

                const remaining = 42 - days.length;
                for (let d = 1; d <= remaining; d++) {
                    const date = new Date(Date.UTC(year, month + 1, d));
                    days.push({ day: d, date, current: false, key: 'n-' + this.dateToDateKey(date) });
                }

                return days;
            },

            prevMonth() {
                this.calendarMonth = new Date(Date.UTC(
                    this.calendarMonth.getUTCFullYear(),
                    this.calendarMonth.getUTCMonth() - 1,
                    1
                ));
            },

            nextMonth() {
                const next = new Date(Date.UTC(
                    this.calendarMonth.getUTCFullYear(),
                    this.calendarMonth.getUTCMonth() + 1,
                    1
                ));
                const currentMonth = this.startOfMonthDate(this.todayDate());
                if (next.getTime() <= currentMonth.getTime()) {
                    this.calendarMonth = next;
                }
            },

            isCurrentMonth() {
                const today = this.todayDate();
                return this.calendarMonth.getUTCFullYear() === today.getUTCFullYear() &&
                    this.calendarMonth.getUTCMonth() === today.getUTCMonth();
            },

            selectCalendarDay(day) {
                if (!day.current || this.isFutureDay(day)) return;
                const clicked = new Date(day.date);
                this.selectedPreset = null;

                if (this.selectingDate === 'start') {
                    this.customStartDate = clicked;
                    if (this.customEndDate && this.customEndDate < clicked) {
                        this.customEndDate = clicked;
                    }
                    if (!this.customEndDate) {
                        this.customEndDate = this.todayDate();
                    }
                    this.selectingDate = 'end';
                    this.fetchUsage();
                } else {
                    if (clicked < this.customStartDate) {
                        this.customEndDate = this.customStartDate;
                        this.customStartDate = clicked;
                    } else {
                        this.customEndDate = clicked;
                    }
                    this.selectingDate = 'start';
                    this.fetchUsage();
                    this.closeDatePicker();
                }
            },

            isToday(day) {
                if (!day.current) return false;
                return this.dateToDateKey(day.date) === this.currentDateKey();
            },

            isFutureDay(day) {
                return this.dateToDateKey(day.date) > this.currentDateKey();
            },

            isRangeStart(day) {
                if (!day.current) return false;
                const start = this._rangeStart();
                if (!start) return false;
                return this.dateToDateKey(day.date) === this.dateToDateKey(start);
            },

            isRangeEnd(day) {
                if (!day.current) return false;
                const end = this._rangeEnd();
                if (!end) return false;
                return this.dateToDateKey(day.date) === this.dateToDateKey(end);
            },

            isInRange(day) {
                if (!day.current) return false;
                const start = this._rangeStart();
                const end = this._rangeEnd();
                if (!start || !end) return false;
                const dayKey = this.dateToDateKey(day.date);
                return dayKey >= this.dateToDateKey(start) && dayKey <= this.dateToDateKey(end);
            },

            _rangeStart() {
                if (this.customStartDate) return this.customStartDate;
                if (this.selectedPreset) {
                    return this.dateKeyToDate(
                        this.addDaysToDateKey(this.currentDateKey(), -(parseInt(this.selectedPreset, 10) - 1))
                    );
                }
                return null;
            },

            _rangeEnd() {
                if (this.customEndDate) return this.customEndDate;
                if (this.customStartDate || this.selectedPreset) {
                    return this.todayDate();
                }
                return null;
            },

            setInterval(val) {
                this.interval = val;
                this.fetchUsage();
            },

            chartTitle() {
                const titles = { daily: 'Daily', weekly: 'Weekly', monthly: 'Monthly', yearly: 'Yearly' };
                return (titles[this.interval] || 'Daily') + ' Token Usage';
            }
        };
    }

    global.dashboardDatePickerModule = dashboardDatePickerModule;
})(window);
