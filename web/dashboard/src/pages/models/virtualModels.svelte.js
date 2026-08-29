// Virtual models (redirects/aliases, load balancers, access policies) state
// for the Models page.

import { errorMessage, getJSON, sendJSON } from "$lib/api/client.js";
import { flash } from "$lib/stores/flash.svelte.js";
import * as m from "$lib/paraglide/messages.js";
import { modelsStore } from "$lib/stores/models.svelte.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import {
  buildDisplayModels,
  buildGlobalScopeRow,
  filterDisplayModels,
  findModelOverrideView,
  groupDisplayModels,
  modelOverridesDefaultEnabled,
  rowAccessSelector,
  rowIsManaged,
} from "./displayRows.js";
import {
  GLOBAL_OVERRIDE_SELECTOR,
  modelKeys,
  normalizedAliasName,
  qualifiedModelName,
} from "./modelIdentity.js";
import {
  computeRenderStep,
  initialRenderStep,
} from "./renderBatching.js";
import {
  modelAccessStateClass,
  modelAccessUserPathsRestrict,
  splitVirtualModelViews,
  strategyOptions,
} from "./routing.js";
import {
  aliasFormTargets,
  buildAliasTogglePayload,
  buildModelTogglePayload,
  buildVirtualModelSavePayload,
  defaultVirtualModelForm,
  normalizeUserPaths,
  removePrimaryTarget as removePrimaryTargetPure,
  virtualModelTargetOptions,
  vmFormHasPrimaryTarget,
  vmFormShowBalancingOptions,
  vmFormShowWeights,
  vmFormStrategyPending,
  vmFormSupportsSlowdown,
  vmRoutingSummary,
} from "./vmForm.js";

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
  vmFormStrategyHelpOpen = $state(false);
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
      return this.displayModels.length > 0 ? m.models_refreshing() : m.models_loading();
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
    return this.rowToggleEnabled(row) ? m.models_enabled() : m.models_disabled();
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
    let subject;
    if (row.is_alias) {
      subject = m.models_alias_subject({ name: String((row.alias && row.alias.name) || "") });
    } else {
      subject = String(row.display_name || (row.access && row.access.selector) || m.models_model_subject());
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

      flash.success(payload.enabled ? m.models_alias_enabled() : m.models_alias_disabled());
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

      flash.success(desired ? m.models_model_enabled() : m.models_model_disabled());
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
      const result = await sendJSON("/admin/virtual-models", options.method, options.payload, {
        label: options.operation,
      });
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

  // vmStrategyOptions builds the strategy dropdown from the strategies this
  // deployment supports (server-driven), keeping the edited row's current
  // value selectable even when the deployment no longer offers it.
  vmStrategyOptions() {
    return strategyOptions(runtimeConfig.virtualModelStrategies(), this.vmForm.strategy);
  }

  vmFormShowWeights() {
    return vmFormShowWeights(this.vmForm);
  }

  vmFormShowBalancingOptions() {
    return vmFormShowBalancingOptions(this.vmForm);
  }

  // vmRoutingSummary describes the form's effect; whether the source names a
  // catalog model decides between "replaced" and plain alias wording.
  vmRoutingSummary() {
    const source = String(this.vmForm.source || "").trim();
    const sourceIsModel = modelsStore.models.some((model) => qualifiedModelName(model) === source);
    return vmRoutingSummary(this.vmForm, sourceIsModel);
  }

  vmFormStrategyPending() {
    return vmFormStrategyPending(this.vmForm);
  }

  vmTargetOptions() {
    return virtualModelTargetOptions(modelsStore.models, this.aliases, this.vmForm.source);
  }

  vmFormSupportsSlowdown() {
    return vmFormSupportsSlowdown(this.vmForm);
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
      return m.models_disabled();
    }
    return this.vmFormToggleRestricted() ? m.models_restricted() : m.models_enabled();
  }

  resetVirtualModelForm() {
    this.vmFormError = "";
    this.vmFormHelpOpen = false;
    this.vmFormUserPathsHelpOpen = false;
    this.vmFormStrategyHelpOpen = false;
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
    this.vmFormDisplayName = m.models_new_virtual();
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
      session_affinity: alias.session_affinity !== false,
      failover: alias.failover !== false,
      user_paths: (Array.isArray(alias.user_paths) ? alias.user_paths : []).join("\n"),
      description: alias.description || "",
      slowdown: alias.slowdown ?? "",
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
    // The model itself is pinned as the first target with the failover
    // strategy, so adding a target makes a fallback for this model rather than
    // silently replacing it. An existing redirect over the model is shown as
    // saved.
    const { primaryModel, primaryWeight, extraTargets } =
      override && Array.isArray(override.targets) && override.targets.length > 0
        ? aliasFormTargets(override)
        : { primaryModel: selector, primaryWeight: 1, extraTargets: [] };
    this.vmForm = {
      ...defaultVirtualModelForm(),
      source: selector,
      target_model: primaryModel,
      target_weight: primaryWeight,
      targets: extraTargets,
      strategy: (override && override.strategy) || "failover",
      session_affinity: !override || override.session_affinity !== false,
      failover: !override || override.failover !== false,
      user_paths: userPaths.join("\n"),
      description: override && override.description ? override.description : "",
      slowdown: override && override.slowdown != null ? override.slowdown : "",
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
    this.vmFormDisplayName = m.models_all_scope();
    this.vmForm = {
      ...defaultVirtualModelForm(),
      source: selector,
      user_paths: userPaths.join("\n"),
      description: override && override.description ? override.description : "",
      slowdown: override && override.slowdown != null ? override.slowdown : "",
      enabled: override ? override.enabled !== false : defaultEnabled,
    };
  }

  openProviderOverrideEdit(group) {
    if (!group || !group.access || !group.access.selector) {
      return;
    }
    this.openVirtualModelEditModel({
      display_name: group.display_name,
      access_display_name: m.models_all_in_provider({ provider: group.display_name }),
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
      this.vmFormError = m.models_managed_cannot_edit();
      return;
    }

    const built = buildVirtualModelSavePayload(this.vmForm, this.vmFormOriginalSource, this.vmFormMode);
    const { payload, source, isRedirect, isRename } = built;

    if (!source) {
      this.vmFormError = m.models_source_required();
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
          ? m.models_overwrite_alias_confirm({ name: existingAlias.name })
          : m.models_overwrite_policy_confirm({ source });
        if (!window.confirm(overwriteMessage)) {
          this.vmFormError = m.models_source_exists();
          return;
        }
      } else if (isRedirect) {
        const matchingModel = this.findConcreteModelByName(source);
        if (matchingModel) {
          const modelName =
            qualifiedModelName(matchingModel) ||
            String((matchingModel.model && matchingModel.model.id) || "").trim();
          if (
            !window.confirm(m.models_mask_create_confirm({ name: modelName }))
          ) {
            this.vmFormError = m.models_source_masks();
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
        this.vmFormError = m.models_source_conflict({ source });
        return;
      }
      if (isRedirect) {
        const matchingModel = this.findConcreteModelByName(source);
        if (matchingModel) {
          const modelName =
            qualifiedModelName(matchingModel) ||
            String((matchingModel.model && matchingModel.model.id) || "").trim();
          if (
            !window.confirm(m.models_mask_rename_confirm({ name: modelName }))
          ) {
            this.vmFormError = m.models_source_masks();
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
        this.vmFormError = m.models_unavailable();
        return;
      }
      if (result.stale) {
        return;
      }
      if (!result.ok) {
        this.vmFormError =
          result.status === 401
            ? m.common_authentication_required()
            : errorMessage(result, m.models_save_failed());
        return;
      }
      const policyPruned = !isRedirect && result.status === 204;
      this.virtualModelsAvailable = true;

      this.closeVirtualModelForm();
      flash.success(
        isRedirect
          ? m.models_alias_saved()
          : policyPruned
            ? m.models_access_reset()
            : m.models_access_saved(),
      );
      void Promise.all([modelsStore.fetchModels(), this.fetchVirtualModels()]);
    } catch (e) {
      console.error("Failed to save virtual model:", e);
      this.vmFormError = m.models_save_failed();
    } finally {
      this.vmSubmitting = false;
    }
  }

  // deleteVirtualModel removes the virtual model for the editor's source.
  async deleteVirtualModel() {
    if (this.vmFormManaged) {
      this.vmFormError = m.models_managed_cannot_remove();
      return;
    }
    const source = String(this.vmForm.source || this.vmFormOriginalSource || "").trim();
    if (!source || !this.vmFormHasExisting) {
      return;
    }
    if (
      !window.confirm(m.models_remove_policy_confirm({ source }))
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
        this.vmFormError = m.models_unavailable();
        return;
      }
      if (result.status !== 404) {
        if (result.stale) {
          return;
        }
        if (!result.ok) {
          this.vmFormError =
            result.status === 401
              ? m.common_authentication_required()
              : errorMessage(result, m.models_remove_failed());
          return;
        }
      }
      this.virtualModelsAvailable = true;

      this.closeVirtualModelForm();
      flash.success(m.models_removed());
      void Promise.all([modelsStore.fetchModels(), this.fetchVirtualModels()]);
    } catch (e) {
      console.error("Failed to delete virtual model:", e);
      this.vmFormError = m.models_remove_failed();
    } finally {
      this.vmDeleting = false;
    }
  }
}

export const virtualModels = new VirtualModelsStore();
