<script>
  // Standard 32px dialog/drawer close button (.dialog-close-btn styles).
  // `iconClass` defaults to the 14px table icon size used by the editors;
  // the auth-style dialogs pass "" to keep their larger X.
  import Icon from "./Icon.svelte";

  let {
    label = "Close",
    onclick,
    class: className = "",
    iconClass = "table-icon-svg",
    disabled = false,
    el = $bindable(null),
  } = $props();
</script>

<button
  type="button"
  class="dialog-close-btn {className}"
  aria-label={label}
  {disabled}
  {onclick}
  bind:this={el}
>
  <Icon name="x" class={iconClass} />
</button>

<style>
  /* Styles owned by this component (moved from dashboard.css). */
  .dialog-close-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: 0 0 32px;
    width: 32px;
    min-width: 32px;
    height: 32px;
    padding: 0;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--text-muted);
    cursor: pointer;
    font: inherit;
    line-height: 1;
    transition: background 0.15s, border-color 0.15s, color 0.15s;
  }

  .dialog-close-btn:hover {
    color: var(--text);
    background: var(--bg-surface-hover);
  }

  .dialog-close-btn:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--accent) 36%, transparent);
    outline-offset: 2px;
  }
</style>
