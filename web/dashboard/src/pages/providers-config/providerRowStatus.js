// Live status for the Providers page rows: pairs each credential row
// (/admin/provider-credentials) with its entry from /admin/providers/status,
// matched by provider name. Pure so it is testable with node.

import { providerLastErrorText } from "../overview/providersLogic.js";

export function providerStatusByName(providers) {
  const byName = new Map();
  for (const provider of Array.isArray(providers) ? providers : []) {
    const name = String((provider && provider.name) || "").trim();
    if (name) byName.set(name, provider);
  }
  return byName;
}

// providerRowStatus returns what the row's Status cell shows, or null when
// there is no live status to show: the gateway does not report the provider,
// or the row is disabled and so not registered for routing.
export function providerRowStatus(row, statusByName) {
  if (!row || row.enabled === false || !(statusByName instanceof Map)) {
    return null;
  }
  const provider = statusByName.get(String(row.name || "").trim());
  if (!provider) return null;
  const runtime = provider.runtime || {};
  return {
    provider,
    // A stale inventory serves direct requests only and is hidden from the
    // model list, so it counts as 0, as on the Overview card.
    modelCount: runtime.inventory_stale ? 0 : Number(runtime.discovered_model_count || 0),
    lastError: providerLastErrorText(provider),
  };
}
