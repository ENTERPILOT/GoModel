// Rows and series behind the usage breakdown charts and tables.
// Kept free of Svelte/DOM dependencies so node:test can exercise it.

import * as m from "../../lib/paraglide/messages.js";

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
