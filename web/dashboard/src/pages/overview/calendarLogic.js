// Contribution calendar math. Pure functions taking today's date key so the
// grid is deterministic and testable with node. Date-key math is UTC-based,
// matching timezone.js.

// Number of active intensity levels (1..CALENDAR_LEVELS); level 0 means no
// activity. Keep in sync with the --cal-level-* ramp and .level-N rules in
// dashboard.css.
const CALENDAR_LEVELS = 10;
// Exponent (<1) for the intensity scale; see calendarLevel() for rationale.
const CALENDAR_SCALE_EXPONENT = 0.7;

function pad(n) {
  return String(n).padStart(2, "0");
}

export function dateKeyToDate(key) {
  if (!key) return null;
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(key);
  if (!match) return null;
  return new Date(
    Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3])),
  );
}

export function dateToDateKey(date) {
  if (!date || typeof date.getTime !== "function" || Number.isNaN(date.getTime())) {
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

export function addDaysToDateKey(key, days) {
  const date = dateKeyToDate(key);
  if (!date) return "";
  date.setUTCDate(date.getUTCDate() + days);
  return dateToDateKey(date);
}

export function calendarLevel(value, max) {
  if (value <= 0 || max <= 0) return 0;
  // Power scale (exponent < 1) against the busiest day. Token counts span
  // orders of magnitude. A linear ratio collapses every quiet day to the
  // lightest shade, while a pure log scale flattens the busy end so a 1M and
  // a 1.5M day land on the same shade. A ~0.7 exponent sits between the two:
  // it keeps high-volume days separated (so big days stay distinguishable)
  // while still lifting quiet days off the lightest level. Spreading the
  // result across CALENDAR_LEVELS buckets gives a gradual ramp instead of a
  // handful of coarse steps.
  const ratio = Math.pow(value / max, CALENDAR_SCALE_EXPONENT);
  const level = Math.ceil(ratio * CALENDAR_LEVELS);
  if (level < 1) return 1;
  if (level > CALENDAR_LEVELS) return CALENDAR_LEVELS;
  return level;
}

export function calendarLegendLevels() {
  // 0 (empty) .. CALENDAR_LEVELS, matching calendarLevel()'s range and the
  // --cal-level-* ramp so the legend always mirrors the grid.
  const levels = [];
  for (let i = 0; i <= CALENDAR_LEVELS; i++) {
    levels.push(i);
  }
  return levels;
}

export function buildCalendarGrid(calendarData, mode, todayKey) {
  const byDate = {};
  (calendarData || []).forEach((d) => {
    byDate[d.date] = d;
  });

  const start = dateKeyToDate(addDaysToDateKey(todayKey, -364));

  // Align start to Sunday so each week column reads top-to-bottom Sun, Mon,
  // ..., Sat — matching the Mon/Wed/Fri day labels (GitHub style).
  const dayOfWeek = start.getUTCDay(); // 0 = Sunday
  start.setUTCDate(start.getUTCDate() - dayOfWeek);

  const days = [];
  for (
    let d = new Date(start);
    dateToDateKey(d) <= todayKey;
    d.setUTCDate(d.getUTCDate() + 1)
  ) {
    const key = dateToDateKey(d);
    const entry = byDate[key];
    let value = 0;
    if (entry) {
      if (mode === "costs") {
        value = entry.total_cost != null ? entry.total_cost : 0;
      } else {
        value = entry.total_tokens || 0;
      }
    }
    days.push({ dateStr: key, value: value, level: 0, empty: false });
  }

  // Calculate levels relative to the busiest day so intensity reflects
  // magnitude (a 10K day reads much lighter than a 1M day).
  let max = 0;
  for (let i = 0; i < days.length; i++) {
    if (days[i].value > max) max = days[i].value;
  }

  for (let i = 0; i < days.length; i++) {
    days[i].level = calendarLevel(days[i].value, max);
  }

  // Build weeks (columns)
  const weeks = [];
  let week = [];
  for (let j = 0; j < days.length; j++) {
    week.push(days[j]);
    if (week.length === 7) {
      weeks.push(week);
      week = [];
    }
  }
  if (week.length > 0) {
    // Pad remaining week with empty slots
    while (week.length < 7) {
      week.push({ dateStr: "", value: 0, level: 0, empty: true });
    }
    weeks.push(week);
  }

  return weeks;
}

export function calendarMonthLabels(todayKey) {
  const start = dateKeyToDate(addDaysToDateKey(todayKey, -364));
  // Sunday-align to match buildCalendarGrid's week columns.
  const dayOfWeek = start.getUTCDay(); // 0 = Sunday
  start.setUTCDate(start.getUTCDate() - dayOfWeek);

  const months = [
    "Jan", "Feb", "Mar", "Apr", "May", "Jun",
    "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
  ];
  const labels = [];
  const seenMonths = {};
  let totalWeeks = 0;

  for (
    let weekStart = new Date(start);
    dateToDateKey(weekStart) <= todayKey;
    weekStart.setUTCDate(weekStart.getUTCDate() + 7), totalWeeks++
  ) {
    let labelDay = null;

    if (totalWeeks === 0) {
      labelDay = new Date(weekStart);
    } else {
      for (let offset = 0; offset < 7; offset++) {
        const d = new Date(weekStart);
        d.setUTCDate(weekStart.getUTCDate() + offset);
        if (dateToDateKey(d) > todayKey) {
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

    const monthKey = labelDay.getUTCFullYear() + "-" + labelDay.getUTCMonth();
    if (seenMonths[monthKey]) {
      continue;
    }

    labels.push({
      label: months[labelDay.getUTCMonth()],
      col: totalWeeks,
      key: monthKey,
    });
    seenMonths[monthKey] = true;
  }

  for (let i = 0; i < labels.length; i++) {
    const nextCol = i + 1 < labels.length ? labels[i + 1].col : totalWeeks;
    labels[i].span = Math.max(1, nextCol - labels[i].col);
  }

  return labels;
}

export function calendarSummaryText(calendarData, mode) {
  let total = 0;
  const data = calendarData || [];
  for (let i = 0; i < data.length; i++) {
    const entry = data[i];
    if (!entry) {
      continue;
    }
    if (mode === "costs") {
      if (entry.total_cost) {
        total += entry.total_cost;
      }
      continue;
    }
    if (entry.total_tokens) {
      total += entry.total_tokens;
    }
  }
  if (mode === "costs") {
    return "$" + total.toFixed(2) + " in the last year";
  }
  return total.toLocaleString() + " tokens in the last year";
}

export function calendarTooltipText(day, mode) {
  if (!day || day.empty) return "";
  if (mode === "costs") {
    return "$" + (day.value || 0).toFixed(4) + " on " + day.dateStr;
  }
  return (day.value || 0).toLocaleString() + " tokens on " + day.dateStr;
}
