// Usage filter dropdown choices: facet options and API key names.
// Kept free of Svelte/DOM dependencies so node:test can exercise it.

import { authKeyActive } from "../auth-keys/authKeysLogic.js";
import { shortSessionID } from "./usage-helpers.js";
import { normalizeUserPath } from "./user-path-tree.js";

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

// Ids of managed auth keys that are not active (deactivated or expired), as
// a lookup object. Uses the API Keys page's rule, so both hide the same keys.
export function authKeyInactiveIDs(keys, now = Date.now()) {
  const inactive = {};
  for (const key of keys || []) {
    const id = String((key && key.id) || "").trim();
    if (id && !authKeyActive(key, now)) inactive[id] = true;
  }
  return inactive;
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
// Keys in `inactive` are left out unless `showInactive` or selected.
export function authKeyOptionList(names, active, { inactive = {}, showInactive = false } = {}) {
  const selected = new Set(filterValues(active));
  const options = Object.entries(names || {})
    .filter(([id]) => showInactive || !inactive[id] || selected.has(id))
    .map(([id, name]) => ({ id, name }));
  for (const id of filterValues(active)) {
    if (!(names && names[id])) options.push({ id, name: authKeyDisplayName(id, names) });
  }
  return options
    .sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id))
    .map((option) => ({ ...option, value: option.id, label: option.name }));
}

// How many keys the API key filter hides: inactive and not selected.
export function hiddenAuthKeyCount(names, active, inactive) {
  const selected = new Set(filterValues(active));
  return Object.keys(names || {}).filter((id) => inactive && inactive[id] && !selected.has(id)).length;
}

// User paths still in use, from an /admin/users response: a path with a
// policy or at least one active bound key.
export function knownUserPaths(nodes) {
  const known = new Set();
  for (const node of nodes || []) {
    if (!node || !(node.configured === true || (node.active_key_count || 0) > 0)) continue;
    const path = normalizeUserPath(node.user_path);
    if (path) known.add(path);
  }
  return known;
}

// User path filter choices from the facet rows. Unless `showInactive`, only
// known and selected paths stay; the tree still adds their ancestors and the
// scope root. Without a known set (users unavailable) every path stays.
export function userPathOptionList(paths, active, { known = null, showInactive = false } = {}) {
  const list = paths || [];
  if (showInactive || !known) return list;
  const selected = new Set(filterValues(active));
  return list.filter((path) => known.has(normalizeUserPath(path)) || selected.has(path));
}

// How many facet paths the user path filter hides.
export function hiddenUserPathCount(paths, active, known) {
  return (paths || []).length - userPathOptionList(paths, active, { known }).length;
}

// The request log shows its API key column only when there is something to
// show: an entry made with a managed key, or an active key filter.
export function usageLogHasAuthKeys(filterAuthKeys, logEntries) {
  if (filterValues(filterAuthKeys).length > 0) return true;
  return (logEntries || []).some((entry) => entry && entry.auth_key_id);
}
