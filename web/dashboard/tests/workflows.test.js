// Ported pure-logic cases from the legacy workflows module tests
// (internal/admin/dashboard/static/js/modules/workflows.test.cjs).
// Feature caps are passed explicitly; the runtime-config flag -> caps mapping
// lives in the shared runtimeConfig store.

import test from "node:test";
import assert from "node:assert/strict";

import {
  defaultWorkflowForm,
  workflowProviderOptions,
  workflowModelOptions,
  workflowPreview,
  workflowSourceFeatures,
  workflowSourceGuardrails,
  workflowActiveScopeMatch,
  workflowDisplayName,
  workflowScopeDisplay,
  normalizeWorkflowScopeUserPath,
  buildWorkflowRequest,
  validateWorkflowRequest,
  shortHash,
  canDeactivateWorkflow,
} from "../src/pages/workflows/workflowsLogic.js";
import {
  workflowChart,
  workflowAuditChart,
  workflowChartWorkflowID,
  workflowRuntimeFromEntry,
  workflowGuardrailLabel,
  workflowAsyncNodeClass,
  workflowCacheNodeClass,
  workflowCacheConnClass,
  workflowCacheStatusLabel,
  workflowFailoverTarget,
  workflowAuthNodeClass,
  workflowAuthNodeSublabel,
  workflowBudgetNodeClass,
  workflowBudgetStatusLabel,
  workflowResponseNodeClass,
  workflowResponseNodeSublabel,
} from "../src/pages/workflows/workflowChartLogic.js";

// All feature gates on: the default when no runtime flags disable anything.
const ALL_CAPS = {
  cache: true,
  audit: true,
  usage: true,
  budget: true,
  guardrails: true,
  failover: true,
};

// Every gate off (legacy: all runtime flags explicitly "off").
const NO_CAPS = {
  cache: false,
  audit: false,
  usage: false,
  budget: false,
  guardrails: false,
  failover: false,
};

// FAILOVER_ENABLED off, everything else on.
const HIDDEN_FAILOVER_CAPS = { ...ALL_CAPS, failover: false };

test("workflowProviderOptions returns unique sorted provider names", () => {
  const models = [
    { provider_type: "anthropic", model: { id: "claude-3-7" } },
    { provider_type: "openai", model: { id: "gpt-5" } },
    { provider_type: "openai", model: { id: "gpt-4o-mini" } },
  ];

  assert.deepEqual(workflowProviderOptions(models, null), ["anthropic", "openai"]);
});

test("defaultWorkflowForm starts failover and budget enabled for new workflows", () => {
  assert.equal(defaultWorkflowForm().features.failover, true);
  assert.equal(defaultWorkflowForm().features.budget, true);
});

test("workflowPreview mirrors the draft workflow card state from the editor form", () => {
  const form = {
    scope_provider: "openai",
    scope_model: "gpt-5",
    name: "Draft workflow",
    description: "Live preview of the edited workflow",
    features: {
      cache: true,
      audit: false,
      usage: true,
      guardrails: true,
      failover: false,
    },
    guardrails: [{ ref: "policy-system", step: 10 }],
  };

  assert.deepEqual(workflowPreview(form, ALL_CAPS), {
    id: "draft-workflow-preview",
    scope_type: "provider_model",
    scope_display: "openai/gpt-5",
    scope: {
      scope_provider_name: "openai",
      scope_model: "gpt-5",
    },
    name: "Draft workflow",
    description: "Live preview of the edited workflow",
    workflow_payload: {
      schema_version: 1,
      features: {
        cache: true,
        audit: false,
        usage: true,
        budget: true,
        guardrails: true,
        failover: false,
      },
      guardrails: [{ ref: "policy-system", step: 10 }],
    },
  });
});

test("workflowPreview renders path-scoped draft labels using canonical scope display", () => {
  const form = {
    scope_provider: "openai",
    scope_model: "gpt-5",
    scope_user_path: " team//alpha/ ",
    name: "Path workflow",
    description: "Preview should include the canonical path scope",
    features: {
      cache: true,
      audit: true,
      usage: true,
      guardrails: false,
      failover: false,
    },
    guardrails: [],
  };

  const preview = workflowPreview(form, ALL_CAPS);
  assert.equal(preview.scope_type, "provider_model_path");
  assert.equal(preview.scope_display, "openai/gpt-5 @ /team/alpha");
  assert.deepEqual(preview.scope, {
    scope_provider_name: "openai",
    scope_model: "gpt-5",
    scope_user_path: "/team/alpha",
  });
});

test("workflowPreview does not coerce blank guardrail steps into step zero", () => {
  const form = {
    scope_provider: "openai",
    scope_model: "gpt-5",
    name: "Draft workflow",
    description: "Preview should not invent step zero",
    features: {
      cache: true,
      audit: true,
      usage: true,
      guardrails: true,
      failover: false,
    },
    guardrails: [{ ref: "policy-system", step: "   " }],
  };

  assert.deepEqual(workflowPreview(form, ALL_CAPS).workflow_payload.guardrails, []);
});

test("workflowChart returns the shared chart contract for workflow sources", () => {
  assert.deepEqual(
    workflowChart(
      {
        id: "workflow-openai-gpt-5-v7",
        scope: {
          scope_provider: "openai",
          scope_model: "gpt-5",
        },
        workflow_payload: {
          features: {
            cache: true,
            audit: true,
            usage: false,
            budget: true,
            guardrails: true,
            failover: true,
          },
          guardrails: [
            { ref: "policy-system", step: 10 },
            { ref: "pii", step: 20 },
          ],
        },
      },
      ALL_CAPS,
    ),
    {
      showBudget: false,
      budgetNodeClass: "",
      budgetStatusLabel: null,
      showGuardrails: true,
      guardrailLabel: "2 steps",
      showCache: true,
      cacheNodeClass: "",
      cacheConnClass: "",
      cacheStatusLabel: null,
      showFailover: true,
      failoverNodeClass: "",
      failoverConnClass: "",
      failoverStatusLabel: null,
      failoverTargetLabel: null,
      aiLabel: "openai",
      aiSublabel: "gpt-5",
      aiConnClass: "",
      aiNodeClass: "",
      responseConnClass: "",
      responseNodeClass: "",
      responseNodeSublabel: null,
      authNodeClass: "",
      authNodeSublabel: null,
      usageNodeClass: "",
      auditNodeClass: "",
      showAsync: true,
      showUsage: false,
      showAudit: true,
      workflowID: "workflow-openai-gpt-5-v7",
    },
  );
});

test("workflowChart masks globally disabled workflow features from persisted workflows", () => {
  const chart = workflowChart(
    {
      scope: {
        scope_provider: "openai",
        scope_model: "gpt-5",
      },
      workflow_payload: {
        features: {
          cache: true,
          audit: true,
          usage: true,
          budget: true,
          guardrails: true,
          failover: true,
        },
        guardrails: [{ ref: "policy-system", step: 10 }],
      },
    },
    NO_CAPS,
  );

  assert.equal(chart.showBudget, false);
  assert.equal(chart.showGuardrails, false);
  assert.equal(chart.showCache, false);
  // Failover keeps the raw payload state even when the global control is off.
  assert.equal(chart.showFailover, true);
  assert.equal(chart.showAsync, false);
  assert.equal(chart.showUsage, false);
  assert.equal(chart.showAudit, false);
  assert.equal(chart.workflowID, null);
});

test("workflowChartWorkflowID ignores the draft preview sentinel and falls back to entry ids", () => {
  assert.equal(
    workflowChartWorkflowID(
      { id: "draft-workflow-preview" },
      { workflow_version_id: "historical-v1" },
    ),
    "historical-v1",
  );
  assert.equal(
    workflowChartWorkflowID(
      { id: "draft-workflow-preview" },
      { workflow_version_id: "draft-workflow-preview" },
    ),
    null,
  );
});

test("workflowAuditChart returns the shared chart contract for audit runtime entries", () => {
  const source = {
    id: "historical-v1",
    scope: {
      scope_provider: "openai",
      scope_model: "gpt-5",
    },
    workflow_payload: {
      features: {
        cache: false,
        audit: true,
        usage: true,
        budget: true,
        guardrails: true,
        failover: true,
      },
      guardrails: [{ ref: "policy-system", step: 10 }],
    },
  };

  assert.deepEqual(
    workflowAuditChart(
      {
        workflow_version_id: "historical-v1",
        cache_type: "semantic",
        provider: "openai",
        model: "gpt-5",
        status_code: 200,
        usage: { entries: 1 },
      },
      source,
      ALL_CAPS,
    ),
    {
      showBudget: true,
      budgetNodeClass: "workflow-node-success",
      budgetStatusLabel: null,
      showGuardrails: true,
      guardrailLabel: "1 step",
      showCache: true,
      cacheNodeClass: "workflow-node-success",
      cacheConnClass: "workflow-conn-hit",
      cacheStatusLabel: "Hit (Semantic)",
      showFailover: true,
      failoverNodeClass: "workflow-node-skipped",
      failoverConnClass: "workflow-conn-dim",
      failoverStatusLabel: null,
      failoverTargetLabel: null,
      aiLabel: "openai",
      aiSublabel: "gpt-5",
      aiConnClass: "workflow-conn-dim",
      aiNodeClass: "workflow-node-skipped",
      responseConnClass: "workflow-conn-dim",
      responseNodeClass: "workflow-node-success",
      responseNodeSublabel: "200",
      authNodeClass: "",
      authNodeSublabel: null,
      usageNodeClass: "workflow-node-success",
      auditNodeClass: "workflow-node-success",
      showAsync: true,
      showUsage: true,
      showAudit: true,
      workflowID: "historical-v1",
    },
  );
});

test("workflowAuditChart forces audit nodes even when the workflow version cannot be resolved", () => {
  const chart = workflowAuditChart(
    {
      workflow_version_id: "missing-workflow",
      cache_type: "exact",
      provider: "openai",
      model: "gpt-5",
      status_code: 200,
    },
    null,
    ALL_CAPS,
  );

  assert.equal(chart.showBudget, false);
  assert.equal(chart.showGuardrails, false);
  assert.equal(chart.showCache, true);
  assert.equal(chart.cacheStatusLabel, "Hit (Exact)");
  assert.equal(chart.showFailover, false);
  assert.equal(chart.aiNodeClass, "workflow-node-skipped");
  assert.equal(chart.responseNodeClass, "workflow-node-success");
  assert.equal(chart.showAsync, true);
  assert.equal(chart.showUsage, false);
  assert.equal(chart.showAudit, true);
  assert.equal(chart.auditNodeClass, "workflow-node-success");
  assert.equal(chart.workflowID, "missing-workflow");
});

test("workflowAuditChart prefers request-time workflow features over current workflow state", () => {
  const source = {
    id: "historical-v2",
    scope: {
      scope_provider: "openai",
      scope_model: "gpt-5",
    },
    workflow_payload: {
      features: {
        cache: true,
        audit: true,
        usage: true,
        budget: true,
        guardrails: true,
        failover: true,
      },
      guardrails: [{ ref: "policy-system", step: 10 }],
    },
  };

  const chart = workflowAuditChart(
    {
      workflow_version_id: "historical-v2",
      provider: "openai",
      model: "gpt-5",
      status_code: 200,
      data: {
        workflow_features: {
          cache: false,
          audit: true,
          usage: false,
          budget: false,
          guardrails: false,
          failover: true,
        },
      },
    },
    source,
    ALL_CAPS,
  );

  assert.equal(chart.showBudget, false);
  assert.equal(chart.showGuardrails, false);
  assert.equal(chart.showCache, false);
  assert.equal(chart.showFailover, true);
  assert.equal(chart.aiNodeClass, "workflow-node-success");
  assert.equal(chart.showUsage, false);
  assert.equal(chart.showAudit, true);
});

test("workflowAuditChart highlights configured failover redirects and exposes the target", () => {
  const source = {
    id: "historical-v3",
    scope: {
      scope_provider: "openai",
      scope_model: "gpt-5",
    },
    workflow_payload: {
      features: {
        cache: false,
        audit: true,
        usage: true,
        budget: true,
        guardrails: false,
        failover: true,
      },
      guardrails: [],
    },
  };

  const chart = workflowAuditChart(
    {
      workflow_version_id: "historical-v3",
      provider: "azure",
      requested_model: "gpt-5",
      status_code: 200,
      usage: { entries: 1 },
      data: {
        workflow_features: {
          cache: false,
          audit: true,
          usage: true,
          budget: true,
          guardrails: false,
          failover: true,
        },
        failover: {
          target_model: "azure/gpt-4o",
        },
      },
    },
    source,
    ALL_CAPS,
  );

  assert.equal(chart.showFailover, true);
  assert.equal(chart.failoverNodeClass, "workflow-node-success");
  assert.equal(chart.failoverConnClass, "workflow-conn-hit");
  assert.equal(chart.failoverStatusLabel, "Redirected");
  assert.equal(chart.failoverTargetLabel, "azure/gpt-4o");
  // The AI node keeps showing the primary route, not the failover target.
  assert.equal(chart.aiLabel, "openai");
  assert.equal(chart.aiSublabel, "gpt-5");

  assert.equal(
    workflowFailoverTarget({
      data: { failover: { target_model: "azure/gpt-4o" } },
    }),
    "azure/gpt-4o",
  );
});

test("workflowRuntimeFromEntry preserves the primary route for cross-provider failover entries", () => {
  assert.deepEqual(
    workflowRuntimeFromEntry(
      {
        provider: "azure",
        requested_model: "gpt-5",
        status_code: 200,
        data: {
          failover: {
            target_model: "azure/gpt-4o",
          },
        },
      },
      {
        scope: {
          scope_provider: "openai",
          scope_model: "gpt-5",
        },
      },
    ),
    {
      cacheHit: false,
      cacheType: null,
      failoverTarget: "azure/gpt-4o",
      provider: "openai",
      model: "gpt-5",
      statusCode: 200,
      responseSuccess: true,
      aiSuccess: true,
      authError: false,
      authMethod: null,
      budgetExceeded: false,
    },
  );
});

test("workflowRuntimeFromEntry derives cache hit state from cache_type", () => {
  const semantic = workflowRuntimeFromEntry({
    cache_type: "semantic",
    provider: "openai",
    model: "gpt-5",
  });
  assert.equal(semantic.cacheHit, true);
  assert.equal(semantic.cacheType, "semantic");
  assert.equal(semantic.aiSuccess, false);

  const empty = workflowRuntimeFromEntry({});
  assert.equal(empty.cacheHit, false);
  assert.equal(empty.cacheType, null);
  assert.equal(empty.statusCode, null);
});

test("audit runtime uses explicit cache-hit labels and highlights the uncached 200 path", () => {
  const semanticHit = workflowRuntimeFromEntry({
    cache_type: "semantic",
    status_code: 200,
  });
  assert.equal(workflowCacheNodeClass(semanticHit), "workflow-node-success");
  assert.equal(workflowCacheConnClass(semanticHit), "workflow-conn-hit");
  assert.equal(workflowCacheStatusLabel(semanticHit), "Hit (Semantic)");

  const uncachedSuccess = workflowRuntimeFromEntry({
    provider: "openai",
    model: "gpt-5",
    status_code: 200,
  });
  assert.equal(uncachedSuccess.cacheHit, false);
  assert.equal(workflowCacheNodeClass(uncachedSuccess), "");
  assert.equal(workflowCacheStatusLabel(uncachedSuccess), null);
});

test("response runtime maps status ranges to chart colors", () => {
  const runtimeFor = (statusCode) =>
    workflowRuntimeFromEntry({ provider: "openai", model: "gpt-5", status_code: statusCode });

  assert.equal(workflowResponseNodeClass(runtimeFor(304)), "workflow-node-neutral");
  assert.equal(workflowResponseNodeSublabel(runtimeFor(304)), "304");
  assert.equal(workflowResponseNodeClass(runtimeFor(429)), "workflow-node-warning");
  assert.equal(workflowResponseNodeClass(runtimeFor(503)), "workflow-node-error");
  assert.equal(workflowResponseNodeClass(runtimeFor(204)), "workflow-node-success");
  assert.equal(runtimeFor(204).aiSuccess, true);
});

test("auth runtime highlights auth node state from audit entries", () => {
  const failedAuth = workflowRuntimeFromEntry({
    auth_method: "api_key",
    error_type: "authentication_error",
  });
  assert.equal(workflowAuthNodeClass(failedAuth), "workflow-node-error");
  assert.equal(workflowAuthNodeSublabel(failedAuth), "api_key");

  const masterKeyAuth = workflowRuntimeFromEntry({
    auth_method: "master_key",
    status_code: 200,
  });
  assert.equal(workflowAuthNodeClass(masterKeyAuth), "workflow-node-success");
  assert.equal(workflowAuthNodeSublabel(masterKeyAuth), "master_key");
});

test("budget runtime highlights audit budget node success and exceeded states", () => {
  const successfulBudget = workflowRuntimeFromEntry({
    status_code: 200,
    data: { workflow_features: { budget: true } },
  });
  assert.equal(
    workflowBudgetNodeClass(true, successfulBudget, true),
    "workflow-node-success",
  );
  assert.equal(workflowBudgetStatusLabel(successfulBudget), null);

  const exceededBudget = workflowRuntimeFromEntry({
    status_code: 429,
    data: {
      error_code: "budget_exceeded",
      workflow_features: { budget: true },
    },
  });
  assert.equal(exceededBudget.budgetExceeded, true);
  assert.equal(workflowBudgetNodeClass(true, exceededBudget, true), "workflow-node-error");
  assert.equal(workflowBudgetStatusLabel(exceededBudget), "Exceeded");

  const chart = workflowAuditChart(
    {
      workflow_version_id: "missing-budget-workflow",
      status_code: 429,
      data: { error_code: "budget_exceeded" },
    },
    null,
    ALL_CAPS,
  );
  assert.equal(chart.showBudget, true);
  assert.equal(chart.budgetNodeClass, "workflow-node-error");
  assert.equal(chart.budgetStatusLabel, "Exceeded");
});

test("workflowAsyncNodeClass only marks async nodes green when highlighted", () => {
  assert.equal(workflowAsyncNodeClass(true, false), "");
  assert.equal(workflowAsyncNodeClass(false, true), "");
  assert.equal(workflowAsyncNodeClass(true, true), "workflow-node-success");
  assert.equal(workflowAsyncNodeClass(true, false, true), "workflow-node-current");
});

test("workflowAuditChart marks live current steps and waits for async flushes", () => {
  const started = workflowAuditChart(
    {
      id: "audit-live-1",
      request_id: "req-live-1",
      _live: true,
      _live_state: "audit.started",
      _live_pending: true,
    },
    null,
    ALL_CAPS,
  );
  assert.equal(started.authNodeClass, "workflow-node-current");
  assert.equal(started.auditNodeClass, "");

  const inAi = workflowAuditChart(
    {
      id: "audit-live-1",
      request_id: "req-live-1",
      provider: "openai",
      requested_model: "gpt-5",
      _live: true,
      _live_state: "audit.updated",
      _live_pending: true,
    },
    null,
    ALL_CAPS,
  );
  assert.equal(inAi.aiNodeClass, "workflow-node-current");

  const auditQueued = workflowAuditChart(
    {
      id: "audit-live-1",
      request_id: "req-live-1",
      provider: "openai",
      requested_model: "gpt-5",
      status_code: 200,
      _live: true,
      _live_state: "audit.completed",
      _live_pending: true,
      _usage_live_state: "usage.completed",
      _usage_live_pending: true,
      _usage_flushed: false,
      data: { workflow_features: { audit: true, usage: true } },
    },
    null,
    ALL_CAPS,
  );
  assert.equal(auditQueued.responseNodeClass, "workflow-node-success");
  assert.equal(auditQueued.auditNodeClass, "workflow-node-current");
  assert.equal(auditQueued.usageNodeClass, "workflow-node-current");

  const auditFlushedUsageQueuedEntry = {
    id: "audit-live-1",
    request_id: "req-live-1",
    provider: "openai",
    requested_model: "gpt-5",
    status_code: 200,
    usage: { entries: 1 },
    _live: true,
    _live_state: "audit.flushed",
    _live_pending: false,
    _audit_flushed: true,
    _usage_live_state: "usage.completed",
    _usage_live_pending: true,
    _usage_flushed: false,
    data: { workflow_features: { audit: true, usage: true } },
  };
  const auditFlushedUsageQueued = workflowAuditChart(
    auditFlushedUsageQueuedEntry,
    null,
    ALL_CAPS,
  );
  assert.equal(auditFlushedUsageQueued.auditNodeClass, "workflow-node-success");
  assert.equal(auditFlushedUsageQueued.usageNodeClass, "workflow-node-current");

  const fullyFlushed = workflowAuditChart(
    {
      ...auditFlushedUsageQueuedEntry,
      _usage_live_state: "usage.flushed",
      _usage_live_pending: false,
      _usage_flushed: true,
    },
    null,
    ALL_CAPS,
  );
  assert.equal(fullyFlushed.auditNodeClass, "workflow-node-success");
  assert.equal(fullyFlushed.usageNodeClass, "workflow-node-success");
});

test("workflowActiveScopeMatch switches submit mode between save and create", () => {
  const workflows = [
    { id: "global-workflow", scope: { scope_provider: "", scope_model: "" } },
    {
      id: "openai-gpt-5-workflow",
      scope: { scope_provider: "openai", scope_model: "gpt-5" },
    },
  ];
  const form = {
    ...defaultWorkflowForm(),
    scope_provider: "openai",
    scope_model: "gpt-5",
  };

  assert.equal(workflowActiveScopeMatch(workflows, form, false).id, "openai-gpt-5-workflow");

  form.scope_model = "gpt-4o-mini";
  assert.equal(workflowActiveScopeMatch(workflows, form, false), null);
});

test("workflowActiveScopeMatch treats path-only selections as scoped", () => {
  const workflows = [
    { id: "global-workflow", scope: { scope_provider: "", scope_model: "" } },
    {
      id: "team-alpha-workflow",
      scope: { scope_provider: "", scope_model: "", scope_user_path: "/team/alpha" },
    },
  ];
  const form = { ...defaultWorkflowForm(), scope_user_path: "team/alpha" };

  assert.equal(workflowActiveScopeMatch(workflows, form, false).id, "team-alpha-workflow");
});

test("buildWorkflowRequest emits provider-model payload and strips guardrails when disabled", () => {
  const form = {
    scope_provider: "openai",
    scope_model: "gpt-5",
    scope_user_path: "/team/alpha",
    name: "OpenAI GPT-5",
    description: "Primary translated requests",
    features: {
      cache: true,
      audit: true,
      usage: true,
      guardrails: false,
      failover: false,
    },
    guardrails: [{ ref: "policy-system", step: 10 }],
  };

  assert.deepEqual(buildWorkflowRequest({ form, caps: ALL_CAPS }), {
    scope_provider_name: "openai",
    scope_model: "gpt-5",
    scope_user_path: "/team/alpha",
    name: "OpenAI GPT-5",
    description: "Primary translated requests",
    workflow_payload: {
      schema_version: 1,
      features: {
        cache: true,
        audit: true,
        usage: true,
        budget: true,
        guardrails: false,
        failover: false,
      },
      guardrails: [],
    },
  });
});

test("buildWorkflowRequest disables budget when usage is disabled in the form", () => {
  const form = {
    scope_provider: "openai",
    scope_model: "gpt-5",
    name: "OpenAI GPT-5",
    description: "Usage disabled",
    features: {
      cache: true,
      audit: true,
      usage: false,
      budget: true,
      guardrails: false,
      failover: true,
    },
    guardrails: [],
  };

  const features = buildWorkflowRequest({ form, caps: ALL_CAPS }).workflow_payload.features;

  assert.equal(features.usage, false);
  assert.equal(features.budget, false);
});

test("buildWorkflowRequest omits failover for new workflows when the control is hidden", () => {
  const form = {
    scope_provider: "openai",
    scope_model: "gpt-5",
    name: "OpenAI GPT-5",
    description: "Preserve hidden failover state",
    features: {
      cache: true,
      audit: true,
      usage: true,
      guardrails: false,
      failover: false,
    },
    guardrails: [],
  };

  assert.deepEqual(
    buildWorkflowRequest({ form, caps: HIDDEN_FAILOVER_CAPS }).workflow_payload.features,
    {
      cache: true,
      audit: true,
      usage: true,
      budget: true,
      guardrails: false,
    },
  );
});

test("buildWorkflowRequest preserves failover state for hydrated workflows even when hidden", () => {
  const form = {
    scope_provider: "openai",
    scope_model: "gpt-5",
    name: "OpenAI GPT-5",
    description: "Preserve hidden failover state",
    features: {
      cache: true,
      audit: true,
      usage: true,
      guardrails: false,
      failover: false,
    },
    guardrails: [],
  };

  assert.deepEqual(
    buildWorkflowRequest({
      form,
      caps: HIDDEN_FAILOVER_CAPS,
      formHydrated: true,
      hydratedScope: { scope_provider: "openai", scope_model: "gpt-5" },
    }).workflow_payload.features,
    {
      cache: true,
      audit: true,
      usage: true,
      budget: true,
      guardrails: false,
      failover: false,
    },
  );
});

test("buildWorkflowRequest preserves hidden failover for fresh save flows matching an active workflow", () => {
  const workflows = [
    {
      id: "openai-gpt-5-workflow",
      scope: { scope_provider: "openai", scope_model: "gpt-5" },
      workflow_payload: {
        features: {
          cache: true,
          audit: true,
          usage: true,
          guardrails: false,
          failover: false,
        },
        guardrails: [],
      },
    },
  ];
  const form = {
    scope_provider: "openai",
    scope_model: "gpt-5",
    name: "OpenAI GPT-5",
    description: "Preserve hidden failover from the active workflow",
    features: {
      cache: true,
      audit: true,
      usage: true,
      guardrails: false,
      failover: true,
    },
    guardrails: [],
  };

  assert.deepEqual(
    buildWorkflowRequest({
      form,
      caps: HIDDEN_FAILOVER_CAPS,
      workflows,
      formHydrated: false,
    }).workflow_payload.features,
    {
      cache: true,
      audit: true,
      usage: true,
      budget: true,
      guardrails: false,
      failover: false,
    },
  );
});

test("buildWorkflowRequest omits hidden failover when a hydrated workflow is retargeted", () => {
  const form = {
    scope_provider: "openai",
    scope_model: "gpt-4o-mini",
    name: "OpenAI GPT-4o mini",
    description: "Retargeted hidden failover should not carry over",
    features: {
      cache: true,
      audit: true,
      usage: true,
      guardrails: false,
      failover: true,
    },
    guardrails: [],
  };

  assert.deepEqual(
    buildWorkflowRequest({
      form,
      caps: HIDDEN_FAILOVER_CAPS,
      formHydrated: true,
      hydratedScope: { scope_provider: "openai", scope_model: "gpt-5" },
    }).workflow_payload.features,
    {
      cache: true,
      audit: true,
      usage: true,
      budget: true,
      guardrails: false,
    },
  );
});

test("buildWorkflowRequest clamps globally disabled features off even when enabled in the form", () => {
  const form = {
    scope_provider: "openai",
    scope_model: "gpt-5",
    name: "OpenAI GPT-5",
    description: "Globally disabled features should be forced off",
    features: {
      cache: true,
      audit: true,
      usage: true,
      guardrails: true,
      failover: true,
    },
    guardrails: [{ ref: "policy-system", step: 10 }],
  };

  assert.deepEqual(buildWorkflowRequest({ form, caps: NO_CAPS }), {
    scope_provider_name: "openai",
    scope_model: "gpt-5",
    name: "OpenAI GPT-5",
    description: "Globally disabled features should be forced off",
    workflow_payload: {
      schema_version: 1,
      features: {
        cache: false,
        audit: false,
        usage: false,
        budget: false,
        guardrails: false,
      },
      guardrails: [],
    },
  });
});

test("buildWorkflowRequest preserves blank guardrail steps as invalid so validation rejects them", () => {
  const models = [{ provider_type: "openai", model: { id: "gpt-5" } }];
  const form = {
    scope_provider: "openai",
    scope_model: "gpt-5",
    name: "OpenAI GPT-5",
    description: "Primary translated requests",
    features: {
      cache: true,
      audit: true,
      usage: true,
      guardrails: true,
      failover: true,
    },
    guardrails: [{ ref: "policy-system", step: "   " }],
  };

  const payload = buildWorkflowRequest({ form, caps: ALL_CAPS });

  assert.ok(Number.isNaN(payload.workflow_payload.guardrails[0].step));
  assert.equal(
    validateWorkflowRequest(payload, { models }),
    "Each guardrail step must use a non-negative integer step number.",
  );
});

test("validateWorkflowRequest rejects negative guardrail steps and duplicate refs", () => {
  const basePayload = (guardrails) => ({
    scope_provider: "",
    scope_model: "",
    name: "Global",
    workflow_payload: {
      schema_version: 1,
      features: { cache: true, audit: true, usage: true, guardrails: true },
      guardrails,
    },
  });

  assert.equal(
    validateWorkflowRequest(basePayload([{ ref: "policy-system", step: -1 }])),
    "Each guardrail step must use a non-negative integer step number.",
  );
  assert.equal(
    validateWorkflowRequest(
      basePayload([
        { ref: "policy-system", step: 10 },
        { ref: "policy-system", step: 20 },
      ]),
    ),
    "Each guardrail ref may appear only once in a workflow.",
  );
  assert.equal(
    validateWorkflowRequest(basePayload([{ ref: "", step: 10 }])),
    "Each guardrail step needs a guardrail ref.",
  );
});

test("validateWorkflowRequest accepts slashless scope_user_path values", () => {
  const models = [{ provider_type: "openai", model: { id: "gpt-5" } }];
  const payload = {
    scope_provider: "openai",
    scope_model: "gpt-5",
    scope_user_path: "/team/alpha",
    name: "Scoped workflow",
    workflow_payload: {
      schema_version: 1,
      features: { cache: true, audit: true, usage: true, guardrails: false },
      guardrails: [],
    },
  };

  assert.equal(validateWorkflowRequest(payload, { models }), "");

  const workflows = [
    {
      id: "openai-gpt-5-team-alpha",
      scope: {
        scope_provider: "openai",
        scope_model: "gpt-5",
        scope_user_path: "/team/alpha",
      },
    },
  ];
  const form = {
    ...defaultWorkflowForm(),
    scope_provider: "openai",
    scope_model: "gpt-5",
    scope_user_path: "team/alpha",
  };
  assert.equal(
    workflowActiveScopeMatch(workflows, form, false).id,
    "openai-gpt-5-team-alpha",
  );
});

test("validateWorkflowRequest rejects invalid scope_user_path segments", () => {
  const payloadWith = (scopeUserPath) => ({
    scope_provider: "",
    scope_model: "",
    scope_user_path: scopeUserPath,
    workflow_payload: {
      schema_version: 1,
      features: { cache: true, audit: true, usage: true, guardrails: false },
      guardrails: [],
    },
  });

  assert.equal(
    validateWorkflowRequest(payloadWith("/team/../alpha")),
    'User path cannot contain "." or ".." segments.',
  );
  assert.equal(
    validateWorkflowRequest(payloadWith("/team:alpha")),
    'User path cannot contain ":" segments.',
  );
});

test("validateWorkflowRequest rejects unregistered provider-model selections", () => {
  const models = [{ provider_type: "openai", model: { id: "gpt-5" } }];

  assert.equal(
    validateWorkflowRequest(
      {
        scope_provider: "anthropic",
        scope_model: "",
        workflow_payload: {
          schema_version: 1,
          features: { cache: true, audit: true, usage: true, guardrails: false },
          guardrails: [],
        },
      },
      { models },
    ),
    "Choose a registered provider name.",
  );

  assert.equal(
    validateWorkflowRequest(
      {
        scope_provider: "openai",
        scope_model: "gpt-4o-mini",
        workflow_payload: {
          schema_version: 1,
          features: { cache: true, audit: true, usage: true, guardrails: false },
          guardrails: [],
        },
      },
      { models },
    ),
    "Choose a registered model for the selected provider name.",
  );
});

test("editing a cloned workflow preserves retired provider and model options", () => {
  const models = [{ provider_type: "openai", model: { id: "gpt-5" } }];
  const hydratedScope = {
    scope_provider: "anthropic",
    scope_model: "claude-retired",
    scope_user_path: "",
  };

  assert.deepEqual(workflowProviderOptions(models, hydratedScope), [
    "anthropic",
    "openai",
  ]);
  assert.deepEqual(workflowModelOptions(models, "anthropic", hydratedScope), [
    "claude-retired",
  ]);

  const form = {
    scope_provider: "anthropic",
    scope_model: "claude-retired",
    name: "Retired workflow",
    description: "Cloned from an older deployment",
    features: {
      cache: true,
      audit: true,
      usage: true,
      guardrails: false,
      failover: true,
    },
    guardrails: [],
  };
  const payload = buildWorkflowRequest({
    form,
    caps: ALL_CAPS,
    formHydrated: true,
    hydratedScope,
  });
  assert.equal(validateWorkflowRequest(payload, { models, hydratedScope }), "");

  const invalidPayload = { ...payload, scope_model: "different-retired-model" };
  assert.equal(
    validateWorkflowRequest(invalidPayload, { models, hydratedScope }),
    "Choose a registered model for the selected provider name.",
  );
});

test("workflowSourceGuardrails keeps step zero but drops negative and fractional steps", () => {
  assert.deepEqual(
    workflowSourceGuardrails({
      workflow_payload: {
        guardrails: [
          { ref: "zero-step", step: 0 },
          { ref: "fractional", step: 1.5 },
          { ref: "negative", step: -1 },
          { ref: "valid", step: 10 },
        ],
      },
    }),
    [
      { ref: "zero-step", step: 0 },
      { ref: "valid", step: 10 },
    ],
  );
});

test("workflowSourceFeatures defaults failover to true when omitted", () => {
  assert.deepEqual(
    workflowSourceFeatures(
      {
        workflow_payload: {
          features: {
            cache: true,
            audit: false,
            usage: true,
            guardrails: false,
          },
        },
      },
      ALL_CAPS,
    ),
    {
      cache: true,
      audit: false,
      usage: true,
      budget: true,
      guardrails: false,
      failover: true,
    },
  );
});

test("workflowSourceFeatures respects effective runtime features for persisted workflows", () => {
  assert.deepEqual(
    workflowSourceFeatures(
      {
        workflow_payload: {
          features: {
            cache: true,
            audit: true,
            usage: true,
            guardrails: true,
            failover: true,
          },
        },
        effective_features: {
          cache: false,
          audit: false,
          usage: true,
          budget: true,
          guardrails: false,
          failover: false,
        },
      },
      ALL_CAPS,
    ),
    {
      cache: false,
      audit: false,
      usage: true,
      budget: true,
      guardrails: false,
      failover: true,
    },
  );
});

test("workflowSourceFeatures masks raw features by global caps when effective features are unavailable", () => {
  assert.deepEqual(
    workflowSourceFeatures(
      {
        workflow_payload: {
          features: {
            cache: true,
            audit: true,
            usage: true,
            guardrails: true,
            failover: true,
          },
        },
      },
      NO_CAPS,
    ),
    {
      cache: false,
      audit: false,
      usage: false,
      budget: false,
      guardrails: false,
      failover: true,
    },
  );
});

test("workflowDisplayName falls back to scope label or All models", () => {
  assert.equal(workflowDisplayName({ name: "", scope_display: "global" }), "All models");
  assert.equal(
    workflowDisplayName({ name: "", scope_display: "openai/gpt-5" }),
    "openai/gpt-5",
  );
  assert.equal(
    workflowDisplayName({ name: "Primary workflow", scope_display: "openai/gpt-5" }),
    "Primary workflow",
  );
});

test("workflowGuardrailLabel only shows a sublabel when guardrail steps exist", () => {
  assert.equal(workflowGuardrailLabel({ workflow_payload: { guardrails: [] } }), "");
  assert.equal(
    workflowGuardrailLabel({
      workflow_payload: { guardrails: [{ ref: "policy-system", step: 10 }] },
    }),
    "1 step",
  );
  assert.equal(
    workflowGuardrailLabel({
      workflow_payload: {
        guardrails: [
          { ref: "policy-system", step: 10 },
          { ref: "pii", step: 20 },
        ],
      },
    }),
    "2 steps",
  );
});

test("scope display and user path normalization", () => {
  assert.equal(normalizeWorkflowScopeUserPath(" team//alpha/ "), "/team/alpha");
  assert.equal(normalizeWorkflowScopeUserPath("/team/../alpha"), "");
  assert.equal(normalizeWorkflowScopeUserPath(""), "");
  assert.equal(
    workflowScopeDisplay({ scope_provider: "openai", scope_model: "gpt-5" }),
    "openai/gpt-5",
  );
  assert.equal(
    workflowScopeDisplay({ scope_provider: "", scope_model: "", scope_user_path: "team/alpha" }),
    "/team/alpha",
  );
  assert.equal(workflowScopeDisplay({}), "global");
});

test("shortHash truncates long hashes and dashes empty ones", () => {
  assert.equal(shortHash(""), "—");
  assert.equal(shortHash("abcdef123456"), "abcdef123456");
  assert.equal(shortHash("abcdef1234567890abcdef"), "abcdef123456…");
});

test("canDeactivateWorkflow blocks only the global workflow", () => {
  assert.equal(canDeactivateWorkflow({ scope_type: "global" }), false);
  assert.equal(canDeactivateWorkflow({ scope_type: "provider" }), true);
  assert.equal(canDeactivateWorkflow({ scope_type: "provider_model_path" }), true);
});

test("a provider's authentication error leaves the gateway auth node green", () => {
  const providerRejectedKey = workflowRuntimeFromEntry({
    auth_method: "master_key",
    provider: "openai",
    status_code: 401,
    error_type: "authentication_error",
    data: { error_provider: "openai", error_message: "Incorrect API key provided" },
  });
  assert.equal(providerRejectedKey.authError, false);
  assert.equal(workflowAuthNodeClass(providerRejectedKey), "workflow-node-success");

  const gatewayRejectedKey = workflowRuntimeFromEntry({
    status_code: 401,
    error_type: "authentication_error",
    data: { error_message: "invalid API key" },
  });
  assert.equal(gatewayRejectedKey.authError, true);
  assert.equal(workflowAuthNodeClass(gatewayRejectedKey), "workflow-node-error");
});
