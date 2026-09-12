// Pure workflow pipeline-chart contract. Builds the plain object rendered by
// WorkflowChart.svelte. Runtime data (audit entries, live-log state) is
// supported so one chart contract serves both configuration previews and
// recorded runs.

import {
  DRAFT_WORKFLOW_PREVIEW_ID,
  workflowNormalizedFeatures,
  workflowSourceFeatures,
  workflowSourceGuardrails,
  workflowScopeProviderValue,
} from "./workflowsLogic.js";
import * as m from "../../lib/paraglide/messages.js";
import {
  WORKFLOW_PHASES,
  normalizeWorkflowPhase,
  phaseLabel,
} from "../../lib/utils/pluginPhases.js";

function workflowPhaseSteps(source, phase) {
  const wanted = normalizeWorkflowPhase(phase);
  return workflowSourceGuardrails(source).filter((step) => step.phase === wanted);
}

// workflowPhaseStepCount counts the configured steps of one phase, the rows
// of the editor's step list; refs sharing a step number each count.
function workflowPhaseStepCount(source, phase) {
  return workflowPhaseSteps(source, phase).length;
}

// workflowMutatingRefs names the guardrail instances that may edit the
// request or response, from GET /admin/workflows/guardrails rows.
function workflowMutatingRefs(guardrailRefs) {
  const names = new Set();
  for (const entry of Array.isArray(guardrailRefs) ? guardrailRefs : []) {
    if (!entry || typeof entry !== "object" || !entry.mutates) continue;
    const name = String(entry.name || "").trim();
    if (name) names.add(name);
  }
  return names;
}

// workflowGuardrailFlow is the execution order of one phase's guardrails:
// steps ascending, each holding the refs that share that step number, which
// the gateway runs together. `mutator` names the step's mutating instance
// (at most one per step; the gateway applies its edit after the step's
// checks) when instance rows are given; the audit view has none. Refs keep
// their configured order; a step whose ref is not chosen yet (editor draft)
// stays in as "" so the flow matches the node's step count.
export function workflowGuardrailFlow(source, phase = "prompt", guardrailRefs = []) {
  const mutating = workflowMutatingRefs(guardrailRefs);
  const byStep = new Map();
  for (const item of workflowPhaseSteps(source, phase)) {
    const ref = String(item.ref || "").trim();
    if (!byStep.has(item.step)) byStep.set(item.step, { step: item.step, refs: [], mutator: null });
    const stage = byStep.get(item.step);
    stage.refs.push(ref);
    if (ref && mutating.has(ref) && !stage.mutator) stage.mutator = ref;
  }
  return Array.from(byStep.keys())
    .sort((a, b) => a - b)
    .map((step) => byStep.get(step));
}

// workflowRuntimeGuardrailFlow is the recorded run of one phase: the
// configured flow plus, per stage, `outcomes` keyed by ref — what each
// instance decided, or a "skipped" pseudo-outcome for a configured ref that
// has no outcome in a finished entry (an earlier block, a cache hit).
// Outcomes whose instance is not configured (the workflow version could not
// be resolved) get a stage of their own so nothing recorded is hidden.
function workflowRuntimeGuardrailFlow(source, phase, guardrailRefs, outcomes, finished) {
  const flow = workflowGuardrailFlow(source, phase, guardrailRefs);
  const seen = new Set();
  for (const stage of flow) {
    stage.outcomes = {};
    for (const ref of stage.refs) {
      if (!ref) continue;
      const outcome = outcomes.find((item) => item.instance === ref);
      if (outcome) {
        stage.outcomes[ref] = workflowFlowOutcome(outcome);
        seen.add(ref);
      } else if (finished) {
        stage.outcomes[ref] = workflowFlowOutcome(null);
      }
    }
  }
  for (const outcome of outcomes) {
    if (seen.has(outcome.instance)) continue;
    seen.add(outcome.instance);
    // Keyed by instance in the chart: its step may collide with a
    // configured stage's.
    flow.push({
      step: outcome.step === null ? outcome.seq : outcome.step,
      id: "outcome-" + outcome.instance,
      refs: [outcome.instance],
      mutator: null,
      outcomes: { [outcome.instance]: workflowFlowOutcome(outcome) },
    });
  }
  return flow;
}

function workflowGuardrailFlows(source, phases, guardrailRefs, runtime) {
  const flows = {};
  const finished = !!(runtime && Number.isFinite(runtime.statusCode));
  for (const phase of WORKFLOW_PHASES) {
    if (!phases[phase]) {
      flows[phase] = [];
    } else if (runtime) {
      flows[phase] = workflowRuntimeGuardrailFlow(
        source,
        phase,
        guardrailRefs,
        workflowPhaseOutcomes(runtime, phase),
        finished,
      );
    } else {
      flows[phase] = workflowGuardrailFlow(source, phase, guardrailRefs);
    }
  }
  return flows;
}

// ─── Guardrail outcomes ───

const GUARDRAIL_ACTIONS = new Set(["allow", "warn", "block", "respond", "failure"]);

function plainObject(value) {
  return !!value && typeof value === "object" && !Array.isArray(value);
}

function nonNegativeInt(value) {
  if (value === null || value === undefined || value === "") return null;
  const number = Number(value);
  return Number.isInteger(number) && number >= 0 ? number : null;
}

// guardrailOutcome normalizes one outcome into the shape the chart and the
// audit page share. `edited` implies a target: the phase's own subject
// unless the record names one.
function guardrailOutcome(fields) {
  const action = String(fields.action || "").trim().toLowerCase();
  const phase = normalizeWorkflowPhase(fields.phase);
  const failMode = String(fields.failMode || "").trim().toLowerCase();
  const edited = !!fields.edited;
  const target = String(fields.target || "").trim().toLowerCase();
  const duration = Number(fields.durationNs);
  return {
    seq: fields.seq,
    phase,
    step: nonNegativeInt(fields.step),
    instance: fields.instance,
    type: String(fields.type || "").trim(),
    action,
    code: String(fields.code || "").trim(),
    message: String(fields.message || "").trim(),
    detail: fields.detail === undefined ? null : fields.detail,
    error: String(fields.error || "").trim(),
    failMode:
      action === "failure" && (failMode === "open" || failMode === "closed") ? failMode : "",
    edited,
    target: edited
      ? target === "request" || target === "response"
        ? target
        : phase === "prompt"
          ? "request"
          : "response"
      : "",
    replacedEvents: nonNegativeInt(fields.replacedEvents),
    droppedEvents: nonNegativeInt(fields.droppedEvents),
    durationNs: Number.isFinite(duration) && duration > 0 ? duration : null,
  };
}

// workflowLegacyGuardrails rebuilds outcomes for entries written before
// data.guardrails existed: the prompt phase recorded its decisions as
// request revisions whose detail is {phase, action, code, message, error}
// under the instance's name, and a chain-level revision whose detail lists
// the instances that edited the request.
function workflowLegacyGuardrails(revisions) {
  if (!Array.isArray(revisions)) return [];
  const outcomes = [];
  const byInstance = new Map();
  const push = (fields) => {
    const outcome = guardrailOutcome({ ...fields, seq: outcomes.length + 1 });
    outcomes.push(outcome);
    byInstance.set(outcome.instance, outcome);
  };
  for (const revision of revisions) {
    if (!plainObject(revision) || !plainObject(revision.detail)) continue;
    const detail = revision.detail;
    const action = String(detail.action || "").trim().toLowerCase();
    if (GUARDRAIL_ACTIONS.has(action)) {
      const instance = String(revision.rewriter || "").trim();
      if (!instance) continue;
      const error = String(detail.error || "").trim();
      push({
        phase: detail.phase,
        instance,
        action: error && action === "allow" ? "failure" : action,
        code: detail.code,
        message: detail.message,
        detail: detail.detail,
        error,
        edited: !revision.no_change,
        target: "request",
      });
      continue;
    }
    if (!Array.isArray(detail.edited)) continue;
    for (const name of detail.edited) {
      const instance = String(name || "").trim();
      if (!instance) continue;
      const known = byInstance.get(instance);
      if (known) {
        known.edited = true;
        known.target = "request";
        continue;
      }
      push({ phase: detail.phase, instance, action: "allow", edited: true, target: "request" });
    }
  }
  return outcomes;
}

// workflowEntryGuardrails returns the outcome of every guardrail instance
// that ran for an audit entry, in execution order, normalized to
// { seq, phase, step, instance, type, action, code, message, detail, error,
//   failMode, edited, target, replacedEvents, droppedEvents, durationNs }.
// Entries without data.guardrails fall back to the legacy revision trail.
export function workflowEntryGuardrails(entry) {
  const data = workflowEntryData(entry);
  if (Array.isArray(data.guardrails)) {
    const outcomes = [];
    data.guardrails.forEach((raw, index) => {
      if (!plainObject(raw)) return;
      const instance = String(raw.instance || "").trim();
      const action = String(raw.action || "").trim().toLowerCase();
      if (!instance || !GUARDRAIL_ACTIONS.has(action)) return;
      const seq = Number(raw.seq);
      outcomes.push(
        guardrailOutcome({
          seq: Number.isFinite(seq) && seq > 0 ? seq : index + 1,
          phase: raw.phase,
          step: raw.step,
          instance,
          type: raw.type,
          action,
          code: raw.code,
          message: raw.message,
          detail: raw.detail,
          error: raw.error,
          failMode: raw.fail_mode,
          edited: raw.edited,
          target: raw.target,
          replacedEvents: raw.replaced_events,
          droppedEvents: raw.dropped_events,
          durationNs: raw.duration_ns,
        }),
      );
    });
    if (outcomes.length > 0) return outcomes.sort((a, b) => a.seq - b.seq);
  }
  return workflowLegacyGuardrails(data.request_revisions);
}

// workflowGuardrailSeverity ranks an outcome for the phase rollup: 2 stops
// the request (block, respond, fail-closed), 1 is a warning (warn, fail-open
// or a failure of unknown mode), 0 let it through.
function workflowGuardrailSeverity(outcome) {
  if (!outcome) return 0;
  switch (outcome.action) {
    case "block":
    case "respond":
      return 2;
    case "failure":
      return outcome.failMode === "closed" ? 2 : 1;
    case "warn":
      return 1;
    default:
      return 0;
  }
}

function workflowGuardrailStops(outcome) {
  return workflowGuardrailSeverity(outcome) === 2;
}

// workflowGuardrailActionLabel is the plain verdict of one outcome; the
// "skipped" pseudo-action names a configured instance that never ran.
export function workflowGuardrailActionLabel(outcome) {
  switch (outcome && outcome.action) {
    case "block":
      return m.workflows_guardrail_status_blocked();
    case "respond":
      return m.workflows_guardrail_status_answered();
    case "failure":
      return m.workflows_guardrail_status_failed();
    case "warn":
      return m.workflows_guardrail_status_warned();
    case "skipped":
      return m.workflows_guardrail_status_skipped();
    default:
      return outcome && outcome.edited
        ? m.workflows_guardrail_status_edited()
        : m.workflows_guardrail_status_passed();
  }
}

// workflowGuardrailOutcomeLabel is the verdict with its code when the
// outcome stopped the request, e.g. "Blocked · content_policy".
export function workflowGuardrailOutcomeLabel(outcome) {
  const label = workflowGuardrailActionLabel(outcome);
  const code = outcome && (outcome.action === "block" || outcome.action === "respond")
    ? outcome.code
    : "";
  return code ? label + " · " + code : label;
}

function workflowGuardrailTone(outcome) {
  if (!outcome) return "skipped";
  switch (workflowGuardrailSeverity(outcome)) {
    case 2:
      return "danger";
    case 1:
      return "warning";
    default:
      return "success";
  }
}

// workflowFlowOutcome is the per-ref entry of a stage's `outcomes`; null
// stands for a configured ref that was skipped.
function workflowFlowOutcome(outcome) {
  if (!outcome) {
    return {
      action: "skipped",
      code: "",
      message: "",
      edited: false,
      error: "",
      failMode: "",
      durationNs: null,
      replacedEvents: null,
      droppedEvents: null,
      tone: "skipped",
      title: m.workflows_guardrail_status_skipped(),
    };
  }
  const title = [workflowGuardrailOutcomeLabel(outcome), outcome.message, outcome.error]
    .filter(Boolean)
    .join("\n");
  return {
    action: outcome.action,
    code: outcome.code,
    message: outcome.message,
    edited: outcome.edited,
    error: outcome.error,
    failMode: outcome.failMode,
    durationNs: outcome.durationNs,
    replacedEvents: outcome.replacedEvents,
    droppedEvents: outcome.droppedEvents,
    tone: workflowGuardrailTone(outcome),
    title,
  };
}

function workflowPhaseOutcomes(runtime, phase) {
  const outcomes = runtime && Array.isArray(runtime.guardrails) ? runtime.guardrails : [];
  return outcomes.filter((outcome) => outcome.phase === phase);
}

// workflowGuardrailPhaseState rolls one phase's outcomes up into the node's
// state class and status label. The worst outcome names the label; a phase
// that is configured but has no outcomes in a finished entry never ran.
function workflowGuardrailPhaseState(outcomes, configured, runtime) {
  if (outcomes.length === 0) {
    if (configured && runtime && Number.isFinite(runtime.statusCode)) {
      return {
        nodeClass: "workflow-node-skipped",
        statusLabel: m.workflows_guardrail_status_skipped(),
      };
    }
    return { nodeClass: "", statusLabel: null };
  }
  let worst = outcomes[0];
  for (const outcome of outcomes) {
    if (workflowGuardrailSeverity(outcome) > workflowGuardrailSeverity(worst)) worst = outcome;
  }
  switch (workflowGuardrailSeverity(worst)) {
    case 2:
      return {
        nodeClass: "workflow-node-error",
        statusLabel: workflowGuardrailOutcomeLabel(worst),
      };
    case 1:
      return {
        nodeClass: "workflow-node-warning",
        statusLabel: workflowGuardrailOutcomeLabel(worst),
      };
    default:
      return {
        nodeClass: "workflow-node-success",
        statusLabel: outcomes.some((outcome) => outcome.edited)
          ? m.workflows_guardrail_status_edited()
          : m.workflows_guardrail_status_passed(),
      };
  }
}

// workflowGuardrailLabel is the step-count sublabel of a guardrail node for
// one phase (prompt by default); "" when that phase has no steps.
export function workflowGuardrailLabel(source, phase = "prompt") {
  const count = workflowPhaseStepCount(source, phase);
  if (count === 0) return "";
  return count === 1
    ? m.workflows_one_step()
    : m.workflows_steps_count({ count });
}

function workflowAiLabel(source, runtime) {
  if (runtime && runtime.provider) return runtime.provider;
  const provider = workflowScopeProviderValue(source && source.scope);
  return provider || "AI";
}

function workflowAiSublabel(source, runtime) {
  if (runtime && runtime.model) return runtime.model;
  return (source && source.scope && source.scope.scope_model) || null;
}

export function workflowChartWorkflowID(source, entry) {
  const sourceID = String((source && source.id) || "").trim();
  if (sourceID && sourceID !== DRAFT_WORKFLOW_PREVIEW_ID) {
    return sourceID;
  }
  const entryID = String((entry && entry.workflow_version_id) || "").trim();
  if (entryID && entryID !== DRAFT_WORKFLOW_PREVIEW_ID) {
    return entryID;
  }
  return null;
}

// ─── Audit-entry runtime extraction ───

function workflowEntryFeatures(entry) {
  const raw = entry && entry.data && entry.data.workflow_features;
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return null;
  }
  return workflowNormalizedFeatures(raw);
}

function workflowEntryFailover(entry) {
  const raw = entry && entry.data && entry.data.failover;
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return null;
  }

  const targetModel = String(raw.target_model || raw.targetModel || "").trim() || null;
  if (!targetModel) {
    return null;
  }

  return {
    targetModel,
  };
}

export function workflowFailoverTarget(entry) {
  const failover = workflowEntryFailover(entry);
  return failover && failover.targetModel ? failover.targetModel : null;
}

function workflowNestedErrorCode(value, depth = 0) {
  if (depth > 4 || value === null || value === undefined) {
    return "";
  }
  if (typeof value === "string") {
    const trimmed = value.trim();
    if (!trimmed || (trimmed[0] !== "{" && trimmed[0] !== "[")) {
      return "";
    }
    try {
      return workflowNestedErrorCode(JSON.parse(trimmed), depth + 1);
    } catch {
      return "";
    }
  }
  if (Array.isArray(value)) {
    for (const item of value) {
      const code = workflowNestedErrorCode(item, depth + 1);
      if (code) return code;
    }
    return "";
  }
  if (typeof value !== "object") {
    return "";
  }
  const code = String(value.code || "").trim();
  if (code) return code;
  if (value.error !== undefined) {
    return workflowNestedErrorCode(value.error, depth + 1);
  }
  return "";
}

function workflowEntryData(entry) {
  return entry && entry.data && typeof entry.data === "object" && !Array.isArray(entry.data)
    ? entry.data
    : {};
}

function workflowEntryErrorCode(entry) {
  const data = workflowEntryData(entry);
  const direct = String(data.error_code || data.errorCode || "").trim();
  if (direct) return direct;
  return workflowNestedErrorCode(data.response_body);
}

// The gateway's Auth step failed only when the authentication error is the
// gateway's own. A provider rejecting its key is also an
// authentication_error, but it names the provider (data.error_provider) and
// the request had already passed the gateway's authentication.
function workflowEntryAuthFailed(entry) {
  const authError =
    String((entry && entry.error_type) || "").trim().toLowerCase() === "authentication_error";
  if (!authError) return false;
  return !String(workflowEntryData(entry).error_provider || "").trim();
}

function workflowQualifiedSelectorParts(selector) {
  const raw = String(selector || "").trim();
  if (!raw) return null;
  const slashIndex = raw.indexOf("/");
  if (slashIndex <= 0 || slashIndex >= raw.length - 1) {
    return null;
  }
  return {
    provider: raw.slice(0, slashIndex),
    model: raw.slice(slashIndex + 1),
  };
}

function workflowPrimaryRouteFromEntry(entry, source) {
  const requestedModel = String(entry.requested_model || entry.model || "").trim();
  const failover = workflowEntryFailover(entry);
  if (!(failover && failover.targetModel)) {
    // The configured instance name tells two instances of one provider type
    // apart; the type is the fallback for entries recorded without a name.
    return {
      provider:
        String(entry.provider_name || entry.provider || "").trim() || null,
      model: requestedModel || null,
    };
  }

  const qualifiedRequested = workflowQualifiedSelectorParts(requestedModel);
  if (qualifiedRequested) {
    return qualifiedRequested;
  }

  const scopeProvider = workflowScopeProviderValue(source && source.scope);
  const scopeModel = scopeProvider
    ? String((source && source.scope && source.scope.scope_model) || "").trim()
    : "";
  if (scopeProvider || scopeModel) {
    return {
      provider: scopeProvider || null,
      model: scopeModel || requestedModel || null,
    };
  }

  return {
    provider: null,
    model: requestedModel || null,
  };
}

// runtime shape: { cacheHit, cacheType, failoverTarget, provider, model,
//   statusCode, responseSuccess, aiSuccess, authError, authMethod,
//   budgetExceeded, guardrails, guardrailBlocked, guardrailAnswered }
// guardrailBlocked: a prompt-phase guardrail stopped the request (block,
// respond or a fail-closed failure), so the model was never called;
// guardrailAnswered narrows that to a guardrail-written response.
export function workflowRuntimeFromEntry(entry, source) {
  if (!entry) return null;
  const normalizedCacheType = (() => {
    const value = String(entry.cache_type || "").trim().toLowerCase();
    if (value === "exact" || value === "semantic") return value;
    return null;
  })();
  const statusCode = (() => {
    if (entry.status_code === undefined || entry.status_code === null) return null;
    const raw = String(entry.status_code).trim();
    if (!raw) return null;
    const value = Number(raw);
    return Number.isFinite(value) ? value : null;
  })();
  const cacheHit = normalizedCacheType
    ? true
    : entry.cache_hit !== undefined && entry.cache_hit !== null
      ? !!entry.cache_hit
      : false;
  const failover = workflowEntryFailover(entry);
  const primaryRoute = workflowPrimaryRouteFromEntry(entry, source);
  const responseSuccess =
    Number.isFinite(statusCode) && statusCode >= 200 && statusCode < 300;
  const authError = workflowEntryAuthFailed(entry);
  const authMethod = String(entry.auth_method || "").trim().toLowerCase() || null;
  const budgetExceeded =
    workflowEntryErrorCode(entry).toLowerCase() === "budget_exceeded";
  const guardrails = workflowEntryGuardrails(entry);
  const promptStop = guardrails.find(
    (outcome) => outcome.phase === "prompt" && workflowGuardrailStops(outcome),
  );
  const guardrailBlocked = !!promptStop;
  // A response or stream guardrail only runs on a model reply, so its
  // outcome proves the call succeeded even when that guardrail then failed
  // the request.
  const modelReplied = guardrails.some((outcome) => outcome.phase !== "prompt");
  return {
    cacheHit,
    cacheType: normalizedCacheType || null,
    failoverTarget: failover && failover.targetModel ? failover.targetModel : null,
    provider: primaryRoute.provider,
    model: primaryRoute.model,
    statusCode,
    responseSuccess,
    aiSuccess: (responseSuccess || modelReplied) && !cacheHit && !guardrailBlocked,
    authError,
    authMethod,
    budgetExceeded,
    guardrails,
    guardrailBlocked,
    guardrailAnswered: guardrailBlocked && promptStop.action === "respond",
  };
}

function workflowRuntimeHasCache(runtime) {
  return !!(runtime && runtime.cacheHit);
}

// The model call is bypassed by a cache hit or by a prompt guardrail that
// stopped the request; the nodes and connectors on the model path dim.
function workflowRuntimeModelSkipped(runtime) {
  return !!(runtime && (runtime.cacheHit || runtime.guardrailBlocked));
}

function workflowRuntimeUsedFailover(runtime) {
  return !!(runtime && runtime.failoverTarget);
}

function workflowRuntimeBudgetExceeded(runtime) {
  return !!(runtime && runtime.budgetExceeded);
}

// ─── Node/connector class helpers ───

export function workflowCacheNodeClass(runtime, current) {
  if (current) return "workflow-node-current";
  return runtime && runtime.cacheHit ? "workflow-node-success" : "";
}

export function workflowCacheConnClass(runtime) {
  return runtime && runtime.cacheHit ? "workflow-conn-hit" : "";
}

export function workflowCacheStatusLabel(runtime) {
  if (!runtime || !runtime.cacheHit) return null;
  if (runtime.cacheType === "semantic") return m.workflows_hit_semantic();
  return m.workflows_hit_exact();
}

export function workflowBudgetNodeClass(visible, runtime, highlightPresent, current) {
  if (!visible) return "";
  if (workflowRuntimeBudgetExceeded(runtime)) return "workflow-node-error";
  if (current) return "workflow-node-current";
  return highlightPresent ? "workflow-node-success" : "";
}

export function workflowBudgetStatusLabel(runtime) {
  return workflowRuntimeBudgetExceeded(runtime) ? m.workflows_exceeded() : null;
}

function workflowFailoverNodeClass(runtime) {
  if (workflowRuntimeModelSkipped(runtime)) return "workflow-node-skipped";
  return runtime && runtime.failoverTarget ? "workflow-node-success" : "";
}

function workflowFailoverConnClass(runtime) {
  if (workflowRuntimeModelSkipped(runtime)) return "workflow-conn-dim";
  return runtime && runtime.failoverTarget ? "workflow-conn-hit" : "";
}

function workflowFailoverStatusLabel(runtime) {
  return runtime && runtime.failoverTarget ? m.workflows_redirected() : null;
}

function workflowFailoverTargetLabel(runtime) {
  return runtime && runtime.failoverTarget ? runtime.failoverTarget : null;
}

// A cache hit or a prompt-guardrail stop bypasses the AI call, so both the
// connector into the AI node and the one out to Response are dimmed.
function workflowBypassedConnClass(runtime) {
  return workflowRuntimeModelSkipped(runtime) ? "workflow-conn-dim" : "";
}

function workflowAiNodeClass(runtime, current) {
  if (!runtime) return "";
  if (workflowRuntimeModelSkipped(runtime)) return "workflow-node-skipped";
  if (current) return "workflow-node-current";
  return runtime.aiSuccess ? "workflow-node-success" : "";
}

// The Response node keeps its status-code color; a guardrail-written answer
// is called out with a badge so a 200 is not read as a model reply.
function workflowResponseNodeBadge(runtime) {
  return runtime && runtime.guardrailAnswered ? m.workflows_answered_by_guardrail() : null;
}

export function workflowResponseNodeClass(runtime, current) {
  if (!runtime) return "";
  const statusCode = runtime.statusCode;
  if (!Number.isFinite(statusCode) && current) return "workflow-node-current";
  if (!Number.isFinite(statusCode)) return "";
  if (statusCode >= 500) return "workflow-node-error";
  if (statusCode >= 400) return "workflow-node-warning";
  if (statusCode >= 300) return "workflow-node-neutral";
  if (statusCode >= 200) return "workflow-node-success";
  return "";
}

export function workflowResponseNodeSublabel(runtime) {
  if (!runtime || !Number.isFinite(runtime.statusCode)) return null;
  return String(runtime.statusCode);
}

export function workflowAuthNodeClass(runtime, current) {
  if (!runtime) return "";
  if (runtime.authError) return "workflow-node-error";
  if (current) return "workflow-node-current";
  if (runtime.authMethod === "api_key" || runtime.authMethod === "master_key") {
    return "workflow-node-success";
  }
  return "";
}

export function workflowAuthNodeSublabel(runtime) {
  if (!runtime || !runtime.authMethod) return null;
  return runtime.authMethod;
}

export function workflowAsyncNodeClass(visible, highlightPresent, current) {
  if (!visible) return "";
  if (current) return "workflow-node-current";
  return highlightPresent ? "workflow-node-success" : "";
}

// ─── Live-log entry state ───

function workflowAuditFlushed(entry, fallback) {
  if (!entry || !entry._live) return !!fallback;
  const state = String(entry._live_state || "").trim();
  return !!entry._audit_flushed || state === "audit.flushed" || state === "audit.detail";
}

function workflowUsageFlushed(entry, fallback) {
  if (!entry) return !!fallback;
  const usage = entry.usage || {};
  const hasUsage = Number(usage.entries || 0) > 0;
  if (!entry._live) return hasUsage;
  const state = String(entry._usage_live_state || "").trim();
  if (entry._usage_flushed || state === "usage.flushed") return true;
  if (entry._usage_live_pending) return false;
  return hasUsage && !entry._live_pending;
}

function workflowLiveUsagePending(entry) {
  return !!(entry && entry._live && entry._usage_live_pending && !entry._usage_flushed);
}

function workflowLiveAuditPending(entry, runtime) {
  if (!entry || !entry._live || workflowAuditFlushed(entry, false)) return false;
  const state = String(entry._live_state || "").trim();
  return state === "audit.completed" || !!(runtime && Number.isFinite(runtime.statusCode));
}

function workflowLiveCurrentStep(entry, runtime, features) {
  if (!entry || !entry._live) return "";
  if (workflowLiveUsagePending(entry)) return "usage";
  if (workflowLiveAuditPending(entry, runtime)) return "audit";
  if (workflowAuditFlushed(entry, false) && !entry._live_pending) return "";

  if (runtime && runtime.cacheHit) return "cache";
  if (runtime && (runtime.provider || runtime.model)) return "ai";
  if (features && features.budget && (entry.workflow_version_id || entry.requested_model)) {
    return "budget";
  }
  if (runtime && runtime.authMethod) return "";
  return "auth";
}

// ─── Chart model ───

function workflowChartModel(source, runtime, options, caps) {
  const config = options || {};
  const features =
    config.features && typeof config.features === "object" && !Array.isArray(config.features)
      ? workflowNormalizedFeatures(config.features)
      : workflowSourceFeatures(source, caps);
  const forceAudit = !!config.forceAudit;
  const highlightAsyncPresent = !!config.highlightAsyncPresent;
  const showBudget = !!features.budget || workflowRuntimeBudgetExceeded(runtime);
  // Recorded outcomes show their nodes even when the workflow version
  // cannot be resolved, the way a recorded failover shows its node.
  const promptOutcomes = workflowPhaseOutcomes(runtime, "prompt");
  const responseOutcomes = workflowPhaseOutcomes(runtime, "response");
  const streamOutcomes = workflowPhaseOutcomes(runtime, "stream");
  const showGuardrails =
    !!features.guardrails ||
    promptOutcomes.length + responseOutcomes.length + streamOutcomes.length > 0;
  // Prompt guardrails run before the model call (the existing node);
  // response and stream guardrails run after it, so they get their own nodes
  // between the model/failover and the Response endpoint.
  const promptConfigured = workflowPhaseStepCount(source, "prompt") > 0;
  const responseConfigured = workflowPhaseStepCount(source, "response") > 0;
  const streamConfigured = workflowPhaseStepCount(source, "stream") > 0;
  const showResponseGuardrails =
    showGuardrails && (responseConfigured || responseOutcomes.length > 0);
  const showStreamGuardrails =
    showGuardrails && (streamConfigured || streamOutcomes.length > 0);
  const promptState = workflowGuardrailPhaseState(
    promptOutcomes,
    showGuardrails && promptConfigured,
    runtime,
  );
  const responseState = workflowGuardrailPhaseState(
    responseOutcomes,
    showResponseGuardrails && responseConfigured,
    runtime,
  );
  const streamState = workflowGuardrailPhaseState(
    streamOutcomes,
    showStreamGuardrails && streamConfigured,
    runtime,
  );
  const showUsage = !!features.usage;
  const showAudit = forceAudit || !!features.audit;
  const showAsync = !!config.forceAsync || !!(showUsage || showAudit);
  const showFailover = !!features.failover || workflowRuntimeUsedFailover(runtime);
  const workflowID = workflowChartWorkflowID(source, config.entry);
  const liveStep = workflowLiveCurrentStep(config.entry, runtime, features);
  const usagePending = workflowLiveUsagePending(config.entry);
  const auditPending = workflowLiveAuditPending(config.entry, runtime);
  const auditFlushed = workflowAuditFlushed(config.entry, highlightAsyncPresent);
  const usageFlushed = workflowUsageFlushed(config.entry, highlightAsyncPresent);
  return {
    showBudget,
    budgetNodeClass: workflowBudgetNodeClass(
      showBudget,
      runtime,
      highlightAsyncPresent,
      liveStep === "budget",
    ),
    budgetStatusLabel: workflowBudgetStatusLabel(runtime),
    showGuardrails,
    guardrailLabel: showGuardrails ? workflowGuardrailLabel(source, "prompt") : "",
    // The prompt node only needs a phase badge once other phases are shown.
    guardrailBadge:
      showResponseGuardrails || showStreamGuardrails ? phaseLabel("prompt") : null,
    guardrailNodeClass: showGuardrails ? promptState.nodeClass : "",
    guardrailStatusLabel: showGuardrails ? promptState.statusLabel : null,
    showResponseGuardrails,
    responseGuardrailLabel: showResponseGuardrails
      ? workflowGuardrailLabel(source, "response")
      : "",
    responseGuardrailBadge: showResponseGuardrails ? phaseLabel("response") : null,
    responseGuardrailNodeClass: showResponseGuardrails ? responseState.nodeClass : "",
    responseGuardrailStatusLabel: showResponseGuardrails ? responseState.statusLabel : null,
    showStreamGuardrails,
    streamGuardrailLabel: showStreamGuardrails ? workflowGuardrailLabel(source, "stream") : "",
    streamGuardrailBadge: showStreamGuardrails ? phaseLabel("stream") : null,
    streamGuardrailNodeClass: showStreamGuardrails ? streamState.nodeClass : "",
    streamGuardrailStatusLabel: showStreamGuardrails ? streamState.statusLabel : null,
    // Per-phase step flow behind each guardrail node's click-to-expand panel.
    guardrailFlows: workflowGuardrailFlows(
      source,
      {
        prompt: showGuardrails,
        response: showResponseGuardrails,
        stream: showStreamGuardrails,
      },
      config.guardrailRefs,
      runtime,
    ),
    showCache: !!config.forceCache || !!features.cache || workflowRuntimeHasCache(runtime),
    cacheNodeClass: workflowCacheNodeClass(runtime, liveStep === "cache"),
    cacheConnClass: workflowCacheConnClass(runtime),
    cacheStatusLabel: workflowCacheStatusLabel(runtime),
    showFailover,
    failoverNodeClass: showFailover ? workflowFailoverNodeClass(runtime) : "",
    failoverConnClass: showFailover ? workflowFailoverConnClass(runtime) : "",
    failoverStatusLabel: showFailover ? workflowFailoverStatusLabel(runtime) : null,
    failoverTargetLabel: showFailover ? workflowFailoverTargetLabel(runtime) : null,
    aiLabel: workflowAiLabel(source, runtime),
    aiSublabel: workflowAiSublabel(source, runtime),
    aiConnClass: workflowBypassedConnClass(runtime),
    aiNodeClass: workflowAiNodeClass(runtime, liveStep === "ai"),
    responseConnClass: workflowBypassedConnClass(runtime),
    responseNodeClass: workflowResponseNodeClass(runtime, liveStep === "response"),
    responseNodeSublabel: workflowResponseNodeSublabel(runtime),
    responseNodeBadge: workflowResponseNodeBadge(runtime),
    authNodeClass: workflowAuthNodeClass(runtime, liveStep === "auth"),
    authNodeSublabel: workflowAuthNodeSublabel(runtime),
    usageNodeClass: workflowAsyncNodeClass(showUsage, usageFlushed, usagePending),
    auditNodeClass: workflowAsyncNodeClass(showAudit, auditFlushed, auditPending),
    showAsync,
    showUsage,
    showAudit,
    workflowID,
  };
}

// workflowChart builds the configuration chart. `guardrailRefs` are the
// GET /admin/workflows/guardrails rows; they tell the step flow which
// instance of a step is its mutator.
export function workflowChart(source, caps, guardrailRefs = []) {
  return workflowChartModel(source, null, { forceCache: false, guardrailRefs }, caps);
}

// workflowAuditChart renders a chart for an audit-log entry. The resolved
// workflow version (source) is passed in by the caller — the version cache
// lives with the audit page.
export function workflowAuditChart(entry, source, caps) {
  const runtime = workflowRuntimeFromEntry(entry, source);
  const features =
    workflowEntryFeatures(entry) ||
    (source
      ? workflowSourceFeatures(source, caps)
      : {
          cache: false,
          audit: false,
          usage: false,
          budget: false,
          guardrails: false,
          failover: false,
        });
  return workflowChartModel(
    source,
    runtime,
    {
      entry,
      features,
      forceAudit: true,
      forceAsync: true,
      highlightAsyncPresent: true,
    },
    caps,
  );
}
