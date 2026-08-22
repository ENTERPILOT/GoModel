<script>
  // Slidable, resizable JSON panel on the right: the request body as it will
  // be sent (live, as the conversation is edited) and the last response.
  import CopyButton from "$lib/components/atoms/CopyButton.svelte";
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import SegmentedControl from "$lib/components/atoms/SegmentedControl.svelte";
  import { createCopyState } from "$lib/utils/clipboard.svelte.js";
  import { motionDuration } from "$lib/utils/motion.js";
  import { readStored, writeStored } from "$lib/utils/storage.js";
  import { untrack } from "svelte";
  import { cubicOut } from "svelte/easing";
  import { slide } from "svelte/transition";
  import * as m from "$lib/paraglide/messages.js";
  import { playgroundStore as store } from "./playground.svelte.js";
  import {
    DEFAULT_JSON_PANEL_WIDTH,
    MIN_JSON_PANEL_WIDTH,
    clampJsonPanelWidth,
    formatJSON,
    maxJsonPanelWidth,
  } from "./playgroundLogic.js";

  const WIDTH_KEY = "gomodel_playground_json_panel_width";

  const initialViewport = typeof window === "undefined" ? 1280 : window.innerWidth;
  let panelWidth = $state(
    clampJsonPanelWidth(readStored(WIDTH_KEY, DEFAULT_JSON_PANEL_WIDTH), initialViewport),
  );
  let panelMax = $state(maxJsonPanelWidth(initialViewport));
  let resizePointerID = null;
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
    writeStored(WIDTH_KEY, panelWidth);
  }

  function resizeWithKeyboard(event) {
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    event.preventDefault();
    panelWidth = clampJsonPanelWidth(
      panelWidth + (event.key === "ArrowLeft" ? 24 : -24),
      window.innerWidth,
    );
    writeStored(WIDTH_KEY, panelWidth);
  }

  $effect(() => {
    if (!store.panelOpen) return;
    const onResize = () => {
      panelMax = maxJsonPanelWidth(window.innerWidth);
      panelWidth = clampJsonPanelWidth(panelWidth, window.innerWidth);
    };
    // The viewport may have changed while the panel was closed (no listener).
    // untrack: the initial clamp reads panelWidth, which must not make this
    // effect re-run (and re-register the listener) on every resize drag.
    untrack(onResize);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  });
</script>

{#if store.panelOpen}
  <aside
    id="playground-json-panel"
    class="playground-json-panel"
    style:--playground-json-panel-width={panelWidth + "px"}
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
        <DialogCloseButton label={m.playground_json_hide()} onclick={() => store.togglePanel()} />
      </div>
    </div>
    <div class="playground-json-body">
      {#if store.panelTab === "request"}
        <p class="playground-json-meta mono">POST {store.endpointPath}</p>
        <pre class="playground-json-code mono">{requestText}</pre>
      {:else if store.sending && store.response === null}
        <p class="playground-json-placeholder">{m.playground_json_streaming()}</p>
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

  @media (max-width: 768px) {
    .playground-json-panel {
      position: fixed;
      inset: 0 0 0 auto;
      width: min(100vw, var(--playground-json-panel-width, 420px));
      border-radius: 0;
      z-index: 30;
    }
  }
</style>
