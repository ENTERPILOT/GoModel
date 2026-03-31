const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

function readFixture(relativePath) {
    return fs.readFileSync(path.join(__dirname, relativePath), 'utf8');
}

function readCSSRule(source, selector) {
    const escapedSelector = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    const match = source.match(new RegExp(`${escapedSelector}\\s*\\{([\\s\\S]*?)\\s*\\}`, 'm'));
    assert.ok(match, `Expected CSS rule for ${selector}`);
    return match[1];
}

test('readCSSRule matches rules with CRLF endings and indented closing braces', () => {
    const css = '.content {\r\n    width: 100%;\r\n    max-width: 1200px;\r\n    }\r\n';

    const rule = readCSSRule(css, '.content');

    assert.match(rule, /width:\s*100%/);
    assert.match(rule, /max-width:\s*1200px/);
});

test('sidebar and main content share the flex layout without manual content offsets', () => {
    const template = readFixture('../../../templates/layout.html');
    const css = readFixture('../../css/dashboard.css');

    assert.match(template, /<aside class="sidebar"[\s\S]*<div class="sidebar-toggle"[\s\S]*<main class="content"/);
    assert.doesNotMatch(template, /content-collapsed/);

    const sidebarRule = readCSSRule(css, '.sidebar');
    assert.match(sidebarRule, /flex:\s*0 0 var\(--sidebar-width\)/);
    assert.match(sidebarRule, /position:\s*sticky/);
    assert.match(sidebarRule, /max-height:\s*100vh/);
    assert.match(sidebarRule, /overflow-y:\s*auto/);
    assert.doesNotMatch(sidebarRule, /position:\s*fixed/);
    assert.doesNotMatch(sidebarRule, /(^|\n)\s*height:\s*100vh/);

    const toggleRule = readCSSRule(css, '.sidebar-toggle');
    assert.match(toggleRule, /flex:\s*0 0 6px/);
    assert.match(toggleRule, /position:\s*sticky/);
    assert.match(toggleRule, /height:\s*100vh/);
    assert.doesNotMatch(toggleRule, /left:\s*var\(--sidebar-width\)/);

    const contentRule = readCSSRule(css, '.content');
    assert.match(contentRule, /flex:\s*1 1 0/);
    assert.match(contentRule, /width:\s*100%/);
    assert.match(contentRule, /max-width:\s*1200px/);
    assert.match(contentRule, /margin:\s*0 auto/);
    assert.doesNotMatch(contentRule, /margin-left:\s*max\(/);

    const collapsedSidebarRule = readCSSRule(css, '.sidebar.sidebar-collapsed');
    assert.match(collapsedSidebarRule, /flex-basis:\s*60px/);
});

test('dashboard layout pins Chart.js to 4.5.0', () => {
    const template = readFixture('../../../templates/layout.html');

    assert.match(
        template,
        /<script src="https:\/\/cdn\.jsdelivr\.net\/npm\/chart\.js@4\.5\.0\/dist\/chart\.umd\.min\.js" integrity="sha384-XcdcwHqIPULERb2yDEM4R0XaQKU3YnDsrTmjACBZyfdVVqjh6xQ4\/DCMd7XLcA6Y" crossorigin="anonymous"><\/script>/
    );
    assert.match(
        template,
        /<script defer src="https:\/\/cdn\.jsdelivr\.net\/npm\/alpinejs@3\.15\.8\/dist\/cdn\.min\.js" integrity="sha384-LXWjKwDZz29o7TduNe\+r\/UxaolHh5FsSvy2W7bDHSZ8jJeGgDeuNnsDNHoxpSgDi" crossorigin="anonymous"><\/script>/
    );
    assert.match(
        template,
        /<script src="https:\/\/unpkg\.com\/htmx\.org@2\.0\.8\/dist\/htmx\.min\.js" integrity="sha384-\/TgkGk7p307TH7EXJDuUlgG3Ce1UVolAOFopFekQkkXihi5u\/6OCvVKyz1W\+idaz" crossorigin="anonymous"><\/script>/
    );
});

test('dashboard pages reuse a shared auth banner template', () => {
    const indexTemplate = readFixture('../../../templates/index.html');
    const authBannerTemplate = readFixture('../../../templates/auth-banner.html');

    assert.match(
        authBannerTemplate,
        /{{define "auth-banner"}}[\s\S]*x-show="authError"[\s\S]*Authentication required\. Enter your API key in the sidebar to view data\.[\s\S]*{{end}}/
    );

    const authBannerCalls = indexTemplate.match(/{{template "auth-banner" \.}}/g) || [];
    assert.equal(authBannerCalls.length, 6);
    assert.doesNotMatch(
        indexTemplate,
        /<div class="alert alert-warning" x-show="authError">[\s\S]*Authentication required\. Enter your API key in the sidebar to view data\.[\s\S]*<\/div>/
    );
});

test('auth key expirations render as a UTC date with the full UTC timestamp in the hover title', () => {
    const indexTemplate = readFixture('../../../templates/index.html');

    assert.match(indexTemplate, /x-text="key\.expires_at \? formatDateUTC\(key\.expires_at\) : '\\u2014'"/);
    assert.match(indexTemplate, /:title="key\.expires_at \? formatTimestampUTC\(key\.expires_at\) : ''"/);
    assert.match(indexTemplate, /:disabled="authKeyFormSubmitting"/);
    assert.match(indexTemplate, /@click="if \(!authKeyFormSubmitting\) openAuthKeyForm\(\)"/);
    assert.match(indexTemplate, /x-show="authKeys\.length === 0 && !authKeysLoading && !authError && !authKeyError && authKeysAvailable"/);
});

test('usage and audit pages reuse a shared pagination template', () => {
    const indexTemplate = readFixture('../../../templates/index.html');
    const paginationTemplate = readFixture('../../../templates/pagination.html');

    assert.match(
        paginationTemplate,
        /{{define "pagination"}}[\s\S]*x-show="{{\.}}\.total > 0"[\s\S]*type="button"[\s\S]*@click="{{\.}}PrevPage\(\)"[\s\S]*type="button"[\s\S]*@click="{{\.}}NextPage\(\)"[\s\S]*{{end}}/
    );
    assert.match(indexTemplate, /{{template "pagination" "usageLog"}}/);
    assert.match(indexTemplate, /{{template "pagination" "auditLog"}}/);
    assert.doesNotMatch(
        indexTemplate,
        /<div class="pagination" x-show="usageLog\.total > 0">[\s\S]*usageLogPrevPage\(\)[\s\S]*<\/div>/
    );
    assert.doesNotMatch(
        indexTemplate,
        /<div class="pagination" x-show="auditLog\.total > 0">[\s\S]*auditLogPrevPage\(\)[\s\S]*<\/div>/
    );
});

test('audit request and response sections reuse a shared audit pane template', () => {
    const indexTemplate = readFixture('../../../templates/index.html');
    const auditPaneTemplate = readFixture('../../../templates/audit-pane.html');

    assert.match(
        auditPaneTemplate,
        /{{define "audit-pane"}}[\s\S]*x-text="{{\.}}\.title"[\s\S]*type="button"[\s\S]*copyAuditJSON\({{\.\}}\.copyBody, \$event\)[\s\S]*x-text="formatJSON\({{\.\}}\.headers\)"[\s\S]*renderBodyWithConversationHighlights\({{\.\}}\.entry, {{\.}}\.body\)[\s\S]*x-text="{{\.}}\.emptyMessage"[\s\S]*x-text="{{\.}}\.tooLargeMessage"[\s\S]*{{end}}/
    );
    assert.match(indexTemplate, /{{template "audit-pane" "auditRequestPane\(entry\)"}}/);
    assert.match(indexTemplate, /{{template "audit-pane" "auditResponsePane\(entry\)"}}/);
    assert.doesNotMatch(indexTemplate, /<section class="audit-pane">[\s\S]*<h4>Request<\/h4>/);
    assert.doesNotMatch(indexTemplate, /<section class="audit-pane">[\s\S]*<h4>Response<\/h4>/);
});
