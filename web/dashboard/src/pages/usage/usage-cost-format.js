// Cost, cache and savings text for the usage stat cards and request log.
// Kept free of Svelte/DOM dependencies so node:test can exercise it.

import { formatCost, formatNumber, formatTokensShort } from "../../lib/utils/format.js";
import * as m from "../../lib/paraglide/messages.js";

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

// --- Billed cost (pricing.markups) ---
// The backend reports billed_cost on the model, user path, and label
// aggregates only; endpoint and session breakdowns stay provider cost.
const BILLED_BREAKDOWN_KINDS = new Set(["model", "userPath", "label"]);

export function breakdownHasBilledCost(kind) {
  return BILLED_BREAKDOWN_KINDS.has(kind);
}

// billedCostTitle names the markup rule a request-log row was billed at, or
// says none matched (billed cost then equals provider cost).
export function billedCostTitle(entry) {
  const raw = entry && entry.markup_percent;
  const percent = Number(raw);
  if (raw == null || raw === "" || !Number.isFinite(percent)) {
    return m.usage_billed_no_markup();
  }
  const selector = String((entry && entry.markup_selector) || "").trim();
  const value = formatNumber(percent);
  return selector
    ? m.usage_billed_markup_rule({ percent: value, selector })
    : m.usage_billed_markup({ percent: value });
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
