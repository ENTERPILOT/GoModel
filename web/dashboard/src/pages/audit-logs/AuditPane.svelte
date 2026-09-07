<script>
  // One request/response pane inside an expanded audit entry. `pane` is an
  // object built by audit-logic.js.
  import CopyButton from "$lib/components/atoms/CopyButton.svelte";
  import { createCopyState } from "$lib/utils/clipboard.svelte.js";
  import { formatJSON } from "./audit-logic.js";
  import { conversationDrawer } from "./conversationDrawer.svelte.js";
  import {
    isAudioBody,
    isImageBody,
    renderAudioBody,
    renderImageBody,
  } from "./conversation-helpers.js";
  import * as m from "$lib/paraglide/messages.js";

  let { pane } = $props();

  const copyBodyState = createCopyState({
    logPrefix: "Failed to copy audit payload:",
  });
  const copyModelState = createCopyState({
    logPrefix: "Failed to copy model name:",
  });

  // The model-strip chips are buttons whose activation copies the full model
  // name, so the complete value is reachable by keyboard and touch (the
  // visual text may be ellipsized).
  function copyModelValue(value) {
    copyModelState.copy(value, (v) => v);
  }
  const copyHeadersState = createCopyState({
    logPrefix: "Failed to copy audit payload:",
  });

  const formattedHeaders = $derived(
    pane && pane.showHeaders ? formatJSON(pane.headers) : "",
  );

  const renderedBody = $derived.by(() => {
    if (!pane || !pane.showBody) return "";
    if (isAudioBody(pane.body)) {
      return renderAudioBody(pane.body);
    }
    if (isImageBody(pane.body)) {
      return renderImageBody(pane.body);
    }
    return conversationDrawer.renderBodyWithConversationHighlights(
      pane.entry,
      pane.body,
      { promptCacheHighlight: pane.promptCacheHighlight },
    );
  });

  const errorClickable = $derived(
    !!(pane && conversationDrawer.canShowConversation(pane.entry)),
  );

  function onErrorKeydown(event) {
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    conversationDrawer.handleErrorConversationClick(event, pane.entry);
  }
</script>

<section
  class="audit-pane"
  class:audit-pane-split={pane && pane.layout === "split"}
  class:audit-pane-split-single={pane &&
    pane.layout === "split" &&
    !(pane.showHeaders && pane.showBody)}
>
  {#if pane.modelStrip}
    <div class="audit-pane-model-strip">
      {#if pane.modelStrip.source}
        <!-- Chip is a button so the full value is keyboard-focusable and
             touch-operable: activate copies the complete model name. -->
        <button
          type="button"
          class="audit-pane-model-strip-chip"
          title={pane.modelStrip.source}
          aria-label={m.audit_model_strip_copy_label({
            label: pane.modelStrip.label,
            model: pane.modelStrip.source,
          })}
          onclick={() => copyModelValue(pane.modelStrip.source)}
        >
          <span class="audit-pane-model-strip-label">{pane.modelStrip.label}</span>
          <span class="mono">{pane.modelStrip.source}</span>
        </button>
        <span class="audit-pane-model-strip-arrow" aria-hidden="true">→</span>
      {/if}
      <button
        type="button"
        class="audit-pane-model-strip-chip"
        title={pane.modelStrip.tried}
        aria-label={m.audit_model_strip_copy_label({
          label: m.audit_model_strip_tried(),
          model: pane.modelStrip.tried,
        })}
        onclick={() => copyModelValue(pane.modelStrip.tried)}
      >
        <span class="audit-pane-model-strip-label">{m.audit_model_strip_tried()}</span>
        <span class="mono">{pane.modelStrip.tried}</span>
      </button>
    </div>
  {/if}
  {#if pane.showErrorMessage}
    <div class="audit-pane-block audit-pane-block-error">
      <h5>{m.audit_error_message_title()}</h5>
      <!-- Clickable error preview opens the Interactions drawer. -->
      <!-- svelte-ignore a11y_no_noninteractive_element_to_interactive_role, a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
      <pre
        class="audit-json audit-pane-error-message"
        class:audit-pane-clickable-preview={errorClickable}
        role={errorClickable ? "button" : null}
        tabindex={errorClickable ? 0 : null}
        onmousedown={(event) => conversationDrawer.startBodyInteraction(event)}
        onkeydown={onErrorKeydown}
        onclick={(event) =>
          conversationDrawer.handleErrorConversationClick(
            event,
            pane.entry,
          )}>{pane.errorMessage}</pre>
    </div>
  {/if}
  {#if pane.showHeaders}
    <div class="audit-pane-block audit-pane-block-headers">
      <div class="audit-pane-block-head">
        <h5>{pane.headersTitle || m.audit_headers_title()}</h5>
        <CopyButton
          state={copyHeadersState}
          label={m.audit_copy_headers()}
          errorLabel={m.common_copy_failed()}
          class="audit-copy-btn"
          onclick={() => copyHeadersState.copy(pane.copyHeaders, formatJSON)}
        />
      </div>
      <pre class="audit-json">{formattedHeaders}</pre>
    </div>
  {/if}
  {#if pane.showBody}
    <div class="audit-pane-block audit-pane-block-body">
      <div class="audit-pane-block-head">
        <div class="audit-pane-block-title">
          <h5>{m.audit_body_title()}</h5>
          {#if pane.bodyCacheRatioLabel}
            <span class="audit-prompt-cache-pill mono"
              >{pane.bodyCacheRatioLabel}</span
            >
          {/if}
          {#if pane.streaming}
            <span class="audit-pane-streaming">
              <span class="live-dot is-streaming" aria-hidden="true"></span>
              <span>{m.audit_streaming()}</span>
            </span>
          {/if}
        </div>
        <CopyButton
          state={copyBodyState}
          label={m.audit_copy_body()}
          errorLabel={m.common_copy_failed()}
          class="audit-copy-btn"
          onclick={() => copyBodyState.copy(pane.copyBody, formatJSON)}
        />
      </div>
      <!-- Clicking a highlighted body snippet opens the Interactions drawer
           (drawer decides via selection state). -->
      <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
      <pre
        class="audit-json audit-json-body"
        onmousedown={(event) => conversationDrawer.startBodyInteraction(event)}
        onclick={(event) =>
          conversationDrawer.handleBodyConversationClick(
            event,
            pane.entry,
            pane.requestStep,
          )}>{@html renderedBody}</pre>
    </div>
  {/if}
  {#if pane.showEmpty}
    <p class="empty-state audit-pane-empty">{pane.emptyMessage}</p>
  {/if}
  {#if pane.showPending}
    <p class="empty-state audit-pane-empty audit-pane-pending">
      <span class="loading-spinner" aria-hidden="true"></span>
      <span>{pane.pendingMessage}</span>
    </p>
  {/if}
  {#if pane.showTooLarge}
    <p class="audit-size-warning">{pane.tooLargeMessage}</p>
  {/if}
</section>

<style>
  /* Request and response panes place Headers (1/3) in the first column and
     Body (2/3) in the second; full-width rows span both columns, and a pane
     with only one of the two stretches it to fill. */
  .audit-pane-split {
    display: grid;
    grid-template-columns: 1fr 2fr;
    gap: 10px 14px;
    align-items: start;
  }

  /* Virtual → tried model strip at the top of a failover attempt pane: which
     virtual model leg this attempt belongs to and which concrete model was
     actually called. Chips stay on one line, ellipsized when long. */
  .audit-pane-model-strip {
    grid-column: 1 / -1;
    display: inline-flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px;
    margin-bottom: 2px;
  }

  .audit-pane-model-strip-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    max-width: 320px;
    padding: 3px 9px;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--bg-surface);
    font-family: inherit;
    font-size: 11px;
    text-align: left;
    appearance: none;
    cursor: pointer;
  }

  .audit-pane-model-strip-chip:hover {
    background: color-mix(in srgb, var(--text) 6%, var(--bg-surface));
    border-color: color-mix(in srgb, var(--border) 60%, var(--text) 40%);
  }

  .audit-pane-model-strip-chip .mono {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 11px;
  }

  .audit-pane-model-strip-label {
    flex: 0 0 auto;
    color: var(--text-muted);
    font-size: 10px;
    font-weight: 700;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }

  .audit-pane-model-strip-arrow {
    color: var(--text-muted);
    font-size: 12px;
  }

  .audit-pane-split-single {
    grid-template-columns: minmax(0, 1fr);
  }

  @container dashboard-content (max-width: 699px) {
    .audit-pane-split {
      grid-template-columns: minmax(0, 1fr);
    }
  }

  .audit-pane-split .audit-pane-block-error, .audit-pane-split .audit-pane-empty, .audit-pane-split .audit-size-warning {
    grid-column: 1 / -1;
  }

  .audit-pane-block {
    min-width: 0;
  }

  .audit-pane-block > :global(h5) {
    margin-bottom: 6px;
  }

  .audit-pane-block-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin-bottom: 6px;
  }

  .audit-pane-block-title {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }

  /* The modifier class rides on the CopyButton child's own <button>. */
  .audit-pane-block-head :global(.audit-copy-btn) {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    flex: 0 0 auto;
    padding: 4px 8px;
    background-color: var(--bg-surface);
    border: 1px solid color-mix(in srgb, var(--border) 70%, var(--text) 30%);
    border-radius: 6px;
    color: var(--text);
    cursor: pointer;
    font-family: inherit;
    font-size: 12px;
    transition:
      background-color 0.15s,
      border-color 0.15s,
      color 0.15s;
  }

  .audit-pane-block-head :global(.audit-copy-btn:hover:not(:disabled)) {
    background: color-mix(in srgb, var(--bg-surface) 80%, var(--text) 20%);
    border-color: color-mix(in srgb, var(--border) 45%, var(--text) 55%);
  }

  .audit-pane-block-head :global(.audit-copy-btn.copy-feedback-btn-copied) {
    background: color-mix(in srgb, var(--success) 18%, var(--bg-surface));
  }

  .audit-json {
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: 6px;
    box-sizing: border-box;
    font-family: "SF Mono", Menlo, Consolas, monospace;
    font-size: 12px;
    line-height: 1.45;
    max-width: 100%;
    padding: 10px;
    max-height: 220px;
    overflow-x: auto;
    overflow-y: auto;
    white-space: pre;
    color: var(--text);
  }

  .audit-pane-error-message {
    color: var(--danger);
  }

  .audit-pane-clickable-preview {
    cursor: pointer;
  }

  .audit-pane-clickable-preview:hover {
    background: color-mix(in srgb, var(--danger) 8%, transparent);
  }

  .audit-json-body {
    white-space: pre;
    overflow-wrap: normal;
  }

  .audit-pane-empty {
    text-align: left;
    padding: 8px 0 0;
  }

  .audit-pane-pending {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .audit-pane-streaming {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    padding-left: 4px;
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.02em;
    color: var(--text-muted);
  }

  .audit-size-warning {
    margin-top: 8px;
    color: var(--warning);
    font-size: 12px;
  }
</style>
