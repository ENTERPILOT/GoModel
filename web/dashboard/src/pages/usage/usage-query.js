// Usage page query: the filter table, and one builder for both the admin API
// calls and the shareable page URL. The dashboard URL mirrors the API
// parameter names by design (model, user_path, rows, interval, ...), so a
// copied link reads like the API call behind it. Pure functions: node:test
// exercises them without a DOM.

// USAGE_FILTERS lists the page's data filters once: the state key and the
// API/URL parameter. Every filter but the session holds several values.
export const USAGE_FILTERS = [
  { key: "models", param: "model" },
  { key: "providers", param: "provider" },
  { key: "labels", param: "label" },
  { key: "userPaths", param: "user_path" },
  { key: "authKeys", param: "auth_key_id" },
  { key: "endpoints", param: "endpoint" },
  { key: "session", param: "session_id", single: true },
];

export const PIVOT_DIMENSIONS = ["model", "provider", "user_path", "label", "endpoint", "auth_key", "period"];
export const PIVOT_METRICS = ["cost", "tokens", "requests"];
export const PIVOT_INTERVALS = ["daily", "weekly", "monthly", "yearly"];

const DATE_KEY = /^\d{4}-\d{2}-\d{2}$/;

// emptyUsageFilters returns filter values with nothing selected.
export function emptyUsageFilters() {
  return Object.fromEntries(USAGE_FILTERS.map((filter) => [filter.key, filter.single ? "" : []]));
}

// usageFiltersEmpty reports whether no filter has a value.
export function usageFiltersEmpty(values) {
  return USAGE_FILTERS.every((filter) => {
    const value = values && values[filter.key];
    return filter.single ? !value : !(value && value.length);
  });
}

// defaultPivotConfig is the breakdown shown before anyone picks one: cost by
// user path per month answers "who spends what, and when" at a glance.
export function defaultPivotConfig() {
  return { rows: "user_path", columns: "period", metric: "cost", interval: "monthly", depth: 0 };
}

// usageQuery renders the view as a query string (without "?"): filters
// (excludeParam leaves one out, for faceted dropdown options), the window
// ({ days } or { start, end }) and the breakdown settings. omitDefaults
// leaves out breakdown settings equal to the defaults, so a plain page keeps
// a clean URL; the API call spells them out.
export function usageQuery({ filters = {}, excludeParam = "", window = null, pivot = null, omitDefaults = false } = {}) {
  const pairs = [];
  for (const filter of USAGE_FILTERS) {
    if (filter.param === excludeParam) continue;
    const value = filters[filter.key];
    for (const item of filter.single ? [value] : value || []) {
      if (item) pairs.push([filter.param, item]);
    }
  }
  if (window && window.start && window.end) {
    pairs.push(["start_date", window.start], ["end_date", window.end]);
  } else if (window && window.days) {
    pairs.push(["days", String(window.days)]);
  }
  if (pivot) pairs.push(...pivotPairs(pivot, omitDefaults));
  return pairs.map(([name, value]) => name + "=" + encodeURIComponent(value)).join("&");
}

// pivotPairs lists the breakdown parameters. interval only matters with a
// period dimension and user_path_depth only with a user path one, so they
// are left out otherwise.
function pivotPairs(pivot, omitDefaults) {
  const defaults = defaultPivotConfig();
  const keep = (field) => !omitDefaults || pivot[field] !== defaults[field];
  const uses = (dimension) => pivot.rows === dimension || pivot.columns === dimension;
  const pairs = [];
  if (keep("rows")) pairs.push(["rows", pivot.rows]);
  if (keep("columns")) pairs.push(["columns", pivot.columns || "none"]);
  if (keep("metric")) pairs.push(["metric", pivot.metric]);
  if (uses("period") && keep("interval")) pairs.push(["interval", pivot.interval]);
  if (uses("user_path") && pivot.depth) pairs.push(["user_path_depth", String(pivot.depth)]);
  return pairs;
}

// parseUsageQuery reads a query string back into the view. Filters are
// always complete (absent ones empty); unknown or malformed values fall back
// to defaults; an absent window or pivot comes back null.
export function parseUsageQuery(search) {
  const params = new URLSearchParams(search || "");
  const filters = {};
  for (const filter of USAGE_FILTERS) {
    const values = [...new Set(params.getAll(filter.param).map((value) => value.trim()).filter(Boolean))];
    filters[filter.key] = filter.single ? values[0] || "" : values;
  }

  let window = null;
  const start = params.get("start_date") || "";
  const end = params.get("end_date") || "";
  const days = parseInt(params.get("days") || "", 10);
  if (DATE_KEY.test(start) && DATE_KEY.test(end)) {
    window = start <= end ? { start, end } : { start: end, end: start };
  } else if (Number.isFinite(days) && days > 0) {
    window = { days: Math.min(days, 365) };
  }

  let pivot = null;
  if (["rows", "columns", "metric", "interval", "user_path_depth"].some((name) => params.has(name))) {
    const defaults = defaultPivotConfig();
    const rows = oneOf(params.get("rows"), PIVOT_DIMENSIONS, defaults.rows);
    const rawColumns = params.get("columns");
    let columns = rawColumns === "none" ? "" : oneOf(rawColumns, PIVOT_DIMENSIONS, defaults.columns);
    if (columns === rows) columns = "";
    const depth = parseInt(params.get("user_path_depth") || "0", 10);
    pivot = {
      rows,
      columns,
      metric: oneOf(params.get("metric"), PIVOT_METRICS, defaults.metric),
      interval: oneOf(params.get("interval"), PIVOT_INTERVALS, defaults.interval),
      depth: Number.isFinite(depth) && depth > 0 ? Math.min(depth, 16) : 0,
    };
  }

  return { filters, window, pivot };
}

function oneOf(value, allowed, fallback) {
  return allowed.includes(value) ? value : fallback;
}

// Request-log and session paging params, appended after the window and filter query.
export function usageLogQueryParams({ limit, offset, hideCached, search }) {
  let qs = "&limit=" + limit + "&offset=" + offset;
  qs += "&cache_mode=" + (hideCached ? "uncached" : "all");
  if (search) qs += "&search=" + encodeURIComponent(search);
  return qs;
}

export function sessionUsageQueryParams({ limit, offset }) {
  return "&limit=" + limit + "&offset=" + offset;
}
