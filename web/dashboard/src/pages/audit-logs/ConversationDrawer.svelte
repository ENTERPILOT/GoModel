<script>
  // Interactions drawer (class names match the dashboard.css selectors).
  // The drawer is not the Modal atom (different markup: slide-in aside +
  // overlay), so it registers with the modals store itself for the app-shell
  // scroll lock, and closes on Escape only while no other modal sits on top
  // of it.
  import DialogCloseButton from "$lib/components/atoms/DialogCloseButton.svelte";
  import { untrack } from "svelte";
  import ChatMessage from "./ChatMessage.svelte";
  import { conversationDrawer } from "./conversationDrawer.svelte.js";
  import { modals } from "$lib/stores/ui.svelte.js";

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
    {#if !drawer.conversationLoading && !drawer.followUpSending && !drawer.conversationError && drawer.conversationMessages.length === 0 && !drawer.conversationLiveWaiting()}
      <p class="empty-state">No interaction data available for this entry.</p>
    {/if}

    {#if drawer.conversationMessages.length > 0}
      <div class="conversation-thread" bind:this={drawer.conversationThreadEl}>
        {#each drawer.conversationMessages as msg (msg.uid)}
          <ChatMessage {msg} />
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

  {#if drawer.conversationAnchorID}
    <div class="conversation-drawer-footer">
      {#if drawer.followUpKind()}
        <form
          class="conversation-composer"
          onsubmit={(event) => {
            event.preventDefault();
            drawer.sendFollowUp();
          }}
        >
          <label for="conversation-follow-up">Send a message</label>
          <textarea
            id="conversation-follow-up"
            rows="3"
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
      {/if}
      <p class="conversation-meta">
        {drawer.conversationFollowLatest
          ? "Following latest interaction"
          : "Opened from log: " + drawer.conversationOpenedFromID}
      </p>
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
    display: flex;
    flex-direction: column;
    gap: 9px;
  }

  .conversation-composer {
    display: flex;
    flex-direction: column;
    gap: 7px;
  }

  .conversation-composer label {
    font-size: 12px;
    font-weight: 700;
  }

  .conversation-composer textarea {
    width: 100%;
    min-height: 70px;
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
</style>
