// Per-category column spec for the models table — the single source of
// truth for the table header (ModelTable) and the row cells (ModelRow), so
// the two can never drift. Each column: headerLines (multi-line headers
// render with <br>), optional cell class, and value(row, pricing).
// Relative import (not $lib) so node:test can resolve this module too.
import { formatPrice, formatPriceFine } from "../../lib/utils/format.js";
import * as m from "../../lib/paraglide/messages.js";

function price(headerLines, value, hint) {
  return { headerLines, class: "col-price", value, hint };
}

const DAY_ORDER = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"];
const DAY_NAMES = ["monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"];

// parseDay mirrors the gateway's weekday parser: "mon" or "monday" (any
// case), never other prefixes such as "monda". Returns null when unknown.
function parseDay(value) {
  const name = String(value || "").trim().toLowerCase();
  const index = DAY_ORDER.indexOf(name.slice(0, 3));
  if (index < 0 || (name.length > 3 && name !== DAY_NAMES[index])) {
    return null;
  }
  return DAY_ORDER[index];
}

// formatDays renders "mon,tue,wed,thu,fri" as "mon–fri", "" when the range
// has no day restriction, and null when it names days but none are known —
// the gateway never applies such a range, so it must not be shown as daily.
function formatDays(days) {
  const listed = Array.isArray(days) ? days : [];
  const known = [...new Set(listed.map(parseDay).filter(Boolean))].sort(
    (a, b) => DAY_ORDER.indexOf(a) - DAY_ORDER.indexOf(b),
  );
  if (listed.length > 0 && known.length === 0) {
    return null;
  }
  const runs = [];
  for (const day of known) {
    const last = runs[runs.length - 1];
    if (last && DAY_ORDER.indexOf(day) === DAY_ORDER.indexOf(last[last.length - 1]) + 1) {
      last.push(day);
    } else {
      runs.push([day]);
    }
  }
  return runs.map((run) => (run.length > 2 ? run[0] + "–" + run[run.length - 1] : run.join(","))).join(", ");
}

// parseClock mirrors the gateway's "HH:MM" grammar ("24:00" allowed as an
// end-of-day bound). Returns the trimmed value, or null when the gateway
// would reject it.
function parseClock(value) {
  const clock = String(value ?? "").trim();
  const match = /^(\d{2}):(\d{2})$/.exec(clock);
  if (!match) {
    return null;
  }
  const hour = Number(match[1]);
  const minute = Number(match[2]);
  if (minute > 59 || hour > 24 || (hour === 24 && minute !== 0)) {
    return null;
  }
  return clock;
}

// formatRange renders one UTC range, or null when the gateway can never
// apply it (unknown days or an unparseable bound).
function formatRange(range) {
  const days = formatDays(range?.days);
  const start = parseClock(range?.start);
  const end = parseClock(range?.end);
  if (days === null || start === null || end === null) {
    return null;
  }
  const clock = start + "–" + end;
  return days ? days + " " + clock : clock;
}

// timeWindowHint summarizes the pricing time windows (e.g. DeepSeek off-peak
// hours) for the given rate fields, or returns "" when none apply to them.
export function timeWindowHint(pricing, fields, format = formatPrice) {
  const windows = Array.isArray(pricing?.time_windows) ? pricing.time_windows : [];
  const lines = [];
  for (const window of windows) {
    const rates = window?.pricing || {};
    if (!fields.some((field) => rates[field] != null)) {
      continue;
    }
    const label = String(window?.label || "").replace(/_/g, " ");
    // A field the window does not override keeps its base price.
    const prices = fields.map((field) => format(rates[field] ?? pricing?.[field])).join(" / ");
    const ranges = (Array.isArray(window?.utc_ranges) ? window.utc_ranges : []).map(formatRange).filter(Boolean);
    if (ranges.length === 0) {
      continue; // a window with no applicable range never prices anything
    }
    const schedule = ranges.join(", ");
    lines.push(m.models_price_time_window_hint({ label, prices, schedule }));
  }
  return lines.join("\n");
}

const inputOutput = price(
  [m.models_column_input_output()],
  (row, p) => formatPrice(p?.input_per_mtok) + " / " + formatPrice(p?.output_per_mtok),
  (row, p) => timeWindowHint(p, ["input_per_mtok", "output_per_mtok"]),
);

const CATEGORY_COLUMNS = {
  all: [inputOutput],
  text_generation: [
    inputOutput,
    price(
      [m.models_column_cached()],
      (row, p) => formatPrice(p?.cached_input_per_mtok),
      (row, p) => timeWindowHint(p, ["cached_input_per_mtok"]),
    ),
  ],
  embedding: [
    price(
      [m.models_column_input(), "$/MTok"],
      (row, p) => formatPrice(p?.input_per_mtok),
      (row, p) => timeWindowHint(p, ["input_per_mtok"]),
    ),
  ],
  image: [price([m.models_column_per_image()], (row, p) => formatPriceFine(p?.per_image))],
  audio: [
    price([m.models_column_per_second()], (row, p) => formatPriceFine(p?.per_second_input)),
    price([m.models_column_per_character()], (row, p) => formatPriceFine(p?.per_character_input)),
  ],
  video: [
    price([m.models_column_per_second_in()], (row, p) => formatPriceFine(p?.per_second_input)),
    price([m.models_column_per_second_out()], (row, p) => formatPriceFine(p?.per_second_output)),
  ],
  utility: [
    price([m.models_column_per_page()], (row, p) => formatPriceFine(p?.per_page)),
    price([m.models_column_per_request()], (row, p) => formatPriceFine(p?.per_request)),
  ],
};

export function categoryColumns(category) {
  return CATEGORY_COLUMNS[category] || CATEGORY_COLUMNS.all;
}

// Full table width: the Model column + category columns + the actions column.
export function categoryColspan(category) {
  return categoryColumns(category).length + 2;
}
