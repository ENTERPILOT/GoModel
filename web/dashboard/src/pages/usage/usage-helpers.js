// Pure logic for the Usage page.
// Kept free of Svelte/DOM dependencies so node:test can exercise it.

import { formatCost, formatNumber, formatTokensShort } from "../../lib/utils/format.js";
import * as m from "../../lib/paraglide/messages.js";
import { AUDIT_TYPES } from "../audit-logs/audit-operations.js";

// emptyUsagePageSummary: the filtered usage-page summaries carry the cache
// split and rewrite-savings fields on top of the shared totals.
export function emptyUsagePageSummary() {
  return {
    total_requests: 0,
    total_input_tokens: 0,
    total_output_tokens: 0,
    total_tokens: 0,
    uncached_input_tokens: 0,
    cached_input_tokens: 0,
    cache_write_input_tokens: 0,
    total_input_cost: null,
    total_output_cost: null,
    total_cost: null,
    rewrite_tokens_saved: 0,
    rewrite_cost_saved: null,
  };
}

export function emptyUsageLog() {
  return { entries: [], total: 0, limit: 50, offset: 0 };
}

export function emptySessionUsage() {
  return { entries: [], total: 0, limit: 50, offset: 0 };
}

export function shortSessionID(value) {
  const sessionID = String(value || "").trim();
  if (sessionID.length <= 26) return sessionID || "-";
  return sessionID.slice(0, 15) + "…" + sessionID.slice(-8);
}

// --- Query building ---

// Page-level data filters, applied to every usage-page request so charts,
// cache cards, and the request log describe the same filtered slice of
// traffic. excludeFacet omits that one filter — used to build facet dropdown
// options that honor every filter except their own.
export function usageFilterQueryStr(filters, excludeFacet) {
  const pairs = [
    ["model", filters && filters.model],
    ["provider", filters && filters.provider],
    ["label", filters && filters.label],
    ["user_path", filters && filters.user_path],
    ["session_id", filters && filters.session_id],
    ["auth_key_id", filters && filters.auth_key_id],
    ["endpoint", filters && filters.endpoint],
  ];
  let qs = "";
  for (const [facet, value] of pairs) {
    if (!value || facet === excludeFacet) continue;
    qs += "&" + facet + "=" + encodeURIComponent(value);
  }
  return qs;
}

// Request-log paging/search params, appended after the window + filter query.
export function usageLogQueryParams({ limit, offset, hideCached, search }) {
  let qs = "&limit=" + limit + "&offset=" + offset;
  qs += "&cache_mode=" + (hideCached ? "uncached" : "all");
  if (search) qs += "&search=" + encodeURIComponent(search);
  return qs;
}

export function sessionUsageQueryParams({ limit, offset }) {
  return "&limit=" + limit + "&offset=" + offset;
}

// Sorted, deduplicated choices for one facet dropdown. The active selection
// stays listed so the select never silently shows "All" while a filter is
// applied.
export function facetOptionList(values, activeValue) {
  const set = new Set(values || []);
  if (activeValue) set.add(activeValue);
  return [...set].sort();
}

// Maps managed auth key ids to their names from an /admin/auth-keys response.
export function authKeyNameMap(keys) {
  const names = {};
  for (const key of keys || []) {
    const id = String((key && key.id) || "").trim();
    if (id) names[id] = String(key.name || "").trim() || id;
  }
  return names;
}

// Usage rows store the key id; show the key's name when it is known (a key
// outside the caller's scope or since deleted falls back to a short id).
export function authKeyDisplayName(id, names) {
  const keyID = String(id || "").trim();
  if (!keyID) return "-";
  return (names && names[keyID]) || shortSessionID(keyID);
}

// API key filter choices sorted by name. The active key stays listed even
// when it is no longer known, so the select never silently shows "All".
export function authKeyOptionList(names, activeID) {
  const options = Object.entries(names || {}).map(([id, name]) => ({ id, name }));
  if (activeID && !(names && names[activeID])) {
    options.push({ id: activeID, name: authKeyDisplayName(activeID, names) });
  }
  return options.sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id));
}

// The request log shows its API key column only when there is something to
// show: an entry made with a managed key, or an active key filter.
export function usageLogHasAuthKeys(filterAuthKey, logEntries) {
  if (filterAuthKey) return true;
  return (logEntries || []).some((entry) => entry && entry.auth_key_id);
}

// --- Stat cards (filtered summaries) ---

// Locally-cached requests over the period and filters, derived as the
// difference between the two summaries so the number stays correct even when
// cache analytics is disabled.
function usagePageCacheHits(uncachedSummary, allSummary) {
  const all = Number((allSummary && allSummary.total_requests) || 0);
  const uncached = Number((uncachedSummary && uncachedSummary.total_requests) || 0);
  const hits = all - uncached;
  return Number.isFinite(hits) && hits > 0 ? hits : 0;
}

// Requests over the period and filters, scoped exactly like the request log:
// cached rows count unless "Hide cached requests" is on.
export function usagePageTotalRequests(uncachedSummary, allSummary, hideCached) {
  const summary = hideCached ? uncachedSummary : allSummary;
  const requests = Number((summary && summary.total_requests) || 0);
  return Number.isFinite(requests) ? requests : 0;
}

export function usagePageRequestsTitle(uncachedSummary, allSummary, hideCached) {
  const hits = usagePageCacheHits(uncachedSummary, allSummary);
  if (hits <= 0) return "";
  if (hideCached) {
    return m.usage_cached_hidden({ count: formatNumber(hits) });
  }
  const provider = Number((uncachedSummary && uncachedSummary.total_requests) || 0);
  return m.usage_requests_sources({
    provider: formatNumber(provider),
    cache: formatNumber(hits),
  });
}

export function usagePageCostTitle(summary) {
  const s = summary || {};
  if (s.total_input_cost === null || s.total_input_cost === undefined) return "";
  return m.usage_cost_split({
    input: formatCost(s.total_input_cost),
    output: formatCost(s.total_output_cost),
  });
}

// --- Rewrite savings ("Pro Saved" card) ---
// Request rewriters (e.g. GoModel Pro token compression) strip prompt tokens
// before the request leaves the gateway. Savings ride on provider usage rows,
// so the uncached summary holds the full totals, and the card only appears
// once a rewriter reported savings. A single card follows the page's
// Tokens/Costs mode instead of showing the same saving twice.
export function rewriteTokensSaved(summary) {
  const saved = Number((summary && summary.rewrite_tokens_saved) || 0);
  return Number.isFinite(saved) && saved > 0 ? saved : 0;
}

export function rewriteSavingsVisible(summary) {
  return rewriteTokensSaved(summary) > 0;
}

export function rewriteCostSaved(summary) {
  const s = summary || {};
  return s.rewrite_cost_saved === undefined ? null : s.rewrite_cost_saved;
}

// Keep the estimator disclosure in one helper so every future surface uses
// the same wording as the Pro compressor's net-characters / 4 calculation.
export function rewriteTokenEstimateMethodText() {
  return m.usage_rewrite_estimate();
}

// Card value: priced savings in costs mode, removed prompt tokens otherwise.
// Tokens use the short form the overview cards and chart axes use — the exact
// count stays in the tooltip.
export function proSavedValueText(summary, mode) {
  if (mode === "costs") return formatCost(rewriteCostSaved(summary));
  return formatTokensShort(rewriteTokensSaved(summary));
}

// Share of the pre-rewrite baseline that the rewriters removed. Tokens mode
// compares prompt savings with recorded input only; output is unaffected by
// prompt compression and must not dilute the percentage. Costs mode keeps
// comparing saved input cost with the period's total recorded spend.
// Null when the baseline is unknown (costs mode with no pricing), so the card
// shows the value alone instead of a bogus 0%.
export function proSavedPercent(summary, mode) {
  const costs = mode === "costs";
  const saved = costs ? Number(rewriteCostSaved(summary)) : rewriteTokensSaved(summary);
  if (!Number.isFinite(saved) || saved <= 0) return null;
  // Read the baseline before coercing: a null total_cost means "not priced",
  // and coercing it to 0 would put the whole baseline in the savings and
  // claim "100% less" for a period whose spend is simply unknown.
  const raw = summary && (costs ? summary.total_cost : summary.total_input_tokens);
  if (raw === null || raw === undefined) return null;
  const recorded = Number(raw);
  if (!Number.isFinite(recorded) || recorded < 0) return null;
  // saved > 0 and recorded >= 0, so the baseline is positive by construction.
  return (saved / (recorded + saved)) * 100;
}

export function proSavedPercentText(summary, mode) {
  const pct = proSavedPercent(summary, mode);
  if (pct === null) return "";
  return m.usage_less({ percent: pct < 0.1 ? "<0.1" : pct.toFixed(1) });
}

export function proSavedTitle(summary, mode) {
  const tokens = rewriteTokensSaved(summary);
  if (tokens <= 0) return "";
  const lines = [
    m.usage_rewrite_tokens_removed({ count: formatNumber(tokens) }),
    rewriteTokenEstimateMethodText(),
    m.usage_rewrite_summed(),
  ];
  const cost = rewriteCostSaved(summary);
  if (cost !== null && cost !== undefined) {
    lines.push(m.usage_rewrite_cost_avoided({ cost: formatCost(cost) }));
    lines.push(m.usage_rewrite_cache_excluded());
  }
  const pct = proSavedPercentText(summary, mode);
  if (pct) {
    lines.push(
      m.usage_rewrite_comparison({
        percent: pct,
        mode: mode === "costs" ? m.usage_mode_cost() : m.usage_mode_tokens(),
      }),
    );
  }
  return lines.join("\n");
}

// --- Request-log entry helpers ---

function costSource(entry) {
  return String((entry && entry.cost_source) || "").trim();
}

export function usesResponseCostPricing(entry) {
  const source = costSource(entry);
  return source === "openrouter_credits" || source === "xai_cost_in_usd_ticks";
}

export function costSourceTooltip(entry) {
  switch (costSource(entry)) {
    case "openrouter_credits":
      return m.usage_openrouter_cost_source();
    case "xai_cost_in_usd_ticks":
      return m.usage_xai_cost_source();
    default:
      return "";
  }
}

function usageEntryCacheType(entry) {
  return String((entry && entry.cache_type) || "")
    .trim()
    .toLowerCase();
}

export function usageEntryCached(entry) {
  const type = usageEntryCacheType(entry);
  return type === "exact" || type === "semantic";
}

export function usageEntryCacheLabel(entry) {
  const type = usageEntryCacheType(entry);
  if (type === "exact") return m.usage_cache_exact();
  if (type === "semantic") return m.usage_cache_semantic();
  return "-";
}

export function cachedCostTitle(entry, baseTitle) {
  const base = baseTitle ? String(baseTitle) : "";
  if (!usageEntryCached(entry)) return base;
  const prefix = m.usage_cache_not_charged();
  return base ? prefix + "\n" + base : prefix;
}

function providerCacheRatio(entry) {
  const ratio = Number(entry && entry.cached_input_ratio);
  if (!Number.isFinite(ratio) || ratio <= 0) return 0;
  return Math.min(1, ratio);
}

export function hasProviderCache(entry) {
  return Number((entry && entry.cached_input_tokens) || 0) > 0;
}

export function providerCacheLabel(entry) {
  if (!hasProviderCache(entry)) return "";
  const pct = providerCacheRatio(entry) * 100;
  return pct.toFixed(1) + "%";
}

export function providerCacheTitle(entry) {
  if (!hasProviderCache(entry)) return "";
  const cached = Number(entry.cached_input_tokens || 0);
  const uncached = Number(entry.uncached_input_tokens || 0);
  const write = Number(entry.cache_write_input_tokens || 0);
  const total = cached + uncached + write;
  const parts = [
    m.usage_provider_cache_title({
      cached: formatNumber(cached),
      total: formatNumber(total),
    }),
  ];
  if (write > 0) {
    parts.push(m.usage_cache_write({ count: formatNumber(write) }));
  }
  return parts.join("\n");
}

export function formatCostTooltip(entry) {
  const lines = [];
  if (costSourceTooltip(entry)) {
    lines.push(costSourceTooltip(entry));
    lines.push("");
  }
  lines.push(m.usage_cost_input_line({ cost: formatCost(entry.input_cost) }));
  lines.push(m.usage_cost_output_line({ cost: formatCost(entry.output_cost) }));
  if (entry.raw_data) {
    lines.push("");
    for (const [key, value] of Object.entries(entry.raw_data)) {
      const label = key.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
      const formatted =
        value && typeof value === "object" ? JSON.stringify(value) : formatNumber(value);
      lines.push(label + ": " + formatted);
    }
  }
  return lines.join("\n");
}

export function entryLabels(entry) {
  return Array.isArray(entry && entry.labels) ? entry.labels : [];
}

// The Labels column only appears when labels are in play: the period has
// by-label aggregates, a label filter is active, or the current log page
// carries labelled entries (e.g. cached-only labelled traffic, which the
// uncached-mode aggregates omit).
export function usageLogHasLabels(labelUsage, filterLabel, logEntries) {
  if ((labelUsage || []).length > 0 || filterLabel) return true;
  return (logEntries || []).some((entry) => entryLabels(entry).length > 0);
}

// --- Breakdown rows / chart series ---

export function usageRowTotalTokens(row) {
  if (row && typeof row.total_tokens === "number") return row.total_tokens;
  return ((row && row.input_tokens) || 0) + ((row && row.output_tokens) || 0);
}

function usageAggregateValue(row, costs) {
  if (costs) return row.total_cost || 0;
  return usageRowTotalTokens(row);
}

export function usageRowsBySelectedValue(items, costs) {
  return [...(items || [])].sort((a, b) => {
    if (costs) {
      return (b.total_cost || 0) - (a.total_cost || 0);
    }
    return usageAggregateValue(b, costs) - usageAggregateValue(a, costs);
  });
}

export function userPathUsageChartVisible(rows) {
  const list = Array.isArray(rows) ? rows : [];
  if (list.length === 0) {
    return false;
  }
  if (list.length !== 1) {
    return true;
  }
  const onlyPath = String((list[0] && list[0].user_path) || "").trim();
  return onlyPath !== "" && onlyPath !== "/";
}

// A view value that renders on the canvas (as opposed to the table).
export function isChartView(view) {
  return (view || "chart") === "chart" || view === "stacked";
}

// Per-row series split for the diverging charts. Rows keep the
// selected-metric ordering; past the top 10 they fold into a single "Other"
// row.
//
// Tokens mode mirrors the overview chart's accounting: the Input series is
// paid input (uncached + cache writes; full input when the split is absent),
// prompt-cache reads and locally-cached tokens are their own series. Costs
// mode splits the estimated prompt-cached read cost out of the input cost;
// local cache hits cost nothing, so they have no cost series.
export function divergingDataFrom(items, labelFor, costs) {
  const sorted = usageRowsBySelectedValue(items, costs);
  const num = (v) => Number(v) || 0;
  // The cached cost is an estimate at current prices while input_cost is the
  // recorded charge, so cap the cached segment at the recorded input cost —
  // the two segments then always sum to it and the bar never exceeds
  // recorded totals.
  const promptOf = (row) => {
    if (!costs) return num(row.cached_input_tokens);
    return Math.min(num(row.cached_input_cost), num(row.input_cost));
  };
  const inputOf = (row) => {
    if (costs) return num(row.input_cost) - promptOf(row);
    const split =
      num(row.uncached_input_tokens) + num(row.cached_input_tokens) + num(row.cache_write_input_tokens);
    return split > 0
      ? num(row.uncached_input_tokens) + num(row.cache_write_input_tokens)
      : num(row.input_tokens);
  };
  const outputOf = (row) => num(costs ? row.output_cost : row.output_tokens);
  const localInputOf = (row) => (costs ? 0 : num(row.local_cached_input_tokens));
  const localOutputOf = (row) => (costs ? 0 : num(row.local_cached_output_tokens));

  const top = sorted.slice(0, 10);
  const rest = sorted.slice(10);

  const labels = top.map(labelFor);
  const inputs = top.map(inputOf);
  const outputs = top.map(outputOf);
  const prompts = top.map(promptOf);
  const localIns = top.map(localInputOf);
  const localOuts = top.map(localOutputOf);

  if (rest.length > 0) {
    labels.push(m.overview_other());
    const sum = (of) => rest.reduce((total, row) => total + of(row), 0);
    inputs.push(sum(inputOf));
    outputs.push(sum(outputOf));
    prompts.push(sum(promptOf));
    localIns.push(sum(localInputOf));
    localOuts.push(sum(localOutputOf));
  }

  return { labels, inputs, outputs, prompts, localIns, localOuts };
}

// Grow the chart wrapper with the row count so horizontal bars stay readable
// instead of squeezing into a fixed height.
export function chartWrapHeight(labelCount) {
  return Math.max(200, labelCount * 32 + 72);
}

// --- Request types and endpoints ---
// The usage page groups endpoints by the same request types as the audit
// log: each type covers one or more gateway operations, so
// /v1/chat/completions and /v1/messages both count as chat. Endpoints
// outside every type fall under "other". The page filters by endpoint, so a
// selection can mix endpoints of different types.

export const USAGE_TYPE_OTHER = "other";

const TYPE_BY_OPERATION = new Map(
  AUDIT_TYPES.flatMap((type) => type.operations.map((op) => [op, type])),
);
const TYPE_BY_KEY = new Map(AUDIT_TYPES.map((type) => [type.key, type]));
const TYPE_ORDER = new Map(AUDIT_TYPES.map((type, index) => [type.key, index]));

// usageTypeForOperation returns the type key an endpoint's operation belongs to.
export function usageTypeForOperation(operation) {
  const type = TYPE_BY_OPERATION.get(String(operation || ""));
  return type ? type.key : USAGE_TYPE_OTHER;
}

export function usageTypeLabel(key) {
  const type = TYPE_BY_KEY.get(key);
  return type ? type.label() : m.usage_type_other();
}

function typeOrder(key) {
  return TYPE_ORDER.has(key) ? TYPE_ORDER.get(key) : AUDIT_TYPES.length;
}

// usageEndpointOptions builds the endpoint filter choices from
// /admin/usage/endpoints rows: grouped in the audit log's type order, each
// described by its type.
export function usageEndpointOptions(rows) {
  const byEndpoint = new Map();
  for (const row of rows || []) {
    if (!row || !row.endpoint || byEndpoint.has(row.endpoint)) continue;
    byEndpoint.set(row.endpoint, usageTypeForOperation(row.operation));
  }
  return [...byEndpoint.entries()]
    .sort(([a, typeA], [b, typeB]) => typeOrder(typeA) - typeOrder(typeB) || a.localeCompare(b))
    .map(([endpoint, type]) => ({ value: endpoint, label: endpoint, description: usageTypeLabel(type) }));
}

// endpointOptionsWithSelection keeps every selected endpoint listed, even one
// without traffic under the other filters, so the dropdown can still uncheck
// it — the endpoint counterpart of facetOptionList.
export function endpointOptionsWithSelection(options, selected) {
  const list = Array.isArray(options) ? options : [];
  const listed = new Set(list.map((option) => option.value));
  const missing = (selected || [])
    .filter((endpoint) => endpoint && !listed.has(endpoint))
    .map((endpoint) => ({ value: endpoint, label: endpoint }));
  return list.concat(missing);
}

// toggleEndpointSelection adds the endpoints to the selection, or removes
// them when every one is already selected — one chip click toggles a whole
// request type.
export function toggleEndpointSelection(selected, endpoints) {
  const current = Array.isArray(selected) ? selected : [];
  const targets = (endpoints || []).filter(Boolean);
  if (targets.length === 0) return current.slice();
  if (targets.every((endpoint) => current.includes(endpoint))) {
    return current.filter((endpoint) => !targets.includes(endpoint));
  }
  return current.concat(targets.filter((endpoint) => !current.includes(endpoint)));
}

export function endpointsSelected(selected, endpoints) {
  const targets = (endpoints || []).filter(Boolean);
  return targets.length > 0 && targets.every((endpoint) => (selected || []).includes(endpoint));
}

// endpointUsageRows tags /admin/usage/endpoints rows for the by-endpoint
// view: each keeps its own totals and gains its request type.
export function endpointUsageRows(rows) {
  return (rows || []).filter(Boolean).map((row) => {
    const type = usageTypeForOperation(row.operation);
    return { ...row, type, type_label: usageTypeLabel(type), label: row.endpoint || "-" };
  });
}

// Nullable sums follow SQL SUM: null only when every value is null.
function addNullable(total, value) {
  if (value === null || value === undefined) return total;
  return (total ?? 0) + Number(value);
}

const TYPE_SUM_FIELDS = [
  "requests",
  "input_tokens",
  "output_tokens",
  "total_tokens",
  "uncached_input_tokens",
  "cached_input_tokens",
  "cache_write_input_tokens",
  "local_cached_input_tokens",
  "local_cached_output_tokens",
];
const TYPE_NULLABLE_FIELDS = ["input_cost", "output_cost", "total_cost", "cached_input_cost"];

// foldEndpointUsageByType sums /admin/usage/endpoints rows into one row per
// request type, keeping the folded endpoints sorted.
export function foldEndpointUsageByType(rows) {
  const byType = new Map();
  for (const row of rows || []) {
    if (!row) continue;
    const key = usageTypeForOperation(row.operation);
    let folded = byType.get(key);
    if (!folded) {
      folded = { type: key, label: usageTypeLabel(key), endpoints: [] };
      for (const field of TYPE_SUM_FIELDS) folded[field] = 0;
      for (const field of TYPE_NULLABLE_FIELDS) folded[field] = null;
      byType.set(key, folded);
    }
    if (row.endpoint) folded.endpoints.push(row.endpoint);
    for (const field of TYPE_SUM_FIELDS) folded[field] += Number(row[field]) || 0;
    for (const field of TYPE_NULLABLE_FIELDS) folded[field] = addNullable(folded[field], row[field]);
  }
  for (const folded of byType.values()) folded.endpoints.sort();
  return [...byType.values()];
}

// endpointListSummary shows the first few folded endpoints; the table cell's
// tooltip carries the full list.
export function endpointListSummary(endpoints, max = 3) {
  const list = endpoints || [];
  if (list.length === 0) return "-";
  const shown = list.slice(0, max).join(", ");
  return list.length > max ? shown + " +" + (list.length - max) : shown;
}

// --- Label chips ---

export { labelColor } from "../../lib/utils/chartTheme.js";
