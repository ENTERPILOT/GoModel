const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

function readFixture(relativePath) {
    return fs.readFileSync(path.join(__dirname, relativePath), 'utf8');
}

test('dashboard layout loads the timezone module before the main bootstrap', () => {
    const layout = readFixture('../../../templates/layout.html');

    assert.match(layout, /<script src="\/admin\/static\/js\/modules\/timezone\.js"><\/script>[\s\S]*<script src="\/admin\/static\/js\/dashboard\.js"><\/script>/);
});

test('dashboard templates expose a settings page and timezone context in activity and log timestamps', () => {
    const template = readFixture('../../../templates/index.html');

    assert.match(template, /<div x-show="page==='settings'">[\s\S]*<h2>User Settings<\/h2>/);
    assert.match(template, /x-ref="timezoneOverrideSelect"[\s\S]*x-model="timezoneOverride"[\s\S]*x-effect="timezoneOptions\.length; timezoneOverride; \$nextTick\(\(\) => syncTimezoneOverrideSelectValue\(\)\)"/);
    assert.match(template, /<option value=""[\s\S]*:selected="!timezoneOverride"/);
    assert.match(template, /<option :value="timeZone\.value"[\s\S]*:selected="timeZone\.value === timezoneOverride"/);
    assert.match(template, /<div class="settings-panel-header" x-data="\{ timezoneHelpOpen: false \}">/);
    assert.match(template, /<button type="button"[\s\S]*class="timezone-help-toggle"[\s\S]*@click="timezoneHelpOpen = !timezoneHelpOpen"/);
    assert.match(template, /<span class="timezone-help-toggle-icon" x-text="timezoneHelpOpen \? '−' : '\?'"><\/span>/);
    assert.match(template, /<p id="timezone-help-copy"[\s\S]*class="settings-panel-copy settings-panel-copy-collapsible"[\s\S]*x-show="timezoneHelpOpen"[\s\S]*x-transition\.opacity\.duration\.200ms[\s\S]*Day-based analytics, charts, and date filters use your effective timezone\./);
    assert.doesNotMatch(template, /Detected: /);
    assert.doesNotMatch(template, /Effective: /);
    assert.doesNotMatch(template, /Mode: /);
    assert.match(template, /x-text="calendarTimeZoneText\(\)"/);
    assert.match(template, /usage-ts[^>]*x-text="formatTimestamp\(entry\.timestamp\)"[^>]*:title="timestampTitle\(entry\.timestamp\)"/);
    assert.match(template, /audit-entry-meta[\s\S]*x-text="formatTimestamp\(entry\.timestamp\)"[\s\S]*:title="timestampTitle\(entry\.timestamp\)"/);
    assert.match(template, /<button(?=[^>]*class="audit-conversation-trigger")(?=[^>]*type="button")[^>]*>/);
});
