// Interactions drawer state singleton (openConversation, closeConversation,
// conversationOpen, conversationLoading, conversationError,
// conversationAnchorID, conversationEntries, conversationMessages,
// conversationLiveEntryId, conversationRequestToken, conversationReturnFocusEl,
// bodyPointerStart, …). Pure message shaping lives in ./conversation-helpers.js.
//
// The drawer imports the liveLogs singleton for the live-state helpers and
// registers itself as the live-conversation sink (refreshLiveConversation),
// so an open live conversation re-renders as stream chunks arrive.

import { getJSON } from "$lib/api/client.js";
import { liveLogs } from "./liveLogs.svelte.js";
import {
  buildConversationMessages,
  canShowConversation,
  formatJSON,
  functionExpandedContent,
  renderBodyWithConversationHighlights,
} from "./conversation-helpers.js";

class ConversationDrawerStore {
  conversationOpen = $state(false);
  conversationLoading = $state(false);
  conversationError = $state("");
  conversationAnchorID = $state("");
  conversationEntries = $state([]);
  conversationMessages = $state([]);
  conversationLiveEntryId = $state("");

  // Non-rendered bookkeeping.
  conversationRequestToken = 0;
  conversationReturnFocusEl = null;
  bodyPointerStart = null;

  // Element refs bound by ConversationDrawer.svelte.
  conversationDialogEl = null;
  conversationCloseBtnEl = null;

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

  // handleErrorConversationClick opens the interactions preview from an
  // error message block. The error text has no conversation segments to
  // highlight, so the whole message acts as the trigger (skipping drags
  // and text selections, like the body handler).
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
    this.conversationEntries = [];
    this.conversationMessages = [];
    document.body.classList.add("conversation-drawer-open");
    requestAnimationFrame(() => this._focusConversationDrawer());

    // A live entry has no persisted row to fetch yet — render it from the
    // live preview data and keep re-rendering as stream events arrive
    // (see refreshLiveConversation).
    if (this._conversationEntryLivePending(entry)) {
      this.conversationLiveEntryId = String(entry.id).trim();
      this.conversationLoading = false;
      this.applyLiveConversationEntry(entry);
      return;
    }
    this.conversationLiveEntryId = "";
    this.conversationLoading = true;
    await this.fetchConversation(entry.id, requestToken);
  }

  // Guarded like every cross-module call: without the live-logs module no
  // entry is ever marked _live, so false is the degraded answer.
  _conversationEntryLivePending(entry) {
    return typeof liveLogs.auditEntryLiveDetailPending === "function" &&
      liveLogs.auditEntryLiveDetailPending(entry);
  }

  applyLiveConversationEntry(entry) {
    this.conversationEntries = [entry];
    this.conversationMessages = this.buildConversationMessages([entry], entry.id);
  }

  // refreshLiveConversation re-renders an open live conversation when its
  // audit entry merges a new live event. Once the entry is persisted, the
  // full thread (prior turns, final bodies) is hydrated from the store
  // instead.
  refreshLiveConversation(entry) {
    if (!this.conversationOpen || !this.conversationLiveEntryId || !entry) return;
    if (String(entry.id || "").trim() !== this.conversationLiveEntryId) return;
    const state = String(entry._live_state || "").trim();
    if (state === "audit.flushed" || state === "audit.detail") {
      this.conversationLiveEntryId = "";
      const requestToken = ++this.conversationRequestToken;
      this.fetchConversation(entry.id, requestToken);
      return;
    }
    this.applyLiveConversationEntry(entry);
  }

  // conversationLiveWaiting reports whether the open live conversation is
  // still waiting on the in-flight request (drives the drawer's spinner).
  conversationLiveWaiting() {
    if (!this.conversationOpen || !this.conversationLiveEntryId) return false;
    const entry = (this.conversationEntries || [])[0];
    if (!entry) return true;
    return typeof liveLogs.liveAuditStateSettled !== "function" ||
      !liveLogs.liveAuditStateSettled(entry._live_state);
  }

  conversationLiveStatusText() {
    return (this.conversationMessages || []).length > 0
      ? "Model is responding…"
      : "Waiting for request data…";
  }

  closeConversation() {
    this.conversationOpen = false;
    this.conversationRequestToken++;
    this.conversationLiveEntryId = "";
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

  async fetchConversation(logID, requestToken) {
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
        return;
      }

      const payload = result.data || {};
      this.conversationAnchorID = payload.anchor_id || logID;
      this.conversationEntries = Array.isArray(payload.entries) ? payload.entries : [];
      this.conversationMessages = this.buildConversationMessages(this.conversationEntries, this.conversationAnchorID);
    } catch (e) {
      if (requestToken !== this.conversationRequestToken) return;
      console.error("Failed to fetch audit conversation:", e);
      this.conversationError = "Failed to load interactions.";
      this.conversationEntries = [];
      this.conversationMessages = [];
    } finally {
      if (requestToken === this.conversationRequestToken) {
        this.conversationLoading = false;
      }
    }
  }

  buildConversationMessages(entries, anchorID) {
    return buildConversationMessages(entries, anchorID);
  }

  functionExpandedContent(msg) {
    return functionExpandedContent(msg);
  }
}

export const conversationDrawer = new ConversationDrawerStore();

// Wire the drawer into the live-log merge loop.
liveLogs.refreshLiveConversation = (entry) => conversationDrawer.refreshLiveConversation(entry);
