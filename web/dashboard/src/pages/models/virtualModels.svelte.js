// Virtual models (redirects/aliases, load balancers, access policies) list
// state for the Models page: fetching, display rows, and the row actions.
// The editor form lives in virtualModelEditor.svelte.js.

import { errorMessage, getJSON, sendJSON } from "$lib/api/client.js";
import { flash } from "$lib/stores/flash.svelte.js";
import * as m from "$lib/paraglide/messages.js";
import { modelsStore } from "$lib/stores/models.svelte.js";
import {
  buildDisplayModels,
  buildGlobalScopeRow,
  filterDisplayModels,
  findModelOverrideView,
  groupDisplayModels,
  rowAccessSelector,
  rowIsManaged,
} from "./displayRows.js";
import {
  GLOBAL_OVERRIDE_SELECTOR,
  modelKeys,
  normalizedAliasName,
  qualifiedModelName,
} from "./modelIdentity.js";
import { computeRenderStep, initialRenderStep } from "./renderBatching.js";
import { modelAccessStateClass, splitVirtualModelViews } from "./routing.js";
import { buildAliasTogglePayload, buildModelTogglePayload } from "./vmForm.js";

class VirtualModelsStore {
  virtualModelsAvailable = $state(true);
  aliases = $state([]);
  modelOverrideViews = $state([]);
  modelRenderLimit = $state(0);
  modelRenderBatchSize = 75;
  modelsRendering = $state(false);
  #renderGeneration = 0;
  aliasLoading = $state(false);
  // Load failures only; mutation feedback goes through the flash store.
  aliasError = $state("");
  rowTogglingKey = $state("");
  rowDeletingKey = $state("");

  // ---- Display rows (derived from the shared model inventory) ----

  displayModels = $derived(
    buildDisplayModels({
      models: modelsStore.models,
      aliases: this.aliases,
      virtualModelsAvailable: this.virtualModelsAvailable,
      activeCategory: modelsStore.activeCategory,
    }),
  );

  displayModelGroups = $derived(
    groupDisplayModels(
      this.displayModels,
      modelsStore.models,
      this.modelOverrideViews,
    ),
  );

  filteredDisplayModels = $derived(
    filterDisplayModels(this.displayModels, modelsStore.filter),
  );

  filteredDisplayModelGroups = $derived.by(() => {
    const filtered = this.filteredDisplayModels;
    const limit = Math.max(
      0,
      Math.min(Number(this.modelRenderLimit || 0), filtered.length),
    );
    if (!modelsStore.filter && limit >= this.displayModels.length) {
      return this.displayModelGroups;
    }
    return groupDisplayModels(
      filtered.slice(0, limit),
      modelsStore.models,
      this.modelOverrideViews,
    );
  });

  // globalScopeRow exposes the global "/" scope as a toggle row, so the global
  // level reuses the same enable/restrict/disable switch as models, aliases,
  // and provider groups.
  globalScopeRow = $derived(
    buildGlobalScopeRow(modelsStore.models, this.modelOverrideViews),
  );

  modelsBusy() {
    return Boolean(modelsStore.loading || this.modelsRendering);
  }

  modelLoadingText() {
    if (modelsStore.loading) {
      return this.displayModels.length > 0
        ? m.models_refreshing()
        : m.models_loading();
    }
    const total = this.filteredDisplayModels.length;
    const visible = Math.min(Number(this.modelRenderLimit || 0), total);
    return m.models_rendering({ visible, total });
  }

  // ---- Incremental render batching (loader paints between batches) ----
  //
  // The Models page owns the lifecycle: an $effect calls
  // restartModelRendering(total) whenever the visible row set changes and
  // stopModelRendering() on teardown. Batches grow the window between
  // animation frames; the generation counter cancels a stale run when a
  // newer restart (or a stop) supersedes it.

  restartModelRendering(total) {
    const generation = ++this.#renderGeneration;
    const step = initialRenderStep(this.modelRenderBatchSize, total);
    this.modelRenderLimit = step.limit;
    this.modelsRendering = step.rendering;
    if (step.rendering) {
      this.#scheduleRenderBatch(generation);
    }
  }

  stopModelRendering() {
    this.#renderGeneration++;
    this.modelsRendering = false;
  }

  #scheduleRenderBatch(generation) {
    const run = () => {
      if (generation !== this.#renderGeneration) {
        return;
      }
      const step = computeRenderStep(
        this.modelRenderLimit,
        this.modelRenderBatchSize,
        this.filteredDisplayModels.length,
      );
      this.modelRenderLimit = step.limit;
      this.modelsRendering = step.rendering;
      if (step.rendering) {
        this.#scheduleRenderBatch(generation);
      }
    };
    if (typeof requestAnimationFrame === "function") {
      requestAnimationFrame(() => setTimeout(run, 0));
    } else {
      setTimeout(run, 0);
    }
  }

  // ---- Fetch ----

  async fetchVirtualModels() {
    this.aliasLoading = true;
    this.aliasError = "";
    try {
      const result = await getJSON("/admin/virtual-models", {
        label: "virtual models",
      });
      if (result.status === 503) {
        this.virtualModelsAvailable = false;
        this.aliases = [];
        this.modelOverrideViews = [];
        return;
      }
      if (result.stale) {
        return;
      }
      this.virtualModelsAvailable = true;
      if (!result.ok) {
        this.aliases = [];
        this.modelOverrideViews = [];
        return;
      }
      const { aliases, policies } = splitVirtualModelViews(result.data);
      this.aliases = aliases;
      this.modelOverrideViews = policies;
    } catch (e) {
      console.error("Failed to fetch virtual models:", e);
      this.aliases = [];
      this.modelOverrideViews = [];
      this.aliasError = m.models_load_failed();
    } finally {
      this.aliasLoading = false;
    }
  }

  // ---- Lookups ----

  qualifiedModelName(model) {
    return qualifiedModelName(model);
  }

  findModelOverrideView(selector) {
    return findModelOverrideView(this.modelOverrideViews, selector);
  }

  hasGlobalModelOverride() {
    return Boolean(this.findModelOverrideView(GLOBAL_OVERRIDE_SELECTOR));
  }

  findExistingAliasByName(name) {
    const normalizedName = normalizedAliasName(name);
    if (!normalizedName) {
      return null;
    }
    for (const alias of this.aliases) {
      if (normalizedAliasName(alias && alias.name) === normalizedName) {
        return alias;
      }
    }
    return null;
  }

  findConcreteModelByName(name) {
    const normalizedName = normalizedAliasName(name);
    if (!normalizedName) {
      return null;
    }
    for (const model of modelsStore.models) {
      if (modelKeys(model).has(normalizedName)) {
        return model;
      }
    }
    return null;
  }

  // ---- Row enable/disable toggle (real models, aliases, groups, global) ----

  rowToggleEnabled(row) {
    if (!row) {
      return false;
    }
    if (row.is_alias) {
      return row.alias && row.alias.enabled !== false;
    }
    return Boolean(row.access && row.access.effective_enabled !== false);
  }

  rowToggleLabel(row) {
    if (this.rowTogglingKey && this.rowTogglingKey === row.key) {
      return m.models_updating();
    }
    if (this.rowToggleRestricted(row)) {
      return m.models_restricted();
    }
    return this.rowToggleEnabled(row)
      ? m.models_enabled()
      : m.models_disabled();
  }

  rowToggleRestricted(row) {
    return (
      Boolean(row) &&
      !row.is_alias &&
      modelAccessStateClass(row.access, this.virtualModelsAvailable) ===
        "is-restricted"
    );
  }

  rowToggleAriaLabel(row) {
    if (!row) {
      return "";
    }
    let subject;
    if (row.is_alias) {
      subject = m.models_alias_subject({
        name: String((row.alias && row.alias.name) || ""),
      });
    } else {
      subject = String(
        row.display_name ||
          (row.access && row.access.selector) ||
          m.models_model_subject(),
      );
    }
    return this.rowToggleEnabled(row)
      ? m.models_disable_action({ subject: subject.trim() })
      : m.models_enable_action({ subject: subject.trim() });
  }

  async toggleRowEnabled(row) {
    if (!this.virtualModelsAvailable) {
      return;
    }
    if (!row || this.rowTogglingKey === row.key) {
      return;
    }
    if (rowIsManaged(row)) {
      flash.success(m.models_managed_read_only());
      return;
    }
    if (row.is_alias) {
      await this.toggleAliasRow(row);
      return;
    }
    await this.toggleModelRow(row);
  }

  async toggleAliasRow(row) {
    const alias = row.alias;
    if (!alias || !alias.name) {
      return;
    }

    this.rowTogglingKey = row.key;

    const payload = buildAliasTogglePayload(alias);
    try {
      const result = await sendJSON("/admin/virtual-models", "PUT", payload, {
        label: "alias state",
      });
      if (result.status === 503) {
        this.virtualModelsAvailable = false;
        flash.error(m.models_unavailable());
        return;
      }
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        flash.error(
          result.status === 401
            ? m.common_authentication_required()
            : errorMessage(result, m.models_alias_update_failed()),
        );
        return;
      }

      flash.success(
        payload.enabled ? m.models_alias_enabled() : m.models_alias_disabled(),
      );
      void this.fetchVirtualModels();
    } catch (e) {
      console.error("Failed to toggle alias state:", e);
      flash.error(m.models_alias_update_failed());
    } finally {
      this.rowTogglingKey = "";
    }
  }

  async toggleModelRow(row) {
    const selector = rowAccessSelector(row);
    if (!selector) {
      return;
    }
    const existingPolicy = this.findModelOverrideView(selector);
    const { method, payload, desired } = buildModelTogglePayload(
      selector,
      existingPolicy,
      row.access || {},
    );

    this.rowTogglingKey = row.key;

    try {
      const result = await sendJSON("/admin/virtual-models", method, payload, {
        label: "model access",
      });
      if (result.status === 503) {
        this.virtualModelsAvailable = false;
        flash.error(m.models_unavailable());
        return;
      }
      if (!(method === "DELETE" && result.status === 404)) {
        if (result.stale) {
          return;
        }
        if (!result.ok) {
          flash.error(
            result.status === 401
              ? m.common_authentication_required()
              : errorMessage(result, m.models_access_update_failed()),
          );
          return;
        }
      }

      flash.success(
        desired ? m.models_model_enabled() : m.models_model_disabled(),
      );
      void Promise.all([modelsStore.fetchModels(), this.fetchVirtualModels()]);
    } catch (e) {
      console.error("Failed to toggle model access:", e);
      flash.error(m.models_access_update_failed());
    } finally {
      this.rowTogglingKey = "";
    }
  }

  // ---- Row deletes ----

  async removeAliasRow(row) {
    if (!(
      row &&
      row.is_alias &&
      row.alias &&
      row.alias.name &&
      !row.alias.managed
    )) {
      return;
    }
    if (this.rowDeletingKey) {
      return;
    }
    const source = String(row.alias.name || "").trim();
    if (!source) {
      return;
    }
    await this.mutateVirtualModelRow({
      rowKey: row.key,
      confirmMessage: m.models_remove_alias_confirm({ source }),
      method: "DELETE",
      payload: { source },
      operation: "virtual model",
      failureMessage: m.models_remove_failed(),
      notice: m.models_removed(),
      ignoreNotFound: true,
    });
  }

  async removeRedirectRow(row) {
    const alias = row && row.masking_alias;
    if (!(row && !row.is_alias && alias && alias.name && !alias.managed)) {
      return;
    }
    if (this.rowDeletingKey) {
      return;
    }
    const source = String(alias.name || "").trim();
    if (!source) {
      return;
    }
    await this.mutateVirtualModelRow({
      rowKey: row.key,
      confirmMessage: m.models_remove_redirect_confirm({ source }),
      method: "PUT",
      payload: {
        source,
        user_paths: Array.isArray(alias.user_paths) ? alias.user_paths : [],
        description: String(alias.description || "").trim(),
        enabled: alias.enabled !== false,
        ...(alias.slowdown != null ? { slowdown: Number(alias.slowdown) } : {}),
      },
      operation: "virtual model redirect",
      failureMessage: m.models_redirect_remove_failed(),
      notice: m.models_redirect_removed(),
    });
  }

  async mutateVirtualModelRow(options) {
    if (this.rowDeletingKey) {
      return;
    }
    if (!window.confirm(options.confirmMessage)) {
      return;
    }

    this.rowDeletingKey = options.rowKey;

    try {
      const result = await sendJSON(
        "/admin/virtual-models",
        options.method,
        options.payload,
        {
          label: options.operation,
        },
      );
      if (result.status === 503) {
        this.virtualModelsAvailable = false;
        flash.error(m.models_unavailable());
        return;
      }
      if (!(options.ignoreNotFound && result.status === 404)) {
        if (result.stale) {
          return;
        }
        if (!result.ok) {
          flash.error(
            result.status === 401
              ? m.common_authentication_required()
              : errorMessage(result, options.failureMessage),
          );
          return;
        }
      }
      this.virtualModelsAvailable = true;

      flash.success(options.notice);
      void Promise.all([modelsStore.fetchModels(), this.fetchVirtualModels()]);
    } catch (e) {
      console.error(options.failureMessage, e);
      flash.error(options.failureMessage);
    } finally {
      this.rowDeletingKey = "";
    }
  }
}

export const virtualModels = new VirtualModelsStore();
