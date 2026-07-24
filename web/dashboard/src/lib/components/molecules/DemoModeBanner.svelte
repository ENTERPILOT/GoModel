<script>
  import { demoMode } from "$lib/api/paths.js";
</script>

{#if demoMode()}
  <aside class="demo-mode-banner" role="alert" aria-label="Demo mode notice">
    <svg class="demo-mode-banner-icon" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/><path d="M12 9v4"/><path d="M12 17h.01"/></svg>
    <div class="demo-mode-banner-copy">
      <strong>Public demo</strong>
      <span>Do not enter sensitive or personal data. Demo data is reset regularly.</span>
    </div>
    <nav class="demo-mode-banner-links" aria-label="GoModel website">
      <a href="https://gomodel.enterpilot.io/?utm_source=gomodel_dashboard" target="_blank" rel="noopener noreferrer">gomodel.enterpilot.io</a>
    </nav>
  </aside>
{/if}
