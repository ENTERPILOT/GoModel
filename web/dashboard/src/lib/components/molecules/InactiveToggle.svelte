<script>
  // Toolbar toggle for revealing rows hidden by an "inactive" filter (API
  // keys, users): checkbox with a localized label and an optional "(count)"
  // of hidden rows when the toggle is off.
  let {
    checked = $bindable(false),
    label = "",
    count = 0,
  } = $props();
</script>

<label class="inactive-toggle">
  <input type="checkbox" bind:checked />
  <span>
    {label}
    {#if count > 0}({count}){/if}
  </span>
</label>

<style>
  .inactive-toggle {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    color: var(--text);
    cursor: pointer;
    user-select: none;
    white-space: nowrap;
  }

  .inactive-toggle input {
    width: 16px;
    height: 16px;
    accent-color: var(--accent);
    cursor: pointer;
  }
</style>
