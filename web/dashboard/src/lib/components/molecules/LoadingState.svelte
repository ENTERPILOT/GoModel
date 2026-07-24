<script>
  // Loading placeholder row (.loading-state markup with the CSS spinner).
  let { label = "Loading...", class: className = "" } = $props();
</script>

<div class="loading-state {className}" role="status" aria-live="polite">
  <span class="loading-spinner" aria-hidden="true"></span>
  <span>{label}</span>
</div>

<style>
  /* Styles owned by this component (moved from dashboard.css). */
  .loading-state {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 10px;
    min-height: 64px;
    color: var(--text-muted);
    font-size: 14px;
  }
</style>
