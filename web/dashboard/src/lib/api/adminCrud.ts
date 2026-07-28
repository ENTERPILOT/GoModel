// Shared request lifecycle for the admin CRUD stores (budgets, rate limits,
// MCP servers, guardrails, auth keys, provider credentials, workflows, ...).
// Every store repeats the same guard ladder around getJSON/sendJSON; these
// helpers encode it once, in the one correct order:
//
//   1. stale   — the response belongs to a previous API key: the caller must
//                return without touching ANY state (CONVENTIONS.md), so it is
//                checked before everything else, including 503.
//   2. unavailable — the feature is disabled on the gateway (503, sometimes
//                404): clear the list and flip the page's "available" flag.
//   3. error   — 401 stays silent (the global auth dialog owns it); other
//                failures produce a human-readable message.
//
// The helpers never touch store state themselves — each store applies the
// returned outcome to its own $state fields, so public store APIs stay put.

import {
  getJSON,
  isAbortError,
  sendJSON,
  type ApiResult,
  type ApiRequestOptions,
} from "./client.ts";
import { errorPayloadMessage } from "./errors.ts";

export type AdminOutcomeStatus = "ok" | "stale" | "unavailable" | "error";

export interface AdminListOutcome<T = unknown> {
  status: AdminOutcomeStatus;
  /** Normalized rows; empty on unavailable/error, untouched rows on ok. */
  items: T[];
  /** Message for the page's inline error slot; empty unless status === "error". */
  error: string;
  result: ApiResult | null;
}

export interface AdminMutationOutcome {
  status: AdminOutcomeStatus;
  /** Message for the form's error slot; empty unless status === "error". */
  error: string;
  result: ApiResult | null;
}

interface LoadAdminListOptions<T> {
  /** Console/error label ("budgets"). */
  label: string;
  /** Inline error fallback; defaults to "Unable to load {label}.". */
  errorFallback?: string;
  /** Statuses meaning "feature disabled" (default [503]). */
  unavailableStatuses?: number[];
  /** Maps the raw payload to rows; defaults to as-is array (else []). */
  normalize?: (data: unknown) => T[];
  /** Extra fetch options (signal, ...). */
  options?: ApiRequestOptions;
}

// loadAdminList GETs an admin collection and reduces the response to an
// outcome the store can apply in a few lines.
export async function loadAdminList<T = unknown>(
  path: string,
  {
    label,
    errorFallback = `Unable to load ${label}.`,
    unavailableStatuses = [503],
    normalize = (data: unknown) => (Array.isArray(data) ? (data as T[]) : []),
    options,
  }: LoadAdminListOptions<T>,
): Promise<AdminListOutcome<T>> {
  let result: ApiResult;
  try {
    result = await getJSON(path, { ...(options || {}), label });
  } catch (e) {
    // Aborted requests stay silent: the caller's signal.aborted guard is
    // what decides whether the outcome is applied at all.
    if (!isAbortError(e)) {
      console.error(`Failed to fetch ${label}:`, e);
    }
    return { status: "error", items: [], error: errorFallback, result: null };
  }
  if (result.stale) {
    return { status: "stale", items: [], error: "", result };
  }
  if (unavailableStatuses.includes(result.status)) {
    return { status: "unavailable", items: [], error: "", result };
  }
  if (!result.ok) {
    const error =
      result.status === 401
        ? ""
        : errorPayloadMessage(result.data, errorFallback);
    return { status: "error", items: [], error, result };
  }
  return { status: "ok", items: normalize(result.data), error: "", result };
}

interface SendAdminMutationOptions {
  /** Console/error label ("save budget"). */
  label: string;
  /** Form error fallback; defaults to "Unable to {label}.". */
  errorFallback?: string;
  /** Statuses meaning "feature disabled" (default [503]). */
  unavailableStatuses?: number[];
  /** Form error text used for the unavailable outcome. */
  unavailableMessage?: string;
  /** Extra fetch options. */
  options?: ApiRequestOptions;
}

// sendAdminMutation wraps sendJSON (PUT/POST/DELETE) with the same ladder.
// Flash messages, closing forms, and refetching stay in the store — only the
// guard boilerplate lives here.
export async function sendAdminMutation(
  path: string,
  method: string,
  body: unknown,
  {
    label,
    errorFallback = `Unable to ${label}.`,
    unavailableStatuses = [503],
    unavailableMessage = "This feature is unavailable on the gateway.",
    options,
  }: SendAdminMutationOptions,
): Promise<AdminMutationOutcome> {
  let result: ApiResult;
  try {
    result = await sendJSON(path, method, body, { ...(options || {}), label });
  } catch (e) {
    if (!isAbortError(e)) {
      console.error(`Failed to ${label}:`, e);
    }
    return { status: "error", error: errorFallback, result: null };
  }
  if (result.stale) {
    return { status: "stale", error: "", result };
  }
  if (unavailableStatuses.includes(result.status)) {
    return { status: "unavailable", error: unavailableMessage, result };
  }
  if (!result.ok) {
    const error =
      result.status === 401
        ? "Authentication required."
        : errorPayloadMessage(result.data, errorFallback);
    return { status: "error", error, result };
  }
  return { status: "ok", error: "", result };
}
