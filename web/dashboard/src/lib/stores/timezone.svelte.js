// Timezone preference + timezone-aware formatting. The effective timezone
// rides on every admin request as the X-GoModel-Timezone header so
// server-side day grouping matches the UI.

const DEFAULT_TIMEZONE = "UTC";
const TIMEZONE_STORAGE_KEY = "gomodel_timezone_override";
const formatterCache = new Map();
const supportedTimeZoneCache = new Map();

function pad(value) {
  return String(value).padStart(2, "0");
}

function browserStorage() {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}

function getCachedFormatter(locale, options) {
  const cacheKey = locale + "|" + JSON.stringify(options);
  if (!formatterCache.has(cacheKey)) {
    formatterCache.set(cacheKey, new Intl.DateTimeFormat(locale, options));
  }
  return formatterCache.get(cacheKey);
}

export function isSupportedTimeZone(zone) {
  if (!zone) return false;
  if (supportedTimeZoneCache.has(zone)) {
    return supportedTimeZoneCache.get(zone);
  }
  try {
    getCachedFormatter("en-US", { timeZone: zone }).format(new Date());
    supportedTimeZoneCache.set(zone, true);
    return true;
  } catch {
    supportedTimeZoneCache.set(zone, false);
    return false;
  }
}

function detectBrowserTimeZone() {
  try {
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
    if (isSupportedTimeZone(zone)) {
      return zone;
    }
  } catch {
    // Fall back to UTC when the runtime cannot resolve an IANA timezone.
  }
  return DEFAULT_TIMEZONE;
}

function loadTimezonePreference() {
  const storage = browserStorage();
  if (!storage) return "";
  let saved = "";
  try {
    saved = storage.getItem(TIMEZONE_STORAGE_KEY) || "";
  } catch {
    saved = "";
  }
  return isSupportedTimeZone(saved) ? saved : "";
}

class TimezoneStore {
  detectedTimezone = $state(DEFAULT_TIMEZONE);
  override = $state("");
  options = $state([]);
  optionsLoaded = $state(false);

  init() {
    this.detectedTimezone = detectBrowserTimeZone();
    this.override = loadTimezonePreference();
  }

  effectiveTimezone() {
    return this.override || this.detectedTimezone || DEFAULT_TIMEZONE;
  }

  dateKeyInTimeZone(date, timeZone) {
    const zone = isSupportedTimeZone(timeZone) ? timeZone : DEFAULT_TIMEZONE;
    const parts = getCachedFormatter("en-CA", {
      timeZone: zone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    }).formatToParts(date);
    const byType = {};
    parts.forEach((part) => {
      byType[part.type] = part.value;
    });
    return byType.year + "-" + byType.month + "-" + byType.day;
  }

  formatTimestampInTimeZone(ts, timeZone) {
    if (ts === null || ts === undefined) return "-";
    const date = new Date(ts);
    if (Number.isNaN(date.getTime())) return "-";
    const zone = isSupportedTimeZone(timeZone) ? timeZone : DEFAULT_TIMEZONE;
    const parts = getCachedFormatter("en-CA", {
      timeZone: zone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hourCycle: "h23",
    }).formatToParts(date);
    const byType = {};
    parts.forEach((part) => {
      byType[part.type] = part.value;
    });
    return (
      byType.year +
      "-" +
      byType.month +
      "-" +
      byType.day +
      " " +
      byType.hour +
      ":" +
      byType.minute +
      ":" +
      byType.second
    );
  }

  formatTimestamp(ts) {
    return this.formatTimestampInTimeZone(ts, this.effectiveTimezone());
  }

  currentDateKey(now) {
    return this.dateKeyInTimeZone(now || new Date(), this.effectiveTimezone());
  }

  dateKeyToDate(key) {
    if (!key) return null;
    const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(key);
    if (!match) return null;
    return new Date(
      Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3])),
    );
  }

  dateToDateKey(date) {
    if (!(date instanceof Date) || Number.isNaN(date.getTime())) {
      return "";
    }
    return (
      date.getUTCFullYear() +
      "-" +
      pad(date.getUTCMonth() + 1) +
      "-" +
      pad(date.getUTCDate())
    );
  }

  addDaysToDateKey(key, days) {
    const date = this.dateKeyToDate(key);
    if (!date) return "";
    date.setUTCDate(date.getUTCDate() + days);
    return this.dateToDateKey(date);
  }

  todayDate() {
    return this.dateKeyToDate(this.currentDateKey());
  }

  startOfMonthDate(date) {
    const value = date instanceof Date ? date : this.todayDate();
    return new Date(Date.UTC(value.getUTCFullYear(), value.getUTCMonth(), 1));
  }

  timeZoneOffsetLabel(zone, now) {
    const timeZone = isSupportedTimeZone(zone) ? zone : DEFAULT_TIMEZONE;
    try {
      const parts = getCachedFormatter("en-US", {
        timeZone,
        hour: "2-digit",
        minute: "2-digit",
        hourCycle: "h23",
        timeZoneName: "longOffset",
      }).formatToParts(now || new Date());
      const namePart = parts.find((part) => part.type === "timeZoneName");
      if (!namePart || !namePart.value) {
        return "UTC+00:00";
      }
      const value = namePart.value.replace("GMT", "UTC");
      return value === "UTC" ? "UTC+00:00" : value;
    } catch {
      return "UTC+00:00";
    }
  }

  timeZoneOffsetMinutes(zone, now) {
    const match = /^UTC([+-])(\d{2}):(\d{2})$/.exec(
      this.timeZoneOffsetLabel(zone, now),
    );
    if (!match) return 0;
    const minutes = Number(match[2]) * 60 + Number(match[3]);
    return match[1] === "-" ? -minutes : minutes;
  }

  timeZoneOptionLabel(zone, now) {
    return zone + " (" + this.timeZoneOffsetLabel(zone, now) + ")";
  }

  detectedTimeZoneLabel() {
    return this.timeZoneOptionLabel(this.detectedTimezone);
  }

  effectiveTimeZoneLabel() {
    return this.timeZoneOptionLabel(this.effectiveTimezone());
  }

  ensureOptions() {
    if (this.optionsLoaded) return;
    const now = new Date();
    let zones = [];
    try {
      if (typeof Intl.supportedValuesOf === "function") {
        zones = Intl.supportedValuesOf("timeZone");
      }
    } catch {
      zones = [];
    }
    [DEFAULT_TIMEZONE, this.detectedTimezone, this.override].forEach((zone) => {
      if (zone && zones.indexOf(zone) === -1 && isSupportedTimeZone(zone)) {
        zones.push(zone);
      }
    });
    zones = zones.filter((zone) => isSupportedTimeZone(zone));
    zones.sort((left, right) => {
      const offsetDiff =
        this.timeZoneOffsetMinutes(left, now) -
        this.timeZoneOffsetMinutes(right, now);
      if (offsetDiff !== 0) return offsetDiff;
      return left.localeCompare(right);
    });
    this.options = zones.map((zone) => ({
      value: zone,
      label: this.timeZoneOptionLabel(zone, now),
    }));
    this.optionsLoaded = true;
  }

  saveOverride() {
    const storage = browserStorage();
    if (storage) {
      if (this.override && isSupportedTimeZone(this.override)) {
        try {
          storage.setItem(TIMEZONE_STORAGE_KEY, this.override);
        } catch {
          // Ignore storage failures and keep the in-memory override active.
        }
      } else {
        try {
          storage.removeItem(TIMEZONE_STORAGE_KEY);
        } catch {
          // Ignore storage failures and still clear the in-memory override.
        }
        this.override = "";
      }
    }
    this.optionsLoaded = false;
    this.ensureOptions();
  }

  clearOverride() {
    const storage = browserStorage();
    if (storage) {
      try {
        storage.removeItem(TIMEZONE_STORAGE_KEY);
      } catch {
        // Ignore storage failures and still clear the in-memory override.
      }
    }
    this.override = "";
  }

  calendarTimeZoneText() {
    const suffix = this.override ? "manual override" : "auto-detected";
    return (
      "Activity grouped by " + this.effectiveTimeZoneLabel() + " (" + suffix + ")"
    );
  }
}

export const timezone = new TimezoneStore();
