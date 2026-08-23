// Sidebar navigation registry: the ordered list of dashboard pages with
// their lucide icons and optional runtime-config visibility gates.
// Each item is { page, label, icon, visible? }: `page` is the route id under
// /admin/dashboard/{page} (see $lib/stores/router), `label` is a generated
// Paraglide message function, `icon` is a lucide icon
// imported below and passed to the Icon atom, and `visible` a feature gate —
// it reads the runtimeConfig runes store, so call it inside a reactive
// context (Sidebar's $derived) to re-filter when flags load.

import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import * as m from "$lib/paraglide/messages.js";
import {
  Box,
  ChartColumn,
  FlaskConical,
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
  { page: "overview", label: m.navigation_overview, icon: LayoutDashboard },
  { page: "providers-config", label: m.navigation_providers, icon: ServerCog },
  { page: "models", label: m.navigation_models, icon: Box },
  { page: "playground", label: m.navigation_playground, icon: FlaskConical },
  { page: "audit-logs", label: m.navigation_audit_logs, icon: History },
  { page: "usage", label: m.navigation_usage, icon: ChartColumn },
  {
    page: "budgets",
    label: m.navigation_budgets,
    icon: Wallet,
    visible: () => runtimeConfig.budgetsVisible(),
  },
  {
    page: "rate-limits",
    label: m.navigation_rate_limits,
    icon: Gauge,
    visible: () => runtimeConfig.rateLimitsVisible(),
  },
  { page: "auth-keys", label: m.navigation_api_keys, icon: KeyRound },
  { page: "workflows", label: m.navigation_workflows, icon: Workflow },
  {
    page: "guardrails",
    label: m.navigation_guardrails_beta,
    icon: ShieldCheck,
    visible: () => runtimeConfig.guardrailsVisible(),
  },
  {
    page: "mcp-servers",
    label: m.navigation_mcp_servers,
    icon: Plug,
    visible: () => runtimeConfig.mcpVisible(),
  },
  { page: "settings", label: m.navigation_settings, icon: Settings },
];
