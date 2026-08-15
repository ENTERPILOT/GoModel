// Interactions drawer state. Pure transcript and branch shaping lives in
// conversation-helpers.js.

import { apiFetch, getJSON, isAbortError } from "$lib/api/client.js";
import * as m from "$lib/paraglide/messages.js";
import { untrack } from "svelte";
import { liveLogs } from "./liveLogs.svelte.js";
import {
  auditRecordChangesAfter,
  auditRecordKey,
  isLiveAuditRecordChange,
} from "./audit-records.js";
import {
  buildConversationView,
  buildFollowUpHeaders,
  buildFollowUpRequest,
  canBuildFollowUpRequest,
  canShowConversation,
  conversationEntryByRequestID,
  conversationEntryIsLatest,
  conversationFollowUpEntry,
  followUpEndpointKind,
  formatJSON,
  latestRenderableConversationEntry,
  matchLiveConversationEntry,
  mergedConversationEntryIDs,
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
  followUpText = $state("");
  followUpSending = $state(false);
  followUpError = $state("");
  followUpRequestID = "";
  followUpAbort = null;

  conversationRequestToken = 0;
  conversationReturnFocusEl = null;
  bodyPointerStart = null;

  conversationDialogEl = $state(null);
  conversationCloseBtnEl = $state(null);
  conversationThreadEl = $state(null);

  get conversationEntries() {
    return (this.conversationEntryIDs || [])
      .map((id) => liveLogs.auditRecord(id))
      .filter(Boolean);
  }

  conversationView = $derived.by(() =>
    buildConversationView(this.conversationEntries, this.conversationAnchorID));

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

  handleBodyConversationClick(event, entry) {
    const wasDrag = this._isBodyDrag(event);
    this.bodyPointerStart = null;
    if (wasDrag) return;
    if (this._hasActiveSelection()) return;
    if (!this.canShowConversation(entry)) return;
    const el = event.target && event.target.closest ? event.target.closest('[data-conversation-trigger="1"]') : null;
    if (!el) return;
    event.preventDefault();
    event.stopPropagation();
    this.openConversation(entry, el);
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

  async openConversation(entry, triggerEl) {
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
    this.followUpText = "";
    this.followUpError = "";
    this.followUpRequestID = "";
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
        this.conversationAnchorID = String(latest.id);
      }
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
      this.conversationAnchorID = entryID;
      this.conversationFollowLatest = true;
    }

    this._addConversationRecord(entry);
    const state = String(eventType || entry._live_state || "").trim();
    // A sibling flush says nothing about whether the mutable selected anchor
    // is queryable. Hydrate only when that anchor itself is persisted.
    if (shouldHydrateConversation(state, entryID, this.conversationAnchorID)) {
      if (this.conversationMessages.length === 0) this.conversationLoading = true;
      const requestToken = ++this.conversationRequestToken;
      void this.fetchConversation(entryID, requestToken, false);
    }
  }

  _addConversationRecord(entry) {
    const id = auditRecordKey(entry);
    if (id && !this.conversationEntryIDs.includes(id)) {
      this._setConversationEntryIDs([...this.conversationEntryIDs, id]);
    }
    if (!this.conversationSessionID && entry.session_id) {
      this.conversationSessionID = String(entry.session_id).trim();
    }
    this._applyConversationView();
    if (this.conversationFollowLatest) this._scheduleLatestScroll();
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
      this.conversationAnchorID = responseAnchorID;
      const anchor = this.conversationEntries.find((entry) => entry.id === this.conversationAnchorID);
      if (anchor && anchor.session_id) this.conversationSessionID = String(anchor.session_id).trim();
      this.conversationTruncated = !!payload.truncated;
      this._applyConversationView();
      if (this.conversationFollowLatest) this._scheduleLatestScroll();
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
        let message = m.interaction_send_unavailable();
        try {
          const payload = await response.json();
          message = payload && payload.error && payload.error.message || message;
        } catch { /* keep the generic message */ }
        this.followUpError = message;
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
          this.conversationAnchorID = String(child.id);
          this.conversationFollowLatest = false;
          if (this.followUpRequestID === requestID) this.followUpRequestID = "";
          this.conversationTruncated = !!payload.truncated;
          if (child.session_id) this.conversationSessionID = String(child.session_id).trim();
          this._applyConversationView();
          this.conversationFollowLatest = true;
          this._scheduleLatestScroll();
          return;
        }
      }
      await waitFor(FOLLOW_UP_POLL_INTERVAL_MS, signal);
    }
    if (this.conversationOpen && requestToken === this.conversationRequestToken) {
      this.followUpError = m.interaction_saved_unavailable();
    }
  }

  _scheduleAnchorScroll() {
    requestAnimationFrame(() => {
      const thread = this.conversationThreadEl;
      if (!thread) return;
      const anchors = thread.querySelectorAll('[data-conversation-anchor="true"]');
      const target = anchors[anchors.length - 1];
      if (target && typeof target.scrollIntoView === "function") {
        target.scrollIntoView({ block: "center" });
      }
    });
  }

  _scheduleLatestScroll() {
    requestAnimationFrame(() => {
      const thread = this.conversationThreadEl;
      if (!thread) return;
      const target = thread.lastElementChild;
      if (target && typeof target.scrollIntoView === "function") {
        target.scrollIntoView({ block: "end" });
      }
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
