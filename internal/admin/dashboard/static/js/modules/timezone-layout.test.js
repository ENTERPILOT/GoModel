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

function readInlineHelpTemplateSource() {
  return [
    readDashboardTemplateSource(),
    readFixture("../../../templates/inline-help-toggle.html"),
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

test("dashboard layout loads the timezone module before the main bootstrap", () => {
  const layout = readFixture("../../../templates/layout.html");

  assert.match(
    layout,
    /<script src="\/admin\/static\/js\/modules\/timezone\.js"><\/script>[\s\S]*<script src="\/admin\/static\/js\/dashboard\.js"><\/script>/,
  );
});

test("dashboard templates expose a settings page and timezone context in activity and log timestamps", () => {
  const template = readInlineHelpTemplateSource();
  const toggleTemplate = readFixture("../../../templates/inline-help-toggle.html");
  const css = readFixture("../../css/dashboard.css");

  assert.match(
    template,
    /<template x-if="page==='settings'">\s*<div>[\s\S]*<h2>Settings<\/h2>/,
  );
  assert.doesNotMatch(template, /Dashboard preferences\./);
  assert.doesNotMatch(template, /settings-subnav/);
  assert.doesNotMatch(template, /navigateSettings/);
  assert.match(template, /x-ref="timezoneOverrideSelect"/);
  assert.match(template, /x-model="timezoneOverride"/);
  assert.match(
    template,
    /x-effect="timezoneOptions\.length; timezoneOverride; \$nextTick\(\(\) => syncTimezoneOverrideSelectValue\(\)\)"/,
  );
  assert.match(template, /<option value=""/);
  assert.match(template, /:selected="!timezoneOverride"/);
  assert.match(template, /<option :value="timeZone\.value"/);
  assert.match(template, /:selected="timeZone\.value === timezoneOverride"/);
  assert.match(
    template,
    /<div class="inline-help-section" x-data="\{ open: false, copyId: 'runtime-refresh-help-copy'[\s\S]*<h3>Runtime Refresh<\/h3>[\s\S]*{{template "inline-help-toggle" \.}}/,
  );
  assert.match(template, /@click="refreshRuntime\(\)"/);
  assert.match(
    template,
    /class="pagination-btn pagination-btn-primary pagination-btn-with-icon settings-refresh-btn"[\s\S]*:class="\{ 'is-refreshing': runtimeRefreshLoading \}"[\s\S]*:aria-busy="runtimeRefreshLoading \? 'true' : 'false'"[\s\S]*data-lucide="refresh-cw"[\s\S]*<span>Refresh<\/span>/,
  );
  assert.doesNotMatch(
    template,
    /class="loading-state settings-refresh-loading"/,
  );
  assert.match(
    template,
    /class="alert alert-success settings-refresh-alert"[\s\S]*role="status"[\s\S]*aria-live="polite"[\s\S]*runtimeRefreshSucceeded\(\)/,
  );
  assert.match(
    template,
    /class="alert alert-warning settings-refresh-alert"[\s\S]*role="alert"[\s\S]*aria-live="assertive"[\s\S]*runtimeRefreshError \|\| runtimeRefreshNotice/,
  );
  assert.match(
    template,
    /class="runtime-refresh-steps"[\s\S]*role="status"[\s\S]*aria-live="polite"[\s\S]*runtimeRefreshStepLabel\(step\)/,
  );
  assert.match(template, /x-for="step in runtimeRefreshSteps\(\)"/);
  assert.match(
    template,
    /<div class="inline-help-section" x-data="\{ open: false, copyId: 'timezone-help-copy'[\s\S]*<h3>Timezone<\/h3>[\s\S]*{{template "inline-help-toggle" \.}}/,
  );
  assert.match(template, /class="inline-help-toggle"/);
  assert.match(template, /class="inline-help-toggle-icon"/);
  assert.match(
    toggleTemplate,
    /{{define "inline-help-toggle"}}[\s\S]*class="inline-help-toggle"[\s\S]*{{end}}/,
  );
  assert.match(
    template,
    /<span class="inline-help-toggle-icon"[^>]*>\?<\/span>/,
  );
  assert.match(template, /:aria-label="open \? hideLabel : showLabel"/);
  assert.match(template, /:aria-controls="copyId"/);
  assert.match(template, /:id="copyId"/);
  assert.match(template, /x-show="open"/);
  assert.match(template, /x-transition\.opacity\.duration\.200ms/);
  assert.doesNotMatch(toggleTemplate, /:title=/);
  assert.match(
    template,
    /Day-based analytics, charts, and date filters use your effective timezone\. Usage and audit logs keep UTC in the hover title while rendering row timestamps in your effective timezone\./,
  );
  assert.doesNotMatch(template, /Detected: /);
  assert.doesNotMatch(template, /Effective: /);
  assert.doesNotMatch(template, /Mode: /);
  assert.doesNotMatch(template, /x-text="calendarTimeZoneText\(\)"/);
  assert.doesNotMatch(template, /class="contribution-calendar-timezone"/);
  assert.match(template, /class="mono usage-ts"/);
  assert.match(template, /x-text="formatTimestamp\(entry\.timestamp\)"/);
  assert.match(template, /:title="timestampTitle\(entry\.timestamp\)"/);
  assert.match(template, /class="audit-entry-right"/);
  assert.match(
    template,
    /<button(?=[^>]*class="audit-conversation-trigger")(?=[^>]*type="button")[^>]*>/,
  );

  const toggleRule = readCSSRule(css, ".inline-help-toggle");
  assert.match(toggleRule, /width:\s*16px/);
  assert.match(toggleRule, /height:\s*16px/);
  assert.match(toggleRule, /position:\s*relative/);
  assert.match(toggleRule, /border-radius:\s*4px/);
  assert.match(toggleRule, /background:\s*transparent/);
  assert.doesNotMatch(toggleRule, /box-shadow:/);

  const toggleHitAreaRule = readCSSRule(css, ".inline-help-toggle::before");
  assert.match(toggleHitAreaRule, /content:\s*""/);
  assert.match(toggleHitAreaRule, /position:\s*absolute/);
  assert.match(toggleHitAreaRule, /width:\s*32px/);
  assert.match(toggleHitAreaRule, /height:\s*32px/);
  assert.match(toggleHitAreaRule, /top:\s*50%/);
  assert.match(toggleHitAreaRule, /left:\s*50%/);
  assert.match(toggleHitAreaRule, /transform:\s*translate\(-50%,\s*-50%\)/);
  assert.match(toggleHitAreaRule, /background:\s*transparent/);
  assert.match(toggleHitAreaRule, /pointer-events:\s*auto/);

  const toggleHoverRule = readCSSRule(css, ".inline-help-toggle:hover");
  assert.match(toggleHoverRule, /background:\s*transparent/);
  assert.doesNotMatch(toggleHoverRule, /transform:/);

  const toggleOpenRule = readCSSRule(css, ".inline-help-toggle.is-open");
  assert.match(toggleOpenRule, /background:\s*transparent/);

  const iconRule = readCSSRule(css, ".inline-help-toggle-icon");
  assert.match(iconRule, /transform:\s*rotate\(0deg\)/);

  const iconOpenRule = readCSSRule(
    css,
    ".inline-help-toggle.is-open .inline-help-toggle-icon",
  );
  assert.match(iconOpenRule, /transform:\s*rotate\(540deg\)/);

  const copyRule = readCSSRule(css, ".inline-help-copy");
  assert.doesNotMatch(copyRule, /border:/);
  assert.doesNotMatch(copyRule, /background:/);
  assert.doesNotMatch(copyRule, /padding:/);

  const refreshRule = readCSSRule(css, ".settings-refresh-section");
  assert.match(refreshRule, /border-top:\s*1px solid var\(--border\)/);
  assert.match(refreshRule, /display:\s*grid/);
  assert.match(refreshRule, /justify-items:\s*start/);

  assert.doesNotMatch(css, /\.settings-refresh-loading\s*\{/);

  const refreshIconRule = readCSSRule(
    css,
    ".settings-refresh-icon,\n.alias-create-icon,\n.form-action-icon",
  );
  assert.match(refreshIconRule, /width:\s*16px/);
  assert.match(refreshIconRule, /height:\s*16px/);

  const refreshAnimatingIconRule = readCSSRule(
    css,
    ".settings-refresh-btn.is-refreshing .settings-refresh-icon",
  );
  assert.match(
    refreshAnimatingIconRule,
    /animation:\s*loading-spin 0\.8s linear infinite/,
  );
  assert.match(refreshAnimatingIconRule, /transform-origin:\s*center/);

  const refreshPartialRule = readCSSRule(
    css,
    ".runtime-refresh-step.is-partial",
  );
  assert.match(refreshPartialRule, /color:\s*var\(--warning\)/);

  const refreshFailedRule = readCSSRule(css, ".runtime-refresh-step.is-failed");
  assert.match(refreshFailedRule, /color:\s*var\(--danger\)/);
});

test("guardrails authoring moved to a top-level page while settings keeps the general switch", () => {
  const template = readInlineHelpTemplateSource();
  const css = readFixture("../../css/dashboard.css");

  assert.match(
    template,
    /<template x-if="page==='settings'">\s*<div>[\s\S]*<h2>Settings<\/h2>[\s\S]*{{template "auth-banner" \.}}/,
  );
  assert.match(
    template,
    /<template x-if="page==='guardrails'">\s*<div>[\s\S]*<div class="inline-help-section" x-data="\{ open: false, copyId: 'guardrails-help-copy'[\s\S]*<h2>Guardrails<\/h2>[\s\S]*{{template "inline-help-toggle" \.}}/,
  );
  assert.match(
    template,
    /Reusable policy objects stored in the database and kept hot in memory for workflow execution\./,
  );
  assert.match(template, /Guardrail Library/);
  assert.match(
    template,
    /class="pagination-btn pagination-btn-primary pagination-btn-with-icon guardrail-create-btn"[\s\S]*@click="openGuardrailCreate\(\)"[\s\S]*data-lucide="plus" class="form-action-icon"[\s\S]*<span>Create(?:&nbsp;| )Guardrail<\/span>/,
  );
  assert.match(
    template,
    /<form class="form" @submit\.prevent="submitGuardrailForm\(\)">[\s\S]*type="submit" class="pagination-btn pagination-btn-primary pagination-btn-with-icon guardrail-submit-btn"[\s\S]*data-lucide="save" class="form-action-icon"[\s\S]*<span>Save Guardrail<\/span>/,
  );
  assert.doesNotMatch(template, /guardrail-submit-btn"[\s\S]*@click="submitGuardrailForm\(\)"/);
  assert.match(
    template,
    /x-show="!authError && guardrailError && !guardrailFormOpen"/,
  );
  assert.match(
    template,
    /class="form-error" x-show="guardrailError" x-text="guardrailError" role="alert" aria-live="assertive"/,
  );
  assert.match(template, /x-ref="guardrailTypeSelect"/);
  assert.match(template, /x-model="guardrailForm\.type"/);
  assert.match(
    template,
    /x-effect="guardrailTypes\.length; guardrailForm\.type; \$nextTick\(\(\) => syncGuardrailTypeSelectValue\(\)\)"/,
  );
  assert.match(
    template,
    /x-model="guardrailForm\.user_path"[^>]*aria-label="Guardrail user path"/,
  );
  assert.match(
    template,
    /copyId: 'guardrail-user-path-help-copy'[\s\S]*Only used for auxiliary rewrite \(llm_based_altering\) guardrails; ignored for other guardrail types\.[\s\S]*<label class="form-field-label" for="guardrail-user-path">User Path<\/label>[\s\S]*{{template "inline-help-toggle" \.}}/,
  );
  assert.match(
    template,
    /aria-describedby="guardrail-user-path-help-copy"/,
  );
  assert.match(
    template,
    /<div class="form-field form-field-wide" x-data="\{ open: false, get copyId\(\) \{ return 'guardrail-field-help-' \+ field\.key \}[\s\S]*get showLabel\(\) \{ return 'Show ' \+ field\.label \+ ' help' \}[\s\S]*get hideLabel\(\) \{ return 'Hide ' \+ field\.label \+ ' help' \}[\s\S]*get text\(\) \{ return field\.help \|\| '' \} \}">/,
  );
  assert.match(
    template,
    /<label class="form-field-label" :for="'guardrail-field-' \+ field\.key" x-text="field\.label"><\/label>[\s\S]*<template x-if="field\.help">[\s\S]*{{template "inline-help-toggle" \.}}/,
  );
  assert.match(
    template,
    /<p :id="copyId" class="inline-help-copy" x-show="open && text" x-transition\.opacity\.duration\.200ms x-text="text"><\/p>/,
  );
  assert.match(
    template,
    /:aria-describedby="field\.help \? 'guardrail-field-help-' \+ field\.key : null"/,
  );
  assert.doesNotMatch(
    template,
    /class="page-subtitle">Reusable policy objects stored in the database and kept hot in memory for workflow execution\.<\/p>/,
  );
  assert.doesNotMatch(template, /class="form-textarea"/);
  assert.doesNotMatch(template, /class="settings-guardrail-textarea"/);
  assert.match(
    template,
    /<fieldset class="form-field form-field-wide form-field-fieldset"[^>]*:aria-describedby="field\.help \? 'guardrail-field-help-' \+ field\.key : null"/,
  );
  assert.match(
    template,
    /<legend class="form-field-legend" x-text="field\.label"><\/legend>/,
  );
  assert.match(
    template,
    /<small class="form-hint" :id="'guardrail-field-help-' \+ field\.key" x-show="field\.help" x-text="field\.help"><\/small>/,
  );

  const fieldsetRule = readCSSRule(css, ".form-field-fieldset");
  assert.match(fieldsetRule, /border:\s*0/);
  assert.match(fieldsetRule, /padding:\s*0/);

  const legendRule = readCSSRule(css, ".form-field-legend");
  assert.match(legendRule, /text-transform:\s*uppercase/);
  assert.match(legendRule, /letter-spacing:\s*0\.5px/);

  const formErrorRule = readCSSRule(css, ".form-error");
  assert.match(formErrorRule, /background:\s*color-mix\(in srgb, var\(--danger\) 10%, var\(--bg-surface\)\)/);
  assert.match(formErrorRule, /overflow-wrap:\s*anywhere/);
});

test("modal forms expose form-scoped error alerts", () => {
  const template = readDashboardTemplateSource();

  assert.match(
    template,
    /class="form-error" x-show="workflowFormError" x-text="workflowFormError" role="alert" aria-live="assertive"/,
  );
  assert.match(
    template,
    /class="form-error" x-show="aliasFormError" x-text="aliasFormError" role="alert" aria-live="assertive"/,
  );
  assert.match(
    template,
    /class="form-error" x-show="modelOverrideError" x-text="modelOverrideError" role="alert" aria-live="assertive"/,
  );
  assert.match(
    template,
    /class="form-error" x-show="authKeyError" x-text="authKeyError" role="alert" aria-live="assertive"/,
  );
  assert.match(
    template,
    /class="form-error" x-show="authKeyError && !authError && !authKeyFormOpen" x-text="authKeyError" role="alert" aria-live="assertive"/,
  );
});
