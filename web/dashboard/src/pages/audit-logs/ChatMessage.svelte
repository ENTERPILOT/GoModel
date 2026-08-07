<script>
  // One interaction message in the Interactions drawer: either a collapsible
  // function call/result note or a regular chat bubble. The <article> class
  // is partly dynamic (roleClass), which keeps the compiler from pruning the
  // role selectors below — those rules must live here, next to the element
  // they scope to (CONVENTIONS rule 3).
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { createCopyState } from "$lib/utils/clipboard.svelte.js";
  import { CircleCheck, Copy } from "lucide";
  import {
    conversationMessageCopyText,
    formatFunctionArguments,
    functionExpandedContent,
  } from "./conversation-helpers.js";

  let { msg, showPromptCache = true } = $props();
  const copyState = createCopyState({ logPrefix: "Failed to copy interaction message:" });

  const isFunctionNote = $derived(msg.role === "function_call" || msg.role === "function_result");

  function cacheFill(ratio) {
    const value = Math.max(0, Math.min(1, Number(ratio || 0)));
    return (value * 100).toFixed(1) + "%";
  }

  function cacheTitle(ratio) {
    const value = Math.max(0, Math.min(1, Number(ratio || 0)));
    return value > 0
      ? Math.round(value * 100) + "% of this visible prompt item is estimated provider-cached"
      : undefined;
  }

  function functionDetailText(m) {
    if (m.role === "function_call") {
      return (m.toolCalls || []).map((tc) => tc.name + "()").join(", ");
    }
    return (m.functionName ? m.functionName + ": " : "") + m.text;
  }
</script>

<article
  class={[
    isFunctionNote ? "chat-function-note" : "chat-message",
    msg.roleClass,
    {
      "is-anchor": msg.isAnchor,
      "is-after-anchor": msg.isAfterAnchor,
    },
  ]}
  style:--prompt-cache-fill={cacheFill(msg.promptCacheRatio)}
  style:--prompt-cache-visibility={showPromptCache && msg.promptCacheRatio > 0 ? "1" : "0"}
  data-conversation-anchor={msg.isAnchor ? "true" : undefined}
  data-conversation-message="true"
  data-entry-id={msg.entryID}
>
  {#if isFunctionNote}
    <details
      class="chat-function-note-details"
      title={showPromptCache ? cacheTitle(msg.promptCacheRatio) : undefined}
    >
      <summary class="chat-function-note-inner">
        <span class="chat-function-label">{msg.roleLabel}</span>
        <span class="chat-function-detail">{functionDetailText(msg)}</span>
        <span class="mono chat-time">{timezone.formatTimestamp(msg.timestamp)}</span>
      </summary>
      {#if msg.functionCallID}
        <div class="chat-function-call-id mono">Call ID: {msg.functionCallID}</div>
      {/if}
      {#if msg.role === "function_call" && msg.toolCalls}
        {#each msg.toolCalls as tc}
          {#if tc.id}
            <div class="chat-function-call-id mono">Call ID{msg.toolCalls.length > 1 ? " (" + tc.name + ")" : ""}: {tc.id}</div>
          {/if}
        {/each}
      {/if}
      <pre class="chat-function-expanded">{functionExpandedContent(msg)}</pre>
    </details>
  {:else}
    <header
      class="chat-message-meta"
      title={showPromptCache ? cacheTitle(msg.promptCacheRatio) : undefined}
    >
      <span class="chat-role">{msg.roleLabel}</span>
      <span class="mono chat-time">{timezone.formatTimestamp(msg.timestamp)}</span>
    </header>
    {#if msg.text}
      <pre
        class="chat-content"
        title={showPromptCache ? cacheTitle(msg.promptCacheRatio) : undefined}
      >{msg.text}</pre>
    {/if}
    {#if msg.toolCalls}
      <footer class="chat-tool-calls">
        {#each msg.toolCalls as tc, tcIdx (tc.name + "-" + tcIdx)}
          <details class="chat-tool-call">
            <summary class="chat-tool-call-summary">
              <span class="chat-tool-call-name">{tc.name + "()"}</span>
              <span class="chat-tool-call-preview">{formatFunctionArguments(tc) || "No arguments"}</span>
            </summary>
            {#if tc.id}
              <div class="chat-function-call-id mono">Call ID: {tc.id}</div>
            {/if}
            <pre class="chat-tool-call-arguments">{formatFunctionArguments(tc) || "No arguments"}</pre>
          </details>
        {/each}
      </footer>
    {/if}
  {/if}
  <button
    type="button"
    class="chat-message-copy"
    class:is-copied={copyState.copied}
    aria-label={copyState.copied ? "Message copied" : "Copy message"}
    title={copyState.copied ? "Copied" : "Copy message"}
    onclick={(event) => {
      event.stopPropagation();
      copyState.copy(conversationMessageCopyText(msg));
    }}
  >
    <Icon icon={copyState.copied ? CircleCheck : Copy} width="14" height="14" />
  </button>
</article>

<style>
  .chat-message {
    position: relative;
    overflow: hidden;
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 10px 42px 10px 12px;
    max-width: 94%;
    background: var(--bg);
  }

  .chat-message.is-anchor {
    border-color: color-mix(in srgb, var(--accent) 55%, var(--border));
    box-shadow: 0 0 0 1px color-mix(in srgb, var(--accent) 25%, transparent);
  }

  .chat-message.is-after-anchor,
  .chat-function-note.is-after-anchor {
    opacity: 0.46;
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

  .chat-message::before,
  .chat-function-note::before {
    content: "";
    position: absolute;
    inset: 0;
    z-index: 0;
    pointer-events: none;
    opacity: var(--prompt-cache-visibility);
    transition: opacity 150ms ease;
    background: linear-gradient(
      90deg,
      color-mix(in srgb, var(--prompt-cache-color) 18%, transparent) 0,
      color-mix(in srgb, var(--prompt-cache-color) 18%, transparent) var(--prompt-cache-fill),
      transparent var(--prompt-cache-fill),
      transparent 100%
    );
  }

  .chat-message > *,
  .chat-function-note > * {
    position: relative;
    z-index: 1;
  }

  .chat-message-copy {
    position: absolute;
    right: 6px;
    bottom: 6px;
    z-index: 2;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 26px;
    height: 26px;
    padding: 0;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: color-mix(in srgb, var(--bg-surface) 92%, transparent);
    color: var(--text-muted);
    opacity: 0;
    pointer-events: none;
    transform: translateY(3px);
    transition: opacity 120ms ease, transform 120ms ease, color 120ms ease,
      background-color 120ms ease, border-color 120ms ease;
  }

  .chat-message:hover .chat-message-copy,
  .chat-function-note:hover .chat-message-copy,
  .chat-message:focus-within .chat-message-copy,
  .chat-function-note:focus-within .chat-message-copy,
  .chat-message-copy.is-copied {
    opacity: 1;
    pointer-events: auto;
    transform: translateY(0);
  }

  .chat-message-copy:hover {
    color: var(--text);
    background: var(--bg-surface-hover);
  }

  .chat-message-copy:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--accent) 45%, transparent);
    outline-offset: 1px;
  }

  .chat-message-copy.is-copied {
    color: var(--success);
    border-color: color-mix(in srgb, var(--success) 45%, var(--border));
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
    position: relative;
    overflow: hidden;
    padding: 3px 5px;
    border-radius: 5px;
    font-size: 11px;
    color: var(--text-muted);
    font-family: "SF Mono", Menlo, Consolas, monospace;
    min-width: 0;
  }

  .chat-tool-call-summary {
    display: flex;
    align-items: baseline;
    gap: 6px;
    min-width: 0;
    cursor: pointer;
  }

  .chat-tool-call-preview {
    min-width: 0;
    flex: 1;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    opacity: 0.8;
  }

  .chat-tool-call-arguments {
    margin-top: 5px;
    padding: 6px 8px;
    border-radius: 6px;
    background: color-mix(in srgb, var(--border) 45%, transparent);
    color: var(--text);
    font: inherit;
    line-height: 1.45;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    max-height: 200px;
    overflow: auto;
  }

  .chat-tool-call-name::before {
    content: "\26A1 ";
  }

  /* Function call / result notes between bubbles */
  .chat-function-note {
    position: relative;
    overflow: hidden;
    align-self: center;
    max-width: 94%;
    padding: 4px 42px 4px 12px;
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
    min-height: 26px;
    overflow: hidden;
  }

  .chat-function-note-inner .chat-time {
    margin-left: auto;
    flex-shrink: 0;
  }

  .chat-function-call-id {
    margin-top: 6px;
    font-size: 10px;
    color: var(--text-muted);
    overflow-wrap: anywhere;
  }

  .chat-function-label {
    font-weight: 600;
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.3px;
    white-space: nowrap;
    flex-shrink: 0;
  }

  .chat-function-label::before {
    content: "\25B8";
    display: inline-block;
    margin-right: 4px;
    transition: transform 120ms ease;
  }

  .chat-function-note-details[open] .chat-function-label::before {
    transform: rotate(90deg);
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

  @media (hover: none) {
    .chat-message-copy {
      opacity: 1;
      pointer-events: auto;
      transform: none;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .chat-message-copy {
      transition: none;
    }
  }
</style>
