// Usage page state shapes: empty results for the summaries, the request log
// and the session breakdown, and the short session id display.
// Kept free of Svelte/DOM dependencies so node:test can exercise it.

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
    total_billed_cost: null,
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
