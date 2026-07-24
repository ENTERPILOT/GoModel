<script>
  // Loading placeholder row (.loading-state markup with the CSS spinner).
  let { label = "Loading...", class: className = "" } = $props();
</script>

<div class="loading-state {className}" role="status" aria-live="polite">
  <span class="loading-spinner" aria-hidden="true"></span>
  <span>{label}</span>
</div>
