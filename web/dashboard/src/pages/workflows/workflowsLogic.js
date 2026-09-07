// Pure workflow form/scope/payload logic.
// No Svelte runes here so node:test can import it directly.
//
// Feature caps ({cache, audit, usage, budget, guardrails, failover}) are the
// runtime-config visibility gates; callers derive them from the shared
// runtimeConfig store (see workflows.svelte.js featureCaps()).

import * as m from "../../lib/paraglide/messages.js";
import {
  DEFAULT_PHASE,
  WORKFLOW_PHASES,
  isWorkflowPhase,
  normalizeWorkflowPhase,
  phasesSupport,
} from "../../lib/utils/pluginPhases.js";

export const DRAFT_WORKFLOW_PREVIEW_ID = "draft-workflow-preview";

// WORKFLOW_SCHEMA_VERSION is what the dashboard POSTs: steps carry a phase.
// Stored version-1 payloads (`guardrails: [{ref, step}]`) are read as
// prompt-phase steps.
export const WORKFLOW_SCHEMA_VERSION = 2;

export function defaultWorkflowForm() {
  return {
    scope_provider: "",
    scope_model: "",
    scope_user_path: "",
    name: "",
    description: "",
    features: {
      cache: true,
      audit: true,
      usage: true,
      budget: true,
      guardrails: false,
      failover: true,
    },
    guardrails: [],
  };
}

export function emptyHydratedScope() {
  return {
    scope_provider: "",
    scope_model: "",
    scope_user_path: "",
  };
}

export function defaultWorkflowGuardrailStep(step, phase) {
  return {
    ref: "",
    phase: normalizeWorkflowPhase(phase),
    step: Number.isFinite(step) ? step : 10,
  };
}

// workflowPayloadSteps reads a payload's steps in the v2 shape
// ({ref, phase, step}). Legacy `guardrails` entries (stored v1 versions, and
// the editor form, which keeps its rows under the same key) are read too; a
// step without a phase is a prompt step. Raw step numbers are passed through
// so callers decide how strictly to validate.
export function workflowPayloadSteps(payload) {
  if (!payload || typeof payload !== "object") {
    return [];
  }
  const raw = Array.isArray(payload.steps)
    ? payload.steps
    : Array.isArray(payload.guardrails)
      ? payload.guardrails
      : [];
  return raw.map((step) => ({
    ref: String((step && step.ref) || "").trim(),
    phase: normalizeWorkflowPhase(step && step.phase),
    step: step && step.step,
  }));
}

// workflowStepGroups groups steps by phase in execution order (prompt,
// response, stream), dropping empty groups. Order within a group is kept.
export function workflowStepGroups(steps) {
  const list = Array.isArray(steps) ? steps : [];
  return WORKFLOW_PHASES.map((phase) => ({
    phase,
    steps: list.filter((step) => normalizeWorkflowPhase(step && step.phase) === phase),
  })).filter((group) => group.steps.length > 0);
}

// workflowGuardrailRefOptions lists the instances a step may reference for a
// phase. GET /admin/workflows/guardrails returns either names (older
// gateway) or {name, type, phases, summary} rows; an entry without `phases`
// is prompt-only. The current ref stays selectable even when it no longer
// qualifies (cloned workflow, removed instance) so the select never blanks.
export function workflowGuardrailRefOptions(refs, phase, current) {
  const wanted = normalizeWorkflowPhase(phase);
  const options = [];
  const seen = new Set();
  for (const entry of Array.isArray(refs) ? refs : []) {
    const name =
      typeof entry === "string"
        ? entry.trim()
        : String((entry && entry.name) || "").trim();
    if (!name || seen.has(name)) continue;
    const phases = typeof entry === "string" ? null : entry && entry.phases;
    if (!phasesSupport(phases, wanted)) continue;
    seen.add(name);
    options.push({
      value: name,
      label: name,
      summary: typeof entry === "string" ? "" : String((entry && entry.summary) || ""),
    });
  }
  const active = String(current || "").trim();
  if (active && !seen.has(active)) {
    options.push({ value: active, label: active, summary: "" });
  }
  return options;
}

export function parseWorkflowGuardrailStep(rawStep) {
  const trimmedStep =
    rawStep === null || rawStep === undefined ? "" : String(rawStep).trim();
  if (trimmedStep === "") {
    return Number.NaN;
  }
  const parsedStep = Number(trimmedStep);
  return Number.isFinite(parsedStep) ? parsedStep : Number.NaN;
}

function workflowReadFeatureFlag(raw, key, defaultValue) {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return defaultValue;
  }
  const capitalizedKey = key.charAt(0).toUpperCase() + key.slice(1);
  for (const candidate of [key, capitalizedKey]) {
    if (
      Object.prototype.hasOwnProperty.call(raw, candidate) &&
      raw[candidate] !== null &&
      raw[candidate] !== undefined
    ) {
      return raw[candidate];
    }
  }
  return defaultValue;
}

function workflowHasDefinedFeatureFlag(raw, key) {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return false;
  }
  const capitalizedKey = key.charAt(0).toUpperCase() + key.slice(1);
  return [key, capitalizedKey].some((candidate) => {
    return (
      Object.prototype.hasOwnProperty.call(raw, candidate) &&
      raw[candidate] !== null &&
      raw[candidate] !== undefined
    );
  });
}

export function workflowNormalizedFeatures(raw) {
  return {
    cache: !!workflowReadFeatureFlag(raw, "cache", false),
    audit: !!workflowReadFeatureFlag(raw, "audit", false),
    usage: !!workflowReadFeatureFlag(raw, "usage", false),
    budget: workflowReadFeatureFlag(raw, "budget", true) !== false,
    guardrails: !!workflowReadFeatureFlag(raw, "guardrails", false),
    failover: workflowReadFeatureFlag(raw, "failover", true) !== false,
  };
}

export function workflowApplyGlobalFeatureCaps(raw, caps) {
  const features = workflowNormalizedFeatures(raw);
  const limits = caps || {};
  const usage = features.usage && !!limits.usage;
  return {
    cache: features.cache && !!limits.cache,
    audit: features.audit && !!limits.audit,
    usage,
    budget: usage && features.budget && !!limits.budget,
    guardrails: features.guardrails && !!limits.guardrails,
    failover: features.failover && !!limits.failover,
  };
}

// workflowSourceFeatures resolves the effective feature set for a stored
// workflow (or draft preview): server-provided effective_features win over the
// raw payload, caps mask everything except failover, whose raw state is kept
// so the toggle round-trips even while the global control is hidden.
export function workflowSourceFeatures(source, caps) {
  const raw =
    source && source.workflow_payload && source.workflow_payload.features
      ? source.workflow_payload.features
      : source && source.features
        ? source.features
        : {};
  const effective =
    source &&
    source.effective_features &&
    typeof source.effective_features === "object" &&
    !Array.isArray(source.effective_features)
      ? source.effective_features
      : null;
  const features = workflowApplyGlobalFeatureCaps(effective || raw, caps);
  return {
    ...features,
    failover: workflowNormalizedFeatures(raw).failover,
  };
}

export function workflowFailoverLabel(source, caps) {
  return workflowSourceFeatures(source, caps).failover ? m.workflows_on() : m.workflows_off();
}

// workflowSourceGuardrails reads a stored workflow's (or the editor form's)
// steps as {ref, phase, step}, accepting the v2 `steps` list and the legacy
// `guardrails` list, and drops steps without a usable step number.
export function workflowSourceGuardrails(source) {
  const payload =
    source && source.workflow_payload && typeof source.workflow_payload === "object"
      ? source.workflow_payload
      : source;
  return workflowPayloadSteps(payload)
    .map((step) => ({
      ref: step.ref,
      phase: step.phase,
      step: parseWorkflowGuardrailStep(step.step),
    }))
    .filter((step) => Number.isInteger(step.step) && step.step >= 0);
}

export function workflowGuardrails(workflow, caps) {
  if (!workflowSourceFeatures(workflow, caps).guardrails) {
    return [];
  }
  return workflowPayloadSteps(workflow && workflow.workflow_payload);
}

export function workflowScopeProviderValue(scope) {
  return String(
    (scope && (scope.scope_provider_name || scope.scope_provider)) || "",
  ).trim();
}

function workflowModelProviderValue(model) {
  return String(
    (model && (model.provider_name || model.provider_type)) || "",
  ).trim();
}

export function workflowProviderOptions(models, hydratedScope) {
  const options = new Set();
  const preservedProvider = String(
    (hydratedScope && hydratedScope.scope_provider) || "",
  ).trim();
  if (preservedProvider) {
    options.add(preservedProvider);
  }
  const list = Array.isArray(models) ? models : [];
  list.forEach((model) => {
    const providerName = workflowModelProviderValue(model);
    if (providerName) {
      options.add(providerName);
    }
  });
  return [...options].sort();
}

export function workflowModelOptions(models, providerName, hydratedScope) {
  const wantedProvider = String(providerName || "").trim();
  const options = new Set();
  const preservedProvider = String(
    (hydratedScope && hydratedScope.scope_provider) || "",
  ).trim();
  const preservedModel = String(
    (hydratedScope && hydratedScope.scope_model) || "",
  ).trim();
  if (wantedProvider && wantedProvider === preservedProvider && preservedModel) {
    options.add(preservedModel);
  }
  const list = Array.isArray(models) ? models : [];
  list.forEach((model) => {
    if (wantedProvider && workflowModelProviderValue(model) !== wantedProvider) {
      return;
    }
    const modelID = String((model && model.model && model.model.id) || "").trim();
    if (modelID) {
      options.add(modelID);
    }
  });
  return [...options].sort();
}

export function workflowScopeTypeLabel(workflow) {
  const scopeType = String((workflow && workflow.scope_type) || "").trim();
  if (scopeType === "provider_model") return m.workflows_scope_provider_model();
  if (scopeType === "provider_model_path") return m.workflows_scope_provider_model_path();
  if (scopeType === "provider_path") return m.workflows_scope_provider_path();
  if (scopeType === "path") return m.workflows_scope_path();
  if (scopeType === "provider") return m.workflows_scope_provider();
  return m.workflows_scope_global();
}

export function workflowScopeLabel(workflow) {
  return String((workflow && workflow.scope_display) || "global").trim() || "global";
}

export function workflowDisplayName(workflow) {
  const explicitName = String((workflow && workflow.name) || "").trim();
  if (explicitName) {
    return explicitName;
  }
  const scopeLabel = workflowScopeLabel(workflow);
  if (scopeLabel === "global") {
    return m.workflows_all_models();
  }
  return scopeLabel;
}

function workflowScopeUserPathValidationError(value) {
  const trimmed = String(value || "").trim();
  if (!trimmed) {
    return "";
  }
  const raw = trimmed.startsWith("/") ? trimmed : "/" + trimmed;
  const segments = raw.split("/");
  for (const part of segments) {
    const segment = String(part || "").trim();
    if (!segment) {
      continue;
    }
    if (segment === "." || segment === "..") {
      return m.api_keys_user_path_segments();
    }
    if (segment.includes(":")) {
      return m.api_keys_user_path_colon();
    }
  }
  return "";
}

export function normalizeWorkflowScopeUserPath(value) {
  if (workflowScopeUserPathValidationError(value)) {
    return "";
  }
  const trimmed = String(value || "").trim();
  if (!trimmed) {
    return "";
  }
  const raw = trimmed.startsWith("/") ? trimmed : "/" + trimmed;
  const segments = raw.split("/");
  const canonical = [];
  for (const part of segments) {
    const segment = String(part || "").trim();
    if (!segment) {
      continue;
    }
    canonical.push(segment);
  }
  if (!canonical.length) {
    return "/";
  }
  return "/" + canonical.join("/");
}

function workflowCurrentScope(form) {
  const currentForm = form || defaultWorkflowForm();
  const provider = String(currentForm.scope_provider || "").trim();
  const userPath = normalizeWorkflowScopeUserPath(currentForm.scope_user_path);
  return {
    scope_provider: provider,
    scope_model: provider ? String(currentForm.scope_model || "").trim() : "",
    scope_user_path: userPath,
  };
}

function workflowScopeType(scope) {
  const provider = String((scope && scope.scope_provider) || "").trim();
  const model = provider ? String((scope && scope.scope_model) || "").trim() : "";
  const userPath = normalizeWorkflowScopeUserPath(scope && scope.scope_user_path);
  if (!provider && !userPath) return "global";
  if (!provider && userPath) return "path";
  if (!model && !userPath) return "provider";
  if (!model && userPath) return "provider_path";
  if (userPath) return "provider_model_path";
  return "provider_model";
}

export function workflowScopeDisplay(scope) {
  const provider = String((scope && scope.scope_provider) || "").trim();
  const model = provider ? String((scope && scope.scope_model) || "").trim() : "";
  const userPath = normalizeWorkflowScopeUserPath(scope && scope.scope_user_path);
  const scopeType = workflowScopeType({
    scope_provider: provider,
    scope_model: model,
    scope_user_path: userPath,
  });
  if (scopeType === "global") return "global";
  if (scopeType === "path") return userPath;
  if (scopeType === "provider") return provider;
  if (scopeType === "provider_path") return provider + " @ " + userPath;
  if (scopeType === "provider_model_path") {
    return provider + "/" + model + " @ " + userPath;
  }
  return provider + "/" + model;
}

function workflowScopeMatches(workflow, scope) {
  const normalized = scope || emptyHydratedScope();
  const provider = workflowScopeProviderValue(workflow && workflow.scope);
  const model = provider
    ? String((workflow && workflow.scope && workflow.scope.scope_model) || "").trim()
    : "";
  const userPath = normalizeWorkflowScopeUserPath(
    workflow && workflow.scope && workflow.scope.scope_user_path,
  );
  return (
    provider === String(normalized.scope_provider || "").trim() &&
    model === String(normalized.scope_model || "").trim() &&
    userPath === normalizeWorkflowScopeUserPath(normalized.scope_user_path)
  );
}

export function workflowActiveScopeMatch(workflows, form, formHydrated) {
  const scope = workflowCurrentScope(form);
  const hasScopedSelection =
    scope.scope_provider !== "" ||
    scope.scope_model !== "" ||
    scope.scope_user_path !== "";
  if (!hasScopedSelection && !formHydrated) {
    return null;
  }
  const list = Array.isArray(workflows) ? workflows : [];
  return list.find((workflow) => workflowScopeMatches(workflow, scope)) || null;
}

export function canDeactivateWorkflow(workflow) {
  return String((workflow && workflow.scope_type) || "").trim() !== "global";
}

export function shortHash(value) {
  const hash = String(value || "").trim();
  if (!hash) return "—";
  if (hash.length <= 14) return hash;
  return hash.slice(0, 12) + "…";
}

export function filterWorkflows(workflows, filter) {
  const list = Array.isArray(workflows) ? workflows : [];
  if (!filter) {
    return list;
  }
  const wanted = String(filter).toLowerCase();
  return list.filter((workflow) => {
    const fields = [
      workflow.name,
      workflow.description,
      workflow.scope_display,
      workflow.scope_type,
      workflowScopeProviderValue(workflow && workflow.scope),
      workflow.scope && workflow.scope.scope_model,
      workflow.scope && workflow.scope.scope_user_path,
      workflow.workflow_hash,
      ...workflowPayloadSteps(workflow.workflow_payload).map((step) => step.ref),
    ];
    return fields.some((value) => String(value || "").toLowerCase().includes(wanted));
  });
}

// workflowPreview builds the draft workflow card mirrored live in the editor.
export function workflowPreview(form, caps) {
  const currentForm = form || defaultWorkflowForm();
  const scope = workflowCurrentScope(currentForm);
  const rawFeatures = workflowNormalizedFeatures(currentForm.features || {});
  const features = workflowApplyGlobalFeatureCaps(rawFeatures, caps);
  features.failover = rawFeatures.failover;
  const guardrailsEnabled = !!features.guardrails;
  const guardrails = guardrailsEnabled ? workflowSourceGuardrails(currentForm) : [];
  const scopeType = workflowScopeType(scope);
  const scopeDisplay = workflowScopeDisplay(scope);

  return {
    id: DRAFT_WORKFLOW_PREVIEW_ID,
    scope_type: scopeType,
    scope_display: scopeDisplay,
    scope: {
      scope_provider_name: scope.scope_provider,
      scope_model: scope.scope_model,
      ...(scope.scope_user_path ? { scope_user_path: scope.scope_user_path } : {}),
    },
    name: String(currentForm.name || "").trim(),
    description: String(currentForm.description || "").trim(),
    workflow_payload: {
      schema_version: WORKFLOW_SCHEMA_VERSION,
      features: {
        cache: !!features.cache,
        audit: !!features.audit,
        usage: !!features.usage,
        budget: !!features.budget,
        guardrails: guardrailsEnabled,
        failover: !!features.failover,
      },
      steps: guardrails,
    },
  };
}

// buildWorkflowRequest builds the POST /admin/workflows payload. The failover
// flag is only included when the control is visible, or when a hidden failover
// state must be preserved: an edited (hydrated) workflow staying on its scope
// keeps the form state; a fresh form matching an active workflow keeps the
// active workflow's stored flag; a retargeted scope omits it entirely.
export function buildWorkflowRequest({
  form,
  caps,
  workflows = [],
  formHydrated = false,
  hydratedScope = null,
}) {
  const currentForm = form || defaultWorkflowForm();
  const provider = String(currentForm.scope_provider || "").trim();
  const model = provider ? String(currentForm.scope_model || "").trim() : "";
  const userPath = normalizeWorkflowScopeUserPath(currentForm.scope_user_path);
  const rawFeatures = workflowNormalizedFeatures(currentForm.features || {});
  const features = workflowApplyGlobalFeatureCaps(rawFeatures, caps);
  const activeScopeMatch = workflowActiveScopeMatch(workflows, currentForm, formHydrated);
  const activeScopeFeatures =
    activeScopeMatch &&
    activeScopeMatch.workflow_payload &&
    activeScopeMatch.workflow_payload.features;
  const activeScopeHasFailover = workflowHasDefinedFeatureFlag(
    activeScopeFeatures,
    "failover",
  );
  const preservedActiveFailover = activeScopeHasFailover
    ? workflowReadFeatureFlag(activeScopeFeatures, "failover", true) !== false
    : null;
  const hydrated = hydratedScope || emptyHydratedScope();
  const sameHydratedScope =
    String(hydrated.scope_provider || "").trim() === provider &&
    String(hydrated.scope_model || "").trim() === model &&
    normalizeWorkflowScopeUserPath(hydrated.scope_user_path) ===
      normalizeWorkflowScopeUserPath(userPath);
  const failoverVisible = !!(caps && caps.failover);
  const includeFailover =
    failoverVisible ||
    (!!formHydrated &&
      sameHydratedScope &&
      Object.prototype.hasOwnProperty.call(rawFeatures, "failover")) ||
    (!formHydrated && !!activeScopeMatch && activeScopeHasFailover);

  const steps = features.guardrails
    ? (Array.isArray(currentForm.guardrails) ? currentForm.guardrails : []).map(
        (step) => ({
          ref: String((step && step.ref) || "").trim(),
          phase: normalizeWorkflowPhase(step && step.phase),
          step: parseWorkflowGuardrailStep(step && step.step),
        }),
      )
    : [];

  const payload = {
    scope_provider_name: provider,
    scope_model: model,
    ...(userPath ? { scope_user_path: userPath } : {}),
    name: String(currentForm.name || "").trim(),
    description: String(currentForm.description || "").trim(),
    workflow_payload: {
      schema_version: WORKFLOW_SCHEMA_VERSION,
      features: {
        cache: !!features.cache,
        audit: !!features.audit,
        usage: !!features.usage,
        budget: !!features.budget,
        guardrails: !!features.guardrails,
      },
      steps,
    },
  };
  if (includeFailover) {
    payload.workflow_payload.features.failover =
      !failoverVisible && !formHydrated && !!activeScopeMatch && activeScopeHasFailover
        ? preservedActiveFailover
        : !!rawFeatures.failover;
  }

  return payload;
}

export function validateWorkflowRequest(payload, { models = [], hydratedScope = null } = {}) {
  const hydrated = hydratedScope || emptyHydratedScope();
  const preservedProvider = String(hydrated.scope_provider || "").trim();
  const preservedModel = String(hydrated.scope_model || "").trim();
  const providerName = String(
    (payload && (payload.scope_provider_name || payload.scope_provider)) || "",
  ).trim();
  const scopeModel = String((payload && payload.scope_model) || "").trim();

  if (providerName) {
    const providers = workflowProviderOptions(models, hydrated);
    if (!providers.includes(providerName) && providerName !== preservedProvider) {
      return m.workflows_provider_invalid();
    }
  }
  if (scopeModel && !providerName) {
    return m.workflows_model_requires_provider();
  }
  if (scopeModel) {
    const modelOptions = workflowModelOptions(models, providerName, hydrated);
    const isPreservedModel =
      providerName === preservedProvider && scopeModel === preservedModel;
    if (!modelOptions.includes(scopeModel) && !isPreservedModel) {
      return m.workflows_model_invalid();
    }
  }
  const userPathError = workflowScopeUserPathValidationError(payload.scope_user_path);
  if (userPathError) {
    return userPathError;
  }
  const features =
    payload.workflow_payload && payload.workflow_payload.features
      ? payload.workflow_payload.features
      : {};
  const steps = Array.isArray(payload.workflow_payload && payload.workflow_payload.steps)
    ? payload.workflow_payload.steps
    : Array.isArray(payload.workflow_payload && payload.workflow_payload.guardrails)
      ? payload.workflow_payload.guardrails
      : [];
  if (!features.guardrails) {
    return "";
  }

  // The same ref may run once per phase (e.g. a scanner on both the prompt
  // and the response), never twice within one phase.
  const seen = new Set();
  for (const step of steps) {
    if (!step.ref) {
      return m.workflows_guardrail_ref_required();
    }
    const phase = step.phase === undefined || step.phase === null ? DEFAULT_PHASE : step.phase;
    if (!isWorkflowPhase(phase)) {
      return m.workflows_guardrail_phase_invalid();
    }
    if (!Number.isInteger(step.step) || step.step < 0) {
      return m.workflows_guardrail_step_invalid();
    }
    const key = normalizeWorkflowPhase(phase) + ":" + step.ref;
    if (seen.has(key)) {
      return m.workflows_guardrail_ref_unique();
    }
    seen.add(key);
  }

  return "";
}
