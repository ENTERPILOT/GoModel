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
