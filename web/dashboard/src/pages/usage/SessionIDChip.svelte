<script>
  // Compact session identity with separate filter and copy actions.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { createCopyState } from "$lib/utils/clipboard.svelte.js";
  import { CircleCheck, Copy } from "lucide";
  import { shortSessionID } from "./usage-helpers.js";

  let { sessionID, active = false, onfilter = null, compact = false } = $props();
  const copyState = createCopyState({ logPrefix: "Failed to copy session ID:" });

  $effect(() => {
    void sessionID;
    copyState.reset();
  });
</script>

<span class="session-id-chip" class:active class:compact>
  <button
    type="button"
    class="session-id-filter mono"
    title="Filter by session {sessionID}"
    onclick={() => onfilter?.(sessionID)}
  >{shortSessionID(sessionID)}</button>
  <button
    type="button"
    class="session-id-copy"
    class:copied={copyState.copied}
    title={copyState.error
      ? "Unable to copy session ID"
      : copyState.copied
        ? "Session ID copied"
        : "Copy session ID"}
    aria-label="Copy session ID {sessionID}"
    onclick={() => copyState.copy(sessionID)}
  >
    <Icon icon={copyState.copied ? CircleCheck : Copy} />
  </button>
</span>

<style>
  .session-id-chip {
    display: inline-flex;
    align-items: stretch;
    max-width: 290px;
    overflow: hidden;
    border: 1px solid color-mix(in srgb, var(--accent) 35%, var(--border));
    border-radius: 999px;
    background: color-mix(in srgb, var(--accent) 10%, var(--bg));
    color: var(--accent-strong, var(--accent));
    vertical-align: middle;
  }

  .session-id-chip:hover,
  .session-id-chip.active {
    background: color-mix(in srgb, var(--accent) 20%, var(--bg));
    border-color: var(--accent);
  }

  .session-id-filter,
  .session-id-copy {
    border: 0;
    background: transparent;
    color: inherit;
    cursor: pointer;
  }

  .session-id-filter {
    min-width: 0;
    overflow: hidden;
    padding: 4px 8px;
    font-size: 12px;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .session-id-copy {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    padding: 0 7px;
    border-left: 1px solid color-mix(in srgb, var(--accent) 25%, var(--border));
  }

  .session-id-copy:hover,
  .session-id-copy.copied {
    background: color-mix(in srgb, var(--accent) 18%, transparent);
  }

  .session-id-copy :global(svg) {
    width: 13px;
    height: 13px;
  }

  .session-id-chip.compact {
    max-width: 220px;
  }

  .session-id-chip.compact .session-id-filter {
    padding: 3px 7px;
    font-size: 11px;
  }

  .session-id-chip.compact .session-id-copy {
    width: 25px;
    padding: 0 6px;
  }
</style>
