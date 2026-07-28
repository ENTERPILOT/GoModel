// Sidebar navigation registry: the ordered list of dashboard pages with
// their lucide icon names and optional runtime-config visibility gates.
// `visible` reads the runtimeConfig runes store, so call it inside a
// reactive context (Sidebar's $derived) to re-filter when flags load.

import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.ts";

export interface NavItem {
  /** Route id under /admin/dashboard/{page} (see $lib/stores/router). */
  page: string;
  label: string;
  /** kebab-case lucide icon name for the Icon atom. */
  icon: string;
  /** Feature gate; omitted means always visible. Evaluate reactively. */
  visible?: () => boolean;
}

export const NAV_ITEMS: NavItem[] = [
  { page: "overview", label: "Overview", icon: "layout-dashboard" },
  { page: "providers-config", label: "Providers", icon: "server-cog" },
  { page: "models", label: "Models", icon: "box" },
  { page: "audit-logs", label: "Audit Logs", icon: "history" },
  { page: "usage", label: "Usage", icon: "chart-column" },
  {
    page: "budgets",
    label: "Budgets",
    icon: "wallet",
    visible: () => runtimeConfig.budgetsVisible(),
  },
  {
    page: "rate-limits",
    label: "Rate Limits",
    icon: "gauge",
    visible: () => runtimeConfig.rateLimitsVisible(),
  },
  { page: "auth-keys", label: "API Keys", icon: "key-round" },
  { page: "workflows", label: "Workflows", icon: "workflow" },
  {
    page: "guardrails",
    label: "Guardrails (experimental)",
    icon: "shield-check",
    visible: () => runtimeConfig.guardrailsVisible(),
  },
  {
    page: "mcp-servers",
    label: "MCP Servers",
    icon: "plug",
    visible: () => runtimeConfig.mcpVisible(),
  },
  { page: "settings", label: "Settings", icon: "settings" },
];
