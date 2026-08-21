<script>
  // Bottom composer: the prompt textarea and the Send/Stop button. Enter
  // sends, Shift+Enter inserts a newline. Sending with an empty prompt
  // re-runs the edited conversation as-is.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { Send, Square } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
  import { playgroundStore as store } from "./playground.svelte.js";

  function onKeydown(event) {
    if (event.key !== "Enter" || event.shiftKey || event.isComposing) return;
    event.preventDefault();
    if (store.canSend) store.send();
  }
</script>

<form
  class="playground-composer"
  onsubmit={(event) => {
    event.preventDefault();
    if (store.sending) store.stop();
    else store.send();
  }}
>
  <textarea
    class="playground-composer-input"
    rows="2"
    aria-label={m.playground_composer_label()}
    placeholder={m.playground_composer_placeholder()}
    bind:value={store.draft}
    onkeydown={onKeydown}
    disabled={store.sending}
  ></textarea>
  <div class="playground-composer-actions">
    <span class="playground-composer-hint">{m.playground_help()}</span>
    {#if store.sending}
      <button type="submit" class="btn btn-danger-outline btn-with-icon">
        <Icon icon={Square} class="table-icon-svg" />
        <span>{m.playground_stop()}</span>
      </button>
    {:else}
      <button type="submit" class="btn btn-primary btn-with-icon" disabled={!store.canSend}>
        <Icon icon={Send} class="table-icon-svg" />
        <span>{m.common_action_send()}</span>
      </button>
    {/if}
  </div>
</form>

<style>
  .playground-composer {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 12px;
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }

  .playground-composer-input {
    min-height: 56px;
    max-height: 30vh;
    background: var(--bg);
    font-size: 13px;
    line-height: 1.5;
    field-sizing: content;
  }

  .playground-composer-actions {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }

  .playground-composer-hint {
    color: var(--text-muted);
    font-size: 12px;
  }

  @media (max-width: 768px) {
    .playground-composer-hint {
      display: none;
    }
  }
</style>
