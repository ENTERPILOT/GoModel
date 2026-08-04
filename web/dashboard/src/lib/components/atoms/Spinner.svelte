<script>
  // Inline loading spinner. size: px number; label: accessible text.
  let { size = 16, label = "Loading", class: className = "" } = $props();
</script>

<span
  class={["spinner", className]}
  style="--spinner-size: {size}px"
  role="status"
  aria-label={label}
></span>

<style>
  .spinner {
    display: inline-block;
    width: var(--spinner-size);
    height: var(--spinner-size);
    border: 2px solid var(--border, rgba(128, 128, 128, 0.35));
    border-top-color: var(--accent, currentColor);
    border-radius: 50%;
    animation: spinner-rotate 0.7s linear infinite;
    flex: none;
  }

  @keyframes spinner-rotate {
    to {
      transform: rotate(360deg);
    }
  }
</style>
