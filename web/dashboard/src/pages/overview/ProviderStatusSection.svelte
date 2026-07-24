<script>
  // Providers Overview section: master details toggle + provider card grid.
  import Spinner from "$lib/components/atoms/Spinner.svelte";
  import ProviderStatusCard from "./ProviderStatusCard.svelte";
  import { providerStatusState } from "./overviewState.svelte.js";

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
        <h3>Providers Overview</h3>
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
    <Spinner size={18} label="Loading provider status" />
  </div>
{/if}

<style>
  /* Loading affordance while the first provider-status fetch is in flight. */
  .provider-status-section-loading {
    display: flex;
    justify-content: center;
    padding: 24px 0;
  }
</style>
