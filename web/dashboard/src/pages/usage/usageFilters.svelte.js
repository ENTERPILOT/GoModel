// The usage page's data filter state, shared by every widget on the page and
// by the live request-log merge (which only inserts previews while no filter
// is active). The filters themselves are listed once in usage-query.js.

import { USAGE_FILTERS, emptyUsageFilters, usageFiltersEmpty } from "./usage-query.js";

class UsageFilterState {
  // Selected values per filter; empty means all. Keys match USAGE_FILTERS.
  models = $state([]);
  providers = $state([]);
  labels = $state([]);
  userPaths = $state([]);
  authKeys = $state([]);
  endpoints = $state([]);
  session = $state("");

  values() {
    return Object.fromEntries(USAGE_FILTERS.map((filter) => [filter.key, this[filter.key]]));
  }

  set(values) {
    const next = { ...emptyUsageFilters(), ...values };
    for (const filter of USAGE_FILTERS) this[filter.key] = next[filter.key];
  }

  clear() {
    this.set({});
  }

  isEmpty() {
    return usageFiltersEmpty(this.values());
  }
}

export const usageFilters = new UsageFilterState();
