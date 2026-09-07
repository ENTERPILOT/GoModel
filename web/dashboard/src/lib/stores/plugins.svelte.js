// Loaded plugins from GET /admin/plugins, shared by the Plugins page and the
// virtual-model editor (route strategy fields). ensureLoaded() shares the
// in-flight request like runtimeConfig; a 404 (an older gateway without the
// endpoint) or 503 (plugin system disabled) reads as "no plugins" and clears
// `available`, while a failed request leaves the store unloaded so the next
// caller retries.

import { loadAdminList } from "$lib/api/adminCrud.js";
import { normalizePlugins, pluginByName, pluginRouteFields, sortPlugins } from "$lib/utils/plugins.js";

class PluginsStore {
  plugins = $state([]);
  loaded = $state(false);
  loading = $state(false);
  available = $state(true);
  #inflight = null;

  byName(name) {
    return pluginByName(this.plugins, name);
  }

  routeFields(name) {
    return pluginRouteFields(this.plugins, name);
  }

  fetch() {
    if (this.#inflight) {
      return this.#inflight;
    }
    this.#inflight = this.#load().finally(() => {
      this.#inflight = null;
    });
    return this.#inflight;
  }

  async ensureLoaded() {
    if (this.#inflight) {
      await this.#inflight;
      return;
    }
    if (this.loaded) {
      return;
    }
    await this.fetch();
  }

  async #load() {
    this.loading = true;
    try {
      const outcome = await loadAdminList("/admin/plugins", {
        label: "plugins",
        unavailableStatuses: [404, 503],
        normalize: normalizePlugins,
      });
      if (outcome.status === "stale") return;
      if (outcome.status === "unavailable") {
        this.plugins = [];
        this.loaded = true;
        this.available = false;
        return;
      }
      if (outcome.status === "error") {
        this.plugins = [];
        this.loaded = false;
        return;
      }
      this.plugins = sortPlugins(outcome.items);
      this.loaded = true;
      this.available = true;
    } finally {
      this.loading = false;
    }
  }
}

export const pluginsStore = new PluginsStore();
