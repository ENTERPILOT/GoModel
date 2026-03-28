const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

function readFixture(relativePath) {
    return fs.readFileSync(path.join(__dirname, relativePath), 'utf8');
}

function readCSSRule(source, selector) {
    const escapedSelector = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    const match = source.match(new RegExp(`${escapedSelector}\\s*\\{([\\s\\S]*?)\\n\\}`, 'm'));
    assert.ok(match, `Expected CSS rule for ${selector}`);
    return match[1];
}

test('async pipeline branch spans full width and keeps the turn inline', () => {
    const template = readFixture('../../../templates/index.html');
    const css = readFixture('../../css/dashboard.css');

    assert.match(
        template,
        /<div class="ep-async-section"[\s\S]*?<div class="ep-async-row">[\s\S]*?<\/div>\s*<div class="ep-async-turn"><\/div>/
    );

    const asyncSectionRule = readCSSRule(css, '.ep-async-section');
    assert.match(asyncSectionRule, /width:\s*100%/);
    assert.doesNotMatch(asyncSectionRule, /flex-direction:\s*column/);
    assert.match(asyncSectionRule, /align-items:\s*center/);
    assert.doesNotMatch(asyncSectionRule, /margin-top:\s*[1-9]/);

    const asyncTurnRule = readCSSRule(css, '.ep-async-turn');
    assert.match(asyncTurnRule, /width:\s*\d/);
    assert.match(asyncTurnRule, /height:\s*2px/);
    assert.doesNotMatch(asyncTurnRule, /border-bottom:/);
    assert.doesNotMatch(asyncTurnRule, /border-right:/);

    const asyncRowRule = readCSSRule(css, '.ep-async-row');
    assert.match(asyncRowRule, /display:\s*flex/);
    assert.match(asyncRowRule, /margin-right:\s*7px/);

    const asyncTurnVerticalRule = readCSSRule(css, '.ep-async-turn::after');
    assert.match(asyncTurnVerticalRule, /border-right:/);
    assert.match(asyncTurnVerticalRule, /bottom:\s*1px/);
    assert.doesNotMatch(asyncTurnVerticalRule, /transform:/);
});

test('async label stays inline on the right side of the branch', () => {
    const template = readFixture('../../../templates/index.html');
    const css = readFixture('../../css/dashboard.css');

    assert.match(
        template,
        /<div class="ep-async-row">[\s\S]*ep-node-async-usage[\s\S]*ep-conn-async[\s\S]*ep-node-async-audit[\s\S]*<\/div>\s*<div class="ep-async-turn"><\/div>\s*<span class="ep-async-label">Async<\/span>/
    );

    const asyncLabelRule = readCSSRule(css, '.ep-async-label');
    assert.doesNotMatch(asyncLabelRule, /position:\s*absolute/);
});

test('workflow nodes use endpoint and feature color groups consistently', () => {
    const css = readFixture('../../css/dashboard.css');

    const endpointRule = readCSSRule(css, '.ep-node-endpoint');
    assert.match(endpointRule, /background:\s*var\(--bg-surface\)/);

    const featureSelectors = [
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
        '.ep-node-guardrails .ep-node-label',
        '.ep-node-async-audit .ep-node-label',
        '.ep-node-async-usage .ep-node-label'
    ];
    for (const selector of featureLabelSelectors) {
        const rule = readCSSRule(css, selector);
        assert.match(rule, /color:\s*var\(--accent\)/);
    }
});

test('execution plan authoring inputs expose stable accessible names', () => {
    const template = readFixture('../../../templates/index.html');

    assert.match(
        template,
        /x-model="executionPlanFilter"[^>]*aria-label="Filter workflows by scope, name, hash, or guardrail"/
    );
    assert.match(
        template,
        /x-model="step\.ref"[^>]*aria-label="Guardrail reference"/
    );
    assert.match(
        template,
        /x-model\.number="step\.step"[^>]*aria-label="Guardrail step"/
    );
});

test('guardrails node only renders a sublabel when step detail exists', () => {
    const template = readFixture('../../../templates/index.html');

    assert.match(
        template,
        /<span class="ep-node-label">Guardrails<\/span>\s*<span class="ep-node-sub" x-show="epGuardrailLabel\(plan\)" x-text="epGuardrailLabel\(plan\)"><\/span>/
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
    const template = readFixture('../../../templates/index.html');
    const css = readFixture('../../css/dashboard.css');

    assert.doesNotMatch(template, /class="ep-node ep-node-ai[^"]*"[^>]*>\s*<div class="ep-node-icon">/);
    assert.doesNotMatch(css, /\.ep-node-ai \.ep-node-icon\s*\{/);
});

test('endpoint pills use dedicated flush-left icons and tighter right padding', () => {
    const template = readFixture('../../../templates/index.html');
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
