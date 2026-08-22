<script>
  // One editable message of the playground conversation: role picker,
  // content textarea and a remove button. Mutates the store's message proxy
  // directly, which keeps the JSON preview in sync as the user types.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import TableActionButton from "$lib/components/atoms/TableActionButton.svelte";
  import { GripVertical, Trash2 } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
  import { playgroundStore as store } from "./playground.svelte.js";
  import { ROLES } from "./playgroundLogic.js";

  let { message } = $props();

  // Arrow keys on the grip move the message without a pointer.
  function onHandleKeydown(event) {
    if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return;
    event.preventDefault();
    store.nudgeMessage(message.id, event.key === "ArrowUp" ? -1 : 1);
    // Keep focus on the grip after Svelte moves the keyed row.
    const handle = event.currentTarget;
    queueMicrotask(() => handle.focus());
  }

  const roleLabels = {
    system: m.playground_role_system,
    user: m.playground_role_user,
    assistant: m.playground_role_assistant,
  };
</script>

<article
  class={["playground-message", "playground-message-" + message.role]}
  data-role={message.role}
  data-sortable-item
>
  <div class="playground-message-head">
    <button
      type="button"
      class="playground-drag-handle"
      data-sortable-handle
      aria-label={m.playground_reorder_message()}
      title={m.playground_reorder_message()}
      disabled={message.pending || store.messages.length < 2}
      onkeydown={onHandleKeydown}
    >
      <Icon icon={GripVertical} class="table-icon-svg" />
    </button>
    <select
      class="playground-role-select mono"
      aria-label={m.playground_role_label()}
      value={message.role}
      disabled={message.pending}
      onchange={(event) => {
        message.role = event.currentTarget.value;
      }}
    >
      {#each ROLES as role (role)}
        <option value={role}>{roleLabels[role]()}</option>
      {/each}
    </select>
    {#if message.pending}
      <span class="playground-message-pending" role="status">
        <span class="loading-spinner" aria-hidden="true"></span>
        <span>{m.playground_sending()}</span>
      </span>
    {/if}
    <TableActionButton
      label={m.playground_remove_message()}
      class="table-icon-btn playground-remove-btn"
      disabled={message.pending}
      onclick={() => store.removeMessage(message.id)}
    >
      <Icon icon={Trash2} class="table-icon-svg" />
    </TableActionButton>
  </div>
  <textarea
    class="playground-message-body"
    rows="2"
    aria-label={roleLabels[message.role]()}
    placeholder={m.playground_message_placeholder()}
    readonly={message.pending}
    bind:value={message.content}
  ></textarea>
</article>

<style>
  .playground-message {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 10px 12px 12px;
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-left: 3px solid var(--playground-role-color, var(--border));
    border-radius: var(--radius);
  }

  .playground-message-system {
    --playground-role-color: var(--warning);
  }

  .playground-message-user {
    --playground-role-color: var(--info);
  }

  .playground-message-assistant {
    --playground-role-color: var(--success);
  }

  .playground-message:global(.is-dragging) {
    box-shadow: 0 10px 24px rgba(0, 0, 0, 0.3);
    border-color: var(--accent);
    opacity: 0.95;
  }

  .playground-message-head {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .playground-drag-handle {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    margin-left: -4px;
    padding: 0;
    background: transparent;
    border: 0;
    border-radius: 4px;
    color: var(--text-muted);
    cursor: grab;
    touch-action: none;
  }

  .playground-drag-handle:hover:not(:disabled),
  .playground-drag-handle:focus-visible {
    background: var(--bg-surface-hover);
    color: var(--text);
  }

  .playground-drag-handle:disabled {
    opacity: 0.35;
    cursor: default;
  }

  .playground-message:global(.is-dragging) .playground-drag-handle {
    cursor: grabbing;
  }

  .playground-role-select {
    padding: 3px 6px;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--playground-role-color, var(--text));
    font-size: 12px;
    font-weight: 600;
    cursor: pointer;
  }

  .playground-role-select:disabled {
    cursor: default;
    opacity: 0.7;
  }

  .playground-message-pending {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    color: var(--text-muted);
    font-size: 12px;
  }

  .playground-message-head :global(.playground-remove-btn) {
    margin-left: auto;
  }

  .playground-message-body {
    min-height: 44px;
    padding: 8px 10px;
    background: var(--bg);
    font-size: 13px;
    line-height: 1.5;
    field-sizing: content;
    max-height: 40vh;
  }

  .playground-message-body[readonly] {
    opacity: 1;
    cursor: text;
  }
</style>
