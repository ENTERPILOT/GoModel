<script>
  // Interactions drawer (class names match the dashboard.css selectors).
  // The drawer is not the Modal atom (different markup: slide-in aside +
  // overlay), so it registers with the modals store itself for the app-shell
  // scroll lock, and closes on Escape only while no other modal sits on top
  // of it.
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import { untrack } from "svelte";
  import { conversationDrawer } from "./conversationDrawer.svelte.js";
  import { modals } from "$lib/stores/ui.svelte.js";
  import { timezone } from "$lib/stores/timezone.svelte.js";

  const drawer = conversationDrawer;

  $effect(() => {
    if (!drawer.conversationOpen) return;
    // untrack: opened() reads AND writes modals.stack; tracking that read
    // would make the effect invalidate itself and loop (effect_update_depth).
    const token = untrack(() => modals.opened());
    const onKeydown = (event) => {
      // openCount includes this drawer; > 1 means another dialog (auth,
      // editor, confirm) is stacked on top and owns the Escape key.
      if (event.key === "Escape" && modals.openCount <= 1) {
        drawer.closeConversation();
      }
    };
    window.addEventListener("keydown", onKeydown);
    return () => {
      modals.closed(token);
      window.removeEventListener("keydown", onKeydown);
    };
  });

  function articleClass(msg) {
    const base =
      msg.role === "function_call" || msg.role === "function_result"
        ? "chat-function-note"
        : "chat-message";
    return [base, msg.roleClass, msg.isAnchor ? "is-anchor" : ""]
      .filter(Boolean)
      .join(" ");
  }

  function functionDetailText(msg) {
    if (msg.role === "function_call") {
      return (msg.toolCalls || []).map((tc) => tc.name + "()").join(", ");
    }
    return (msg.functionName ? msg.functionName + ": " : "") + msg.text;
  }
</script>

{#if drawer.conversationOpen}
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <div
    class="conversation-overlay"
    aria-hidden="true"
    onclick={() => drawer.closeConversation()}
  ></div>
{/if}
<div
  class="conversation-drawer"
  class:open={drawer.conversationOpen}
  bind:this={drawer.conversationDialogEl}
  tabindex="-1"
  role="dialog"
  aria-modal="true"
  aria-hidden={!drawer.conversationOpen}
  aria-labelledby="interactions-drawer-title"
  aria-describedby="interactions-drawer-content"
>
  <div class="conversation-drawer-header">
    <h3 id="interactions-drawer-title">Interactions</h3>
    <DialogCloseButton
      label="Close interactions"
      onclick={() => drawer.closeConversation()}
      bind:el={drawer.conversationCloseBtnEl}
    />
  </div>

  <div id="interactions-drawer-content">
    {#if drawer.conversationError}
      <div class="alert alert-warning">{drawer.conversationError}</div>
    {/if}
    {#if drawer.conversationLoading}
      <p class="empty-state">Loading interactions...</p>
    {/if}
    {#if !drawer.conversationLoading && !drawer.conversationError && drawer.conversationMessages.length === 0 && !drawer.conversationLiveWaiting()}
      <p class="empty-state">No interaction data available for this entry.</p>
    {/if}

    {#if drawer.conversationMessages.length > 0}
      <div class="conversation-thread">
        {#each drawer.conversationMessages as msg (msg.uid)}
          <article class={articleClass(msg)}>
            {#if msg.role === "function_call" || msg.role === "function_result"}
              <details class="chat-function-note-details">
                <summary class="chat-function-note-inner">
                  <span class="chat-function-label">{msg.roleLabel}</span>
                  <span class="chat-function-detail">{functionDetailText(msg)}</span>
                </summary>
                <pre class="chat-function-expanded">{drawer.functionExpandedContent(msg)}</pre>
              </details>
            {:else}
              <header class="chat-message-meta">
                <span class="chat-role">{msg.roleLabel}</span>
                <span class="mono chat-time">{timezone.formatTimestamp(msg.timestamp)}</span>
              </header>
              {#if msg.text}
                <pre class="chat-content">{msg.text}</pre>
              {/if}
              {#if msg.toolCalls}
                <footer class="chat-tool-calls">
                  {#each msg.toolCalls as tc, tcIdx (tc.name + "-" + tcIdx)}
                    <div class="chat-tool-call">
                      <span class="chat-tool-call-name">{tc.name + "()"}</span>
                    </div>
                  {/each}
                </footer>
              {/if}
            {/if}
          </article>
        {/each}
      </div>
    {/if}

    {#if drawer.conversationLiveWaiting()}
      <div class="conversation-live-status" role="status" aria-live="polite">
        <span class="loading-spinner" aria-hidden="true"></span>
        <span>{drawer.conversationLiveStatusText()}</span>
      </div>
    {/if}
  </div>

  {#if drawer.conversationAnchorID}
    <div class="conversation-drawer-footer">
      <p class="conversation-meta">{"Opened from log: " + drawer.conversationAnchorID}</p>
    </div>
  {/if}
</div>
