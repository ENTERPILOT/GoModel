// Virtual-model editor state for the Models page: the unified form that
// creates or edits a redirect/alias, load balancer, or access policy. The
// list itself (rows, toggles, deletes) lives in virtualModels.svelte.js; the
// editor reads its aliases and refreshes it after a save.

import { errorMessage, sendJSON } from "$lib/api/client.js";
import { flash } from "$lib/stores/flash.svelte.js";
import * as m from "$lib/paraglide/messages.js";
import { modelsStore } from "$lib/stores/models.svelte.js";
import { runtimeConfig } from "$lib/stores/runtimeConfig.svelte.js";
import {
  modelOverridesDefaultEnabled,
  rowAccessSelector,
} from "./displayRows.js";
import {
  GLOBAL_OVERRIDE_SELECTOR,
  qualifiedModelName,
} from "./modelIdentity.js";
import { modelAccessUserPathsRestrict, strategyOptions } from "./routing.js";
import {
  aliasFormTargets,
  buildVirtualModelSavePayload,
  defaultVirtualModelForm,
  normalizeUserPaths,
  removePrimaryTarget as removePrimaryTargetPure,
  virtualModelTargetOptions,
  vmRoutingSummary,
} from "./vmForm.js";
import { virtualModels } from "./virtualModels.svelte.js";

class VirtualModelEditorStore {
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

  addVmTarget() {
    if (!Array.isArray(this.vmForm.targets)) {
      this.vmForm.targets = [];
    }
    this.vmForm.targets.push({ provider: "", model: "", weight: 1 });
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

  // vmStrategyOptions builds the strategy dropdown from the strategies this
  // deployment supports (server-driven), keeping the edited row's current
  // value selectable even when the deployment no longer offers it.
  vmStrategyOptions() {
    return strategyOptions(
      runtimeConfig.virtualModelStrategies(),
      this.vmForm.strategy,
    );
  }

  // vmRoutingSummary describes the form's effect; whether the source names a
  // catalog model decides between "replaced" and plain alias wording.
  vmRoutingSummary() {
    const source = String(this.vmForm.source || "").trim();
    const sourceIsModel = modelsStore.models.some(
      (model) => qualifiedModelName(model) === source,
    );
    return vmRoutingSummary(this.vmForm, sourceIsModel);
  }

  vmTargetOptions() {
    return virtualModelTargetOptions(
      modelsStore.models,
      virtualModels.aliases,
      this.vmForm.source,
    );
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
    return this.vmFormToggleRestricted()
      ? m.models_restricted()
      : m.models_enabled();
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
    this.vmFormDefaultEnabled = modelOverridesDefaultEnabled(
      modelsStore.models,
    );
    this.vmFormEffectiveEnabled = alias.enabled !== false;

    const { primaryProvider, primaryModel, primaryWeight, extraTargets } =
      aliasFormTargets(alias);
    this.vmForm = {
      source: alias.name || "",
      target_provider: primaryProvider,
      target_model: primaryModel,
      target_weight: primaryWeight,
      targets: extraTargets,
      strategy: alias.strategy || "round_robin",
      session_affinity: alias.session_affinity !== false,
      failover: alias.failover !== false,
      user_paths: (Array.isArray(alias.user_paths)
        ? alias.user_paths
        : []
      ).join("\n"),
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
    this.vmFormDisplayName =
      row.access_display_name || row.display_name || selector || "";
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
    const override = virtualModels.findModelOverrideView(selector);
    const userPaths =
      override && Array.isArray(override.user_paths) ? override.user_paths : [];
    const defaultEnabled = modelOverridesDefaultEnabled(modelsStore.models);

    this.resetVirtualModelForm();
    this.vmFormOpen = true;
    this.vmFormMode = "edit";
    this.vmFormSourceLocked = true;
    this.vmFormHasExisting = Boolean(override);
    this.vmFormOriginalSource = selector;
    this.vmFormDefaultEnabled = defaultEnabled;
    this.vmFormEffectiveEnabled = override
      ? override.enabled !== false
      : defaultEnabled;
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
      access_display_name: m.models_all_in_provider({
        provider: group.display_name,
      }),
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

    const built = buildVirtualModelSavePayload(
      this.vmForm,
      this.vmFormOriginalSource,
      this.vmFormMode,
    );
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
      const existingAlias = virtualModels.findExistingAliasByName(source);
      const existingPolicy = existingAlias
        ? null
        : virtualModels.findModelOverrideView(source);
      if (existingAlias || existingPolicy) {
        const overwriteMessage = existingAlias
          ? m.models_overwrite_alias_confirm({ name: existingAlias.name })
          : m.models_overwrite_policy_confirm({ source });
        if (!window.confirm(overwriteMessage)) {
          this.vmFormError = m.models_source_exists();
          return;
        }
      } else if (isRedirect) {
        const matchingModel = virtualModels.findConcreteModelByName(source);
        if (matchingModel) {
          const modelName =
            qualifiedModelName(matchingModel) ||
            String(
              (matchingModel.model && matchingModel.model.id) || "",
            ).trim();
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
      const existingAlias =
        (virtualModels.aliases || []).find(
          (entry) => entry && entry.name === source,
        ) || null;
      const existingPolicy = existingAlias
        ? null
        : virtualModels.findModelOverrideView(source);
      if (existingAlias || existingPolicy) {
        this.vmFormError = m.models_source_conflict({ source });
        return;
      }
      if (isRedirect) {
        const matchingModel = virtualModels.findConcreteModelByName(source);
        if (matchingModel) {
          const modelName =
            qualifiedModelName(matchingModel) ||
            String(
              (matchingModel.model && matchingModel.model.id) || "",
            ).trim();
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
        virtualModels.virtualModelsAvailable = false;
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
      virtualModels.virtualModelsAvailable = true;

      this.closeVirtualModelForm();
      flash.success(
        isRedirect
          ? m.models_alias_saved()
          : policyPruned
            ? m.models_access_reset()
            : m.models_access_saved(),
      );
      void Promise.all([
        modelsStore.fetchModels(),
        virtualModels.fetchVirtualModels(),
      ]);
    } catch (e) {
      console.error("Failed to save virtual model:", e);
      this.vmFormError = m.models_save_failed();
    } finally {
      this.vmSubmitting = false;
    }
  }

  // deleteVirtualModel removes the virtual model the editor was opened on.
  // The source field may hold an unsaved rename, so the persisted source
  // wins: deleting by the typed name would miss the row (and a 404 reads as
  // "already gone").
  async deleteVirtualModel() {
    if (this.vmFormManaged) {
      this.vmFormError = m.models_managed_cannot_remove();
      return;
    }
    const source = String(
      this.vmFormOriginalSource || this.vmForm.source || "",
    ).trim();
    if (!source || !this.vmFormHasExisting) {
      return;
    }
    if (!window.confirm(m.models_remove_policy_confirm({ source }))) {
      return;
    }

    this.vmDeleting = true;
    this.vmFormError = "";

    try {
      const result = await sendJSON(
        "/admin/virtual-models",
        "DELETE",
        { source },
        {
          label: "virtual model",
        },
      );
      if (result.status === 503) {
        virtualModels.virtualModelsAvailable = false;
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
      virtualModels.virtualModelsAvailable = true;

      this.closeVirtualModelForm();
      flash.success(m.models_removed());
      void Promise.all([
        modelsStore.fetchModels(),
        virtualModels.fetchVirtualModels(),
      ]);
    } catch (e) {
      console.error("Failed to delete virtual model:", e);
      this.vmFormError = m.models_remove_failed();
    } finally {
      this.vmDeleting = false;
    }
  }
}

export const virtualModelEditor = new VirtualModelEditorStore();
