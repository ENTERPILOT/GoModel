<script>
  // Guardrails page: definitions library + schema-driven editor. Port of
  // templates/page-guardrails.html + static/js/modules/guardrails.js.
  import AuthBanner from "$lib/components/organisms/AuthBanner.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import { formatNumber } from "$lib/utils/format.js";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import GuardrailList from "./GuardrailList.svelte";
  import GuardrailEditor from "./GuardrailEditor.svelte";
  import { guardrailsStore as store } from "./guardrails.svelte.js";

  const PAGE = "guardrails";

  // Re-fetch when the page becomes active or the API key changes.
  $effect(() => {
    void auth.refreshTick;
    if (router.page === PAGE) {
      runtimeConfig.ensureLoaded();
      store.fetchPage();
    }
  });
</script>

<div>
  <div class="page-header">
    <div>
      <InlineHelpSection
        copyId="guardrails-help-copy"
        label="guardrails help"
        text="Reusable policy objects stored in the database and kept hot in memory for workflow execution."
      >
        {#snippet title()}
          <h2>Guardrails</h2>
        {/snippet}
      </InlineHelpSection>
    </div>
  </div>

  <div class="settings-guardrails-hero">
    <div>
      <p class="settings-kicker">Reusable Policy Objects</p>
      <h3>Guardrail Library</h3>
      <p>
        Store guardrails in the database, keep them hot in memory, and attach
        them to workflows by reference.
      </p>
    </div>
    <div class="settings-guardrails-meta">
      <div class="settings-guardrails-stat">
        <span class="settings-guardrails-stat-label">Instances</span>
        <strong>{formatNumber(store.guardrails.length)}</strong>
      </div>
      <div class="settings-guardrails-stat">
        <span class="settings-guardrails-stat-label">Types</span>
        <strong>{formatNumber(store.types.length)}</strong>
      </div>
    </div>
  </div>

  <AuthBanner />

  {#if !runtimeConfig.guardrailsVisible()}
    <div class="alert alert-warning">
      Runtime guardrail execution is currently off because
      <code>GUARDRAILS_ENABLED</code> is disabled. You can still manage
      definitions here.
    </div>
  {/if}
  {#if !auth.authError && !store.available}
    <div class="alert alert-warning">Guardrails feature is unavailable.</div>
  {/if}
  {#if !auth.authError && store.error && !store.formOpen}
    <div class="alert alert-warning">{store.error}</div>
  {/if}

  <GuardrailEditor />
  <GuardrailList />
</div>

<style>
  .settings-guardrails-hero {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 20px;
    padding: 24px;
    margin-bottom: 20px;
    border: 1px solid color-mix(in srgb, var(--accent) 14%, var(--border));
    border-radius: var(--radius);
    background:
      radial-gradient(
        circle at top right,
        color-mix(in srgb, var(--accent-hover) 18%, transparent),
        transparent 42%
      ),
      radial-gradient(
        circle at bottom left,
        color-mix(in srgb, var(--accent) 16%, transparent),
        transparent 40%
      ),
      var(--bg-surface);
  }

  .settings-kicker {
    margin: 0 0 10px;
    color: var(--accent);
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }

  .settings-guardrails-hero :global(h3) {
    margin-bottom: 8px;
  }

  .settings-guardrails-hero :global(p:last-child) {
    margin-bottom: 0;
    color: var(--text-muted);
  }

  .settings-guardrails-meta {
    display: flex;
    gap: 12px;
  }

  .settings-guardrails-stat {
    min-width: 110px;
    padding: 14px 16px;
    border: 1px solid color-mix(in srgb, var(--accent) 14%, var(--border));
    border-radius: 16px;
    background: color-mix(in srgb, var(--bg-surface-hover) 76%, transparent);
  }

  .settings-guardrails-stat-label {
    display: block;
    margin-bottom: 8px;
    color: var(--text-muted);
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }

  .settings-guardrails-stat :global(strong) {
    font-size: 24px;
    font-weight: 700;
  }

  @media (max-width: 768px) {
    .settings-guardrails-hero {
        flex-direction: column;
      }

    .settings-guardrails-meta {
        width: 100%;
      }

    .settings-guardrails-stat {
        flex: 1 1 0;
      }
  }
</style>
