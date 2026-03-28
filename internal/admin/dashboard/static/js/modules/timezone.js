(function(global) {
    const DEFAULT_TIMEZONE = 'UTC';
    const TIMEZONE_STORAGE_KEY = 'gomodel_timezone_override';

    function pad(value) {
        return String(value).padStart(2, '0');
    }

    function browserStorage() {
        return global.localStorage || null;
    }

    function dashboardTimezoneModule() {
        return {
            detectedTimezone: DEFAULT_TIMEZONE,
            timezoneOverride: '',
            timezoneOptions: [],
            timezoneOptionsLoaded: false,

            initTimeZoneState() {
                this.detectedTimezone = this.detectBrowserTimeZone();
                this.loadTimezonePreference();
                this.calendarMonth = this.startOfMonthDate(this.customEndDate || this.todayDate());
            },

            detectBrowserTimeZone() {
                try {
                    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
                    if (this.isSupportedTimeZone(zone)) {
                        return zone;
                    }
                } catch (_) {
                    // Fall back to UTC when the runtime cannot resolve an IANA timezone.
                }
                return DEFAULT_TIMEZONE;
            },

            isSupportedTimeZone(zone) {
                if (!zone) {
                    return false;
                }
                try {
                    new Intl.DateTimeFormat('en-US', { timeZone: zone }).format(new Date());
                    return true;
                } catch (_) {
                    return false;
                }
            },

            loadTimezonePreference() {
                const storage = browserStorage();
                if (!storage) {
                    this.timezoneOverride = '';
                    return;
                }

                const saved = storage.getItem(TIMEZONE_STORAGE_KEY) || '';
                this.timezoneOverride = this.isSupportedTimeZone(saved) ? saved : '';
            },

            effectiveTimezone() {
                return this.timezoneOverride || this.detectedTimezone || DEFAULT_TIMEZONE;
            },

            dateKeyInTimeZone(date, timeZone) {
                const zone = this.isSupportedTimeZone(timeZone) ? timeZone : DEFAULT_TIMEZONE;
                const parts = new Intl.DateTimeFormat('en-CA', {
                    timeZone: zone,
                    year: 'numeric',
                    month: '2-digit',
                    day: '2-digit'
                }).formatToParts(date);

                const byType = {};
                parts.forEach((part) => {
                    byType[part.type] = part.value;
                });

                return byType.year + '-' + byType.month + '-' + byType.day;
            },

            currentDateKey(now) {
                return this.dateKeyInTimeZone(now || new Date(), this.effectiveTimezone());
            },

            dateKeyToDate(key) {
                if (!key) {
                    return null;
                }

                const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(key);
                if (!match) {
                    return null;
                }

                return new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3])));
            },

            dateToDateKey(date) {
                if (!(date instanceof Date) || Number.isNaN(date.getTime())) {
                    return '';
                }

                return date.getUTCFullYear() + '-' +
                    pad(date.getUTCMonth() + 1) + '-' +
                    pad(date.getUTCDate());
            },

            addDaysToDateKey(key, days) {
                const date = this.dateKeyToDate(key);
                if (!date) {
                    return '';
                }

                date.setUTCDate(date.getUTCDate() + days);
                return this.dateToDateKey(date);
            },

            todayDate() {
                return this.dateKeyToDate(this.currentDateKey());
            },

            startOfMonthDate(date) {
                const value = date instanceof Date ? date : this.todayDate();
                return new Date(Date.UTC(value.getUTCFullYear(), value.getUTCMonth(), 1));
            },

            timeZoneOffsetLabel(zone, now) {
                const timeZone = this.isSupportedTimeZone(zone) ? zone : DEFAULT_TIMEZONE;
                try {
                    const parts = new Intl.DateTimeFormat('en-US', {
                        timeZone: timeZone,
                        hour: '2-digit',
                        minute: '2-digit',
                        hourCycle: 'h23',
                        timeZoneName: 'longOffset'
                    }).formatToParts(now || new Date());

                    const namePart = parts.find((part) => part.type === 'timeZoneName');
                    if (!namePart || !namePart.value) {
                        return 'UTC+00:00';
                    }

                    const value = namePart.value.replace('GMT', 'UTC');
                    return value === 'UTC' ? 'UTC+00:00' : value;
                } catch (_) {
                    return 'UTC+00:00';
                }
            },

            timeZoneOffsetMinutes(zone, now) {
                const match = /^UTC([+-])(\d{2}):(\d{2})$/.exec(this.timeZoneOffsetLabel(zone, now));
                if (!match) {
                    return 0;
                }

                const minutes = Number(match[2]) * 60 + Number(match[3]);
                return match[1] === '-' ? -minutes : minutes;
            },

            timeZoneOptionLabel(zone, now) {
                return zone + ' (' + this.timeZoneOffsetLabel(zone, now) + ')';
            },

            detectedTimeZoneLabel() {
                return this.timeZoneOptionLabel(this.detectedTimezone);
            },

            effectiveTimeZoneLabel() {
                return this.timeZoneOptionLabel(this.effectiveTimezone());
            },

            ensureTimezoneOptions() {
                if (this.timezoneOptionsLoaded) {
                    return;
                }

                const now = new Date();
                let zones = [];
                try {
                    if (typeof Intl.supportedValuesOf === 'function') {
                        zones = Intl.supportedValuesOf('timeZone');
                    }
                } catch (_) {
                    zones = [];
                }

                [DEFAULT_TIMEZONE, this.detectedTimezone, this.timezoneOverride].forEach((zone) => {
                    if (zone && zones.indexOf(zone) === -1 && this.isSupportedTimeZone(zone)) {
                        zones.push(zone);
                    }
                });

                zones = zones.filter((zone) => this.isSupportedTimeZone(zone));
                zones.sort((left, right) => {
                    const offsetDiff = this.timeZoneOffsetMinutes(left, now) - this.timeZoneOffsetMinutes(right, now);
                    if (offsetDiff !== 0) {
                        return offsetDiff;
                    }
                    return left.localeCompare(right);
                });

                this.timezoneOptions = zones.map((zone) => ({
                    value: zone,
                    label: this.timeZoneOptionLabel(zone, now)
                }));
                this.timezoneOptionsLoaded = true;
            },

            saveTimezoneOverride() {
                const storage = browserStorage();
                if (storage) {
                    if (this.timezoneOverride && this.isSupportedTimeZone(this.timezoneOverride)) {
                        storage.setItem(TIMEZONE_STORAGE_KEY, this.timezoneOverride);
                    } else {
                        storage.removeItem(TIMEZONE_STORAGE_KEY);
                        this.timezoneOverride = '';
                    }
                }

                this.timezoneOptionsLoaded = false;
                this.ensureTimezoneOptions();
                this.calendarMonth = this.startOfMonthDate(this.customEndDate || this.todayDate());
                this.refreshTimeZoneData();
            },

            clearTimezoneOverride() {
                const storage = browserStorage();
                if (storage) {
                    storage.removeItem(TIMEZONE_STORAGE_KEY);
                }
                this.timezoneOverride = '';
                this.calendarMonth = this.startOfMonthDate(this.customEndDate || this.todayDate());
                this.refreshTimeZoneData();
            },

            refreshTimeZoneData() {
                if (typeof this.fetchAll === 'function') {
                    this.fetchAll();
                }
                if (this.page === 'audit-logs' && typeof this.fetchAuditLog === 'function') {
                    this.fetchAuditLog(true);
                }
            },

            calendarTimeZoneText() {
                const suffix = this.timezoneOverride ? 'manual override' : 'auto-detected';
                return 'Activity grouped by ' + this.effectiveTimeZoneLabel() + ' (' + suffix + ')';
            },

            timestampTitle(ts) {
                if (!ts || typeof this.formatTimestampUTC !== 'function') {
                    return '';
                }
                return 'UTC: ' + this.formatTimestampUTC(ts);
            }
        };
    }

    global.dashboardTimezoneModule = dashboardTimezoneModule;
})(window);
