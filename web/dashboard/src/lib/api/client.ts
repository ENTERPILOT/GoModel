// Admin API client. Wraps fetch with base-path joining, auth headers, the
// timezone header, and stale-auth handling. Pages should use getJSON /
// sendJSON; apiFetch is the low-level escape hatch (SSE, blobs, ...).

import { gomodelPath } from "./paths.ts";
import { auth, normalizeApiKey } from "$lib/stores/auth.svelte.ts";
import { timezone } from "$lib/stores/timezone.svelte.ts";

// STALE marks a response that belongs to a previous API key; callers must
// leave their state untouched when they see it.
export const STALE = Symbol("stale-auth-response");

// The envelope every getJSON/sendJSON call resolves to. `stale === true`
// means the response was issued under an older API key: leave state alone.
export interface ApiResult<T = unknown> {
  ok: boolean;
  stale: boolean;
  status: number;
  data: T | null;
  res: Response;
}

// fetch() init plus the console label used when a request fails.
export interface ApiRequestOptions extends RequestInit {
  label?: string;
}

export function apiHeaders(): Record<string, string> {
  const h: Record<string, string> = { "Content-Type": "application/json" };
  const apiKey = normalizeApiKey(auth.apiKey);
  if (apiKey) {
    h.Authorization = "Bearer " + apiKey;
  }
  h["X-GoModel-Timezone"] = timezone.effectiveTimezone();
  return h;
}

// apiFetch performs a raw fetch with auth headers against an app path.
export function apiFetch(
  path: string,
  options: ApiRequestOptions = {},
): Promise<Response> {
  return fetch(gomodelPath(path), {
    ...options,
    headers: { ...apiHeaders(), ...(options.headers || {}) },
  });
}

async function request(
  path: string,
  options: ApiRequestOptions,
  { label = path, parse = true }: { label?: string; parse?: boolean } = {},
): Promise<ApiResult> {
  const generation = auth.generation;
  const res = await apiFetch(path, options);
  if (res.status === 401) {
    auth.handleUnauthorized(generation);
    return { ok: false, stale: generation < auth.generation, status: 401, data: null, res };
  }
  const stale = generation < auth.generation;
  if (!res.ok) {
    let data: unknown = null;
    try {
      data = await res.json();
    } catch {
      data = null;
    }
    // A valid key without dashboard access is as unusable as a wrong key:
    // reopen the auth dialog with a message explaining why.
    const code = (data as { error?: { code?: string } } | null)?.error?.code;
    if (res.status === 403 && code === "dashboard_access_denied") {
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
  let data: unknown = null;
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
export function getJSON(
  path: string,
  options: ApiRequestOptions = {},
): Promise<ApiResult> {
  return request(path, { ...options, method: options.method || "GET" }, {
    label: options.label || path,
  });
}

// sendJSON sends a JSON body (POST/PUT/DELETE) and parses a JSON response
// when one is present.
export function sendJSON(
  path: string,
  method: string,
  body?: unknown,
  options: ApiRequestOptions = {},
): Promise<ApiResult> {
  const init: ApiRequestOptions = { ...options, method };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
  }
  return request(path, init, { label: options.label || path });
}

// errorMessage extracts a human-readable message from an admin error payload.
export { errorMessage, errorPayloadMessage } from "./errors.ts";

export function isAbortError(error: unknown): boolean {
  const e = error as { name?: string; code?: number } | null | undefined;
  return Boolean(e) && (e?.name === "AbortError" || e?.code === 20);
}
