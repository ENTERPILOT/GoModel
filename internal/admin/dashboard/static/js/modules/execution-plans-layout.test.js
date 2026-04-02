const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

function readFixture(relativePath) {
    return fs.readFileSync(path.join(__dirname, relativePath), 'utf8');
}

function readExecutionPlanTemplateSource() {
    return [
        readFixture('../../../templates/index.html'),
        readFixture('../../../templates/execution-plan-chart.html')
    ].join('\n');
}

function readCSSRule(source, selector) {
    const escapedSelector = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    const match = source.match(new RegExp(`${escapedSelector}\\s*\\{([\\s\\S]*?)\\n\\}`, 'm'));
    assert.ok(match, `Expected CSS rule for ${selector}`);
    return match[1];
}

test('execution pipeline uses a two-row grid with a single terminal cell spanning both rows', () => {
    const template = readExecutionPlanTemplateSource();
    const css = readFixture('../../css/dashboard.css');

    assert.match(
        template,
        /<div class="exec-pipeline-grid">[\s\S]*<div class="exec-pipeline-row exec-pipeline-row-request">[\s\S]*<div class="ep-terminal">[\s\S]*<div class="exec-pipeline-row exec-pipeline-row-response">/
    );
    assert.doesNotMatch(template, /ep-downstream/);
    assert.doesNotMatch(template, /ep-async-section|ep-async-row|ep-async-turn/);

    const gridRule = readCSSRule(css, '.exec-pipeline-grid');
    assert.match(gridRule, /display:\s*grid/);
    assert.match(gridRule, /grid-template-columns:\s*minmax\(0,\s*1fr\)\s+minmax\(120px,\s*max-content\)/);
    assert.match(gridRule, /grid-template-rows:\s*auto auto/);
    assert.match(gridRule, /column-gap:\s*0/);
    assert.match(gridRule, /row-gap:\s*0/);

    const terminalRule = readCSSRule(css, '.ep-terminal');
    assert.match(terminalRule, /grid-column:\s*2/);
    assert.match(terminalRule, /grid-row:\s*1\s*\/\s*span 2/);
    assert.match(terminalRule, /display:\s*flex/);
    assert.match(terminalRule, /align-items:\s*stretch/);
    assert.match(terminalRule, /justify-self:\s*stretch/);
    assert.match(terminalRule, /width:\s*100%/);
    assert.match(terminalRule, /min-width:\s*120px/);

    const requestRowRule = readCSSRule(css, '.exec-pipeline-row-request');
    assert.match(requestRowRule, /grid-column:\s*1/);
    assert.match(requestRowRule, /grid-row:\s*1/);

    const responseRowRule = readCSSRule(css, '.exec-pipeline-row-response');
    assert.match(responseRowRule, /grid-column:\s*1/);
    assert.match(responseRowRule, /grid-row:\s*2/);
    assert.match(responseRowRule, /justify-content:\s*flex-end/);
});

test('response row flows inline from the terminal column to response and async nodes', () => {
    const template = readExecutionPlanTemplateSource();
    const css = readFixture('../../css/dashboard.css');

    assert.match(
        template,
        /<div class="exec-pipeline-row exec-pipeline-row-response">[\s\S]*ep-node-async-usage[\s\S]*ep-conn-async[\s\S]*ep-node-async-audit[\s\S]*<div class="ep-step ep-step-async-branch" x-show="{{\.}}\.showAsync">[\s\S]*ep-conn-async[\s\S]*<span class="ep-async-label">Async<\/span>[\s\S]*<div class="ep-node ep-node-endpoint" :class="{{\.}}\.responseNodeClass">[\s\S]*<div class="ep-conn ep-conn-rtl ep-conn-grow" :class="{{\.}}\.responseConnClass"><\/div>/
    );

    const asyncLabelRule = readCSSRule(css, '.ep-async-label');
    assert.match(asyncLabelRule, /position:\s*absolute/);
    assert.match(asyncLabelRule, /left:\s*50%/);
    assert.match(asyncLabelRule, /bottom:\s*calc\(100%\s*\+\s*6px\)/);
    assert.match(asyncLabelRule, /transform:\s*translateX\(-50%\)/);

    const rtlConnectorRule = readCSSRule(css, '.ep-conn-rtl::after');
    assert.match(rtlConnectorRule, /left:\s*-1px/);
    assert.match(rtlConnectorRule, /clip-path:\s*polygon\(100% 0,\s*0 50%,\s*100% 100%\)/);

    const asyncConnectorRule = readCSSRule(css, '.ep-conn-async');
    assert.match(asyncConnectorRule, /width:\s*50px/);
    assert.match(asyncConnectorRule, /min-width:\s*50px/);

    const growConnectorRule = readCSSRule(css, '.ep-conn-grow');
    assert.match(growConnectorRule, /flex:\s*1/);
    assert.match(growConnectorRule, /min-width:\s*16px/);
    assert.match(growConnectorRule, /width:\s*auto/);

    const terminalNodeRule = readCSSRule(css, '.ep-terminal .ep-node');
    assert.match(terminalNodeRule, /height:\s*100%/);
    assert.match(terminalNodeRule, /flex:\s*1\s+1\s+auto/);
});

test('workflow nodes use endpoint and feature color groups consistently', () => {
    const css = readFixture('../../css/dashboard.css');

    const endpointRule = readCSSRule(css, '.ep-node-endpoint');
    assert.match(endpointRule, /background:\s*var\(--bg-surface\)/);

    const featureSelectors = [
        '.ep-node-cache',
        '.ep-node-auth',
        '.ep-node-guardrails',
        '.ep-node-async-audit',
        '.ep-node-async-usage'
    ];
    for (const selector of featureSelectors) {
        const rule = readCSSRule(css, selector);
        assert.match(rule, /border-color:\s*color-mix\(in srgb, var\(--accent\) 46%, var\(--border\)\)/);
        assert.match(rule, /background:\s*color-mix\(in srgb, var\(--accent\) 8%, var\(--bg-surface\)\)/);
    }

    const featureIconSelectors = [
        '.ep-node-cache .ep-node-icon',
        '.ep-node-auth .ep-node-icon',
        '.ep-node-guardrails .ep-node-icon',
        '.ep-node-async-audit .ep-node-icon',
        '.ep-node-async-usage .ep-node-icon'
    ];
    for (const selector of featureIconSelectors) {
        const rule = readCSSRule(css, selector);
        assert.match(rule, /background:\s*color-mix\(in srgb, var\(--accent\) 16%, var\(--bg\)\)/);
        assert.match(rule, /color:\s*var\(--accent\)/);
    }

    const featureLabelSelectors = [
        '.ep-node-cache .ep-node-label',
        '.ep-node-auth .ep-node-label',
        '.ep-node-guardrails .ep-node-label',
        '.ep-node-async-audit .ep-node-label',
        '.ep-node-async-usage .ep-node-label'
    ];
    for (const selector of featureLabelSelectors) {
        const rule = readCSSRule(css, selector);
        assert.match(rule, /color:\s*var\(--accent\)/);
    }

    const authSubRule = readCSSRule(css, '.ep-node-auth .ep-node-sub');
    assert.match(authSubRule, /color:\s*color-mix\(in srgb, var\(--accent\) 70%, var\(--text-muted\)\)/);
});

test('auth node uses the cache iconography in execution plan charts', () => {
    const chartTemplate = readFixture('../../../templates/execution-plan-chart.html');

    assert.match(
        chartTemplate,
        /<div class="ep-node ep-node-auth"[\s\S]*?<svg viewBox="0 0 24 24"><ellipse cx="12" cy="5" rx="9" ry="3"\/><path d="M21 12c0 1\.66-4 3-9 3s-9-1\.34-9-3"\/><path d="M3 5v14c0 1\.66 4 3 9 3s9-1\.34 9-3V5"\/><\/svg>[\s\S]*?<span class="ep-node-label">Auth<\/span>/
    );
});

test('execution plan authoring inputs expose stable accessible names', () => {
    const template = readFixture('../../../templates/index.html');

    assert.match(
        template,
        /x-model="executionPlanFilter"[^>]*aria-label="Filter workflows by scope, name, hash, or guardrail"/
    );
    assert.match(
        template,
        /x-model="step\.ref"[^>]*:aria-label="'Guardrail reference ' \+ \(index \+ 1\)"/
    );
    assert.match(
        template,
        /x-model\.number="step\.step"[^>]*:aria-label="'Guardrail step ' \+ \(index \+ 1\)"/
    );
});

test('workflow editor labels the audit toggle as Audit Logs', () => {
    const template = readFixture('../../../templates/index.html');

    assert.match(
        template,
        /x-model="executionPlanForm\.features\.audit"[\s\S]*?<span>Audit Logs<\/span>/
    );
});

test('workflow actions use New Workflow copy for open and submit buttons', () => {
    const template = readFixture('../../../templates/index.html');

    assert.match(
        template,
        /@click="openExecutionPlanCreate\(\)">New Workflow<\/button>/
    );
    assert.match(
        template,
        /class="pagination-btn pagination-btn-primary execution-plan-submit-btn"[\s\S]*executionPlanSubmitting \? executionPlanSubmittingLabel\(\) : executionPlanSubmitLabel\(\)/
    );
    assert.match(
        template,
        /execution-plan-submit-icon[\s\S]*<svg x-show="executionPlanSubmitMode\(\) === 'create'" viewBox="0 0 24 24">[\s\S]*<svg x-show="executionPlanSubmitMode\(\) === 'save'" viewBox="0 0 24 24">/
    );
});

test('workflow editor uses the shared helper disclosure instead of a title-only question mark', () => {
    const template = [
        readFixture('../../../templates/index.html'),
        readFixture('../../../templates/helper-disclosure.html')
    ].join('\n');

    assert.match(
        template,
        /{{template "helper-disclosure" "\{ heading: 'Workflow', open: false, copyId: 'workflow-help-copy'[\s\S]*Create immutable version\. Submitting activates it for the selected scope\./
    );
    assert.doesNotMatch(template, /class="execution-plan-help"/);
    assert.doesNotMatch(template, /title="Create immutable version\. Submitting activates it for the selected scope\."/);
});

test('workflow failover controls are gated by the runtime FEATURE_FALLBACK_MODE flag', () => {
    const template = readFixture('../../../templates/index.html');

    assert.match(
        template,
        /x-show="executionPlanFailoverVisible\(\)"[\s\S]*x-model="executionPlanForm\.features\.fallback"/
    );
    assert.match(
        template,
        /x-show="executionPlanFailoverVisible\(\)"[\s\S]*x-text="'Failover: ' \+ executionPlanFallbackLabel\(plan\)"/
    );
});

test('workflow feature controls and guardrail sections are gated by global runtime visibility helpers', () => {
    const template = readFixture('../../../templates/index.html');

    assert.match(
        template,
        /x-show="executionPlanCacheVisible\(\)"[\s\S]*x-model="executionPlanForm\.features\.cache"/
    );
    assert.match(
        template,
        /x-show="executionPlanAuditVisible\(\)"[\s\S]*x-model="executionPlanForm\.features\.audit"/
    );
    assert.match(
        template,
        /x-show="executionPlanUsageVisible\(\)"[\s\S]*x-model="executionPlanForm\.features\.usage"/
    );
    assert.match(
        template,
        /x-show="executionPlanGuardrailsVisible\(\)"[\s\S]*x-model="executionPlanForm\.features\.guardrails"/
    );
    assert.match(
        template,
        /<div class="execution-plan-guardrails" x-show="executionPlanGuardrailsVisible\(\)">/
    );
    assert.match(
        template,
        /<div class="execution-plan-guardrails" x-show="executionPlanGuardrailsVisible\(\)">[\s\S]*planGuardrails\(plan\)/
    );
    assert.match(
        template,
        /<div class="execution-plan-guardrail-editor" x-show="executionPlanForm\.features\.guardrails && executionPlanGuardrailsVisible\(\)">/
    );
});

test('workflow editor renders a live preview card from the draft workflow state', () => {
    const template = readFixture('../../../templates/index.html');
    const chartTemplate = readFixture('../../../templates/execution-plan-chart.html');

    assert.match(
        template,
        /<article class="execution-plan-card execution-plan-preview-card">[\s\S]*x-text="workflowDisplayName\(executionPlanPreview\(\)\)"[\s\S]*x-text="planScopeLabel\(executionPlanPreview\(\)\)"[\s\S]*x-text="'Failover: ' \+ executionPlanFallbackLabel\(executionPlanPreview\(\)\)"[\s\S]*{{template "execution-plan-chart" "executionPlanWorkflowChart\(executionPlanPreview\(\)\)"}}[\s\S]*x-show="planGuardrails\(executionPlanPreview\(\)\)\.length > 0"/
    );
    assert.match(
        chartTemplate,
        /{{define "execution-plan-chart"}}[\s\S]*<span class="ep-node-label">Auth<\/span>[\s\S]*x-text="{{\.}}\.authNodeSublabel"[\s\S]*x-show="{{\.}}\.terminalKind === 'auth'"[\s\S]*x-show="{{\.}}\.terminalKind === 'guardrails'"[\s\S]*x-show="{{\.}}\.terminalKind === 'cache'"[\s\S]*x-show="{{\.}}\.terminalKind === 'ai'"[\s\S]*x-text="{{\.}}\.aiLabel"/
    );
});

test('audit log pipeline binds cache visibility and runtime highlight classes across the full path', () => {
    const template = readExecutionPlanTemplateSource();
    const css = readFixture('../../css/dashboard.css');

    assert.match(
        template,
        /{{template "execution-plan-chart" "executionPlanAuditChart\(entry\)"}}[\s\S]*<div class="ep-terminal">[\s\S]*<div class="ep-node ep-node-cache ep-node-compact" :class="{{\.}}\.cacheNodeClass" x-show="{{\.}}\.terminalKind === 'cache'">[\s\S]*x-text="{{\.}}\.cacheStatusLabel"/
    );
    assert.match(
        template,
        /:class="{{\.}}\.authNodeClass"[\s\S]*x-show="{{\.}}\.terminalKind === 'auth'"[\s\S]*x-show="{{\.}}\.terminalKind === 'guardrails'"[\s\S]*x-show="{{\.}}\.showUsage"[\s\S]*x-show="{{\.}}\.showAudit"/
    );
    assert.match(
        template,
        /<div class="exec-pipeline" :class="\{ 'exec-pipeline-has-meta': {{\.}}\.workflowID \}">[\s\S]*<div class="exec-pipeline-meta" x-show="{{\.}}\.workflowID">[\s\S]*x-text="'id: ' \+ {{\.}}\.workflowID"/
    );
    assert.match(
        template,
        /<div class="ep-node ep-node-ai ep-node-compact" :class="{{\.}}\.aiNodeClass" x-show="{{\.}}\.terminalKind === 'ai'">[\s\S]*x-text="{{\.}}\.aiLabel"/
    );
    assert.match(
        template,
        /<div class="ep-node ep-node-endpoint" :class="{{\.}}\.responseNodeClass">[\s\S]*<div class="ep-conn ep-conn-rtl ep-conn-grow" :class="{{\.}}\.responseConnClass"><\/div>/
    );

    const aiSuccessRule = readCSSRule(css, '.ep-node-ai-success');
    assert.match(aiSuccessRule, /border-color:\s*color-mix\(in srgb, var\(--success\) 52%, var\(--border\)\)/);
    assert.match(aiSuccessRule, /background:\s*color-mix\(in srgb, var\(--success\) 9%, var\(--bg-surface\)\)/);

    const endpointSuccessRule = readCSSRule(css, '.ep-node-endpoint-success');
    assert.match(endpointSuccessRule, /border-color:\s*color-mix\(in srgb, var\(--success\) 52%, var\(--border\)\)/);
    assert.match(endpointSuccessRule, /background:\s*color-mix\(in srgb, var\(--success\) 9%, var\(--bg-surface\)\)/);

    const semanticCacheRule = readCSSRule(css, '.ep-node-cache-semantic');
    assert.match(semanticCacheRule, /border-color:\s*color-mix\(in srgb, var\(--success\) 52%, var\(--border\)\)/);
    assert.match(semanticCacheRule, /background:\s*color-mix\(in srgb, var\(--success\) 9%, var\(--bg-surface\)\)/);

    const pipelineRule = readCSSRule(css, '.exec-pipeline');
    assert.match(pipelineRule, /position:\s*relative/);

    const metaRule = readCSSRule(css, '.exec-pipeline-meta');
    assert.match(metaRule, /position:\s*absolute/);
    assert.match(metaRule, /top:\s*12px/);
    assert.match(metaRule, /right:\s*14px/);

    const authSuccessRule = readCSSRule(css, '.ep-node-auth-success');
    assert.match(authSuccessRule, /border-color:\s*color-mix\(in srgb, var\(--success\) 52%, var\(--border\)\)/);
    assert.match(authSuccessRule, /background:\s*color-mix\(in srgb, var\(--success\) 9%, var\(--bg-surface\)\)/);

    const authErrorRule = readCSSRule(css, '.ep-node-auth-error');
    assert.match(authErrorRule, /border-color:\s*color-mix\(in srgb, var\(--danger\) 52%, var\(--border\)\)/);
    assert.match(authErrorRule, /background:\s*color-mix\(in srgb, var\(--danger\) 9%, var\(--bg-surface\)\)/);

    const skippedAiRule = readCSSRule(css, '.ep-node-ai-skipped');
    assert.match(skippedAiRule, /position:\s*relative/);
    assert.match(skippedAiRule, /opacity:\s*0\.28/);
});

test('execution plan card actions expose plan-specific accessible names', () => {
    const template = readFixture('../../../templates/index.html');

    assert.match(
        template,
        /class="table-action-btn table-action-btn-danger"[\s\S]*?:aria-label="'Deactivate workflow ' \+ workflowDisplayName\(plan\)"/
    );
    assert.match(
        template,
        /class="table-action-btn"[^>]*:aria-label="'Edit workflow ' \+ workflowDisplayName\(plan\)"/
    );
});

test('guardrails node only renders a sublabel when step detail exists', () => {
    const template = readExecutionPlanTemplateSource();

    assert.match(
        template,
        /<span class="ep-node-label">Guardrails<\/span>\s*<span class="ep-node-sub" x-show="{{\.}}\.guardrailLabel" x-text="{{\.}}\.guardrailLabel"><\/span>/
    );
});

test('execution pipeline icons use lowercase currentcolor keyword', () => {
    const css = readFixture('../../css/dashboard.css');
    const iconRule = readCSSRule(css, '.ep-node-icon svg');

    assert.match(iconRule, /stroke:\s*currentcolor;/);
});

test('exec pipeline has bottom spacing so adjacent cards do not touch it', () => {
    const css = readFixture('../../css/dashboard.css');
    const pipelineRule = readCSSRule(css, '.exec-pipeline');

    assert.match(pipelineRule, /margin-bottom:\s*\d+px/);
});

test('execution pipeline uses var(--radius) for chart-local corners', () => {
    const css = readFixture('../../css/dashboard.css');

    const radiusSelectors = [
        '.exec-pipeline',
        '.ep-node',
        '.ep-node-icon',
        '.ep-node-badge',
        '.ep-node-endpoint',
        '.ep-node-icon-endpoint',
        '.ep-node-ai',
        '.ep-node-async',
        '.ep-node-async .ep-node-icon'
    ];

    for (const selector of radiusSelectors) {
        const rule = readCSSRule(css, selector);
        assert.match(rule, /border-radius:\s*var\(--radius\)/);
    }
});

test('AI node renders as a text-only card without an icon', () => {
    const template = readExecutionPlanTemplateSource();
    const css = readFixture('../../css/dashboard.css');

    assert.doesNotMatch(template, /class="ep-node ep-node-ai[^"]*"[^>]*>\s*<div class="ep-node-icon">/);
    assert.doesNotMatch(css, /\.ep-node-ai \.ep-node-icon\s*\{/);
    assert.doesNotMatch(css, /\.ep-terminal\s+\.ep-node-ai\s*\{/);
});

test('workflow cards reuse the extracted execution plan chart template', () => {
    const template = readFixture('../../../templates/index.html');

    assert.match(template, /{{template "execution-plan-chart" "executionPlanWorkflowChart\(executionPlanPreview\(\)\)"}}/);
    assert.match(template, /{{template "execution-plan-chart" "executionPlanWorkflowChart\(plan\)"}}/);
    assert.match(template, /{{template "execution-plan-chart" "executionPlanAuditChart\(entry\)"}}/);
});

test('endpoint pills use dedicated flush-left icons and tighter right padding', () => {
    const template = readExecutionPlanTemplateSource();
    const css = readFixture('../../css/dashboard.css');

    assert.match(template, /class="ep-node-icon ep-node-icon-endpoint"/);

    const endpointRule = readCSSRule(css, '.ep-node-endpoint');
    assert.match(endpointRule, /padding:\s*10px 14px/);

    const endpointIconRule = readCSSRule(css, '.ep-node-icon-endpoint');
    assert.match(endpointIconRule, /width:\s*auto/);
    assert.match(endpointIconRule, /height:\s*auto/);
    assert.match(endpointIconRule, /justify-content:\s*flex-start/);
    assert.match(endpointIconRule, /padding:\s*0/);
});
