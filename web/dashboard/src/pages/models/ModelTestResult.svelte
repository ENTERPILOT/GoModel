<script>
  // A row's latest test outcome, shown as a secondary line under the model
  // name: progress, then latency with a reply excerpt or the error.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { CircleAlert, CircleCheck } from "lucide";
  import * as m from "$lib/paraglide/messages.js";
  import { modelTestState } from "./modelTest.svelte.js";

  let { row } = $props();

  const result = $derived(modelTestState.result(row));
  const running = $derived(result?.state === "running");
</script>

{#if result}
  <div class="model-test-result" class:is-error={result.state === "error"} role="status">
    {#if running}
      {m.models_test_running()}
    {:else if result.state === "ok"}
      <Icon icon={CircleCheck} class="model-test-result-icon" />
      <span>
        {m.models_test_ok({ seconds: (result.durationMs / 1000).toFixed(2) })} ·
        {#if result.reply}
          <span class="mono">“{result.reply}”</span>
        {:else if result.finishReason}
          {m.models_test_ok_empty_reason({ reason: result.finishReason })}
        {:else}
          {m.models_test_ok_empty()}
        {/if}
      </span>
    {:else}
      <Icon icon={CircleAlert} class="model-test-result-icon" />
      <span>
        {result.status
          ? m.models_test_failed_status({ status: result.status, message: result.message })
          : m.models_test_failed({ message: result.message })}
      </span>
    {/if}
    {#if !running}
      <button
        type="button"
        class="model-test-dismiss-btn mono"
        aria-label={m.models_test_dismiss({ name: row.display_name })}
        title={m.models_test_dismiss({ name: row.display_name })}
        onclick={() => modelTestState.dismiss(row)}
      >[{m.models_test_dismiss_short()}]</button>
    {/if}
  </div>
{/if}

<style>
  /* Font size and color match ModelRow's other secondary lines. */
  .model-test-result {
    display: flex;
    align-items: flex-start;
    gap: 6px;
    max-width: 72ch;
    overflow-wrap: anywhere;
    font-size: 12px;
    color: var(--text-muted);
  }

  .model-test-result.is-error {
    color: var(--danger);
  }

  /* :global so the rule reaches the SVG rendered by the Icon component. */
  .model-test-result :global(.model-test-result-icon) {
    flex: 0 0 auto;
    width: 14px;
    height: 14px;
    margin-top: 1px;
  }

  .model-test-result:not(.is-error) :global(.model-test-result-icon) {
    color: var(--success);
  }

  .model-test-dismiss-btn {
    appearance: none;
    flex: 0 0 auto;
    padding: 0;
    border: 0;
    background: none;
    color: var(--text-muted);
    font-size: 11px;
    cursor: pointer;
  }

  .model-test-dismiss-btn:hover {
    text-decoration: underline;
  }
</style>
