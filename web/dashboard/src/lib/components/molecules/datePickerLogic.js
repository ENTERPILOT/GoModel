// Pure calendar-grid math for DatePicker. Everything here works in UTC and
// takes explicit inputs so it can be unit-tested without a DOM or a store.

/**
 * The 42 cells (6 weeks, Monday first) of `calendarMonth + offset`.
 * `dateKey` maps a Date to the store's canonical day key and is only used to
 * build stable {#each} keys. Cells outside the month carry `current: false`.
 */
export function calendarDays(calendarMonth, offset, dateKey) {
  const year = calendarMonth.getUTCFullYear();
  const month = calendarMonth.getUTCMonth() + offset;
  const first = new Date(Date.UTC(year, month, 1));
  const last = new Date(Date.UTC(year, month + 1, 0));
  const leadingBlanks = (first.getUTCDay() + 6) % 7;
  const days = [];

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
export function shiftMonth(calendarMonth, step, limit) {
  const next = new Date(
    Date.UTC(calendarMonth.getUTCFullYear(), calendarMonth.getUTCMonth() + step, 1),
  );
  if (limit && next.getTime() > limit.getTime()) return calendarMonth;
  return next;
}

/** True when `calendarMonth` is the month `today` falls in. */
export function isSameMonth(calendarMonth, today) {
  return (
    calendarMonth.getUTCFullYear() === today.getUTCFullYear() &&
    calendarMonth.getUTCMonth() === today.getUTCMonth()
  );
}
