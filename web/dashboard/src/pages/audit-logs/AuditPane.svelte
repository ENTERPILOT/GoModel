<script>
  // One request/response pane inside an expanded audit entry. `pane` is an
  // object built by audit-logic.js.
  import { createCopyState } from "$lib/utils/clipboard.svelte.js";
  import { formatJSON } from "./audit-logic.js";
  import { conversationDrawer } from "./conversationDrawer.svelte.js";
  import { isAudioBody, renderAudioBody } from "./conversation-helpers.js";

  let { pane } = $props();

  const copyBodyState = createCopyState({
    logPrefix: "Failed to copy audit payload:",
  });
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
  {#if pane.showErrorMessage}
    <div class="audit-pane-block audit-pane-block-error">
      <h5>Error Message</h5>
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
        <h5>{pane.headersTitle || "Headers"}</h5>
        <button
          type="button"
          class="copy-feedback-btn audit-copy-btn"
          class:copy-feedback-btn-copied={copyHeadersState.copied}
          onclick={(event) => {
            event.preventDefault();
            copyHeadersState.copy(pane.copyHeaders, formatJSON);
          }}
        >
          {#if !copyHeadersState.copied}
            <svg
              width="14"
              height="14"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
              stroke-linecap="round"
              stroke-linejoin="round"
              ><rect x="9" y="9" width="13" height="13" rx="2" /><path
                d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"
              /></svg
            >
          {:else}
            <svg
              width="14"
              height="14"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2.5"
              stroke-linecap="round"
              stroke-linejoin="round"
              ><circle cx="12" cy="12" r="10" /><path
                d="M8 12l3 3 5-5"
              /></svg
            >
          {/if}
          <span aria-live="polite" aria-atomic="true"
            >{copyHeadersState.error
              ? "Copy failed"
              : copyHeadersState.copied
                ? "Copied"
                : "Copy Headers"}</span
          >
        </button>
      </div>
      <pre class="audit-json">{formattedHeaders}</pre>
    </div>
  {/if}
  {#if pane.showBody}
    <div class="audit-pane-block audit-pane-block-body">
      <div class="audit-pane-block-head">
        <div class="audit-pane-block-title">
          <h5>Body</h5>
          {#if pane.bodyCacheRatioLabel}
            <span class="audit-prompt-cache-pill mono"
              >{pane.bodyCacheRatioLabel}</span
            >
          {/if}
          {#if pane.streaming}
            <span class="audit-pane-streaming">
              <span class="live-dot is-streaming" aria-hidden="true"></span>
              <span>streaming</span>
            </span>
          {/if}
        </div>
        <button
          type="button"
          class="copy-feedback-btn audit-copy-btn"
          class:copy-feedback-btn-copied={copyBodyState.copied}
          onclick={(event) => {
            event.preventDefault();
            copyBodyState.copy(pane.copyBody, formatJSON);
          }}
        >
          {#if !copyBodyState.copied}
            <svg
              width="14"
              height="14"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
              stroke-linecap="round"
              stroke-linejoin="round"
              ><rect x="9" y="9" width="13" height="13" rx="2" /><path
                d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"
              /></svg
            >
          {:else}
            <svg
              width="14"
              height="14"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2.5"
              stroke-linecap="round"
              stroke-linejoin="round"
              ><circle cx="12" cy="12" r="10" /><path
                d="M8 12l3 3 5-5"
              /></svg
            >
          {/if}
          <span aria-live="polite" aria-atomic="true"
            >{copyBodyState.error
              ? "Copy failed"
              : copyBodyState.copied
                ? "Copied"
                : "Copy Body"}</span
          >
        </button>
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
