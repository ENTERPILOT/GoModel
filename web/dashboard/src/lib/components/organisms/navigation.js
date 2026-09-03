// Sidebar navigation registry: the ordered list of dashboard pages with
// their lucide icons and optional runtime-config visibility gates.
// Each item is { page, label, icon, visible?, notify? }: `page` is the route
// id under /admin/dashboard/{page} (see $lib/stores/router), `label` is a
// generated Paraglide message function, `icon` is a lucide icon
// imported below and passed to the Icon atom, `visible` a feature gate, and
// `notify` marks the item with a dot when it has something waiting. Both
// read runes stores, so call them inside a reactive context (Sidebar's
// $derived and markup) to re-evaluate when the underlying state changes.

import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import { access } from "$lib/stores/access.svelte.js";
import { versionStore } from "$lib/stores/version.svelte.js";
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
  Users,
  Wallet,
  Workflow,
} from "lucide";

// Gateway-wide configuration pages are hidden from scoped admins (a key bound
// to a user path); their endpoints answer 403 for such credentials.
const globalOnly = () => !access.scoped;

export const NAV_ITEMS = [
  { page: "overview", label: m.navigation_overview, icon: LayoutDashboard },
  {
    page: "providers-config",
    label: m.navigation_providers,
    icon: ServerCog,
    visible: globalOnly,
  },
  { page: "models", label: m.navigation_models, icon: Box, visible: globalOnly },
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
  { page: "users", label: m.navigation_users, icon: Users },
  {
    page: "workflows",
    label: m.navigation_workflows,
    icon: Workflow,
    visible: globalOnly,
  },
  {
    page: "guardrails",
    label: m.navigation_guardrails_beta,
    icon: ShieldCheck,
    visible: () => globalOnly() && runtimeConfig.guardrailsVisible(),
  },
  {
    page: "mcp-servers",
    label: m.navigation_mcp_servers,
    icon: Plug,
    visible: () => globalOnly() && runtimeConfig.mcpVisible(),
  },
  {
    page: "settings",
    label: m.navigation_settings,
    icon: Settings,
    // A newer release is announced on the Settings page; the dot is what
    // makes an operator look there.
    notify: () => versionStore.updateAvailable,
  },
];
