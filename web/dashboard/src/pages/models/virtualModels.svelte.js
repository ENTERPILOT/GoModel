// Virtual models (redirects/aliases, load balancers, access policies) state
// for the Models page.

import { errorMessage, getJSON, sendJSON } from "$lib/api/client.js";
import { flash } from "$lib/stores/flash.svelte.js";
import { modelsStore } from "$lib/stores/models.svelte.js";
import {
  GLOBAL_OVERRIDE_SELECTOR,
  aliasFormTargets,
  buildAliasTogglePayload,
  buildDisplayModels,
  buildGlobalScopeRow,
  buildModelTogglePayload,
  buildVirtualModelSavePayload,
  computeRenderStep,
  defaultVirtualModelForm,
  filterDisplayModels,
  findModelOverrideView,
  groupDisplayModels,
  initialRenderStep,
  modelAccessStateClass,
  modelKeys,
  modelOverridesDefaultEnabled,
  normalizeUserPaths,
  normalizedAliasName,
  qualifiedModelName,
  rowAccessSelector,
  rowIsManaged,
  splitVirtualModelViews,
  vmFormHasPrimaryTarget,
  vmFormShowStrategy,
  vmFormShowWeights,
  modelAccessUserPathsRestrict,
  removePrimaryTarget as removePrimaryTargetPure,
} from "./virtualModelsLogic.js";

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

  // Unified editor (redirects/aliases and access policies share it).
  vmFormOpen = $state(false);
  vmFormHelpOpen = $state(false);
  vmFormUserPathsHelpOpen = $state(false);
  vmFormMode = $state("create");
  vmFormError = $state("");
  vmSubmitting = $state(false);
  vmDeleting = $state(false);
  vmFormHasExisting = $state(false);
  vmFormDefaultEnabled = $state(true);
  vmFormEffectiveEnabled = $state(true);
  vmFormDisplayName = $state("");
  vmFormSourceLocked = $state(false);
  vmFormOriginalSource = $state("");
  vmFormManaged = $state(false);
  vmForm = $state(defaultVirtualModelForm());

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
    groupDisplayModels(this.displayModels, modelsStore.models, this.modelOverrideViews),
  );

  filteredDisplayModels = $derived(filterDisplayModels(this.displayModels, modelsStore.filter));

  filteredDisplayModelGroups = $derived.by(() => {
    const filtered = this.filteredDisplayModels;
    const limit = Math.max(0, Math.min(Number(this.modelRenderLimit || 0), filtered.length));
    if (!modelsStore.filter && limit >= this.displayModels.length) {
      return this.displayModelGroups;
    }
    return groupDisplayModels(filtered.slice(0, limit), modelsStore.models, this.modelOverrideViews);
  });

  // globalScopeRow exposes the global "/" scope as a toggle row, so the global
  // level reuses the same enable/restrict/disable switch as models, aliases,
  // and provider groups.
  globalScopeRow = $derived(buildGlobalScopeRow(modelsStore.models, this.modelOverrideViews));

  modelsBusy() {
    return Boolean(modelsStore.loading || this.modelsRendering);
  }

  modelLoadingText() {
    if (modelsStore.loading) {
      return this.displayModels.length > 0 ? "Refreshing models..." : "Loading models...";
    }
    const total = this.filteredDisplayModels.length;
    const visible = Math.min(Number(this.modelRenderLimit || 0), total);
    return "Rendering models... " + visible + " / " + total;
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
      const result = await getJSON("/admin/virtual-models", { label: "virtual models" });
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
      this.aliasError = "Unable to load virtual models.";
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
      return "Updating...";
    }
    if (this.rowToggleRestricted(row)) {
      return "Restricted";
    }
    return this.rowToggleEnabled(row) ? "Enabled" : "Disabled";
  }

  rowToggleRestricted(row) {
    return (
      Boolean(row) &&
      !row.is_alias &&
      modelAccessStateClass(row.access, this.virtualModelsAvailable) === "is-restricted"
    );
  }

  rowToggleAriaLabel(row) {
    if (!row) {
      return "";
    }
    const action = this.rowToggleEnabled(row) ? "Disable " : "Enable ";
    let subject;
    if (row.is_alias) {
      subject = "alias " + String((row.alias && row.alias.name) || "");
    } else {
      subject = String(row.display_name || (row.access && row.access.selector) || "model");
    }
    return action + subject.trim();
  }

  async toggleRowEnabled(row) {
    if (!this.virtualModelsAvailable) {
      return;
    }
    if (!row || this.rowTogglingKey === row.key) {
      return;
    }
    if (rowIsManaged(row)) {
      flash.success("This virtual model is managed by configuration and is read-only.");
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
        flash.error("Virtual models feature is unavailable.");
        return;
      }
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        flash.error(
          result.status === 401
            ? "Authentication required."
            : errorMessage(result, "Failed to update alias state."),
        );
        return;
      }

      flash.success(payload.enabled ? "Alias enabled." : "Alias disabled.");
      await this.fetchVirtualModels();
    } catch (e) {
      console.error("Failed to toggle alias state:", e);
      flash.error("Failed to update alias state.");
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
        flash.error("Virtual models feature is unavailable.");
        return;
      }
      if (!(method === "DELETE" && result.status === 404)) {
        if (result.stale) {
          return;
        }
        if (!result.ok) {
          flash.error(
            result.status === 401
              ? "Authentication required."
              : errorMessage(result, "Failed to update model access."),
          );
          return;
        }
      }

      flash.success(desired ? "Model enabled." : "Model disabled.");
      await Promise.all([modelsStore.fetchModels(), this.fetchVirtualModels()]);
    } catch (e) {
      console.error("Failed to toggle model access:", e);
      flash.error("Failed to update model access.");
    } finally {
      this.rowTogglingKey = "";
    }
  }

  // ---- Row deletes ----

  async removeAliasRow(row) {
    if (!(row && row.is_alias && row.alias && row.alias.name && !row.alias.managed)) {
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
      confirmMessage: 'Remove the virtual model alias "' + source + '"?',
      method: "DELETE",
      payload: { source },
      operation: "virtual model",
      failureMessage: "Failed to remove virtual model.",
      notice: "Virtual model removed.",
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
      confirmMessage:
        'Remove the redirect for "' + source + '"? Other virtual model settings will be preserved.',
      method: "PUT",
      payload: {
        source,
        user_paths: Array.isArray(alias.user_paths) ? alias.user_paths : [],
        description: String(alias.description || "").trim(),
        enabled: alias.enabled !== false,
      },
      operation: "virtual model redirect",
      failureMessage: "Failed to remove redirect.",
      notice: "Redirect removed. Other virtual model settings were preserved.",
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
      const result = await sendJSON("/admin/virtual-models", options.method, options.payload, {
        label: options.operation,
      });
      if (result.status === 503) {
        this.virtualModelsAvailable = false;
        flash.error("Virtual models feature is unavailable.");
        return;
      }
      if (!(options.ignoreNotFound && result.status === 404)) {
        if (result.stale) {
          return;
        }
        if (!result.ok) {
          flash.error(
            result.status === 401
              ? "Authentication required."
              : errorMessage(result, options.failureMessage),
          );
          return;
        }
      }
      this.virtualModelsAvailable = true;

      flash.success(options.notice);
      await Promise.all([modelsStore.fetchModels(), this.fetchVirtualModels()]);
    } catch (e) {
      console.error(options.failureMessage, e);
      flash.error(options.failureMessage);
    } finally {
      this.rowDeletingKey = "";
    }
  }

  // ---- Unified virtual-model editor ----

  addVmTarget() {
    if (!Array.isArray(this.vmForm.targets)) {
      this.vmForm.targets = [];
    }
    this.vmForm.targets.push({ model: "", weight: 1 });
  }

  removeVmTarget(index) {
    if (!Array.isArray(this.vmForm.targets)) {
      return;
    }
    this.vmForm.targets.splice(index, 1);
  }

  removePrimaryTarget() {
    removePrimaryTargetPure(this.vmForm);
  }

  vmFormHasPrimaryTarget() {
    return vmFormHasPrimaryTarget(this.vmForm);
  }

  vmFormShowStrategy() {
    return vmFormShowStrategy(this.vmForm);
  }

  vmFormShowWeights() {
    return vmFormShowWeights(this.vmForm);
  }

  // The edit-modal status switch reuses the same .alias-toggle component and
  // three states, derived from the form's own fields: it is Restricted when
  // enabled and scoped to non-global user paths.
  vmFormToggleRestricted() {
    return (
      Boolean(this.vmForm && this.vmForm.enabled) &&
      modelAccessUserPathsRestrict(normalizeUserPaths(this.vmForm.user_paths))
    );
  }

  vmFormToggleLabel() {
    if (!this.vmForm || !this.vmForm.enabled) {
      return "Disabled";
    }
    return this.vmFormToggleRestricted() ? "Restricted" : "Enabled";
  }

  resetVirtualModelForm() {
    this.vmFormError = "";
    this.vmFormHelpOpen = false;
    this.vmFormUserPathsHelpOpen = false;
    this.vmSubmitting = false;
    this.vmDeleting = false;
    this.vmFormHasExisting = false;
    this.vmFormDefaultEnabled = true;
    this.vmFormEffectiveEnabled = true;
    this.vmFormDisplayName = "";
    this.vmFormSourceLocked = false;
    this.vmFormOriginalSource = "";
    this.vmFormManaged = false;
    this.vmForm = defaultVirtualModelForm();
  }

  closeVirtualModelForm() {
    this.vmFormOpen = false;
    this.resetVirtualModelForm();
  }

  // openVirtualModelCreate opens the editor in create mode with an editable
  // Source. Optionally prefills the Target model from a model row.
  openVirtualModelCreate(model) {
    this.resetVirtualModelForm();
    this.vmFormOpen = true;
    this.vmFormMode = "create";
    this.vmFormSourceLocked = false;
    this.vmFormDisplayName = "New virtual model";
    if (model && model.model && model.model.id) {
      this.vmForm.target_model = qualifiedModelName(model);
    }
  }

  // openVirtualModelEditAlias edits an existing redirect/alias. Source is
  // editable: changing it renames the virtual model (the original source
  // travels with the save as old_source so the backend moves the row).
  openVirtualModelEditAlias(alias) {
    if (!alias) {
      return;
    }
    this.resetVirtualModelForm();
    this.vmFormOpen = true;
    this.vmFormMode = "edit";
    this.vmFormSourceLocked = false;
    this.vmFormHasExisting = true;
    this.vmFormManaged = Boolean(alias.managed);
    this.vmFormOriginalSource = alias.name || "";
    this.vmFormDisplayName = alias.name || "";
    // A redirect has a single enabled flag; show it against the process-wide
    // default so a disabled alias reads "Effective now: no".
    this.vmFormDefaultEnabled = modelOverridesDefaultEnabled(modelsStore.models);
    this.vmFormEffectiveEnabled = alias.enabled !== false;

    const { primaryModel, primaryWeight, extraTargets } = aliasFormTargets(alias);
    this.vmForm = {
      source: alias.name || "",
      target_model: primaryModel,
      target_weight: primaryWeight,
      targets: extraTargets,
      strategy: alias.strategy || "round_robin",
      user_paths: (Array.isArray(alias.user_paths) ? alias.user_paths : []).join("\n"),
      description: alias.description || "",
      enabled: alias.enabled !== false,
    };
  }

  // openVirtualModelEditModel edits the virtual model attached to a real
  // model row (or seeds a policy for it). Source is locked and prefilled with
  // the model selector.
  openVirtualModelEditModel(row) {
    if (!row || row.is_alias) {
      return;
    }
    const access = row.access || {};
    const override = access.override || null;
    const userPaths =
      override && Array.isArray(override.user_paths)
        ? override.user_paths
        : Array.isArray(access.user_paths)
          ? access.user_paths
          : [];
    const selector = rowAccessSelector(row);

    this.resetVirtualModelForm();
    this.vmFormOpen = true;
    this.vmFormMode = "edit";
    this.vmFormSourceLocked = true;
    this.vmFormHasExisting = Boolean(override);
    this.vmFormOriginalSource = selector;
    // Prefer the override's own enabled value; fall back to the backend
    // effective state only when no override row exists for this selector.
    const overrideEnabled = override
      ? override.enabled !== false
      : access.effective_enabled !== false;
    this.vmFormDefaultEnabled = access.default_enabled !== false;
    this.vmFormEffectiveEnabled = overrideEnabled;
    this.vmFormManaged = Boolean(override && override.managed);
    this.vmFormDisplayName = row.access_display_name || row.display_name || selector || "";
    this.vmForm = {
      source: selector,
      target_model: "",
      target_weight: "",
      targets: [],
      strategy: "round_robin",
      user_paths: userPaths.join("\n"),
      description: override && override.description ? override.description : "",
      enabled: overrideEnabled,
    };
  }

  openGlobalModelOverrideEdit() {
    const selector = GLOBAL_OVERRIDE_SELECTOR;
    const override = this.findModelOverrideView(selector);
    const userPaths = override && Array.isArray(override.user_paths) ? override.user_paths : [];
    const defaultEnabled = modelOverridesDefaultEnabled(modelsStore.models);

    this.resetVirtualModelForm();
    this.vmFormOpen = true;
    this.vmFormMode = "edit";
    this.vmFormSourceLocked = true;
    this.vmFormHasExisting = Boolean(override);
    this.vmFormOriginalSource = selector;
    this.vmFormDefaultEnabled = defaultEnabled;
    this.vmFormEffectiveEnabled = override ? override.enabled !== false : defaultEnabled;
    this.vmFormManaged = Boolean(override && override.managed);
    this.vmFormDisplayName = "All providers and models";
    this.vmForm = {
      source: selector,
      target_model: "",
      target_weight: "",
      targets: [],
      strategy: "round_robin",
      user_paths: userPaths.join("\n"),
      description: override && override.description ? override.description : "",
      enabled: override ? override.enabled !== false : defaultEnabled,
    };
  }

  openProviderOverrideEdit(group) {
    if (!group || !group.access || !group.access.selector) {
      return;
    }
    this.openVirtualModelEditModel({
      display_name: group.display_name,
      access_display_name: "All models in " + group.display_name,
      provider_name: group.provider_name,
      provider_type: group.provider_type,
      access: group.access,
      override_selector: group.access.selector,
      is_alias: false,
    });
  }

  // submitVirtualModelForm saves the unified editor: a filled target makes a
  // redirect/alias (several targets load balance by strategy), an empty one
  // makes an access policy.
  async submitVirtualModelForm() {
    if (this.vmFormManaged) {
      this.vmFormError = "This virtual model is managed by configuration and cannot be edited here.";
      return;
    }

    const built = buildVirtualModelSavePayload(this.vmForm, this.vmFormOriginalSource, this.vmFormMode);
    const { payload, source, isRedirect, isRename } = built;

    if (!source) {
      this.vmFormError = "Source is required.";
      return;
    }

    this.vmFormError = "";

    // In create mode warn about clobbering an existing virtual model (alias or
    // access policy) on the same source, or masking a concrete model. Edit
    // mode locks the source so none of these apply. The overwrite check must
    // run even with an empty target, since that is a policy upsert that can
    // still replace an existing redirect/policy row.
    if (this.vmFormMode !== "edit") {
      const existingAlias = this.findExistingAliasByName(source);
      const existingPolicy = existingAlias ? null : this.findModelOverrideView(source);
      if (existingAlias || existingPolicy) {
        const overwriteMessage = existingAlias
          ? 'A virtual model named "' +
            existingAlias.name +
            '" already exists. Saving will update that virtual model. Continue?'
          : 'An access policy for "' +
            source +
            '" already exists. Saving will update that virtual model. Continue?';
        if (!window.confirm(overwriteMessage)) {
          this.vmFormError = "Choose a different source or edit the existing virtual model.";
          return;
        }
      } else if (isRedirect) {
        const matchingModel = this.findConcreteModelByName(source);
        if (matchingModel) {
          const modelName =
            qualifiedModelName(matchingModel) ||
            String((matchingModel.model && matchingModel.model.id) || "").trim();
          if (
            !window.confirm(
              'A model named "' +
                modelName +
                '" already exists. Creating this alias will mask that model in the list. Continue?',
            )
          ) {
            this.vmFormError = "Choose a different source to avoid masking an existing model.";
            return;
          }
        }
      }
    } else if (isRename) {
      // Renaming: the backend refuses to clobber an existing row, so block
      // early with a clear message instead of overwriting another virtual
      // model. Match the source exactly (case-sensitive) like the backend key,
      // so a case-only rename (e.g. "Smart" -> "smart") is not wrongly
      // rejected. Masking a concrete model is still a soft confirm.
      const existingAlias = (this.aliases || []).find((entry) => entry && entry.name === source) || null;
      const existingPolicy = existingAlias ? null : this.findModelOverrideView(source);
      if (existingAlias || existingPolicy) {
        this.vmFormError = 'A virtual model for "' + source + '" already exists. Choose a different source.';
        return;
      }
      if (isRedirect) {
        const matchingModel = this.findConcreteModelByName(source);
        if (matchingModel) {
          const modelName =
            qualifiedModelName(matchingModel) ||
            String((matchingModel.model && matchingModel.model.id) || "").trim();
          if (
            !window.confirm(
              'A model named "' +
                modelName +
                '" already exists. Renaming to that name will mask the model in the list. Continue?',
            )
          ) {
            this.vmFormError = "Choose a different source to avoid masking an existing model.";
            return;
          }
        }
      }
    }

    this.vmSubmitting = true;

    try {
      const result = await sendJSON("/admin/virtual-models", "PUT", payload, {
        label: "virtual model",
      });
      if (result.status === 503) {
        this.virtualModelsAvailable = false;
        this.vmFormError = "Virtual models feature is unavailable.";
        return;
      }
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        this.vmFormError =
          result.status === 401
            ? "Authentication required."
            : errorMessage(result, "Failed to save virtual model.");
        return;
      }
      const policyPruned = !isRedirect && result.status === 204;
      this.virtualModelsAvailable = true;

      this.closeVirtualModelForm();
      flash.success(
        isRedirect
          ? "Alias saved."
          : policyPruned
            ? "Model access reset to inherited/default."
            : "Model access saved.",
      );
      await Promise.all([modelsStore.fetchModels(), this.fetchVirtualModels()]);
    } catch (e) {
      console.error("Failed to save virtual model:", e);
      this.vmFormError = "Failed to save virtual model.";
    } finally {
      this.vmSubmitting = false;
    }
  }

  // deleteVirtualModel removes the virtual model for the editor's source.
  async deleteVirtualModel() {
    if (this.vmFormManaged) {
      this.vmFormError = "This virtual model is managed by configuration and cannot be removed here.";
      return;
    }
    const source = String(this.vmForm.source || this.vmFormOriginalSource || "").trim();
    if (!source || !this.vmFormHasExisting) {
      return;
    }
    if (
      !window.confirm(
        'Remove the virtual model for "' + source + '"? This reverts to inherited/default behavior.',
      )
    ) {
      return;
    }

    this.vmDeleting = true;
    this.vmFormError = "";

    try {
      const result = await sendJSON("/admin/virtual-models", "DELETE", { source }, {
        label: "virtual model",
      });
      if (result.status === 503) {
        this.virtualModelsAvailable = false;
        this.vmFormError = "Virtual models feature is unavailable.";
        return;
      }
      if (result.status !== 404) {
        if (result.stale) {
          return;
        }
        if (!result.ok) {
          this.vmFormError =
            result.status === 401
              ? "Authentication required."
              : errorMessage(result, "Failed to remove virtual model.");
          return;
        }
      }
      this.virtualModelsAvailable = true;

      this.closeVirtualModelForm();
      flash.success("Virtual model removed.");
      await Promise.all([modelsStore.fetchModels(), this.fetchVirtualModels()]);
    } catch (e) {
      console.error("Failed to delete virtual model:", e);
      this.vmFormError = "Failed to remove virtual model.";
    } finally {
      this.vmDeleting = false;
    }
  }
}

export const virtualModels = new VirtualModelsStore();
