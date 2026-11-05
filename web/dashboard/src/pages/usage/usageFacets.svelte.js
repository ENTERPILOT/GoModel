// Usage filter dropdown choices. Facet options follow the faceted-search
// rule: each facet's choices honor every active filter except its own, so a
// selected value never erases its alternatives. Selected values always stay
// listed so they can be unchecked.

import { getJSON, isAbortError } from "$lib/api/client.js";
import { providerDisplayValue } from "$lib/utils/format.js";
import { authKeyNameMap, authKeyOptionList, facetOptionList } from "./usage-filter-options.js";
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
    return authKeyOptionList(this.authKeyNames, usageFilters.authKeys);
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
      this.authKeyNames = result.ok && Array.isArray(result.data) ? authKeyNameMap(result.data) : {};
    } catch (e) {
      if (isAbortError(e)) return;
      console.error("Failed to fetch auth key names:", e);
      this.authKeyNames = {};
    } finally {
      this.#finish("authKeys", controller);
    }
  }
}

export const usageFacets = new UsageFacetsState();
