// Breakdown (pivot) section state: the rows/columns/metric configuration and
// the last /admin/usage/breakdown result. The usage page builds the query
// (usage-query.js) and mirrors the configuration into the URL.

import { getJSON, isAbortError } from "$lib/api/client.js";
import { defaultPivotConfig } from "./usage-query.js";

class UsagePivotState {
  rows = $state(defaultPivotConfig().rows);
  columns = $state(defaultPivotConfig().columns);
  metric = $state(defaultPivotConfig().metric);
  interval = $state(defaultPivotConfig().interval);
  depth = $state(defaultPivotConfig().depth);
  view = $state("chart");
  data = $state(null);
  loading = $state(false);
  failed = $state(false);

  #controller = null;

  config() {
    return { rows: this.rows, columns: this.columns, metric: this.metric, interval: this.interval, depth: this.depth };
  }

  applyConfig(config) {
    this.rows = config.rows;
    this.columns = config.columns;
    this.metric = config.metric;
    this.interval = config.interval;
    this.depth = config.depth;
  }

  // set changes one setting; picking the other axis's dimension swaps the
  // two so rows and columns never match.
  set(field, value) {
    if (field === "rows" && value === this.columns) this.columns = this.rows;
    if (field === "columns" && value === this.rows) this.rows = this.columns || this.rows;
    this[field] = value;
    if (this.columns === this.rows) this.columns = "";
  }

  uses(dimension) {
    return this.rows === dimension || this.columns === dimension;
  }

  // fetch loads the breakdown for query, which the usage page builds from
  // its filters, window and this configuration.
  async fetch(query) {
    if (this.#controller) this.#controller.abort();
    const controller = new AbortController();
    this.#controller = controller;
    this.loading = true;
    try {
      const result = await getJSON(
        "/admin/usage/breakdown?" + query,
        { label: "usage breakdown", signal: controller.signal },
      );
      if (result.stale || controller.signal.aborted) return;
      this.failed = !result.ok;
      this.data = result.ok && result.data && typeof result.data === "object" ? result.data : null;
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch usage breakdown:", e);
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

export const usagePivot = new UsagePivotState();
