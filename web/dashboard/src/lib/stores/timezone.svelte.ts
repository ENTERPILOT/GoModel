// Timezone preference + timezone-aware formatting. The effective timezone
// rides on every admin request as the X-GoModel-Timezone header so
// server-side day grouping matches the UI.

import { browserStorage } from "$lib/utils/storage.ts";

const DEFAULT_TIMEZONE = "UTC";
const TIMEZONE_STORAGE_KEY = "gomodel_timezone_override";
const formatterCache = new Map<string, Intl.DateTimeFormat>();
const supportedTimeZoneCache = new Map<string, boolean>();

function pad(value: number): string {
  return String(value).padStart(2, "0");
}

function getCachedFormatter(
  locale: string,
  options: Intl.DateTimeFormatOptions,
): Intl.DateTimeFormat {
  const cacheKey = locale + "|" + JSON.stringify(options);
  let formatter = formatterCache.get(cacheKey);
  if (!formatter) {
    formatter = new Intl.DateTimeFormat(locale, options);
    formatterCache.set(cacheKey, formatter);
  }
  return formatter;
}

export function isSupportedTimeZone(zone: string | null | undefined): boolean {
  if (!zone) return false;
  const cached = supportedTimeZoneCache.get(zone);
  if (cached !== undefined) {
    return cached;
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

function detectBrowserTimeZone(): string {
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

function loadTimezonePreference(): string {
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

function formatToPartsMap(
  formatter: Intl.DateTimeFormat,
  date: Date,
): Record<string, string> {
  const byType: Record<string, string> = {};
  formatter.formatToParts(date).forEach((part) => {
    byType[part.type] = part.value;
  });
  return byType;
}

class TimezoneStore {
  detectedTimezone = $state(DEFAULT_TIMEZONE);
  override = $state("");
  options = $state<{ value: string; label: string }[]>([]);
  optionsLoaded = $state(false);

  init(): void {
    this.detectedTimezone = detectBrowserTimeZone();
    this.override = loadTimezonePreference();
  }

  effectiveTimezone(): string {
    return this.override || this.detectedTimezone || DEFAULT_TIMEZONE;
  }

  dateKeyInTimeZone(date: Date, timeZone: string): string {
    const zone = isSupportedTimeZone(timeZone) ? timeZone : DEFAULT_TIMEZONE;
    const byType = formatToPartsMap(
      getCachedFormatter("en-CA", {
        timeZone: zone,
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
      }),
      date,
    );
    return byType.year + "-" + byType.month + "-" + byType.day;
  }

  formatTimestampInTimeZone(
    ts: string | number | Date | null | undefined,
    timeZone: string,
  ): string {
    if (ts === null || ts === undefined) return "-";
    const date = new Date(ts);
    if (Number.isNaN(date.getTime())) return "-";
    const zone = isSupportedTimeZone(timeZone) ? timeZone : DEFAULT_TIMEZONE;
    const byType = formatToPartsMap(
      getCachedFormatter("en-CA", {
        timeZone: zone,
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
        hourCycle: "h23",
      }),
      date,
    );
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

  formatTimestamp(ts: string | number | Date | null | undefined): string {
    return this.formatTimestampInTimeZone(ts, this.effectiveTimezone());
  }

  currentDateKey(now?: Date): string {
    return this.dateKeyInTimeZone(now || new Date(), this.effectiveTimezone());
  }

  dateKeyToDate(key: string | null | undefined): Date | null {
    if (!key) return null;
    const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(key);
    if (!match) return null;
    return new Date(
      Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3])),
    );
  }

  dateToDateKey(date: Date | null | undefined): string {
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

  addDaysToDateKey(key: string, days: number): string {
    const date = this.dateKeyToDate(key);
    if (!date) return "";
    date.setUTCDate(date.getUTCDate() + days);
    return this.dateToDateKey(date);
  }

  // currentDateKey always yields a parseable key, so this never returns null
  // in practice; the fallback keeps the signature honest.
  todayDate(): Date | null {
    return this.dateKeyToDate(this.currentDateKey());
  }

  startOfMonthDate(date?: Date | null): Date {
    const value = date instanceof Date ? date : (this.todayDate() as Date);
    return new Date(Date.UTC(value.getUTCFullYear(), value.getUTCMonth(), 1));
  }

  timeZoneOffsetLabel(zone: string, now?: Date): string {
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

  timeZoneOffsetMinutes(zone: string, now?: Date): number {
    const match = /^UTC([+-])(\d{2}):(\d{2})$/.exec(
      this.timeZoneOffsetLabel(zone, now),
    );
    if (!match) return 0;
    const minutes = Number(match[2]) * 60 + Number(match[3]);
    return match[1] === "-" ? -minutes : minutes;
  }

  timeZoneOptionLabel(zone: string, now?: Date): string {
    return zone + " (" + this.timeZoneOffsetLabel(zone, now) + ")";
  }

  detectedTimeZoneLabel(): string {
    return this.timeZoneOptionLabel(this.detectedTimezone);
  }

  effectiveTimeZoneLabel(): string {
    return this.timeZoneOptionLabel(this.effectiveTimezone());
  }

  ensureOptions(): void {
    if (this.optionsLoaded) return;
    const now = new Date();
    let zones: string[] = [];
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

  saveOverride(): void {
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

  clearOverride(): void {
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

  calendarTimeZoneText(): string {
    const suffix = this.override ? "manual override" : "auto-detected";
    return (
      "Activity grouped by " + this.effectiveTimeZoneLabel() + " (" + suffix + ")"
    );
  }
}

export const timezone = new TimezoneStore();
