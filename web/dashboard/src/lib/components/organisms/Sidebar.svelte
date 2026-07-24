<script>
  import Icon from "$lib/components/atoms/Icon.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { themeStore, sidebar } from "$lib/stores/ui.svelte.js";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
  import { gomodelPath } from "$lib/api/paths.js";

  const navItems = $derived(
    [
      { page: "overview", label: "Overview", icon: "layout-dashboard" },
      { page: "providers-config", label: "Providers", icon: "server-cog" },
      { page: "models", label: "Models", icon: "box" },
      { page: "audit-logs", label: "Audit Logs", icon: "history" },
      { page: "usage", label: "Usage", icon: "chart-column" },
      {
        page: "budgets",
        label: "Budgets",
        icon: "wallet",
        visible: runtimeConfig.budgetsVisible(),
      },
      {
        page: "rate-limits",
        label: "Rate Limits",
        icon: "gauge",
        visible: runtimeConfig.rateLimitsVisible(),
      },
      { page: "auth-keys", label: "API Keys", icon: "key-round" },
      { page: "workflows", label: "Workflows", icon: "workflow" },
      {
        page: "guardrails",
        label: "Guardrails (experimental)",
        icon: "shield-check",
        visible: runtimeConfig.guardrailsVisible(),
      },
      {
        page: "mcp-servers",
        label: "MCP Servers",
        icon: "plug",
        visible: runtimeConfig.mcpVisible(),
      },
      { page: "settings", label: "Settings", icon: "settings" },
    ].filter((item) => item.visible !== false),
  );

  const themeTitle = $derived(
    themeStore.theme === "light"
      ? "Light theme"
      : themeStore.theme === "dark"
        ? "Dark theme"
        : "System theme",
  );
</script>

<aside class="sidebar" class:sidebar-collapsed={sidebar.collapsed}>
  <div class="sidebar-header">
    <div class="sidebar-logo">
      <svg viewBox="0 0 30 30" fill="none" xmlns="http://www.w3.org/2000/svg">
        <path d="M15 3L25.39 9L25.39 21L15 27L4.61 21L4.61 9Z" stroke="currentColor" stroke-width="2" fill="none"/>
        <circle cx="15" cy="15" r="4" fill="currentColor" opacity="0.3"/>
        <path d="M15 9.5L15 6M15 20.5L15 24M19.76 12.25L22.79 10.5M10.24 17.75L7.21 19.5M19.76 17.75L22.79 19.5M10.24 12.25L7.21 10.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
      </svg>
    </div>
    <h1>GoModel</h1>
    <span class="badge">Admin</span>
  </div>
  <nav class="sidebar-nav">
    {#each navItems as item (item.page)}
      <a
        href={gomodelPath("/admin/dashboard/" + item.page)}
        class="nav-item"
        class:active={router.page === item.page}
        title={item.label}
        onclick={(event) => {
          event.preventDefault();
          router.navigate(item.page);
        }}
      >
        <Icon name={item.icon} class="nav-icon" />
        <span>{item.label}</span>
      </a>
    {/each}
  </nav>
  <div class="sidebar-footer">
    <div class="theme-toggle">
      <button
        class="theme-btn"
        class:active={themeStore.theme === "light"}
        onclick={() => themeStore.set("light")}
        title="Light theme"
        aria-label="Light theme"
      >
        <Icon name="sun" class="theme-icon" />
      </button>
      <button
        class="theme-btn"
        class:active={themeStore.theme === "system"}
        onclick={() => themeStore.set("system")}
        title="System theme"
        aria-label="System theme"
      >
        <Icon name="monitor" class="theme-icon" />
      </button>
      <button
        class="theme-btn"
        class:active={themeStore.theme === "dark"}
        onclick={() => themeStore.set("dark")}
        title="Dark theme"
        aria-label="Dark theme"
      >
        <Icon name="moon" class="theme-icon" />
      </button>
    </div>
    <button
      class="theme-toggle-mobile"
      onclick={() => themeStore.toggle()}
      title={themeTitle}
      aria-label={themeTitle}
    >
      {#if themeStore.theme === "light"}
        <Icon name="sun" class="theme-icon" />
      {:else if themeStore.theme === "dark"}
        <Icon name="moon" class="theme-icon" />
      {:else}
        <Icon name="monitor" class="theme-icon" />
      {/if}
    </button>
    {#if auth.needsAuth || auth.hasApiKey()}
      <div class="api-key-section">
        <button
          type="button"
          class="api-key-open-btn"
          onclick={() => auth.openDialog()}
          aria-label={auth.needsAuth ? "Enter API key" : "Change API key"}
        >
          <Icon name="lock-keyhole" class="api-key-open-icon" />
          <span>{auth.needsAuth ? "Enter API key" : "Change API key"}</span>
        </button>
      </div>
    {/if}
  </div>
</aside>
<button
  type="button"
  class="sidebar-toggle"
  class:collapsed={sidebar.collapsed}
  onclick={() => sidebar.toggle()}
  title={sidebar.collapsed ? "Expand sidebar" : "Collapse sidebar"}
  aria-label={sidebar.collapsed ? "Expand sidebar" : "Collapse sidebar"}
  aria-expanded={!sidebar.collapsed}
></button>
