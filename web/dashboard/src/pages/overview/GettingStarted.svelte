<script>
  // First-run checklist: add a provider, see its models, send a request. It
  // reads what the overview already fetched, links each open step to the page
  // that completes it, and disappears once every step is done or the
  // operator hides it (remembered in localStorage).
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import { demoMode } from "$lib/api/paths.js";
  import { browserStorage } from "$lib/utils/storage.js";
  import { providerStatusState, calendarState } from "./overviewState.svelte.js";
  import {
    gettingStartedSteps,
    gettingStartedProgress,
    gettingStartedVisible,
    loadGettingStartedDismissed,
    saveGettingStartedDismissed,
  } from "./gettingStartedLogic.js";
  import { Circle, CircleCheck } from "lucide";
  import * as m from "$lib/paraglide/messages.js";

  const COPY = {
    provider: {
      title: m.overview_setup_provider_title,
      help: m.overview_setup_provider_help,
      page: m.navigation_providers,
    },
    models: {
      title: m.overview_setup_models_title,
      help: m.overview_setup_models_help,
      page: m.navigation_models,
    },
    request: {
      title: m.overview_setup_request_title,
      help: m.overview_setup_request_help,
      page: m.navigation_playground,
    },
  };

  let dismissed = $state(loadGettingStartedDismissed(browserStorage()));

  const usageTracked = $derived(runtimeConfig.usageVisible());
  const steps = $derived(
    gettingStartedSteps({
      providers: providerStatusState.status.providers,
      dailyUsage: calendarState.data,
      usageTracked,
    }),
  );
  const progress = $derived(gettingStartedProgress(steps));
  const nextStep = $derived(steps.find((step) => !step.done));
  const visible = $derived(
    gettingStartedVisible({
      loaded:
        runtimeConfig.loaded &&
        providerStatusState.loadSucceeded &&
        (!usageTracked || calendarState.loadSucceeded),
      dismissed,
      demo: demoMode(),
      steps,
    }),
  );

  function dismiss() {
    dismissed = true;
    saveGettingStartedDismissed(browserStorage());
  }
</script>

{#if visible}
  <section class="getting-started" aria-labelledby="getting-started-title">
    <div class="getting-started-header">
      <div>
        <h3 id="getting-started-title">{m.overview_setup_title()}</h3>
        <p class="getting-started-intro">{m.overview_setup_intro()}</p>
      </div>
      <div class="getting-started-actions">
        <span class="getting-started-progress">
          {m.overview_setup_progress({
            done: progress.done,
            total: progress.total,
          })}
        </span>
        <button type="button" class="btn" onclick={dismiss}>
          {m.overview_setup_dismiss()}
        </button>
      </div>
    </div>
    <ol class="getting-started-steps">
      {#each steps as step (step.id)}
        <li class="getting-started-step" class:is-done={step.done}>
          <Icon
            icon={step.done ? CircleCheck : Circle}
            class="getting-started-step-icon"
          />
          <div class="getting-started-step-copy">
            <strong>{COPY[step.id].title()}</strong>
            <span>{COPY[step.id].help()}</span>
          </div>
          {#if step.done}
            <span class="getting-started-step-done">{m.overview_setup_done()}</span>
          {:else}
            <button
              type="button"
              class="btn"
              class:btn-primary={step === nextStep}
              onclick={() => router.navigate(step.page)}
            >
              {m.overview_setup_open({ page: COPY[step.id].page() })}
            </button>
          {/if}
        </li>
      {/each}
    </ol>
  </section>
{/if}

<style>
  .getting-started {
    margin-bottom: var(--space-stack);
    padding: var(--space-block);
    border: 1px solid color-mix(in srgb, var(--accent) 45%, var(--border));
    border-radius: var(--radius);
    background: var(--bg-surface);
  }

  .getting-started-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 12px;
    margin-bottom: 16px;
  }

  .getting-started-header h3 {
    margin: 0 0 4px;
    font-size: 16px;
    font-weight: 600;
  }

  .getting-started-intro {
    margin: 0;
    color: var(--text-muted);
    font-size: 13px;
  }

  .getting-started-actions {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .getting-started-progress {
    color: var(--text-muted);
    font-size: 13px;
    white-space: nowrap;
  }

  .getting-started-steps {
    display: grid;
    gap: 12px;
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .getting-started-step {
    display: flex;
    align-items: flex-start;
    flex-wrap: wrap;
    gap: 12px;
  }

  .getting-started-step :global(.getting-started-step-icon) {
    flex: 0 0 18px;
    width: 18px;
    height: 18px;
    margin-top: 2px;
    color: var(--text-muted);
  }

  .getting-started-step.is-done :global(.getting-started-step-icon) {
    color: var(--success);
  }

  .getting-started-step-copy {
    display: grid;
    flex: 1;
    gap: 2px;
    min-width: 200px;
    font-size: 13px;
  }

  .getting-started-step-copy span,
  .getting-started-step-done {
    color: var(--text-muted);
  }

  .getting-started-step-done {
    font-size: 13px;
    white-space: nowrap;
  }
</style>
