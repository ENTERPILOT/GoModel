// Nested-error-message extraction, shared by the audit pane's error block and
// the Interactions drawer.
//
// Provider errors reach us wrapped several times over — a JSON string inside a
// `message` field inside a JSON string — so finding the human-readable text
// means unwrapping until something stops parsing. Both readers used to carry
// their own copy of this walk (one recursive, one breadth-first, with
// different depth limits), which meant the same entry could show two different
// error messages depending on where you looked.
//
// Plain ESM, no runes: node --test imports this file directly.

const MAX_DEPTH = 6;

export function tryParseJSON(value) {
  if (typeof value !== "string") return null;
  try {
    return JSON.parse(value);
  } catch {
    return null;
  }
}

// normalizeErrorText unwraps a message that is itself JSON. `fallback` is an
// optional last resort applied to the parsed payload (the drawer pulls message
// content out of it) before giving up and returning the raw text.
export function normalizeErrorText(value, depth = 0, fallback = null) {
  const text = String(value || "").trim();
  if (!text) return "";
  if (depth > MAX_DEPTH) return text;

  const parsed = tryParseJSON(text);
  if (parsed == null) return text;

  const nested = findNestedErrorMessage(parsed, depth + 1, fallback);
  if (nested) return nested;
  return (fallback && fallback(parsed)) || text;
}

// findNestedErrorMessage walks a payload depth-first for the most specific
// error text it can reach: an `error` field (string, `.message`, or nested
// object) first, then a `message` that sits next to error-ish metadata, then
// every remaining value.
export function findNestedErrorMessage(value, depth = 0, fallback = null, seen = null) {
  if (value == null || depth > MAX_DEPTH) return "";

  if (typeof value === "string") {
    const parsed = tryParseJSON(value.trim());
    return parsed == null
      ? ""
      : findNestedErrorMessage(parsed, depth + 1, fallback, seen);
  }

  if (typeof value !== "object") return "";

  // Payloads can be self-referential, and revisiting a node cannot yield a
  // message the first visit missed.
  const visited = seen || new Set();
  if (visited.has(value)) return "";
  visited.add(value);

  if (Array.isArray(value)) {
    for (let i = 0; i < value.length; i++) {
      const message = findNestedErrorMessage(value[i], depth + 1, fallback, visited);
      if (message) return message;
    }
    return "";
  }

  if (value.error !== undefined) {
    if (typeof value.error === "string") {
      return normalizeErrorText(value.error, depth + 1, fallback);
    }
    if (
      value.error &&
      typeof value.error.message === "string" &&
      value.error.message.trim()
    ) {
      return normalizeErrorText(value.error.message, depth + 1, fallback);
    }
    const nestedError = findNestedErrorMessage(value.error, depth + 1, fallback, visited);
    if (nestedError) return nestedError;
  }

  if (
    typeof value.message === "string" &&
    value.message.trim() &&
    (value.error !== undefined ||
      value.code !== undefined ||
      value.status !== undefined ||
      value.type !== undefined)
  ) {
    return normalizeErrorText(value.message, depth + 1, fallback);
  }

  const keys = Object.keys(value);
  for (let i = 0; i < keys.length; i++) {
    if (keys[i] === "error") continue;
    const message = findNestedErrorMessage(value[keys[i]], depth + 1, fallback, visited);
    if (message) return message;
  }
  return "";
}
