<script>
  // One interaction message in the Interactions drawer: either a collapsible
  // function call/result note or a regular chat bubble. The <article> class
  // is a computed string (base + roleClass + is-anchor), which keeps the
  // compiler from pruning the role/anchor selectors below — those rules must
  // live here, next to the element they scope to (CONVENTIONS rule 3).
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { functionExpandedContent } from "./conversation-helpers.js";

  let { msg } = $props();

  function articleClass(m) {
    const base =
      m.role === "function_call" || m.role === "function_result"
        ? "chat-function-note"
        : "chat-message";
    return [base, m.roleClass, m.isAnchor ? "is-anchor" : ""]
      .filter(Boolean)
      .join(" ");
  }

  function functionDetailText(m) {
    if (m.role === "function_call") {
      return (m.toolCalls || []).map((tc) => tc.name + "()").join(", ");
    }
    return (m.functionName ? m.functionName + ": " : "") + m.text;
  }
</script>

<article class={articleClass(msg)}>
  {#if msg.role === "function_call" || msg.role === "function_result"}
    <details class="chat-function-note-details">
      <summary class="chat-function-note-inner">
        <span class="chat-function-label">{msg.roleLabel}</span>
        <span class="chat-function-detail">{functionDetailText(msg)}</span>
      </summary>
      <pre class="chat-function-expanded">{functionExpandedContent(msg)}</pre>
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

<style>
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
