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
    assert.match(template, /x-ref="timezoneOverrideSelect"/);
    assert.match(template, /x-model="timezoneOverride"/);
    assert.match(template, /x-effect="timezoneOptions\.length; timezoneOverride; \$nextTick\(\(\) => syncTimezoneOverrideSelectValue\(\)\)"/);
    assert.match(template, /<option value=""/);
    assert.match(template, /:selected="!timezoneOverride"/);
    assert.match(template, /<option :value="timeZone\.value"/);
    assert.match(template, /:selected="timeZone\.value === timezoneOverride"/);
    assert.match(template, /<div class="settings-panel-header" x-data="\{ timezoneHelpOpen: false \}">/);
    assert.match(template, /class="timezone-help-toggle"/);
    assert.match(template, /@click="timezoneHelpOpen = !timezoneHelpOpen"/);
    assert.match(template, /class="timezone-help-toggle-icon"/);
    assert.match(template, /x-text="timezoneHelpOpen \? '−' : '\?'"/);
    assert.match(template, /id="timezone-help-copy"/);
    assert.match(template, /x-show="timezoneHelpOpen"/);
    assert.match(template, /x-transition\.opacity\.duration\.200ms/);
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
});
