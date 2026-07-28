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
//
// Outcomes: loadAdminList resolves to { status, items, error, result } and
// sendAdminMutation to { status, error, result }, where status is one of
// "ok" | "stale" | "unavailable" | "error", `error` is the message for the
// page/form error slot (empty unless status === "error", except the
// unavailable mutation message), and `result` is the raw getJSON/sendJSON
// envelope (null when the request itself threw).

import { getJSON, isAbortError, sendJSON } from "./client.js";
import { errorPayloadMessage } from "./errors.js";

// loadAdminList GETs an admin collection and reduces the response to an
// outcome the store can apply in a few lines. Options:
//   label               — console/error label ("budgets")
//   errorFallback       — inline error fallback (default "Unable to load {label}.")
//   unavailableStatuses — statuses meaning "feature disabled" (default [503])
//   normalize           — maps the raw payload to rows (default as-is array, else [])
//   options             — extra fetch options (signal, ...)
export async function loadAdminList(
  path,
  {
    label,
    errorFallback = `Unable to load ${label}.`,
    unavailableStatuses = [503],
    normalize = (data) => (Array.isArray(data) ? data : []),
    options,
  },
) {
  let result;
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

// sendAdminMutation wraps sendJSON (PUT/POST/DELETE) with the same ladder.
// Flash messages, closing forms, and refetching stay in the store — only the
// guard boilerplate lives here. Options:
//   label               — console/error label ("save budget")
//   errorFallback       — form error fallback (default "Unable to {label}.")
//   unavailableStatuses — statuses meaning "feature disabled" (default [503])
//   unavailableMessage  — form error text for the unavailable outcome
//   options             — extra fetch options
export async function sendAdminMutation(
  path,
  method,
  body,
  {
    label,
    errorFallback = `Unable to ${label}.`,
    unavailableStatuses = [503],
    unavailableMessage = "This feature is unavailable on the gateway.",
    options,
  },
) {
  let result;
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
