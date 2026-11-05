// Usage filter dropdown choices: facet options and API key names.
// Kept free of Svelte/DOM dependencies so node:test can exercise it.

import { shortSessionID } from "./usage-helpers.js";

// filterValues normalizes one filter to its list of selected values: a
// string is one value, an array its non-empty entries.
export function filterValues(value) {
  if (Array.isArray(value)) return value.filter((item) => item != null && String(item) !== "");
  return value ? [value] : [];
}

// Sorted, deduplicated choices for one facet dropdown. Active selections
// stay listed so the dropdown can still uncheck a value that has no traffic
// under the other filters.
export function facetOptionList(values, active) {
  const set = new Set(values || []);
  for (const value of filterValues(active)) set.add(value);
  return [...set].sort();
}

// Maps managed auth key ids to their names from an /admin/auth-keys response.
export function authKeyNameMap(keys) {
  const names = {};
  for (const key of keys || []) {
    const id = String((key && key.id) || "").trim();
    if (id) names[id] = String(key.name || "").trim() || id;
  }
  return names;
}

// Usage rows store the key id; show the key's name when it is known (a key
// outside the caller's scope or since deleted falls back to a short id).
export function authKeyDisplayName(id, names) {
  const keyID = String(id || "").trim();
  if (!keyID) return "-";
  return (names && names[keyID]) || shortSessionID(keyID);
}

// API key filter choices sorted by name, as SearchSelect options. Active keys
// stay listed even when no longer known, so they can still be unchecked.
export function authKeyOptionList(names, active) {
  const options = Object.entries(names || {}).map(([id, name]) => ({ id, name }));
  for (const id of filterValues(active)) {
    if (!(names && names[id])) options.push({ id, name: authKeyDisplayName(id, names) });
  }
  return options
    .sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id))
    .map((option) => ({ ...option, value: option.id, label: option.name }));
}

// The request log shows its API key column only when there is something to
// show: an entry made with a managed key, or an active key filter.
export function usageLogHasAuthKeys(filterAuthKeys, logEntries) {
  if (filterValues(filterAuthKeys).length > 0) return true;
  return (logEntries || []).some((entry) => entry && entry.auth_key_id);
}
