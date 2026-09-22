// State of the model details accordion: which rows are open, which metadata
// layer the panels show, and the per-row layers fetched from
// /admin/models/metadata. Keyed by the display row key, so an open panel
// survives inventory refetches, category batching and virtual-model
// mutations that rebuild the rows.

import { getJSON, isAbortError } from "$lib/api/client.js";

class ModelDetailsState {
  #expanded = $state({});
  // The layer every open panel renders ("effective" | "provider" | "catalog"
  // | "config"). Shared across rows so the list can be walked in one view.
  view = $state("effective");
  // Row key -> { status: "loading" | "ok" | "error", layers }. Loaded on
  // demand when a row opens and kept for the page session.
  #layers = $state({});
  #controllers = new Map();

  isExpanded(row) {
    return Boolean(row && row.key && this.#expanded[row.key]);
  }

  toggle(row) {
    if (!row || !row.key) return;
    const next = { ...this.#expanded };
    if (next[row.key]) {
      delete next[row.key];
    } else {
      next[row.key] = true;
    }
    this.#expanded = next;
  }

  layersEntry(row) {
    return row && row.key ? this.#layers[row.key] || null : null;
  }

  // loadLayers fetches the metadata layers of the row's model once. Alias
  // rows carry their target model, so they resolve the same way; a row
  // without a provider (unresolved target) has nothing to fetch.
  async loadLayers(row) {
    const key = row && row.key;
    const provider = row && row.provider_name;
    const model = row && row.model && row.model.id;
    if (!key || !provider || !model) return;
    if (this.#layers[key] || this.#controllers.has(key)) return;

    const controller = new AbortController();
    this.#controllers.set(key, controller);
    this.#setEntry(key, { status: "loading", layers: null });
    try {
      const query =
        "?provider=" + encodeURIComponent(provider) + "&model=" + encodeURIComponent(model);
      const result = await getJSON("/admin/models/metadata" + query, {
        label: "model metadata",
        signal: controller.signal,
      });
      if (result.stale || controller.signal.aborted || result.status === 401) {
        this.#dropEntry(key);
        return;
      }
      if (result.ok && result.data && typeof result.data === "object") {
        this.#setEntry(key, { status: "ok", layers: result.data });
      } else {
        this.#setEntry(key, { status: "error", layers: null });
      }
    } catch (e) {
      if (isAbortError(e)) {
        this.#dropEntry(key);
        return;
      }
      console.error("Failed to fetch model metadata:", e);
      this.#setEntry(key, { status: "error", layers: null });
    } finally {
      if (this.#controllers.get(key) === controller) {
        this.#controllers.delete(key);
      }
    }
  }

  // clearLayers forgets every fetched layer set and cancels loads in flight;
  // the Models page calls it when the inventory is refetched.
  clearLayers() {
    for (const controller of this.#controllers.values()) controller.abort();
    this.#controllers.clear();
    this.#layers = {};
  }

  #setEntry(key, entry) {
    this.#layers = { ...this.#layers, [key]: entry };
  }

  #dropEntry(key) {
    if (!this.#layers[key]) return;
    const next = { ...this.#layers };
    delete next[key];
    this.#layers = next;
  }
}

export const modelDetailsState = new ModelDetailsState();
