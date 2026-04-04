const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

function readFixture(relativePath) {
    return fs.readFileSync(path.join(__dirname, relativePath), 'utf8');
}

function readHelperDisclosureTemplateSource() {
    return [
        readFixture('../../../templates/index.html'),
        readFixture('../../../templates/helper-disclosure.html')
    ].join('\n');
}

function readCSSRule(source, selector) {
    const escapedSelector = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    const match = source.match(new RegExp(`${escapedSelector}\\s*\\{([\\s\\S]*?)\\n\\}`, 'm'));
    assert.ok(match, `Expected CSS rule for ${selector}`);
    return match[1];
}

test('dashboard layout loads the timezone module before the main bootstrap', () => {
    const layout = readFixture('../../../templates/layout.html');

    assert.match(layout, /<script src="\/admin\/static\/js\/modules\/timezone\.js"><\/script>[\s\S]*<script src="\/admin\/static\/js\/dashboard\.js"><\/script>/);
});

test('dashboard templates expose a settings page and timezone context in activity and log timestamps', () => {
    const template = readHelperDisclosureTemplateSource();
    const helperTemplate = readFixture('../../../templates/helper-disclosure.html');
    const css = readFixture('../../css/dashboard.css');

    assert.match(template, /<div x-show="page==='settings'">[\s\S]*<h2>Settings<\/h2>/);
    assert.match(template, /@click="navigateSettings\('general'\)"/);
    assert.doesNotMatch(template, /@click="navigateSettings\('guardrails'\)"/);
    assert.match(template, /x-ref="timezoneOverrideSelect"/);
    assert.match(template, /x-model="timezoneOverride"/);
    assert.match(template, /x-effect="timezoneOptions\.length; timezoneOverride; \$nextTick\(\(\) => syncTimezoneOverrideSelectValue\(\)\)"/);
    assert.match(template, /<option value=""/);
    assert.match(template, /:selected="!timezoneOverride"/);
    assert.match(template, /<option :value="timeZone\.value"/);
    assert.match(template, /:selected="timeZone\.value === timezoneOverride"/);
    assert.match(template, /{{template "helper-disclosure" "\{ heading: 'Timezone', open: false, copyId: 'timezone-help-copy'/);
    assert.match(template, /class="inline-help-toggle"/);
    assert.match(template, /class="inline-help-toggle-icon"/);
    assert.match(template, /<span class="inline-help-toggle-icon"[^>]*>\?<\/span>/);
    assert.match(template, /:aria-label="open \? hideLabel : showLabel"/);
    assert.match(template, /:aria-controls="copyId"/);
    assert.match(template, /:id="copyId"/);
    assert.match(template, /x-show="open"/);
    assert.match(template, /x-transition\.opacity\.duration\.200ms/);
    assert.doesNotMatch(helperTemplate, /:title=/);
    assert.match(template, /Day-based analytics, charts, and date filters use your effective timezone\. Usage and audit logs keep UTC in the hover title while rendering row timestamps in your effective timezone\./);
    assert.doesNotMatch(template, /Detected: /);
    assert.doesNotMatch(template, /Effective: /);
    assert.doesNotMatch(template, /Mode: /);
    assert.match(template, /x-text="calendarTimeZoneText\(\)"/);
    assert.match(template, /class="mono usage-ts"/);
    assert.match(template, /x-text="formatTimestamp\(entry\.timestamp\)"/);
    assert.match(template, /:title="timestampTitle\(entry\.timestamp\)"/);
    assert.match(template, /class="audit-entry-meta"/);
    assert.match(template, /<button(?=[^>]*class="audit-conversation-trigger")(?=[^>]*type="button")[^>]*>/);

    const toggleRule = readCSSRule(css, '.inline-help-toggle');
    assert.match(toggleRule, /width:\s*16px/);
    assert.match(toggleRule, /height:\s*16px/);
    assert.match(toggleRule, /position:\s*relative/);
    assert.match(toggleRule, /border-radius:\s*4px/);
    assert.match(toggleRule, /background:\s*transparent/);
    assert.doesNotMatch(toggleRule, /box-shadow:/);

    const toggleHitAreaRule = readCSSRule(css, '.inline-help-toggle::before');
    assert.match(toggleHitAreaRule, /content:\s*""/);
    assert.match(toggleHitAreaRule, /position:\s*absolute/);
    assert.match(toggleHitAreaRule, /width:\s*32px/);
    assert.match(toggleHitAreaRule, /height:\s*32px/);
    assert.match(toggleHitAreaRule, /top:\s*50%/);
    assert.match(toggleHitAreaRule, /left:\s*50%/);
    assert.match(toggleHitAreaRule, /transform:\s*translate\(-50%,\s*-50%\)/);
    assert.match(toggleHitAreaRule, /background:\s*transparent/);
    assert.match(toggleHitAreaRule, /pointer-events:\s*auto/);

    const toggleHoverRule = readCSSRule(css, '.inline-help-toggle:hover');
    assert.match(toggleHoverRule, /background:\s*transparent/);
    assert.doesNotMatch(toggleHoverRule, /transform:/);

    const toggleOpenRule = readCSSRule(css, '.inline-help-toggle.is-open');
    assert.match(toggleOpenRule, /background:\s*transparent/);

    const iconRule = readCSSRule(css, '.inline-help-toggle-icon');
    assert.match(iconRule, /transform:\s*rotate\(0deg\)/);

    const iconOpenRule = readCSSRule(css, '.inline-help-toggle.is-open .inline-help-toggle-icon');
    assert.match(iconOpenRule, /transform:\s*rotate\(540deg\)/);

    const copyRule = readCSSRule(css, '.inline-help-copy');
    assert.doesNotMatch(copyRule, /border:/);
    assert.doesNotMatch(copyRule, /background:/);
    assert.doesNotMatch(copyRule, /padding:/);
});

test('guardrails authoring moved to a top-level page while settings keeps the general switch', () => {
    const template = readHelperDisclosureTemplateSource();

    assert.match(template, /<div class="settings-subnav">[\s\S]*class="settings-subnav-btn active"[\s\S]*>General<\/button>/);
    assert.match(template, /<div x-show="page==='guardrails'">[\s\S]*<h2>Guardrails<\/h2>/);
    assert.match(template, /Guardrail Library/);
    assert.match(template, /x-ref="guardrailTypeSelect"/);
    assert.match(template, /x-model="guardrailForm\.type"/);
    assert.match(template, /x-effect="guardrailTypes\.length; guardrailForm\.type; \$nextTick\(\(\) => syncGuardrailTypeSelectValue\(\)\)"/);
    assert.match(template, /x-model="guardrailForm\.user_path"[^>]*aria-label="Guardrail user path"/);
    assert.match(template, /Reserved for future UI visibility scoping\. It does not affect runtime execution yet\./);
});
