// Cost-per-active-user section state: the group, month range and cost basis
// picked, and the last /admin/usage/benchmark result. The page filters apply;
// the date picker does not, since the benchmark reads whole months.

import { getJSON, isAbortError } from "$lib/api/client.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import { benchmarkQuery, DEFAULT_BENCHMARK_MONTHS } from "./benchmark-helpers.js";
import { usageFilters } from "./usageFilters.svelte.js";

class UsageBenchmarkState {
  // parent is the group; empty asks for the API default (the caller's scope
  // root, or /).
  parent = $state("");
  months = $state(DEFAULT_BENCHMARK_MONTHS);
  costBasis = $state("provider");
  view = $state("chart");
  data = $state(null);
  loading = $state(false);
  failed = $state(false);

  #controller = null;

  // set changes one setting and refetches.
  set(field, value) {
    this[field] = value;
    return this.fetch();
  }

  async fetch() {
    if (this.#controller) this.#controller.abort();
    const controller = new AbortController();
    this.#controller = controller;
    this.loading = true;
    const query = benchmarkQuery({
      filters: usageFilters.values(),
      parent: this.parent,
      months: this.months,
      // Billed cost needs markups; without them the toggle is hidden.
      costBasis: runtimeConfig.billedCostVisible() ? this.costBasis : "provider",
    });
    try {
      const result = await getJSON("/admin/usage/benchmark?" + query, {
        label: "usage benchmark",
        signal: controller.signal,
      });
      if (result.stale || controller.signal.aborted) return;
      this.failed = !result.ok;
      this.data = result.ok && result.data && typeof result.data === "object" ? result.data : null;
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch usage benchmark:", e);
      this.failed = true;
      this.data = null;
    } finally {
      if (this.#controller === controller) {
        this.#controller = null;
        this.loading = false;
      }
    }
  }
}

export const usageBenchmark = new UsageBenchmarkState();
