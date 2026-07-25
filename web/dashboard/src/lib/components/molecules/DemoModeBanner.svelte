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

<style>
  .demo-mode-banner {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: center;
    gap: 12px;
    margin-bottom: 24px;
    padding: 12px 16px;
    border: 1px solid color-mix(in srgb, var(--warning) 55%, var(--border));
    border-radius: var(--radius);
    background: color-mix(in srgb, var(--warning) 14%, var(--bg-surface));
    color: var(--text);
    box-shadow: 0 8px 24px color-mix(in srgb, var(--bg) 70%, transparent);
  }

  .demo-mode-banner-icon {
    width: 20px;
    height: 20px;
    flex: 0 0 20px;
    color: var(--warning);
  }

  .demo-mode-banner-copy {
    display: flex;
    align-items: baseline;
    gap: 8px;
    min-width: 0;
    font-size: 13px;
  }

  .demo-mode-banner :global(strong) {
    flex-shrink: 0;
    color: var(--warning);
    font-size: 12px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }

  .demo-mode-banner-links {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 6px;
  }

  .demo-mode-banner-links :global(a) {
    display: inline-flex;
    align-items: center;
    min-height: 28px;
    padding: 4px 8px;
    border: 1px solid color-mix(in srgb, var(--warning) 42%, var(--border));
    border-radius: var(--radius);
    color: var(--text);
    font-size: 12px;
    font-weight: 600;
    line-height: 1;
    text-decoration: none;
    white-space: nowrap;
  }

  .demo-mode-banner-links :global(a:hover) {
    border-color: var(--warning);
    background: color-mix(in srgb, var(--warning) 16%, transparent);
    color: var(--text);
  }

  .demo-mode-banner-links :global(a:focus-visible) {
    outline: 2px solid color-mix(in srgb, var(--warning) 42%, transparent);
    outline-offset: 2px;
  }

  @media (max-width: 768px) {
    .demo-mode-banner {
        align-items: flex-start;
        grid-template-columns: auto minmax(0, 1fr);
      }

    .demo-mode-banner-copy {
        display: grid;
        gap: 2px;
      }

    .demo-mode-banner-links {
        grid-column: 2;
        justify-content: flex-start;
        flex-wrap: wrap;
      }
  }
</style>
