<script>
  // Standard toolbar search field: magnifier icon inside a text input.
  // `label` fills aria-label; `class` adds modifiers to the wrapper.
  import Icon from "$lib/components/atoms/Icon.svelte";

  let {
    value = $bindable(""),
    placeholder = "",
    label = "",
    id = undefined,
    oninput = undefined,
    class: className = "",
  } = $props();
</script>

<div class="filter-input-wrap {className}">
  <Icon name="search" class="filter-input-icon" />
  <input
    type="text"
    class="filter-input"
    {id}
    {placeholder}
    aria-label={label}
    bind:value
    {oninput}
  />
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
</style>
