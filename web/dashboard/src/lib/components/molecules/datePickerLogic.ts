// Pure calendar-grid math for DatePicker. Everything here works in UTC and
// takes explicit inputs so it can be unit-tested without a DOM or a store.

const MONTH_NAMES = [
  "January",
  "February",
  "March",
  "April",
  "May",
  "June",
  "July",
  "August",
  "September",
  "October",
  "November",
  "December",
];

/** Month heading for `calendarMonth` shifted by `offset` months. */
export function calendarTitle(calendarMonth: Date, offset: number): string {
  const d = new Date(
    Date.UTC(calendarMonth.getUTCFullYear(), calendarMonth.getUTCMonth() + offset, 1),
  );
  return MONTH_NAMES[d.getUTCMonth()] + " " + d.getUTCFullYear();
}

export interface CalendarDay {
  day: number;
  date: Date;
  current: boolean;
  key: string;
}

/**
 * The 42 cells (6 weeks, Monday first) of `calendarMonth + offset`.
 * `dateKey` maps a Date to the store's canonical day key and is only used to
 * build stable {#each} keys. Cells outside the month carry `current: false`.
 */
export function calendarDays(
  calendarMonth: Date,
  offset: number,
  dateKey: (date: Date) => string,
): CalendarDay[] {
  const year = calendarMonth.getUTCFullYear();
  const month = calendarMonth.getUTCMonth() + offset;
  const first = new Date(Date.UTC(year, month, 1));
  const last = new Date(Date.UTC(year, month + 1, 0));
  const leadingBlanks = (first.getUTCDay() + 6) % 7;
  const days: CalendarDay[] = [];

  const prevLast = new Date(Date.UTC(year, month, 0));
  for (let i = leadingBlanks - 1; i >= 0; i--) {
    const day = prevLast.getUTCDate() - i;
    const date = new Date(Date.UTC(year, month - 1, day));
    days.push({ day, date, current: false, key: "p-" + dateKey(date) });
  }
  for (let day = 1; day <= last.getUTCDate(); day++) {
    const date = new Date(Date.UTC(year, month, day));
    days.push({ day, date, current: true, key: "c-" + dateKey(date) });
  }
  const trailing = 42 - days.length;
  for (let day = 1; day <= trailing; day++) {
    const date = new Date(Date.UTC(year, month + 1, day));
    days.push({ day, date, current: false, key: "n-" + dateKey(date) });
  }
  return days;
}

/** `calendarMonth` shifted by `step` months, clamped to `limit` (inclusive). */
export function shiftMonth(
  calendarMonth: Date,
  step: number,
  limit?: Date | null,
): Date {
  const next = new Date(
    Date.UTC(calendarMonth.getUTCFullYear(), calendarMonth.getUTCMonth() + step, 1),
  );
  if (limit && next.getTime() > limit.getTime()) return calendarMonth;
  return next;
}

/** True when `calendarMonth` is the month `today` falls in. */
export function isSameMonth(calendarMonth: Date, today: Date): boolean {
  return (
    calendarMonth.getUTCFullYear() === today.getUTCFullYear() &&
    calendarMonth.getUTCMonth() === today.getUTCMonth()
  );
}
