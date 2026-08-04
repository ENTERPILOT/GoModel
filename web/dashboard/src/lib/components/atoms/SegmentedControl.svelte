<script>
  // Segmented button group for switching between a few exclusive options
  // (Tokens/Costs, chart intervals, throughput granularity, ...).
  // options: array of {value, label}; onchange fires with the picked value.
  let {
    options = [],
    value,
    onchange,
    ariaLabel = "",
    class: className = "",
  } = $props();
</script>

<div class={["segmented-control", className]} role="group" aria-label={ariaLabel}>
  {#each options as option (option.value)}
    <button
      type="button"
      class="segmented-btn"
      class:active={value === option.value}
      aria-pressed={value === option.value}
      onclick={() => onchange?.(option.value)}
    >{option.label}</button>
  {/each}
</div>

<style>
  /* Interval Picker */
  .segmented-control {
    display: inline-flex;
    align-items: center;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 2px;
  }

  .segmented-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 5px 12px;
    background: transparent;
    border: none;
    border-radius: 4px;
    color: var(--text-muted);
    font-size: 12px;
    font-family: inherit;
    font-weight: 500;
    cursor: pointer;
    transition: all 0.15s;
    white-space: nowrap;
  }

  .segmented-btn:hover {
    color: var(--text);
  }

  .segmented-btn.active {
    background: var(--accent);
    color: #fff;
  }

  @media (max-width: 768px) {
    /* Segmented controls mobile */
    .segmented-btn {
        padding: 4px 8px;
        font-size: 11px;
      }
  }
</style>
