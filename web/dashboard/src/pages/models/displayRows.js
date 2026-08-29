// The models table: display rows, filtering, provider grouping, and row predicates/labels.
// Pure logic (no Svelte) so the node:test suite can exercise it directly;
// relative imports keep it loadable outside Vite.

import * as m from "../../lib/paraglide/messages.js";
import {
  GLOBAL_OVERRIDE_SELECTOR,
  aliasKeys,
  modelKeys,
  normalizedAliasName,
  qualifiedModelName,
} from "./modelIdentity.js";

import {
  aliasStateClass,
  aliasStateText,
  aliasTargetLabel,
  modelAccessSummary,
} from "./routing.js";

export function buildDisplayModels({
  models,
  aliases,
  virtualModelsAvailable,
  activeCategory,
}) {
  const safeModels = Array.isArray(models) ? models : [];
  const safeAliases = Array.isArray(aliases) ? aliases : [];
  const redirectsBySource = new Map();
  if (virtualModelsAvailable) {
    for (const alias of safeAliases) {
      const aliasName = normalizedAliasName(alias && alias.name);
      if (!aliasName || alias.enabled === false || !alias.valid) {
        continue;
      }
      redirectsBySource.set(aliasName, alias);
    }
  }

  const concreteModelsByKey = new Map();
  const rows = safeModels.map((model) => {
    const displayName = qualifiedModelName(model);
    let maskingAlias = null;
    for (const key of modelKeys(model)) {
      if (!concreteModelsByKey.has(key)) {
        concreteModelsByKey.set(key, model);
      }
      if (!maskingAlias && redirectsBySource.has(key)) {
        maskingAlias = redirectsBySource.get(key);
      }
    }
    const access = model && model.access ? model.access : null;
    return {
      key: "model:" + displayName,
      display_name: displayName,
      secondary_name: "",
      provider_name: model.provider_name || "",
      provider_type: model.provider_type || "",
      model: model.model,
      selector: model.selector || "",
      is_alias: false,
      alias: null,
      access,
      masking_alias: maskingAlias,
      has_virtual_model: Boolean(maskingAlias || (access && access.override)),
      alias_state_class: "",
      alias_state_text: "",
    };
  });

  if (!virtualModelsAvailable) {
    return rows;
  }

  for (const alias of safeAliases) {
    const sourceModel = concreteModelsByKey.get(
      normalizedAliasName(alias && alias.name),
    );
    if (alias && alias.enabled !== false && alias.valid && sourceModel) {
      continue;
    }
    let targetModel = null;
    for (const key of aliasKeys(alias)) {
      targetModel = concreteModelsByKey.get(key) || null;
      if (targetModel) break;
    }
    if (!targetModel && activeCategory && activeCategory !== "all") {
      continue;
    }

    rows.push({
      key: "alias:" + alias.name,
      display_name: alias.name,
      secondary_name: aliasTargetLabel(alias),
      provider_name: targetModel ? targetModel.provider_name || "" : "",
      provider_type: targetModel
        ? targetModel.provider_type || alias.provider_type || ""
        : alias.provider_type || "",
      model: targetModel
        ? targetModel.model
        : { id: alias.name, object: "model" },
      selector: "",
      is_alias: true,
      alias,
      access: null,
      masking_alias: null,
      source_model_exists: Boolean(sourceModel),
      has_virtual_model: true,
      alias_state_class: aliasStateClass(alias),
      alias_state_text: aliasStateText(alias),
    });
  }

  return rows.sort((a, b) => {
    if (a.is_alias !== b.is_alias) {
      return a.is_alias ? -1 : 1;
    }
    return String(a.display_name || "").localeCompare(
      String(b.display_name || ""),
    );
  });
}

// filterDisplayModels keeps an identity guarantee: with an empty
// filter the input array is returned untouched.
export function filterDisplayModels(rows, modelFilter) {
  if (!modelFilter) return rows;
  const filter = String(modelFilter).toLowerCase();
  return rows.filter((row) => {
    const fields = [
      row.display_name,
      row.secondary_name,
      row.provider_name,
      row.provider_type,
      row.model && row.model.owned_by,
      row.alias && row.alias.description,
      row.alias && row.alias_state_text,
      row.model && row.model.metadata && Array.isArray(row.model.metadata.categories)
        ? row.model.metadata.categories.join(",")
        : "",
    ];
    return fields.some((value) =>
      String(value || "")
        .toLowerCase()
        .includes(filter),
    );
  });
}

// ---- Provider grouping ----

function providerGroupDisplayName(providerName, providerType) {
  const normalizedProviderName = String(providerName || "").trim();
  if (normalizedProviderName) {
    return normalizedProviderName;
  }
  const normalizedProviderType = String(providerType || "").trim();
  if (normalizedProviderType) {
    return normalizedProviderType;
  }
  return m.models_unassigned();
}

function providerGroupTypeLabel(providerName, providerType) {
  const normalizedProviderName = String(providerName || "").trim();
  const normalizedProviderType = String(providerType || "").trim();
  if (
    !normalizedProviderType ||
    normalizedProviderType === normalizedProviderName
  ) {
    return "";
  }
  return normalizedProviderType;
}

function providerOverrideSelector(providerName) {
  const normalizedProviderName = String(providerName || "").trim();
  if (!normalizedProviderName) {
    return "";
  }
  return normalizedProviderName + "/";
}

function providerGroupItemCountLabel(rows) {
  const safeRows = Array.isArray(rows) ? rows : [];
  const modelCount = safeRows.filter((row) => row && !row.is_alias).length;
  const aliasCount = safeRows.filter((row) => row && row.is_alias).length;
  const parts = [];
  if (modelCount > 0) {
    parts.push(m.models_count({ count: modelCount }));
  }
  if (aliasCount > 0) {
    parts.push(m.models_alias_count({ count: aliasCount }));
  }
  return parts.join(" · ");
}

function providerGroupDefaultEnabled(models, providerName, providerType) {
  const normalizedProviderName = String(providerName || "").trim();
  const normalizedProviderType = String(providerType || "").trim();
  for (const model of Array.isArray(models) ? models : []) {
    const modelProviderName = String(
      (model && model.provider_name) || "",
    ).trim();
    const modelProviderType = String(
      (model && model.provider_type) || "",
    ).trim();
    if (
      normalizedProviderName &&
      modelProviderName !== normalizedProviderName
    ) {
      continue;
    }
    if (
      !normalizedProviderName &&
      normalizedProviderType &&
      modelProviderType !== normalizedProviderType
    ) {
      continue;
    }
    if (model && model.access) {
      return model.access.default_enabled !== false;
    }
  }
  return true;
}

export function modelOverridesDefaultEnabled(models) {
  for (const model of Array.isArray(models) ? models : []) {
    if (model && model.access) {
      return model.access.default_enabled !== false;
    }
  }
  return true;
}

export function findModelOverrideView(modelOverrideViews, selector) {
  const normalizedSelector = String(selector || "").trim();
  if (!normalizedSelector) {
    return null;
  }
  for (const override of Array.isArray(modelOverrideViews)
    ? modelOverrideViews
    : []) {
    if (
      String((override && override.selector) || "").trim() ===
      normalizedSelector
    ) {
      return override;
    }
  }
  return null;
}

function providerGroupAccess(
  models,
  providerName,
  providerType,
  overridesBySelector,
) {
  const selector = providerOverrideSelector(providerName);
  const globalOverride = overridesBySelector
    ? overridesBySelector.get(GLOBAL_OVERRIDE_SELECTOR) || null
    : null;
  const override =
    selector && overridesBySelector
      ? overridesBySelector.get(selector) || null
      : null;
  const defaultEnabled = providerGroupDefaultEnabled(
    models,
    providerName,
    providerType,
  );
  const inheritedOverride = override || globalOverride;
  const userPaths =
    inheritedOverride && Array.isArray(inheritedOverride.user_paths)
      ? Array.from(new Set(inheritedOverride.user_paths)).sort()
      : [];

  return {
    selector,
    default_enabled: defaultEnabled,
    // Honor the override's enabled VALUE, not just its presence: a disabled
    // policy turns the selector off even though an override exists.
    effective_enabled: inheritedOverride
      ? inheritedOverride.enabled !== false
      : defaultEnabled,
    user_paths: userPaths,
    override,
  };
}

export function groupDisplayModels(rows, models, modelOverrideViews) {
  if (!Array.isArray(rows) || rows.length === 0) {
    return [];
  }

  const overridesBySelector = new Map();
  for (const override of Array.isArray(modelOverrideViews)
    ? modelOverrideViews
    : []) {
    const selector = String((override && override.selector) || "").trim();
    if (selector) {
      overridesBySelector.set(selector, override);
    }
  }

  const virtualRows = [];
  const groups = new Map();
  for (const row of rows) {
    if (row && row.is_alias) {
      virtualRows.push(row);
      continue;
    }
    const providerName = String((row && row.provider_name) || "").trim();
    const providerType = String((row && row.provider_type) || "").trim();
    const key =
      "provider-group:" + (providerName || providerType || "unassigned");
    if (!groups.has(key)) {
      groups.set(key, {
        key,
        provider_name: providerName,
        provider_type: providerType,
        display_name: providerGroupDisplayName(providerName, providerType),
        type_label: providerGroupTypeLabel(providerName, providerType),
        rows: [],
      });
    }

    const group = groups.get(key);
    if (!group.provider_name && providerName) {
      group.provider_name = providerName;
    }
    if (!group.provider_type && providerType) {
      group.provider_type = providerType;
    }
    group.display_name = providerGroupDisplayName(
      group.provider_name,
      group.provider_type,
    );
    group.type_label = providerGroupTypeLabel(
      group.provider_name,
      group.provider_type,
    );
    group.rows.push(row);
  }

  const result = Array.from(groups.values())
    .map((group) => {
      const access = providerGroupAccess(
        models,
        group.provider_name,
        group.provider_type,
        overridesBySelector,
      );
      return {
        ...group,
        access,
        access_summary: modelAccessSummary(access),
        item_count_label: providerGroupItemCountLabel(group.rows),
      };
    })
    .sort((a, b) =>
      String(a.display_name || "").localeCompare(String(b.display_name || "")),
    );
  if (virtualRows.length > 0) {
    result.unshift({
      key: "virtual-model-group",
      is_virtual_models: true,
      provider_name: "",
      provider_type: "",
      display_name: m.models_virtual_models_group(),
      type_label: "",
      rows: virtualRows,
      access: { selector: "" },
      access_summary: "",
      item_count_label: providerGroupItemCountLabel(virtualRows),
    });
  }
  return result;
}

export function buildGlobalScopeRow(models, modelOverrideViews) {
  const override = findModelOverrideView(
    modelOverrideViews,
    GLOBAL_OVERRIDE_SELECTOR,
  );
  const defaultEnabled = modelOverridesDefaultEnabled(models);
  const userPaths =
    override && Array.isArray(override.user_paths) ? override.user_paths : [];
  return {
    key: "scope-global",
    is_alias: false,
    display_name: m.models_global_scope_subject(),
    access: {
      selector: GLOBAL_OVERRIDE_SELECTOR,
      default_enabled: defaultEnabled,
      effective_enabled: override ? override.enabled !== false : defaultEnabled,
      user_paths: userPaths,
      override,
    },
  };
}

// ---- Row predicates / labels ----

export function rowAccessSelector(row) {
  if (!row) {
    return "";
  }
  const accessSelector = String(
    (row.access && row.access.selector) || "",
  ).trim();
  if (accessSelector) {
    return accessSelector;
  }
  const overrideSelector = String(row.override_selector || "").trim();
  if (overrideSelector) {
    return overrideSelector;
  }
  return qualifiedModelName(row);
}

// Returns a class array for Svelte's clsx-style class attribute.
export function displayRowClass(row) {
  if (!row) return [];
  const classes = [];
  if (row.is_alias) {
    classes.push("alias-row", aliasStateClass(row.alias));
  } else if (row.has_virtual_model) {
    // Real model rows carrying a virtual model render alias-like so operators
    // can spot them at a glance.
    classes.push("alias-row", "is-valid");
  }
  if (!row.is_alias && row.masking_alias) {
    classes.push("masked-model-row");
  }
  if (!row.is_alias && row.access && row.access.effective_enabled === false) {
    classes.push("model-access-disabled-row");
  }
  return classes;
}

export function aliasRowCanRemove(row) {
  return Boolean(
    row && row.is_alias && row.alias && row.alias.name && !row.alias.managed,
  );
}

export function rowRedirectCanRemove(row) {
  return Boolean(
    row &&
    !row.is_alias &&
    row.masking_alias &&
    row.masking_alias.name &&
    !row.masking_alias.managed,
  );
}

// rowAnchorID gives an alias row a DOM id unique to its name. The name is
// percent-encoded rather than squashed to hyphens, so distinct names such as
// "foo/bar" and "foo-bar" never share an id.
export function rowAnchorID(row) {
  if (!row) return "";
  if (row.is_alias && row.alias && row.alias.name) {
    return "alias-row-" + encodeURIComponent(String(row.alias.name));
  }
  return "";
}

// rowIsManaged reports whether a row is backed by a config-managed virtual
// model, which the admin API refuses to change.
export function rowIsManaged(row) {
  if (!row) {
    return false;
  }
  if (row.is_alias) {
    return Boolean(row.alias && row.alias.managed);
  }
  return Boolean(
    (row.access && row.access.override && row.access.override.managed) ||
    (row.masking_alias && row.masking_alias.managed),
  );
}

export function hasAccessOverride(access) {
  return Boolean(access && access.override);
}

export function modelOverrideEditButtonClass(hasOverride) {
  return hasOverride ? "table-action-btn-active" : "";
}

export function modelOverrideEditButtonLabel(subject, hasOverride) {
  const value = String(subject || m.models_global_access());
  return hasOverride
    ? m.models_edit_action_virtual({ subject: value })
    : m.models_edit_action({ subject: value });
}
