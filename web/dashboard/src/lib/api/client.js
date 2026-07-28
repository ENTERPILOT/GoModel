// Admin API client. Wraps fetch with base-path joining, auth headers, the
// timezone header, and stale-auth handling. Pages should use getJSON /
// sendJSON; apiFetch is the low-level escape hatch (SSE, blobs, ...).

import { gomodelPath } from "./paths.js";
import { auth, normalizeApiKey } from "$lib/stores/auth.svelte.js";
import { timezone } from "$lib/stores/timezone.svelte.js";

// STALE marks a response that belongs to a previous API key; callers must
// leave their state untouched when they see it.
export const STALE = Symbol("stale-auth-response");

export function apiHeaders() {
  const h = { "Content-Type": "application/json" };
  const apiKey = normalizeApiKey(auth.apiKey);
  if (apiKey) {
    h.Authorization = "Bearer " + apiKey;
  }
  h["X-GoModel-Timezone"] = timezone.effectiveTimezone();
  return h;
}

// apiFetch performs a raw fetch with auth headers against an app path.
export function apiFetch(path, options = {}) {
  return fetch(gomodelPath(path), {
    ...options,
    headers: { ...apiHeaders(), ...(options.headers || {}) },
  });
}

async function request(path, options, { label = path, parse = true } = {}) {
  const generation = auth.generation;
  const res = await apiFetch(path, options);
  if (res.status === 401) {
    auth.handleUnauthorized(generation);
    return { ok: false, stale: generation < auth.generation, status: 401, data: null, res };
  }
  const stale = generation < auth.generation;
  if (!res.ok) {
    let data = null;
    try {
      data = await res.json();
    } catch {
      data = null;
    }
    // A valid key without dashboard access is as unusable as a wrong key:
    // reopen the auth dialog with a message explaining why.
    if (res.status === 403 && data?.error?.code === "dashboard_access_denied") {
      auth.handleUnauthorized(
        generation,
        "This API key does not have dashboard access. Use the master key or a key with dashboard access.",
      );
      return { ok: false, stale, status: res.status, data, res };
    }
    if (!stale) {
      console.error(`Failed to fetch ${label}: ${res.status} ${res.statusText}`);
    }
    return { ok: false, stale, status: res.status, data, res };
  }
  let data = null;
  if (parse) {
    try {
      data = await res.json();
    } catch {
      data = null;
    }
  }
  return { ok: true, stale, status: res.status, data, res };
}

// getJSON fetches JSON from an admin endpoint.
// Returns { ok, stale, status, data, res }. Network errors propagate.
export function getJSON(path, options = {}) {
  return request(path, { ...options, method: options.method || "GET" }, {
    label: options.label || path,
  });
}

// sendJSON sends a JSON body (POST/PUT/DELETE) and parses a JSON response
// when one is present.
export function sendJSON(path, method, body, options = {}) {
  const init = { ...options, method };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
  }
  return request(path, init, { label: options.label || path });
}

// errorMessage extracts a human-readable message from an admin error payload.
export { errorMessage, errorPayloadMessage } from "./errors.js";

export function isAbortError(error) {
  return Boolean(error) && (error.name === "AbortError" || error.code === 20);
}
