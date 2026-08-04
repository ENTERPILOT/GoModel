<script>
  // Expandable details of one provider card: status reason, last error,
  // request health (breaker state + per-model traffic), and config rows.
  // Props: `provider` (status row), `expanded` (bool, drives the collapse).
  //
  // Split out of ProviderStatusCard.svelte. Every class styled below appears
  // statically in this file's markup — the breaker-state palette classes are
  // applied via class: directives (not a computed class string) so the
  // compiler can see them and the scope hash keeps matching.
  import {
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

  let { provider, expanded } = $props();

  const breakerClass = $derived(providerBreakerStateClass(provider));

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
                class={["provider-status-health-state", breakerClass]}
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

<style>
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

  .provider-status-config-label {
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--text-muted);
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

  /* Health palette: reads identically to the card's status pill (which keeps
     its half of these rules in ProviderStatusCard.svelte). */
  .provider-status-health-state.is-healthy {
    color: var(--success);
    border-color: color-mix(in srgb, var(--success) 45%, var(--border));
    background: color-mix(in srgb, var(--success) 10%, transparent);
  }

  .provider-status-health-state.is-degraded {
    color: var(--warning);
    border-color: color-mix(in srgb, var(--warning) 48%, var(--border));
    background: color-mix(in srgb, var(--warning) 26%, var(--bg-surface));
  }

  .provider-status-health-state.is-unhealthy {
    color: var(--danger);
    border-color: color-mix(in srgb, var(--danger) 45%, var(--border));
    background: color-mix(in srgb, var(--danger) 10%, transparent);
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
</style>
