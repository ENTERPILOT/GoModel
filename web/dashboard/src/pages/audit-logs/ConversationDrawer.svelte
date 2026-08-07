<script>
  // Resizable Interactions side panel. It is a direct child of the app flex
  // shell, so its width is taken from .content instead of covering it.
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { readStored, writeStored } from "$lib/utils/storage.js";
  import { motionDuration } from "$lib/utils/motion.js";
  import { modals } from "$lib/stores/ui.svelte.js";
  import { router } from "$lib/stores/router.svelte.js";
  import { ArrowDown, ArrowUp, Maximize2, Minimize2 } from "lucide";
  import { tick } from "svelte";
  import { cubicOut } from "svelte/easing";
  import { fly, slide } from "svelte/transition";
  import ChatMessage from "./ChatMessage.svelte";
  import { conversationDrawer } from "./conversationDrawer.svelte.js";
  import {
    DEFAULT_CONVERSATION_PANEL_WIDTH,
    clampConversationPanelWidth,
    conversationMessageNavigationIndex,
    conversationPanelBounds,
    conversationPanelWidthFromPointer,
  } from "./conversation-panel.js";

  const drawer = conversationDrawer;
  const storedWidth = Number(readStored("gomodel_interactions_panel_width"));
  const promptCacheFillStorageKey = "gomodel_interactions_prompt_cache_fill";
  let preferredWidth = Number.isFinite(storedWidth) && storedWidth > 0
    ? storedWidth
    : DEFAULT_CONVERSATION_PANEL_WIDTH;
  let panelWidth = $state(preferredWidth);
  let panelMin = $state(320);
  let panelMax = $state(760);
  let fullscreen = $state(false);
  let returningFromFullscreen = $state(false);
  let showPromptCache = $state(readStored(promptCacheFillStorageKey, "true") !== "false");
  let resizePointerID = null;

  function interactionsTransition(node, { fullscreen: isFullscreen, revealWithoutSlide = false }) {
    const duration = motionDuration(180);
    if (isFullscreen) return fly(node, { x: panelWidth, duration, easing: cubicOut });
    if (revealWithoutSlide) return { duration: 0 };
    return slide(node, { axis: "x", duration, easing: cubicOut });
  }

  async function setFullscreen(next) {
    if (next === fullscreen) return;
    const returning = fullscreen && !next;
    returningFromFullscreen = returning;
    fullscreen = next;
    if (returning) {
      await tick();
      returningFromFullscreen = false;
    }
  }

  function togglePromptCacheFill() {
    showPromptCache = !showPromptCache;
    writeStored(promptCacheFillStorageKey, showPromptCache);
  }

  function leadingShellWidth() {
    const sidebarWidth = document.querySelector(".sidebar")
      ?.getBoundingClientRect().width || 0;
    const toggleWidth = document.querySelector(".sidebar-toggle")
      ?.getBoundingClientRect().width || 0;
    return sidebarWidth + toggleWidth;
  }

  function syncPanelWidth() {
    const leading = leadingShellWidth();
    const bounds = conversationPanelBounds(window.innerWidth, leading);
    panelMin = bounds.min;
    panelMax = bounds.max;
    panelWidth = clampConversationPanelWidth(
      preferredWidth,
      window.innerWidth,
      leading,
    );
  }

  function resizeFromPointer(clientX) {
    panelWidth = conversationPanelWidthFromPointer(
      clientX,
      window.innerWidth,
      leadingShellWidth(),
    );
    preferredWidth = panelWidth;
  }

  function startResize(event) {
    if (event.button !== 0) return;
    event.preventDefault();
    resizePointerID = event.pointerId;
    event.currentTarget.setPointerCapture(event.pointerId);
    document.body.classList.add("conversation-panel-resizing");
    resizeFromPointer(event.clientX);
  }

  function dragResize(event) {
    if (event.pointerId !== resizePointerID) return;
    resizeFromPointer(event.clientX);
  }

  function finishResize(event) {
    if (resizePointerID === null ||
        (event.pointerId !== undefined && event.pointerId !== resizePointerID)) return;
    resizePointerID = null;
    document.body.classList.remove("conversation-panel-resizing");
    writeStored("gomodel_interactions_panel_width", preferredWidth);
  }

  function resizeWithKeyboard(event) {
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    event.preventDefault();
    preferredWidth = panelWidth + (event.key === "ArrowLeft" ? 24 : -24);
    syncPanelWidth();
    preferredWidth = panelWidth;
    writeStored("gomodel_interactions_panel_width", preferredWidth);
  }

  function scrollToConversationMessage(direction) {
    const content = document.getElementById("interactions-drawer-content");
    const thread = drawer.conversationThreadEl;
    if (!content || !thread) return;
    const messages = [...thread.querySelectorAll('[data-conversation-message="true"]')];
    const contentRect = content.getBoundingClientRect();
    const contentTop = contentRect.top + 14;
    const index = conversationMessageNavigationIndex(
      messages.map((message) => message.getBoundingClientRect().top),
      contentTop,
      direction,
    );
    const target = messages[index];
    if (!target) return;
    const targetTop = content.scrollTop
      + target.getBoundingClientRect().top
      - contentRect.top
      - 14;
    content.scrollTo({
      top: targetTop,
      behavior: motionDuration(1) > 0 ? "smooth" : "auto",
    });
  }

  function dashboardShellElements() {
    return [
      document.querySelector(".sidebar"),
      document.querySelector(".sidebar-toggle"),
      document.getElementById("dashboard-content"),
    ].filter(Boolean);
  }

  $effect(() => {
    if (!drawer.conversationOpen) return;
    syncPanelWidth();
    const sidebarEl = document.querySelector(".sidebar");
    const sidebarObserver = new ResizeObserver(syncPanelWidth);
    if (sidebarEl) sidebarObserver.observe(sidebarEl);
    const onKeydown = (event) => {
      if (event.key === "Escape" && !modals.anyOpen) {
        if (fullscreen) void setFullscreen(false);
        else drawer.closeConversation();
      }
    };
    window.addEventListener("keydown", onKeydown);
    window.addEventListener("resize", syncPanelWidth);
    return () => {
      finishResize({});
      sidebarObserver.disconnect();
      window.removeEventListener("keydown", onKeydown);
      window.removeEventListener("resize", syncPanelWidth);
    };
  });

  $effect(() => {
    if (!drawer.conversationOpen) fullscreen = false;
  });

  $effect(() => {
    if (!fullscreen) return;
    const shellElements = dashboardShellElements().map((element) => ({
      element,
      inert: element.inert,
      ariaHidden: element.getAttribute("aria-hidden"),
    }));
    shellElements.forEach(({ element }) => {
      element.inert = true;
      element.setAttribute("aria-hidden", "true");
    });
    return () => {
      shellElements.forEach(({ element, inert, ariaHidden }) => {
        element.inert = inert;
        if (ariaHidden === null) element.removeAttribute("aria-hidden");
        else element.setAttribute("aria-hidden", ariaHidden);
      });
    };
  });

  $effect(() => {
    if (router.page !== "audit-logs" && drawer.conversationOpen) {
      drawer.closeConversation();
    }
  });
</script>

{#if drawer.conversationOpen}
{#each [fullscreen] as renderedFullscreen (renderedFullscreen)}
<!-- The keyed block animates fullscreen swaps; global also covers the parent open/close block. -->
<aside
  class="conversation-drawer"
  class:conversation-drawer-fullscreen={renderedFullscreen}
  style:--conversation-panel-width={panelWidth + "px"}
  bind:this={drawer.conversationDialogEl}
  tabindex="-1"
  role={renderedFullscreen ? "dialog" : undefined}
  aria-modal={renderedFullscreen ? "true" : undefined}
  aria-labelledby="interactions-drawer-title"
  transition:interactionsTransition|global={{
    fullscreen: renderedFullscreen,
    revealWithoutSlide: !renderedFullscreen && returningFromFullscreen,
  }}
>
  <!-- A focusable separator is the ARIA window-splitter pattern. Svelte's
       static checker treats separator as non-interactive despite aria-valuenow. -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
  <div
    class="conversation-resize-handle"
    role="separator"
    aria-label="Resize interactions panel"
    aria-orientation="vertical"
    aria-controls="dashboard-content interactions-drawer-content"
    aria-valuemin={panelMin}
    aria-valuemax={panelMax}
    aria-valuenow={panelWidth}
    tabindex="0"
    onpointerdown={startResize}
    onpointermove={dragResize}
    onpointerup={finishResize}
    onpointercancel={finishResize}
    onlostpointercapture={finishResize}
    onkeydown={resizeWithKeyboard}
  ></div>
  <div class="conversation-drawer-header">
    <div class="conversation-drawer-title">
      <h3 id="interactions-drawer-title">Interactions</h3>
      <span
        class="conversation-follow-status"
        role="status"
        aria-label={drawer.conversationFollowLatest
          ? "Following the latest interaction"
          : "Viewing a historical interaction"}
        title={drawer.conversationFollowLatest
          ? "Following the latest interaction as new events arrive"
          : "Viewing a historical interaction; live updates are not followed"}
      >
        <span
          class="live-dot"
          class:is-streaming={drawer.conversationFollowLatest}
          aria-hidden="true"
        ></span>
      </span>
    </div>
    <div class="conversation-drawer-header-actions">
      <button
        type="button"
        class="table-action-btn table-icon-btn"
        aria-label={renderedFullscreen ? "Exit fullscreen interactions" : "Show interactions fullscreen"}
        title={renderedFullscreen ? "Exit fullscreen" : "Fullscreen"}
        aria-pressed={renderedFullscreen}
        onclick={() => setFullscreen(!renderedFullscreen)}
      >
        <Icon icon={renderedFullscreen ? Minimize2 : Maximize2} class="table-icon-svg" />
      </button>
      <DialogCloseButton
        label="Close interactions"
        onclick={() => drawer.closeConversation()}
        bind:el={drawer.conversationCloseBtnEl}
      />
    </div>
  </div>

  <div id="interactions-drawer-content">
    {#if drawer.conversationMessages.length > 1}
      <div class="conversation-message-navigation" role="group" aria-label="Navigate interaction messages">
        <button
          type="button"
          aria-label="Previous message"
          title="Previous message"
          onclick={() => scrollToConversationMessage(-1)}
        >
          <Icon icon={ArrowUp} width="14" height="14" />
        </button>
        <button
          type="button"
          aria-label="Next message"
          title="Next message"
          onclick={() => scrollToConversationMessage(1)}
        >
          <Icon icon={ArrowDown} width="14" height="14" />
        </button>
      </div>
    {/if}
    {#if drawer.conversationError}
      <div class="alert alert-warning">{drawer.conversationError}</div>
    {/if}
    {#if drawer.conversationLoading}
      <p class="empty-state">Loading interactions...</p>
    {/if}
    {#if !drawer.conversationLoading && !drawer.followUpSending && !drawer.conversationError && drawer.conversationMessages.length === 0 && !drawer.conversationLiveWaiting()}
      <p class="empty-state">No interaction data available for this entry.</p>
    {/if}

    {#if drawer.conversationMessages.length > 0}
      {#if drawer.conversationMessages.some((msg) =>
        Number(msg.promptCacheRatio || 0) > 0
      )}
        <button
          type="button"
          class="conversation-cache-legend"
          role="switch"
          aria-checked={showPromptCache}
          title={(showPromptCache ? "Hide" : "Show") + " estimated provider prompt-cache fill"}
          onclick={togglePromptCacheFill}
        >
          <span class="conversation-cache-switch" class:is-active={showPromptCache} aria-hidden="true">
            <span class="conversation-cache-switch-thumb"></span>
          </span>
          <span>Blue fill shows cached prompt share <span class="conversation-cache-estimate">(estimated)</span></span>
        </button>
      {/if}
      <div class="conversation-thread" bind:this={drawer.conversationThreadEl}>
        {#each drawer.conversationMessages as msg (msg.uid)}
          <ChatMessage {msg} {showPromptCache} />
        {/each}
      </div>
    {/if}

    {#if drawer.conversationTruncated}
      <p class="conversation-truncated">Showing the newest part of this session and the selected log.</p>
    {/if}

    {#if drawer.conversationLiveWaiting()}
      <div class="conversation-live-status" role="status" aria-live="polite">
        <span class="loading-spinner" aria-hidden="true"></span>
        <span>{drawer.conversationLiveStatusText()}</span>
      </div>
    {/if}
  </div>

  {#if drawer.conversationAnchorID && drawer.followUpKind()}
    <div class="conversation-drawer-footer">
      <form
        class="conversation-composer"
        onsubmit={(event) => {
          event.preventDefault();
          drawer.sendFollowUp();
        }}
      >
        <textarea
          id="conversation-follow-up"
          rows="2"
          aria-label="Send a message"
          placeholder="Continue this interaction…"
          bind:value={drawer.followUpText}
          disabled={drawer.followUpSending || drawer.conversationLiveWaiting()}
        ></textarea>
        {#if drawer.followUpError}
          <p class="conversation-send-error" role="alert">{drawer.followUpError}</p>
        {/if}
        <div class="conversation-composer-actions">
          <span class="conversation-endpoint mono">{drawer.selectedConversationEntry()?.path || ""}</span>
          <button class="btn btn-primary" type="submit" disabled={!drawer.canSendFollowUp()}>
            {drawer.followUpSending ? "Sending…" : "Send"}
          </button>
        </div>
      </form>
    </div>
  {/if}
</aside>
{/each}
{/if}

<style>
  .conversation-drawer {
    position: sticky;
    top: 0;
    flex: 0 0 auto;
    width: var(--conversation-panel-width);
    height: 100vh;
    min-width: 0;
    background: var(--bg-surface);
    border-left: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    z-index: 12;
  }

  .conversation-drawer-fullscreen {
    position: fixed;
    inset: 0;
    width: 100vw;
    height: 100vh;
    height: 100dvh;
    flex-basis: auto;
    border-left: 0;
    z-index: 70;
  }

  .conversation-drawer-fullscreen .conversation-resize-handle {
    display: none;
  }

  .conversation-resize-handle {
    position: absolute;
    z-index: 2;
    top: 0;
    bottom: 0;
    left: -5px;
    width: 10px;
    padding: 0;
    border: 0;
    border-radius: 0;
    background: transparent;
    cursor: col-resize;
    touch-action: none;
    outline: none;
  }

  .conversation-resize-handle::after {
    content: "";
    position: absolute;
    top: 0;
    bottom: 0;
    left: 4px;
    width: 2px;
    background: transparent;
    transition: background 0.15s;
  }

  .conversation-resize-handle:hover::after,
  .conversation-resize-handle:focus-visible::after {
    background: color-mix(in srgb, var(--accent) 70%, var(--border));
  }

  :global(body.conversation-panel-resizing) {
    cursor: col-resize;
    user-select: none;
  }

  .conversation-drawer-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    padding: 14px 16px;
    border-bottom: 1px solid var(--border);
  }

  .conversation-drawer-header-actions {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .conversation-drawer-title {
    display: flex;
    align-items: center;
    gap: 9px;
    min-width: 0;
  }

  .conversation-follow-status {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 4px;
    margin: -4px;
  }

  .conversation-drawer-header :global(h3) {
    font-size: 16px;
    font-weight: 700;
  }

  #interactions-drawer-content {
    position: relative;
  }

  .conversation-message-navigation {
    position: sticky;
    top: 12px;
    z-index: 4;
    display: flex;
    flex-direction: column;
    gap: 4px;
    width: max-content;
    height: 0;
    margin-left: auto;
    margin-right: 12px;
    opacity: 0;
    pointer-events: none;
    transform: translateY(-3px);
    transition: opacity 120ms ease, transform 120ms ease;
  }

  #interactions-drawer-content:hover .conversation-message-navigation,
  .conversation-message-navigation:focus-within {
    opacity: 1;
    pointer-events: auto;
    transform: translateY(0);
  }

  .conversation-message-navigation button {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 30px;
    height: 30px;
    padding: 0;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: color-mix(in srgb, var(--bg-surface) 94%, transparent);
    color: var(--text-muted);
    box-shadow: 0 1px 3px color-mix(in srgb, #000 16%, transparent);
  }

  .conversation-message-navigation button:hover {
    color: var(--text);
    background: var(--bg-surface-hover);
  }

  .conversation-message-navigation button:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--accent) 45%, transparent);
    outline-offset: 1px;
  }

  .conversation-drawer-footer {
    flex-shrink: 0;
    border-top: 1px solid var(--border);
    padding: 10px 16px;
    background: var(--bg-surface);
    display: flex;
    flex-direction: column;
    gap: 9px;
  }

  .conversation-composer {
    display: flex;
    flex-direction: column;
    gap: 7px;
  }

  .conversation-composer textarea {
    width: 100%;
    min-height: 50px;
    resize: vertical;
    border: 1px solid var(--border);
    border-radius: 7px;
    padding: 9px 10px;
    background: var(--bg);
    color: var(--text);
    font: inherit;
    line-height: 1.45;
  }

  .conversation-composer textarea:focus {
    outline: 2px solid color-mix(in srgb, var(--accent) 38%, transparent);
    outline-offset: 1px;
  }

  .conversation-composer textarea:disabled {
    opacity: 0.65;
  }

  .conversation-composer-actions {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
  }

  .conversation-endpoint {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-muted);
    font-size: 11px;
  }

  .conversation-send-error {
    color: var(--danger);
    font-size: 12px;
  }

  .conversation-truncated {
    padding: 0 16px 14px;
    color: var(--text-muted);
    font-size: 12px;
  }

  .conversation-cache-legend {
    display: flex;
    align-items: center;
    gap: 7px;
    margin: 12px 16px 0;
    padding: 0;
    border: 0;
    background: transparent;
    color: var(--text-muted);
    font-size: 11px;
    font-family: inherit;
    text-align: left;
    cursor: pointer;
  }

  .conversation-cache-legend:hover {
    color: var(--text);
  }

  .conversation-cache-legend:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--prompt-cache-color) 35%, transparent);
    outline-offset: 3px;
    border-radius: 3px;
  }

  .conversation-cache-switch {
    position: relative;
    width: 28px;
    height: 16px;
    flex: 0 0 auto;
    border-radius: 999px;
    background: color-mix(in srgb, var(--text-muted) 30%, var(--border));
    transition: background-color 150ms ease;
  }

  .conversation-cache-switch.is-active {
    background: color-mix(in srgb, var(--prompt-cache-color) 80%, var(--bg));
  }

  .conversation-cache-switch-thumb {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 12px;
    height: 12px;
    border-radius: 50%;
    background: #fff;
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.24);
    transition: transform 150ms ease;
  }

  .conversation-cache-switch.is-active .conversation-cache-switch-thumb {
    transform: translateX(12px);
  }

  .conversation-cache-estimate {
    opacity: 0.75;
  }

  .conversation-thread {
    padding: 14px 16px 20px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .conversation-live-status {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 4px 16px 20px;
    color: var(--text-muted);
    font-size: 13px;
  }

  @media (hover: none) {
    .conversation-message-navigation {
      opacity: 1;
      pointer-events: auto;
      transform: none;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .conversation-message-navigation {
      transition: none;
    }
  }
</style>
