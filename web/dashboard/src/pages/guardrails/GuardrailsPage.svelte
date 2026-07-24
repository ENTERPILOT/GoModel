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
  {#if !auth.authError && store.notice && !store.error}
    <div class="alert alert-success">{store.notice}</div>
  {/if}

  <GuardrailEditor />
  <GuardrailList />
</div>
