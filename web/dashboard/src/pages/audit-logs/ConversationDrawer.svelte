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

<style>
  .conversation-overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.3);
    z-index: 50;
  }

  .conversation-drawer-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    padding: 14px 16px;
    border-bottom: 1px solid var(--border);
  }

  .conversation-drawer-header :global(h3) {
    font-size: 16px;
    font-weight: 700;
  }

  .conversation-meta {
    color: var(--text-muted);
    font-size: 12px;
    font-family: "SF Mono", Menlo, Consolas, monospace;
  }

  .conversation-drawer-footer {
    flex-shrink: 0;
    border-top: 1px solid var(--border);
    padding: 10px 16px;
    background: var(--bg-surface);
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

  .chat-message {
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 10px 12px;
    max-width: 94%;
    background: var(--bg);
  }

  .chat-message.is-anchor {
    border-color: color-mix(in srgb, var(--accent) 55%, var(--border));
    box-shadow: 0 0 0 1px color-mix(in srgb, var(--accent) 25%, transparent);
  }

  .chat-message.role-user {
    align-self: flex-start;
  }

  .chat-message.role-assistant {
    align-self: flex-end;
    background: color-mix(in srgb, var(--accent) 18%, var(--bg));
  }

  .chat-message.role-system {
    align-self: center;
    width: 100%;
    max-width: 100%;
    background: color-mix(in srgb, var(--warning) 10%, var(--bg));
  }

  .chat-message.role-error {
    align-self: flex-end;
    border-color: color-mix(in srgb, var(--danger) 55%, var(--border));
    background: color-mix(in srgb, var(--danger) 10%, var(--bg));
  }

  .chat-message.role-error .chat-role {
    color: color-mix(in srgb, var(--danger) 75%, var(--text-muted));
  }

  .chat-message-meta {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 6px;
  }

  .chat-role {
    font-size: 12px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.4px;
    color: var(--text-muted);
  }

  .chat-time {
    font-size: 11px;
    color: var(--text-muted);
    white-space: nowrap;
  }

  .chat-content {
    font-family:
      Inter,
      -apple-system,
      BlinkMacSystemFont,
      "Segoe UI",
      Roboto,
      sans-serif;
    font-size: 13px;
    line-height: 1.5;
    white-space: pre-wrap;
    overflow-wrap: break-word;
    color: var(--text);
  }

  /* Tool call footer on assistant bubbles */
  .chat-tool-calls {
    margin-top: 8px;
    padding-top: 6px;
    border-top: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 3px;
  }

  .chat-tool-call {
    font-size: 11px;
    color: var(--text-muted);
    font-family: "SF Mono", Menlo, Consolas, monospace;
  }

  .chat-tool-call-name::before {
    content: "\26A1 ";
  }

  /* Function call / result notes between bubbles */
  .chat-function-note {
    align-self: center;
    max-width: 94%;
    padding: 4px 12px;
    border-radius: 12px;
    font-size: 12px;
    color: var(--text-muted);
    background: color-mix(in srgb, var(--border) 40%, transparent);
    border: 1px dashed var(--border);
  }

  .chat-function-note.is-anchor {
    border-color: color-mix(in srgb, var(--accent) 55%, var(--border));
  }

  .chat-function-note-inner {
    display: flex;
    align-items: baseline;
    gap: 6px;
    overflow: hidden;
  }

  .chat-function-label {
    font-weight: 600;
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.3px;
    white-space: nowrap;
    flex-shrink: 0;
  }

  .chat-function-detail {
    font-family: "SF Mono", Menlo, Consolas, monospace;
    font-size: 11px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .chat-function-note.role-function-call {
    align-self: flex-end;
    background: color-mix(in srgb, var(--accent) 8%, transparent);
    border-color: color-mix(in srgb, var(--accent) 30%, var(--border));
  }

  .chat-function-note.role-function-result {
    align-self: flex-start;
    background: color-mix(in srgb, var(--success) 8%, transparent);
    border-color: color-mix(in srgb, var(--success) 30%, var(--border));
  }

  .chat-function-note.is-anchor.role-function-call, .chat-function-note.is-anchor.role-function-result {
    border-color: color-mix(in srgb, var(--accent) 55%, var(--border));
  }

  /* Collapsible function notes */
  .chat-function-note-details {
    width: 100%;
  }

  .chat-function-note-details > :global(summary) {
    list-style: none;
    cursor: pointer;
  }

  .chat-function-note-details > :global(summary::-webkit-details-marker) {
    display: none;
  }

  .chat-function-expanded {
    font-family: "SF Mono", Menlo, Consolas, monospace;
    font-size: 11px;
    line-height: 1.45;
    margin-top: 6px;
    padding-top: 6px;
    border-top: 1px solid var(--border);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    color: var(--text);
    max-height: 200px;
    overflow: auto;
  }

  @media (max-width: 768px) {
    .chat-message {
        max-width: 100%;
      }
  }
</style>
