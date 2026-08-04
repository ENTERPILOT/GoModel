<script>
  // Loading placeholder row (.loading-state markup with the CSS spinner).
  // Global modifier classes can override the default min-height by setting
  // --loading-state-min-height on the same element.
  let { label = "Loading...", class: className = "" } = $props();
</script>

<div class={["loading-state", className]} role="status" aria-live="polite">
  <span class="loading-spinner" aria-hidden="true"></span>
  <span>{label}</span>
</div>

<style>
  .loading-state {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 10px;
    min-height: var(--loading-state-min-height, 64px);
    color: var(--text-muted);
    font-size: 14px;
  }
</style>
