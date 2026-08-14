<script>
  // Settings page: timezone override, budget reset anchors, tagging header
  // rules, usage pricing recalculation, runtime refresh, and the version
  // footer. Each section fetches its own data when the page becomes active
  // or auth.refreshTick changes.
  import { router } from "$lib/stores/router.svelte.js";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import { appVersion } from "$lib/api/paths.js";
  import LocaleSelector from "$lib/components/molecules/LocaleSelector.svelte";
  import * as m from "$lib/paraglide/messages.js";
  import TimezoneSettings from "./TimezoneSettings.svelte";
  import FailoverSettings from "./FailoverSettings.svelte";
  import BudgetSettings from "./BudgetSettings.svelte";
  import BudgetResetSettings from "./BudgetResetSettings.svelte";
  import TaggingSettings from "./TaggingSettings.svelte";
  import PricingRecalculation from "./PricingRecalculation.svelte";
  import RuntimeRefresh from "./RuntimeRefresh.svelte";
  import RuntimeSettings from "./RuntimeSettings.svelte";

  const PAGE = "settings";

  $effect(() => {
    void auth.refreshTick;
    if (router.page === PAGE) {
      timezone.ensureOptions();
      runtimeConfig.ensureLoaded();
    }
  });
</script>

<div>
  <div class="page-header">
    <div>
      <h2>{m.settings_title()}</h2>
    </div>
  </div>

  <div class="settings-panel">
    <TimezoneSettings />
    <LocaleSelector />
    <FailoverSettings />
    <BudgetSettings />
    <BudgetResetSettings />
    <TaggingSettings />
    <RuntimeSettings />
    <PricingRecalculation />
    <RuntimeRefresh />
  </div>

  <p class="settings-version-footer mono">{appVersion()}</p>
</div>

<style>
  .settings-version-footer {
    margin-top: 24px;
    color: var(--text-muted);
    font-size: 12px;
    text-align: right;
  }
</style>
