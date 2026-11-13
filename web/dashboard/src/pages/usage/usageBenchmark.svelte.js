// Cost-per-active-user section state: the group, month range and cost basis
// picked, and the last /admin/usage/benchmark result. The page filters apply;
// the date picker does not, since the benchmark reads whole months.

import { getJSON, isAbortError } from "$lib/api/client.js";
import { access } from "$lib/stores/access.svelte.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import {
  benchmarkFetchNeeded,
  benchmarkParentInScope,
  benchmarkQuery,
  DEFAULT_BENCHMARK_MONTHS,
  effectiveBenchmarkCostBasis,
} from "./benchmark-helpers.js";
import { usageFilters } from "./usageFilters.svelte.js";

class UsageBenchmarkState {
  // parent is the group; empty asks for the API default (the caller's scope
  // root, or /).
  parent = $state("");
  months = $state(DEFAULT_BENCHMARK_MONTHS);
  costBasis = $state("provider");
  view = $state("chart");
  data = $state(null);
  // True from the start: before the first answer there is no data, which
  // must not read as "no active users".
  loading = $state(true);
  failed = $state(false);

  #controller = null;
  // The request in flight or shown; null after a failure, so it is retried.
  #query = null;

  // set changes one setting and refetches.
  set(field, value) {
    this[field] = value;
    return this.fetch();
  }

  // fetch loads the benchmark. The usage page calls it with everything else
  // it refetches, so without force it only runs when the request changed
  // (group, months, cost basis, page filters): each run aggregates months
  // of usage, and a date-range change does not alter it.
  async fetch({ force = false } = {}) {
    this.loading = true;
    // Both reload on an API-key change, and the new scope decides whether
    // the picked group may still be asked for. The first await also lets
    // the key change start those reloads before the scope is read.
    await runtimeConfig.ensureLoaded();
    await access.ensureLoaded();
    this.parent = benchmarkParentInScope(this.parent, access);
    const query = benchmarkQuery({
      filters: usageFilters.values(),
      parent: this.parent,
      months: this.months,
      // Billed cost needs markups; without them the toggle is hidden.
      costBasis: effectiveBenchmarkCostBasis(this.costBasis, runtimeConfig),
    });
    if (!benchmarkFetchNeeded(query, this.#query, force)) {
      this.loading = this.#controller !== null;
      return;
    }
    if (this.#controller) this.#controller.abort();
    const controller = new AbortController();
    this.#controller = controller;
    this.#query = query;
    let loaded = false;
    try {
      const result = await getJSON("/admin/usage/benchmark?" + query, {
        label: "usage benchmark",
        signal: controller.signal,
      });
      if (result.stale || controller.signal.aborted) return;
      this.failed = !result.ok;
      this.data = result.ok && result.data && typeof result.data === "object" ? result.data : null;
      loaded = result.ok;
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch usage benchmark:", e);
      this.failed = true;
      this.data = null;
    } finally {
      if (this.#controller === controller) {
        this.#controller = null;
        this.loading = false;
        if (!loaded) this.#query = null;
      }
    }
  }
}

export const usageBenchmark = new UsageBenchmarkState();
