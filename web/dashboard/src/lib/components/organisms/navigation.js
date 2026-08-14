// Sidebar navigation registry: the ordered list of dashboard pages with
// their lucide icons and optional runtime-config visibility gates.
// Each item is { page, labelKey, icon, visible? }: `page` is the route id under
// /admin/dashboard/{page} (see $lib/stores/router), `labelKey` is resolved by
// the rendering component, `icon` is a lucide icon
// imported below and passed to the Icon atom, and `visible` a feature gate —
// it reads the runtimeConfig runes store, so call it inside a reactive
// context (Sidebar's $derived) to re-filter when flags load.

import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import {
  Box,
  ChartColumn,
  Gauge,
  History,
  KeyRound,
  LayoutDashboard,
  Plug,
  ServerCog,
  Settings,
  ShieldCheck,
  Wallet,
  Workflow,
} from "lucide";

export const NAV_ITEMS = [
  { page: "overview", labelKey: "navigation.overview", icon: LayoutDashboard },
  { page: "providers-config", labelKey: "navigation.providers", icon: ServerCog },
  { page: "models", labelKey: "navigation.models", icon: Box },
  { page: "audit-logs", labelKey: "navigation.auditLogs", icon: History },
  { page: "usage", labelKey: "navigation.usage", icon: ChartColumn },
  {
    page: "budgets",
    labelKey: "navigation.budgets",
    icon: Wallet,
    visible: () => runtimeConfig.budgetsVisible(),
  },
  {
    page: "rate-limits",
    labelKey: "navigation.rateLimits",
    icon: Gauge,
    visible: () => runtimeConfig.rateLimitsVisible(),
  },
  { page: "auth-keys", labelKey: "navigation.apiKeys", icon: KeyRound },
  { page: "workflows", labelKey: "navigation.workflows", icon: Workflow },
  {
    page: "guardrails",
    labelKey: "navigation.guardrailsExperimental",
    icon: ShieldCheck,
    visible: () => runtimeConfig.guardrailsVisible(),
  },
  {
    page: "mcp-servers",
    labelKey: "navigation.mcpServers",
    icon: Plug,
    visible: () => runtimeConfig.mcpVisible(),
  },
  { page: "settings", labelKey: "navigation.settings", icon: Settings },
];
