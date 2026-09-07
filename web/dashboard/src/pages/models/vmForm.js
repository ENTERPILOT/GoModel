// The virtual-model editor: form defaults and helpers, and the admin API payloads built from a form.
// Pure logic (no Svelte) so the node:test suite can exercise it directly;
// relative imports keep it loadable outside Vite.

import * as m from "../../lib/paraglide/messages.js";
import { cloneSchemaConfig } from "../../lib/utils/schemaFields.js";
import {
  GLOBAL_OVERRIDE_SELECTOR,
  qualifiedModelName,
} from "./modelIdentity.js";

import {
  collectExtraTargets,
  qualifyTarget,
  strategyLabel,
  strategyUsesWeights,
  targetEntry,
} from "./routing.js";

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
      options.unshift({
        value,
        label: value,
        description: m.models_this_model(),
      });
      continue;
    }
    options.push({
      value,
      label: value,
      description: String(model.provider_name || ""),
    });
  }
  for (const alias of Array.isArray(aliases) ? aliases : []) {
    const value = String((alias && alias.name) || "").trim();
    if (!value || value === current || seen.has(value)) continue;
    seen.add(value);
    options.push({
      value,
      label: value,
      description: m.models_virtual_model(),
    });
  }
  return options;
}

export function defaultVirtualModelForm() {
  return {
    source: "",
    target_provider: "",
    target_model: "",
    target_weight: 1,
    targets: [],
    strategy: "round_robin",
    // A "plugin" strategy names its routing plugin and carries that plugin's
    // route-scoped settings (see GET /admin/plugins route_fields).
    strategy_plugin: "",
    strategy_config: {},
    session_affinity: true,
    failover: true,
    user_paths: "",
    description: "",
    slowdown: "",
    enabled: true,
  };
}

// vmFormPluginStrategy reports whether the form routes through a plugin.
export function vmFormPluginStrategy(form) {
  return String((form && form.strategy) || "").toLowerCase() === "plugin";
}

// pluginStrategyFields returns the strategy_plugin/strategy_config pair to
// persist for a plugin strategy; an empty config is omitted.
function pluginStrategyFields(source) {
  const fields = {
    strategy_plugin: String((source && source.strategy_plugin) || "").trim(),
  };
  const config = cloneSchemaConfig(source && source.strategy_config);
  if (Object.keys(config).length > 0) {
    fields.strategy_config = config;
  }
  return fields;
}

export function vmFormHasPrimaryTarget(form) {
  return String((form && form.target_model) || "").trim() !== "";
}

// vmFormTargetCount is the total number of reorderable rows in the editor.
export function vmFormTargetCount(form) {
  if (!form) return 0;
  const primary = vmFormHasPrimaryTarget(form) ? 1 : 0;
  return primary + (Array.isArray(form.targets) ? form.targets.length : 0);
}

// normalizedTargetWeight mirrors the backend contract: a non-positive or
// unset weight counts as 1.
function normalizedTargetWeight(value) {
  const weight = Number(value);
  return Number.isFinite(weight) && weight > 0 ? weight : 1;
}

// vmFormPopulatedTargetCount counts only targets that hold a model; blank
// placeholder rows do not make reordering meaningful.
export function vmFormPopulatedTargetCount(form) {
  return flattenFormTargets(form).filter((target) => target.model !== "").length;
}

// flattenFormTargets returns the full target list in display order: primary
// first, so position 0 is the failover primary / first round-robin slot.
export function flattenFormTargets(form) {
  if (!form) return [];
  const list = [];
  if (vmFormHasPrimaryTarget(form)) {
    list.push({
      provider: String(form.target_provider || ""),
      model: String(form.target_model || "").trim(),
      weight: normalizedTargetWeight(form.target_weight),
    });
  }
  if (Array.isArray(form.targets)) {
    for (const target of form.targets) {
      list.push({
        provider: String((target && target.provider) || ""),
        model: String((target && target.model) || ""),
        weight: normalizedTargetWeight((target && target.weight)),
      });
    }
  }
  return list;
}

// moveFormTarget splices the flattened target list in place, writing the
// result back into the primary slot plus the extras array. Returns false on
// invalid bounds or no-op moves.
export function moveFormTarget(form, from, to) {
  if (!form) return false;
  const list = flattenFormTargets(form);
  const length = list.length;
  if (length < 2) return false;
  if (from < 0 || to < 0 || from >= length || to >= length) return false;
  if (from === to) return false;
  const [entry] = list.splice(from, 1);
  list.splice(to, 0, entry);
  const first = list[0];
  form.target_provider = String(first.provider || "");
  form.target_model = first.model || "";
  form.target_weight = normalizedTargetWeight(first.weight);
  form.targets = list.slice(1).map((row) => ({
    provider: row.provider || "",
    model: row.model || "",
    weight: normalizedTargetWeight(row.weight),
  }));
  return true;
}

// vmFormSelfOnly: the editor opened from a real model pins that model as its
// first target so an added target becomes a fallback rather than a
// replacement. With nothing added, the pinned row is not a redirect — the
// model just serves itself — and the form saves as a plain policy.
export function vmFormSelfOnly(form) {
  const source = String((form && form.source) || "").trim();
  const primary = String((form && form.target_model) || "").trim();
  return (
    Boolean(source) &&
    primary === source &&
    collectExtraTargets(form && form.targets).length === 0
  );
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
  return (
    Boolean(source) &&
    source !== GLOBAL_OVERRIDE_SELECTOR &&
    !source.endsWith("/")
  );
}

// vmFormHasExtraTargetRows: a second target row (filled or not) means the
// editor is configuring a load balancer.
function vmFormHasExtraTargetRows(form) {
  return (
    Boolean(form) && Array.isArray(form.targets) && form.targets.length > 0
  );
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
  return (
    vmFormHasExtraTargetRows(form) && strategyUsesWeights(form && form.strategy)
  );
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
  targets.push(
    ...collectExtraTargets(form && form.targets).map(qualifyTarget),
  );
  if (!source || targets.length === 0) return { text: "", replaces: false };

  const strategy = strategyLabel(form.strategy, form.strategy_plugin);
  const failover = isFailoverStrategy(form && form.strategy);
  const others = targets.filter((target) => target !== source);
  const selfFirst = targets[0] === source;

  if (sourceIsModel && others.length < targets.length) {
    if (others.length === 0) return { text: "", replaces: false };
    if (failover && selfFirst) {
      return {
        text: m.models_summary_failover({
          source,
          fallbacks: others.join(" → "),
        }),
        replaces: false,
      };
    }
    return {
      text: m.models_summary_balanced({
        source,
        peers: others.join(", "),
        strategy,
      }),
      replaces: false,
    };
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
        ? m.models_summary_replaced_failover({
            source,
            first: targets[0],
            rest: targets.slice(1).join(" → "),
          })
        : m.models_summary_alias_failover({
            source,
            first: targets[0],
            rest: targets.slice(1).join(" → "),
          }),
      replaces: sourceIsModel,
    };
  }
  return {
    text: sourceIsModel
      ? m.models_summary_replaced_balanced({
          source,
          targets: targets.join(", "),
          strategy,
        })
      : m.models_summary_alias_balanced({
          source,
          targets: targets.join(", "),
          strategy,
        }),
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
    form.target_provider = next.provider || "";
    form.target_model = next.model || "";
    form.target_weight = next.weight || 1;
    return;
  }
  form.target_provider = "";
  form.target_model = "";
  form.target_weight = 1;
}

// aliasFormTargets maps a stored alias onto the editor's primary/extra target
// fields (a stored target with no explicit weight is the neutral default 1).
// An explicit provider rides along so an edit does not turn a pinned target
// into a by-name one.
export function aliasFormTargets(alias) {
  const lbTargets = Array.isArray(alias && alias.targets) ? alias.targets : [];
  if (lbTargets.length > 0) {
    return {
      primaryProvider: lbTargets[0].provider || "",
      primaryModel: qualifyTarget(lbTargets[0]),
      primaryWeight: lbTargets[0].weight || 1,
      extraTargets: lbTargets.slice(1).map((target) => ({
        provider: target.provider || "",
        model: qualifyTarget(target),
        weight: target.weight || 1,
      })),
    };
  }
  return {
    primaryProvider: (alias && alias.target_provider) || "",
    primaryModel:
      alias && alias.target_provider
        ? alias.target_provider + "/" + alias.target_model
        : (alias && alias.target_model) || "",
    primaryWeight: 1,
    extraTargets: [],
  };
}

// weightless strips the weight from an API target for strategies that ignore
// it, so a value with no effect is not persisted.
function weightless(target) {
  return target.provider
    ? { provider: target.provider, model: target.model }
    : { model: target.model };
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
      targets.push(
        targetEntry(primaryTarget, form.target_weight, form.target_provider),
      );
    }
    targets.push(...extraTargets);
    if (targets.length > 1) {
      // Multiple targets load balance; carry the chosen strategy. Strategies
      // that ignore weight drop it rather than persist a value with no effect.
      const strategy = form.strategy || "round_robin";
      payload.targets = strategyUsesWeights(strategy)
        ? targets
        : targets.map(weightless);
      payload.strategy = strategy;
      if (vmFormPluginStrategy(form)) {
        Object.assign(payload, pluginStrategyFields(form));
      }
      // Affinity defaults to on server-side; only an explicit opt-out is sent.
      if (form && form.session_affinity === false) {
        payload.session_affinity = false;
      }
      // Same for failover: on by default, only the opt-out travels.
      if (form && form.failover === false) {
        payload.failover = false;
      }
    } else if (targets[0].provider) {
      // Only the targets list can carry an explicit provider.
      payload.targets = [weightless(targets[0])];
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
    if (vmFormPluginStrategy(alias)) {
      Object.assign(payload, pluginStrategyFields(alias));
    }
    if (alias.session_affinity === false) {
      payload.session_affinity = false;
    }
    if (alias.failover === false) {
      payload.failover = false;
    }
    // Strategies that ignore weight persist weight-less targets — same
    // contract as the editor save path.
    const targets = lbTargets.map((target) =>
      targetEntry(qualifyTarget(target), target.weight, target.provider),
    );
    payload.targets = strategyUsesWeights(payload.strategy)
      ? targets
      : targets.map(weightless);
  } else if (lbTargets.length === 1 && lbTargets[0].provider) {
    payload.targets = [weightless(targetEntry(qualifyTarget(lbTargets[0]), null, lbTargets[0].provider))];
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
    existingPolicy && Array.isArray(existingPolicy.user_paths)
      ? existingPolicy.user_paths
      : [];
  const existingDescription = String(
    (existingPolicy && existingPolicy.description) || "",
  ).trim();
  const hasExistingSlowdown = Boolean(
    existingPolicy && existingPolicy.slowdown != null,
  );
  const existingSlowdown = Number(
    hasExistingSlowdown ? existingPolicy.slowdown : 0,
  );

  const preserved = {};
  if (existingDescription) preserved.description = existingDescription;
  if (hasExistingSlowdown && Number.isFinite(existingSlowdown)) {
    preserved.slowdown = existingSlowdown;
  }

  let method = "PUT";
  let payload;
  if (desired === false) {
    payload = {
      source: selector,
      enabled: false,
      user_paths: existingPaths,
      ...preserved,
    };
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
    payload = {
      source: selector,
      enabled: true,
      user_paths: existingPaths,
      ...preserved,
    };
  }
  return { method, payload, desired };
}
