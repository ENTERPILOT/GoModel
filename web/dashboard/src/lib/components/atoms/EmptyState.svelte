<script>
  // Empty-table / empty-list placeholder.
  let { icon = "inbox", title = "No data", hint = "", children } = $props();
  import Icon from "./Icon.svelte";
</script>

<div class="empty-state">
  <Icon name={icon} class="empty-state-icon" />
  <p class="empty-state-title">{title}</p>
  {#if hint}
    <p class="empty-state-hint">{hint}</p>
  {/if}
  {@render children?.()}
</div>
