// Pure virtual-models logic. Every function is side-effect free (except
// removePrimaryTarget, which mutates the form it is given) so the node:test
// suite can exercise it without Svelte.

import * as m from "../../lib/paraglide/messages.js";

export const GLOBAL_OVERRIDE_SELECTOR = "/";

// ---- Naming / identity helpers ----

export function normalizedAliasName(name) {
  return String(name || "").trim().toLowerCase();
}

export function qualifiedModelName(model) {
  if (!model) {
    return "";
  }
  const selector = String(model.selector || "").trim();
  if (selector) {
    return selector;
  }
  if (!model.model || !model.model.id) {
    return "";
  }
  const modelID = String(model.model.id || "").trim();
  const providerName = String(model.provider_name || "").trim();
  if (providerName) {
    return providerName + "/" + modelID;
  }
  const providerType = String(model.provider_type || "").trim();
  if (!providerType || modelID.includes("/")) {
    return modelID;
  }
  return providerType + "/" + modelID;
}

function modelIdentifierKeys(modelID, providerType, providerName, selector) {
  const keys = new Set();
  const normalizedModelID = String(modelID || "").trim().toLowerCase();
  const provider = String(providerType || "").trim().toLowerCase();
  const providerLabel = String(providerName || "").trim().toLowerCase();
  const normalizedSelector = String(selector || "").trim().toLowerCase();
  if (normalizedSelector) {
    keys.add(normalizedSelector);
  }
  if (!normalizedModelID) {
    return keys;
  }

  keys.add(normalizedModelID);
  if (providerLabel) {
    keys.add(providerLabel + "/" + normalizedModelID);
  }
  if (provider && !normalizedModelID.includes("/")) {
    keys.add(provider + "/" + normalizedModelID);
  }

  const parts = normalizedModelID.split("/");
  if (parts.length === 2 && parts[1]) {
    keys.add(parts[1]);
  }

  return keys;
}

export function modelKeys(model) {
  return modelIdentifierKeys(
    model && model.model ? model.model.id : "",
    model ? model.provider_type : "",
    model ? model.provider_name : "",
    model ? model.selector : "",
  );
}

function aliasKeys(alias) {
  const keys = new Set();
  const resolved = String(alias.resolved_model || "").trim().toLowerCase();
  const targetModel = String(alias.target_model || "").trim().toLowerCase();
  const targetProvider = String(alias.target_provider || "").trim().toLowerCase();
  if (resolved) {
    keys.add(resolved);
    const resolvedParts = resolved.split("/");
    if (resolvedParts.length === 2 && resolvedParts[1]) {
      keys.add(resolvedParts[1]);
    }
  }
  if (targetModel) {
    keys.add(targetModel);
    const targetParts = targetModel.split("/");
    if (targetParts.length === 2 && targetParts[1]) {
      keys.add(targetParts[1]);
    }
  }
  if (targetModel && targetProvider) {
    keys.add(targetProvider + "/" + targetModel);
  }
  return keys;
}

// ---- Load-balancing target helpers ----

// qualifyTarget renders a {provider, model} target as a single selector. The
// model name may itself contain slashes (e.g. provider "groq" with model
// "openai/gpt-oss-120b"), so only skip re-qualifying when the model already
// carries this provider's prefix — never on the mere presence of a slash.
function qualifyTarget(target) {
  if (!target) {
    return "";
  }
  const provider = String(target.provider || "").trim();
  const model = String(target.model || "").trim();
  if (!provider || !model) {
    return model;
  }
  if (model === provider || model.startsWith(provider + "/")) {
    return model;
  }
  return provider + "/" + model;
}

function parseWeight(value) {
  if (value === "" || value === null || value === undefined) {
    return null;
  }
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return null;
  }
  return parsed;
}

// targetEntry builds a {model, weight?} API target, attaching weight only when
// it parses to a positive number.
function targetEntry(model, weightValue) {
  const entry = { model };
  const weight = parseWeight(weightValue);
  if (weight !== null) {
    entry.weight = weight;
  }
  return entry;
}

// collectExtraTargets normalizes the additional-target rows into API targets,
// dropping blanks and invalid weights.
function collectExtraTargets(rows) {
  const safeRows = Array.isArray(rows) ? rows : [];
  const targets = [];
  for (const row of safeRows) {
    const model = String((row && row.model) || "").trim();
    if (model) {
      targets.push(targetEntry(model, row && row.weight));
    }
  }
  return targets;
}

// strategyLabel is the short strategy name used in list cells and summaries
// (the editor dropdown carries the long, explanatory labels).
function strategyLabel(strategy) {
  switch (String(strategy || "").toLowerCase()) {
    case "cost":
      return m.models_strategy_name_cost();
    case "failover":
      return m.models_strategy_name_failover();
    case "adaptive":
      return m.models_strategy_name_adaptive();
    case "round_robin":
    case "":
      return m.models_strategy_name_round_robin();
    default:
      return strategy;
  }
}

// Strategies where per-target weight has no effect: cost always routes to the
// cheapest target and failover always to the first available one, so the
// editor hides weights and the save path strips them.
const WEIGHTLESS_STRATEGIES = new Set(["cost", "failover"]);

function strategyUsesWeights(strategy) {
  return !WEIGHTLESS_STRATEGIES.has(String(strategy || "").toLowerCase());
}

// Editor labels per strategy value. Values come from the backend
// (VIRTUAL_MODEL_STRATEGIES); presentation stays here so copy does not ship
// with the gateway. Unknown values fall back to the raw value.
const STRATEGY_OPTION_LABELS = {
  round_robin: m.models_strategy_round_robin(),
  cost: m.models_strategy_cost(),
  adaptive: m.models_strategy_adaptive(),
  failover: m.models_strategy_failover(),
};

// strategyOptions builds the editor dropdown from the deployment's supported
// strategies, always including the edited row's current value so an existing
// virtual model never renders a blank select (and never gets silently
// rewritten to another strategy on save).
export function strategyOptions(supported, current) {
  const values = Array.isArray(supported) ? [...supported] : [];
  const active = String(current || "")
    .trim()
    .toLowerCase();
  if (active && !values.includes(active)) {
    values.push(active);
  }
  return values.map((value) => ({
    value,
    label: STRATEGY_OPTION_LABELS[value] || value,
  }));
}

// ---- Views mapping ----

// mapRedirectView maps a redirect View into the shape the renderer needs.
export function mapRedirectView(view) {
  const rawTargets = Array.isArray(view.targets) ? view.targets : [];
  const target = rawTargets.length > 0 ? rawTargets[0] : {};
  const targets = rawTargets.map((entry) => {
    const mapped = { provider: entry.provider || "", model: entry.model || "" };
    if (entry.weight) {
      mapped.weight = entry.weight;
    }
    return mapped;
  });
  return {
    name: view.source,
    target_provider: target.provider || "",
    target_model: target.model || "",
    targets,
    strategy: view.strategy || "",
    // Tri-state on the wire; only explicit false disables session affinity.
    session_affinity: view.session_affinity !== false,
    failover: view.failover !== false,
    description: view.description || "",
    slowdown: view.slowdown == null ? null : Number(view.slowdown),
    enabled: view.enabled !== false,
    managed: Boolean(view.managed),
    valid: Boolean(view.valid),
    resolved_model: view.resolved_model || "",
    provider_type: view.provider_type || "",
    user_paths: Array.isArray(view.user_paths) ? view.user_paths : [],
  };
}

// splitVirtualModelViews splits /admin/virtual-models Views into redirect
// aliases (renderer shape) and access-policy overrides.
export function splitVirtualModelViews(views) {
  const safeViews = Array.isArray(views) ? views : [];
  const aliases = [];
  const policies = [];
  for (const view of safeViews) {
    if (!view || typeof view !== "object") {
      continue;
    }
    if (view.kind === "redirect") {
      aliases.push(mapRedirectView(view));
    } else if (view.kind === "policy") {
      policies.push({
        selector: view.source,
        provider_name: view.provider_name || "",
        model: view.model || "",
        user_paths: Array.isArray(view.user_paths) ? view.user_paths : [],
        description: view.description || "",
        slowdown: view.slowdown == null ? null : Number(view.slowdown),
        enabled: view.enabled !== false,
        managed: Boolean(view.managed),
        scope_kind: view.scope_kind || "",
      });
    }
  }
  return { aliases, policies };
}

// ---- Alias / access presentation ----

// aliasTargets lists a redirect's targets as qualified selectors, falling
// back to the single-target fields of a plain alias.
export function aliasTargets(alias) {
  if (!alias) return [];
  const targets = Array.isArray(alias.targets) ? alias.targets.map(qualifyTarget).filter(Boolean) : [];
  if (targets.length > 0) return targets;
  const single = alias.target_provider
    ? alias.target_provider + "/" + alias.target_model
    : String(alias.target_model || "");
  return single ? [single] : [];
}

// targetListLabel joins targets for a table cell, truncating long lists.
function targetListLabel(targets, separator) {
  const shown = targets.slice(0, 3);
  const more = targets.length - shown.length;
  return shown.join(separator) + (more > 0 ? " +" + more : "");
}

export function aliasTargetLabel(alias) {
  if (!alias) return "—";
  const targets = aliasTargets(alias);
  if (targets.length > 1) {
    return targetListLabel(targets, ", ") + " · " + strategyLabel(alias.strategy);
  }
  if (alias.resolved_model) return alias.resolved_model;
  return targets[0] || "—";
}

// ---- Routing kind of a virtual model over a real model ----

// maskingRoutingKind says what a redirect whose source is a real model does
// to requests for that model, so the list can show the right icon and words:
//   "failover" — the model serves first; the other targets are its safety net
//                (failover strategy with the model as the first target).
//   "balanced" — the model is one of several targets a strategy spreads
//                requests across (it may or may not fail over).
//   "redirect" — the model is not a target at all: requests go elsewhere.
// With failover switched off globally, a failover-strategy redirect always
// serves its first target, which for these rows is the model itself, so it
// reads as balanced-without-failover rather than promising a fallback.
export function maskingRoutingKind(alias, globalFailover = true) {
  const targets = aliasTargets(alias);
  const source = normalizedAliasName(alias && alias.name);
  const selfIndex = targets.findIndex((target) => normalizedAliasName(target) === source);
  if (selfIndex < 0) return "redirect";
  const strategy = String((alias && alias.strategy) || "").toLowerCase();
  if (strategy === "failover") {
    if (selfIndex !== 0) return "redirect";
    return globalFailover && targets.length > 1 ? "failover" : "balanced";
  }
  return "balanced";
}

// maskingFailsOver reports whether a balanced/failover row actually retries
// on the remaining targets, for the tooltip.
export function maskingFailsOver(alias, globalFailover = true) {
  if (!globalFailover) return false;
  if (aliasTargets(alias).length < 2) return false;
  if (String((alias && alias.strategy) || "").toLowerCase() === "failover") return true;
  return !(alias && alias.failover === false);
}

// maskingRoutingLabel is the secondary line next to the kind's prefix: the
// fallbacks in order, the balancing peers with the strategy, or the redirect
// destination.
export function maskingRoutingLabel(alias, kind) {
  const source = normalizedAliasName(alias && alias.name);
  const others = aliasTargets(alias).filter((target) => normalizedAliasName(target) !== source);
  switch (kind) {
    case "failover":
      return targetListLabel(others, " → ");
    case "balanced":
      return targetListLabel(others, ", ") + " · " + strategyLabel(alias && alias.strategy);
    default:
      return aliasTargetLabel(alias);
  }
}

function aliasStateClass(alias) {
  if (!alias) return "is-invalid";
  if (alias.enabled === false) return "is-disabled";
  if (!alias.valid) return "is-invalid";
  return "is-valid";
}

function aliasStateText(alias) {
  if (!alias) return m.models_invalid();
  if (alias.enabled === false) return m.models_disabled();
  if (!alias.valid) return m.models_invalid();
  return m.models_active();
}

export function modelAccessUserPathsRestrict(paths) {
  return Array.isArray(paths) && paths.length > 0 && paths.indexOf("/") === -1;
}

export function modelAccessStateClass(access, virtualModelsAvailable) {
  if (!virtualModelsAvailable) return "";
  if (!access) return "";
  if (access.effective_enabled === false) return "is-disabled";
  if (modelAccessUserPathsRestrict(access.user_paths)) {
    return "is-restricted";
  }
  return "is-enabled";
}

function modelAccessSummary(access) {
  if (!access) {
    return "";
  }

  const parts = [];
  if (access.effective_enabled === false) {
    parts.push(access.default_enabled === false ? m.models_disabled_default() : m.models_disabled());
  }

  const userPaths = Array.isArray(access.user_paths) ? access.user_paths : [];
  if (userPaths.length > 0) {
    parts.push(m.models_allowed_for({ paths: userPaths.join(", ") }));
  }

  return parts.join(" · ");
}

// ---- Display rows ----

export function buildDisplayModels({ models, aliases, virtualModelsAvailable, activeCategory }) {
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
    const sourceModel = concreteModelsByKey.get(normalizedAliasName(alias && alias.name));
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
      model: targetModel ? targetModel.model : { id: alias.name, object: "model" },
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
    return String(a.display_name || "").localeCompare(String(b.display_name || ""));
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
      row.model && row.model.metadata && row.model.metadata.modes
        ? row.model.metadata.modes.join(",")
        : "",
      row.model && row.model.metadata && row.model.metadata.categories
        ? row.model.metadata.categories.join(",")
        : "",
    ];
    return fields.some((value) => String(value || "").toLowerCase().includes(filter));
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
  if (!normalizedProviderType || normalizedProviderType === normalizedProviderName) {
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
    const modelProviderName = String((model && model.provider_name) || "").trim();
    const modelProviderType = String((model && model.provider_type) || "").trim();
    if (normalizedProviderName && modelProviderName !== normalizedProviderName) {
      continue;
    }
    if (!normalizedProviderName && normalizedProviderType && modelProviderType !== normalizedProviderType) {
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
  for (const override of Array.isArray(modelOverrideViews) ? modelOverrideViews : []) {
    if (String((override && override.selector) || "").trim() === normalizedSelector) {
      return override;
    }
  }
  return null;
}

function providerGroupAccess(models, providerName, providerType, overridesBySelector) {
  const selector = providerOverrideSelector(providerName);
  const globalOverride = overridesBySelector
    ? overridesBySelector.get(GLOBAL_OVERRIDE_SELECTOR) || null
    : null;
  const override = selector && overridesBySelector ? overridesBySelector.get(selector) || null : null;
  const defaultEnabled = providerGroupDefaultEnabled(models, providerName, providerType);
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
    effective_enabled: inheritedOverride ? inheritedOverride.enabled !== false : defaultEnabled,
    user_paths: userPaths,
    override,
  };
}

export function groupDisplayModels(rows, models, modelOverrideViews) {
  if (!Array.isArray(rows) || rows.length === 0) {
    return [];
  }

  const overridesBySelector = new Map();
  for (const override of Array.isArray(modelOverrideViews) ? modelOverrideViews : []) {
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
    const key = "provider-group:" + (providerName || providerType || "unassigned");
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
    group.display_name = providerGroupDisplayName(group.provider_name, group.provider_type);
    group.type_label = providerGroupTypeLabel(group.provider_name, group.provider_type);
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
    .sort((a, b) => String(a.display_name || "").localeCompare(String(b.display_name || "")));
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
  const override = findModelOverrideView(modelOverrideViews, GLOBAL_OVERRIDE_SELECTOR);
  const defaultEnabled = modelOverridesDefaultEnabled(models);
  const userPaths = override && Array.isArray(override.user_paths) ? override.user_paths : [];
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
  const accessSelector = String((row.access && row.access.selector) || "").trim();
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
  return Boolean(row && row.is_alias && row.alias && row.alias.name && !row.alias.managed);
}

export function rowRedirectCanRemove(row) {
  return Boolean(
    row && !row.is_alias && row.masking_alias && row.masking_alias.name && !row.masking_alias.managed,
  );
}

export function rowAnchorID(row) {
  if (!row) return "";
  if (row.is_alias && row.alias && row.alias.name) {
    return "alias-row-" + String(row.alias.name).replace(/[^a-zA-Z0-9_-]+/g, "-");
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

// ---- Editor form helpers ----

// virtualModelTargetOptions lists what a target row may pick: every catalog
// model (described by its provider) and every other redirect, since a target
// may chain through a virtual model — the one being edited excluded, as a
// redirect cannot target itself alone. When the source is itself a catalog
// model, that model is listed first as "this model": naming it as a target
// keeps serving it and turns the other targets into fallbacks.
export function virtualModelTargetOptions(models, aliases, source) {
  const current = String(source || "").trim();
  const options = [];
  const seen = new Set();
  for (const model of Array.isArray(models) ? models : []) {
    const value = qualifiedModelName(model);
    if (!value || seen.has(value)) continue;
    seen.add(value);
    if (value === current) {
      options.unshift({ value, label: value, description: m.models_this_model() });
      continue;
    }
    options.push({ value, label: value, description: String(model.provider_name || "") });
  }
  for (const alias of Array.isArray(aliases) ? aliases : []) {
    const value = String((alias && alias.name) || "").trim();
    if (!value || value === current || seen.has(value)) continue;
    seen.add(value);
    options.push({ value, label: value, description: m.models_virtual_model() });
  }
  return options;
}

export function defaultVirtualModelForm() {
  return {
    source: "",
    target_model: "",
    target_weight: 1,
    targets: [],
    strategy: "round_robin",
    session_affinity: true,
    failover: true,
    user_paths: "",
    description: "",
    slowdown: "",
    enabled: true,
  };
}

export function vmFormHasPrimaryTarget(form) {
  return String((form && form.target_model) || "").trim() !== "";
}

// vmFormSelfOnly: the editor opened from a real model pins that model as its
// first target so an added target becomes a fallback rather than a
// replacement. With nothing added, the pinned row is not a redirect — the
// model just serves itself — and the form saves as a plain policy.
export function vmFormSelfOnly(form) {
  const source = String((form && form.source) || "").trim();
  const primary = String((form && form.target_model) || "").trim();
  return Boolean(source) && primary === source && collectExtraTargets(form && form.targets).length === 0;
}

export function vmFormIsRedirect(form) {
  if (vmFormSelfOnly(form)) {
    return false;
  }
  if (String((form && form.target_model) || "").trim()) {
    return true;
  }
  return collectExtraTargets(form && form.targets).length > 0;
}

export function vmFormSupportsSlowdown(form) {
  if (vmFormIsRedirect(form)) {
    return true;
  }
  const source = String((form && form.source) || "").trim();
  return Boolean(source) && source !== GLOBAL_OVERRIDE_SELECTOR && !source.endsWith("/");
}

// vmFormHasExtraTargetRows: a second target row (filled or not) means the
// editor is configuring a load balancer.
export function vmFormHasExtraTargetRows(form) {
  return Boolean(form) && Array.isArray(form.targets) && form.targets.length > 0;
}

// vmFormStrategyPending: the strategy is always shown, so the editor does not
// change shape as targets come and go; until a second target row exists the
// dropdown is disabled and its help explains that the choice applies from
// the second target on.
export function vmFormStrategyPending(form) {
  return !vmFormHasExtraTargetRows(form);
}

// vmFormShowWeights: weights are hidden for strategies that ignore them, to
// avoid implying they have an effect, and until there is something to
// balance against.
export function vmFormShowWeights(form) {
  return vmFormHasExtraTargetRows(form) && strategyUsesWeights(form && form.strategy);
}

// vmFormShowBalancingOptions: session keeping and the failover opt-out only
// mean something for strategies that spread requests. The failover strategy
// always retries its primary first (nothing to pin) and always fails over
// (nothing to opt out of), so both checkboxes are hidden for it.
export function vmFormShowBalancingOptions(form) {
  return !isFailoverStrategy(form && form.strategy);
}

// vmRoutingSummary spells out, in one sentence, what saving the form does to
// requests for its source, so the effect is visible before it is saved —
// above all that a real model listed without itself as a target is replaced,
// not backed up. It returns "" for a form with no target (an access policy)
// and flags the replacement case so the editor can make it stand out.
export function vmRoutingSummary(form, sourceIsModel) {
  const source = String((form && form.source) || "").trim();
  const targets = [];
  const primary = String((form && form.target_model) || "").trim();
  if (primary) targets.push(primary);
  targets.push(...collectExtraTargets(form && form.targets).map((target) => target.model));
  if (!source || targets.length === 0) return { text: "", replaces: false };

  const strategy = strategyLabel(form.strategy);
  const failover = isFailoverStrategy(form && form.strategy);
  const others = targets.filter((target) => target !== source);
  const selfFirst = targets[0] === source;

  if (sourceIsModel && others.length < targets.length) {
    if (others.length === 0) return { text: "", replaces: false };
    if (failover && selfFirst) {
      return { text: m.models_summary_failover({ source, fallbacks: others.join(" → ") }), replaces: false };
    }
    return { text: m.models_summary_balanced({ source, peers: others.join(", "), strategy }), replaces: false };
  }
  if (targets.length === 1) {
    return {
      text: sourceIsModel
        ? m.models_summary_replaced({ source, target: targets[0] })
        : m.models_summary_alias({ source, target: targets[0] }),
      replaces: sourceIsModel,
    };
  }
  if (failover) {
    return {
      text: sourceIsModel
        ? m.models_summary_replaced_failover({ source, first: targets[0], rest: targets.slice(1).join(" → ") })
        : m.models_summary_alias_failover({ source, first: targets[0], rest: targets.slice(1).join(" → ") }),
      replaces: sourceIsModel,
    };
  }
  return {
    text: sourceIsModel
      ? m.models_summary_replaced_balanced({ source, targets: targets.join(", "), strategy })
      : m.models_summary_alias_balanced({ source, targets: targets.join(", "), strategy }),
    replaces: sourceIsModel,
  };
}

function isFailoverStrategy(strategy) {
  return String(strategy || "").toLowerCase() === "failover";
}

// removePrimaryTarget clears the first target row, promoting the next
// additional target (if any) into that slot to keep the list contiguous.
export function removePrimaryTarget(form) {
  const rows = Array.isArray(form.targets) ? form.targets : [];
  if (rows.length > 0) {
    const next = rows.shift();
    form.target_model = next.model || "";
    form.target_weight = next.weight || 1;
    return;
  }
  form.target_model = "";
  form.target_weight = 1;
}

// aliasFormTargets maps a stored alias onto the editor's primary/extra target
// fields (a stored target with no explicit weight is the neutral default 1).
export function aliasFormTargets(alias) {
  const lbTargets = Array.isArray(alias && alias.targets) ? alias.targets : [];
  if (lbTargets.length > 0) {
    return {
      primaryModel: qualifyTarget(lbTargets[0]),
      primaryWeight: lbTargets[0].weight || 1,
      extraTargets: lbTargets.slice(1).map((target) => ({
        model: qualifyTarget(target),
        weight: target.weight || 1,
      })),
    };
  }
  return {
    primaryModel: alias && alias.target_provider
      ? alias.target_provider + "/" + alias.target_model
      : (alias && alias.target_model) || "",
    primaryWeight: 1,
    extraTargets: [],
  };
}

export function normalizeUserPaths(raw) {
  return String(raw || "")
    .split(/\r?\n|,/)
    .map((value) => String(value || "").trim())
    .filter(Boolean);
}

// ---- API payload builders ----

// buildVirtualModelSavePayload assembles the unified editor's PUT body: a
// filled target makes a redirect/alias (several targets load balance by
// strategy), an empty one makes an access policy.
export function buildVirtualModelSavePayload(form, originalSource, mode) {
  const source = String((form && form.source) || "").trim();
  const primaryTarget = String((form && form.target_model) || "").trim();
  const extraTargets = collectExtraTargets(form && form.targets);
  const isRedirect = vmFormIsRedirect(form);
  const original = String(originalSource || "").trim();
  const isRename = mode === "edit" && Boolean(original) && source !== original;

  const payload = {
    source,
    user_paths: normalizeUserPaths(form && form.user_paths),
    description: String((form && form.description) || "").trim(),
    enabled: Boolean(form && form.enabled),
  };
  const slowdownInput = form && form.slowdown;
  if (slowdownInput !== "" && slowdownInput != null) {
    const slowdown = Number(slowdownInput);
    if (Number.isFinite(slowdown)) {
      payload.slowdown = slowdown;
    }
  }
  if (isRename) {
    // Carry the prior key so the backend moves the row instead of leaving an
    // orphan behind under the old source.
    payload.old_source = original;
  }
  if (isRedirect) {
    const targets = [];
    if (primaryTarget) {
      targets.push(targetEntry(primaryTarget, form.target_weight));
    }
    targets.push(...extraTargets);
    if (targets.length > 1) {
      // Multiple targets load balance; carry the chosen strategy. Strategies
      // that ignore weight drop it rather than persist a value with no effect.
      const strategy = form.strategy || "round_robin";
      payload.targets = strategyUsesWeights(strategy)
        ? targets
        : targets.map((target) => ({ model: target.model }));
      payload.strategy = strategy;
      // Affinity defaults to on server-side; only an explicit opt-out is sent.
      if (form && form.session_affinity === false) {
        payload.session_affinity = false;
      }
      // Same for failover: on by default, only the opt-out travels.
      if (form && form.failover === false) {
        payload.failover = false;
      }
    } else {
      // A single target stays a plain alias on the back-compat field.
      payload.target_model = targets[0].model;
    }
  }
  return { payload, source, isRedirect, isRename };
}

// buildAliasTogglePayload flips an alias's enabled flag, round-tripping every
// target and the strategy so toggling a load-balanced redirect never
// collapses it to its first target.
export function buildAliasTogglePayload(alias) {
  const payload = {
    source: alias.name,
    description: String(alias.description || "").trim(),
    user_paths: Array.isArray(alias.user_paths) ? alias.user_paths : [],
    enabled: alias.enabled === false,
  };
  if (alias.slowdown != null) {
    const slowdown = Number(alias.slowdown);
    if (Number.isFinite(slowdown)) {
      payload.slowdown = slowdown;
    }
  }
  const lbTargets = Array.isArray(alias.targets) ? alias.targets : [];
  if (lbTargets.length > 1) {
    payload.strategy = alias.strategy || "round_robin";
    if (alias.session_affinity === false) {
      payload.session_affinity = false;
    }
    if (alias.failover === false) {
      payload.failover = false;
    }
    // Strategies that ignore weight persist weight-less targets — same
    // contract as the editor save path.
    payload.targets = strategyUsesWeights(payload.strategy)
      ? lbTargets.map((target) => targetEntry(qualifyTarget(target), target.weight))
      : lbTargets.map((target) => ({ model: qualifyTarget(target) }));
  } else if (lbTargets.length === 1) {
    payload.target_model = qualifyTarget(lbTargets[0]);
  } else {
    payload.target_model = alias.target_provider
      ? alias.target_provider + "/" + alias.target_model
      : alias.target_model;
  }
  return payload;
}

// buildModelTogglePayload flips a real-model/provider/global policy row.
export function buildModelTogglePayload(selector, existingPolicy, access) {
  const safeAccess = access || {};
  const desired = !(safeAccess.effective_enabled !== false);
  const existingPaths =
    existingPolicy && Array.isArray(existingPolicy.user_paths) ? existingPolicy.user_paths : [];
  const existingDescription = String((existingPolicy && existingPolicy.description) || "").trim();
  const hasExistingSlowdown = Boolean(existingPolicy && existingPolicy.slowdown != null);
  const existingSlowdown = Number(hasExistingSlowdown ? existingPolicy.slowdown : 0);

  const preserved = {};
  if (existingDescription) preserved.description = existingDescription;
  if (hasExistingSlowdown && Number.isFinite(existingSlowdown)) {
    preserved.slowdown = existingSlowdown;
  }

  let method = "PUT";
  let payload;
  if (desired === false) {
    payload = { source: selector, enabled: false, user_paths: existingPaths, ...preserved };
  } else if (
    existingPolicy &&
    existingPaths.length === 0 &&
    !existingDescription &&
    !hasExistingSlowdown &&
    safeAccess.default_enabled !== false
  ) {
    // Removing a path-less policy only enables the model when the default is
    // on; in a default-disabled deployment we must keep an explicit enabled
    // policy instead of falling back to the default.
    method = "DELETE";
    payload = { source: selector };
  } else {
    payload = { source: selector, enabled: true, user_paths: existingPaths, ...preserved };
  }
  return { method, payload, desired };
}

// ---- Incremental render batching ----

// computeRenderStep advances the bounded row-render window by one batch.
export function computeRenderStep(currentLimit, batchSize, total) {
  const size = Math.max(1, Number(batchSize || 75));
  const limit = Math.min(total, currentLimit + size);
  return { limit, rendering: limit < total };
}

// initialRenderStep starts a fresh render pass with the first batch.
export function initialRenderStep(batchSize, total) {
  return computeRenderStep(0, batchSize, total);
}
