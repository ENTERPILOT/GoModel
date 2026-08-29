// Targets, strategies and what a virtual model does with requests for its source: view mapping, labels, and the routing kind shown on the models list.
// Pure logic (no Svelte) so the node:test suite can exercise it directly;
// relative imports keep it loadable outside Vite.

import * as m from "../../lib/paraglide/messages.js";

import { normalizedAliasName } from "./modelIdentity.js";

// qualifyTarget renders a {provider, model} target as a single selector. The
// model name may itself contain slashes (e.g. provider "groq" with model
// "openai/gpt-oss-120b"), so only skip re-qualifying when the model already
// carries this provider's prefix — never on the mere presence of a slash.
export function qualifyTarget(target) {
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
export function targetEntry(model, weightValue) {
  const entry = { model };
  const weight = parseWeight(weightValue);
  if (weight !== null) {
    entry.weight = weight;
  }
  return entry;
}

// collectExtraTargets normalizes the additional-target rows into API targets,
// dropping blanks and invalid weights.
export function collectExtraTargets(rows) {
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
export function strategyLabel(strategy) {
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

export function strategyUsesWeights(strategy) {
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
function aliasTargets(alias) {
  if (!alias) return [];
  const targets = Array.isArray(alias.targets)
    ? alias.targets.map(qualifyTarget).filter(Boolean)
    : [];
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
    return (
      targetListLabel(targets, ", ") + " · " + strategyLabel(alias.strategy)
    );
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
  const selfIndex = targets.findIndex(
    (target) => normalizedAliasName(target) === source,
  );
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
  if (String((alias && alias.strategy) || "").toLowerCase() === "failover")
    return true;
  return !(alias && alias.failover === false);
}

// maskingRoutingLabel is the secondary line next to the kind's prefix: the
// fallbacks in order, the balancing peers with the strategy, or the redirect
// destination.
export function maskingRoutingLabel(alias, kind) {
  const source = normalizedAliasName(alias && alias.name);
  const others = aliasTargets(alias).filter(
    (target) => normalizedAliasName(target) !== source,
  );
  switch (kind) {
    case "failover":
      return targetListLabel(others, " → ");
    case "balanced":
      return (
        targetListLabel(others, ", ") +
        " · " +
        strategyLabel(alias && alias.strategy)
      );
    default:
      return aliasTargetLabel(alias);
  }
}

export function aliasStateClass(alias) {
  if (!alias) return "is-invalid";
  if (alias.enabled === false) return "is-disabled";
  if (!alias.valid) return "is-invalid";
  return "is-valid";
}

export function aliasStateText(alias) {
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

export function modelAccessSummary(access) {
  if (!access) {
    return "";
  }

  const parts = [];
  if (access.effective_enabled === false) {
    parts.push(
      access.default_enabled === false
        ? m.models_disabled_default()
        : m.models_disabled(),
    );
  }

  const userPaths = Array.isArray(access.user_paths) ? access.user_paths : [];
  if (userPaths.length > 0) {
    parts.push(m.models_allowed_for({ paths: userPaths.join(", ") }));
  }

  return parts.join(" · ");
}
