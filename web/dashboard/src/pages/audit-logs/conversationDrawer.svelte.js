// Interactions drawer state. Pure transcript and branch shaping lives in
// conversation-helpers.js.

import { apiFetch, getJSON, isAbortError } from "$lib/api/client.js";
import { liveLogs } from "./liveLogs.svelte.js";
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
  renderBodyWithConversationHighlights,
} from "./conversation-helpers.js";

const FOLLOW_UP_TIMEOUT_MS = 10 * 60 * 1000;
const FOLLOW_UP_PERSISTENCE_TIMEOUT_MS = 15 * 1000;
const FOLLOW_UP_POLL_INTERVAL_MS = 250;

class ConversationDrawerStore {
  conversationOpen = $state(false);
  conversationLoading = $state(false);
  conversationError = $state("");
  conversationAnchorID = $state("");
  conversationEntries = $state([]);
  conversationMessages = $state([]);
  conversationBranchEntryIDs = $state([]);
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
    this.conversationEntries = [];
    this.conversationMessages = [];
    this.conversationBranchEntryIDs = [];
    this.conversationTruncated = false;
    this.conversationFollowLatest = false;
    this.followUpText = "";
    this.followUpError = "";
    this.followUpRequestID = "";
    document.body.classList.add("conversation-drawer-open");
    requestAnimationFrame(() => this._focusConversationDrawer());

    // A live entry has no persisted row to fetch yet — render it from the
    // live preview data and keep re-rendering as stream events arrive
    // (see refreshLiveConversation).
    if (this._conversationEntryLivePending(entry)) {
      this.conversationFollowLatest = true;
      this.conversationLoading = false;
      this.applyLiveConversationEntry(entry);
      this._scheduleLatestScroll();
      return;
    }
    this.conversationLoading = true;
    await this.fetchConversation(entry.id, requestToken, true, true);
  }

  _conversationEntryLivePending(entry) {
    return typeof liveLogs.auditEntryLiveDetailPending === "function" &&
      liveLogs.auditEntryLiveDetailPending(entry);
  }

  applyLiveConversationEntry(entry) {
    const entries = [...(this.conversationEntries || [])];
    const id = String(entry.id || "").trim();
    const requestID = String(entry.request_id || "").trim();
    const index = entries.findIndex((candidate) =>
      (id && String(candidate.id || "").trim() === id) ||
      (requestID && String(candidate.request_id || "").trim() === requestID));
    if (index >= 0) entries.splice(index, 1, { ...entries[index], ...entry });
    else entries.push(entry);
    this.conversationEntries = entries;
    if (!this.conversationSessionID && entry.session_id) {
      this.conversationSessionID = String(entry.session_id).trim();
    }
    this._applyConversationView(entries);
    if (this.conversationFollowLatest) this._scheduleLatestScroll();
  }

  _applyConversationView(entries) {
    let view = buildConversationView(entries, this.conversationAnchorID);
    if (this.conversationFollowLatest) {
      const latest = latestRenderableConversationEntry(entries, view.entryIDs);
      if (latest && latest.id && String(latest.id) !== this.conversationAnchorID) {
        this.conversationAnchorID = String(latest.id);
        view = buildConversationView(entries, this.conversationAnchorID);
      }
    }
    this.conversationBranchEntryIDs = view.entryIDs;
    this.conversationMessages = view.messages;
  }

  // refreshLiveConversation re-renders an open live conversation when its
  // audit entry merges a new live event. Once the entry is persisted, the
  // full thread (prior turns, final bodies) is hydrated from the store
  // instead.
  refreshLiveConversation(entry) {
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

    const state = String(entry._live_state || "").trim();
    if (state === "audit.flushed" || state === "audit.detail") {
      this.applyLiveConversationEntry(entry);
      if (this.conversationMessages.length === 0) this.conversationLoading = true;
      const requestToken = ++this.conversationRequestToken;
      this.fetchConversation(this.conversationAnchorID || entryID, requestToken, false);
      return;
    }
    this.applyLiveConversationEntry(entry);
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
      ? "Model is responding…"
      : "Waiting for request data…";
  }

  closeConversation() {
    if (this.followUpAbort) this.followUpAbort.abort();
    this.conversationOpen = false;
    this.conversationRequestToken++;
    this.conversationSessionID = "";
    this.conversationBranchEntryIDs = [];
    this.conversationFollowLatest = false;
    this.conversationOpenedFromID = "";
    this.followUpRequestID = "";
    this.followUpSending = false;
    document.body.classList.remove("conversation-drawer-open");
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
        this.conversationError = "Unable to load interactions.";
        this.conversationEntries = [];
        this.conversationMessages = [];
        this.conversationBranchEntryIDs = [];
        return;
      }

      const payload = result.data || {};
      const responseAnchorID = payload.anchor_id || logID;
      this.conversationEntries = Array.isArray(payload.entries) ? payload.entries : [];
      if (detectFollowLatest) {
        this.conversationFollowLatest = conversationEntryIsLatest(this.conversationEntries, responseAnchorID);
      }
      this.conversationAnchorID = responseAnchorID;
      const anchor = this.conversationEntries.find((entry) => entry.id === this.conversationAnchorID);
      if (anchor && anchor.session_id) this.conversationSessionID = String(anchor.session_id).trim();
      this.conversationTruncated = !!payload.truncated;
      this._applyConversationView(this.conversationEntries);
      if (this.conversationFollowLatest) this._scheduleLatestScroll();
      else if (scrollToAnchor) this._scheduleAnchorScroll();
    } catch (e) {
      if (requestToken !== this.conversationRequestToken) return;
      console.error("Failed to fetch audit conversation:", e);
      this.conversationError = "Failed to load interactions.";
      this.conversationEntries = [];
      this.conversationMessages = [];
      this.conversationBranchEntryIDs = [];
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
        let message = "Unable to send message.";
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
        if (timedOut && this.conversationOpen) this.followUpError = "The request timed out.";
      } else {
        console.error("Failed to send interaction follow-up:", error);
        this.followUpError = "Failed to send message.";
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
          this.conversationEntries = entries;
          this.conversationAnchorID = String(child.id);
          this.conversationFollowLatest = false;
          if (this.followUpRequestID === requestID) this.followUpRequestID = "";
          this.conversationTruncated = !!payload.truncated;
          if (child.session_id) this.conversationSessionID = String(child.session_id).trim();
          this._applyConversationView(entries);
          this.conversationFollowLatest = true;
          this._scheduleLatestScroll();
          return;
        }
      }
      await waitFor(FOLLOW_UP_POLL_INTERVAL_MS, signal);
    }
    if (this.conversationOpen && requestToken === this.conversationRequestToken) {
      this.followUpError = "Message sent, but its saved interaction is not available yet.";
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

// Wire the drawer into the live-log merge loop.
liveLogs.refreshLiveConversation = (entry) => conversationDrawer.refreshLiveConversation(entry);
