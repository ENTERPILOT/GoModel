// Shared usage summary / daily series / cache overview used by the overview
// and usage pages. Pages layer their own filters via the extraQuery argument
// (the usage page filters everything; the overview stays unfiltered).

import { getJSON, isAbortError } from "$lib/api/client.js";
import { dateRange } from "./dateRange.svelte.js";
import { runtimeConfig } from "./runtimeConfig.svelte.js";

export function emptyUsageSummary() {
  return {
    total_requests: 0,
    total_input_tokens: 0,
    total_output_tokens: 0,
    total_tokens: 0,
    total_input_cost: null,
    total_output_cost: null,
    total_cost: null,
  };
}

export function emptyCacheOverview() {
  return {
    summary: {
      total_hits: 0,
      exact_hits: 0,
      semantic_hits: 0,
      total_input_tokens: 0,
      total_output_tokens: 0,
      total_tokens: 0,
      total_saved_cost: null,
    },
    daily: [],
  };
}

class UsageDataStore {
  summary = $state(emptyUsageSummary());
  daily = $state([]);
  cacheOverview = $state(emptyCacheOverview());
  loading = $state(false);
  #usageController = null;
  #cacheController = null;

  cacheAnalyticsEnabled() {
    return runtimeConfig.cacheVisible();
  }

  async fetchUsage() {
    if (this.#usageController) this.#usageController.abort();
    const controller = new AbortController();
    this.#usageController = controller;
    this.loading = true;
    try {
      const queryStr = dateRange.queryStr() + "&interval=" + dateRange.interval;
      const [summaryResult, dailyResult] = await Promise.all([
        getJSON("/admin/usage/summary?" + queryStr, {
          label: "usage summary",
          signal: controller.signal,
        }),
        getJSON("/admin/usage/daily?" + queryStr, {
          label: "usage daily",
          signal: controller.signal,
        }),
      ]);
      if (summaryResult.stale || dailyResult.stale) return;
      if (controller.signal.aborted) return;
      if (!summaryResult.ok || !dailyResult.ok) {
        // Reset cache overview too: Total Requests and the cache meter derive
        // from it, so leaving it stale would show the previous period's cache
        // hits next to the empty summary.
        this.summary = emptyUsageSummary();
        this.daily = [];
        this.cacheOverview = emptyCacheOverview();
        return;
      }
      this.summary = summaryResult.data || emptyUsageSummary();
      this.daily = Array.isArray(dailyResult.data) ? dailyResult.data : [];
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch usage:", e);
      this.summary = emptyUsageSummary();
      this.daily = [];
    } finally {
      if (this.#usageController === controller) {
        this.#usageController = null;
        this.loading = false;
      }
    }
  }

  // fetchCacheOverview loads cache analytics for the current window.
  // extraQuery: additional &facet=value filters (usage page only).
  async fetchCacheOverview(extraQuery = "") {
    await runtimeConfig.ensureLoaded();
    if (!this.cacheAnalyticsEnabled()) {
      this.cacheOverview = emptyCacheOverview();
      return;
    }
    if (this.#cacheController) this.#cacheController.abort();
    const controller = new AbortController();
    this.#cacheController = controller;
    try {
      const queryStr =
        dateRange.queryStr() + "&interval=" + dateRange.interval + extraQuery;
      const result = await getJSON("/admin/cache/overview?" + queryStr, {
        label: "cache overview",
        signal: controller.signal,
      });
      if (result.stale) return;
      if (controller.signal.aborted) return;
      if (!result.ok) {
        this.cacheOverview = emptyCacheOverview();
        return;
      }
      const payload =
        result.data && typeof result.data === "object"
          ? result.data
          : emptyCacheOverview();
      if (!payload.summary) {
        payload.summary = emptyCacheOverview().summary;
      }
      if (!Array.isArray(payload.daily)) {
        payload.daily = [];
      }
      this.cacheOverview = payload;
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch cache overview:", e);
      this.cacheOverview = emptyCacheOverview();
    } finally {
      if (this.#cacheController === controller) {
        this.#cacheController = null;
      }
    }
  }
}

export const usageData = new UsageDataStore();
