<script>
  // Providers Overview section: master details toggle + provider card grid.
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import ProviderStatusCard from "./ProviderStatusCard.svelte";
  import { providerStatusState } from "./overviewState.svelte.js";
  import * as m from "$lib/paraglide/messages.js";

  const providers = $derived(providerStatusState.status.providers);
</script>

{#if providers.length > 0}
  <div
    id="provider-status-section"
    class="provider-status-section"
    tabindex="-1"
  >
    <div class="provider-status-section-header">
      <div>
        <h3>{m.overview_providers_title()}</h3>
      </div>
      <button
        type="button"
        class="provider-status-toggle"
        role="switch"
        aria-checked={providerStatusState.detailsExpanded}
        title={providerStatusState.detailsToggleLabel()}
        onclick={() => providerStatusState.toggleDetails()}
      >
        <span class="provider-status-toggle-copy"
        >{providerStatusState.detailsToggleLabel()}</span>
        <span
          class="provider-status-toggle-track"
          class:is-active={providerStatusState.detailsExpanded}
        >
          <span class="provider-status-toggle-thumb"></span>
        </span>
      </button>
    </div>
    <div class="provider-status-grid">
      {#each providers as provider (provider.name)}
        <ProviderStatusCard {provider} />
      {/each}
    </div>
  </div>
{:else if providerStatusState.loading && !providerStatusState.loadedOnce}
  <div class="provider-status-section provider-status-section-loading">
    <Spinner size={18} label={m.overview_loading_provider_status()} />
  </div>
{/if}

<style>
  /* Loading affordance while the first provider-status fetch is in flight. */
  .provider-status-section-loading {
    display: flex;
    justify-content: center;
    padding: 24px 0;
  }
.provider-status-section-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 16px;
}

.provider-status-toggle {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
  background: var(--bg-surface);
  border: 1px solid var(--border);
  /* Matches .btn and .table-action-btn; only the switch itself stays round. */
  border-radius: 6px;
  color: var(--text);
  font-size: 12px;
  font-family: inherit;
  font-weight: 600;
  cursor: pointer;
  transition:
    background-color 0.18s ease,
    border-color 0.18s ease,
    color 0.18s ease;
}

.provider-status-toggle:hover {
  background: var(--bg-surface-hover);
}

.provider-status-toggle:focus-visible {
  outline: 2px solid color-mix(in srgb, var(--accent) 28%, transparent);
  outline-offset: 2px;
}

.provider-status-toggle-copy {
  white-space: nowrap;
}

.provider-status-toggle-track {
  position: relative;
  width: 34px;
  height: 20px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--text-muted) 35%, var(--border));
  transition: background-color 0.2s ease;
  flex-shrink: 0;
}

.provider-status-toggle-track.is-active {
  background: color-mix(in srgb, var(--accent) 82%, var(--bg-surface));
}

.provider-status-toggle-thumb {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: #fff;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.24);
  transition: transform 0.2s ease;
}

.provider-status-toggle-track.is-active .provider-status-toggle-thumb {
  transform: translateX(14px);
}

.provider-status-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: 16px;
  /* Cards size to their own content; a collapsed card must not stretch to
     match an expanded neighbour in the same row. */
  align-items: start;
}

@media (max-width: 768px) {
  .provider-status-section-header {
      flex-direction: column;
      align-items: flex-start;
    }

  .provider-status-toggle {
      width: 100%;
      justify-content: space-between;
    }
}
</style>
