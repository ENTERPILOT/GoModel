const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

function readFixture(relativePath) {
  return fs.readFileSync(path.join(__dirname, relativePath), "utf8");
}

function readDashboardTemplateSource() {
  return [
    readFixture("../../../templates/index.html"),
    readFixture("../../../templates/page-overview.html"),
    readFixture("../../../templates/page-usage.html"),
    readFixture("../../../templates/page-settings.html"),
    readFixture("../../../templates/page-guardrails.html"),
    readFixture("../../../templates/page-auth-keys.html"),
    readFixture("../../../templates/page-models.html"),
    readFixture("../../../templates/page-workflows.html"),
    readFixture("../../../templates/page-audit-logs.html"),
  ].join("\n");
}

function readWorkflowTemplateSource() {
  return [
    readDashboardTemplateSource(),
    readFixture("../../../templates/workflow-chart.html"),
  ].join("\n");
}

function readCSSRule(source, selector) {
  const escapedSelector = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = source.match(
    new RegExp(`${escapedSelector}\\s*\\{([\\s\\S]*?)\\n\\}`, "m"),
  );
  assert.ok(match, `Expected CSS rule for ${selector}`);
  return match[1];
}

test("async pipeline branch spans full width and offsets the turn below the main row", () => {
  const template = readWorkflowTemplateSource();
  const css = readFixture("../../css/dashboard.css");

  assert.match(
    template,
    /<div class="workflow-async-section"[\s\S]*?<div class="workflow-async-row">[\s\S]*?<\/div>\s*<div class="workflow-async-turn"><\/div>/,
  );

  const asyncSectionRule = readCSSRule(css, ".workflow-async-section");
  assert.match(asyncSectionRule, /width:\s*100%/);
  assert.doesNotMatch(asyncSectionRule, /flex-direction:\s*column/);
  assert.match(asyncSectionRule, /align-items:\s*center/);
  assert.match(asyncSectionRule, /margin-top:\s*10px/);

  const asyncTurnRule = readCSSRule(css, ".workflow-async-turn");
  assert.match(asyncTurnRule, /flex:\s*0\s+0\s+60px/);
  assert.match(asyncTurnRule, /height:\s*2px/);
  assert.doesNotMatch(asyncTurnRule, /border-bottom:/);
  assert.doesNotMatch(asyncTurnRule, /border-right:/);

  const asyncRowRule = readCSSRule(css, ".workflow-async-row");
  assert.match(asyncRowRule, /display:\s*flex/);
  assert.match(asyncRowRule, /margin-right:\s*7px/);

  const asyncTurnVerticalRule = readCSSRule(css, ".workflow-async-turn::after");
  assert.match(asyncTurnVerticalRule, /border-right:/);
  assert.match(asyncTurnVerticalRule, /bottom:\s*1px/);
  assert.doesNotMatch(asyncTurnVerticalRule, /transform:/);
});

test("async label stays inline on the right side of the branch", () => {
  const template = readWorkflowTemplateSource();
  const css = readFixture("../../css/dashboard.css");

  assert.match(
    template,
    /<div class="workflow-async-row">[\s\S]*workflow-node-async-usage[\s\S]*workflow-conn-async[\s\S]*workflow-node-async-audit[\s\S]*<\/div>\s*<div class="workflow-async-turn"><\/div>\s*<span class="workflow-async-label">Async<\/span>/,
  );
  assert.match(
    template,
    /<div class="workflow-conn workflow-conn-async" x-show="chart\.showUsage && chart\.showAudit"><\/div>\s*<div class="workflow-node workflow-node-feature workflow-node-async workflow-node-async-audit" x-show="chart\.showAudit" :class="chart\.auditNodeClass">/,
  );
  assert.match(
    template,
    /<div class="workflow-node workflow-node-feature workflow-node-async workflow-node-async-usage" x-show="chart\.showUsage" :class="chart\.usageNodeClass">/,
  );

  const asyncLabelRule = readCSSRule(css, ".workflow-async-label");
  assert.doesNotMatch(asyncLabelRule, /position:\s*absolute/);
  assert.doesNotMatch(css, /\.workflow-node-async-usage\s*\{/);
  assert.doesNotMatch(css, /\.workflow-node-async-audit\s*\{/);
});

test("workflow nodes use endpoint and feature color groups consistently", () => {
  const css = readFixture("../../css/dashboard.css");

  const endpointRule = readCSSRule(css, ".workflow-node-endpoint");
  assert.match(endpointRule, /background:\s*var\(--bg-surface\)/);

  const featureRule = readCSSRule(css, ".workflow-node-feature");
  assert.match(
    featureRule,
    /border-color:\s*color-mix\(in srgb, var\(--accent\) 46%, var\(--border\)\)/,
  );
  assert.match(
    featureRule,
    /background:\s*color-mix\(in srgb, var\(--accent\) 8%, var\(--bg-surface\)\)/,
  );

  const featureIconRule = readCSSRule(
    css,
    ".workflow-node-feature .workflow-node-icon",
  );
  assert.match(
    featureIconRule,
    /background:\s*color-mix\(in srgb, var\(--accent\) 16%, var\(--bg\)\)/,
  );
  assert.match(featureIconRule, /color:\s*var\(--accent\)/);

  const featureLabelRule = readCSSRule(
    css,
    ".workflow-node-feature .workflow-node-label",
  );
  assert.match(featureLabelRule, /color:\s*var\(--accent\)/);

  const authSubRule = readCSSRule(
    css,
    ".workflow-node-feature .workflow-node-sub",
  );
  assert.match(
    authSubRule,
    /color:\s*color-mix\(in srgb, var\(--accent\) 70%, var\(--text-muted\)\)/,
  );
});

test("auth node uses the cache iconography in workflow charts", () => {
  const chartTemplate = readFixture("../../../templates/workflow-chart.html");

  assert.match(
    chartTemplate,
    /<div class="workflow-node workflow-node-feature workflow-node-auth"[\s\S]*?<svg viewBox="0 0 24 24"><ellipse cx="12" cy="5" rx="9" ry="3"\/><path d="M21 12c0 1\.66-4 3-9 3s-9-1\.34-9-3"\/><path d="M3 5v14c0 1\.66 4 3 9 3s9-1\.34 9-3V5"\/><\/svg>[\s\S]*?<span class="workflow-node-label">Auth<\/span>/,
  );
});

test("budget node uses the same wallet iconography as the budget navigation item", () => {
  const chartTemplate = readFixture("../../../templates/workflow-chart.html");
  const sidebarTemplate = readFixture("../../../templates/sidebar.html");

  assert.match(sidebarTemplate, /data-lucide="wallet" class="nav-icon"/);
  assert.match(
    chartTemplate,
    /workflow-node-budget[\s\S]*?<svg viewBox="0 0 24 24"><path d="M19 7V4a1 1 0 0 0-1-1H5a2 2 0 0 0 0 4h15a1 1 0 0 1 1 1v4h-3a2 2 0 0 0 0 4h3a1 1 0 0 0 1-1v-2a1 1 0 0 0-1-1"\/><path d="M3 5v14a2 2 0 0 0 2 2h15a1 1 0 0 0 1-1v-4"\/><\/svg>/,
  );
});

test("workflow authoring inputs expose stable accessible names", () => {
  const template = readDashboardTemplateSource();

  assert.match(
    template,
    /x-model="workflowFilter"[^>]*aria-label="Filter workflows by scope, name, hash, or guardrail"/,
  );
  assert.match(
    template,
    /<div class="form-field workflow-guardrail-field">[\s\S]*<label class="form-field-label" :for="'workflow-guardrail-ref-' \+ index">Guardrail reference<\/label>[\s\S]*:id="'workflow-guardrail-ref-' \+ index"[\s\S]*x-model="step\.ref"[^>]*:aria-label="'Guardrail reference ' \+ \(index \+ 1\)"/,
  );
  assert.match(
    template,
    /<div class="form-field workflow-guardrail-step-field">[\s\S]*<label class="form-field-label" :for="'workflow-guardrail-step-' \+ index">Step<\/label>[\s\S]*:id="'workflow-guardrail-step-' \+ index"[\s\S]*x-model\.number="step\.step"[^>]*:aria-label="'Guardrail step ' \+ \(index \+ 1\)"/,
  );
});

test("workflow editor labels the audit toggle as Audit Logs", () => {
  const template = readDashboardTemplateSource();

  assert.match(
    template,
    /x-model="workflowForm\.features\.audit"[\s\S]*?<span>Audit Logs<\/span>/,
  );
});

test("workflow actions use New Workflow copy for open and submit buttons", () => {
  const template = readDashboardTemplateSource();

  assert.match(
    template,
    /class="pagination-btn pagination-btn-primary pagination-btn-with-icon workflow-create-btn"[\s\S]*@click="openWorkflowCreate\(\)"[\s\S]*data-lucide="plus" class="form-action-icon"[\s\S]*<span>New&nbsp;Workflow<\/span>/,
  );
  assert.match(
    template,
    /class="pagination-btn pagination-btn-primary pagination-btn-with-icon workflow-submit-btn"[\s\S]*workflowSubmitting \? workflowSubmittingLabel\(\) : workflowSubmitLabel\(\)/,
  );
  assert.match(
    template,
    /data-lucide="plus" class="form-action-icon" x-show="workflowSubmitMode\(\) === 'create'"[\s\S]*data-lucide="save" class="form-action-icon" x-show="workflowSubmitMode\(\) === 'save'"/,
  );
});

test("workflow editor renders inside the shared modal overlay", () => {
  const template = readDashboardTemplateSource();
  const css = readFixture("../../css/dashboard.css");

  assert.match(
    template,
    /<div class="editor-modal-backdrop"[\s\S]*x-show="workflowFormOpen"[\s\S]*x-transition\.opacity\.duration\.160ms[\s\S]*aria-hidden="true"><\/div>\s*<div class="editor-modal-shell editor-modal-shell-wide"[\s\S]*x-show="workflowFormOpen"[\s\S]*x-transition\.opacity\.duration\.160ms[\s\S]*@click="closeWorkflowForm\(\)"[\s\S]*@keydown\.escape\.window="workflowFormOpen && !authDialogOpen && closeWorkflowForm\(\)"[\s\S]*<section class="model-editor workflow-editor" x-show="workflowFormOpen" x-ref="workflowEditor" role="dialog" aria-modal="true" aria-label="Workflow editor" @click\.stop>/,
  );
  assert.match(
    template,
    /<select id="workflow-scope-provider"[^>]*data-modal-autofocus/,
  );

  const backdropRule = readCSSRule(css, ".editor-modal-backdrop");
  assert.match(backdropRule, /position:\s*fixed/);
  assert.match(backdropRule, /z-index:\s*80/);

  const shellRule = readCSSRule(css, ".editor-modal-shell");
  assert.match(shellRule, /position:\s*fixed/);
  assert.match(shellRule, /place-items:\s*center/);
  assert.match(shellRule, /overflow-y:\s*auto/);

  const shellChildRule = readCSSRule(css, ".editor-modal-shell > *");
  assert.match(shellChildRule, /width:\s*min\(760px,\s*100%\)/);
  assert.match(
    shellChildRule,
    /max-height:\s*min\(calc\(100vh - 40px\),\s*960px\)/,
  );
  assert.match(shellChildRule, /overflow:\s*auto/);

  const wideShellRule = readCSSRule(
    css,
    ".editor-modal-shell.editor-modal-shell-wide > *",
  );
  assert.match(wideShellRule, /width:\s*min\(1080px,\s*100%\)/);
});

test("workflow editor keeps its title inline and reuses only the help toggle button", () => {
  const template = [
    readDashboardTemplateSource(),
    readFixture("../../../templates/inline-help-toggle.html"),
  ].join("\n");

  assert.match(
    template,
    /<div class="inline-help-section" x-data="\{ open: false, copyId: 'workflow-help-copy'[\s\S]*Create immutable version\. Submitting activates it for the selected scope\.[\s\S]*<h3 x-text="workflowSubmitMode\(\) === 'save' \? 'Edit Workflow' : 'Create Workflow'"><\/h3>[\s\S]*{{template "inline-help-toggle" \.}}/,
  );
  assert.match(template, /{{define "inline-help-toggle"}}/);
  assert.doesNotMatch(template, /class="workflow-help"/);
  assert.doesNotMatch(
    template,
    /title="Create immutable version\. Submitting activates it for the selected scope\."/,
  );
});

test("workflow failover controls are gated by the runtime FEATURE_FALLBACK_MODE flag", () => {
  const template = readDashboardTemplateSource();

  assert.match(
    template,
    /x-show="workflowFailoverVisible\(\)"[\s\S]*x-model="workflowForm\.features\.fallback"/,
  );
  assert.match(
    template,
    /x-show="workflowFailoverVisible\(\)"[\s\S]*x-text="'Failover: ' \+ workflowFallbackLabel\(workflow\)"/,
  );
});

test("workflow feature controls and guardrail sections are gated by global runtime visibility helpers", () => {
  const template = readDashboardTemplateSource();

  assert.match(
    template,
    /x-show="workflowCacheVisible\(\)"[\s\S]*x-model="workflowForm\.features\.cache"/,
  );
  assert.match(
    template,
    /x-show="workflowAuditVisible\(\)"[\s\S]*x-model="workflowForm\.features\.audit"/,
  );
  assert.match(
    template,
    /x-show="workflowUsageVisible\(\)"[\s\S]*x-model="workflowForm\.features\.usage"/,
  );
  assert.match(
    template,
    /x-show="workflowBudgetVisible\(\)"[\s\S]*x-model="workflowForm\.features\.budget"/,
  );
  assert.match(
    template,
    /x-show="workflowGuardrailsVisible\(\)"[\s\S]*x-model="workflowForm\.features\.guardrails"/,
  );
  assert.match(
    template,
    /<div class="workflow-guardrails" x-show="workflowGuardrailsVisible\(\)">/,
  );
  assert.match(
    template,
    /<div class="workflow-guardrails" x-show="workflowGuardrailsVisible\(\)">[\s\S]*workflowGuardrails\(workflow\)/,
  );
  assert.match(
    template,
    /<div class="workflow-guardrail-editor" x-show="workflowForm\.features\.guardrails && workflowGuardrailsVisible\(\)">/,
  );
});

test("workflow editor renders a live preview card from the draft workflow state", () => {
  const template = readDashboardTemplateSource();
  const chartTemplate = readFixture("../../../templates/workflow-chart.html");

  assert.match(
    template,
    /<article class="workflow-card workflow-preview-card">[\s\S]*x-text="workflowDisplayName\(workflowPreview\(\)\)"[\s\S]*x-text="workflowScopeLabel\(workflowPreview\(\)\)"[\s\S]*x-text="'Failover: ' \+ workflowFallbackLabel\(workflowPreview\(\)\)"[\s\S]*{{template "workflow-chart" "workflowChart\(workflowPreview\(\)\)"}}[\s\S]*x-show="workflowGuardrails\(workflowPreview\(\)\)\.length > 0"/,
  );
  assert.match(
    chartTemplate,
    /{{define "workflow-chart"}}[\s\S]*x-data="\{ chart: {{\.}} \|\| \{\} \}"[\s\S]*x-effect="chart = {{\.}} \|\| \{\}"[\s\S]*<span class="workflow-node-label">Auth<\/span>[\s\S]*x-text="chart\.authNodeSublabel"[\s\S]*x-show="chart\.showCache"[\s\S]*x-show="chart\.showBudget"[\s\S]*x-show="chart\.showGuardrails"[\s\S]*x-text="chart\.aiLabel"[\s\S]*x-show="chart\.showFailover"[\s\S]*x-text="chart\.failoverTargetLabel"/,
  );
  assert.doesNotMatch(chartTemplate, /x-data="\{ workflow:/);
});

test("audit log pipeline binds cache visibility and runtime highlight classes across the full path", () => {
  const template = readWorkflowTemplateSource();
  const css = readFixture("../../css/dashboard.css");

  assert.match(
    template,
    /{{template "workflow-chart" "workflowAuditChart\(entry\)"}}[\s\S]*<div class="workflow-conn" x-show="chart\.showCache" :class="chart\.cacheConnClass"><\/div>[\s\S]*<div class="workflow-node workflow-node-feature workflow-node-cache" x-show="chart\.showCache" :class="chart\.cacheNodeClass">[\s\S]*x-text="chart\.cacheStatusLabel"[\s\S]*<div class="workflow-conn" x-show="chart\.showFailover" :class="chart\.failoverConnClass"><\/div>[\s\S]*<div class="workflow-node workflow-node-feature workflow-node-failover" x-show="chart\.showFailover" :class="chart\.failoverNodeClass">[\s\S]*x-text="chart\.failoverStatusLabel"[\s\S]*x-text="chart\.failoverTargetLabel"/,
  );
  assert.match(
    template,
    /:class="chart\.authNodeClass"[\s\S]*x-show="chart\.showGuardrails"[\s\S]*x-show="chart\.showUsage"[\s\S]*x-show="chart\.showAudit"/,
  );
  assert.match(
    template,
    /<div class="workflow-conn" :class="chart\.aiConnClass"><\/div>[\s\S]*<div class="workflow-node workflow-node-ai" :class="chart\.aiNodeClass">/,
  );
  assert.match(
    template,
    /<div class="workflow-pipeline" x-data="\{ chart: {{\.}} \|\| \{\} \}" x-effect="chart = {{\.}} \|\| \{\}" :class="\{ 'workflow-pipeline-has-meta': chart\.workflowID \}">[\s\S]*<button type="button"[\s\S]*class="workflow-pipeline-meta workflow-pipeline-meta-copy mono"[\s\S]*x-show="chart\.workflowID"[\s\S]*x-data="workflowIDChip\(chart\.workflowID\)"[\s\S]*x-effect="setWorkflowID\(chart\.workflowID\)"[\s\S]*@click\.prevent="copyWorkflowID\(\)"[\s\S]*<span class="workflow-pipeline-meta-label">id:<\/span>[\s\S]*<span class="workflow-pipeline-meta-placeholder">\.\.\.<\/span>[\s\S]*x-text="workflowID"/,
  );
  assert.match(
    template,
    /<div class="workflow-conn" :class="chart\.responseConnClass"><\/div>[\s\S]*<div class="workflow-node workflow-node-endpoint" :class="chart\.responseNodeClass">/,
  );
  assert.doesNotMatch(
    template,
    /<div class="workflow-pipeline-meta" x-show="chart\.workflowID">/,
  );

  const successRule = readCSSRule(css, ".workflow-node-success");
  assert.match(
    successRule,
    /border-color:\s*color-mix\(in srgb, var\(--success\) 52%, var\(--border\)\)/,
  );
  assert.match(
    successRule,
    /background:\s*color-mix\(in srgb, var\(--success\) 9%, var\(--bg-surface\)\)/,
  );

  const errorRule = readCSSRule(css, ".workflow-node-error");
  assert.match(
    errorRule,
    /border-color:\s*color-mix\(in srgb, var\(--danger\) 52%, var\(--border\)\)/,
  );
  assert.match(
    errorRule,
    /background:\s*color-mix\(in srgb, var\(--danger\) 9%, var\(--bg-surface\)\)/,
  );

  const pipelineRule = readCSSRule(css, ".workflow-pipeline");
  assert.match(pipelineRule, /position:\s*relative/);

  const metaRule = readCSSRule(css, ".workflow-pipeline-meta");
  assert.match(metaRule, /position:\s*absolute/);
  assert.match(metaRule, /top:\s*12px/);
  assert.match(metaRule, /right:\s*14px/);
  assert.match(metaRule, /font-size:\s*12px/);
  assert.match(metaRule, /font-weight:\s*500/);

  const skippedAiRule = readCSSRule(css, ".workflow-node-skipped");
  assert.match(skippedAiRule, /position:\s*relative/);
  assert.match(skippedAiRule, /opacity:\s*0\.28/);
});

test("workflow id pill expands on hover and turns green with a clipboard icon after copy", () => {
  const template = readWorkflowTemplateSource();
  const css = readFixture("../../css/dashboard.css");

  assert.match(
    template,
    /class="workflow-pipeline-meta workflow-pipeline-meta-copy mono"[\s\S]*:class="\{ 'workflow-pipeline-meta-copied': copyState\.copied, 'workflow-pipeline-meta-error': copyState\.error \}"[\s\S]*:title="copyTitle\(\)"[\s\S]*:aria-label="copyAriaLabel\(\)"[\s\S]*<span class="workflow-pipeline-meta-label">id:<\/span>[\s\S]*<span class="workflow-pipeline-meta-placeholder">\.\.\.<\/span>[\s\S]*<span class="workflow-pipeline-meta-value" x-text="workflowID"><\/span>[\s\S]*<span class="workflow-pipeline-meta-icon" aria-hidden="true">[\s\S]*<svg viewBox="0 0 24 24"><rect x="9" y="9" width="13" height="13" rx="2"\/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"\/><\/svg>/,
  );

  const copyRule = readCSSRule(css, ".workflow-pipeline-meta-copy");
  assert.match(copyRule, /cursor:\s*pointer/);
  assert.match(copyRule, /overflow:\s*hidden/);
  assert.match(copyRule, /transition:\s*background-color 0\.15s/);

  const placeholderRule = readCSSRule(
    css,
    ".workflow-pipeline-meta-placeholder",
  );
  assert.match(placeholderRule, /max-width:\s*3ch/);
  assert.match(placeholderRule, /margin-left:\s*4px/);
  assert.match(placeholderRule, /opacity:\s*1/);

  const labelRule = readCSSRule(css, ".workflow-pipeline-meta-label");
  assert.match(labelRule, /font-weight:\s*700/);

  const valueRule = readCSSRule(css, ".workflow-pipeline-meta-value");
  assert.match(valueRule, /max-width:\s*0/);
  assert.match(valueRule, /margin-left:\s*0/);
  assert.match(valueRule, /opacity:\s*0/);

  const expandedPlaceholderRule = readCSSRule(
    css,
    ".workflow-pipeline-meta-copy:hover .workflow-pipeline-meta-placeholder,\n.workflow-pipeline-meta-copy:focus-visible .workflow-pipeline-meta-placeholder,\n.workflow-pipeline-meta-copied .workflow-pipeline-meta-placeholder,\n.workflow-pipeline-meta-error .workflow-pipeline-meta-placeholder",
  );
  assert.match(expandedPlaceholderRule, /max-width:\s*0/);
  assert.match(expandedPlaceholderRule, /opacity:\s*0/);

  const expandedValueRule = readCSSRule(
    css,
    ".workflow-pipeline-meta-copy:hover .workflow-pipeline-meta-value,\n.workflow-pipeline-meta-copy:focus-visible .workflow-pipeline-meta-value,\n.workflow-pipeline-meta-copied .workflow-pipeline-meta-value,\n.workflow-pipeline-meta-error .workflow-pipeline-meta-value",
  );
  assert.match(expandedValueRule, /max-width:\s*42ch/);
  assert.match(expandedValueRule, /margin-left:\s*4px/);
  assert.match(expandedValueRule, /opacity:\s*1/);

  const copiedRule = readCSSRule(
    css,
    ".workflow-pipeline-meta-copied,\n.workflow-pipeline-meta-copied:hover,\n.workflow-pipeline-meta-copied:focus-visible",
  );
  assert.match(copiedRule, /color:\s*var\(--success\)/);

  const baseIconRule = readCSSRule(css, ".workflow-pipeline-meta-icon");
  assert.match(baseIconRule, /display:\s*inline-flex/);
  assert.match(baseIconRule, /align-items:\s*center/);
  assert.match(baseIconRule, /height:\s*14px/);
  assert.match(
    baseIconRule,
    /transform:\s*translateX\(4px\) translateY\(1px\) scale\(0\.84\)/,
  );

  const iconRule = readCSSRule(
    css,
    ".workflow-pipeline-meta-copied .workflow-pipeline-meta-icon",
  );
  assert.match(iconRule, /opacity:\s*1/);
  assert.match(iconRule, /width:\s*14px/);
  assert.match(iconRule, /transform:\s*translateY\(1px\)/);
});

test("workflow pipeline main row is flattened without workflow-left or workflow-right wrappers", () => {
  const template = readFixture("../../../templates/workflow-chart.html");
  const css = readFixture("../../css/dashboard.css");

  assert.doesNotMatch(template, /class="workflow-left"/);
  assert.doesNotMatch(template, /class="workflow-right"/);
  assert.doesNotMatch(template, /class="workflow-step"/);
  assert.match(
    template,
    /<div class="workflow-pipeline-row">[\s\S]*<div class="workflow-node workflow-node-endpoint">[\s\S]*<div class="workflow-conn"><\/div>[\s\S]*<div class="workflow-node workflow-node-feature workflow-node-auth"[\s\S]*<div class="workflow-conn" x-show="chart\.showCache" :class="chart\.cacheConnClass"><\/div>[\s\S]*<div class="workflow-node workflow-node-feature workflow-node-cache" x-show="chart\.showCache" :class="chart\.cacheNodeClass">[\s\S]*<div class="workflow-conn" x-show="chart\.showBudget"><\/div>[\s\S]*<div class="workflow-node workflow-node-feature workflow-node-budget" x-show="chart\.showBudget" :class="chart\.budgetNodeClass">[\s\S]*<div class="workflow-conn" :class="chart\.aiConnClass"><\/div>[\s\S]*<div class="workflow-node workflow-node-ai" :class="chart\.aiNodeClass">[\s\S]*<div class="workflow-conn" :class="chart\.responseConnClass"><\/div>[\s\S]*<div class="workflow-node workflow-node-endpoint" :class="chart\.responseNodeClass">/,
  );
  assert.doesNotMatch(css, /\.workflow-left\s*,/);
  assert.doesNotMatch(css, /\.workflow-right\s*\{/);
  assert.doesNotMatch(css, /\.workflow-step\s*\{/);
  assert.doesNotMatch(css, /\.workflow-conn-grow\s*\{/);
});

test("workflow pipeline main-row connectors share the row width evenly", () => {
  const css = readFixture("../../css/dashboard.css");
  const connRule = readCSSRule(css, ".workflow-conn");

  assert.match(connRule, /flex:\s*1 1 0/);
  assert.match(connRule, /min-width:\s*13px/);
  assert.match(connRule, /width:\s*auto/);
});

test("workflow card actions expose workflow-specific accessible names", () => {
  const template = readDashboardTemplateSource();

  assert.match(
    template,
    /class="table-action-btn table-action-btn-danger"[\s\S]*?:aria-label="'Deactivate workflow ' \+ workflowDisplayName\(workflow\)"/,
  );
  assert.match(
    template,
    /class="table-action-btn table-icon-btn"[\s\S]*:aria-label="'Edit workflow ' \+ workflowDisplayName\(workflow\)"[\s\S]*{{template "edit-icon"}}/,
  );
});

test("guardrails node only renders a sublabel when step detail exists", () => {
  const template = readWorkflowTemplateSource();

  assert.match(
    template,
    /<span class="workflow-node-label">Guardrails<\/span>\s*<span class="workflow-node-sub" x-show="chart\.guardrailLabel" x-text="chart\.guardrailLabel"><\/span>/,
  );
});

test("workflow pipeline icons use lowercase currentcolor keyword", () => {
  const css = readFixture("../../css/dashboard.css");
  const iconRule = readCSSRule(css, ".workflow-node-icon svg");

  assert.match(iconRule, /stroke:\s*currentcolor;/);
});

test("workflow pipeline has bottom spacing so adjacent cards do not touch it", () => {
  const css = readFixture("../../css/dashboard.css");
  const pipelineRule = readCSSRule(css, ".workflow-pipeline");

  assert.match(pipelineRule, /margin-bottom:\s*\d+px/);
});

test("workflow pipeline uses var(--radius) for chart-local corners", () => {
  const css = readFixture("../../css/dashboard.css");

  const radiusSelectors = [
    ".workflow-pipeline",
    ".workflow-node",
    ".workflow-node-icon",
    ".workflow-node-badge",
    ".workflow-node-endpoint",
    ".workflow-node-icon-endpoint",
    ".workflow-node-ai",
    ".workflow-node-async",
    ".workflow-node-async .workflow-node-icon",
  ];

  for (const selector of radiusSelectors) {
    const rule = readCSSRule(css, selector);
    assert.match(rule, /border-radius:\s*var\(--radius\)/);
  }
});

test("AI node renders as a text-only card without an icon", () => {
  const template = readWorkflowTemplateSource();
  const css = readFixture("../../css/dashboard.css");

  assert.doesNotMatch(
    template,
    /class="workflow-node workflow-node-ai[^"]*"[^>]*>\s*<div class="workflow-node-icon">/,
  );
  assert.doesNotMatch(css, /\.workflow-node-ai \.workflow-node-icon\s*\{/);
});

test("workflow cards reuse the extracted workflow chart template", () => {
  const template = readDashboardTemplateSource();

  assert.match(
    template,
    /{{template "workflow-chart" "workflowChart\(workflowPreview\(\)\)"}}/,
  );
  assert.match(
    template,
    /{{template "workflow-chart" "workflowChart\(workflow\)"}}/,
  );
  assert.match(
    template,
    /{{template "workflow-chart" "workflowAuditChart\(entry\)"}}/,
  );
});

test("endpoint pills use dedicated flush-left icons and tighter right padding", () => {
  const template = readWorkflowTemplateSource();
  const css = readFixture("../../css/dashboard.css");

  assert.match(
    template,
    /class="workflow-node-icon workflow-node-icon-endpoint"/,
  );

  const endpointRule = readCSSRule(css, ".workflow-node-endpoint");
  assert.match(endpointRule, /padding:\s*10px 14px/);

  const endpointIconRule = readCSSRule(css, ".workflow-node-icon-endpoint");
  assert.match(endpointIconRule, /width:\s*auto/);
  assert.match(endpointIconRule, /height:\s*auto/);
  assert.match(endpointIconRule, /justify-content:\s*flex-start/);
  assert.match(endpointIconRule, /padding:\s*0/);
});
