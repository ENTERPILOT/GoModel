// Shared reporting window (preset "last N days" or a custom range) used by
// the overview, usage, audit, and settings pages. All math is UTC-day based
// in the effective timezone.
//
// The window is remembered in localStorage across sessions. A window that
// ended on today keeps tracking today (it slides forward on a day change);
// one that ended in the past stays put. See dateRangePrefs.js.

import { timezone } from "./timezone.svelte.js";
import { formatDateParam } from "$lib/utils/format.js";
import { readStored, writeStored } from "$lib/utils/storage.js";
import {
  DATE_RANGE_STORAGE_KEY,
  DEFAULT_PRESET_DAYS,
  parseDateRange,
  serializeDateRange,
  windowEndingToday,
} from "./dateRangePrefs.js";
import {
  rangeChartTitle,
  rangeLabel,
  rangeSpanLabel,
} from "./dateRangeText.js";

// How often an open dashboard re-checks whether the day rolled over.
const DAY_SYNC_INTERVAL_MS = 60_000;

class DateRangeStore {
  days = $state(DEFAULT_PRESET_DAYS);
  selectedPreset = $state(DEFAULT_PRESET_DAYS);
  customStartDate = $state(null);
  customEndDate = $state(null);
  // True while the custom window ends on today, i.e. it follows the day.
  followsToday = $state(false);
  interval = $state("daily");
  // Bumped whenever a day rollover moved the window on its own. DatePicker
  // watches it and fires its onchange, so the page reloads its data instead
  // of showing yesterday's numbers under today's dates.
  syncTick = $state(0);

  // init restores the saved window and keeps a today-anchored one current.
  // Call after timezone.init(), since "today" is timezone dependent.
  init() {
    this.restore();
    this.syncToToday();
    if (typeof setInterval === "function") {
      setInterval(() => this.syncToToday(), DAY_SYNC_INTERVAL_MS);
    }
  }

  // queryStr renders the window as usage-endpoint query params.
  queryStr() {
    if (this.customStartDate && this.customEndDate) {
      return (
        "start_date=" +
        formatDateParam(this.customStartDate) +
        "&end_date=" +
        formatDateParam(this.customEndDate)
      );
    }
    return "days=" + this.days;
  }

  selectPreset(days) {
    this.selectedPreset = days;
    this.customStartDate = null;
    this.customEndDate = null;
    this.followsToday = false;
    this.days = days;
    this.persist();
  }

  // selectStart opens a custom window; the end defaults to today so the
  // picker always has a complete range while the user picks the other edge.
  selectStart(date) {
    this.selectedPreset = null;
    this.customStartDate = date;
    if (this.customEndDate && this.customEndDate < date) {
      this.customEndDate = date;
    }
    if (!this.customEndDate) {
      this.customEndDate = timezone.todayDate();
    }
    this.afterCustomChange();
  }

  // selectEnd closes the window, swapping the edges when the click lands
  // before the start.
  selectEnd(date) {
    this.selectedPreset = null;
    if (this.customStartDate && date < this.customStartDate) {
      this.customEndDate = this.customStartDate;
      this.customStartDate = date;
    } else {
      this.customEndDate = date;
      if (!this.customStartDate) this.customStartDate = date;
    }
    this.afterCustomChange();
  }

  // syncToToday slides a today-anchored window forward when the day rolled
  // over. Presets are computed from today already, so they need no shift.
  // Returns true when the window moved.
  syncToToday() {
    if (!this.followsToday || !this.customStartDate || !this.customEndDate) {
      return false;
    }
    const todayKey = timezone.currentDateKey();
    const endKey = timezone.dateToDateKey(this.customEndDate);
    if (!todayKey || endKey === todayKey) return false;

    const next = windowEndingToday(
      timezone.dateToDateKey(this.customStartDate),
      endKey,
      todayKey,
    );
    this.customStartDate = timezone.dateKeyToDate(next.start);
    this.customEndDate = timezone.dateKeyToDate(next.end);
    this.persist();
    this.syncTick++;
    return true;
  }

  dateRangeLabel() {
    return rangeLabel({
      selectedPreset: this.selectedPreset,
      startKey: timezone.dateToDateKey(this.customStartDate),
      endKey: timezone.dateToDateKey(this.customEndDate),
      todayKey: timezone.currentDateKey(),
    });
  }

  dateRangeSpanLabel() {
    return rangeSpanLabel({
      selectedPreset: this.selectedPreset,
      startKey: timezone.dateToDateKey(this.customStartDate),
      endKey: timezone.dateToDateKey(this.customEndDate),
      followsToday: this.followsToday,
      todayKey: timezone.currentDateKey(),
    });
  }

  rangeStart() {
    if (this.customStartDate) return this.customStartDate;
    if (this.selectedPreset) {
      return timezone.dateKeyToDate(
        timezone.addDaysToDateKey(
          timezone.currentDateKey(),
          -(parseInt(this.selectedPreset, 10) - 1),
        ),
      );
    }
    return null;
  }

  rangeEnd() {
    if (this.customEndDate) return this.customEndDate;
    if (this.customStartDate || this.selectedPreset) {
      return timezone.todayDate();
    }
    return null;
  }

  chartTitle() {
    return rangeChartTitle(this.interval);
  }

  // --- persistence ---

  afterCustomChange() {
    this.followsToday =
      timezone.dateToDateKey(this.customEndDate) === timezone.currentDateKey();
    this.persist();
  }

  restore() {
    const stored = parseDateRange(readStored(DATE_RANGE_STORAGE_KEY));
    if (!stored) return;
    if (stored.mode === "preset") {
      this.selectPreset(stored.days);
      return;
    }
    this.selectedPreset = null;
    this.customStartDate = timezone.dateKeyToDate(stored.start);
    this.customEndDate = timezone.dateKeyToDate(stored.end);
    this.followsToday = stored.follow;
  }

  persist() {
    const payload = serializeDateRange({
      selectedPreset: this.selectedPreset,
      startKey: timezone.dateToDateKey(this.customStartDate),
      endKey: timezone.dateToDateKey(this.customEndDate),
      followsToday: this.followsToday,
    });
    if (payload) writeStored(DATE_RANGE_STORAGE_KEY, JSON.stringify(payload));
  }
}

export const dateRange = new DateRangeStore();
