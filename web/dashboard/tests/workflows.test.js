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
  workflowScopeBadgeVisible,
  nextWorkflowGuardrailStep,
  workflowGuardrailStepIssues,
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
  workflowGuardrailFlow,
  workflowEntryGuardrails,
  workflowGuardrailActionLabel,
  workflowGuardrailOutcomeLabel,
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

// The flow's pseudo-outcome of a configured ref that never ran.
const SKIPPED_OUTCOME = {
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
  title: "Skipped",
};

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
      schema_version: 2,
      features: {
        cache: true,
        audit: false,
        usage: true,
        budget: true,
        guardrails: true,
        failover: false,
      },
      steps: [{ ref: "policy-system", phase: "prompt", step: 10 }],
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

  assert.deepEqual(workflowPreview(form, ALL_CAPS).workflow_payload.steps, []);
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
      guardrailBadge: null,
      guardrailNodeClass: "",
      guardrailStatusLabel: null,
      showResponseGuardrails: false,
      responseGuardrailLabel: "",
      responseGuardrailBadge: null,
      responseGuardrailNodeClass: "",
      responseGuardrailStatusLabel: null,
      showStreamGuardrails: false,
      streamGuardrailLabel: "",
      streamGuardrailBadge: null,
      streamGuardrailNodeClass: "",
      streamGuardrailStatusLabel: null,
      guardrailFlows: {
        prompt: [
          { step: 10, refs: ["policy-system"], mutator: null },
          { step: 20, refs: ["pii"], mutator: null },
        ],
        response: [],
        stream: [],
      },
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
      responseNodeBadge: null,
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
      guardrailBadge: null,
      // A cache hit skipped the guardrails: the finished entry has no outcomes.
      guardrailNodeClass: "workflow-node-skipped",
      guardrailStatusLabel: "Skipped",
      showResponseGuardrails: false,
      responseGuardrailLabel: "",
      responseGuardrailBadge: null,
      responseGuardrailNodeClass: "",
      responseGuardrailStatusLabel: null,
      showStreamGuardrails: false,
      streamGuardrailLabel: "",
      streamGuardrailBadge: null,
      streamGuardrailNodeClass: "",
      streamGuardrailStatusLabel: null,
      guardrailFlows: {
        prompt: [
          {
            step: 10,
            refs: ["policy-system"],
            mutator: null,
            outcomes: { "policy-system": SKIPPED_OUTCOME },
          },
        ],
        response: [],
        stream: [],
      },
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
      responseNodeBadge: null,
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

test("workflowAuditChart names the configured provider instance, not its type", () => {
  const entry = {
    workflow_version_id: "missing-workflow",
    provider: "deepseek",
    provider_name: "mockds",
    model: "deepseek-flash",
    status_code: 200,
  };

  assert.equal(workflowAuditChart(entry, null, ALL_CAPS).aiLabel, "mockds");
  // Entries recorded without an instance name still show the provider type.
  assert.equal(
    workflowAuditChart({ ...entry, provider_name: "" }, null, ALL_CAPS).aiLabel,
    "deepseek",
  );
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
      guardrails: [],
      guardrailBlocked: false,
      guardrailAnswered: false,
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
      schema_version: 2,
      features: {
        cache: true,
        audit: true,
        usage: true,
        budget: true,
        guardrails: false,
        failover: false,
      },
      steps: [],
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
      schema_version: 2,
      features: {
        cache: false,
        audit: false,
        usage: false,
        budget: false,
        guardrails: false,
      },
      steps: [],
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

  assert.ok(Number.isNaN(payload.workflow_payload.steps[0].step));
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
    "Each guardrail ref may appear only once per phase.",
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
      { ref: "zero-step", phase: "prompt", step: 0 },
      { ref: "valid", phase: "prompt", step: 10 },
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

test("workflowScopeBadgeVisible hides a badge that repeats the card head", () => {
  // Global: the kicker already says Global.
  assert.equal(workflowScopeBadgeVisible({ name: "global", scope_display: "global" }), false);
  assert.equal(workflowScopeBadgeVisible({ name: "", scope_display: "global" }), false);
  // Unnamed: the title already is the scope.
  assert.equal(workflowScopeBadgeVisible({ name: "", scope_display: "openai/gpt-5" }), false);
  // Named: the scope badge adds information.
  assert.equal(
    workflowScopeBadgeVisible({ name: "Primary", scope_display: "openai/gpt-5" }),
    true,
  );
});

test("nextWorkflowGuardrailStep orders a new row after its own phase's rows", () => {
  const steps = [
    { ref: "a", phase: "prompt", step: 10 },
    { ref: "b", phase: "prompt", step: "40" },
    { ref: "c", phase: "response", step: 50 },
    { ref: "d", phase: "stream", step: "" },
  ];
  assert.equal(nextWorkflowGuardrailStep(steps, "prompt"), 50);
  assert.equal(nextWorkflowGuardrailStep(steps, "response"), 60);
  assert.equal(nextWorkflowGuardrailStep(steps, "stream"), 10);
  assert.equal(nextWorkflowGuardrailStep([], "prompt"), 10);
  // A legacy row without a phase is a prompt row.
  assert.equal(nextWorkflowGuardrailStep([{ ref: "x", step: 20 }], "prompt"), 30);
});

test("workflowGuardrailStepIssues flags the fields validateWorkflowRequest rejects", () => {
  assert.deepEqual(workflowGuardrailStepIssues({ ref: "scan", phase: "prompt", step: 10 }), {
    ref: false,
    step: false,
  });
  assert.deepEqual(workflowGuardrailStepIssues({ ref: "  ", phase: "prompt", step: "10" }), {
    ref: true,
    step: false,
  });
  assert.deepEqual(workflowGuardrailStepIssues({ ref: "scan", phase: "prompt", step: "" }), {
    ref: false,
    step: true,
  });
  assert.deepEqual(workflowGuardrailStepIssues({ ref: "scan", phase: "prompt", step: -1 }), {
    ref: false,
    step: true,
  });
  assert.deepEqual(workflowGuardrailStepIssues({ ref: "scan", step: 1.5 }), {
    ref: false,
    step: true,
  });
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

// ─── Schema v2: phased steps ───

import {
  workflowGuardrailRefOptions,
  workflowPayloadSteps,
} from "../src/pages/workflows/workflowsLogic.js";

test("buildWorkflowRequest posts schema_version 2 steps with phases", () => {
  const form = {
    ...defaultWorkflowForm(),
    features: { ...defaultWorkflowForm().features, guardrails: true },
    guardrails: [
      { ref: "pii-redact", phase: "prompt", step: 10 },
      { ref: "secret-scan", phase: "response", step: 10 },
      { ref: "secret-scan", phase: "stream", step: "10" },
      { ref: "legacy", step: 20 },
    ],
  };
  const payload = buildWorkflowRequest({ form, caps: ALL_CAPS });
  assert.equal(payload.workflow_payload.schema_version, 2);
  assert.equal("guardrails" in payload.workflow_payload, false);
  assert.deepEqual(payload.workflow_payload.steps, [
    { ref: "pii-redact", phase: "prompt", step: 10 },
    { ref: "secret-scan", phase: "response", step: 10 },
    { ref: "secret-scan", phase: "stream", step: 10 },
    { ref: "legacy", phase: "prompt", step: 20 },
  ]);
  assert.equal(validateWorkflowRequest(payload), "");
});

test("validateWorkflowRequest allows a ref once per phase and rejects unknown phases", () => {
  const payload = (steps) => ({
    scope_provider: "",
    scope_model: "",
    workflow_payload: {
      schema_version: 2,
      features: { guardrails: true },
      steps,
    },
  });
  assert.equal(
    validateWorkflowRequest(
      payload([
        { ref: "scan", phase: "prompt", step: 10 },
        { ref: "scan", phase: "response", step: 10 },
      ]),
    ),
    "",
  );
  assert.equal(
    validateWorkflowRequest(
      payload([
        { ref: "scan", phase: "response", step: 10 },
        { ref: "scan", phase: "response", step: 20 },
      ]),
    ),
    "Each guardrail ref may appear only once per phase.",
  );
  assert.equal(
    validateWorkflowRequest(payload([{ ref: "scan", phase: "route", step: 10 }])),
    "Each guardrail step must use the prompt, response, or stream phase.",
  );
});

test("legacy v1 payloads load as prompt steps; v2 payloads keep their phases", () => {
  assert.deepEqual(
    workflowSourceGuardrails({
      workflow_payload: { schema_version: 1, guardrails: [{ ref: "old", step: 10 }] },
    }),
    [{ ref: "old", phase: "prompt", step: 10 }],
  );
  assert.deepEqual(
    workflowSourceGuardrails({
      workflow_payload: {
        schema_version: 2,
        steps: [
          { ref: "a", phase: "response", step: 10 },
          { ref: "b", phase: "bogus", step: 20 },
        ],
      },
    }),
    [
      { ref: "a", phase: "response", step: 10 },
      { ref: "b", phase: "prompt", step: 20 },
    ],
  );
  // The editor form keeps its rows under `guardrails` but carries phases.
  assert.deepEqual(
    workflowPayloadSteps({ guardrails: [{ ref: "x", phase: "stream", step: 5 }] }),
    [{ ref: "x", phase: "stream", step: 5 }],
  );
});

test("workflowGuardrailRefOptions filters instances by phase and keeps the current ref", () => {
  const refs = [
    { name: "pii-redact", phases: ["prompt", "response"] },
    { name: "stream-scan", phases: ["stream"] },
    { name: "legacy-object" },
    "legacy-string",
  ];
  const names = (phase, current) =>
    workflowGuardrailRefOptions(refs, phase, current).map((option) => option.value);

  assert.deepEqual(names("prompt"), ["pii-redact", "legacy-object", "legacy-string"]);
  assert.deepEqual(names("response"), ["pii-redact"]);
  assert.deepEqual(names("stream"), ["stream-scan"]);
  // A cloned workflow's ref stays selectable even when it no longer qualifies.
  assert.deepEqual(names("stream", "pii-redact"), ["stream-scan", "pii-redact"]);
  assert.deepEqual(names("prompt", ""), ["pii-redact", "legacy-object", "legacy-string"]);
});

test("workflowChart adds response and stream guardrail nodes after the model", () => {
  const chart = workflowChart(
    {
      workflow_payload: {
        schema_version: 2,
        features: { guardrails: true },
        steps: [
          { ref: "pii", phase: "prompt", step: 10 },
          { ref: "scan", phase: "response", step: 10 },
          { ref: "scan", phase: "stream", step: 10 },
          { ref: "scan2", phase: "stream", step: 20 },
        ],
      },
    },
    ALL_CAPS,
  );
  assert.equal(chart.showGuardrails, true);
  assert.equal(chart.guardrailLabel, "1 step");
  assert.equal(chart.guardrailBadge, "Prompt");
  assert.equal(chart.showResponseGuardrails, true);
  assert.equal(chart.responseGuardrailLabel, "1 step");
  assert.equal(chart.responseGuardrailBadge, "Response");
  assert.equal(chart.showStreamGuardrails, true);
  assert.equal(chart.streamGuardrailLabel, "2 steps");
  assert.equal(chart.streamGuardrailBadge, "Stream");

  const promptOnly = workflowChart(
    { workflow_payload: { features: { guardrails: true }, guardrails: [{ ref: "pii", step: 10 }] } },
    ALL_CAPS,
  );
  assert.equal(promptOnly.guardrailBadge, null);
  assert.equal(promptOnly.showResponseGuardrails, false);
  assert.equal(promptOnly.showStreamGuardrails, false);
});

test("workflowGuardrailFlow orders steps ascending and groups same-step refs as parallel", () => {
  const source = {
    workflow_payload: {
      schema_version: 2,
      features: { guardrails: true },
      steps: [
        { ref: "late", phase: "prompt", step: 30 },
        { ref: "reader-a", phase: "prompt", step: "10" },
        { ref: "mutator", phase: "prompt", step: 10 },
        { ref: "  ", phase: "prompt", step: 10 },
        { ref: "middle", phase: "prompt", step: 20 },
        { ref: "scan", phase: "response", step: 10 },
        { ref: "blank", phase: "prompt", step: " " },
      ],
    },
  };
  assert.deepEqual(workflowGuardrailFlow(source, "prompt"), [
    { step: 10, refs: ["reader-a", "mutator", ""], mutator: null },
    { step: 20, refs: ["middle"], mutator: null },
    { step: 30, refs: ["late"], mutator: null },
  ]);
  assert.deepEqual(workflowGuardrailFlow(source, "response"), [
    { step: 10, refs: ["scan"], mutator: null },
  ]);
  assert.deepEqual(workflowGuardrailFlow(source, "stream"), []);
  assert.deepEqual(workflowGuardrailFlow(null), []);

  // An editor draft step with no ref chosen yet still counts as a step on
  // the node, so it stays in the flow as a blank placeholder.
  const draft = { features: { guardrails: true }, guardrails: [{ ref: "", phase: "stream", step: 10 }] };
  assert.equal(workflowGuardrailLabel(draft, "stream"), "1 step");
  assert.deepEqual(workflowGuardrailFlow(draft, "stream"), [{ step: 10, refs: [""], mutator: null }]);
});

test("workflowGuardrailFlow sets a step's mutating instance apart from its readers", () => {
  const source = {
    features: { guardrails: true },
    guardrails: [
      { ref: "pii", phase: "prompt", step: 10 },
      { ref: "rewrite", phase: "prompt", step: 10 },
      { ref: "toxicity", phase: "prompt", step: 10 },
      { ref: "inject", phase: "prompt", step: 20 },
      { ref: "scan", phase: "prompt", step: 30 },
    ],
  };
  const refs = [
    { name: "pii", mutates: false },
    { name: "rewrite", mutates: true },
    { name: "inject", mutates: true },
    { name: "toxicity" },
  ];
  // Refs keep their order and stack together; the step's mutating instance
  // is named apart so the chart can mark it. An unknown ref (scan) is a
  // check.
  assert.deepEqual(workflowGuardrailFlow(source, "prompt", refs), [
    { step: 10, refs: ["pii", "rewrite", "toxicity"], mutator: "rewrite" },
    { step: 20, refs: ["inject"], mutator: "inject" },
    { step: 30, refs: ["scan"], mutator: null },
  ]);
  // Without instance rows no mutator is known.
  assert.deepEqual(workflowGuardrailFlow(source, "prompt"), [
    { step: 10, refs: ["pii", "rewrite", "toxicity"], mutator: null },
    { step: 20, refs: ["inject"], mutator: null },
    { step: 30, refs: ["scan"], mutator: null },
  ]);
});

test("workflowChart only carries step flows for phases that have a node", () => {
  const chart = workflowChart(
    {
      workflow_payload: {
        schema_version: 2,
        features: { guardrails: true },
        steps: [
          { ref: "pii", phase: "prompt", step: 10 },
          { ref: "scan", phase: "stream", step: 5 },
          { ref: "scan2", phase: "stream", step: 5 },
        ],
      },
    },
    ALL_CAPS,
  );
  assert.deepEqual(chart.guardrailFlows, {
    prompt: [{ step: 10, refs: ["pii"], mutator: null }],
    response: [],
    stream: [{ step: 5, refs: ["scan", "scan2"], mutator: null }],
  });
  // Every configured step counts on the node, refs sharing a number too.
  assert.equal(chart.streamGuardrailLabel, "2 steps");

  const typed = workflowChart(
    {
      workflow_payload: {
        schema_version: 2,
        features: { guardrails: true },
        steps: [
          { ref: "pii", phase: "prompt", step: 10 },
          { ref: "rewrite", phase: "prompt", step: 10 },
        ],
      },
    },
    ALL_CAPS,
    [{ name: "rewrite", mutates: true }],
  );
  assert.deepEqual(typed.guardrailFlows.prompt, [
    { step: 10, refs: ["pii", "rewrite"], mutator: "rewrite" },
  ]);

  const disabled = workflowChart(
    {
      workflow_payload: {
        features: { guardrails: false },
        guardrails: [{ ref: "pii", step: 10 }],
      },
    },
    ALL_CAPS,
  );
  assert.deepEqual(disabled.guardrailFlows, { prompt: [], response: [], stream: [] });
});

// ─── Guardrail outcomes ───

const GUARDED_SOURCE = {
  id: "guarded-v3",
  scope: { scope_provider: "openai", scope_model: "gpt-5" },
  workflow_payload: {
    schema_version: 2,
    features: { audit: true, usage: true, guardrails: true, cache: false, budget: false, failover: false },
    steps: [
      { ref: "pii-filter", phase: "prompt", step: 10 },
      { ref: "policy", phase: "prompt", step: 20 },
      { ref: "scan", phase: "response", step: 10 },
      { ref: "redact", phase: "stream", step: 10 },
    ],
  },
};

function guardedEntry(guardrails, extra = {}) {
  return {
    workflow_version_id: "guarded-v3",
    provider: "openai",
    model: "gpt-5",
    status_code: 200,
    usage: { entries: 1 },
    data: { guardrails },
    ...extra,
  };
}

test("workflowEntryGuardrails normalizes recorded outcomes in execution order", () => {
  const outcomes = workflowEntryGuardrails(
    guardedEntry([
      {
        seq: 2,
        phase: "Response",
        step: 10,
        instance: " scan ",
        type: "string_replace",
        action: "ALLOW",
        edited: true,
        replaced_events: "3",
        duration_ns: 1500000,
      },
      { seq: 1, phase: "prompt", step: 10, instance: "pii-filter", action: "warn", code: "pii", message: "found an email" },
      { seq: 3, phase: "stream", instance: "redact", action: "failure", fail_mode: "open", error: "timeout", detail: { retries: 2 } },
      { seq: 4, phase: "prompt", instance: "", action: "block" },
      { seq: 5, phase: "prompt", instance: "odd", action: "maybe" },
      "junk",
    ]),
  );
  assert.deepEqual(
    outcomes.map((o) => [o.seq, o.phase, o.instance, o.action]),
    [
      [1, "prompt", "pii-filter", "warn"],
      [2, "response", "scan", "allow"],
      [3, "stream", "redact", "failure"],
    ],
  );
  assert.deepEqual(outcomes[0], {
    seq: 1,
    phase: "prompt",
    step: 10,
    instance: "pii-filter",
    type: "",
    action: "warn",
    code: "pii",
    message: "found an email",
    detail: null,
    error: "",
    failMode: "",
    edited: false,
    target: "",
    replacedEvents: null,
    droppedEvents: null,
    durationNs: null,
  });
  // An edit without a target names the phase's own subject.
  assert.equal(outcomes[1].type, "string_replace");
  assert.equal(outcomes[1].edited, true);
  assert.equal(outcomes[1].target, "response");
  assert.equal(outcomes[1].replacedEvents, 3);
  assert.equal(outcomes[1].durationNs, 1500000);
  assert.equal(outcomes[2].failMode, "open");
  assert.equal(outcomes[2].error, "timeout");
  assert.deepEqual(outcomes[2].detail, { retries: 2 });
  // fail_mode only means something on a failure.
  assert.equal(
    workflowEntryGuardrails(guardedEntry([{ instance: "x", action: "allow", fail_mode: "closed" }]))[0].failMode,
    "",
  );
  assert.deepEqual(workflowEntryGuardrails(null), []);
  assert.deepEqual(workflowEntryGuardrails({ data: { guardrails: "nope" } }), []);
});

test("workflowEntryGuardrails rebuilds outcomes from the legacy revision trail", () => {
  const entry = {
    status_code: 403,
    data: {
      request_revisions: [
        { seq: 1, rewriter: "compress", bytes_before: 100, bytes_after: 80, body: {} },
        {
          seq: 2,
          rewriter: "pii-filter",
          bytes_before: 80,
          bytes_after: 80,
          no_change: true,
          detail: { phase: "prompt", action: "allow", code: "", message: "" },
        },
        {
          seq: 3,
          rewriter: "inject, rewrite",
          bytes_before: 80,
          bytes_after: 90,
          detail: { phase: "prompt", edited: ["inject", "rewrite"] },
        },
        {
          seq: 4,
          rewriter: "policy",
          bytes_before: 90,
          bytes_after: 90,
          no_change: true,
          detail: { phase: "prompt", action: "block", code: "content_policy", message: "not allowed" },
        },
        {
          seq: 5,
          rewriter: "flaky",
          bytes_before: 90,
          bytes_after: 90,
          no_change: true,
          detail: { phase: "prompt", action: "allow", error: "boom" },
        },
      ],
    },
  };
  const outcomes = workflowEntryGuardrails(entry);
  assert.deepEqual(
    outcomes.map((o) => [o.seq, o.instance, o.action, o.edited, o.target]),
    [
      [1, "pii-filter", "allow", false, ""],
      [2, "inject", "allow", true, "request"],
      [3, "rewrite", "allow", true, "request"],
      [4, "policy", "block", false, ""],
      [5, "flaky", "failure", false, ""],
    ],
  );
  assert.equal(outcomes[3].code, "content_policy");
  assert.equal(outcomes[3].message, "not allowed");
  assert.equal(outcomes[4].error, "boom");
  assert.equal(outcomes[4].failMode, "");

  // A step-level edit revision marks its instance as edited in place.
  const stepEdit = workflowEntryGuardrails({
    data: {
      request_revisions: [
        { seq: 1, rewriter: "rewrite", bytes_before: 10, bytes_after: 12, detail: { phase: "prompt", action: "allow" } },
      ],
    },
  });
  assert.deepEqual(stepEdit.map((o) => [o.instance, o.edited]), [["rewrite", true]]);

  // data.guardrails wins over the legacy trail when present.
  assert.deepEqual(
    workflowEntryGuardrails({
      data: {
        guardrails: [{ instance: "new", action: "allow" }],
        request_revisions: entry.data.request_revisions,
      },
    }).map((o) => o.instance),
    ["new"],
  );
});

test("guardrail labels carry the code only for a stop", () => {
  assert.equal(workflowGuardrailActionLabel({ action: "block" }), "Blocked");
  assert.equal(workflowGuardrailOutcomeLabel({ action: "block", code: "content_policy" }), "Blocked · content_policy");
  assert.equal(workflowGuardrailOutcomeLabel({ action: "respond", code: "canned" }), "Answered · canned");
  assert.equal(workflowGuardrailOutcomeLabel({ action: "warn", code: "pii" }), "Warned");
  assert.equal(workflowGuardrailOutcomeLabel({ action: "failure", code: "x" }), "Failed");
  assert.equal(workflowGuardrailOutcomeLabel({ action: "allow", edited: true }), "Edited");
  assert.equal(workflowGuardrailOutcomeLabel({ action: "allow" }), "Passed");
  assert.equal(workflowGuardrailOutcomeLabel({ action: "skipped" }), "Skipped");
});

test("a prompt block colors the node red, skips the model and keeps the response status", () => {
  const chart = workflowAuditChart(
    guardedEntry(
      [
        { seq: 1, phase: "prompt", step: 10, instance: "pii-filter", action: "allow", edited: true },
        { seq: 2, phase: "prompt", step: 20, instance: "policy", action: "block", code: "content_policy", message: "no" },
      ],
      { status_code: 403 },
    ),
    GUARDED_SOURCE,
    ALL_CAPS,
  );
  assert.equal(chart.guardrailNodeClass, "workflow-node-error");
  assert.equal(chart.guardrailStatusLabel, "Blocked · content_policy");
  assert.equal(chart.aiNodeClass, "workflow-node-skipped");
  assert.equal(chart.aiConnClass, "workflow-conn-dim");
  assert.equal(chart.responseConnClass, "workflow-conn-dim");
  assert.equal(chart.responseNodeClass, "workflow-node-warning");
  assert.equal(chart.responseNodeSublabel, "403");
  assert.equal(chart.responseNodeBadge, null);
  // The later phases never ran: skipped, with their refs skipped too.
  assert.equal(chart.responseGuardrailNodeClass, "workflow-node-skipped");
  assert.equal(chart.responseGuardrailStatusLabel, "Skipped");
  assert.equal(chart.streamGuardrailNodeClass, "workflow-node-skipped");
  assert.deepEqual(chart.guardrailFlows.response, [
    { step: 10, refs: ["scan"], mutator: null, outcomes: { scan: SKIPPED_OUTCOME } },
  ]);
  // The prompt flow carries each ref's outcome and the edited marker.
  const prompt = chart.guardrailFlows.prompt;
  assert.deepEqual(prompt.map((stage) => stage.refs), [["pii-filter"], ["policy"]]);
  assert.equal(prompt[0].outcomes["pii-filter"].tone, "success");
  assert.equal(prompt[0].outcomes["pii-filter"].edited, true);
  assert.equal(prompt[0].outcomes["pii-filter"].title, "Edited");
  assert.deepEqual(prompt[1].outcomes.policy, {
    action: "block",
    code: "content_policy",
    message: "no",
    edited: false,
    error: "",
    failMode: "",
    durationNs: null,
    replacedEvents: null,
    droppedEvents: null,
    tone: "danger",
    title: "Blocked · content_policy\nno",
  });

  const runtime = workflowRuntimeFromEntry(guardedEntry(
    [{ seq: 1, phase: "prompt", instance: "policy", action: "block" }],
    { status_code: 403 },
  ), GUARDED_SOURCE);
  assert.equal(runtime.guardrailBlocked, true);
  assert.equal(runtime.guardrailAnswered, false);
  assert.equal(runtime.aiSuccess, false);
});

test("a prompt respond marks the response as answered by the guardrail", () => {
  const chart = workflowAuditChart(
    guardedEntry([
      { seq: 1, phase: "prompt", step: 10, instance: "pii-filter", action: "allow" },
      { seq: 2, phase: "prompt", step: 20, instance: "policy", action: "respond", code: "canned" },
    ]),
    GUARDED_SOURCE,
    ALL_CAPS,
  );
  assert.equal(chart.guardrailNodeClass, "workflow-node-error");
  assert.equal(chart.guardrailStatusLabel, "Answered · canned");
  assert.equal(chart.aiNodeClass, "workflow-node-skipped");
  assert.equal(chart.responseNodeClass, "workflow-node-success");
  assert.equal(chart.responseNodeBadge, "Answered by guardrail");
  const runtime = workflowRuntimeFromEntry(
    guardedEntry([{ seq: 1, phase: "prompt", instance: "policy", action: "respond" }]),
    GUARDED_SOURCE,
  );
  assert.equal(runtime.guardrailBlocked, true);
  assert.equal(runtime.guardrailAnswered, true);
});

test("guardrail phase rollups rank failures by fail mode and warnings over passes", () => {
  const rollup = (outcomes) => {
    const chart = workflowAuditChart(guardedEntry(outcomes), GUARDED_SOURCE, ALL_CAPS);
    return [chart.guardrailNodeClass, chart.guardrailStatusLabel];
  };
  assert.deepEqual(
    rollup([
      { phase: "prompt", instance: "pii-filter", action: "allow" },
      { phase: "prompt", instance: "policy", action: "allow" },
    ]),
    ["workflow-node-success", "Passed"],
  );
  assert.deepEqual(
    rollup([
      { phase: "prompt", instance: "pii-filter", action: "allow", edited: true },
      { phase: "prompt", instance: "policy", action: "allow" },
    ]),
    ["workflow-node-success", "Edited"],
  );
  assert.deepEqual(
    rollup([
      { phase: "prompt", instance: "pii-filter", action: "warn", code: "pii" },
      { phase: "prompt", instance: "policy", action: "allow", edited: true },
    ]),
    ["workflow-node-warning", "Warned"],
  );
  assert.deepEqual(
    rollup([
      { phase: "prompt", instance: "pii-filter", action: "failure", fail_mode: "open", error: "timeout" },
      { phase: "prompt", instance: "policy", action: "allow" },
    ]),
    ["workflow-node-warning", "Failed"],
  );
  assert.deepEqual(
    rollup([{ phase: "prompt", instance: "pii-filter", action: "failure", fail_mode: "closed", error: "boom" }]),
    ["workflow-node-error", "Failed"],
  );
  // The worst outcome names the label, whatever its position.
  assert.deepEqual(
    rollup([
      { phase: "prompt", instance: "pii-filter", action: "warn" },
      { phase: "prompt", instance: "policy", action: "block", code: "x" },
    ]),
    ["workflow-node-error", "Blocked · x"],
  );

  // A fail-open failure keeps the model call: the AI node stays green.
  const open = workflowAuditChart(
    guardedEntry([{ phase: "prompt", instance: "pii-filter", action: "failure", fail_mode: "open" }]),
    GUARDED_SOURCE,
    ALL_CAPS,
  );
  assert.equal(open.aiNodeClass, "workflow-node-success");
  assert.equal(workflowRuntimeFromEntry(guardedEntry(
    [{ phase: "prompt", instance: "pii-filter", action: "failure", fail_mode: "open" }],
  ), GUARDED_SOURCE).guardrailBlocked, false);
  // A fail-closed one did not.
  assert.equal(workflowRuntimeFromEntry(guardedEntry(
    [{ phase: "prompt", instance: "pii-filter", action: "failure", fail_mode: "closed" }],
    { status_code: 502 },
  ), GUARDED_SOURCE).guardrailBlocked, true);
});

test("a response-phase block leaves the AI node green and colors the response node by status", () => {
  const chart = workflowAuditChart(
    guardedEntry(
      [
        { seq: 1, phase: "prompt", step: 10, instance: "pii-filter", action: "allow" },
        { seq: 2, phase: "prompt", step: 20, instance: "policy", action: "allow" },
        { seq: 3, phase: "response", step: 10, instance: "scan", action: "block", code: "leak" },
      ],
      { status_code: 403 },
    ),
    GUARDED_SOURCE,
    ALL_CAPS,
  );
  assert.equal(chart.guardrailNodeClass, "workflow-node-success");
  assert.equal(chart.guardrailStatusLabel, "Passed");
  assert.equal(chart.aiNodeClass, "workflow-node-success");
  assert.equal(chart.aiConnClass, "");
  assert.equal(chart.responseGuardrailNodeClass, "workflow-node-error");
  assert.equal(chart.responseGuardrailStatusLabel, "Blocked · leak");
  assert.equal(chart.streamGuardrailNodeClass, "workflow-node-skipped");
  assert.equal(chart.responseNodeClass, "workflow-node-warning");
  assert.equal(chart.responseNodeBadge, null);
});

test("guardrail phases without outcomes are skipped only once the entry finished", () => {
  // Live, still running: nothing is known yet.
  const running = workflowAuditChart(
    { ...guardedEntry([]), status_code: null, _live: true, _live_pending: true, _live_state: "audit.started" },
    GUARDED_SOURCE,
    ALL_CAPS,
  );
  assert.equal(running.guardrailNodeClass, "");
  assert.equal(running.guardrailStatusLabel, null);
  assert.equal(running.responseGuardrailNodeClass, "");
  assert.deepEqual(running.guardrailFlows.prompt, [
    { step: 10, refs: ["pii-filter"], mutator: null, outcomes: {} },
    { step: 20, refs: ["policy"], mutator: null, outcomes: {} },
  ]);
  // A finished entry with a passed prompt phase but no later outcomes (the
  // stream phase does not run for a non-streaming call, say).
  const finished = workflowAuditChart(
    guardedEntry([
      { phase: "prompt", instance: "pii-filter", action: "allow" },
      { phase: "prompt", instance: "policy", action: "allow" },
      { phase: "response", instance: "scan", action: "allow" },
    ]),
    GUARDED_SOURCE,
    ALL_CAPS,
  );
  assert.equal(finished.guardrailNodeClass, "workflow-node-success");
  assert.equal(finished.responseGuardrailNodeClass, "workflow-node-success");
  assert.equal(finished.streamGuardrailNodeClass, "workflow-node-skipped");
  assert.equal(finished.streamGuardrailStatusLabel, "Skipped");
  // A configured ref the phase never reached is skipped inside a phase that ran.
  const partial = workflowAuditChart(
    guardedEntry([{ phase: "prompt", step: 10, instance: "pii-filter", action: "block" }], { status_code: 403 }),
    GUARDED_SOURCE,
    ALL_CAPS,
  );
  assert.equal(partial.guardrailFlows.prompt[0].outcomes["pii-filter"].tone, "danger");
  assert.deepEqual(partial.guardrailFlows.prompt[1].outcomes, { policy: SKIPPED_OUTCOME });
  // Configuration charts carry no outcomes at all.
  const config = workflowChart(GUARDED_SOURCE, ALL_CAPS);
  assert.equal(config.guardrailNodeClass, "");
  assert.equal(config.guardrailStatusLabel, null);
  assert.equal(config.responseNodeBadge, null);
  assert.deepEqual(config.guardrailFlows.prompt, [
    { step: 10, refs: ["pii-filter"], mutator: null },
    { step: 20, refs: ["policy"], mutator: null },
  ]);
});

test("recorded outcomes show guardrail nodes even when the workflow version is unresolved", () => {
  const chart = workflowAuditChart(
    {
      workflow_version_id: "gone",
      provider: "openai",
      model: "gpt-5",
      status_code: 200,
      data: {
        workflow_features: { guardrails: false, audit: true, usage: true },
        guardrails: [
          { seq: 1, phase: "prompt", step: 10, instance: "pii-filter", action: "warn", code: "pii" },
          { seq: 2, phase: "stream", step: 5, instance: "redact", action: "allow", edited: true, replaced_events: 2 },
        ],
      },
    },
    null,
    ALL_CAPS,
  );
  assert.equal(chart.showGuardrails, true);
  assert.equal(chart.guardrailNodeClass, "workflow-node-warning");
  assert.equal(chart.guardrailStatusLabel, "Warned");
  assert.equal(chart.showResponseGuardrails, false);
  assert.equal(chart.showStreamGuardrails, true);
  assert.equal(chart.streamGuardrailNodeClass, "workflow-node-success");
  assert.equal(chart.streamGuardrailStatusLabel, "Edited");
  // Unconfigured instances get a stage each so the flow still opens.
  assert.deepEqual(chart.guardrailFlows.prompt.map((s) => [s.step, s.refs]), [[10, ["pii-filter"]]]);
  assert.equal(chart.guardrailFlows.stream[0].outcomes.redact.replacedEvents, 2);
  assert.equal(chart.guardrailFlows.stream[0].outcomes.redact.tone, "success");
});
