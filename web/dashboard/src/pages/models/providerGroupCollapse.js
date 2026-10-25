// Collapsed provider groups on the Models page. Pure helpers (state lives in
// providerGroups.svelte.js) so parsing and toggling are testable with node.
// The preference is per browser: group keys are stored in localStorage.

export const COLLAPSED_PROVIDER_GROUPS_STORAGE_KEY =
  "gomodel_models_collapsed_provider_groups";

/** Parse the stored JSON list, dropping anything that is not a group key. */
export function parseCollapsedGroups(raw) {
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return [...new Set(parsed.filter((key) => typeof key === "string" && key))];
  } catch {
    return [];
  }
}

export function serializeCollapsedGroups(keys) {
  return JSON.stringify(Array.isArray(keys) ? keys : []);
}

/** Return a new key list with every one of `groupKeys` collapsed or expanded. */
export function setGroupsCollapsed(keys, groupKeys, collapsed) {
  const current = Array.isArray(keys) ? keys : [];
  const targets = (Array.isArray(groupKeys) ? groupKeys : []).filter(Boolean);
  if (!collapsed) return current.filter((key) => !targets.includes(key));
  return [...new Set([...current, ...targets])];
}

/** True when there is at least one group and every one of them is collapsed. */
export function allGroupsCollapsed(keys, groupKeys) {
  const current = Array.isArray(keys) ? keys : [];
  const targets = (Array.isArray(groupKeys) ? groupKeys : []).filter(Boolean);
  return targets.length > 0 && targets.every((key) => current.includes(key));
}

/** Return a new key list with `key` added or removed. */
export function toggleCollapsedGroup(keys, key) {
  const current = Array.isArray(keys) ? keys : [];
  if (!key) return current;
  return current.includes(key)
    ? current.filter((existing) => existing !== key)
    : [...current, key];
}
