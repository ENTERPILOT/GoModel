// Interactions drawer state. Pure transcript and branch shaping lives in
// conversation-helpers.js.

import { apiFetch, getJSON, isAbortError } from "$lib/api/client.js";
import { isGatewayAuthError } from "$lib/api/errors.js";
import { auth } from "$lib/stores/auth.svelte.js";
import * as m from "$lib/paraglide/messages.js";
import { untrack } from "svelte";
import { liveLogs } from "./liveLogs.svelte.js";
import {
  auditRecordChangesAfter,
  auditRecordKey,
  isLiveAuditRecordChange,
} from "./audit-records.js";
import {
  conversationAnchorScrollTop,
  conversationPinnedToBottom,
} from "./conversation-panel.js";
import {
  REQUEST_STEP_FINAL,
  buildConversationView,
  buildFollowUpHeaders,
  buildFollowUpRequest,
  canBuildFollowUpRequest,
  canShowConversation,
  conversationEntryByRequestID,
  conversationEntryIsLatest,
  conversationFollowUpEntry,
  conversationRequestSteps,
  followUpEndpointKind,
  formatJSON,
  latestRenderableConversationEntry,
  matchLiveConversationEntry,
  mergedConversationEntryIDs,
  normalizedConversationRequestStep,
  renderBodyWithConversationHighlights,
  shouldHydrateConversation,
} from "./conversation-helpers.js";

const FOLLOW_UP_TIMEOUT_MS = 10 * 60 * 1000;
const FOLLOW_UP_PERSISTENCE_TIMEOUT_MS = 15 * 1000;
const FOLLOW_UP_POLL_INTERVAL_MS = 250;

class ConversationDrawerStore {
  conversationOpen = $state(false);
  conversationLoading = $state(false);
  conversationError = $state("");
  conversationAnchorID = $state("");
  conversationEntryIDs = $state([]);
  conversationSessionID = $state("");
  conversationTruncated = $state(false);
  conversationFollowLatest = $state(false);
  conversationOpenedFromID = $state("");
  // Which request step of the previewed (anchor) entry the transcript renders.
  // Defaults to the final shape — the request actually sent to the provider.
  conversationRequestStep = $state(REQUEST_STEP_FINAL);
  followUpText = $state("");
  followUpSending = $state(false);
  followUpError = $state("");
  followUpRequestID = "";
  followUpAbort = null;

  conversationRequestToken = 0;
  conversationReturnFocusEl = null;
  bodyPointerStart = null;
  revisionDetailRequested = new Set();

  conversationDialogEl = $state(null);
  conversationCloseBtnEl = $state(null);
  conversationContentEl = $state(null);
  conversationThreadEl = $state(null);
  // Whether the transcript view sits at its bottom edge. Tracked from the
  // content's scroll events (user or programmatic) rather than measured on
  // demand: by the time a record change reaches the drawer, Svelte has
  // already grown the DOM, so a fresh measurement always reads "scrolled up".
  conversationPinned = true;

  get conversationEntries() {
    return (this.conversationEntryIDs || [])
      .map((id) => liveLogs.auditRecord(id))
      .filter(Boolean);
  }

  conversationView = $derived.by(() =>
    buildConversationView(this.conversationEntries, this.conversationAnchorID, {
      anchorRequestStep: this.conversationRequestStep,
    }));

  get conversationMessages() {
    return this.conversationView.messages;
  }

  get conversationBranchEntryIDs() {
    return this.conversationView.entryIDs;
  }

  _setConversationEntryIDs(ids) {
    this.conversationEntryIDs = [...new Set((Array.isArray(ids) ? ids : []).filter(Boolean))];
    liveLogs.pinAuditRecords(this.conversationEntryIDs);
  }

  canShowConversation(entry) {
    return canShowConversation(entry);
  }

  startBodyInteraction(event) {
    this.bodyPointerStart = {
      x: event.clientX,
      y: event.clientY,
    };
  }

  _isBodyDrag(event) {
    if (!this.bodyPointerStart) return false;
    const dx = Math.abs(event.clientX - this.bodyPointerStart.x);
    const dy = Math.abs(event.clientY - this.bodyPointerStart.y);
    return dx > 4 || dy > 4;
  }

  _hasActiveSelection() {
    const selection = window.getSelection ? window.getSelection() : null;
    if (!selection) return false;
    if (selection.isCollapsed) return false;
    return String(selection.toString() || "").trim().length > 0;
  }

  handleBodyConversationClick(event, entry, requestStep) {
    const wasDrag = this._isBodyDrag(event);
    this.bodyPointerStart = null;
    if (wasDrag) return;
    if (this._hasActiveSelection()) return;
    if (!this.canShowConversation(entry)) return;
    const el = event.target && event.target.closest ? event.target.closest('[data-conversation-trigger="1"]') : null;
    if (!el) return;
    event.preventDefault();
    event.stopPropagation();
    this.openConversation(entry, el, requestStep);
  }

  handleErrorConversationClick(event, entry) {
    const wasDrag = this._isBodyDrag(event);
    this.bodyPointerStart = null;
    if (wasDrag) return;
    if (this._hasActiveSelection()) return;
    if (!this.canShowConversation(entry)) return;
    event.preventDefault();
    event.stopPropagation();
    this.openConversation(entry, event.currentTarget);
  }

  formatJSON(value) {
    return formatJSON(value);
  }

  renderBodyWithConversationHighlights(entry, value, options) {
    return renderBodyWithConversationHighlights(entry, value, {
      formatJSON: (v) => this.formatJSON(v),
      canShowConversation: (e) => this.canShowConversation(e),
      promptCacheHighlight: options && options.promptCacheHighlight,
    });
  }

  async openConversation(entry, triggerEl, requestStep) {
    if (!entry || !entry.id || !this.canShowConversation(entry)) return;

    const activeEl = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    if (triggerEl instanceof HTMLElement) {
      this.conversationReturnFocusEl = triggerEl;
    } else if (activeEl && activeEl !== document.body) {
      this.conversationReturnFocusEl = activeEl;
    }

    const requestToken = ++this.conversationRequestToken;
    this.conversationOpen = true;
    this.conversationError = "";
    this.conversationAnchorID = entry.id;
    this.conversationOpenedFromID = entry.id;
    this.conversationSessionID = String(entry.session_id || "").trim();
    this._setConversationEntryIDs([]);
    this.conversationTruncated = false;
    this.conversationFollowLatest = false;
    // Opening from a specific audit pane (original request or one rewrite)
    // previews that step; every other entry point defaults to the final
    // shape sent to the provider.
    this.conversationRequestStep =
      normalizedConversationRequestStep(entry, requestStep);
    this.revisionDetailRequested = new Set();
    this.followUpText = "";
    this.followUpError = "";
    this.followUpRequestID = "";
    this.conversationPinned = true;
    requestAnimationFrame(() => this._focusConversationDrawer());

    // A live entry has no persisted row to fetch yet — render it from the
    // live preview data and keep re-rendering as the shared record cache
    // receives stream events.
    if (this._conversationEntryLivePending(entry)) {
      this.conversationFollowLatest = true;
      this.conversationLoading = false;
      this._addConversationRecord(
        liveLogs.upsertAuditRecord(entry, "conversation.live"),
      );
      return;
    }
    this.conversationLoading = true;
    await this.fetchConversation(entry.id, requestToken, true, true);
  }

  _conversationEntryLivePending(entry) {
    return typeof liveLogs.auditEntryLiveDetailPending === "function" &&
      liveLogs.auditEntryLiveDetailPending(entry);
  }

  _applyConversationView() {
    const entries = this.conversationEntries;
    const view = buildConversationView(entries, this.conversationAnchorID);
    if (this.conversationFollowLatest) {
      const latest = latestRenderableConversationEntry(entries, view.entryIDs);
      if (latest && latest.id && String(latest.id) !== this.conversationAnchorID) {
        this._setConversationAnchor(String(latest.id));
      }
    }
    void this._ensureAnchorRevisionBodies();
  }

  // Request-step preview (ingress rewrites, e.g. token compression).

  // A step selection addresses one entry's rewrite chain. When the anchor
  // moves to a different entry (follow-latest, a persisted follow-up), the
  // new anchor renders its own final shape until the operator picks again.
  _setConversationAnchor(id) {
    const next = String(id || "");
    if (next === this.conversationAnchorID) return;
    this.conversationAnchorID = next;
    this.conversationRequestStep = REQUEST_STEP_FINAL;
  }

  conversationRequestSteps() {
    return conversationRequestSteps(this.selectedConversationEntry());
  }

  selectRequestStep(stepID) {
    this.conversationRequestStep = String(stepID || REQUEST_STEP_FINAL);
    void this._ensureAnchorRevisionBodies();
  }

  // Conversation entries arrive with revision bodies stripped (metadata
  // only). When the previewed entry has rewrite steps whose bodies are
  // missing, load the full record once from the detail endpoint so the step
  // preview — including the default final shape — can render them.
  async _ensureAnchorRevisionBodies() {
    const entry = this.selectedConversationEntry();
    const entryID = String(entry && entry.id || "").trim();
    if (!entryID || entry._detail_loaded) return;
    const steps = conversationRequestSteps(entry);
    if (!steps.some((step) => step.seq > 0 && !step.hasBody)) return;
    // Capture the guard set: openConversation replaces it per drawer
    // session, and a request finishing after a reopen must not unpin the new
    // session's guard for the same entry. The success path is session-safe
    // as-is — it only enriches the shared, monotonic record cache.
    const requested = this.revisionDetailRequested;
    if (requested.has(entryID)) return;
    requested.add(entryID);
    try {
      const result = await getJSON(
        "/admin/audit/detail?log_id=" + encodeURIComponent(entryID),
        { label: "audit detail" },
      );
      // A request that produced no usable detail must not pin the guard, or
      // a transient failure would lock the preview to the original request
      // until the drawer is reopened. The next record change or step
      // selection retries.
      if (!result.ok || result.stale || !result.data) {
        requested.delete(entryID);
        return;
      }
      liveLogs.upsertAuditRecord(
        { ...result.data, _detail_loaded: true },
        "audit.detail",
      );
    } catch (e) {
      requested.delete(entryID);
      console.error("Failed to fetch audit detail for request steps:", e);
    }
  }

  // Re-render an open conversation when its normalized audit record changes.
  // Once the selected anchor is persisted, hydrate prior turns from storage.
  handleAuditRecordChange(entry, eventType) {
    if (!this.conversationOpen || !entry) return;
    const entryID = String(entry.id || "").trim();
    const match = matchLiveConversationEntry(this.conversationEntries, this.conversationAnchorID,
      this.conversationSessionID, this.followUpRequestID, entry);
    if (!match.accepted) return;
    this.followUpRequestID = match.followUpRequestID;

    if (entryID && match.submittedChild) {
      this._setConversationAnchor(entryID);
      this.conversationFollowLatest = true;
    }

    // The operator's own follow-up always comes into view; every other live
    // update only keeps following while the view is already at the bottom.
    this._addConversationRecord(entry, !!match.submittedChild);
    const state = String(eventType || entry._live_state || "").trim();
    // A sibling flush says nothing about whether the mutable selected anchor
    // is queryable. Hydrate only when that anchor itself is persisted.
    if (shouldHydrateConversation(state, entryID, this.conversationAnchorID)) {
      if (this.conversationMessages.length === 0) this.conversationLoading = true;
      const requestToken = ++this.conversationRequestToken;
      void this.fetchConversation(entryID, requestToken, false);
    }
  }

  _addConversationRecord(entry, forceScroll = false) {
    const id = auditRecordKey(entry);
    if (id && !this.conversationEntryIDs.includes(id)) {
      this._setConversationEntryIDs([...this.conversationEntryIDs, id]);
    }
    if (!this.conversationSessionID && entry.session_id) {
      this.conversationSessionID = String(entry.session_id).trim();
    }
    this._applyConversationView();
    if (this.conversationFollowLatest) this._scheduleLatestScroll(forceScroll);
  }

  // conversationLiveWaiting reports whether the open live conversation is
  // still waiting on the in-flight request (drives the drawer's spinner).
  conversationLiveWaiting() {
    if (!this.conversationOpen) return false;
    const branchIDs = new Set(this.conversationBranchEntryIDs || []);
    return (this.conversationEntries || []).some((entry) => branchIDs.has(String(entry && entry.id || "")) && entry._live &&
      (typeof liveLogs.liveAuditStateSettled !== "function" ||
        !liveLogs.liveAuditStateSettled(entry._live_state)));
  }

  conversationLiveStatusText() {
    return (this.conversationMessages || []).length > 0
      ? m.interaction_model_responding()
      : m.audit_waiting_request_data();
  }

  closeConversation() {
    if (this.followUpAbort) this.followUpAbort.abort();
    this.conversationOpen = false;
    this.conversationRequestToken++;
    this.conversationSessionID = "";
    this._setConversationEntryIDs([]);
    this.conversationFollowLatest = false;
    this.conversationOpenedFromID = "";
    this.followUpRequestID = "";
    this.followUpSending = false;
    const returnFocusEl = this.conversationReturnFocusEl;
    this.conversationReturnFocusEl = null;
    if (returnFocusEl && typeof returnFocusEl.focus === "function" && document.contains(returnFocusEl)) {
      requestAnimationFrame(() => returnFocusEl.focus());
    }
  }

  _focusConversationDrawer() {
    if (!this.conversationOpen) return;
    const closeBtn = this.conversationCloseBtnEl;
    if (closeBtn && typeof closeBtn.focus === "function") {
      closeBtn.focus();
      return;
    }
    const drawer = this.conversationDialogEl;
    if (drawer && typeof drawer.focus === "function") {
      drawer.focus();
    }
  }

  async fetchConversation(logID, requestToken, scrollToAnchor = true, detectFollowLatest = false) {
    try {
      const qs = "log_id=" + encodeURIComponent(logID) + "&limit=120";
      const result = await getJSON("/admin/audit/conversation?" + qs, {
        label: "audit conversation",
      });

      if (requestToken !== this.conversationRequestToken) return;
      if (result.stale) return;

      if (!result.ok) {
        this.conversationError = m.interaction_load_unavailable();
        return;
      }

      const payload = result.data || {};
      const responseAnchorID = payload.anchor_id || logID;
      const incoming = Array.isArray(payload.entries) ? payload.entries : [];
      const stored = liveLogs.upsertAuditRecords(incoming, "conversation.hydrated");
      const ids = mergedConversationEntryIDs(this.conversationEntryIDs, stored);
      if (ids.length > this.conversationEntryIDs.length) {
        this._setConversationEntryIDs(ids);
      }
      if (detectFollowLatest) {
        this.conversationFollowLatest = conversationEntryIsLatest(stored, responseAnchorID);
      }
      this._setConversationAnchor(responseAnchorID);
      const anchor = this.conversationEntries.find((entry) => entry.id === this.conversationAnchorID);
      if (anchor && anchor.session_id) this.conversationSessionID = String(anchor.session_id).trim();
      this.conversationTruncated = !!payload.truncated;
      this._applyConversationView();
      // Opening the drawer (scrollToAnchor) positions the view on the latest
      // turn or the anchor. The re-hydration after a live flush redraws the
      // same transcript from storage and must not move a reader who has
      // scrolled up, so it only keeps an existing pin.
      if (this.conversationFollowLatest) this._scheduleLatestScroll(scrollToAnchor);
      else if (scrollToAnchor) this._scheduleAnchorScroll();
    } catch (e) {
      if (requestToken !== this.conversationRequestToken) return;
      console.error("Failed to fetch audit conversation:", e);
      this.conversationError = m.interaction_load_failed();
    } finally {
      if (requestToken === this.conversationRequestToken) {
        this.conversationLoading = false;
      }
    }
  }

  selectedConversationEntry() {
    return conversationFollowUpEntry(this.conversationEntries, this.conversationAnchorID);
  }

  followUpKind() {
    const selected = this.selectedConversationEntry();
    if (!canBuildFollowUpRequest(selected)) return "";
    return followUpEndpointKind(selected.path);
  }

  canSendFollowUp() {
    return !!this.followUpKind() && !!String(this.followUpText || "").trim() &&
      !this.followUpSending && !this.conversationLiveWaiting();
  }

  async sendFollowUp() {
    if (!this.canSendFollowUp()) return;
    const entry = this.selectedConversationEntry();
    const body = buildFollowUpRequest(entry, this.followUpText);
    if (!entry || !body) return;
    const parentID = this.conversationAnchorID;
    const requestID = createRequestID();
    const controller = new AbortController();
    let timedOut = false;
    const timeout = setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, FOLLOW_UP_TIMEOUT_MS);

    this.followUpSending = true;
    this.followUpError = "";
    this.followUpRequestID = requestID;
    this.followUpAbort = controller;
    const generation = auth.generation;
    try {
      const headers = buildFollowUpHeaders(entry, parentID, requestID);
      const response = await apiFetch(entry.path, {
        method: "POST",
        headers,
        body: JSON.stringify(body),
        signal: controller.signal,
      });
      const responseRequestID = String(response.headers.get("X-Request-ID") || "").trim();
      if (responseRequestID && this.followUpRequestID) this.followUpRequestID = responseRequestID;
      if (!response.ok) {
        let payload = null;
        try {
          payload = await response.json();
        } catch { /* keep the generic message */ }
        // The gateway rejecting the dashboard key reopens the key dialog; a
        // provider rejecting its own key reads as a failed follow-up.
        if (response.status === 401 && isGatewayAuthError(payload)) {
          auth.handleUnauthorized(generation);
        }
        this.followUpError =
          (payload && payload.error && payload.error.message) || m.interaction_send_unavailable();
        if (this.followUpRequestID === requestID || this.followUpRequestID === responseRequestID) {
          this.followUpRequestID = "";
        }
        return;
      }
      this.followUpText = "";
      await drainResponse(response);
      if (!liveLogs.liveLogsStreaming && this.conversationOpen) {
        const requestToken = ++this.conversationRequestToken;
        await this._selectPersistedFollowUp(parentID, this.followUpRequestID, requestToken, controller.signal);
      }
    } catch (error) {
      if (isAbortError(error) || controller.signal.aborted) {
        if (timedOut && this.conversationOpen) {
          this.followUpError = m.interaction_request_timed_out();
        }
      } else {
        console.error("Failed to send interaction follow-up:", error);
        this.followUpError = m.interaction_send_failed();
      }
    } finally {
      clearTimeout(timeout);
      if (this.followUpAbort === controller) {
        this.followUpAbort = null;
        this.followUpSending = false;
      }
    }
  }

  async _selectPersistedFollowUp(parentID, requestID, requestToken, signal) {
    const deadline = Date.now() + FOLLOW_UP_PERSISTENCE_TIMEOUT_MS;
    while (this.conversationOpen && requestToken === this.conversationRequestToken && Date.now() < deadline) {
      const qs = "log_id=" + encodeURIComponent(parentID) + "&limit=120";
      const result = await getJSON("/admin/audit/conversation?" + qs, {
        label: "audit conversation",
        signal,
      });
      if (result.stale || requestToken !== this.conversationRequestToken) return;
      if (result.ok) {
        const payload = result.data || {};
        const entries = Array.isArray(payload.entries) ? payload.entries : [];
        const child = conversationEntryByRequestID(entries, requestID);
        if (child && child.id) {
          const stored = liveLogs.upsertAuditRecords(entries, "conversation.hydrated");
          this._setConversationEntryIDs(
            mergedConversationEntryIDs(this.conversationEntryIDs, stored),
          );
          this._setConversationAnchor(String(child.id));
          this.conversationFollowLatest = false;
          if (this.followUpRequestID === requestID) this.followUpRequestID = "";
          this.conversationTruncated = !!payload.truncated;
          if (child.session_id) this.conversationSessionID = String(child.session_id).trim();
          this._applyConversationView();
          this.conversationFollowLatest = true;
          this._scheduleLatestScroll(true);
          return;
        }
      }
      await waitFor(FOLLOW_UP_POLL_INTERVAL_MS, signal);
    }
    if (this.conversationOpen && requestToken === this.conversationRequestToken) {
      this.followUpError = m.interaction_saved_unavailable();
    }
  }

  // Both scroll helpers move the drawer's own scroll container instead of
  // calling scrollIntoView: scrollIntoView also scrolls every scrollable
  // ancestor, so each streamed chunk used to drag the audit list behind the
  // drawer along with it.
  _scheduleAnchorScroll() {
    requestAnimationFrame(() => {
      const content = this.conversationContentEl;
      const thread = this.conversationThreadEl;
      if (!content || !thread) return;
      const anchors = thread.querySelectorAll('[data-conversation-anchor="true"]');
      const target = anchors[anchors.length - 1];
      if (!target) return;
      const contentRect = content.getBoundingClientRect();
      const targetRect = target.getBoundingClientRect();
      content.scrollTop = conversationAnchorScrollTop(
        content.scrollTop,
        content.clientHeight,
        contentRect.top,
        targetRect.top,
        targetRect.height,
      );
    });
  }

  // noteConversationScroll records whether the view is at the bottom after
  // every scroll of the drawer content — the operator reading history
  // unpins it, scrolling back down (or our own follow scroll) re-pins it.
  noteConversationScroll() {
    const content = this.conversationContentEl;
    if (!content) return;
    this.conversationPinned = conversationPinnedToBottom(
      content.scrollTop,
      content.scrollHeight,
      content.clientHeight,
    );
  }

  // _scheduleLatestScroll keeps a followed transcript pinned to its newest
  // content. Once the operator has scrolled up to read, a streamed chunk
  // must not pull the view back down. `force` (a fresh hydration, the
  // operator's own follow-up) re-pins unconditionally.
  _scheduleLatestScroll(force = false) {
    if (!force && !this.conversationPinned) return;
    this.conversationPinned = true;
    requestAnimationFrame(() => {
      const el = this.conversationContentEl;
      if (el) el.scrollTop = el.scrollHeight;
    });
  }
}

async function drainResponse(response) {
  const reader = response && response.body && typeof response.body.getReader === "function"
    ? response.body.getReader()
    : null;
  if (!reader) {
    await response.text();
    return;
  }
  while (!(await reader.read()).done) { /* drain without retaining the body */ }
}

function createRequestID() {
  if (globalThis.crypto && typeof globalThis.crypto.randomUUID === "function") {
    return globalThis.crypto.randomUUID();
  }
  return "dashboard-" + Date.now().toString(36) + "-" + Math.random().toString(36).slice(2);
}

function waitFor(milliseconds, signal) {
  return new Promise((resolve, reject) => {
    let timeout;
    const abort = () => {
      clearTimeout(timeout);
      reject(new DOMException("Aborted", "AbortError"));
    };
    const finish = () => {
      if (signal) signal.removeEventListener("abort", abort);
      resolve();
    };
    timeout = setTimeout(finish, milliseconds);
    if (!signal) return;
    if (signal.aborted) {
      abort();
      return;
    }
    signal.addEventListener("abort", abort, { once: true });
  });
}

export const conversationDrawer = new ConversationDrawerStore();

// The shared audit-record cache is the only handoff between the live stream
// and the drawer. React to its compact change marker instead of registering a
// mutable callback on the transport store.
let seenAuditRecordVersion = 0;
$effect.root(() => {
  $effect(() => {
    const changes = auditRecordChangesAfter(
      liveLogs.auditRecordChanges,
      seenAuditRecordVersion,
    );
    if (changes.length === 0) return;
    seenAuditRecordVersion = Number(changes[changes.length - 1].version || 0);
    untrack(() => {
      changes.forEach((change) => {
        if (!isLiveAuditRecordChange(change)) return;
        const entry = liveLogs.auditRecord(change.key);
        conversationDrawer.handleAuditRecordChange(entry, change.eventType);
      });
    });
  });
});
