// The usage view as a query: admin API calls and the shareable page URL are
// both built from the filters, the window and the breakdown settings with
// usageQuery, so they cannot drift apart.

import { router } from "$lib/stores/router.svelte.js";
import { dateRange } from "$lib/stores/dateRange.svelte.js";
import { defaultPivotConfig, parseUsageQuery, usageQuery } from "./usage-query.js";
import { usageFilters } from "./usageFilters.svelte.js";
import { usagePivot } from "./usagePivot.svelte.js";

// usageApiQuery renders the window and the filters (minus excludeParam, for
// faceted dropdown options) as an admin API query.
export function usageApiQuery(excludeParam = "") {
  return usageQuery({ filters: usageFilters.values(), excludeParam, window: dateRange.window() });
}

// usageFilterQueryStr renders only the filters, prefixed with "&", for calls
// that add their own window (the shared cache overview).
export function usageFilterQueryStr() {
  const qs = usageQuery({ filters: usageFilters.values() });
  return qs ? "&" + qs : "";
}

// usagePivotQuery renders the breakdown request.
export function usagePivotQuery() {
  return usageQuery({ filters: usageFilters.values(), window: dateRange.window(), pivot: usagePivot.config() });
}

// syncUsageUrl mirrors the view into the address bar without adding history
// entries, so a copied link reproduces it.
export function syncUsageUrl() {
  if (router.page !== "usage" || typeof window === "undefined") return;
  const qs = usageQuery({
    filters: usageFilters.values(),
    window: dateRange.window(),
    pivot: usagePivot.config(),
    omitDefaults: true,
  });
  const { pathname, search, hash } = window.location;
  const url = pathname + (qs ? "?" + qs : "") + hash;
  if (url !== pathname + search + hash) history.replaceState(history.state, "", url);
}

// restoreUsageUrl applies a shared link's view. It runs when the page opens
// with a query string; without one the in-memory view stays as it was.
export function restoreUsageUrl() {
  if (typeof window === "undefined" || !window.location.search) return;
  const view = parseUsageQuery(window.location.search);
  usageFilters.set(view.filters);
  dateRange.applyWindow(view.window);
  // The URL leaves default breakdown settings out, so none means default.
  usagePivot.applyConfig(view.pivot || defaultPivotConfig());
}
