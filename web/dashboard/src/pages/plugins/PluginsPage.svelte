<script>
  // Plugins page: every plugin type loaded by the gateway (GET /admin/plugins)
  // with its version, hooks, source, and health. The list is fetched when
  // this page is active and again whenever the API key changes.
  import AuthBanner from "$lib/components/organisms/AuthBanner.svelte";
  import InlineHelpSection from "$lib/components/molecules/InlineHelpSection.svelte";
  import LoadingState from "$lib/components/molecules/LoadingState.svelte";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { router } from "$lib/stores/router.svelte.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import { pluginsStore } from "$lib/stores/plugins.svelte.js";
  import PluginList from "./PluginList.svelte";
  import * as m from "$lib/paraglide/messages.js";

  const PAGE = "plugins";

  // Re-fetch when the page becomes active or the API key changes.
  $effect(() => {
    void auth.refreshTick;
    if (router.page === PAGE) {
      runtimeConfig.ensureLoaded();
      pluginsStore.fetch();
    }
  });

  const ready = $derived(!auth.authError && pluginsStore.available && pluginsStore.loaded);
</script>

<div>
  <div class="page-header">
    <div>
      <InlineHelpSection copyId="plugins-help-copy" label={m.plugins_help_label()} text={m.plugins_help()}>
        {#snippet title()}<h2>{m.plugins_title()}</h2>{/snippet}
      </InlineHelpSection>
    </div>
    <div class="page-header-controls">
      {#if ready && pluginsStore.plugins.length > 0}
        <span class="provider-badge">{m.plugins_count({ count: pluginsStore.plugins.length })}</span>
      {/if}
    </div>
  </div>

  <AuthBanner />

  {#if !auth.authError && !pluginsStore.available}
    <div class="alert alert-warning">{m.plugins_unavailable()}</div>
  {/if}
  {#if !auth.authError && pluginsStore.loading && !pluginsStore.loaded}
    <LoadingState label={m.plugins_loading()} />
  {/if}

  {#if ready && pluginsStore.plugins.length > 0}
    <PluginList />
  {/if}
  {#if ready && pluginsStore.plugins.length === 0}
    <p class="empty-state">{m.plugins_empty()}</p>
  {/if}
</div>
