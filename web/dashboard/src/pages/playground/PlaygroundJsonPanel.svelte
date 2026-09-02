<script>
  // Slidable, resizable JSON panel on the right: the request body as it will
  // be sent (live, as the conversation is edited) and the last response.
  import CopyButton from "$lib/components/atoms/CopyButton.svelte";
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import SegmentedControl from "$lib/components/atoms/SegmentedControl.svelte";
  import { modals } from "$lib/stores/ui.svelte.js";
  import { createCopyState } from "$lib/utils/clipboard.svelte.js";
  import { motionDuration } from "$lib/utils/motion.js";
  import { readStored, writeStored } from "$lib/utils/storage.js";
  import { cubicOut } from "svelte/easing";
  import { slide } from "svelte/transition";
  import * as m from "$lib/paraglide/messages.js";
  import { playgroundStore as store } from "./playground.svelte.js";
  import {
    DEFAULT_JSON_PANEL_WIDTH,
    JSON_PANEL_FULLSCREEN_MAX_VIEWPORT,
    MIN_JSON_PANEL_WIDTH,
    clampJsonPanelWidth,
    formatJSON,
    maxJsonPanelWidth,
  } from "./playgroundLogic.js";

  const WIDTH_KEY = "gomodel_playground_json_panel_width";

  const initialViewport = typeof window === "undefined" ? 1280 : window.innerWidth;
  // preferredWidth is the width the user chose (persisted); panelWidth is
  // that width clamped to the current viewport, so a narrow window does not
  // permanently shrink the preference.
  let preferredWidth = Number(readStored(WIDTH_KEY, DEFAULT_JSON_PANEL_WIDTH)) || DEFAULT_JSON_PANEL_WIDTH;
  let panelWidth = $state(clampJsonPanelWidth(preferredWidth, initialViewport));
  let panelMax = $state(maxJsonPanelWidth(initialViewport));
  let resizePointerID = null;
  // True while the panel covers the whole viewport (phone widths, see the
  // media query below). It then behaves as a modal: the page behind it is
  // inert, focus moves into it, and Escape closes it.
  let coversViewport = $state(false);
  let closeButtonEl = $state(null);
  const copyState = createCopyState({ logPrefix: "Failed to copy playground JSON:" });

  const tabOptions = $derived([
    { value: "request", label: m.playground_json_request() },
    { value: "response", label: m.playground_json_response() },
  ]);

  const requestText = $derived(formatJSON(store.requestBody));
  const responseText = $derived(formatJSON(store.response));
  const shownText = $derived(store.panelTab === "request" ? requestText : responseText);

  function metaParts(meta) {
    if (!meta) return [];
    const parts = [];
    if (meta.status) parts.push(m.playground_meta_status({ status: meta.status }));
    parts.push(m.playground_meta_duration({ seconds: (meta.durationMs / 1000).toFixed(2) }));
    if (meta.usage) {
      parts.push(m.playground_meta_tokens({ input: meta.usage.input, output: meta.usage.output }));
    }
    if (meta.streamed) parts.push(m.playground_meta_events({ count: meta.events }));
    return parts;
  }

  function resizeFromPointer(clientX) {
    panelWidth = clampJsonPanelWidth(window.innerWidth - clientX, window.innerWidth);
    preferredWidth = panelWidth;
  }

  function startResize(event) {
    if (event.button !== 0) return;
    event.preventDefault();
    resizePointerID = event.pointerId;
    event.currentTarget.setPointerCapture(event.pointerId);
    resizeFromPointer(event.clientX);
  }

  function dragResize(event) {
    if (event.pointerId !== resizePointerID) return;
    resizeFromPointer(event.clientX);
  }

  function finishResize(event) {
    if (resizePointerID === null || event.pointerId !== resizePointerID) return;
    resizePointerID = null;
    writeStored(WIDTH_KEY, preferredWidth);
  }

  function resizeWithKeyboard(event) {
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    event.preventDefault();
    panelWidth = clampJsonPanelWidth(
      panelWidth + (event.key === "ArrowLeft" ? 24 : -24),
      window.innerWidth,
    );
    preferredWidth = panelWidth;
    writeStored(WIDTH_KEY, preferredWidth);
  }

  $effect(() => {
    if (!store.panelOpen) return;
    const phoneViewport = window.matchMedia(
      "(max-width: " + JSON_PANEL_FULLSCREEN_MAX_VIEWPORT + "px)",
    );
    const sync = () => {
      coversViewport = phoneViewport.matches;
    };
    sync();
    phoneViewport.addEventListener("change", sync);
    return () => {
      phoneViewport.removeEventListener("change", sync);
      coversViewport = false;
    };
  });

  $effect(() => {
    if (!coversViewport) return;
    const shellElements = [
      document.querySelector(".sidebar"),
      document.querySelector(".sidebar-toggle"),
      document.querySelector(".playground-main"),
    ]
      .filter(Boolean)
      .map((element) => ({ element, inert: element.inert }));
    shellElements.forEach(({ element }) => {
      element.inert = true;
    });
    const returnFocusEl = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    requestAnimationFrame(() => closeButtonEl?.focus());
    const onKeydown = (event) => {
      if (event.key === "Escape" && !modals.anyOpen) store.togglePanel();
    };
    window.addEventListener("keydown", onKeydown);
    return () => {
      window.removeEventListener("keydown", onKeydown);
      shellElements.forEach(({ element, inert }) => {
        element.inert = inert;
      });
      if (returnFocusEl && document.contains(returnFocusEl)) returnFocusEl.focus();
    };
  });

  $effect(() => {
    if (!store.panelOpen) return;
    // Re-clamps the committed preference (a plain variable, so this effect
    // depends on panelOpen only) whenever the viewport changes.
    const onResize = () => {
      panelMax = maxJsonPanelWidth(window.innerWidth);
      panelWidth = clampJsonPanelWidth(preferredWidth, window.innerWidth);
    };
    // The viewport may have changed while the panel was closed (no listener).
    onResize();
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  });
</script>

{#if store.panelOpen}
  <aside
    id="playground-json-panel"
    class="playground-json-panel"
    style:--playground-json-panel-width={panelWidth + "px"}
    role={coversViewport ? "dialog" : undefined}
    aria-modal={coversViewport ? "true" : undefined}
    aria-labelledby="playground-json-title"
    transition:slide={{ axis: "x", duration: motionDuration(180), easing: cubicOut }}
  >
    <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
    <div
      class="playground-json-resize-handle"
      role="separator"
      aria-label={m.playground_resize_label()}
      aria-orientation="vertical"
      aria-valuemin={MIN_JSON_PANEL_WIDTH}
      aria-valuemax={panelMax}
      aria-valuenow={panelWidth}
      tabindex="0"
      onpointerdown={startResize}
      onpointermove={dragResize}
      onpointerup={finishResize}
      onpointercancel={finishResize}
      onkeydown={resizeWithKeyboard}
    ></div>
    <div class="playground-json-header">
      <h3 id="playground-json-title">{m.playground_json_title()}</h3>
      <SegmentedControl
        options={tabOptions}
        value={store.panelTab}
        ariaLabel={m.playground_json_title()}
        onchange={(value) => {
          store.panelTab = value;
        }}
      />
      <div class="playground-json-actions">
        <CopyButton
          state={copyState}
          label={m.playground_copy()}
          copiedLabel={m.common_copied()}
          errorLabel={m.common_copy_failed()}
          onclick={() => copyState.copy(shownText)}
        />
        <DialogCloseButton
          label={m.playground_json_hide()}
          bind:el={closeButtonEl}
          onclick={() => store.togglePanel()}
        />
      </div>
    </div>
    <!-- A scrollable region needs a tab stop so keyboard users can scroll it. -->
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <div
      class="playground-json-body"
      role="region"
      tabindex="0"
      aria-label={store.panelTab === "request" ? m.playground_json_request() : m.playground_json_response()}
    >
      {#if store.panelTab === "request"}
        <p class="playground-json-meta mono">POST {store.endpointPath}</p>
        <pre class="playground-json-code mono">{requestText}</pre>
      {:else if store.sending && store.response === null}
        <p class="playground-json-placeholder">
          {store.sendingStream ? m.playground_json_streaming() : m.playground_sending()}
        </p>
      {:else if store.response === null}
        <p class="playground-json-placeholder">{m.playground_json_empty_response()}</p>
      {:else}
        {#if store.responseMeta}
          <p class="playground-json-meta mono">{metaParts(store.responseMeta).join(" · ")}</p>
        {/if}
        <pre class="playground-json-code mono">{responseText}</pre>
      {/if}
    </div>
  </aside>
{/if}

<style>
  .playground-json-panel {
    position: relative;
    display: flex;
    flex: 0 0 var(--playground-json-panel-width, 420px);
    flex-direction: column;
    width: var(--playground-json-panel-width, 420px);
    min-height: 0;
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }

  .playground-json-resize-handle {
    position: absolute;
    top: 0;
    bottom: 0;
    left: 0;
    width: 8px;
    cursor: col-resize;
    touch-action: none;
    z-index: 1;
  }

  .playground-json-resize-handle:hover,
  .playground-json-resize-handle:focus-visible {
    background: color-mix(in srgb, var(--accent) 35%, transparent);
    outline: none;
  }

  .playground-json-header {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px;
    padding: 12px 14px;
    border-bottom: 1px solid var(--border);
  }

  .playground-json-header h3 {
    font-size: 14px;
    font-weight: 700;
  }

  .playground-json-actions {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-left: auto;
  }

  .playground-json-actions :global(.copy-feedback-btn) {
    padding: 5px 10px;
    font-size: 12px;
  }

  .playground-json-body {
    flex: 1 1 0;
    min-height: 0;
    padding: 12px 14px;
    overflow: auto;
  }

  .playground-json-meta {
    margin-bottom: 10px;
    color: var(--text-muted);
    font-size: 11px;
  }

  .playground-json-placeholder {
    color: var(--text-muted);
    font-size: 13px;
  }

  .playground-json-code {
    margin: 0;
    color: var(--text);
    font-size: 12px;
    line-height: 1.55;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }

  /* Mobile: the panel takes the whole viewport. A partial overlay left a
     sliver of the page peeking out that was neither readable nor tappable,
     and dragging the edge is not a phone gesture. */
  @media (max-width: 768px) {
    .playground-json-panel {
      position: fixed;
      inset: 0;
      width: 100vw;
      height: 100dvh;
      border: 0;
      border-radius: 0;
      z-index: 30;
      padding: env(safe-area-inset-top, 0px) env(safe-area-inset-right, 0px)
        env(safe-area-inset-bottom, 0px) env(safe-area-inset-left, 0px);
    }

    .playground-json-resize-handle {
      display: none;
    }

    .playground-json-header {
      flex-wrap: nowrap;
    }

    .playground-json-header :global(.segmented-control) {
      min-width: 0;
    }

    .playground-json-actions {
      flex-shrink: 0;
    }
  }
</style>
