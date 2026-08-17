<script>
  // Standard toolbar search field: magnifier icon inside a text input, with
  // an optional trailing clear action. `loading` shows a small trailing
  // spinner while a search-triggered fetch is in flight.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import { Search, X } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  let {
    value = $bindable(""),
    placeholder = "",
    label = "",
    id = undefined,
    oninput = undefined,
    onclear = undefined,
    clearLabel = "",
    loading = false,
    class: className = "",
  } = $props();

  // Clearable inputs always need a non-empty accessible name for the clear
  // button; fall back to a generic label when a caller omits one.
  const effectiveClearLabel = $derived(clearLabel || m.common_action_clear());

  function clear() {
    value = "";
    onclear?.();
  }
</script>

<div class={["filter-input-wrap", className]}>
  <Icon icon={Search} class="filter-input-icon" />
  <input
    type="text"
    class="filter-input"
    {id}
    {placeholder}
    aria-label={label}
    class:filter-input-clearable={!!onclear}
    bind:value
    {oninput}
  />
  {#if loading}
    <span class="filter-input-spinner" class:with-clear={!!(onclear && value)}>
      <Spinner size={14} label={m.common_loading()} />
    </span>
  {/if}
  {#if onclear && value}
    <button
      type="button"
      class="filter-input-clear"
      title={effectiveClearLabel}
      aria-label={effectiveClearLabel}
      onclick={clear}
    >
      <Icon icon={X} />
    </button>
  {/if}
</div>

<style>
  .filter-input-wrap {
    position: relative;
    display: flex;
    flex: 1;
    min-width: min(400px, 100%);
    width: 100%;
  }

  .filter-input {
    flex: 1;
    min-width: 0;
    width: 100%;
    padding-left: 34px;
  }

  .filter-input-clearable {
    padding-right: 34px;
  }

  /* The class rides on the Icon child's own <svg>, so it needs :global. */
  .filter-input-wrap :global(.filter-input-icon) {
    position: absolute;
    left: 12px;
    top: 50%;
    width: 14px;
    height: 14px;
    color: var(--text-muted);
    pointer-events: none;
    transform: translateY(-50%);
  }

  .filter-input-spinner {
    position: absolute;
    right: 12px;
    top: 50%;
    display: inline-flex;
    transform: translateY(-50%);
    pointer-events: none;
  }

  /* Make room for the trailing clear button when both are shown. */
  .filter-input-spinner.with-clear {
    right: 38px;
  }

  .filter-input-clear {
    position: absolute;
    right: 7px;
    top: 50%;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    padding: 0;
    border: 0;
    border-radius: 4px;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    transform: translateY(-50%);
  }

  .filter-input-clear:hover {
    background: var(--bg-surface-hover);
    color: var(--text);
  }

  .filter-input-clear :global(svg) {
    width: 14px;
    height: 14px;
  }
</style>
