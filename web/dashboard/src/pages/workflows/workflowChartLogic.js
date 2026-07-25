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

export function workflowGuardrailLabel(source) {
  const count = workflowSourceGuardrails(source).length;
  if (count === 0) return "";
  return count === 1 ? "1 step" : count + " steps";
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

function workflowEntryErrorCode(entry) {
  const data =
    entry && entry.data && typeof entry.data === "object" && !Array.isArray(entry.data)
      ? entry.data
      : {};
  const direct = String(data.error_code || data.errorCode || "").trim();
  if (direct) return direct;
  return workflowNestedErrorCode(data.response_body);
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
  const requestedModel = String(
    (entry && (entry.requested_model || entry.model)) || "",
  ).trim();
  const failover = workflowEntryFailover(entry);
  if (!(failover && failover.targetModel)) {
    return {
      provider: String((entry && entry.provider) || "").trim() || null,
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
//   budgetExceeded }
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
  const authError =
    String(entry.error_type || "").trim().toLowerCase() === "authentication_error";
  const authMethod = String(entry.auth_method || "").trim().toLowerCase() || null;
  const budgetExceeded =
    workflowEntryErrorCode(entry).toLowerCase() === "budget_exceeded";
  return {
    cacheHit,
    cacheType: normalizedCacheType || null,
    failoverTarget: failover && failover.targetModel ? failover.targetModel : null,
    provider: primaryRoute.provider,
    model: primaryRoute.model,
    statusCode,
    responseSuccess,
    aiSuccess: responseSuccess && !cacheHit,
    authError,
    authMethod,
    budgetExceeded,
  };
}

function workflowRuntimeHasCache(runtime) {
  return !!(runtime && runtime.cacheHit);
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
  if (runtime.cacheType === "semantic") return "Hit (Semantic)";
  return "Hit (Exact)";
}

export function workflowBudgetNodeClass(visible, runtime, highlightPresent, current) {
  if (!visible) return "";
  if (workflowRuntimeBudgetExceeded(runtime)) return "workflow-node-error";
  if (current) return "workflow-node-current";
  return highlightPresent ? "workflow-node-success" : "";
}

export function workflowBudgetStatusLabel(runtime) {
  return workflowRuntimeBudgetExceeded(runtime) ? "Exceeded" : null;
}

function workflowFailoverNodeClass(runtime) {
  if (runtime && runtime.cacheHit) return "workflow-node-skipped";
  return runtime && runtime.failoverTarget ? "workflow-node-success" : "";
}

function workflowFailoverConnClass(runtime) {
  if (runtime && runtime.cacheHit) return "workflow-conn-dim";
  return runtime && runtime.failoverTarget ? "workflow-conn-hit" : "";
}

function workflowFailoverStatusLabel(runtime) {
  return runtime && runtime.failoverTarget ? "Redirected" : null;
}

function workflowFailoverTargetLabel(runtime) {
  return runtime && runtime.failoverTarget ? runtime.failoverTarget : null;
}

// A cache hit bypasses the AI call, so both the connector into the AI node
// and the one out to Response are dimmed.
function workflowBypassedConnClass(runtime) {
  return runtime && runtime.cacheHit ? "workflow-conn-dim" : "";
}

function workflowAiNodeClass(runtime, current) {
  if (!runtime) return "";
  if (runtime.cacheHit) return "workflow-node-skipped";
  if (current) return "workflow-node-current";
  return runtime.aiSuccess ? "workflow-node-success" : "";
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
  const showGuardrails = !!features.guardrails;
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
    guardrailLabel: showGuardrails ? workflowGuardrailLabel(source) : "",
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

export function workflowChart(source, caps) {
  return workflowChartModel(source, null, { forceCache: false }, caps);
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
