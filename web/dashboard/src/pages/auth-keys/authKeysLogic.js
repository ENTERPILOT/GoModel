// Pure logic for the API Keys page.

export function defaultAuthKeyForm() {
  return { name: "", description: "", user_path: "", labels: "", expires_at: "" };
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
      return 'User path cannot contain "." or ".." segments.';
    }
    if (segment.includes(":")) {
      return 'User path cannot contain ":" segments.';
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
    return { error: "Name is required." };
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
  };
  if (source.expires_at) {
    payload.expires_at = source.expires_at + "T23:59:59Z";
  }
  return { payload };
}

// authKeyErrorMessage extracts the admin error payload message
// ({ error: { message } }) from a parsed response body.
export function authKeyErrorMessage(data, fallback) {
  if (
    data &&
    typeof data === "object" &&
    data.error &&
    typeof data.error === "object" &&
    typeof data.error.message === "string" &&
    data.error.message
  ) {
    return data.error.message;
  }
  return fallback;
}

// Deterministic label -> palette color so a label keeps one color across the
// usage charts and every chip on the dashboard (same palette + djb2 hash as
// src/pages/usage/usage-helpers.js).
const LABEL_PALETTE = [
  "#c2845a", "#7a9e7e", "#d4a574", "#b8a98e", "#8b9e6b",
  "#7d8a97", "#c47a5a", "#6b8e6b", "#a09486", "#9b7ea4",
  "#c49a6c",
];

export function labelColor(label) {
  let hash = 5381;
  const text = String(label || "");
  for (let i = 0; i < text.length; i++) {
    hash = ((hash << 5) + hash + text.charCodeAt(i)) | 0;
  }
  return LABEL_PALETTE[Math.abs(hash) % LABEL_PALETTE.length];
}

// labelChipStyle returns the inline style string for a usage-label chip.
export function labelChipStyle(label) {
  return "--label-color: " + labelColor(label);
}
