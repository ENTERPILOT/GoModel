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

<div class="segmented-control {className}" role="group" aria-label={ariaLabel}>
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
