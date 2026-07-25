<script>
  // One provider card in the Providers Overview grid: status pill, runtime
  // meta, and the expandable details (reason, request health, config).
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { formatNumber } from "$lib/utils/format.js";
  import { providerStatusState } from "./overviewState.svelte.js";
  import {
    providerStatusBadgeClass,
    providerTypeLabel,
    providerDocUrl,
    providerLastCheckedTime,
    providerLastCheckedTitle,
    providerStatusPillTitle,
    providerRequestHealth,
    providerBreakerState,
    providerBreakerStateLabel,
    providerBreakerStateClass,
    providerRecentTrafficSummary,
    providerHealthModels,
    providerHealthModelStats,
    providerHealthModelTitle,
    providerModelsSummary,
    providerRetrySummary,
    providerCircuitBreakerSummary,
  } from "./providersLogic.js";

  let { provider } = $props();

  const expanded = $derived(providerStatusState.cardExpanded(provider));
  const formatTimestamp = (ts) => timezone.formatTimestamp(ts);

  // Config rows that are only rendered when the provider declares them.
  const optionalConfig = $derived(
    [
      ["Base URL", provider.config?.base_url],
      ["API Version", provider.config?.api_version],
    ].filter(([, value]) => !!value),
  );
</script>

{#snippet configRow(label, value, code = false)}
  <div class="provider-status-config-row">
    <span class="provider-status-config-label">{label}</span>
    {#if code}
      <code class="provider-status-config-value">{value}</code>
    {:else}
      <span class="provider-status-config-value">{value}</span>
    {/if}
  </div>
{/snippet}

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

  <div
    class="provider-status-details"
    class:is-expanded={expanded}
    class:is-collapsed={!expanded}
    aria-hidden={!expanded}
  >
    <div class="provider-status-details-inner">
      <p class="provider-status-reason">{provider.status_reason}</p>
      {#if provider.last_error}
        <p class="provider-status-error">{provider.last_error}</p>
      {/if}

      {#if providerRequestHealth(provider)}
        <div class="provider-status-health">
          {@render configRow("Recent Requests", providerRecentTrafficSummary(provider))}
          {#if providerBreakerState(provider)}
            <div class="provider-status-config-row">
              <span class="provider-status-config-label">Breaker State</span>
              <span>
                <span
                  class="provider-status-health-state {providerBreakerStateClass(provider)}"
                >{providerBreakerStateLabel(provider)}</span>
              </span>
            </div>
          {/if}
          {#if providerHealthModels(provider).length > 0}
            <div class="provider-status-config-row">
              <span class="provider-status-config-label">Models (Recent Traffic)</span>
              <div class="provider-status-health-models">
                {#each providerHealthModels(provider) as model (model.model)}
                  <div
                    class="provider-status-health-model"
                    class:is-flagged={model.flagged}
                    title={providerHealthModelTitle(model)}
                  >
                    <span class="provider-status-health-model-name mono"
                    >{model.model}</span>
                    <span class="provider-status-health-model-stats"
                    >{providerHealthModelStats(model)}</span>
                  </div>
                {/each}
              </div>
            </div>
          {/if}
        </div>
      {/if}

      <div class="provider-status-config">
        {#each optionalConfig as [label, value] (label)}
          {@render configRow(label, value, true)}
        {/each}
        {@render configRow("Configured Models", providerModelsSummary(provider))}
        {@render configRow("Retry", providerRetrySummary(provider))}
        {@render configRow("Circuit Breaker", providerCircuitBreakerSummary(provider))}
      </div>
    </div>
  </div>

  <button
    type="button"
    class="provider-status-card-toggle"
    class:is-expanded={expanded}
    aria-expanded={expanded}
    aria-label={(expanded ? "Collapse " : "Expand ") + provider.name + " details"}
    title={expanded ? "Collapse details" : "Expand details"}
    onclick={() => providerStatusState.toggleCard(provider)}
  >
    <Icon name="chevron-down" class="provider-status-card-toggle-icon" />
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

  /* Shared health palette: the card's status pill and the breaker-state pill
     inside the details read identically. */
  .provider-status-pill.is-healthy,
  .provider-status-health-state.is-healthy {
    color: var(--success);
    border-color: color-mix(in srgb, var(--success) 45%, var(--border));
    background: color-mix(in srgb, var(--success) 10%, transparent);
  }

  .provider-status-pill.is-degraded,
  .provider-status-health-state.is-degraded {
    color: var(--warning);
    border-color: color-mix(in srgb, var(--warning) 48%, var(--border));
    background: color-mix(in srgb, var(--warning) 26%, var(--bg-surface));
  }

  .provider-status-pill.is-unhealthy,
  .provider-status-health-state.is-unhealthy {
    color: var(--danger);
    border-color: color-mix(in srgb, var(--danger) 45%, var(--border));
    background: color-mix(in srgb, var(--danger) 10%, transparent);
  }

  .provider-status-details {
    display: grid;
    grid-template-rows: 0fr;
    opacity: 0;
    transition: grid-template-rows 0.28s ease, opacity 0.22s ease;
  }

  .provider-status-details.is-expanded {
    grid-template-rows: 1fr;
    opacity: 1;
  }

  .provider-status-details.is-collapsed {
    pointer-events: none;
  }

  .provider-status-details-inner {
    min-height: 0;
    overflow: hidden;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }

  .provider-status-reason {
    font-size: 13px;
    color: var(--text-muted);
  }

  .provider-status-error {
    font-size: 12px;
    color: var(--danger);
    overflow-wrap: break-word;
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

  .provider-status-meta-label, .provider-status-config-label {
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

  .provider-status-config {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding-top: 12px;
    border-top: 1px solid var(--border);
  }

  .provider-status-config-row {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .provider-status-config-value {
    display: block;
    font-size: 13px;
    color: var(--text);
    overflow-wrap: break-word;
  }

  .provider-status-health {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding-top: 12px;
    margin-bottom: 12px;
    border-top: 1px solid var(--border);
  }

  .provider-status-health-state {
    display: inline-flex;
    align-items: center;
    padding: 1px 8px;
    border-radius: 999px;
    border: 1px solid var(--border);
    font-size: 12px;
    font-weight: 600;
  }

  .provider-status-health-models {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }

  .provider-status-health-model {
    display: flex;
    justify-content: space-between;
    gap: 8px;
    font-size: 13px;
    color: var(--text);
  }

  .provider-status-health-model.is-flagged {
    color: var(--danger);
  }

  .provider-status-health-model-name {
    overflow-wrap: anywhere;
  }

  .provider-status-health-model-stats {
    white-space: nowrap;
    color: var(--text-muted);
  }

  .provider-status-health-model.is-flagged .provider-status-health-model-stats {
    color: var(--danger);
    font-weight: 700;
  }

  @media (max-width: 768px) {
    .provider-status-meta {
        grid-template-columns: 1fr;
      }
  }
</style>
