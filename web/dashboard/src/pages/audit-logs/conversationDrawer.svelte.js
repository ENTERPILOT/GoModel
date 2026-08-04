// Interactions drawer state. Pure transcript and branch shaping lives in
// conversation-helpers.js.

import { apiFetch, getJSON } from "$lib/api/client.js";
import { liveLogs } from "./liveLogs.svelte.js";
import {
  buildConversationView,
  buildFollowUpHeaders,
  buildFollowUpRequest,
  canShowConversation,
  conversationEntryIsLatest,
  conversationFollowUpEntry,
  followUpEndpointKind,
  formatJSON,
  interactionParentID,
  latestRenderableConversationEntry,
  renderBodyWithConversationHighlights,
} from "./conversation-helpers.js";

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
  followUpParentID = "";

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
    this.followUpParentID = "";
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
    const entrySessionID = String(entry.session_id || "").trim();
    const parentID = interactionParentID(entry);
    const knownEntry = (this.conversationEntries || []).some((candidate) =>
      String(candidate.id || "").trim() === entryID ||
      (entry.request_id && candidate.request_id === entry.request_id));
    const sameSession = !!this.conversationSessionID && entrySessionID === this.conversationSessionID;
    const linkedParent = !!parentID && (parentID === this.conversationAnchorID ||
      (this.conversationEntries || []).some((candidate) => candidate.id === parentID));
    if (!knownEntry && !sameSession && !linkedParent) return;

    if (entryID && parentID === this.followUpParentID) {
      this.conversationAnchorID = entryID;
      this.conversationFollowLatest = true;
      this.followUpParentID = "";
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
    this.conversationOpen = false;
    this.conversationRequestToken++;
    this.conversationSessionID = "";
    this.conversationBranchEntryIDs = [];
    this.conversationFollowLatest = false;
    this.conversationOpenedFromID = "";
    this.followUpParentID = "";
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
    if (!selected || !selected.data || !selected.data.request_body) return "";
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

    this.followUpSending = true;
    this.followUpError = "";
    this.followUpParentID = parentID;
    try {
      const response = await apiFetch(entry.path, {
        method: "POST",
        headers: buildFollowUpHeaders(entry, parentID),
        body: JSON.stringify(body),
      });
      if (!response.ok) {
        let message = "Unable to send message.";
        try {
          const payload = await response.json();
          message = payload && payload.error && payload.error.message || message;
        } catch { /* keep the generic message */ }
        this.followUpError = message;
        if (this.followUpParentID === parentID) this.followUpParentID = "";
        return;
      }
      this.followUpText = "";
      await drainResponse(response);
      if (!liveLogs.liveLogsStreaming && this.conversationOpen && this.conversationAnchorID) {
        const requestToken = ++this.conversationRequestToken;
        await this.fetchConversation(this.conversationAnchorID, requestToken, false);
      }
    } catch (error) {
      console.error("Failed to send interaction follow-up:", error);
      this.followUpError = "Failed to send message.";
      if (this.followUpParentID === parentID) this.followUpParentID = "";
    } finally {
      this.followUpSending = false;
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

export const conversationDrawer = new ConversationDrawerStore();

// Wire the drawer into the live-log merge loop.
liveLogs.refreshLiveConversation = (entry) => conversationDrawer.refreshLiveConversation(entry);
