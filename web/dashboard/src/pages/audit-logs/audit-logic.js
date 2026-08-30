// Pure audit-log display/query logic (plus the tiny workflowFailoverTarget
// helper the metadata row needs).
// Keep this file free of Svelte-runtime imports so node:test can import it
// directly.

import { formatJSON, formatNumber } from "../../lib/utils/format.js";
import * as m from "../../lib/paraglide/messages.js";
import {
  findNestedErrorMessage,
  normalizeErrorText,
  tryParseJSON,
} from "./error-text.js";

// Re-exported: the panes are built here, so consumers get their formatter from
// the same module.
export { formatJSON };

function auditErrorMessageFromField(value) {
  if (value == null) return "";
  if (typeof value === "string") return normalizeErrorText(value, 0);
  return findNestedErrorMessage(value, 0);
}

function auditEntryStatusCodeValue(entry, data) {
  const candidates = [entry.status_code, entry.status, data.status_code, data.status];

  for (let i = 0; i < candidates.length; i++) {
    const parsed = Number(candidates[i]);
    if (Number.isFinite(parsed)) return parsed;
  }

  return null;
}

function hasTopLevelAuditErrorShape(value) {
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

function shouldInspectAuditResponseBody(entry, data) {
  const statusCode = auditEntryStatusCodeValue(entry, data);
  if (statusCode !== null && statusCode >= 400) return true;
  return hasTopLevelAuditErrorShape(data.response_body);
}

export function auditEntryErrorMessage(entry) {
  const data = entry && entry.data ? entry.data : null;
  if (!data) return "";
  const fieldMessage = auditErrorMessageFromField(data.error_message);
  if (fieldMessage) return fieldMessage;
  if (!shouldInspectAuditResponseBody(entry, data)) return "";
  return findNestedErrorMessage(data.response_body, 0);
}

// --- Retention note ---------------------------------------------------------
// `raw` is the raw LOGGING_RETENTION_DAYS runtime-config value.

function auditRetentionDays(raw) {
  if (raw === undefined || raw === null || String(raw).trim() === "")
    return null;

  const days = Number(raw);
  if (!Number.isInteger(days) || days < 0) return null;
  return days;
}

export function auditRetentionText(raw) {
  const days = auditRetentionDays(raw);
  if (days === null) return "";
  return (
    auditRetentionPrefix(raw) + auditRetentionHighlight(raw) + "."
  );
}

export function auditRetentionPrefix(raw) {
  const days = auditRetentionDays(raw);
  if (days === null) return "";
  return days === 0
    ? m.audit_retention_prefix_indefinite()
    : m.audit_retention_prefix_days();
}

export function auditRetentionHighlight(raw) {
  const days = auditRetentionDays(raw);
  if (days === null) return "";
  if (days === 0) return m.audit_retention_indefinitely();
  return m.audit_retention_days({ count: days });
}

// --- Query building ---------------------------------------------------------

// buildAuditLogQuery renders the GET /admin/audit/log query string: the
// shared date window first, then paging, then only the consolidated audit
// filters that are set.
export function buildAuditLogQuery({
  dateQuery,
  limit,
  offset,
  search,
  method,
  statusCode,
  stream,
}) {
  let qs = dateQuery;
  qs += "&limit=" + limit + "&offset=" + offset;
  if (search) qs += "&search=" + encodeURIComponent(search);
  if (method) qs += "&method=" + encodeURIComponent(method);
  if (statusCode) qs += "&status_code=" + encodeURIComponent(statusCode);
  if (stream) qs += "&stream=" + encodeURIComponent(stream);
  return qs;
}

// buildAuditSessionQuery renders the thread-children fetch for one session:
// GET /admin/audit/log?session_id=…  Deliberately no active list filters, so
// an expanded thread shows the whole session even when requests fall outside
// the date, user-path, or other filters used to find the thread.
export function buildAuditSessionQuery({ sessionId, limit }) {
  return (
    "session_id=" +
    encodeURIComponent(sessionId) +
    "&limit=" +
    (limit || 100) +
    "&offset=0"
  );
}

// --- Session grouping -------------------------------------------------------
// Grouped mode reuses the flat list shape: entries hold thread HEADS (each a
// normal audit entry plus `session_count`), total counts threads.

export function auditSessionId(entry) {
  return String((entry && entry.session_id) || "").trim();
}

export function auditSessionCount(entry) {
  const count = Number(entry && entry.session_count);
  return Number.isFinite(count) && count > 1 ? count : 1;
}

// auditIsThreadHead reports whether a row gets the expander: it belongs to a
// session with more entries than itself.
export function auditIsThreadHead(entry) {
  return !!auditSessionId(entry) && auditSessionCount(entry) > 1;
}

// auditLogFromSessions maps the GET /admin/audit/sessions payload into the
// shared list shape: one head entry per thread, newest-activity first.
export function auditLogFromSessions(payload) {
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
  };
}

// mergeAuditThreadChildren folds a fetched session_id page into a thread's
// children slot: head rows are dropped (the unfolded list holds only the
// older requests), and still-live entries from the previous partial list that
// the server does not return yet (displaced from the head list before being
// persisted) are preserved on top. preservedCount lets the caller keep the
// thread total honest.
export function mergeAuditThreadChildren(previousList, fetchedEntries, heads) {
  const headKeys = new Set(
    (Array.isArray(heads) ? heads : []).flatMap((head) =>
      auditEntryIdentityKeys(head),
    ),
  );
  const fetched = (Array.isArray(fetchedEntries) ? fetchedEntries : []).filter(
    (entry) => !auditEntryIdentityKeys(entry).some((key) => headKeys.has(key)),
  );
  const knownKeys = new Set([
    ...headKeys,
    ...fetched.flatMap((entry) => auditEntryIdentityKeys(entry)),
  ]);
  const preserved = (
    previousList && Array.isArray(previousList.entries)
      ? previousList.entries
      : []
  ).filter(
    (entry) =>
      entry &&
      entry._live &&
      !auditEntryIdentityKeys(entry).some((key) => knownKeys.has(key)),
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
      .map((entry) => auditSessionId(entry))
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

export function auditEntryKey(entry) {
  return String((entry && entry.id) || "").trim();
}

function auditEntryIdentityKeys(entry) {
  if (!entry) return [];
  const keys = [];
  const id = String(entry.id || "").trim();
  const requestID = String(entry.request_id || "").trim();
  if (id) keys.push("id:" + id);
  if (requestID) keys.push("request:" + requestID);
  return keys;
}

function auditEntryLivePreviewPending(entry) {
  return !!(entry && entry._live && entry._live_pending && !entry._audit_flushed);
}

function auditLiveDateRangeAllowsNow(filters) {
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

export function auditLogAllowsLiveEntries(payload, filters) {
  return (
    payload &&
    Number(payload.offset || 0) === 0 &&
    !(filters && filters.search) &&
    !(filters && filters.method) &&
    !(filters && filters.statusCode) &&
    !(filters && filters.stream) &&
    auditLiveDateRangeAllowsNow(filters)
  );
}

// auditLogWithLiveEntries prepends still-pending live preview rows onto a
// freshly fetched page so an in-flight request does not disappear on refresh.
// Persisted rows replace matching previews (matched by id or request_id).
export function auditLogWithLiveEntries(payload, currentEntries, filters) {
  const next =
    payload && typeof payload === "object"
      ? { ...payload }
      : { entries: [], total: 0, limit: 25, offset: 0 };
  const entries = Array.isArray(next.entries) ? next.entries : [];
  next.entries = entries;
  if (!auditLogAllowsLiveEntries(next, filters)) return next;

  const liveEntries = (Array.isArray(currentEntries) ? currentEntries : []).filter(
    (entry) => auditEntryLivePreviewPending(entry),
  );
  if (liveEntries.length === 0) return next;

  const persistedKeys = new Set(
    entries.flatMap((entry) => auditEntryIdentityKeys(entry)),
  );
  const prepend = [];
  liveEntries.forEach((entry) => {
    const keys = auditEntryIdentityKeys(entry);
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

// auditGroupedLogWithLiveEntries is auditLogWithLiveEntries for grouped mode:
// a still-pending live preview folds into its session's fetched head instead
// of duplicating the thread; previews without an on-screen thread prepend as
// singleton heads.
export function auditGroupedLogWithLiveEntries(payload, currentEntries, filters) {
  const next =
    payload && typeof payload === "object"
      ? { ...payload }
      : { entries: [], total: 0, limit: 25, offset: 0 };
  const entries = Array.isArray(next.entries) ? next.entries : [];
  next.entries = entries;
  if (!auditLogAllowsLiveEntries(next, filters)) return next;

  const liveEntries = (Array.isArray(currentEntries) ? currentEntries : []).filter(
    (entry) => auditEntryLivePreviewPending(entry),
  );
  if (liveEntries.length === 0) return next;

  const persistedKeys = new Set(
    entries.flatMap((entry) => auditEntryIdentityKeys(entry)),
  );
  const headBySession = new Map();
  entries.forEach((entry, index) => {
    const sid = auditSessionId(entry);
    if (sid && !headBySession.has(sid)) headBySession.set(sid, index);
  });

  const prepend = [];
  let merged = entries;
  liveEntries.forEach((entry) => {
    const keys = auditEntryIdentityKeys(entry);
    if (keys.length === 0) return;
    // The persisted page already carries this request (as a head): keep it.
    if (keys.some((key) => persistedKeys.has(key))) return;
    const sid = auditSessionId(entry);
    if (sid && headBySession.has(sid)) {
      // Fold into the fetched thread: the pending preview is newer than the
      // persisted head, so it becomes the head. Counts already accumulated by
      // the live grouping path are authoritative; otherwise this distinct
      // request increments the fetched count.
      const index = headBySession.get(sid);
      if (merged === entries) merged = [...entries];
      const previousCount = auditSessionCount(merged[index]);
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
  const key = auditEntryKey(entry);
  const current = expanded || {};
  if (!key) return current;
  if (current[key]) return {};
  // Audit details behave like an accordion: opening one row folds any row that
  // was already open, including entries nested under a grouped session.
  return { [key]: true };
}

export function pruneExpandedEntries(expanded, entries) {
  const current = expanded || {};
  const keys = new Set(
    (Array.isArray(entries) ? entries : [])
      .map((entry) => auditEntryKey(entry))
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

export function auditEntryLiveInProgress(entry) {
  if (!entry || !entry._live || !entry._live_pending) return false;
  const liveState = String(entry._live_state || "").trim();
  if (
    liveState === "audit.completed" ||
    liveState === "audit.flushed" ||
    liveState === "audit.detail"
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

function auditAttempts(entry) {
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

// auditUsesPerAttemptResponses reports whether responses should be split into
// one tab per attempt — when a failover/retry happened (more than one attempt)
// or any attempt failed — instead of a single combined Response tab.
function auditUsesPerAttemptResponses(entry) {
  const attempts = auditAttempts(entry);
  return (
    attempts.length > 1 || attempts.some((attempt) => !(attempt && attempt.success))
  );
}

function auditAttemptStatus(attempt) {
  if (!attempt) return "-";
  const status = attempt.status_code || attempt.status;
  if (status) return String(status);
  return attempt.success ? "ok" : "error";
}

function auditAttemptKind(attempt) {
  const kind = String((attempt && attempt.kind) || "").trim();
  return kind || "attempt";
}

function auditAttemptProvider(attempt) {
  if (!attempt) return "-";
  const name = String(attempt.provider_name || "").trim();
  const type = String(attempt.provider_type || attempt.provider || "").trim();
  if (name && type && name !== type) return name + " (" + type + ")";
  return name || type || "-";
}

function auditAttemptModel(attempt) {
  return String((attempt && attempt.model) || "").trim() || "-";
}

// auditAttemptTrack returns the attempt list when it is worth surfacing on the
// collapsed summary row: a retry/failover happened (more than one attempt) or
// any single attempt failed. A lone successful attempt is the common case and
// needs no indicator.
export function auditAttemptTrack(entry) {
  const attempts = auditAttempts(entry);
  if (attempts.length > 1) return attempts;
  if (attempts.some((attempt) => !(attempt && attempt.success))) return attempts;
  return [];
}

export function auditAttemptTrackCount(entry) {
  return auditAttempts(entry).length + "×";
}

export function auditAttemptTrackTitle(entry) {
  const attempts = auditAttempts(entry);
  const failed = attempts.filter((attempt) => !(attempt && attempt.success)).length;
  return failed > 0
    ? m.audit_provider_attempts_failed({ count: attempts.length, failed })
    : m.audit_provider_attempts({ count: attempts.length });
}

export function auditAttemptSegmentTitle(attempt) {
  if (!attempt) return "";
  const parts = ["#" + Number(attempt.seq || 0)];
  const kind = auditAttemptKind(attempt);
  if (kind && kind !== "attempt") parts.push(kind);
  parts.push(auditAttemptStatus(attempt));
  const provider = auditAttemptProvider(attempt);
  if (provider && provider !== "-") parts.push(provider);
  const model = auditAttemptModel(attempt);
  if (model && model !== "-") parts.push(model);
  parts.push(
    attempt.success ? m.audit_attempt_succeeded() : m.audit_attempt_failed_status(),
  );
  return parts.join(" · ");
}

// auditAttemptBody is the captured raw upstream response body only: the final
// response for the successful attempt, or the provider's raw error body for a
// failed one. The normalized error message is surfaced separately.
function auditAttemptBody(entry, attempt) {
  if (!attempt) return null;
  if (attempt.success) {
    const data = entry && entry.data ? entry.data : null;
    return data && data.response_body != null ? data.response_body : null;
  }
  return attempt.response_body != null && attempt.response_body !== ""
    ? attempt.response_body
    : null;
}

function auditAttemptErrorMessage(attempt) {
  if (!attempt || attempt.success) return "";
  const message = String(attempt.error_message || "").trim();
  const code = String(attempt.error_code || "").trim();
  const type = String(attempt.error_type || "").trim();
  if (message && code) return code + ": " + message;
  return message || code || type || m.audit_attempt_failed();
}

function auditAttemptHeaders(entry, attempt) {
  if (!attempt) return null;
  if (attempt.success) {
    const data = entry && entry.data ? entry.data : null;
    return data ? data.response_headers : null;
  }
  return attempt.response_headers || null;
}

function auditAttemptStatusCode(attempt) {
  const code = Number(attempt && attempt.status_code);
  return Number.isFinite(code) && code > 0 ? code : null;
}

function auditAttemptResponsePane(entry, attempt) {
  const success = !!(attempt && attempt.success);
  const data = entry && entry.data ? entry.data : null;
  const body = auditAttemptBody(entry, attempt);
  const headers = auditAttemptHeaders(entry, attempt);
  const errorMessage = auditAttemptErrorMessage(attempt);
  const hasBody = body != null && body !== "";
  const kind = auditAttemptKind(attempt);
  // With only one response tab the seq/type/status chips are just noise (it's
  // the whole response); show them only to tell apart multiple attempt tabs.
  const single = auditAttempts(entry).length <= 1;

  return {
    title: m.audit_response_title(),
    direction: "response",
    seq: single ? 0 : Number((attempt && attempt.seq) || 0),
    kind: single ? "" : kind === "attempt" ? "" : kind,
    statusCode: single ? null : auditAttemptStatusCode(attempt),
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
    emptyMessage: m.audit_attempt_not_captured(),
    showTooLarge: !!(success && data && data.response_body_too_big_to_handle),
    tooLargeMessage: m.audit_response_body_too_large(),
  };
}

// --- Request revisions (ingress rewrites) -----------------------------------

export function auditRequestRevisions(entry) {
  return entry && entry.data && Array.isArray(entry.data.request_revisions)
    ? entry.data.request_revisions
    : [];
}

// auditChangedRequestRevisions returns the revisions that actually rewrote the
// body — the ones worth their own pane. Rewriters that ran and changed nothing
// are recorded too (no_change), but they get a pill on the Request tab instead.
export function auditChangedRequestRevisions(entry) {
  return auditRequestRevisions(entry).filter(
    (revision) => !(revision && revision.no_change),
  );
}

// auditNoChangeSteps summarizes the rewriters that inspected the request and
// left it byte-identical, so the tab strip can show the step ran without
// spending a tab on an empty pane.
export function auditNoChangeSteps(entry) {
  return auditRequestRevisions(entry)
    .filter((revision) => revision && revision.no_change)
    .map((revision) => {
      const rewriter = String(revision.rewriter || "rewriter");
      return {
        id: "step-" + Number(revision.seq || 0),
        rewriter,
        label: m.audit_rewriter_no_change({ rewriter }),
        title: m.audit_rewriter_unchanged_help({ rewriter }),
      };
    });
}

// auditRevisionPercentLabel renders how much of the request body this revision
// removed (e.g. "-44%"), or '' when sizes are missing or the revision didn't
// shrink the body.
export function auditRevisionPercentLabel(revision) {
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

// auditRequestRevisionPane renders one ingress rewrite: a structured summary
// of what the rewriter changed plus the rewritten body when it was captured.
export function auditRequestRevisionPane(entry, revision) {
  const body = revision && revision.body;
  const hasBody = body != null && body !== "";
  const single = auditChangedRequestRevisions(entry).length <= 1;
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
    title: m.audit_rewritten_title(),
    direction: "request",
    seq: single ? 0 : Number((revision && revision.seq) || 0),
    // Opening the Interactions drawer from this pane previews this rewrite
    // step (see conversationDrawer.openConversation).
    requestStep: "revision-" + Number((revision && revision.seq) || 0),
    kind: revision && revision.rewriter ? String(revision.rewriter) : "",
    savingsLabel: auditRevisionPercentLabel(revision),
    layout: "split",
    entry,
    copyHeaders: summary,
    copyBody: body,
    showErrorMessage: false,
    errorMessage: null,
    showHeaders: true,
    headers: summary,
    headersTitle: m.audit_what_changed_title(),
    showBody: hasBody,
    body,
    showEmpty: false,
    emptyMessage: "",
    showTooLarge: !hasBody,
    tooLargeMessage: m.audit_rewritten_body_not_captured(),
  };
}

// --- Usage / prompt-cache helpers -------------------------------------------


function auditUsage(entry) {
  const usage = entry && entry.usage;
  if (!usage || typeof usage !== "object") return null;
  return usage;
}

export function auditHasCachedTokens(entry) {
  const usage = auditUsage(entry);
  return Number((usage && usage.cached_input_tokens) || 0) > 0;
}

export function auditCacheSharePercent(entry) {
  const usage = auditUsage(entry);
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

export function auditCacheRatioLabel(entry) {
  const usage = auditUsage(entry);
  if (!usage) return "";
  const inputTokens = Number(usage.input_tokens || 0);
  const cachedTokens = Number(usage.cached_input_tokens || 0);
  if (inputTokens <= 0) {
    return m.audit_cached_count({ count: formatNumber(cachedTokens) });
  }
  return m.audit_cached_percent({
    percent: auditCacheSharePercent(entry).toFixed(1),
  });
}

export function auditCacheRatioPillLabel(entry) {
  if (!auditHasCachedTokens(entry)) return "";
  return auditCacheRatioLabel(entry);
}

// auditPromptCacheHighlight derives the estimated cached prompt prefix from
// the request body. `extractSegments` is the conversation-helpers
// extractRequestPromptTextSegments function (injected to keep this pure).
export function auditPromptCacheHighlight(entry, extractSegments) {
  const usage = auditUsage(entry);
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

export function auditRequestPane(entry, extractSegments) {
  const data = entry && entry.data ? entry.data : null;
  const empty = !data || (!data.request_headers && !data.request_body);
  const pending = empty && auditEntryLiveInProgress(entry);

  return {
    title: m.audit_request_title(),
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
    bodyCacheRatioLabel: auditCacheRatioPillLabel(entry),
    promptCacheHighlight: auditPromptCacheHighlight(entry, extractSegments),
    // Ingress rewriters that ran without changing anything, rendered as muted
    // pills on the tab so the step is visible but costs no tab.
    noChangeSteps: auditNoChangeSteps(entry),
    showEmpty: empty && !pending,
    emptyMessage: m.audit_request_not_captured(),
    showPending: pending,
    pendingMessage: m.audit_waiting_request_data(),
    showTooLarge: !!(data && data.request_body_too_big_to_handle),
    tooLargeMessage: m.audit_request_body_too_large(),
  };
}

export function auditResponsePane(entry) {
  const data = entry && entry.data ? entry.data : null;
  const errorMessage = auditEntryErrorMessage(entry);
  const empty =
    !data || (!errorMessage && !data.response_headers && !data.response_body);
  const pending = empty && auditEntryLiveInProgress(entry);

  return {
    title: m.audit_response_title(),
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
      auditEntryLiveInProgress(entry),
    showEmpty: empty && !pending,
    emptyMessage: m.audit_response_not_captured(),
    showPending: pending,
    pendingMessage: m.audit_response_in_progress(),
    showTooLarge: !!(data && data.response_body_too_big_to_handle),
    tooLargeMessage: m.audit_response_body_too_large(),
  };
}

// auditPanes returns the ordered Request/Response panes that back the tab
// strip: the original request, one pane per ingress rewrite revision, then
// either the single response or one pane per provider attempt.
export function auditPanes(entry, extractSegments) {
  const panes = [{ id: "request", pane: auditRequestPane(entry, extractSegments) }];
  auditChangedRequestRevisions(entry).forEach((revision) => {
    panes.push({
      id: "revision-" + Number((revision && revision.seq) || 0),
      pane: auditRequestRevisionPane(entry, revision),
    });
  });
  if (auditUsesPerAttemptResponses(entry)) {
    auditAttempts(entry).forEach((attempt) => {
      panes.push({
        id: "response-" + Number((attempt && attempt.seq) || 0),
        pane: auditAttemptResponsePane(entry, attempt),
      });
    });
  } else {
    panes.push({ id: "response", pane: auditResponsePane(entry) });
  }
  return panes;
}

// auditDefaultPaneTab selects the tab shown first: the last valid (successful)
// response, falling back to the last attempt when none succeeded, and to the
// single response otherwise.
function auditDefaultPaneTab(entry) {
  if (!auditUsesPerAttemptResponses(entry)) return "response";
  const attempts = auditAttempts(entry);
  let target = null;
  attempts.forEach((attempt) => {
    if (attempt && attempt.success) target = attempt;
  });
  if (!target) target = attempts[attempts.length - 1];
  return target ? "response-" + Number(target.seq || 0) : "request";
}

// auditEffectiveTab resolves the active tab id, falling back to the default
// when nothing is selected yet or the selection no longer exists (e.g. a live
// entry gained attempts after the first render). Callers that already hold the
// entry's panes pass them in; rebuilding the whole set just to check an id
// re-walks every attempt and re-parses the error body.
export function auditEffectiveTab(active, entry, panes) {
  if (active) {
    const current = Array.isArray(panes) ? panes : auditPanes(entry);
    if (current.some((p) => p.id === active)) return active;
  }
  return auditDefaultPaneTab(entry);
}

// auditTabKeydownTarget implements roving-tabindex keyboard navigation for the
// request/response tablist. It returns the tab id to activate, or null for
// unhandled keys (the caller keeps the current selection, moves focus, and
// calls preventDefault).
export function auditTabKeydownTarget(key, ids, currentId) {
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
