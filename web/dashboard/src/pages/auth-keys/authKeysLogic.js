// Pure logic for the API Keys page.

import * as m from "../../lib/paraglide/messages.js";

export function defaultAuthKeyForm() {
  return {
    name: "",
    description: "",
    user_path: "",
    labels: "",
    dashboard_access: false,
    expires_at: "",
  };
}

// parseAuthKeyLabels splits a comma-separated label string into a trimmed,
// de-duplicated list (order preserved).
export function parseAuthKeyLabels(value) {
  const labels = [];
  for (const piece of String(value || "").split(",")) {
    const label = piece.trim();
    if (label && !labels.includes(label)) {
      labels.push(label);
    }
  }
  return labels;
}

// authKeyUserPathValidationError returns a human-readable validation error
// for a user-path binding, or "" when the value is acceptable.
export function authKeyUserPathValidationError(value) {
  const trimmed = String(value || "").trim();
  if (!trimmed) {
    return "";
  }
  const raw = trimmed.startsWith("/") ? trimmed : "/" + trimmed;
  for (const part of raw.split("/")) {
    const segment = String(part || "").trim();
    if (!segment) {
      continue;
    }
    if (segment === "." || segment === "..") {
      return m.api_keys_user_path_segments();
    }
    if (segment.includes(":")) {
      return m.api_keys_user_path_colon();
    }
  }
  return "";
}

// normalizeAuthKeyUserPath canonicalizes a user path (leading slash, trimmed
// segments, empty segments dropped). Invalid paths normalize to "".
export function normalizeAuthKeyUserPath(value) {
  if (authKeyUserPathValidationError(value)) {
    return "";
  }
  const trimmed = String(value || "").trim();
  if (!trimmed) {
    return "";
  }
  const raw = trimmed.startsWith("/") ? trimmed : "/" + trimmed;
  const canonical = [];
  for (const part of raw.split("/")) {
    const segment = String(part || "").trim();
    if (!segment) {
      continue;
    }
    canonical.push(segment);
  }
  if (!canonical.length) {
    return "/";
  }
  return "/" + canonical.join("/");
}

// buildCreateAuthKeyPayload validates the create form and builds the POST
// /admin/auth-keys payload. Returns { error } on validation failure, or
// { payload } on success. Date-only expirations are serialized to the end of
// the selected UTC day.
export function buildCreateAuthKeyPayload(form) {
  const source = form || {};
  const name = String(source.name || "").trim();
  if (!name) {
    return { error: m.api_keys_name_required() };
  }
  const userPathError = authKeyUserPathValidationError(source.user_path);
  if (userPathError) {
    return { error: userPathError };
  }
  const userPath = normalizeAuthKeyUserPath(source.user_path);
  const labels = parseAuthKeyLabels(source.labels);
  const payload = {
    name,
    description: String(source.description || "").trim() || undefined,
    user_path: userPath || undefined,
    labels: labels.length ? labels : undefined,
    dashboard_access: source.dashboard_access ? true : undefined,
  };
  if (source.expires_at) {
    payload.expires_at = source.expires_at + "T23:59:59Z";
  }
  return { payload };
}


// authKeyExpired reports whether a key's expiration has passed. Keys without
// an expiration never expire; an unparseable timestamp is treated as not
// expired so a bad value never hides a working key.
export function authKeyExpired(key, now = Date.now()) {
  const raw = key && key.expires_at;
  if (!raw) {
    return false;
  }
  const expiresAt = Date.parse(raw);
  return Number.isFinite(expiresAt) && expiresAt <= now;
}

// authKeyDeactivated reports whether a key was permanently deactivated, as
// opposed to merely expired.
export function authKeyDeactivated(key) {
  if (!key) {
    return false;
  }
  return Boolean(key.deactivated_at) || key.enabled === false;
}

// authKeyActive mirrors the backend's `active` flag but re-derives the two
// inputs it can check locally — deactivation and expiration — so visibility
// and sort ranking always agree even if a payload contradicts itself.
export function authKeyActive(key, now = Date.now()) {
  if (!key || key.active === false || authKeyDeactivated(key)) {
    return false;
  }
  return !authKeyExpired(key, now);
}

// authKeySearchText builds the haystack the toolbar query matches against.
function authKeySearchText(key) {
  return [
    key.name,
    key.description,
    key.user_path,
    key.redacted_value,
    ...(key.labels || []),
  ]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
}

// filterAuthKeys applies the API Keys toolbar: inactive keys (deactivated or
// expired) are hidden unless `showInactive`, and `query` matches the name,
// description, user path, labels, or redacted token.
export function filterAuthKeys(keys, options = {}) {
  const { query = "", showInactive = false, now = Date.now() } = options;
  const needle = String(query || "").trim().toLowerCase();
  const list = Array.isArray(keys) ? keys : [];
  return list.filter((key) => {
    if (!showInactive && !authKeyActive(key, now)) {
      return false;
    }
    return !needle || authKeySearchText(key).includes(needle);
  });
}

// authKeyRank groups keys by how usable they are: live keys first, then
// expired ones, then permanently deactivated ones at the bottom.
function authKeyRank(key, now) {
  if (authKeyDeactivated(key)) {
    return 2;
  }
  return authKeyActive(key, now) ? 0 : 1;
}

// expiresAtValue orders expirations by remaining lifetime: a key that never
// expires outlives every dated one, so it sorts above them.
function expiresAtValue(key) {
  const raw = key && key.expires_at;
  if (!raw) {
    return Infinity;
  }
  const parsed = Date.parse(raw);
  return Number.isFinite(parsed) ? parsed : Infinity;
}

// deactivatedAtValue orders deactivations most-recent-first; an unknown
// timestamp sorts last within the group.
function deactivatedAtValue(key) {
  const parsed = Date.parse((key && key.deactivated_at) || "");
  return Number.isFinite(parsed) ? parsed : -Infinity;
}

// sortAuthKeys orders the table: live keys first (longest-lived at the top,
// so keys about to expire surface at the bottom of the group), then expired
// keys most-recently-expired first, then deactivated keys most-recently-
// deactivated first. Names break every tie so the order stays stable.
export function sortAuthKeys(keys, now = Date.now()) {
  const list = Array.isArray(keys) ? keys.slice() : [];
  return list.sort((a, b) => {
    const rankA = authKeyRank(a, now);
    const rankB = authKeyRank(b, now);
    if (rankA !== rankB) {
      return rankA - rankB;
    }
    const [valueA, valueB] =
      rankA === 2
        ? [deactivatedAtValue(a), deactivatedAtValue(b)]
        : [expiresAtValue(a), expiresAtValue(b)];
    if (valueA !== valueB) {
      return valueA > valueB ? -1 : 1;
    }
    return String(a.name || "").localeCompare(String(b.name || ""));
  });
}

// countInactiveAuthKeys counts the keys hidden by the default view.
export function countInactiveAuthKeys(keys, now = Date.now()) {
  const list = Array.isArray(keys) ? keys : [];
  return list.reduce((total, key) => total + (authKeyActive(key, now) ? 0 : 1), 0);
}

// Label chips share the dashboard-wide palette and chip styling.
export { labelChipStyle, labelColor } from "../../lib/utils/chartTheme.js";
