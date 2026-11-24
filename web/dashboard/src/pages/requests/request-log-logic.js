// Pure request-log display/query logic (plus the tiny workflowFailoverTarget
// helper the metadata row needs).
// Keep this file free of Svelte-runtime imports so node:test can import it
// directly.

import { formatJSON, formatNumber } from "../../lib/utils/format.js";
import * as m from "../../lib/paraglide/messages.js";
import { formatNumber as formatLocaleNumber } from "../../lib/i18n/locale.js";
import { phaseLabel } from "../../lib/utils/pluginPhases.js";
import {
  requestEntryTypeVisible,
  requestLogExcludeOperationsQuery,
  requestTypeForPath,
} from "./request-operations.js";
import {
  workflowEntryGuardrails,
  workflowGuardrailActionLabel,
} from "../workflows/workflowChartLogic.js";
import {
  findNestedErrorMessage,
  normalizeErrorText,
  tryParseJSON,
} from "./error-text.js";

// Re-exported: the panes are built here, so consumers get their formatter from
// the same module.
export { formatJSON };

function requestLogErrorMessageFromField(value) {
  if (value == null) return "";
  if (typeof value === "string") return normalizeErrorText(value, 0);
  return findNestedErrorMessage(value, 0);
}

function requestEntryStatusCodeValue(entry, data) {
  const candidates = [entry.status_code, entry.status, data.status_code, data.status];

  for (let i = 0; i < candidates.length; i++) {
    const parsed = Number(candidates[i]);
    if (Number.isFinite(parsed)) return parsed;
  }

  return null;
}

function hasTopLevelRequestLogErrorShape(value) {
  if (value == null) return false;

  let candidate = value;
  if (typeof candidate === "string") {
    const parsed = tryParseJSON(candidate.trim());
    if (parsed == null) return false;
    candidate = parsed;
  }

  if (Array.isArray(candidate) || typeof candidate !== "object") return false;
  if (candidate.error !== undefined) return true;
  if (typeof candidate.message === "string" && candidate.message.trim())
    return true;

  const topLevelErrorFields = ["detail", "error_message", "error_msg", "title"];
  for (let i = 0; i < topLevelErrorFields.length; i++) {
    const field = topLevelErrorFields[i];
    if (typeof candidate[field] === "string" && candidate[field].trim())
      return true;
  }

  return false;
}

function shouldInspectRequestLogResponseBody(entry, data) {
  const statusCode = requestEntryStatusCodeValue(entry, data);
  if (statusCode !== null && statusCode >= 400) return true;
  return hasTopLevelRequestLogErrorShape(data.response_body);
}

export function requestEntryErrorMessage(entry) {
  const data = entry && entry.data ? entry.data : null;
  if (!data) return "";
  const fieldMessage = requestLogErrorMessageFromField(data.error_message);
  if (fieldMessage) return fieldMessage;
  if (!shouldInspectRequestLogResponseBody(entry, data)) return "";
  return findNestedErrorMessage(data.response_body, 0);
}

// --- Retention note ---------------------------------------------------------
// `raw` is the raw LOGGING_RETENTION_DAYS runtime-config value.

function requestLogRetentionDays(raw) {
  if (raw === undefined || raw === null || String(raw).trim() === "")
    return null;

  const days = Number(raw);
  if (!Number.isInteger(days) || days < 0) return null;
  return days;
}

export function requestLogRetentionText(raw) {
  const days = requestLogRetentionDays(raw);
  if (days === null) return "";
  return (
    requestLogRetentionPrefix(raw) + requestLogRetentionHighlight(raw) + "."
  );
}

export function requestLogRetentionPrefix(raw) {
  const days = requestLogRetentionDays(raw);
  if (days === null) return "";
  return days === 0
    ? m.requests_retention_prefix_indefinite()
    : m.requests_retention_prefix_days();
}

export function requestLogRetentionHighlight(raw) {
  const days = requestLogRetentionDays(raw);
  if (days === null) return "";
  if (days === 0) return m.requests_retention_indefinitely();
  return m.requests_retention_days({ count: days });
}

// --- Query building ---------------------------------------------------------

// REQUEST_LOG_SEARCH_FIELDS lists the search-box field selector options and the API
// parameter each one sends. "All fields" (the default) is the free-text
// search across every column; the others target one column, so the database
// can answer from that column's index instead of sweeping the whole table.
export const REQUEST_LOG_SEARCH_FIELDS = [
  {
    key: "",
    param: "search",
    label: () => m.requests_search_field_all(),
    placeholder: () => m.requests_search_placeholder(),
  },
  {
    key: "user_path",
    param: "user_path_prefix",
    label: () => m.requests_search_field_user_path(),
    placeholder: () => m.requests_search_placeholder_user_path(),
  },
  {
    key: "model",
    param: "model_prefix",
    label: () => m.requests_search_field_model(),
    placeholder: () => m.requests_search_placeholder_model(),
  },
  {
    key: "request_id",
    param: "request_id",
    label: () => m.requests_search_field_request_id(),
    placeholder: () => m.requests_search_placeholder_request_id(),
  },
  {
    key: "session_id",
    param: "session_id",
    label: () => m.requests_search_field_session_id(),
    placeholder: () => m.requests_search_placeholder_session_id(),
  },
  {
    key: "provider",
    param: "provider",
    label: () => m.requests_search_field_provider(),
    placeholder: () => m.requests_search_placeholder_provider(),
  },
  {
    key: "error_type",
    param: "error_type",
    label: () => m.requests_search_field_error_type(),
    placeholder: () => m.requests_search_placeholder_error_type(),
  },
];

// requestLogSearchField returns the selector option for key, falling back to
// "All fields" for an unknown key.
export function requestLogSearchField(key) {
  return REQUEST_LOG_SEARCH_FIELDS.find((field) => field.key === key) || REQUEST_LOG_SEARCH_FIELDS[0];
}

// buildRequestLogQuery renders the GET /admin/request-logs query string: the
// shared date window first, then paging, then only the consolidated request log
// filters that are set. The search value goes to the parameter of the
// selected search field.
export function buildRequestLogQuery({
  dateQuery,
  limit,
  offset,
  search,
  searchField,
  method,
  statusCode,
  stream,
  hiddenTypes,
}) {
  let qs = dateQuery;
  qs += "&limit=" + limit + "&offset=" + offset;
  const term = String(search || "").trim();
  if (term) {
    qs += "&" + requestLogSearchField(searchField).param + "=" + encodeURIComponent(term);
  }
  if (method) qs += "&method=" + encodeURIComponent(method);
  if (statusCode) qs += "&status_code=" + encodeURIComponent(statusCode);
  if (stream) qs += "&stream=" + encodeURIComponent(stream);
  return qs + excludeOperationsParam(hiddenTypes);
}

function excludeOperationsParam(hiddenTypes) {
  const operations = requestLogExcludeOperationsQuery(hiddenTypes);
  return operations ? "&exclude_operation=" + encodeURIComponent(operations) : "";
}

// buildRequestLogSessionQuery renders the thread-children fetch for one session:
// GET /admin/request-logs?session_id=…  Deliberately no active list filters, so
// an expanded thread shows the whole session even when requests fall outside
// the date, user-path, or other filters used to find the thread. Hidden
// request types are the exception: hiding MCP hides it inside threads too.
export function buildRequestLogSessionQuery({ sessionId, limit, hiddenTypes }) {
  return (
    "session_id=" +
    encodeURIComponent(sessionId) +
    "&limit=" +
    (limit || 100) +
    "&offset=0" +
    excludeOperationsParam(hiddenTypes)
  );
}

// --- Session grouping -------------------------------------------------------
// Grouped mode reuses the flat list shape: entries hold thread HEADS (each a
// normal request log entry plus `session_count`), total counts threads.

export function requestSessionId(entry) {
  return String((entry && entry.session_id) || "").trim();
}

export function requestSessionCount(entry) {
  const count = Number(entry && entry.session_count);
  return Number.isFinite(count) && count > 1 ? count : 1;
}

// requestIsThreadHead reports whether a row gets the expander: it belongs to a
// session with more entries than itself.
export function requestIsThreadHead(entry) {
  return !!requestSessionId(entry) && requestSessionCount(entry) > 1;
}

// requestLogFromSessions maps the GET /admin/request-logs/sessions payload into the
// shared list shape: one head entry per thread, newest-activity first.
export function requestLogFromSessions(payload) {
  const sessions = Array.isArray(payload && payload.sessions)
    ? payload.sessions
    : [];
  return {
    entries: sessions
      .filter((session) => session && session.latest)
      .map((session) => ({
        ...session.latest,
        session_count: Math.max(1, Number(session.request_count) || 1),
      })),
    total: Number((payload && payload.total) || 0),
    limit: Number((payload && payload.limit) || 25),
    offset: Number((payload && payload.offset) || 0),
    ...(payload && payload.as_of ? { as_of: payload.as_of } : {}),
  };
}

// mergeRequestThreadChildren folds a fetched session_id page into a thread's
// children slot: head rows are dropped (the unfolded list holds only the
// older requests), and still-live entries from the previous partial list that
// the server does not return yet (displaced from the head list before being
// persisted) are preserved on top. preservedCount lets the caller keep the
// thread total honest.
export function mergeRequestThreadChildren(previousList, fetchedEntries, heads) {
  const headKeys = new Set(
    (Array.isArray(heads) ? heads : []).flatMap((head) =>
      requestEntryIdentityKeys(head),
    ),
  );
  const fetched = (Array.isArray(fetchedEntries) ? fetchedEntries : []).filter(
    (entry) => !requestEntryIdentityKeys(entry).some((key) => headKeys.has(key)),
  );
  const knownKeys = new Set([
    ...headKeys,
    ...fetched.flatMap((entry) => requestEntryIdentityKeys(entry)),
  ]);
  const preserved = (
    previousList && Array.isArray(previousList.entries)
      ? previousList.entries
      : []
  ).filter(
    (entry) =>
      entry &&
      entry._live &&
      !requestEntryIdentityKeys(entry).some((key) => knownKeys.has(key)),
  );
  return {
    entries: [...preserved, ...fetched],
    preservedCount: preserved.length,
  };
}

export function toggleExpandedThread(expanded, sessionId) {
  const current = expanded || {};
  if (!sessionId) return current;
  if (current[sessionId]) {
    const next = { ...current };
    delete next[sessionId];
    return next;
  }
  return { ...current, [sessionId]: true };
}

// pruneThreadMap keeps only keys whose session still appears among the current
// head entries, so open threads survive refetches of the same page but stale
// state is dropped once a thread leaves the page.
export function pruneThreadMap(map, entries) {
  const current = map || {};
  const active = new Set(
    (Array.isArray(entries) ? entries : [])
      .map((entry) => requestSessionId(entry))
      .filter(Boolean),
  );
  const next = {};
  let changed = false;
  Object.keys(current).forEach((key) => {
    if (active.has(key)) {
      next[key] = current[key];
      return;
    }
    changed = true;
  });
  return changed ? next : current;
}

// --- Live-entry merge -------------------------------------------------------
// `filters` carries the current consolidated filters plus the custom date
// range: { search, method, statusCode, stream, customStartDate, customEndDate }.

export function requestEntryKey(entry) {
  return String((entry && entry.id) || "").trim();
}

function requestEntryIdentityKeys(entry) {
  if (!entry) return [];
  const keys = [];
  const id = String(entry.id || "").trim();
  const requestID = String(entry.request_id || "").trim();
  if (id) keys.push("id:" + id);
  if (requestID) keys.push("request:" + requestID);
  return keys;
}

function requestEntryLivePreviewPending(entry) {
  return !!(entry && entry._live && entry._live_pending && !entry._request_log_flushed);
}

function requestLogLiveDateRangeAllowsNow(filters) {
  const customStartDate = filters && filters.customStartDate;
  const customEndDate = filters && filters.customEndDate;
  if (!customStartDate && !customEndDate) return true;
  const now = new Date();
  if (customStartDate) {
    const start = new Date(customStartDate);
    start.setHours(0, 0, 0, 0);
    if (Number.isFinite(start.getTime()) && now < start) return false;
  }
  if (customEndDate) {
    const end = new Date(customEndDate);
    end.setHours(23, 59, 59, 999);
    if (Number.isFinite(end.getTime()) && now > end) return false;
  }
  return true;
}

export function requestLogAllowsLiveEntries(payload, filters) {
  return (
    payload &&
    Number(payload.offset || 0) === 0 &&
    !(filters && filters.search) &&
    !(filters && filters.method) &&
    !(filters && filters.statusCode) &&
    !(filters && filters.stream) &&
    requestLogLiveDateRangeAllowsNow(filters)
  );
}

// requestLogWithLiveEntries prepends still-pending live preview rows onto a
// freshly fetched page so an in-flight request does not disappear on refresh.
// Persisted rows replace matching previews (matched by id or request_id).
export function requestLogWithLiveEntries(payload, currentEntries, filters) {
  const next =
    payload && typeof payload === "object"
      ? { ...payload }
      : { entries: [], total: 0, limit: 25, offset: 0 };
  const entries = Array.isArray(next.entries) ? next.entries : [];
  next.entries = entries;
  if (!requestLogAllowsLiveEntries(next, filters)) return next;

  const liveEntries = (Array.isArray(currentEntries) ? currentEntries : []).filter(
    (entry) =>
      requestEntryLivePreviewPending(entry) &&
      requestEntryTypeVisible(entry, filters && filters.hiddenTypes),
  );
  if (liveEntries.length === 0) return next;

  const persistedKeys = new Set(
    entries.flatMap((entry) => requestEntryIdentityKeys(entry)),
  );
  const prepend = [];
  liveEntries.forEach((entry) => {
    const keys = requestEntryIdentityKeys(entry);
    if (keys.length === 0) return;
    if (keys.some((key) => persistedKeys.has(key))) return;
    keys.forEach((key) => persistedKeys.add(key));
    prepend.push(entry);
  });
  if (prepend.length === 0) return next;

  next.entries = [...prepend, ...entries].slice(0, next.limit || 25);
  next.total = Number(next.total || 0) + prepend.length;
  return next;
}

// groupedRequestLogWithLiveEntries is requestLogWithLiveEntries for grouped mode:
// a still-pending live preview folds into its session's fetched head instead
// of duplicating the thread; previews without an on-screen thread prepend as
// singleton heads.
export function groupedRequestLogWithLiveEntries(payload, currentEntries, filters) {
  const next =
    payload && typeof payload === "object"
      ? { ...payload }
      : { entries: [], total: 0, limit: 25, offset: 0 };
  const entries = Array.isArray(next.entries) ? next.entries : [];
  next.entries = entries;
  if (!requestLogAllowsLiveEntries(next, filters)) return next;

  const liveEntries = (Array.isArray(currentEntries) ? currentEntries : []).filter(
    (entry) =>
      requestEntryLivePreviewPending(entry) &&
      requestEntryTypeVisible(entry, filters && filters.hiddenTypes),
  );
  if (liveEntries.length === 0) return next;

  const persistedKeys = new Set(
    entries.flatMap((entry) => requestEntryIdentityKeys(entry)),
  );
  const headBySession = new Map();
  entries.forEach((entry, index) => {
    const sid = requestSessionId(entry);
    if (sid && !headBySession.has(sid)) headBySession.set(sid, index);
  });

  const prepend = [];
  let merged = entries;
  liveEntries.forEach((entry) => {
    const keys = requestEntryIdentityKeys(entry);
    if (keys.length === 0) return;
    // The persisted page already carries this request (as a head): keep it.
    if (keys.some((key) => persistedKeys.has(key))) return;
    const sid = requestSessionId(entry);
    if (sid && headBySession.has(sid)) {
      // Fold into the fetched thread: the pending preview is newer than the
      // persisted head, so it becomes the head. Counts already accumulated by
      // the live grouping path are authoritative; otherwise this distinct
      // request increments the fetched count.
      const index = headBySession.get(sid);
      if (merged === entries) merged = [...entries];
      const previousCount = requestSessionCount(merged[index]);
      const incomingValue = Number(entry && entry.session_count);
      const incomingCount =
        Number.isFinite(incomingValue) && incomingValue > 1
          ? incomingValue
          : null;
      merged[index] = {
        ...entry,
        session_count:
          incomingCount === null
            ? previousCount + 1
            : Math.max(previousCount, incomingCount),
      };
      keys.forEach((key) => persistedKeys.add(key));
      return;
    }
    keys.forEach((key) => persistedKeys.add(key));
    prepend.push(entry);
  });

  next.entries = [...prepend, ...merged].slice(0, next.limit || 25);
  next.total = Number(next.total || 0) + prepend.length;
  return next;
}

// --- Expanded-entry map -----------------------------------------------------

export function toggleExpandedEntry(expanded, entry) {
  const key = requestEntryKey(entry);
  const current = expanded || {};
  if (!key) return current;
  if (current[key]) return {};
  // Request log details behave like an accordion: opening one row folds any row that
  // was already open, including entries nested under a grouped session.
  return { [key]: true };
}

export function pruneExpandedEntries(expanded, entries) {
  const current = expanded || {};
  const keys = new Set(
    (Array.isArray(entries) ? entries : [])
      .map((entry) => requestEntryKey(entry))
      .filter(Boolean),
  );
  const next = {};
  let changed = false;

  Object.keys(current).forEach((key) => {
    if (keys.has(key)) {
      next[key] = true;
      return;
    }
    changed = true;
  });

  return changed ? next : current;
}

// --- Row display ------------------------------------------------------------

export function formatDurationNs(ns) {
  if (ns == null) return "-";
  const v = Number(ns);
  if (!Number.isFinite(v)) return "-";
  if (v <= 0) return "pending";
  if (v < 1000000) return Math.round(v / 1000) + " µs";
  if (v < 1000000000) return (v / 1000000).toFixed(2) + " ms";
  return (v / 1000000000).toFixed(2) + " s";
}

// Request types whose output tokens are generated text, so output tokens over
// duration reads as generation speed. Embeddings, images, audio, batches and
// realtime sessions have no comparable per-request rate.
const TOKEN_RATE_TYPES = new Set(["chat", "responses"]);

// tokenRateType returns the request type the rate gate checks. A passthrough
// path (/p/{provider}/{endpoint}) is typed by its provider endpoint, so a
// passthrough image generation is not shown as text speed.
function tokenRateType(path) {
  const type = requestTypeForPath(path);
  if (type !== "passthrough") return type;
  const endpoint = String(path).trim().split("?")[0].split("/").slice(3).join("/");
  return requestTypeForPath("/v1/" + endpoint.replace(/^v1\//, ""));
}

// requestOutputTokensPerSecond is output tokens over the entry's total duration
// (time to first token included), or null when either is missing or zero, the
// request does not generate text, or the reply came from the response cache
// (its duration is the cache lookup, not generation). Provider attempts carry
// no usage, so the rate is only known per entry.
export function requestOutputTokensPerSecond(entry) {
  if (!entry || !TOKEN_RATE_TYPES.has(tokenRateType(entry.path))) return null;
  const cacheType = String(entry.cache_type || "").trim().toLowerCase();
  if (cacheType === "exact" || cacheType === "semantic") return null;
  const usage = requestUsage(entry);
  const outputTokens = Number(usage && usage.output_tokens);
  const durationNs = Number(entry.duration_ns);
  if (!Number.isFinite(outputTokens) || outputTokens <= 0) return null;
  if (!Number.isFinite(durationNs) || durationNs <= 0) return null;
  return outputTokens / (durationNs / 1e9);
}

// formatTokensPerSecond renders a rate as "42.1 tok/s" in the active locale
// (whole numbers from 100 up, "<0.1" for slower positive rates), or "" when
// there is no rate.
export function formatTokensPerSecond(rate) {
  const v = Number(rate);
  if (rate == null || !Number.isFinite(v) || v <= 0) return "";
  const oneDecimal = { minimumFractionDigits: 1, maximumFractionDigits: 1 };
  const tenths = Math.round(v * 10) / 10;
  let text;
  if (tenths >= 100) text = formatLocaleNumber(Math.round(v), { maximumFractionDigits: 0 });
  else if (tenths < 0.1) text = "<" + formatLocaleNumber(0.1, oneDecimal);
  else text = formatLocaleNumber(tenths, oneDecimal);
  return m.requests_tokens_per_second({ rate: text });
}

export function statusCodeClass(statusCode) {
  if (statusCode === null || statusCode === undefined || statusCode === "")
    return "status-unknown";
  const parsedStatus = Number(statusCode);
  if (!Number.isFinite(parsedStatus)) return "status-unknown";
  if (parsedStatus >= 500) return "status-error";
  if (parsedStatus >= 400) return "status-warning";
  if (parsedStatus >= 300) return "status-neutral";
  return "status-success";
}

export function requestEntryLiveInProgress(entry) {
  if (!entry || !entry._live || !entry._live_pending) return false;
  const liveState = String(entry._live_state || "").trim();
  if (
    liveState === "request_log.completed" ||
    liveState === "request_log.flushed" ||
    liveState === "request_log.detail"
  ) {
    return false;
  }
  // A partial response body means the stream is still running, regardless of
  // the other completion signals (streamed entries carry status 200 from the
  // moment headers were committed).
  if (entry._response_partial) return true;
  if (
    entry.status_code !== null &&
    entry.status_code !== undefined &&
    entry.status_code !== ""
  )
    return false;
  if (Number(entry.duration_ns || 0) > 0) return false;
  if (entry.error_type || entry.error_message) return false;

  const data = entry.data || {};
  return !(data.response_headers || data.response_body || data.error_message);
}

// workflowFailoverTarget surfaces the runtime failover target model recorded
// on the entry.
export function workflowFailoverTarget(entry) {
  const raw = entry && entry.data && entry.data.failover;
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const targetModel =
    String(raw.target_model || raw.targetModel || "").trim() || null;
  return targetModel;
}

// --- Provider attempts ------------------------------------------------------

function requestAttempts(entry) {
  const attempts =
    entry && entry.data && Array.isArray(entry.data.attempts)
      ? entry.data.attempts
      : [];
  return attempts
    .map((attempt, index) => ({
      ...attempt,
      seq: Number((attempt && attempt.seq) || index + 1),
    }))
    .sort((a, b) => a.seq - b.seq);
}

// requestUsesPerAttemptResponses reports whether responses should be split into
// one tab per attempt — when a failover/retry happened (more than one attempt)
// or any attempt failed — instead of a single combined Response tab.
function requestUsesPerAttemptResponses(entry) {
  const attempts = requestAttempts(entry);
  return (
    attempts.length > 1 || attempts.some((attempt) => !(attempt && attempt.success))
  );
}

function requestAttemptStatus(attempt) {
  if (!attempt) return "-";
  const status = attempt.status_code || attempt.status;
  if (status) return String(status);
  return attempt.success ? "ok" : "error";
}

function requestAttemptKind(attempt) {
  const kind = String((attempt && attempt.kind) || "").trim();
  return kind || "attempt";
}

function requestAttemptProvider(attempt) {
  if (!attempt) return "-";
  const name = String(attempt.provider_name || "").trim();
  const type = String(attempt.provider_type || attempt.provider || "").trim();
  if (name && type && name !== type) return name + " (" + type + ")";
  return name || type || "-";
}

function requestAttemptModel(attempt) {
  return String((attempt && attempt.model) || "").trim() || "-";
}

// requestAttemptTrack returns the attempt list when it is worth surfacing on the
// collapsed summary row: a retry/failover happened (more than one attempt) or
// any single attempt failed. A lone successful attempt is the common case and
// needs no indicator.
export function requestAttemptTrack(entry) {
  const attempts = requestAttempts(entry);
  if (attempts.length > 1) return attempts;
  if (attempts.some((attempt) => !(attempt && attempt.success))) return attempts;
  return [];
}

export function requestAttemptTrackCount(entry) {
  return requestAttempts(entry).length + "×";
}

export function requestAttemptTrackTitle(entry) {
  const attempts = requestAttempts(entry);
  const failed = attempts.filter((attempt) => !(attempt && attempt.success)).length;
  return failed > 0
    ? m.requests_provider_attempts_failed({ count: attempts.length, failed })
    : m.requests_provider_attempts({ count: attempts.length });
}

export function requestAttemptSegmentTitle(attempt) {
  if (!attempt) return "";
  const parts = ["#" + Number(attempt.seq || 0)];
  const kind = requestAttemptKind(attempt);
  if (kind && kind !== "attempt") parts.push(kind);
  parts.push(requestAttemptStatus(attempt));
  const provider = requestAttemptProvider(attempt);
  if (provider && provider !== "-") parts.push(provider);
  const model = requestAttemptModel(attempt);
  if (model && model !== "-") parts.push(model);
  parts.push(
    attempt.success ? m.requests_attempt_succeeded() : m.requests_attempt_failed_status(),
  );
  return parts.join(" · ");
}

// requestAttemptBody is the captured raw upstream response body only: the final
// response for the successful attempt, or the provider's raw error body for a
// failed one. The normalized error message is surfaced separately.
function requestAttemptBody(entry, attempt) {
  if (!attempt) return null;
  if (attempt.success) {
    const data = entry && entry.data ? entry.data : null;
    return data && data.response_body != null ? data.response_body : null;
  }
  return attempt.response_body != null && attempt.response_body !== ""
    ? attempt.response_body
    : null;
}

function requestAttemptErrorMessage(attempt) {
  if (!attempt || attempt.success) return "";
  const message = String(attempt.error_message || "").trim();
  const code = String(attempt.error_code || "").trim();
  const type = String(attempt.error_type || "").trim();
  if (message && code) return code + ": " + message;
  return message || code || type || m.requests_attempt_failed();
}

function requestAttemptHeaders(entry, attempt) {
  if (!attempt) return null;
  if (attempt.success) {
    const data = entry && entry.data ? entry.data : null;
    return data ? data.response_headers : null;
  }
  return attempt.response_headers || null;
}

function requestAttemptStatusCode(attempt) {
  const code = Number(attempt && attempt.status_code);
  return Number.isFinite(code) && code > 0 ? code : null;
}

/**
 * Builds one per-attempt response pane: captured body/headers, the normalized
 * error message, and the model strip naming which model the request named
 * versus which model this attempt actually tried.
 */
function requestAttemptResponsePane(entry, attempt) {
  const success = !!(attempt && attempt.success);
  const data = entry && entry.data ? entry.data : null;
  const body = requestAttemptBody(entry, attempt);
  const headers = requestAttemptHeaders(entry, attempt);
  const errorMessage = requestAttemptErrorMessage(attempt);
  const hasBody = body != null && body !== "";
  const kind = requestAttemptKind(attempt);
  // With only one response tab the seq/type/status chips are just noise (it's
  // the whole response); show them only to tell apart multiple attempt tabs.
  const single = requestAttempts(entry).length <= 1;
  // The tried model drives the in-pane model strip. Hidden on the single tab
  // (the row's model column already shows it) and for entries that predate
  // per-attempt model capture ("-" placeholder).
  const triedModel = single ? "" : requestAttemptModel(attempt);
  const tried = triedModel === "-" ? "" : triedModel;
  const source = String(
    (entry && entry.requested_model) ||
      (entry && entry.model) ||
      (data && data.request_body && data.request_body.model) ||
      "",
  ).trim();

  return {
    title: m.requests_response_title(),
    direction: "response",
    seq: single ? 0 : Number((attempt && attempt.seq) || 0),
    kind: single ? "" : kind === "attempt" ? "" : kind,
    // In-pane strip under the tab header: the model the request named and
    // the concrete model this attempt actually tried, so a failed step reads
    // "requested → tried" without hovering anything. The source is only
    // called "virtual" when an alias actually resolved — failover rules also
    // apply to plain concrete models, so a bare requested name must not
    // mislabel itself as a virtual model. Requires both values: a strip that
    // named only the tried model would read as if the request model were
    // unknown by design, which it is not.
    modelStrip:
      tried && !single && source
        ? {
            label:
              entry && entry.alias_used
                ? m.requests_model_strip_virtual()
                : m.requests_model_strip_requested(),
            source,
            tried,
          }
        : null,
    statusCode: single ? null : requestAttemptStatusCode(attempt),
    layout: "split",
    entry,
    copyHeaders: headers,
    copyBody: body,
    showErrorMessage: !!errorMessage,
    errorMessage,
    showHeaders: !!headers,
    headers,
    showBody: hasBody,
    body,
    showEmpty: !errorMessage && !hasBody && !headers,
    emptyMessage: m.requests_attempt_not_captured(),
    showTooLarge: !!(success && data && data.response_body_too_big_to_handle),
    tooLargeMessage: m.requests_response_body_too_large(),
  };
}

// --- Request revisions (ingress rewrites) -----------------------------------

export function requestRevisions(entry) {
  return entry && entry.data && Array.isArray(entry.data.request_revisions)
    ? entry.data.request_revisions
    : [];
}

// changedRequestRevisions returns the revisions that actually rewrote the
// body — the ones worth their own pane. Rewriters that ran and changed nothing
// are recorded too (no_change), but they get a pill on the Request tab instead.
export function changedRequestRevisions(entry) {
  return requestRevisions(entry).filter(
    (revision) => !(revision && revision.no_change),
  );
}

// requestNoChangeSteps summarizes the rewriters that inspected the request and
// left it byte-identical, so the tab strip can show the step ran without
// spending a tab on an empty pane.
export function requestNoChangeSteps(entry) {
  return requestRevisions(entry)
    .filter((revision) => revision && revision.no_change)
    .map((revision) => {
      const rewriter = String(revision.rewriter || "rewriter");
      return {
        id: "step-" + Number(revision.seq || 0),
        rewriter,
        label: m.requests_rewriter_no_change({ rewriter }),
        title: m.requests_rewriter_unchanged_help({ rewriter }),
      };
    });
}

// requestRevisionPercentLabel renders how much of the request body this revision
// removed (e.g. "-44%"), or '' when sizes are missing or the revision didn't
// shrink the body.
export function requestRevisionPercentLabel(revision) {
  const before = Number(revision && revision.bytes_before);
  const after = Number(revision && revision.bytes_after);
  if (
    !Number.isFinite(before) ||
    !Number.isFinite(after) ||
    before <= 0 ||
    after >= before
  )
    return "";
  const pct = (1 - after / before) * 100;
  return "-" + (pct >= 10 ? String(Math.round(pct)) : pct.toFixed(1)) + "%";
}

// requestRevisionPane renders one ingress rewrite: a structured summary
// of what the rewriter changed plus the rewritten body when it was captured.
export function requestRevisionPane(entry, revision) {
  const body = revision && revision.body;
  const hasBody = body != null && body !== "";
  const single = changedRequestRevisions(entry).length <= 1;
  const summary = {
    rewriter: (revision && revision.rewriter) || "",
    bytes:
      Number((revision && revision.bytes_before) || 0) +
      " → " +
      Number((revision && revision.bytes_after) || 0),
  };
  if (revision && revision.detail != null) {
    summary.detail = revision.detail;
  }

  return {
    title: m.requests_rewritten_title(),
    direction: "request",
    seq: single ? 0 : Number((revision && revision.seq) || 0),
    // Opening the Interactions drawer from this pane previews this rewrite
    // step (see conversationDrawer.openConversation).
    requestStep: "revision-" + Number((revision && revision.seq) || 0),
    kind: revision && revision.rewriter ? String(revision.rewriter) : "",
    savingsLabel: requestRevisionPercentLabel(revision),
    layout: "split",
    entry,
    copyHeaders: summary,
    copyBody: body,
    showErrorMessage: false,
    errorMessage: null,
    showHeaders: true,
    headers: summary,
    headersTitle: m.requests_what_changed_title(),
    showBody: hasBody,
    body,
    showEmpty: false,
    emptyMessage: "",
    showTooLarge: !hasBody,
    tooLargeMessage: m.requests_rewritten_body_not_captured(),
  };
}

// --- Usage / prompt-cache helpers -------------------------------------------


function requestUsage(entry) {
  const usage = entry && entry.usage;
  if (!usage || typeof usage !== "object") return null;
  return usage;
}

export function requestHasCachedTokens(entry) {
  const usage = requestUsage(entry);
  return Number((usage && usage.cached_input_tokens) || 0) > 0;
}

export function requestCacheSharePercent(entry) {
  const usage = requestUsage(entry);
  const inputTokens = Number((usage && usage.input_tokens) || 0);
  const cachedTokens = Number((usage && usage.cached_input_tokens) || 0);
  if (
    !Number.isFinite(inputTokens) ||
    inputTokens <= 0 ||
    !Number.isFinite(cachedTokens) ||
    cachedTokens <= 0
  ) {
    return 0;
  }
  return Math.max(0, Math.min(100, (cachedTokens / inputTokens) * 100));
}

export function requestCacheRatioLabel(entry) {
  const usage = requestUsage(entry);
  if (!usage) return "";
  const inputTokens = Number(usage.input_tokens || 0);
  const cachedTokens = Number(usage.cached_input_tokens || 0);
  if (inputTokens <= 0) {
    return m.requests_cached_count({ count: formatNumber(cachedTokens) });
  }
  return m.requests_cached_percent({
    percent: requestCacheSharePercent(entry).toFixed(1),
  });
}

export function requestCacheRatioPillLabel(entry) {
  if (!requestHasCachedTokens(entry)) return "";
  return requestCacheRatioLabel(entry);
}

// requestPromptCacheHighlight derives the estimated cached prompt prefix from
// the request body. `extractSegments` is the conversation-helpers
// extractRequestPromptTextSegments function (injected to keep this pure).
export function requestPromptCacheHighlight(entry, extractSegments) {
  const usage = requestUsage(entry);
  if (!usage || !entry || !entry.data || !entry.data.request_body) return null;

  const estimatedChars = Number(usage.estimated_cached_characters || 0);
  if (!Number.isFinite(estimatedChars) || estimatedChars <= 0) {
    return null;
  }

  if (typeof extractSegments !== "function") {
    return null;
  }

  const segments = extractSegments(entry.data.request_body);
  if (!Array.isArray(segments) || segments.length === 0) {
    return null;
  }

  return {
    characters: estimatedChars,
    segments,
  };
}

// --- Panes ------------------------------------------------------------------

export function requestLogRequestPane(entry, extractSegments) {
  const data = entry && entry.data ? entry.data : null;
  const empty = !data || (!data.request_headers && !data.request_body);
  const pending = empty && requestEntryLiveInProgress(entry);

  return {
    title: m.requests_request_title(),
    direction: "request",
    // Opening the Interactions drawer from this pane previews the original
    // client request (see conversationDrawer.openConversation).
    requestStep: "original",
    layout: "split",
    entry,
    copyHeaders: data && data.request_headers,
    copyBody: data && data.request_body,
    showErrorMessage: false,
    errorMessage: null,
    showHeaders: !!(data && data.request_headers),
    headers: data && data.request_headers,
    showBody: !!(data && data.request_body),
    body: data && data.request_body,
    bodyCacheRatioLabel: requestCacheRatioPillLabel(entry),
    promptCacheHighlight: requestPromptCacheHighlight(entry, extractSegments),
    // Ingress rewriters that ran without changing anything, rendered as muted
    // pills on the tab so the step is visible but costs no tab.
    noChangeSteps: requestNoChangeSteps(entry),
    showEmpty: empty && !pending,
    emptyMessage: m.requests_request_not_captured(),
    showPending: pending,
    pendingMessage: m.requests_waiting_request_data(),
    showTooLarge: !!(data && data.request_body_too_big_to_handle),
    tooLargeMessage: m.requests_request_body_too_large(),
  };
}

export function requestLogResponsePane(entry) {
  const data = entry && entry.data ? entry.data : null;
  const errorMessage = requestEntryErrorMessage(entry);
  const empty =
    !data || (!errorMessage && !data.response_headers && !data.response_body);
  const pending = empty && requestEntryLiveInProgress(entry);

  return {
    title: m.requests_response_title(),
    direction: "response",
    layout: "split",
    entry,
    copyHeaders: data && data.response_headers,
    copyBody: data && data.response_body,
    showErrorMessage: !!errorMessage,
    errorMessage,
    showHeaders: !!(data && data.response_headers),
    headers: data && data.response_headers,
    showBody: !!(data && data.response_body),
    body: data && data.response_body,
    streaming:
      !!(entry && entry._response_partial && data && data.response_body) &&
      requestEntryLiveInProgress(entry),
    showEmpty: empty && !pending,
    emptyMessage: m.requests_response_not_captured(),
    showPending: pending,
    pendingMessage: m.requests_response_in_progress(),
    showTooLarge: !!(data && data.response_body_too_big_to_handle),
    tooLargeMessage: m.requests_response_body_too_large(),
  };
}

// --- Guardrail outcomes -----------------------------------------------------

// requestGuardrailActionClass maps an outcome's verdict onto the status badge
// tones: stopped the request (block, respond, fail-closed) reads as an
// error, warn and fail-open as a warning, allow as success.
export function requestGuardrailActionClass(outcome) {
  switch (outcome && outcome.action) {
    case "block":
    case "respond":
      return "status-error";
    case "failure":
      return outcome.failMode === "closed" ? "status-error" : "status-warning";
    case "warn":
      return "status-warning";
    case "allow":
      return "status-success";
    default:
      return "status-neutral";
  }
}

function requestGuardrailFailModeLabel(outcome) {
  switch (outcome.failMode) {
    case "open":
      return m.requests_guardrail_fail_open();
    case "closed":
      return m.requests_guardrail_fail_closed();
    default:
      return "";
  }
}

function requestGuardrailEditedLabel(outcome) {
  if (!outcome.edited) return "";
  return outcome.target === "response"
    ? m.requests_guardrail_edited_response()
    : m.requests_guardrail_edited_request();
}

function requestGuardrailStreamLabel(outcome) {
  if (outcome.replacedEvents === null && outcome.droppedEvents === null) return "";
  return m.requests_guardrail_stream_events({
    replaced: formatNumber(outcome.replacedEvents || 0),
    dropped: formatNumber(outcome.droppedEvents || 0),
  });
}

// requestGuardrailRows flattens the entry's guardrail outcomes into the rows
// of the Guardrails table, one per instance that ran, in execution order.
// `detail` and `error` are pre-formatted for the expandable block; the list
// projection strips detail, so a row may gain it once the detail loads.
export function requestGuardrailRows(entry) {
  return workflowEntryGuardrails(entry).map((outcome) => ({
    id: "guardrail-" + outcome.seq,
    seq: outcome.seq,
    phase: phaseLabel(outcome.phase),
    step: outcome.step === null ? "" : String(outcome.step),
    instance: outcome.instance,
    type: outcome.type,
    action: outcome.action,
    actionLabel: workflowGuardrailActionLabel({ ...outcome, edited: false }),
    actionClass: requestGuardrailActionClass(outcome),
    edited: requestGuardrailEditedLabel(outcome),
    code: outcome.code,
    message: outcome.message,
    failMode: requestGuardrailFailModeLabel(outcome),
    streamEvents: requestGuardrailStreamLabel(outcome),
    duration: outcome.durationNs === null ? "" : formatDurationNs(outcome.durationNs),
    detail: outcome.detail === null || outcome.detail === undefined ? "" : formatJSON(outcome.detail),
    error: outcome.error,
  }));
}

// requestGuardrailVerdict is the one guardrail outcome worth a metadata badge:
// the first that stopped the request (blocked, answered, failed closed),
// else the first warning. Null when every guardrail let the request through.
export function requestGuardrailVerdict(entry) {
  const outcomes = workflowEntryGuardrails(entry);
  const outcome =
    outcomes.find((item) => requestGuardrailActionClass(item) === "status-error") ||
    outcomes.find((item) => requestGuardrailActionClass(item) === "status-warning");
  if (!outcome) return null;
  const instance = outcome.instance;
  const stop = requestGuardrailActionClass(outcome) === "status-error";
  let text;
  if (outcome.action === "failure") text = m.requests_guardrail_badge_failed({ instance });
  else if (outcome.action === "respond") text = m.requests_guardrail_badge_answered({ instance });
  else if (stop) text = m.requests_guardrail_badge_blocked({ instance });
  else text = m.requests_guardrail_badge_warned({ instance });
  return {
    tone: stop ? "danger" : "warning",
    text,
    actionLabel: workflowGuardrailActionLabel({ ...outcome, edited: false }),
  };
}

// requestGuardrailsPane backs the Guardrails tab: the outcome table plus the
// tab's own badge, which carries the worst verdict, or the row count when
// every guardrail let the request through.
export function requestGuardrailsPane(entry) {
  const rows = requestGuardrailRows(entry);
  const verdict = requestGuardrailVerdict(entry);
  return {
    type: "guardrails",
    title: m.requests_tab_guardrails(),
    entry,
    rows,
    badge: verdict
      ? {
          text: verdict.actionLabel,
          class: verdict.tone === "danger" ? "status-error" : "status-warning",
        }
      : { text: formatNumber(rows.length), class: "status-success" },
  };
}

// requestLogPanes returns the ordered Request/Response panes that back the tab
// strip: the original request, one pane per ingress rewrite revision, then
// either the single response or one pane per provider attempt, and last the
// guardrail outcomes when any guardrail ran.
export function requestLogPanes(entry, extractSegments) {
  const panes = [{ id: "request", pane: requestLogRequestPane(entry, extractSegments) }];
  changedRequestRevisions(entry).forEach((revision) => {
    panes.push({
      id: "revision-" + Number((revision && revision.seq) || 0),
      pane: requestRevisionPane(entry, revision),
    });
  });
  if (requestUsesPerAttemptResponses(entry)) {
    requestAttempts(entry).forEach((attempt) => {
      panes.push({
        id: "response-" + Number((attempt && attempt.seq) || 0),
        pane: requestAttemptResponsePane(entry, attempt),
      });
    });
  } else {
    panes.push({ id: "response", pane: requestLogResponsePane(entry) });
  }
  const guardrails = requestGuardrailsPane(entry);
  if (guardrails.rows.length > 0) {
    panes.push({ id: "guardrails", pane: guardrails });
  }
  return panes;
}

// requestLogDefaultPaneTab selects the tab shown first: the last valid (successful)
// response, falling back to the last attempt when none succeeded, and to the
// single response otherwise.
function requestLogDefaultPaneTab(entry) {
  if (!requestUsesPerAttemptResponses(entry)) return "response";
  const attempts = requestAttempts(entry);
  let target = null;
  attempts.forEach((attempt) => {
    if (attempt && attempt.success) target = attempt;
  });
  if (!target) target = attempts[attempts.length - 1];
  return target ? "response-" + Number(target.seq || 0) : "request";
}

// requestLogEffectiveTab resolves the active tab id, falling back to the default
// when nothing is selected yet or the selection no longer exists (e.g. a live
// entry gained attempts after the first render). Callers that already hold the
// entry's panes pass them in; rebuilding the whole set just to check an id
// re-walks every attempt and re-parses the error body.
export function requestLogEffectiveTab(active, entry, panes) {
  if (active) {
    const current = Array.isArray(panes) ? panes : requestLogPanes(entry);
    if (current.some((p) => p.id === active)) return active;
  }
  return requestLogDefaultPaneTab(entry);
}

// requestLogTabKeydownTarget implements roving-tabindex keyboard navigation for the
// request/response tablist. It returns the tab id to activate, or null for
// unhandled keys (the caller keeps the current selection, moves focus, and
// calls preventDefault).
export function requestLogTabKeydownTarget(key, ids, currentId) {
  if (!ids || !ids.length) return null;
  let idx = ids.indexOf(currentId);
  if (idx < 0) idx = 0;
  let next;
  switch (key) {
    case "ArrowRight":
    case "ArrowDown":
      next = (idx + 1) % ids.length;
      break;
    case "ArrowLeft":
    case "ArrowUp":
      next = (idx - 1 + ids.length) % ids.length;
      break;
    case "Home":
      next = 0;
      break;
    case "End":
      next = ids.length - 1;
      break;
    default:
      return null;
  }
  return ids[next];
}
