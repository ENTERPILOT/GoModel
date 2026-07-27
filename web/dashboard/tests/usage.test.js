// Pure-logic tests for the Usage page, ported from the legacy
// usage.test.cjs (query building, facet exclusion, summary shaping,
// entry helpers, chart series).

import test from "node:test";
import assert from "node:assert/strict";

import {
  cachedCostTitle,
  chartWrapHeight,
  costSourceTooltip,
  divergingDataFrom,
  emptyUsagePageSummary,
  facetOptionList,
  hasProviderCache,
  labelColor,
  providerCacheLabel,
  providerCacheTitle,
  proSavedPercent,
  proSavedPercentText,
  proSavedTitle,
  proSavedValueText,
  rewriteCostSaved,
  rewriteSavingsVisible,
  rewriteTokensSaved,
  usageEntryCached,
  usageEntryCacheLabel,
  usageFilterQueryStr,
  usageLogHasLabels,
  usageLogQueryParams,
  usagePageCostTitle,
  usagePageRequestsTitle,
  usagePageTotalRequests,
  usageRowsBySelectedValue,
  usageRowTotalTokens,
  userPathUsageChartVisible,
  usesResponseCostPricing,
} from "../src/pages/usage/usage-helpers.js";
import { horizontalUsageChartConfig } from "../src/pages/usage/usage-chart-config.js";

test("usesResponseCostPricing detects provider-reported costs", () => {
  assert.equal(usesResponseCostPricing({ cost_source: "openrouter_credits" }), true);
  assert.equal(usesResponseCostPricing({ cost_source: "xai_cost_in_usd_ticks" }), true);
  assert.equal(usesResponseCostPricing({ cost_source: "model_pricing" }), false);
  assert.equal(usesResponseCostPricing({}), false);
});

test("costSourceTooltip explains provider-reported costs", () => {
  assert.equal(
    costSourceTooltip({ cost_source: "openrouter_credits" }),
    "Costs from OpenRouter USD-based credits.",
  );
  assert.equal(
    costSourceTooltip({ cost_source: "xai_cost_in_usd_ticks" }),
    "Costs from xAI usage.cost_in_usd_ticks.",
  );
  assert.equal(costSourceTooltip({ cost_source: "model_pricing" }), "");
});

test("usageEntryCached detects exact and semantic cache types and ignores others", () => {
  assert.equal(usageEntryCached({ cache_type: "exact" }), true);
  assert.equal(usageEntryCached({ cache_type: " Semantic " }), true);
  assert.equal(usageEntryCached({ cache_type: "" }), false);
  assert.equal(usageEntryCached({}), false);
  assert.equal(usageEntryCached({ cache_type: "other" }), false);
});

test("usageEntryCacheLabel returns capitalized cache type or dash", () => {
  assert.equal(usageEntryCacheLabel({ cache_type: "exact" }), "Exact");
  assert.equal(usageEntryCacheLabel({ cache_type: "SEMANTIC" }), "Semantic");
  assert.equal(usageEntryCacheLabel({}), "-");
  assert.equal(usageEntryCacheLabel({ cache_type: "other" }), "-");
});

test("hasProviderCache detects positive cached_input_tokens", () => {
  assert.equal(hasProviderCache({ cached_input_tokens: 100 }), true);
  assert.equal(hasProviderCache({ cached_input_tokens: 0 }), false);
  assert.equal(hasProviderCache({}), false);
  assert.equal(hasProviderCache(null), false);
});

test("providerCacheLabel renders percentage with one decimal", () => {
  assert.equal(providerCacheLabel({ cached_input_tokens: 50, cached_input_ratio: 0.25 }), "25.0%");
  assert.equal(providerCacheLabel({ cached_input_tokens: 1, cached_input_ratio: 0.1234 }), "12.3%");
  assert.equal(providerCacheLabel({}), "");
});

test("providerCacheTitle reports cached and total input tokens, with cache write when present", () => {
  assert.equal(
    providerCacheTitle({ cached_input_tokens: 90, uncached_input_tokens: 50, cache_write_input_tokens: 0 }),
    "90 cached / 140 input tokens",
  );
  assert.equal(
    providerCacheTitle({ cached_input_tokens: 90, uncached_input_tokens: 50, cache_write_input_tokens: 30 }),
    "90 cached / 170 input tokens\n30 cache write",
  );
  assert.equal(providerCacheTitle({}), "");
});

test("cachedCostTitle prepends savings note for cached entries and passes through otherwise", () => {
  assert.equal(
    cachedCostTitle({ cache_type: "exact" }, "12 tokens"),
    "Saved by cache — not charged\n12 tokens",
  );
  assert.equal(cachedCostTitle({ cache_type: "semantic" }, ""), "Saved by cache — not charged");
  assert.equal(cachedCostTitle({}, "12 tokens"), "12 tokens");
  assert.equal(cachedCostTitle({}, ""), "");
});

// --- Query building ---

test("usageFilterQueryStr includes every active filter, URL-encoded", () => {
  const qs = usageFilterQueryStr({
    model: "gpt-5",
    provider: "openai",
    label: "env:prod",
    user_path: "/team",
  });
  assert.equal(qs, "&model=gpt-5&provider=openai&label=env%3Aprod&user_path=%2Fteam");
});

test("usageFilterQueryStr skips empty filters", () => {
  assert.equal(usageFilterQueryStr({ model: "", provider: "", label: "", user_path: "" }), "");
  assert.equal(usageFilterQueryStr({ label: "team alpha" }), "&label=team%20alpha");
});

test("usageFilterQueryStr honors every filter except the excluded facet", () => {
  const filters = {
    model: "gpt-5",
    provider: "openai",
    label: "env:prod",
    user_path: "/team",
  };
  const withoutModel = usageFilterQueryStr(filters, "model");
  assert.doesNotMatch(withoutModel, /model=/);
  assert.match(withoutModel, /provider=openai/);
  assert.match(withoutModel, /label=env%3Aprod/);
  assert.match(withoutModel, /user_path=%2Fteam/);

  const withoutProvider = usageFilterQueryStr(filters, "provider");
  assert.doesNotMatch(withoutProvider, /provider=/);
  assert.match(withoutProvider, /model=gpt-5/);

  const withoutLabel = usageFilterQueryStr(filters, "label");
  assert.doesNotMatch(withoutLabel, /label=/);
  assert.match(withoutLabel, /provider=openai/);
});

test("usageLogQueryParams includes cache_mode=all by default so cached records are returned", () => {
  const qs = usageLogQueryParams({ limit: 50, offset: 0, hideCached: false, search: "" });
  assert.match(qs, /limit=50/);
  assert.match(qs, /offset=0/);
  assert.match(qs, /cache_mode=all/);
  assert.doesNotMatch(qs, /cache_mode=uncached/);
  assert.doesNotMatch(qs, /search=/);
});

test("usageLogQueryParams switches to cache_mode=uncached when hide-cached is on", () => {
  const qs = usageLogQueryParams({ limit: 50, offset: 100, hideCached: true, search: "" });
  assert.match(qs, /cache_mode=uncached/);
  assert.match(qs, /offset=100/);
});

test("usageLogQueryParams URL-encodes the search term", () => {
  const qs = usageLogQueryParams({ limit: 50, offset: 0, hideCached: false, search: "team alpha" });
  assert.match(qs, /search=team%20alpha/);
});

// --- Facet options ---

test("facetOptionList sorts choices and keeps a stale selection listed", () => {
  assert.deepEqual(facetOptionList(["prod", "alpha"], ""), ["alpha", "prod"]);
  assert.deepEqual(facetOptionList(["gpt-5"], ""), ["gpt-5"]);
  assert.deepEqual(facetOptionList(["prod", "alpha"], "removed"), ["alpha", "prod", "removed"]);
  assert.deepEqual(facetOptionList(["prod", "alpha"], "prod"), ["alpha", "prod"]);
});

// --- Stat cards ---

test("usage page stat cards follow the log cache scope and derive hits from the two summaries", () => {
  const uncached = { total_requests: 90, total_cost: 1.5, total_input_cost: 1.0, total_output_cost: 0.5 };
  const all = { total_requests: 100 };

  // Default log view shows cached rows, so the card counts all rows.
  assert.equal(usagePageTotalRequests(uncached, all, false), 100);
  assert.equal(usagePageRequestsTitle(uncached, all, false), "90 to providers + 10 from cache");
  assert.equal(usagePageCostTitle(uncached), "$1.00 input + $0.50 output");

  // Hiding cached rows narrows the card to provider requests, like the log.
  assert.equal(usagePageTotalRequests(uncached, all, true), 90);
  assert.equal(usagePageRequestsTitle(uncached, all, true), "10 cached requests hidden");

  // No cached traffic: plain count, no tooltip.
  assert.equal(usagePageTotalRequests(uncached, { total_requests: 90 }, false), 90);
  assert.equal(usagePageRequestsTitle(uncached, { total_requests: 90 }, false), "");

  assert.equal(usagePageCostTitle({}), "");
});

test("usage page Pro Saved card shows only when rewriters saved tokens", () => {
  // No savings reported: card hidden, zero-safe values.
  assert.equal(rewriteSavingsVisible({ total_requests: 10 }), false);
  assert.equal(rewriteTokensSaved({ total_requests: 10 }), 0);
  assert.equal(rewriteCostSaved({ total_requests: 10 }), null);
  assert.equal(proSavedTitle({ total_requests: 10 }, "tokens"), "");

  // Savings with pricing: card visible, tokens and cost both available.
  const withSavings = { rewrite_tokens_saved: 4200, rewrite_cost_saved: 0.0125 };
  assert.equal(rewriteSavingsVisible(withSavings), true);
  assert.equal(rewriteTokensSaved(withSavings), 4200);
  assert.equal(rewriteCostSaved(withSavings), 0.0125);
  assert.match(
    proSavedTitle(withSavings, "tokens"),
    /^4,200 prompt tokens removed by request rewriters before reaching providers\n\$0\.0125 saved/,
  );

  // Savings without pricing: tokens still surface, cost stays null.
  assert.equal(rewriteSavingsVisible({ rewrite_tokens_saved: 100, rewrite_cost_saved: null }), true);
  assert.equal(rewriteCostSaved({ rewrite_tokens_saved: 100, rewrite_cost_saved: null }), null);

  // Defensive: negative or garbage totals never surface.
  assert.equal(rewriteSavingsVisible({ rewrite_tokens_saved: -5 }), false);
  assert.equal(rewriteSavingsVisible({ rewrite_tokens_saved: "NaN?" }), false);
});

test("Pro Saved value and share follow the Tokens/Costs mode", () => {
  const summary = {
    total_tokens: 8000,
    total_cost: 0.08,
    rewrite_tokens_saved: 2000,
    rewrite_cost_saved: 0.02,
  };

  // Tokens mode: removed prompt tokens in the overview's short form,
  // 2000 / (8000 + 2000) = 20%.
  assert.equal(proSavedValueText(summary, "tokens"), "2K");
  assert.equal(proSavedPercentText(summary, "tokens"), "20.0% less");

  // Short form scales like the overview cards; exact counts live in the title.
  assert.equal(proSavedValueText({ rewrite_tokens_saved: 2186 }, "tokens"), "2.2K");
  assert.equal(proSavedValueText({ rewrite_tokens_saved: 3_400_000 }, "tokens"), "3.4M");
  assert.equal(proSavedValueText({ rewrite_tokens_saved: 840 }, "tokens"), "840");
  assert.match(proSavedTitle({ rewrite_tokens_saved: 2186 }, "tokens"), /^2,186 prompt tokens/);

  // Costs mode: priced savings against the recorded spend, same 20%.
  assert.equal(proSavedValueText(summary, "costs"), "$0.02");
  assert.equal(proSavedPercentText(summary, "costs"), "20.0% less");

  // Tiny but non-zero shares stay visible rather than rounding to 0.0%.
  assert.equal(
    proSavedPercentText({ total_tokens: 10_000_000, rewrite_tokens_saved: 1 }, "tokens"),
    "<0.1% less",
  );

  // No pricing in costs mode: the value renders, the share is suppressed.
  const unpriced = { total_tokens: 900, total_cost: null, rewrite_tokens_saved: 100 };
  assert.equal(proSavedValueText(unpriced, "costs"), "---");
  assert.equal(proSavedPercentText(unpriced, "costs"), "");
  assert.equal(proSavedPercent(unpriced, "costs"), null);
  assert.equal(proSavedPercentText(unpriced, "tokens"), "10.0% less");

  // Priced savings against an unpriced period: the baseline is unknown, not
  // zero, so the value still renders but the share must stay suppressed
  // rather than claim the period was all savings.
  const savedButUnpriced = {
    total_tokens: 900,
    total_cost: null,
    rewrite_tokens_saved: 100,
    rewrite_cost_saved: 0.02,
  };
  assert.equal(proSavedValueText(savedButUnpriced, "costs"), "$0.02");
  assert.equal(proSavedPercent(savedButUnpriced, "costs"), null);
  assert.equal(proSavedPercentText(savedButUnpriced, "costs"), "");

  // Same for a missing token baseline.
  assert.equal(proSavedPercentText({ rewrite_tokens_saved: 100 }, "tokens"), "");

  // A genuinely zero baseline is knowable, so the share still renders.
  assert.equal(
    proSavedPercentText({ total_tokens: 0, rewrite_tokens_saved: 100 }, "tokens"),
    "100.0% less",
  );

  // No savings at all: nothing to show in either mode.
  assert.equal(proSavedPercentText({ total_tokens: 500 }, "tokens"), "");
  assert.equal(proSavedPercentText({}, "costs"), "");
});

test("emptyUsagePageSummary carries the cache split and rewrite fields", () => {
  const summary = emptyUsagePageSummary();
  assert.equal(summary.total_requests, 0);
  assert.equal(summary.uncached_input_tokens, 0);
  assert.equal(summary.cached_input_tokens, 0);
  assert.equal(summary.cache_write_input_tokens, 0);
  assert.equal(summary.total_cost, null);
  assert.equal(summary.rewrite_tokens_saved, 0);
  assert.equal(summary.rewrite_cost_saved, null);
});

// --- Labels ---

test("usageLogHasLabels reflects aggregates, active filter, or labelled entries", () => {
  assert.equal(usageLogHasLabels([], "", [{ labels: null }, {}]), false);
  assert.equal(usageLogHasLabels([], "", [{ labels: ["alpha"] }]), true);
  assert.equal(usageLogHasLabels([], "alpha", []), true);
  assert.equal(usageLogHasLabels([{ label: "alpha" }], "", []), true);
});

test("labelColor is deterministic and stays inside the palette", () => {
  assert.equal(labelColor("prod"), labelColor("prod"));
  assert.match(labelColor("prod"), /^#[0-9a-f]{6}$/);
  assert.match(labelColor(""), /^#[0-9a-f]{6}$/);
});

// --- Breakdown rows and chart series ---

test("usageRowTotalTokens uses total_tokens and falls back to input plus output", () => {
  assert.equal(usageRowTotalTokens({ total_tokens: 155, input_tokens: 120, output_tokens: 30 }), 155);
  assert.equal(usageRowTotalTokens({ input_tokens: 120, output_tokens: 30 }), 150);
  assert.equal(usageRowTotalTokens(null), 0);
});

test("usageRowsBySelectedValue orders by tokens or costs depending on mode", () => {
  const rows = [
    { model: "a", total_tokens: 10, total_cost: 5 },
    { model: "b", total_tokens: 30, total_cost: 1 },
  ];
  assert.deepEqual(
    usageRowsBySelectedValue(rows, false).map((r) => r.model),
    ["b", "a"],
  );
  assert.deepEqual(
    usageRowsBySelectedValue(rows, true).map((r) => r.model),
    ["a", "b"],
  );
});

test("userPathUsageChartVisible hides the chart for the sole root path", () => {
  assert.equal(userPathUsageChartVisible([]), false);
  assert.equal(userPathUsageChartVisible([{ user_path: "/" }]), false);
  assert.equal(userPathUsageChartVisible([{ user_path: "" }]), false);
  assert.equal(userPathUsageChartVisible([{ user_path: "/team" }]), true);
  assert.equal(userPathUsageChartVisible([{ user_path: "/" }, { user_path: "/team" }]), true);
});

test("divergingDataFrom splits paid input from cache series in tokens mode", () => {
  const rows = [
    {
      model: "a",
      input_tokens: 100,
      output_tokens: 40,
      uncached_input_tokens: 60,
      cached_input_tokens: 30,
      cache_write_input_tokens: 10,
      local_cached_input_tokens: 5,
      local_cached_output_tokens: 7,
    },
  ];
  const series = divergingDataFrom(rows, (r) => r.model, false);
  assert.deepEqual(series.labels, ["a"]);
  assert.deepEqual(series.inputs, [70]); // uncached + cache write
  assert.deepEqual(series.outputs, [40]);
  assert.deepEqual(series.prompts, [30]);
  assert.deepEqual(series.localIns, [5]);
  assert.deepEqual(series.localOuts, [7]);
});

test("divergingDataFrom falls back to full input when the cache split is absent", () => {
  const series = divergingDataFrom(
    [{ model: "a", input_tokens: 100, output_tokens: 40 }],
    (r) => r.model,
    false,
  );
  assert.deepEqual(series.inputs, [100]);
});

test("divergingDataFrom caps the prompt-cached cost at the recorded input cost", () => {
  const series = divergingDataFrom(
    [{ model: "a", input_cost: 1.0, output_cost: 0.4, cached_input_cost: 2.5 }],
    (r) => r.model,
    true,
  );
  assert.deepEqual(series.prompts, [1.0]);
  assert.deepEqual(series.inputs, [0]); // input cost minus capped prompt cost
  assert.deepEqual(series.outputs, [0.4]);
  assert.deepEqual(series.localIns, [0]); // no local cost series in costs mode
  assert.deepEqual(series.localOuts, [0]);
});

test("divergingDataFrom folds rows past the top 10 into Other", () => {
  const rows = [];
  for (let i = 0; i < 12; i++) {
    rows.push({ model: "m" + i, input_tokens: 100 - i, output_tokens: 1, total_tokens: 101 - i });
  }
  const series = divergingDataFrom(rows, (r) => r.model, false);
  assert.equal(series.labels.length, 11);
  assert.equal(series.labels[10], "Other");
  // The two smallest rows (input 90 + 89) folded together.
  assert.equal(series.inputs[10], 179);
  assert.equal(series.outputs[10], 2);
});

test("chartWrapHeight grows with the row count with a floor of 200", () => {
  assert.equal(chartWrapHeight(0), 200);
  assert.equal(chartWrapHeight(1), 200);
  assert.equal(chartWrapHeight(10), 10 * 32 + 72);
});

// --- Chart config ---

const chartTestColors = {
  grid: "#111",
  text: "#222",
  tooltipBg: "#333",
  tooltipBorder: "#444",
  tooltipText: "#555",
};

test("horizontalUsageChartConfig negates input-side series in the diverging view", () => {
  const series = {
    inputs: [70],
    outputs: [40],
    prompts: [30],
    localIns: [5],
    localOuts: [7],
  };
  const config = horizontalUsageChartConfig(chartTestColors, ["a"], series, {
    stacked: false,
    costs: false,
  });
  assert.equal(config.type, "bar");
  assert.equal(config.options.indexAxis, "y");
  const byLabel = Object.fromEntries(config.data.datasets.map((d) => [d.label, d.data]));
  assert.deepEqual(byLabel["Input Tokens"], [-70]);
  assert.deepEqual(byLabel["Output Tokens"], [40]);
  assert.deepEqual(byLabel["Prompt Cached"], [-30]);
  assert.deepEqual(byLabel["Locally Cached (Input)"], [-5]);
  assert.deepEqual(byLabel["Locally Cached (Output)"], [7]);
});

test("horizontalUsageChartConfig piles everything rightward in the stacked view", () => {
  const series = { inputs: [70], outputs: [40], prompts: [30], localIns: [0], localOuts: [0] };
  const config = horizontalUsageChartConfig(chartTestColors, ["a"], series, {
    stacked: true,
    costs: false,
  });
  const byLabel = Object.fromEntries(config.data.datasets.map((d) => [d.label, d.data]));
  assert.deepEqual(byLabel["Input Tokens"], [70]);
  assert.deepEqual(byLabel["Prompt Cached"], [30]);
  // Zero-valued cache series are dropped entirely.
  assert.equal(byLabel["Locally Cached (Input)"], undefined);
  assert.equal(byLabel["Locally Cached (Output)"], undefined);
});

test("horizontalUsageChartConfig uses cost labels and no local series in costs mode", () => {
  const series = { inputs: [0.6], outputs: [0.4], prompts: [0.1], localIns: [5], localOuts: [7] };
  const config = horizontalUsageChartConfig(chartTestColors, ["a"], series, {
    stacked: false,
    costs: true,
  });
  const labels = config.data.datasets.map((d) => d.label);
  assert.deepEqual(labels, ["Input Cost", "Output Cost", "Prompt Cached Cost"]);
  // Axis ticks format as dollars in costs mode.
  assert.equal(config.options.scales.x.ticks.callback(-1.5), "$1.50");
});
