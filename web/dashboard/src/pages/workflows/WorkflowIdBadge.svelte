<script>
  // The workflow-id pill in the pipeline chart's top-right corner. Collapsed
  // to "id: ..." until hovered/focused, and click-to-copy.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { createCopyState } from "$lib/utils/clipboard.svelte.js";
  import { Copy } from "lucide";

  let { workflowID = "" } = $props();

  const copyState = createCopyState({ logPrefix: "Failed to copy workflow ID:" });

  // Reset the copied/error feedback whenever the badge points at another
  // workflow id.
  $effect(() => {
    void workflowID;
    copyState.reset();
  });

  const title = $derived(
    copyState.error
      ? "Unable to copy workflow ID"
      : copyState.copied
        ? "Workflow ID copied"
        : "Copy workflow ID",
  );
  const ariaLabel = $derived(workflowID ? title + " " + workflowID : title);

  async function copy(event) {
    event.preventDefault();
    if (!workflowID) return;
    await copyState.copy(workflowID);
  }
</script>

<button
  type="button"
  class="workflow-pipeline-meta mono"
  class:workflow-pipeline-meta-copied={copyState.copied}
  class:workflow-pipeline-meta-error={copyState.error}
  {title}
  aria-label={ariaLabel}
  onclick={copy}
>
  <span class="workflow-pipeline-meta-label">id:</span>
  <span class="workflow-pipeline-meta-placeholder">...</span>
  <span class="workflow-pipeline-meta-value">{workflowID}</span>
  <span class="workflow-pipeline-meta-icon" aria-hidden="true">
    <Icon icon={Copy} />
  </span>
</button>

<style>
  .workflow-pipeline-meta {
    position: absolute;
    top: 12px;
    right: 14px;
    display: inline-flex;
    align-items: center;
    gap: 0;
    min-width: 0;
    max-width: calc(100% - 28px);
    padding: 2px 10px;
    border-radius: 12px;
    border: 1px solid var(--border);
    background: color-mix(in srgb, var(--bg-surface) 86%, transparent);
    color: var(--text-muted);
    font-size: 12px;
    font-weight: 500;
    line-height: 1.2;
    white-space: nowrap;
    appearance: none;
    cursor: pointer;
    text-align: left;
    overflow: hidden;
    transition:
      background-color 0.15s,
      border-color 0.15s,
      color 0.15s,
      box-shadow 0.15s;
  }

  .workflow-pipeline-meta:hover,
  .workflow-pipeline-meta:focus-visible {
    border-color: color-mix(in srgb, var(--accent) 40%, var(--border));
    background: color-mix(in srgb, var(--accent) 8%, var(--bg-surface));
    color: color-mix(in srgb, var(--accent) 74%, var(--text));
  }

  .workflow-pipeline-meta:focus-visible {
    outline: none;
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--accent) 18%, transparent);
  }

  .workflow-pipeline-meta-label {
    flex: 0 0 auto;
    font-weight: 700;
  }

  /* The "..." placeholder and the id itself swap places on hover/focus (and
     stay swapped once copy feedback is showing). */
  .workflow-pipeline-meta-placeholder {
    flex: 0 0 auto;
    max-width: 3ch;
    margin-left: 4px;
    overflow: hidden;
    opacity: 1;
    transition:
      max-width 0.18s ease,
      margin-left 0.18s ease,
      opacity 0.15s ease;
  }

  .workflow-pipeline-meta-value {
    flex: 0 1 auto;
    max-width: 0;
    margin-left: 0;
    overflow: hidden;
    opacity: 0;
    text-overflow: clip;
    transition:
      max-width 0.22s ease,
      margin-left 0.18s ease,
      opacity 0.15s ease;
  }

  .workflow-pipeline-meta:hover .workflow-pipeline-meta-placeholder,
  .workflow-pipeline-meta:focus-visible .workflow-pipeline-meta-placeholder,
  .workflow-pipeline-meta-copied .workflow-pipeline-meta-placeholder,
  .workflow-pipeline-meta-error .workflow-pipeline-meta-placeholder {
    max-width: 0;
    margin-left: 0;
    opacity: 0;
  }

  .workflow-pipeline-meta:hover .workflow-pipeline-meta-value,
  .workflow-pipeline-meta:focus-visible .workflow-pipeline-meta-value,
  .workflow-pipeline-meta-copied .workflow-pipeline-meta-value,
  .workflow-pipeline-meta-error .workflow-pipeline-meta-value {
    max-width: 42ch;
    margin-left: 4px;
    opacity: 1;
  }

  .workflow-pipeline-meta-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: 0 0 auto;
    width: 0;
    height: 14px;
    margin-left: 0;
    overflow: hidden;
    opacity: 0;
    line-height: 0;
    transform: translateX(4px) translateY(1px) scale(0.84);
    transition:
      width 0.18s ease,
      margin-left 0.18s ease,
      opacity 0.15s ease,
      transform 0.18s ease;
  }

  .workflow-pipeline-meta-icon :global(svg) {
    width: 14px;
    height: 14px;
  }

  .workflow-pipeline-meta-copied,
  .workflow-pipeline-meta-copied:hover,
  .workflow-pipeline-meta-copied:focus-visible {
    background: color-mix(in srgb, var(--success) 12%, var(--bg));
    border-color: color-mix(in srgb, var(--success) 40%, var(--border));
    color: var(--success);
  }

  .workflow-pipeline-meta-copied .workflow-pipeline-meta-icon {
    width: 14px;
    margin-left: 6px;
    opacity: 1;
    transform: translateY(1px);
  }

  .workflow-pipeline-meta-error,
  .workflow-pipeline-meta-error:hover,
  .workflow-pipeline-meta-error:focus-visible {
    background: color-mix(in srgb, var(--danger) 10%, var(--bg));
    border-color: color-mix(in srgb, var(--danger) 34%, var(--border));
    color: var(--danger);
  }
</style>
