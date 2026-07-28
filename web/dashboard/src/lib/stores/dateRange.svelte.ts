// Shared reporting window (preset "last N days" or a custom range) used by
// the overview, usage, audit, and settings pages. All math is UTC-day based
// in the effective timezone.

import { timezone } from "./timezone.svelte.ts";
import { formatDateParam } from "$lib/utils/format.ts";

class DateRangeStore {
  days = $state("30");
  selectedPreset = $state("30");
  customStartDate = $state<Date | null>(null);
  customEndDate = $state<Date | null>(null);
  interval = $state("daily");

  // queryStr renders the window as usage-endpoint query params.
  queryStr(): string {
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

  selectPreset(days: string): void {
    this.selectedPreset = days;
    this.customStartDate = null;
    this.customEndDate = null;
    this.days = days;
  }

  dateRangeLabel(): string {
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

  formatDateShort(date: Date): string {
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

  rangeStart(): Date | null {
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

  rangeEnd(): Date | null {
    if (this.customEndDate) return this.customEndDate;
    if (this.customStartDate || this.selectedPreset) {
      return timezone.todayDate();
    }
    return null;
  }

  chartTitle(): string {
    const titles: Record<string, string> = {
      daily: "Daily",
      weekly: "Weekly",
      monthly: "Monthly",
      yearly: "Yearly",
    };
    return (titles[this.interval] || "Daily") + " Token Usage";
  }
}

export const dateRange = new DateRangeStore();
