<script>
  // A provider's status pill, shared by the Overview cards and the Providers
  // page rows. The tooltip carries the classification reason and last error.
  //
  // The state classes come from a computed string (providerStatusBadgeClass),
  // so the palette rules must stay in this file where the compiler can see
  // the pill markup.
  import {
    providerStatusBadgeClass,
    providerStatusPillTitle,
  } from "./providersLogic.js";

  let { provider } = $props();
</script>

<span
  class="provider-status-pill {providerStatusBadgeClass(provider.status)}"
  title={providerStatusPillTitle(provider)}
>{provider.status_label}</span>

<style>
  .provider-status-pill {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-height: 28px;
    padding: 0 10px;
    border-radius: 999px;
    border: 1px solid var(--border);
    font-size: 12px;
    font-weight: 700;
    white-space: nowrap;
  }

  /* Status pill palette: reads identically to the breaker-state pill inside
     the card details (ProviderStatusCardDetails.svelte keeps its half of
     these rules). */
  .provider-status-pill.is-healthy {
    color: var(--success);
    border-color: color-mix(in srgb, var(--success) 45%, var(--border));
    background: color-mix(in srgb, var(--success) 10%, transparent);
  }

  .provider-status-pill.is-degraded {
    color: var(--warning);
    border-color: color-mix(in srgb, var(--warning) 48%, var(--border));
    background: color-mix(in srgb, var(--warning) 26%, var(--bg-surface));
  }

  .provider-status-pill.is-unhealthy {
    color: var(--danger);
    border-color: color-mix(in srgb, var(--danger) 45%, var(--border));
    background: color-mix(in srgb, var(--danger) 10%, transparent);
  }
</style>
