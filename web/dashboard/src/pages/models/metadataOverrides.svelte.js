// Model metadata overrides state for the Models page: the stored overrides
// (/admin/model-metadata-overrides) and the editor opened from a model row.
// A save or removal re-enriches the gateway's catalog, so the inventory and
// category counts are refetched afterwards.

import { loadAdminList, sendAdminMutation } from "$lib/api/adminCrud.js";
import { flash } from "$lib/stores/flash.svelte.js";
import { modelsStore } from "$lib/stores/models.svelte.js";
import * as m from "$lib/paraglide/messages.js";
import {
  buildMetadataOverridePayload,
  findMetadataOverride,
  formFromOverride,
  inheritedTokenLimits,
  metadataOverrideSelector,
} from "./metadataOverridesLogic.js";

const PATH = "/admin/model-metadata-overrides";

class MetadataOverridesStore {
  // True once the stored overrides are loaded. Editing waits for it: a form
  // opened while the list is loading or failed to load starts blank, and
  // saving it would replace the stored override.
  loaded = $state(false);
  loadError = $state("");
  overrides = $state([]);
  formOpen = $state(false);
  submitting = $state(false);
  error = $state("");
  hasExisting = $state(false);
  displayName = $state("");
  selector = $state("");
  // The token limits the row inherits, for placeholders.
  inherited = $state({});
  form = $state(formFromOverride(null));

  #fetchSeq = 0;

  async fetchOverrides() {
    const seq = ++this.#fetchSeq;
    const outcome = await loadAdminList(PATH, {
      label: "model metadata overrides",
      errorFallback: m.models_metadata_load_failed(),
    });
    // A slower earlier request must not replace a newer list.
    if (outcome.status === "stale" || seq !== this.#fetchSeq) return;
    this.loaded = outcome.status === "ok";
    this.loadError = outcome.error;
    this.overrides = outcome.items;
  }

  hasOverride(row) {
    return Boolean(findMetadataOverride(this.overrides, metadataOverrideSelector(row)));
  }

  canEdit(row) {
    return this.loaded && Boolean(row) && !row.is_alias && Boolean(metadataOverrideSelector(row));
  }

  open(row) {
    if (!this.canEdit(row)) return;
    const selector = metadataOverrideSelector(row);
    const override = findMetadataOverride(this.overrides, selector);
    this.selector = selector;
    this.displayName = row.display_name || selector;
    this.inherited = inheritedTokenLimits(row.model && row.model.metadata, override);
    this.hasExisting = Boolean(override);
    this.form = formFromOverride(override);
    this.error = "";
    this.formOpen = true;
  }

  close() {
    this.formOpen = false;
    this.submitting = false;
    this.error = "";
    this.inherited = {};
  }

  toggleCategory(category, checked) {
    const next = this.form.categories.filter((item) => item !== category);
    if (checked) next.push(category);
    this.form.categories = next;
  }

  async submit() {
    const payload = buildMetadataOverridePayload(this.form);
    if (payload.error) {
      this.error = payload.error;
      return;
    }
    await this.#mutate("PUT", { selector: this.selector, metadata: payload.metadata }, m.models_metadata_saved(), m.models_metadata_save_failed());
  }

  async remove() {
    if (!this.hasExisting || !window.confirm(m.models_metadata_remove_confirm({ selector: this.selector }))) {
      return;
    }
    await this.#mutate("DELETE", { selector: this.selector }, m.models_metadata_removed(), m.models_metadata_remove_failed());
  }

  async #mutate(method, body, successMessage, errorFallback) {
    this.submitting = true;
    this.error = "";
    try {
      const outcome = await sendAdminMutation(PATH, method, body, {
        label: "model metadata override",
        errorFallback,
        unavailableMessage: m.models_metadata_feature_unavailable(),
      });
      if (outcome.status === "stale") return;
      // A 404 on removal means it is already gone, which is the goal.
      const gone = method === "DELETE" && outcome.result && outcome.result.status === 404;
      if (outcome.status !== "ok" && !gone) {
        if (outcome.status === "unavailable") this.loaded = false;
        this.error = outcome.error;
        return;
      }
      this.close();
      flash.success(successMessage);
      void Promise.all([this.fetchOverrides(), modelsStore.fetchModels(), modelsStore.fetchCategories()]);
    } finally {
      this.submitting = false;
    }
  }
}

export const metadataOverrides = new MetadataOverridesStore();
