// Pure failover logic (qualifiedModelName is not in $lib/utils/format.ts,
// which only has value-shaped variants). Kept rune-free so node:test can
// exercise it directly; the failover store delegates here.

import { qualifiedModelName } from "./virtualModelsLogic.js";

export { qualifiedModelName };

export function failoverPrimaryModel(rule) {
  return String((rule && (rule.primary_model || rule.source)) || "").trim();
}

export function failoverTargets(rule) {
  if (Array.isArray(rule && rule.fallback_models)) return rule.fallback_models;
  if (Array.isArray(rule && rule.targets)) return rule.targets;
  return [];
}

export function normalizeFailoverRules(payload) {
  if (!Array.isArray(payload)) return [];
  return payload.map((rule) => ({
    ...rule,
    source: failoverPrimaryModel(rule),
    targets: failoverTargets(rule),
  }));
}

export function failoverTargetLabel(rule) {
  const targets = failoverTargets(rule);
  if (targets.length === 0) return "-";
  return targets.join(", ");
}

export function failoverRuleStatus(rule) {
  if (rule && rule.enabled === false) return "Off";
  if (rule && rule.managed) return "Config";
  return "On";
}

export function findFailoverMapping(rules, source) {
  const primary = String(source || "").trim();
  if (!primary) return null;
  return (
    (Array.isArray(rules) ? rules : []).find(
      (rule) => failoverPrimaryModel(rule) === primary,
    ) || null
  );
}

export function hasActiveFailoverMapping(rules, row) {
  if (!row || row.is_alias) return false;
  const mapping = findFailoverMapping(rules, qualifiedModelName(row));
  return Boolean(
    mapping &&
      mapping.enabled !== false &&
      failoverTargets(mapping).length > 0,
  );
}

export function failoverButtonClass(rules, row) {
  return hasActiveFailoverMapping(rules, row)
    ? "table-action-btn-failover-active"
    : "";
}

export function failoverButtonLabel(rules, row) {
  const label = row && row.display_name ? row.display_name : "model";
  const base = "Edit failover for " + label;
  return hasActiveFailoverMapping(rules, row) ? base + " (active)" : base;
}

// --- Editor form helpers -------------------------------------------------

// failoverFormTargets flattens the primary input + extra rows into the
// trimmed, non-empty fallback list.
export function failoverFormTargets(form) {
  const values = [form && form.target_model];
  const rows = Array.isArray(form && form.targets) ? form.targets : [];
  rows.forEach((target) => values.push(target && target.model));
  return values.map((value) => String(value || "").trim()).filter(Boolean);
}

// splitFailoverTargets is the inverse: a flat target list into the form's
// {target_model, targets} shape.
export function splitFailoverTargets(targets) {
  const values = Array.isArray(targets)
    ? targets.map((value) => String(value || "").trim()).filter(Boolean)
    : [];
  return {
    target_model: values[0] || "",
    targets: values.slice(1).map((model) => ({ model })),
  };
}

export function failoverRulePayload(form) {
  return {
    primary_model: String((form && form.source) || "").trim(),
    fallback_models: failoverFormTargets(form),
    enabled: !(form && form.enabled === false),
  };
}

// --- Draft (generated rules) helpers -------------------------------------

export function failoverDraftKey(rule) {
  return failoverPrimaryModel(rule);
}

export function buildDraftSelections(rules) {
  const selections = {};
  (Array.isArray(rules) ? rules : []).forEach((rule) => {
    const key = failoverDraftKey(rule);
    if (key) selections[key] = true;
  });
  return selections;
}

export function failoverDraftSelected(selections, rule) {
  const key = failoverDraftKey(rule);
  return Boolean(key && selections && selections[key]);
}

export function selectedFailoverDrafts(rules, selections) {
  return (Array.isArray(rules) ? rules : []).filter((rule) =>
    failoverDraftSelected(selections, rule),
  );
}

export function allFailoverDraftsSelected(rules, selections) {
  const list = Array.isArray(rules) ? rules : [];
  return (
    list.length > 0 && selectedFailoverDrafts(list, selections).length === list.length
  );
}

export function failoverDraftSearchText(rule) {
  return [failoverPrimaryModel(rule), failoverTargets(rule).join(" ")]
    .join(" ")
    .toLowerCase();
}

export function filterFailoverDrafts(rules, filter) {
  const list = Array.isArray(rules) ? rules : [];
  const query = String(filter || "")
    .trim()
    .toLowerCase();
  if (!query) return list;
  return list.filter((rule) => failoverDraftSearchText(rule).includes(query));
}

export function failoverDraftPayload(rule) {
  return {
    primary_model: failoverPrimaryModel(rule),
    fallback_models: failoverTargets(rule)
      .map((model) => String(model || "").trim())
      .filter(Boolean),
    enabled: Boolean(rule && rule.enabled !== false),
  };
}
