// Usage page view <-> URL query string. The parameter names mirror the admin
// usage API (model, user_path, rows, interval, ...), so a copied dashboard
// link reproduces the view and reads like the API call behind it. Pure
// functions: node:test exercises them without a DOM.

const LIST_PARAMS = {
  models: "model",
  providers: "provider",
  labels: "label",
  userPaths: "user_path",
  authKeys: "auth_key_id",
  endpoints: "endpoint",
};

export const PIVOT_DIMENSIONS = ["model", "provider", "user_path", "label", "endpoint", "auth_key", "period"];
export const PIVOT_METRICS = ["cost", "tokens", "requests"];
export const PIVOT_INTERVALS = ["daily", "weekly", "monthly", "yearly"];

const DATE_KEY = /^\d{4}-\d{2}-\d{2}$/;

// defaultPivotConfig is the breakdown shown before anyone picks one: cost by
// user path per month answers "who spends what, and when" at a glance.
export function defaultPivotConfig() {
  return { rows: "user_path", columns: "period", metric: "cost", interval: "monthly", depth: 0 };
}

// usageViewToQuery renders the view as a query string (without "?"). Values
// equal to the defaults are left out so a plain page keeps a clean URL.
export function usageViewToQuery({ filters = {}, session = "", window = {}, pivot = null } = {}) {
  const params = new URLSearchParams();
  for (const [key, name] of Object.entries(LIST_PARAMS)) {
    for (const value of filters[key] || []) {
      if (value) params.append(name, value);
    }
  }
  if (session) params.set("session_id", session);
  if (window.start && window.end) {
    params.set("start_date", window.start);
    params.set("end_date", window.end);
  } else if (window.days) {
    params.set("days", String(window.days));
  }
  if (pivot) {
    const defaults = defaultPivotConfig();
    if (pivot.rows !== defaults.rows) params.set("rows", pivot.rows);
    if (pivot.columns !== defaults.columns) params.set("columns", pivot.columns || "none");
    if (pivot.metric !== defaults.metric) params.set("metric", pivot.metric);
    if (pivot.interval !== defaults.interval) params.set("interval", pivot.interval);
    if (pivot.depth) params.set("user_path_depth", String(pivot.depth));
  }
  return params.toString();
}

// usageViewFromQuery parses a query string back into the view. Filters are
// always complete (absent ones empty); unknown or malformed values fall back
// to defaults; an absent window or pivot comes back null so the caller keeps
// its current one.
export function usageViewFromQuery(search) {
  const params = new URLSearchParams(search || "");
  const filters = {};
  for (const [key, name] of Object.entries(LIST_PARAMS)) {
    filters[key] = unique(params.getAll(name).map((value) => value.trim()).filter(Boolean));
  }
  const session = (params.get("session_id") || "").trim();

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

  return { filters, session, window, pivot };
}

function oneOf(value, allowed, fallback) {
  return allowed.includes(value) ? value : fallback;
}

function unique(values) {
  return [...new Set(values)];
}
