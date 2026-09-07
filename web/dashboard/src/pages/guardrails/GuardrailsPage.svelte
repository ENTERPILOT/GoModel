<script>
  import * as m from "$lib/paraglide/messages.js";
  // Guardrails page: definitions library + schema-driven editor. Port of
  // templates/page-guardrails.html + static/js/modules/guardrails.js.
  import AuthBanner from "$lib/components/organisms/AuthBanner.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import GuardrailList from "./GuardrailList.svelte";
  import GuardrailEditor from "./GuardrailEditor.svelte";
  import PluginList from "./PluginList.svelte";
  import { guardrailsStore as store } from "./guardrails.svelte.js";
  import { pluginsStore } from "$lib/stores/plugins.svelte.js";

  const PAGE = "guardrails";

  // Re-fetch when the page becomes active or the API key changes.
  $effect(() => {
    void auth.refreshTick;
    if (router.page === PAGE) {
      runtimeConfig.ensureLoaded();
      store.fetchPage();
      pluginsStore.fetch();
    }
  });
</script>

<div>
  <div class="page-header">
    <div>
      <InlineHelpSection
        copyId="guardrails-help-copy"
        label={m.guardrails_help_label()}
        text={m.guardrails_help()}
      >
        {#snippet title()}
          <h2>{m.guardrails_title()}</h2>
        {/snippet}
      </InlineHelpSection>
    </div>
  </div>

  <AuthBanner />

  {#if !runtimeConfig.guardrailsVisible()}
    <div class="alert alert-warning">
      {m.guardrails_disabled()}
    </div>
  {/if}
  {#if !auth.authError && !store.available}
    <div class="alert alert-warning">{m.guardrails_unavailable()}</div>
  {/if}
  {#if !auth.authError && store.error && !store.formOpen}
    <div class="alert alert-warning">{store.error}</div>
  {/if}
  {#if !auth.authError && store.typesError && store.typesError !== store.error && !store.formOpen}
    <div class="alert alert-warning">{store.typesError}</div>
  {/if}

  <GuardrailEditor />
  <GuardrailList />
  <PluginList />
</div>
