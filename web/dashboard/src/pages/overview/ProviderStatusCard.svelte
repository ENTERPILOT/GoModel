<script>
  // One provider card in the Providers Overview grid: status pill, runtime
  // meta, and the expand/collapse toggle. The expandable details body lives
  // in ProviderStatusCardDetails.svelte.
  //
  // The status pill's state classes come from a computed string
  // (providerStatusBadgeClass), so its palette rules must stay in this file
  // where the compiler can see the pill markup.
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { formatNumber } from "$lib/utils/format.js";
  import { providerStatusState } from "./overviewState.svelte.js";
  import ProviderStatusCardDetails from "./ProviderStatusCardDetails.svelte";
  import {
    providerStatusBadgeClass,
    providerTypeLabel,
    providerDocUrl,
    providerLastCheckedTime,
    providerLastCheckedTitle,
    providerStatusPillTitle,
  } from "./providersLogic.js";
  import { ChevronDown } from "lucide";

  let { provider } = $props();

  const expanded = $derived(providerStatusState.cardExpanded(provider));
  const formatTimestamp = (ts) => timezone.formatTimestamp(ts);
</script>

<article class="provider-status-card">
  <div class="provider-status-card-head">
    <div>
      <h4 class="provider-status-name">
        <span>{provider.name}</span>
        {#if providerTypeLabel(provider)}
          <span class="provider-status-name-type">({providerTypeLabel(provider)})</span>
        {/if}
        {#if providerDocUrl(provider)}
          <a
            class="inline-help-toggle provider-doc-help"
            href={providerDocUrl(provider)}
            target="_blank"
            rel="noopener noreferrer"
            aria-label={"View " +
              (providerTypeLabel(provider) || provider.name) +
              " provider docs"}
            title={"View " +
              (providerTypeLabel(provider) || provider.name) +
              " provider docs"}
          >
            <span class="inline-help-toggle-icon" aria-hidden="true">?</span>
          </a>
        {/if}
      </h4>
    </div>
    <span
      class="provider-status-pill {providerStatusBadgeClass(provider.status)}"
      title={providerStatusPillTitle(provider)}
    >{provider.status_label}</span>
  </div>

  <div class="provider-status-meta">
    <div class="provider-status-meta-item">
      <span class="provider-status-meta-label">Models Available</span>
      <span class="provider-status-meta-value mono"
      >{formatNumber(provider.runtime?.discovered_model_count)}</span>
    </div>
    <div class="provider-status-meta-item">
      <span class="provider-status-meta-label">Last Checked</span>
      <span
        class="provider-status-meta-value mono"
        title={providerLastCheckedTitle(provider, formatTimestamp)}
      >{providerLastCheckedTime(provider, formatTimestamp)}</span>
    </div>
  </div>

  <ProviderStatusCardDetails {provider} {expanded} />

  <button
    type="button"
    class="provider-status-card-toggle"
    class:is-expanded={expanded}
    aria-expanded={expanded}
    aria-label={(expanded ? "Collapse " : "Expand ") + provider.name + " details"}
    title={expanded ? "Collapse details" : "Expand details"}
    onclick={() => providerStatusState.toggleCard(provider)}
  >
    <Icon icon={ChevronDown} class="provider-status-card-toggle-icon" />
  </button>
</article>

<style>
  .provider-status-card {
    display: flex;
    flex-direction: column;
    gap: 14px;
    padding: 18px;
    background: var(--bg-surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }

  .provider-status-card-toggle {
    /* Bleed to the card edges so the strip spans the full card width. */
    margin: auto -18px -18px;
    padding: 7px 0;
    display: flex;
    align-items: center;
    justify-content: center;
    background: transparent;
    border: 0;
    border-top: 1px solid var(--border);
    border-radius: 0 0 var(--radius) var(--radius);
    color: var(--text-muted);
    cursor: pointer;
    transition: background 0.15s ease, color 0.15s ease;
  }

  .provider-status-card-toggle:hover {
    background: color-mix(in srgb, var(--accent) 10%, transparent);
    color: var(--text);
  }

  .provider-status-card-toggle:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--accent) 32%, transparent);
    outline-offset: -2px;
  }

  /* The class rides on the Icon child's own <svg>, so it needs :global. */
  .provider-status-card-toggle :global(.provider-status-card-toggle-icon) {
    display: block;
    width: 16px;
    height: 16px;
    transition: transform 0.28s ease;
  }

  .provider-status-card-toggle.is-expanded :global(.provider-status-card-toggle-icon) {
    transform: rotate(180deg);
  }

  .provider-status-card-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 12px;
  }

  .provider-status-name {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 6px;
    font-size: 16px;
    font-weight: 700;
    letter-spacing: -0.02em;
  }

  /* Provider docs help icon — reuses the standard help-toggle badge as a link. */
  .provider-doc-help {
    align-self: center;
    text-decoration: none;
  }

  .provider-status-name-type {
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--text-muted);
  }

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
     the details (ProviderStatusCardDetails.svelte keeps its half of these
     rules). Kept here because the pill's state class is a computed string. */
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

  .provider-status-meta {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 12px;
  }

  .provider-status-meta-item {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 10px 12px;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 6px;
  }

  .provider-status-meta-label {
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--text-muted);
  }

  .provider-status-meta-value {
    font-size: 14px;
    color: var(--text);
  }

  @media (max-width: 768px) {
    .provider-status-meta {
        grid-template-columns: 1fr;
      }
  }
</style>
