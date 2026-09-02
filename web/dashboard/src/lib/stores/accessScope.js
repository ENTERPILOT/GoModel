// Pure access-scope helpers shared by the access store, page logic, and the
// node:test suite. Kept free of Svelte/store imports so plain modules can use
// them.
//
// A dashboard credential is either global (master key, or a managed key
// without a bound user path) or scoped to the user-path subtree its key is
// bound to. Scoped admins only see and manage that subtree, and the pages
// that expose gateway-wide configuration are hidden from them.

// GLOBAL_ONLY_PAGES lists the route ids whose content is gateway-wide
// configuration; a scoped admin has no access to their endpoints.
export const GLOBAL_ONLY_PAGES = Object.freeze([
  "providers-config",
  "models",
  "workflows",
  "guardrails",
  "mcp-servers",
]);

// normalizeScopePath canonicalizes a user path (leading slash, trimmed
// segments, empty segments dropped). Anything unusable normalizes to "".
export function normalizeScopePath(value) {
  const trimmed = String(value || "").trim();
  if (!trimmed) {
    return "";
  }
  const raw = trimmed.startsWith("/") ? trimmed : "/" + trimmed;
  const canonical = [];
  for (const part of raw.split("/")) {
    const segment = part.trim();
    if (!segment) {
      continue;
    }
    if (segment === "." || segment === ".." || segment.includes(":")) {
      return "";
    }
    canonical.push(segment);
  }
  return canonical.length ? "/" + canonical.join("/") : "/";
}

// parseAccessScope reads the GET /admin/access payload. Anything but a
// well-formed scoped payload is treated as global so older backends (no such
// endpoint) keep the dashboard fully usable.
export function parseAccessScope(data) {
  if (!data || typeof data !== "object" || Array.isArray(data)) {
    return { scope: "global", userPath: "" };
  }
  if (String(data.scope || "") !== "user_path") {
    return { scope: "global", userPath: "" };
  }
  const userPath = normalizeScopePath(data.user_path);
  if (!userPath || userPath === "/") {
    return { scope: "global", userPath: "" };
  }
  return { scope: "user_path", userPath };
}

// scopeAllows mirrors the backend rule: a global root ("" or "/") admits
// every path; otherwise the path must be the root itself or a descendant
// on a segment boundary ("/acme" never admits "/acme-old").
export function scopeAllows(root, path) {
  const base = normalizeScopePath(root);
  if (!base || base === "/") {
    return true;
  }
  const candidate = normalizeScopePath(path);
  if (!candidate) {
    return false;
  }
  return candidate === base || candidate.startsWith(base + "/");
}

// pageVisibleForScope reports whether a route id is usable under the scope.
export function pageVisibleForScope(page, scoped) {
  return !scoped || !GLOBAL_ONLY_PAGES.includes(page);
}
