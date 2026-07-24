<script>
  // One provider card in the Providers Overview grid: status pill, runtime
  // meta, and the expandable details (reason, request health, config).
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
          <div class="provider-status-config-row">
            <span class="provider-status-config-label">Recent Requests</span>
            <span class="provider-status-config-value"
            >{providerRecentTrafficSummary(provider)}</span>
          </div>
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
        {#if provider.config?.base_url}
          <div class="provider-status-config-row">
            <span class="provider-status-config-label">Base URL</span>
            <code class="provider-status-config-value">{provider.config.base_url}</code>
          </div>
        {/if}
        {#if provider.config?.api_version}
          <div class="provider-status-config-row">
            <span class="provider-status-config-label">API Version</span>
            <code class="provider-status-config-value">{provider.config.api_version}</code>
          </div>
        {/if}
        <div class="provider-status-config-row">
          <span class="provider-status-config-label">Configured Models</span>
          <span class="provider-status-config-value">{providerModelsSummary(provider)}</span>
        </div>
        <div class="provider-status-config-row">
          <span class="provider-status-config-label">Retry</span>
          <span class="provider-status-config-value">{providerRetrySummary(provider)}</span>
        </div>
        <div class="provider-status-config-row">
          <span class="provider-status-config-label">Circuit Breaker</span>
          <span class="provider-status-config-value"
          >{providerCircuitBreakerSummary(provider)}</span>
        </div>
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
    <svg
      class="provider-status-card-toggle-icon"
      width="16"
      height="16"
      viewBox="0 0 16 16"
      fill="none"
      aria-hidden="true"
    >
      <path
        d="M4 6l4 4 4-4"
        stroke="currentColor"
        stroke-width="1.8"
        stroke-linecap="round"
        stroke-linejoin="round"
      />
    </svg>
  </button>
</article>
