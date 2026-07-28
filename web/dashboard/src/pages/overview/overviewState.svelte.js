// Overview page state: provider status (with the "Starting" poll loop and
// card expand preferences), audit stats, the MCP servers summary, and the
// activity-calendar data.

import { getJSON, isAbortError } from "$lib/api/client.ts";
import { dateRange } from "$lib/stores/dateRange.svelte.ts";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.ts";
import {
  emptyProviderStatus,
  providerStatusNeedsPolling,
  providerCardExpanded,
  loadProviderPreferences,
  saveDetailsPreference,
  saveCardOverrides,
  PROVIDER_STATUS_POLL_MS,
} from "./providersLogic.js";
import { emptyAuditStats, normalizeAuditStats } from "./auditStatsLogic.js";
import { browserStorage } from "$lib/utils/storage.ts";

class ProviderStatusState {
  status = $state(emptyProviderStatus());
  loading = $state(false);
  loadedOnce = $state(false);
  detailsExpanded = $state(false);
  cardOverrides = $state({});
  #controller = null;
  #pollTimer = null;
  #prefsLoaded = false;

  initPreferences() {
    if (this.#prefsLoaded) return;
    this.#prefsLoaded = true;
    const prefs = loadProviderPreferences(browserStorage());
    this.detailsExpanded = prefs.detailsExpanded;
    this.cardOverrides = prefs.cardOverrides;
  }

  cardExpanded(provider) {
    return providerCardExpanded(this.cardOverrides, this.detailsExpanded, provider);
  }

  toggleCard(provider) {
    const name = provider && provider.name ? String(provider.name) : "";
    if (!name) return;
    const overrides = { ...this.cardOverrides };
    overrides[name] = !this.cardExpanded(provider);
    this.cardOverrides = overrides;
    saveCardOverrides(browserStorage(), this.cardOverrides);
  }

  toggleDetails() {
    this.detailsExpanded = !this.detailsExpanded;
    // The section-wide switch acts as a master control: drop any per-card
    // overrides so every card follows it again.
    this.cardOverrides = {};
    saveDetailsPreference(browserStorage(), this.detailsExpanded);
    saveCardOverrides(browserStorage(), this.cardOverrides);
  }

  detailsToggleLabel() {
    return this.detailsExpanded ? "Show Details" : "Hide Details";
  }

  async fetch() {
    this.initPreferences();
    if (this.#controller) this.#controller.abort();
    const controller = new AbortController();
    this.#controller = controller;
    this.loading = true;
    try {
      const result = await getJSON("/admin/providers/status", {
        label: "provider status",
        signal: controller.signal,
      });
      if (result.stale || controller.signal.aborted) return;
      if (!result.ok) {
        this.status = emptyProviderStatus();
        this.#clearPoll();
        return;
      }
      const payload =
        result.data && typeof result.data === "object"
          ? result.data
          : emptyProviderStatus();
      if (!payload.summary) {
        payload.summary = emptyProviderStatus().summary;
      }
      if (!Array.isArray(payload.providers)) {
        payload.providers = [];
      }
      this.status = payload;
      this.#schedulePoll();
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch provider status:", e);
      this.status = emptyProviderStatus();
      this.#clearPoll();
    } finally {
      if (this.#controller === controller) {
        this.#controller = null;
        this.loading = false;
        this.loadedOnce = true;
      }
    }
  }

  // Re-probe every 3s while any provider is still "Starting", so the cards
  // settle without a manual refresh.
  #schedulePoll() {
    this.#clearPoll();
    if (!providerStatusNeedsPolling(this.status.providers)) {
      return;
    }
    this.#pollTimer = setTimeout(() => {
      this.#pollTimer = null;
      this.fetch();
    }, PROVIDER_STATUS_POLL_MS);
  }

  #clearPoll() {
    if (this.#pollTimer) {
      clearTimeout(this.#pollTimer);
      this.#pollTimer = null;
    }
  }

  stopPolling() {
    this.#clearPoll();
  }
}

class AuditStatsState {
  stats = $state(emptyAuditStats());
  loading = $state(false);
  #fetchToken = 0;

  async fetch() {
    const requestToken = ++this.#fetchToken;
    this.loading = true;
    try {
      const result = await getJSON("/admin/audit/stats?" + dateRange.queryStr(), {
        label: "audit stats",
      });
      if (result.stale) return;
      if (requestToken !== this.#fetchToken) return;
      if (!result.ok) {
        this.stats = emptyAuditStats();
        return;
      }
      this.stats = normalizeAuditStats(result.data);
    } catch (e) {
      console.error("Failed to fetch audit stats:", e);
      if (requestToken !== this.#fetchToken) return;
      this.stats = emptyAuditStats();
    } finally {
      if (requestToken === this.#fetchToken) {
        this.loading = false;
      }
    }
  }
}

// The overview MCP card only needs the server list for its connected/total
// summary; the MCP module waits for runtime config and skips the request when
// the feature is disabled.
class McpServersState {
  servers = $state([]);
  available = $state(false);
  loading = $state(false);

  async fetch() {
    await runtimeConfig.ensureLoaded();
    if (!runtimeConfig.mcpVisible()) {
      this.available = false;
      this.servers = [];
      return;
    }
    this.loading = true;
    try {
      const result = await getJSON("/admin/mcp-servers", { label: "mcp servers" });
      if (result.stale) return;
      if (result.status === 503 || result.status === 404) {
        this.available = false;
        this.servers = [];
        return;
      }
      this.available = true;
      if (!result.ok) {
        this.servers = [];
        return;
      }
      this.servers = Array.isArray(result.data) ? result.data : [];
    } catch (e) {
      console.error("Failed to fetch MCP servers:", e);
      this.servers = [];
    } finally {
      this.loading = false;
    }
  }
}

// Activity calendar data: always the trailing 365 days regardless of the
// selected reporting window.
class CalendarState {
  data = $state([]);
  mode = $state("tokens");
  loading = $state(false);
  #controller = null;

  async fetch() {
    if (this.#controller) this.#controller.abort();
    const controller = new AbortController();
    this.#controller = controller;
    this.loading = true;
    try {
      const result = await getJSON("/admin/usage/daily?days=365&interval=daily", {
        label: "calendar",
        signal: controller.signal,
      });
      if (result.stale || controller.signal.aborted) return;
      if (!result.ok) {
        this.data = [];
        return;
      }
      this.data = Array.isArray(result.data) ? result.data : [];
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch calendar data:", e);
      this.data = [];
    } finally {
      if (this.#controller === controller) {
        this.#controller = null;
        this.loading = false;
      }
    }
  }
}

export const providerStatusState = new ProviderStatusState();
export const auditStatsState = new AuditStatsState();
export const mcpServersState = new McpServersState();
export const calendarState = new CalendarState();
