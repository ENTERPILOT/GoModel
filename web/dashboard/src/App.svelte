<script>
  import Sidebar from "$lib/components/organisms/Sidebar.svelte";
  import AuthDialog from "$lib/components/organisms/AuthDialog.svelte";
  import TypedConfirmationDialog from "$lib/components/organisms/TypedConfirmationDialog.svelte";
  import FlashMessages from "$lib/components/organisms/FlashMessages.svelte";
  import DemoModeBanner from "$lib/components/molecules/DemoModeBanner.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { themeStore, sidebar, modals } from "$lib/stores/ui.svelte.js";
  import { timezone } from "$lib/stores/timezone.svelte.js";
  import { dateRange } from "$lib/stores/dateRange.svelte.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import { modelsStore } from "$lib/stores/models.svelte.js";
  import { syncDocumentLocale } from "$lib/i18n/locale.js";

  import OverviewPage from "$pages/overview/OverviewPage.svelte";
  import UsagePage from "$pages/usage/UsagePage.svelte";
  import BudgetsPage from "$pages/budgets/BudgetsPage.svelte";
  import RateLimitsPage from "$pages/rate-limits/RateLimitsPage.svelte";
  import ModelsPage from "$pages/models/ModelsPage.svelte";
  import WorkflowsPage from "$pages/workflows/WorkflowsPage.svelte";
  import AuditLogsPage from "$pages/audit-logs/AuditLogsPage.svelte";
  import GuardrailsPage from "$pages/guardrails/GuardrailsPage.svelte";
  import McpServersPage from "$pages/mcp-servers/McpServersPage.svelte";
  import ProvidersConfigPage from "$pages/providers-config/ProvidersConfigPage.svelte";
  import AuthKeysPage from "$pages/auth-keys/AuthKeysPage.svelte";
  import SettingsPage from "$pages/settings/SettingsPage.svelte";
  import ConversationDrawer from "$pages/audit-logs/ConversationDrawer.svelte";
  import { conversationDrawer } from "$pages/audit-logs/conversationDrawer.svelte.js";

  const pageComponents = {
    overview: OverviewPage,
    usage: UsagePage,
    budgets: BudgetsPage,
    "rate-limits": RateLimitsPage,
    models: ModelsPage,
    workflows: WorkflowsPage,
    "audit-logs": AuditLogsPage,
    guardrails: GuardrailsPage,
    "mcp-servers": McpServersPage,
    "providers-config": ProvidersConfigPage,
    "auth-keys": AuthKeysPage,
    settings: SettingsPage,
  };

  syncDocumentLocale();
  timezone.init();
  dateRange.init(); // after timezone.init(): "today" is timezone dependent
  auth.init();
  themeStore.init();
  sidebar.init();
  router.init();

  // Shared inventory refetch on boot and whenever the API key changes.
  $effect(() => {
    void auth.refreshTick;
    runtimeConfig.fetch();
    modelsStore.fetchModels();
    modelsStore.fetchCategories();
  });

  // Body-level modal class (scroll lock while any overlay dialog is open).
  $effect(() => {
    document.body.classList.toggle("dashboard-modal-open", modals.anyOpen);
  });

  const PageComponent = $derived(pageComponents[router.page] || OverviewPage);
</script>

<Sidebar />
<main
  id="dashboard-content"
  class="content"
  class:interactions-open={conversationDrawer.conversationOpen}
>
  <DemoModeBanner />
  <PageComponent />
</main>
<ConversationDrawer />
<AuthDialog />
<TypedConfirmationDialog />
<FlashMessages />
