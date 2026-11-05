// Request labels on the usage page: the log's label column and chip colors.
// Kept free of Svelte/DOM dependencies so node:test can exercise it.

import { filterValues } from "./usage-filter-options.js";

export function entryLabels(entry) {
  return Array.isArray(entry && entry.labels) ? entry.labels : [];
}

// The Labels column only appears when labels are in play: the period has
// by-label aggregates, a label filter is active, or the current log page
// carries labelled entries (e.g. cached-only labelled traffic, which the
// uncached-mode aggregates omit).
export function usageLogHasLabels(labelUsage, filterLabels, logEntries) {
  if ((labelUsage || []).length > 0 || filterValues(filterLabels).length > 0) return true;
  return (logEntries || []).some((entry) => entryLabels(entry).length > 0);
}

export { labelColor } from "../../lib/utils/chartTheme.js";
