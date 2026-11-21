// Overview page state: provider status (with the "Starting" poll loop and
// card expand preferences), request stats, the MCP servers summary, and the
// activity-calendar data.

import { getJSON, isAbortError } from "$lib/api/client.js";
import * as m from "$lib/paraglide/messages.js";
import { dateRange } from "$lib/stores/dateRange.svelte.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import {
  emptyProviderStatus,
  isProviderStatusPayload,
  providerStatusNeedsPolling,
  providerCardExpanded,
  loadProviderPreferences,
  saveDetailsPreference,
  saveCardOverrides,
  PROVIDER_STATUS_POLL_MS,
} from "./providersLogic.js";
import { emptyRequestStats, normalizeRequestStats } from "./requestStatsLogic.js";
import { browserStorage } from "$lib/utils/storage.js";

class ProviderStatusState {
  status = $state(emptyProviderStatus());
  loading = $state(false);
  loadedOnce = $state(false);
  // True while `status` is what the gateway answered on this visit, false
  // after a failed or unreadable load: an empty provider list is then
  // unknown, not "none". The page clears it before each load.
  loadSucceeded = $state(false);
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
    return this.detailsExpanded
      ? m.overview_hide_details()
      : m.overview_show_details();
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
        this.loadSucceeded = false;
        this.#clearPoll();
        return;
      }
      const payload =
        result.data && typeof result.data === "object"
          ? result.data
          : emptyProviderStatus();
      const answered = isProviderStatusPayload(result.data);
      if (!payload.summary) {
        payload.summary = emptyProviderStatus().summary;
      }
      if (!Array.isArray(payload.providers)) {
        payload.providers = [];
      }
      this.status = payload;
      this.loadSucceeded = answered;
      this.#schedulePoll();
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch provider status:", e);
      this.status = emptyProviderStatus();
      this.loadSucceeded = false;
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

class RequestStatsState {
  stats = $state(emptyRequestStats());
  loading = $state(false);
  #fetchToken = 0;

  async fetch() {
    const requestToken = ++this.#fetchToken;
    this.loading = true;
    try {
      const result = await getJSON("/admin/request-logs/stats?" + dateRange.queryStr(), {
        label: "request stats",
      });
      if (result.stale) return;
      if (requestToken !== this.#fetchToken) return;
      if (!result.ok) {
        this.stats = emptyRequestStats();
        return;
      }
      this.stats = normalizeRequestStats(result.data);
    } catch (e) {
      console.error("Failed to fetch request stats:", e);
      if (requestToken !== this.#fetchToken) return;
      this.stats = emptyRequestStats();
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
  // True while `data` is what the gateway answered on this visit; see
  // ProviderStatusState.loadSucceeded.
  loadSucceeded = $state(false);
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
        this.loadSucceeded = false;
        return;
      }
      this.data = Array.isArray(result.data) ? result.data : [];
      this.loadSucceeded = Array.isArray(result.data);
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch calendar data:", e);
      this.data = [];
      this.loadSucceeded = false;
    } finally {
      if (this.#controller === controller) {
        this.#controller = null;
        this.loading = false;
      }
    }
  }
}

export const providerStatusState = new ProviderStatusState();
export const requestStatsState = new RequestStatsState();
export const mcpServersState = new McpServersState();
export const calendarState = new CalendarState();
