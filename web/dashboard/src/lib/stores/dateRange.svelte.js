// Shared reporting window (preset "last N days" or a custom range) used by
// the overview, usage, audit, and settings pages. All math is UTC-day based
// in the effective timezone.

import { timezone } from "./timezone.svelte.js";
import { formatDateParam } from "$lib/utils/format.js";

class DateRangeStore {
  days = $state("30");
  selectedPreset = $state("30");
  customStartDate = $state(null);
  customEndDate = $state(null);
  interval = $state("daily");

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
    this.days = days;
  }

  dateRangeLabel() {
    if (this.selectedPreset) return "Last " + this.selectedPreset + " days";
    if (this.customStartDate && this.customEndDate) {
      return (
        this.formatDateShort(this.customStartDate) +
        " – " +
        this.formatDateShort(this.customEndDate)
      );
    }
    if (this.customStartDate) {
      return this.formatDateShort(this.customStartDate) + " – ...";
    }
    return "Last 30 days";
  }

  formatDateShort(date) {
    const months = [
      "Jan",
      "Feb",
      "Mar",
      "Apr",
      "May",
      "Jun",
      "Jul",
      "Aug",
      "Sep",
      "Oct",
      "Nov",
      "Dec",
    ];
    return (
      months[date.getUTCMonth()] +
      " " +
      date.getUTCDate() +
      ", " +
      date.getUTCFullYear()
    );
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
    const titles = {
      daily: "Daily",
      weekly: "Weekly",
      monthly: "Monthly",
      yearly: "Yearly",
    };
    return (titles[this.interval] || "Daily") + " Token Usage";
  }
}

export const dateRange = new DateRangeStore();
