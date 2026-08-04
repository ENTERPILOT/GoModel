<script>
  import Icon from "$lib/components/atoms/Icon.svelte";
  import GoModelLogo from "$lib/components/atoms/GoModelLogo.svelte";
  import ThemeToggle from "./ThemeToggle.svelte";
  import { router } from "$lib/stores/router.svelte.js";
  import { sidebar } from "$lib/stores/ui.svelte.js";
  import { auth } from "$lib/stores/auth.svelte.js";
  import { gomodelPath } from "$lib/api/paths.js";
  import { NAV_ITEMS } from "./navigation.js";
  import { LockKeyhole } from "lucide";

  // Visibility gates read the runtimeConfig store, so this re-filters when
  // the flags load.
  const navItems = $derived(
    NAV_ITEMS.filter((item) => !item.visible || item.visible()),
  );
</script>

<aside class="sidebar" class:sidebar-collapsed={sidebar.collapsed}>
  <div class="sidebar-header">
    <div class="sidebar-logo">
      <GoModelLogo />
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
        <Icon icon={item.icon} class="nav-icon" />
        <span>{item.label}</span>
      </a>
    {/each}
  </nav>
  <div class="sidebar-footer">
    <ThemeToggle compact={sidebar.collapsed} />
    {#if auth.needsAuth || auth.hasApiKey()}
      <div class="api-key-section">
        <button
          type="button"
          class="api-key-open-btn"
          onclick={() => auth.openDialog()}
          aria-label={auth.needsAuth ? "Enter API key" : "Change API key"}
        >
          <Icon icon={LockKeyhole} class="api-key-open-icon" />
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

<style>
.sidebar {
    flex: 0 0 var(--sidebar-width);
    width: var(--sidebar-width);
    background: var(--bg-surface);
    border-right: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    position: sticky;
    top: 0;
    max-height: 100vh;
    overflow-y: auto;
    -webkit-overflow-scrolling: touch;
    z-index: 10;
    transition:
      flex-basis 0.2s,
      width 0.2s;
  }

.sidebar-header {
    padding: 20px;
    border-bottom: 1px solid var(--border);
    display: flex;
    align-items: center;
    gap: 10px;
  }

.sidebar-logo {
    width: 28px;
    height: 28px;
    flex-shrink: 0;
    color: var(--accent);
  }

.sidebar-logo :global(svg) {
    width: 100%;
    height: 100%;
  }

.sidebar-header :global(h1) {
    font-size: 18px;
    font-weight: 700;
    letter-spacing: -0.3px;
  }

.sidebar-nav {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 12px;
    flex: 1;
  }

.nav-item {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 12px;
    border-radius: var(--radius);
    color: var(--text-muted);
    text-decoration: none;
    font-size: 14px;
    font-weight: 500;
    transition: all 0.15s;
  }

.nav-item:hover {
    background: var(--bg-surface-hover);
    color: var(--text);
  }

.nav-item.active {
    background: var(--accent);
    color: #fff;
  }

.sidebar-footer {
    padding: 16px;
    border-top: 1px solid var(--border);
  }

.api-key-section {
    display: grid;
    gap: 8px;
  }

.api-key-open-btn {
    width: 100%;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    padding: 8px 10px;
    background: transparent;
    border: 1px solid var(--accent);
    border-radius: var(--radius);
    color: var(--accent);
    font-size: 13px;
    font-family: inherit;
    font-weight: 600;
    cursor: pointer;
    transition:
      background-color 0.15s,
      border-color 0.15s;
  }

.api-key-open-btn:hover {
    background: color-mix(in srgb, var(--accent) 10%, transparent);
    border-color: color-mix(in srgb, var(--accent) 78%, var(--text));
    color: color-mix(in srgb, var(--accent) 78%, var(--text));
  }

.api-key-open-btn:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--accent) 36%, transparent);
    outline-offset: 2px;
  }

/* Sidebar toggle handle */
.sidebar-toggle {
    flex: 0 0 6px;
    position: sticky;
    top: 0;
    width: 6px;
    height: 100vh;
    padding: 0;
    background: transparent;
    border: none;
    cursor: w-resize;
    z-index: 11;
    transition: background 0.15s;
  }

.sidebar-toggle:hover {
    background: color-mix(in srgb, var(--accent) 15%, transparent);
  }

.sidebar-toggle.collapsed {
    cursor: e-resize;
  }

.sidebar-toggle:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--accent) 36%, transparent);
    outline-offset: 2px;
  }

/* Collapsed sidebar (desktop) */
.sidebar.sidebar-collapsed {
    flex-basis: 60px;
    width: 60px;
  }

.sidebar.sidebar-collapsed .sidebar-header {
    justify-content: center;
    padding: 16px;
  }

.sidebar.sidebar-collapsed .sidebar-header :global(h1), .sidebar.sidebar-collapsed :global(.badge) {
    display: none;
  }

.sidebar.sidebar-collapsed .sidebar-nav .nav-item {
    justify-content: center;
    padding: 10px;
  }

.sidebar.sidebar-collapsed .sidebar-nav .nav-item :global(span) {
    display: none;
  }

.sidebar.sidebar-collapsed .sidebar-footer {
    padding: 8px;
  }

.sidebar.sidebar-collapsed .sidebar-footer .api-key-section {
    display: none;
  }

@media (max-width: 768px) {
  .sidebar {
          width: 60px;
          flex-basis: 60px;
        }

  .sidebar-header {
          justify-content: center;
          padding: 16px;
        }

  .sidebar-header :global(h1) {
          display: none;
        }

  .sidebar-nav .nav-item {
          justify-content: center;
          padding: 10px;
        }

  .sidebar-nav .nav-item :global(span) {
          display: none;
        }

  .sidebar-footer {
          display: grid;
          gap: 8px;
          padding: 8px;
        }

  .sidebar-footer .api-key-section {
          display: grid;
        }

  .sidebar.sidebar-collapsed .sidebar-footer .api-key-section { display: grid; }

  .sidebar-footer .api-key-open-btn {
          width: 36px;
          height: 36px;
          min-height: 36px;
          justify-self: center;
          padding: 0;
        }

  .sidebar-footer .api-key-open-btn :global(span) {
          display: none;
        }

  .sidebar-toggle {
          display: none;
        }
}
</style>
