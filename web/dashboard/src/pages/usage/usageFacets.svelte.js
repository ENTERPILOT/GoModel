// Usage filter dropdown choices. Facet options follow the faceted-search
// rule: each facet's choices honor every active filter except its own, so a
// selected value never erases its alternatives. Selected values always stay
// listed so they can be unchecked.

import { getJSON, isAbortError } from "$lib/api/client.js";
import { providerDisplayValue } from "$lib/utils/format.js";
import {
  authKeyInactiveIDs,
  authKeyNameMap,
  authKeyOptionList,
  facetOptionList,
  hiddenAuthKeyCount,
  hiddenUserPathCount,
  knownUserPaths,
  userPathOptionList,
} from "./usage-filter-options.js";
import { endpointOptionsWithSelection, usageEndpointOptions } from "./usage-endpoints.js";
import { usageFilters } from "./usageFilters.svelte.js";
import { usageApiQuery } from "./usage-view.js";

function emptyOptions() {
  return { models: [], providers: [], labels: [], endpoints: [], userPaths: [] };
}

class UsageFacetsState {
  options = $state(emptyOptions());
  // Managed auth key names by id, so the API key filter, log column and
  // breakdown can show names instead of ids. Empty when unavailable.
  authKeyNames = $state({});
  // Ids of inactive (deactivated or expired) keys, hidden from the API key
  // filter unless showInactiveKeys. Names stay in authKeyNames for display.
  inactiveAuthKeys = $state({});
  showInactiveKeys = $state(false);
  // Paths with a policy or an active key (from /admin/users); the user path
  // filter hides other paths unless showInactivePaths. Null when the users
  // list is unavailable, which shows every path.
  knownUserPaths = $state(null);
  showInactivePaths = $state(false);

  #controllers = {};

  #start(key) {
    this.#controllers[key]?.abort();
    const controller = new AbortController();
    this.#controllers[key] = controller;
    return controller;
  }

  #finish(key, controller) {
    if (this.#controllers[key] === controller) this.#controllers[key] = null;
  }

  modelOptions() {
    return facetOptionList(this.options.models, usageFilters.models);
  }

  providerOptions() {
    return facetOptionList(this.options.providers, usageFilters.providers);
  }

  labelOptions() {
    return facetOptionList(this.options.labels, usageFilters.labels);
  }

  authKeyOptions() {
    return authKeyOptionList(this.authKeyNames, usageFilters.authKeys, {
      inactive: this.inactiveAuthKeys,
      showInactive: this.showInactiveKeys,
    });
  }

  hiddenAuthKeyCount() {
    return hiddenAuthKeyCount(this.authKeyNames, usageFilters.authKeys, this.inactiveAuthKeys);
  }

  userPathOptions() {
    return userPathOptionList(this.options.userPaths, usageFilters.userPaths, {
      known: this.knownUserPaths,
      showInactive: this.showInactivePaths,
    });
  }

  hiddenUserPathCount() {
    return hiddenUserPathCount(this.options.userPaths, usageFilters.userPaths, this.knownUserPaths);
  }

  endpointOptions() {
    return endpointOptionsWithSelection(this.options.endpoints, usageFilters.endpoints);
  }

  async fetch() {
    const controller = this.#start("facets");
    try {
      const fetchRows = async (endpoint, excludeParam) => {
        const result = await getJSON(endpoint + "?" + usageApiQuery(excludeParam), {
          label: "usage facet options",
          signal: controller.signal,
        });
        if (result.stale) return null;
        if (!result.ok) return [];
        return Array.isArray(result.data) ? result.data : [];
      };
      // Without a model or provider filter, the two by-model queries are
      // identical; fetch once and reuse.
      const modelRowsPromise = fetchRows("/admin/usage/models", "model");
      const sharedByModel = !usageFilters.models.length && !usageFilters.providers.length;
      const results = await Promise.all([
        modelRowsPromise,
        sharedByModel ? modelRowsPromise : fetchRows("/admin/usage/models", "provider"),
        fetchRows("/admin/usage/labels", "label"),
        fetchRows("/admin/usage/endpoints", "endpoint"),
        fetchRows("/admin/usage/user-paths", "user_path"),
      ]);
      if (controller.signal.aborted || results.includes(null)) return;
      const [modelRows, providerRows, labelRows, endpointRows, userPathRows] = results;
      this.options = {
        models: modelRows.map((row) => row && row.model).filter(Boolean),
        providers: providerRows.map((row) => providerDisplayValue(row)).filter(Boolean),
        labels: labelRows.map((row) => row && row.label).filter(Boolean),
        endpoints: usageEndpointOptions(endpointRows),
        userPaths: userPathRows.map((row) => row && row.user_path).filter(Boolean),
      };
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch usage facet options:", e);
      this.options = emptyOptions();
    } finally {
      this.#finish("facets", controller);
    }
  }

  // Key names change rarely, so they load when the page opens rather than on
  // every filter change. The list is already scoped to the caller.
  async fetchAuthKeyNames() {
    const controller = this.#start("authKeys");
    try {
      const result = await getJSON("/admin/auth-keys", {
        label: "usage auth key names",
        signal: controller.signal,
      });
      if (result.stale || controller.signal.aborted) return;
      const keys = result.ok && Array.isArray(result.data) ? result.data : [];
      this.authKeyNames = authKeyNameMap(keys);
      this.inactiveAuthKeys = authKeyInactiveIDs(keys);
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch auth key names:", e);
      this.authKeyNames = {};
      this.inactiveAuthKeys = {};
    } finally {
      this.#finish("authKeys", controller);
    }
  }

  // Policies and key bindings change rarely too, so the known user paths load
  // with the key names. Any failure (feature off, no access, network) leaves
  // the set null so the filter falls back to every facet path.
  async fetchKnownUserPaths() {
    const controller = this.#start("users");
    try {
      const result = await getJSON("/admin/users", {
        label: "usage known user paths",
        signal: controller.signal,
      });
      if (result.stale || controller.signal.aborted) return;
      const nodes = result.ok && result.data && result.data.users;
      this.knownUserPaths = Array.isArray(nodes) ? knownUserPaths(nodes) : null;
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch known user paths:", e);
      this.knownUserPaths = null;
    } finally {
      this.#finish("users", controller);
    }
  }
}

export const usageFacets = new UsageFacetsState();
