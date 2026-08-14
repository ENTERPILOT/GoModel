// Summary-card totals + cache meter math. Pure functions taking the usage
// summary / cache overview payloads.

import { formatNumber } from "../../lib/utils/format.js";
import * as m from "../../lib/paraglide/messages.js";

export function summaryTotalTokens(summary) {
  const s = summary || {};
  if (s.total_tokens !== null && s.total_tokens !== undefined) {
    const total = Number(s.total_tokens);
    if (Number.isFinite(total)) {
      return total;
    }
  }
  const input = Number(s.total_input_tokens || 0);
  const output = Number(s.total_output_tokens || 0);
  return (Number.isFinite(input) ? input : 0) + (Number.isFinite(output) ? output : 0);
}

// Local response-cache hits over the period. The summary endpoint counts
// provider (uncached-mode) requests only, so hits live in the cache overview.
// Zero when cache analytics is off (overview unfetched).
function summaryCacheHits(cacheOverview, cacheEnabled) {
  if (!cacheEnabled) return 0;
  const cacheSummary =
    cacheOverview && cacheOverview.summary ? cacheOverview.summary : {};
  const hits = Number(cacheSummary.total_hits || 0);
  return Number.isFinite(hits) && hits > 0 ? hits : 0;
}

// Total requests including local cache hits (provider requests + hits).
export function summaryTotalRequests(summary, cacheOverview, cacheEnabled) {
  const requests = Number((summary && summary.total_requests) || 0);
  return (
    (Number.isFinite(requests) ? requests : 0) +
    summaryCacheHits(cacheOverview, cacheEnabled)
  );
}

export function summaryTotalRequestsTitle(summary, cacheOverview, cacheEnabled) {
  const hits = summaryCacheHits(cacheOverview, cacheEnabled);
  if (hits <= 0) return "";
  const provider = summaryTotalRequests(summary, cacheOverview, cacheEnabled) - hits;
  return (
    m.overview_total_requests_help({
      provider: formatNumber(provider),
      cache: formatNumber(hits),
    })
  );
}

export function cacheOverviewTotalTokens(cacheOverview) {
  const summary =
    cacheOverview && cacheOverview.summary ? cacheOverview.summary : {};
  const input = Number(summary.total_input_tokens || 0);
  const output = Number(summary.total_output_tokens || 0);
  return (Number.isFinite(input) ? input : 0) + (Number.isFinite(output) ? output : 0);
}

// --- Cache meter ---
// Splits the selected period's input tokens into three buckets that sum to
// 100%: not-cached, locally-cached (GoModel response cache), and
// prompt-cached (provider cache reads). The provider split comes from
// /admin/usage/summary (uncached/cached/cache-write over provider rows); the
// local slice from /admin/cache/overview. Both already refresh with the
// period, so the meter follows the picker for free.
function cacheMeterRawSegments(summary, cacheOverview, cacheEnabled) {
  const positive = (value) => {
    const n = Number(value || 0);
    return Number.isFinite(n) && n > 0 ? n : 0;
  };
  const s = summary || {};
  const uncached = positive(s.uncached_input_tokens);
  const promptCached = positive(s.cached_input_tokens);
  const cacheWrite = positive(s.cache_write_input_tokens);
  const cacheSummary =
    cacheOverview && cacheOverview.summary ? cacheOverview.summary : {};
  const locallyCached = cacheEnabled ? positive(cacheSummary.total_input_tokens) : 0;
  return [
    {
      key: "uncached",
      label: m.overview_cache_regular(),
      tokens: uncached + cacheWrite,
      colorVar: "--cache-meter-uncached",
      note:
        cacheWrite > 0
          ? m.overview_cache_regular_note({ count: formatNumber(cacheWrite) })
          : "",
    },
    {
      key: "prompt",
      label: m.overview_cache_prompt(),
      tokens: promptCached,
      colorVar: "--cache-meter-prompt",
      note: m.overview_cache_prompt_note(),
    },
    {
      key: "local",
      label: m.overview_cache_local(),
      tokens: locallyCached,
      colorVar: "--cache-meter-local",
      note: m.overview_cache_local_note(),
    },
  ];
}

function cacheMeterTotal(summary, cacheOverview, cacheEnabled) {
  return cacheMeterRawSegments(summary, cacheOverview, cacheEnabled).reduce(
    (sum, seg) => sum + seg.tokens,
    0,
  );
}

export function cacheMeterVisible(summary, cacheOverview, cacheEnabled) {
  return cacheMeterTotal(summary, cacheOverview, cacheEnabled) > 0;
}

// The three fixed categories with integer percentages. Largest-remainder
// rounding keeps the percents summing to exactly 100 when there is data; with
// no usage every category is 0% so the meter can still render as an empty
// key. Used by the legend (all three shown) and, filtered to non-zero, by the
// bar segments.
export function cacheMeterCategories(summary, cacheOverview, cacheEnabled) {
  const categories = cacheMeterRawSegments(summary, cacheOverview, cacheEnabled);
  const total = categories.reduce((sum, seg) => sum + seg.tokens, 0);
  if (total <= 0) {
    return categories.map((seg) => Object.assign({}, seg, { pct: 0 }));
  }
  const withPct = categories.map((seg) => {
    const exact = (seg.tokens / total) * 100;
    const floor = Math.floor(exact);
    return Object.assign({}, seg, { pct: floor, remainder: exact - floor });
  });
  let leftover = 100 - withPct.reduce((sum, seg) => sum + seg.pct, 0);
  withPct
    .map((seg, index) => ({ index, remainder: seg.remainder, tokens: seg.tokens }))
    .filter((entry) => entry.tokens > 0)
    .sort((a, b) => b.remainder - a.remainder)
    .forEach((entry) => {
      if (leftover > 0) {
        withPct[entry.index].pct += 1;
        leftover -= 1;
      }
    });
  return withPct;
}

// Non-zero categories only, so the bar never renders zero-width slivers.
// Empty when there is no usage.
export function cacheMeterSegments(summary, cacheOverview, cacheEnabled) {
  return cacheMeterCategories(summary, cacheOverview, cacheEnabled).filter(
    (seg) => seg.tokens > 0,
  );
}

export function cacheMeterSegmentTitle(segment) {
  const parts = [
    m.overview_cache_segment_title({
      label: segment.label,
      tokens: formatNumber(segment.tokens),
      percent: segment.pct,
    }),
  ];
  if (segment.note) parts.push(segment.note);
  return parts.join("\n");
}

export function cacheMeterAriaLabel(segments) {
  const parts = (segments || []).map((seg) => seg.label + " " + seg.pct + "%");
  return (
    m.overview_cache_breakdown_label({
      details: parts.length ? parts.join(", ") : m.overview_no_data(),
    })
  );
}
