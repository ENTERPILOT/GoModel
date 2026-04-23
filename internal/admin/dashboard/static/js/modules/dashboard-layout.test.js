const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

function readFixture(relativePath) {
  return fs.readFileSync(path.join(__dirname, relativePath), "utf8");
}

function readDashboardShellTemplate() {
  const layout = readFixture("../../../templates/layout.html");
  const sidebar = readFixture("../../../templates/sidebar.html");

  return layout.replace('{{template "sidebar" .}}', sidebar);
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

function readCSSRule(source, selector) {
  const escapedSelector = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = source.match(
    new RegExp(`${escapedSelector}\\s*\\{([\\s\\S]*?)\\s*\\}`, "m"),
  );
  assert.ok(match, `Expected CSS rule for ${selector}`);
  return match[1];
}

test("readCSSRule matches rules with CRLF endings and indented closing braces", () => {
  const css =
    ".content {\r\n    width: 100%;\r\n    max-width: 1200px;\r\n    }\r\n";

  const rule = readCSSRule(css, ".content");

  assert.match(rule, /width:\s*100%/);
  assert.match(rule, /max-width:\s*1200px/);
});

test("sidebar and main content share the flex layout without manual content offsets", () => {
  const template = readDashboardShellTemplate();
  const css = readFixture("../../css/dashboard.css");

  assert.match(
    readFixture("../../../templates/layout.html"),
    /{{template "sidebar" \.}}/,
  );
  assert.match(
    template,
    /<aside class="sidebar"[\s\S]*<button type="button"[\s\S]*class="sidebar-toggle"[\s\S]*<main class="content"/,
  );
  assert.doesNotMatch(template, /content-collapsed/);
  assert.match(
    template,
    /href="\/admin\/dashboard\/overview"[\s\S]*<span>Overview<\/span>[\s\S]*href="\/admin\/dashboard\/models"[\s\S]*<span>Models<\/span>[\s\S]*href="\/admin\/dashboard\/audit-logs"[\s\S]*<span>Audit Logs<\/span>[\s\S]*href="\/admin\/dashboard\/usage"[\s\S]*<span>Usage<\/span>[\s\S]*href="\/admin\/dashboard\/auth-keys"[\s\S]*<span>API Keys<\/span>[\s\S]*href="\/admin\/dashboard\/workflows"[\s\S]*<span>Workflows<\/span>[\s\S]*href="\/admin\/dashboard\/guardrails"[\s\S]*x-show="guardrailsPageVisible\(\)"[\s\S]*<span>Guardrails \(experimental\)<\/span>[\s\S]*href="\/admin\/dashboard\/settings"[\s\S]*<span>Settings<\/span>/,
  );

  const sidebarRule = readCSSRule(css, ".sidebar");
  assert.match(sidebarRule, /flex:\s*0 0 var\(--sidebar-width\)/);
  assert.match(sidebarRule, /position:\s*sticky/);
  assert.match(sidebarRule, /max-height:\s*100vh/);
  assert.match(sidebarRule, /overflow-y:\s*auto/);
  assert.doesNotMatch(sidebarRule, /position:\s*fixed/);
  assert.doesNotMatch(sidebarRule, /(^|\n)\s*height:\s*100vh/);

  const toggleRule = readCSSRule(css, ".sidebar-toggle");
  assert.match(toggleRule, /flex:\s*0 0 6px/);
  assert.match(toggleRule, /position:\s*sticky/);
  assert.match(toggleRule, /height:\s*100vh/);
  assert.doesNotMatch(toggleRule, /left:\s*var\(--sidebar-width\)/);

  const contentRule = readCSSRule(css, ".content");
  assert.match(contentRule, /flex:\s*1 1 0/);
  assert.match(contentRule, /width:\s*100%/);
  assert.match(contentRule, /max-width:\s*1200px/);
  assert.match(contentRule, /margin:\s*0 auto/);
  assert.doesNotMatch(contentRule, /margin-left:\s*max\(/);

  const collapsedSidebarRule = readCSSRule(css, ".sidebar.sidebar-collapsed");
  assert.match(collapsedSidebarRule, /flex-basis:\s*60px/);
});

test("sidebar theme controls and collapse handle stay keyboard accessible", () => {
  const template = readDashboardShellTemplate();

  assert.match(
    template,
    /class="theme-btn"[\s\S]*@click="setTheme\('light'\)"[\s\S]*title="Light theme"[\s\S]*aria-label="Light theme"/,
  );
  assert.match(
    template,
    /class="theme-btn"[\s\S]*@click="setTheme\('system'\)"[\s\S]*title="System theme"[\s\S]*aria-label="System theme"/,
  );
  assert.match(
    template,
    /class="theme-btn"[\s\S]*@click="setTheme\('dark'\)"[\s\S]*title="Dark theme"[\s\S]*aria-label="Dark theme"/,
  );
  assert.match(
    template,
    /class="theme-toggle-mobile"[\s\S]*@click="toggleTheme\(\)"[\s\S]*:title="theme === 'light' \? 'Light theme' : theme === 'dark' \? 'Dark theme' : 'System theme'"[\s\S]*:aria-label="theme === 'light' \? 'Light theme' : theme === 'dark' \? 'Dark theme' : 'System theme'"/,
  );
  assert.match(
    template,
    /<button type="button"[\s\S]*class="sidebar-toggle"[\s\S]*@click="toggleSidebar\(\)"[\s\S]*:aria-label="sidebarCollapsed \? 'Expand sidebar' : 'Collapse sidebar'"[\s\S]*:aria-expanded="sidebarCollapsed \? 'false' : 'true'"/,
  );
  assert.doesNotMatch(template, /tabindex="-1"/);
  assert.doesNotMatch(template, /@mousedown="toggleSidebar\(\)"/);
});

test("mono utility only sets the font family and font-size-md carries the 13px size", () => {
  const css = readFixture("../../css/dashboard.css");

  const monoRule = readCSSRule(css, ".mono");
  assert.match(
    monoRule,
    /font-family:\s*'SF Mono', Menlo, Consolas, monospace/,
  );
  assert.doesNotMatch(monoRule, /font-size:/);

  const fontSizeMdRule = readCSSRule(css, ".font-size-md");
  assert.match(fontSizeMdRule, /font-size:\s*13px/);
});

test("dashboard layout pins Chart.js to 4.5.0 and avoids unused htmx", () => {
  const template = readFixture("../../../templates/layout.html");

  assert.match(
    template,
    /<link rel="stylesheet" href="{{assetURL "css\/dashboard\.css"}}">/,
  );
  assert.match(
    template,
    /<script src="https:\/\/cdn\.jsdelivr\.net\/npm\/chart\.js@4\.5\.0\/dist\/chart\.umd\.min\.js" integrity="sha384-XcdcwHqIPULERb2yDEM4R0XaQKU3YnDsrTmjACBZyfdVVqjh6xQ4\/DCMd7XLcA6Y" crossorigin="anonymous"><\/script>/,
  );
  assert.match(
    template,
    /<script defer src="https:\/\/cdn\.jsdelivr\.net\/npm\/alpinejs@3\.15\.8\/dist\/cdn\.min\.js" integrity="sha384-LXWjKwDZz29o7TduNe\+r\/UxaolHh5FsSvy2W7bDHSZ8jJeGgDeuNnsDNHoxpSgDi" crossorigin="anonymous"><\/script>/,
  );
  assert.match(
    template,
    /<script src="https:\/\/cdn\.jsdelivr\.net\/npm\/lucide@0\.577\.0\/dist\/umd\/lucide\.min\.js" integrity="sha384-orgVf2eX2\+m1zKAOIi09hD0W6GtVhoOUmqDK\+sysYB2JTZ4vS86j4jm\+X7a4Nnei" crossorigin="anonymous"><\/script>/,
  );
  assert.doesNotMatch(template, /htmx/i);
  assert.match(
    template,
    /<script src="\/admin\/static\/js\/modules\/conversation-helpers\.js"><\/script>[\s\S]*<script src="\/admin\/static\/js\/modules\/icons\.js"><\/script>[\s\S]*<script src="\/admin\/static\/js\/modules\/clipboard\.js"><\/script>[\s\S]*<script src="\/admin\/static\/js\/modules\/providers\.js"><\/script>[\s\S]*<script src="\/admin\/static\/js\/modules\/audit-list\.js"><\/script>[\s\S]*<script src="\/admin\/static\/js\/modules\/auth-keys\.js"><\/script>[\s\S]*<script src="\/admin\/static\/js\/modules\/guardrails\.js"><\/script>/,
  );
});

test("dashboard chrome uses Lucide icons for stable navigation and auth controls", () => {
  const template = readDashboardShellTemplate();
  const css = readFixture("../../css/dashboard.css");
  const iconsModule = readFixture("icons.js");

  assert.match(
    template,
    /data-lucide="layout-dashboard" class="nav-icon"[\s\S]*<span>Overview<\/span>/,
  );
  assert.match(
    template,
    /data-lucide="box" class="nav-icon"[\s\S]*<span>Models<\/span>/,
  );
  assert.match(
    template,
    /data-lucide="history" class="nav-icon"[\s\S]*<span>Audit Logs<\/span>/,
  );
  assert.match(
    template,
    /data-lucide="chart-column" class="nav-icon"[\s\S]*<span>Usage<\/span>/,
  );
  assert.match(
    template,
    /data-lucide="key-round" class="nav-icon"[\s\S]*<span>API Keys<\/span>/,
  );
  assert.match(
    template,
    /data-lucide="workflow" class="nav-icon"[\s\S]*<span>Workflows<\/span>/,
  );
  assert.match(
    template,
    /data-lucide="shield-check" class="nav-icon"[\s\S]*<span>Guardrails \(experimental\)<\/span>/,
  );
  assert.match(
    template,
    /data-lucide="settings" class="nav-icon"[\s\S]*<span>Settings<\/span>/,
  );
  assert.match(template, /data-lucide="sun" class="theme-icon"/);
  assert.match(template, /data-lucide="monitor" class="theme-icon"/);
  assert.match(template, /data-lucide="moon" class="theme-icon"/);
  assert.match(
    template,
    /data-lucide="lock-keyhole" class="api-key-open-icon"/,
  );
  assert.match(
    template,
    /data-lucide="lock-keyhole" class="auth-dialog-input-icon"/,
  );
  assert.match(template, /data-lucide="check" class="auth-dialog-submit-icon"/);

  const navIconRule = readCSSRule(css, ".nav-icon");
  assert.match(navIconRule, /width:\s*18px/);
  assert.match(navIconRule, /height:\s*18px/);
  assert.match(navIconRule, /flex:\s*0 0 18px/);

  assert.match(iconsModule, /lucide\.createIcons/);
  assert.match(iconsModule, /focusable/);
});

test("overview page shows provider status summary and per-provider cards keyed by configured provider name", () => {
  const indexTemplate = readDashboardTemplateSource();
  const css = readFixture("../../css/dashboard.css");

  assert.doesNotMatch(indexTemplate, /class="provider-status-strip"/);
  assert.match(
    indexTemplate,
    /class="card provider-status-flag" x-show="providerStatus\.summary\.total > 0"/,
  );
  assert.match(
    indexTemplate,
    /Local Cache Output Tokens[\s\S]*class="card provider-status-flag"[\s\S]*<!-- Usage Chart -->/,
  );
  assert.match(
    indexTemplate,
    /<div class="card-label">Provider Status<\/div>[\s\S]*class="card-value provider-status-value"[\s\S]*providerStatusRatioText\(\)/,
  );
  assert.match(
    indexTemplate,
    /class="provider-status-card-link"[\s\S]*x-show="providerStatusHasIssues\(\)"[\s\S]*@click="scrollToProviderStatusSection\(\)"/,
  );
  assert.doesNotMatch(
    indexTemplate,
    /Counted by configured provider name, not provider type\./,
  );
  assert.match(indexTemplate, /@click="scrollToProviderStatusSection\(\)"/);
  assert.match(
    indexTemplate,
    /id="provider-status-section" class="provider-status-section" tabindex="-1" x-show="providerStatus\.providers\.length > 0"/,
  );
  assert.match(indexTemplate, /<h3>Providers Overview<\/h3>/);
  assert.match(
    indexTemplate,
    /class="provider-status-toggle"[\s\S]*role="switch"[\s\S]*@click="toggleProviderStatusDetails\(\)"/,
  );
  assert.match(
    indexTemplate,
    /class="provider-status-name"[\s\S]*x-text="provider\.name"[\s\S]*class="provider-status-name-type"[\s\S]*providerTypeLabel\(provider\)/,
  );
  assert.doesNotMatch(indexTemplate, /class="provider-status-type"/);
  assert.match(indexTemplate, /x-text="providerLastCheckedTime\(provider\)"/);
  assert.match(indexTemplate, /:title="providerLastCheckedTitle\(provider\)"/);
  assert.match(
    indexTemplate,
    /class="provider-status-details"[\s\S]*providerStatusDetailsExpanded/,
  );

  const stripRule = readCSSRule(css, ".provider-status-flag");
  assert.match(stripRule, /grid-column:\s*span 2/);

  const linkRule = readCSSRule(css, ".provider-status-card-link");
  assert.match(linkRule, /background:\s*transparent/);
  assert.match(linkRule, /text-align:\s*left/);

  const gridRule = readCSSRule(css, ".provider-status-grid");
  assert.match(gridRule, /display:\s*grid/);
  assert.match(
    gridRule,
    /grid-template-columns:\s*repeat\(auto-fit, minmax\(280px, 1fr\)\)/,
  );

  const toggleRule = readCSSRule(css, ".provider-status-toggle");
  assert.match(toggleRule, /display:\s*inline-flex/);
  assert.match(toggleRule, /border-radius:\s*999px/);

  const detailsRule = readCSSRule(css, ".provider-status-details");
  assert.match(detailsRule, /grid-template-rows:\s*0fr/);
  assert.match(
    detailsRule,
    /transition:\s*grid-template-rows 0\.28s ease, opacity 0\.22s ease/,
  );
});

test("dashboard pages reuse a shared auth banner template", () => {
  const indexTemplate = readDashboardTemplateSource();
  const authBannerTemplate = readFixture("../../../templates/auth-banner.html");

  assert.match(
    authBannerTemplate,
    /{{define "auth-banner"}}[\s\S]*class="alert alert-warning auth-banner"[\s\S]*x-show="authError"[\s\S]*Authentication required for dashboard data\.[\s\S]*@click="openAuthDialog\(\)"[\s\S]*Enter API key[\s\S]*{{end}}/,
  );

  const authBannerCalls =
    indexTemplate.match(/{{template "auth-banner" \.}}/g) || [];
  assert.equal(authBannerCalls.length, 8);
  assert.match(
    indexTemplate,
    /<template x-if="page==='settings'">\s*<div>[\s\S]*{{template "auth-banner" \.}}/,
  );
  assert.match(
    indexTemplate,
    /<template x-if="page==='guardrails'">\s*<div>[\s\S]*{{template "auth-banner" \.}}/,
  );
  assert.doesNotMatch(
    indexTemplate,
    /Enter your API key in the sidebar to view data/,
  );
});

test("dashboard auth uses a root-level dialog instead of a hidden sidebar input", () => {
  const template = readDashboardShellTemplate();
  const css = readFixture("../../css/dashboard.css");

  assert.doesNotMatch(template, /<input id="apiKey"/);
  assert.match(
    template,
    /class="api-key-section" x-show="needsAuth \|\| hasApiKey\(\)"[\s\S]*class="api-key-open-btn"[\s\S]*@click="openAuthDialog\(\)"[\s\S]*class="api-key-open-icon"[\s\S]*Change API key/,
  );
  assert.doesNotMatch(template, /class="api-key-title"/);
  assert.doesNotMatch(template, /API key set/);
  assert.doesNotMatch(template, /Admin access/);
  const backdropBlock = template.match(
    /<div class="auth-dialog-backdrop"[\s\S]*?<\/div>/,
  );
  assert.ok(backdropBlock, "Expected auth dialog backdrop block");
  assert.match(backdropBlock[0], /x-show="authDialogOpen"/);
  assert.match(backdropBlock[0], /aria-hidden="true"/);
  assert.doesNotMatch(backdropBlock[0], /@click="closeAuthDialog\(\)"/);

  const shellOpening = template.match(
    /<div class="auth-dialog-shell"[\s\S]*?<section class="auth-dialog"/,
  );
  assert.ok(shellOpening, "Expected auth dialog shell block");
  assert.match(
    shellOpening[0],
    /x-show="authDialogOpen"[\s\S]*@click="closeAuthDialog\(\)"/,
  );
  assert.match(
    template,
    /role="dialog"[\s\S]*aria-modal="true"[\s\S]*@click\.stop[\s\S]*id="authDialogApiKey"/,
  );
  assert.match(
    template,
    /class="auth-dialog-input-shell"[\s\S]*class="auth-dialog-input-icon"[\s\S]*id="authDialogApiKey"/,
  );
  assert.match(
    template,
    /x-text="needsAuth \? 'Dashboard locked' : 'Change API key'"/,
  );
  assert.match(
    template,
    /class="auth-dialog-close dialog-close-btn"[\s\S]*aria-label="Close authentication dialog"[\s\S]*{{template "x-icon"}}/,
  );
  assert.match(
    template,
    /class="pagination-btn pagination-btn-primary pagination-btn-with-icon auth-dialog-submit-btn"[\s\S]*class="auth-dialog-submit-icon"[\s\S]*x-text="needsAuth \? 'Unlock dashboard' : 'Save API key'"/,
  );
  assert.match(template, /placeholder="Master key or bearer token"/);
  assert.match(template, /aria-label="API key"/);
  assert.match(
    template,
    /<input id="authDialogApiKey"[\s\S]*type="password"[\s\S]*autocomplete="current-password"[\s\S]*x-model="apiKey"/,
  );
  assert.match(
    template,
    /<p class="auth-dialog-hint">Stored in this browser\. Requests use the Authorization bearer header\.<\/p>/,
  );
  assert.doesNotMatch(template, />Not now<\/button>/);
  assert.match(
    template,
    /<form class="auth-dialog-form" @submit\.prevent="submitApiKey\(\)"/,
  );

  const cloakRule = readCSSRule(css, "[x-cloak]");
  assert.match(cloakRule, /display:\s*none !important/);

  const shellRule = readCSSRule(css, ".auth-dialog-shell");
  assert.match(shellRule, /position:\s*fixed/);
  assert.match(shellRule, /place-items:\s*center/);
  assert.match(shellRule, /z-index:\s*90/);

  const dialogRule = readCSSRule(css, ".auth-dialog");
  assert.match(dialogRule, /width:\s*min\(440px, 100%\)/);
  assert.match(dialogRule, /border-radius:\s*var\(--radius\)/);

  const apiKeyButtonRule = readCSSRule(css, ".api-key-open-btn");
  assert.match(apiKeyButtonRule, /display:\s*inline-flex/);
  assert.match(apiKeyButtonRule, /background:\s*transparent/);
  assert.match(apiKeyButtonRule, /border:\s*1px solid var\(--accent\)/);
  assert.match(apiKeyButtonRule, /color:\s*var\(--accent\)/);

  const submitIconRule = readCSSRule(css, ".auth-dialog-submit-icon");
  assert.match(submitIconRule, /width:\s*16px/);
  assert.match(submitIconRule, /height:\s*16px/);

  const bannerRule = readCSSRule(css, ".auth-banner");
  assert.match(bannerRule, /display:\s*flex/);
  assert.match(bannerRule, /flex-wrap:\s*wrap/);

  assert.match(
    css,
    /@media \(max-width: 768px\)[\s\S]*\.sidebar\.sidebar-collapsed \.sidebar-footer \.api-key-section\s*\{ display:\s*grid; \}/,
  );
  assert.match(
    css,
    /@media \(max-width: 768px\)[\s\S]*\.sidebar-footer \.api-key-open-btn span\s*\{\s*display:\s*none;/,
  );
  assert.match(
    css,
    /@media \(max-width: 768px\)[\s\S]*\.sidebar-footer \.api-key-open-btn\s*\{[\s\S]*width:\s*36px;[\s\S]*height:\s*36px;/,
  );
  assert.match(
    css,
    /@media \(max-width: 768px\)[\s\S]*\.sidebar-footer \.api-key-open-icon\s*\{[\s\S]*width:\s*16px/,
  );
});

test("auth key expirations render as a UTC date with the full UTC timestamp in the hover title", () => {
  const indexTemplate = readDashboardTemplateSource();
  const css = readFixture("../../css/dashboard.css");
  const plusIconTemplate = readFixture("../../../templates/plus-icon.html");
  const authKeyFormMatch = indexTemplate.match(
    /<div class="model-editor auth-key-editor"[\s\S]*?<\/form>/,
  );

  assert.ok(authKeyFormMatch, "Expected auth key editor block");

  const authKeyForm = authKeyFormMatch[0];
  const userPathIndex = authKeyForm.indexOf(
    '<label class="form-field-label" for="auth-key-user-path">User Path (optional)</label>',
  );
  const helperIndex = authKeyForm.indexOf(
    '{{template "inline-help-toggle" .}}',
  );
  const userPathInputIndex = authKeyForm.indexOf('id="auth-key-user-path"');
  const descriptionIndex = authKeyForm.indexOf(
    '<label class="form-field-label" for="auth-key-description">Description (optional)</label>',
  );

  assert.match(
    indexTemplate,
    /x-text="key\.expires_at \? formatDateUTC\(key\.expires_at\) : '\\u2014'"/,
  );
  assert.match(
    indexTemplate,
    /:title="key\.expires_at \? formatTimestampUTC\(key\.expires_at\) : ''"/,
  );
  assert.match(
    indexTemplate,
    /id="auth-key-user-path"[^>]*x-model="authKeyForm\.user_path"[^>]*aria-describedby="auth-key-user-path-help-copy"/,
  );
  assert.match(
    authKeyForm,
    /<h3>Create API Key<\/h3>[\s\S]*class="dialog-close-btn"[\s\S]*@click="closeAuthKeyForm\(\)"[\s\S]*{{template "x-icon"}}/,
  );
  assert.match(indexTemplate, /class="model-editor auth-key-editor"/);
  assert.match(
    plusIconTemplate,
    /{{define "plus-icon"}}[\s\S]*<path d="M12 5v14"><\/path>[\s\S]*<path d="M5 12h14"><\/path>[\s\S]*{{end}}/,
  );
  assert.match(
    indexTemplate,
    /class="pagination-btn pagination-btn-primary pagination-btn-with-icon"[\s\S]*{{template "plus-icon"}}[\s\S]*<span>Create API Key<\/span>/,
  );
  assert.match(
    authKeyForm,
    /class="pagination-btn pagination-btn-primary pagination-btn-with-icon"[\s\S]*x-show="!authKeyFormSubmitting"[\s\S]*{{template "plus-icon"}}[\s\S]*x-text="authKeyFormSubmitting \? 'Creating\.\.\.' : 'Create API Key'"/,
  );
  assert.notEqual(userPathIndex, -1);
  assert.notEqual(helperIndex, -1);
  assert.notEqual(userPathInputIndex, -1);
  assert.notEqual(descriptionIndex, -1);
  assert.ok(userPathIndex < helperIndex);
  assert.ok(helperIndex < userPathInputIndex);
  assert.ok(userPathInputIndex < descriptionIndex);
  assert.match(
    authKeyForm,
    /<div class="inline-help-section" x-data="\{ open: false, copyId: 'auth-key-user-path-help-copy'[\s\S]*<label class="form-field-label" for="auth-key-user-path">User Path \(optional\)<\/label>[\s\S]*{{template "inline-help-toggle" \.}}[\s\S]*<p :id="copyId" class="inline-help-copy" x-show="open" x-transition\.opacity\.duration\.200ms x-text="text"><\/p>/,
  );
  assert.match(authKeyForm, /placeholder="ex\. \/department1\/team-a"/);
  assert.match(authKeyForm, /copyId: 'auth-key-user-path-help-copy'/);
  assert.match(
    authKeyForm,
    /When set, this key overrides X-GoModel-User-Path for audit logging and downstream request context\./,
  );
  assert.doesNotMatch(authKeyForm, /id="auth-key-user-path"[^>]*aria-label=/);
  assert.doesNotMatch(authKeyForm, /User Path Override/);
  assert.doesNotMatch(authKeyForm, /Managed key/);
  assert.doesNotMatch(
    indexTemplate,
    /<p class="form-hint">\s*When set, this key overrides <code>X-GoModel-User-Path<\/code> for audit logging and downstream request context\.\s*<\/p>/,
  );
  assert.match(indexTemplate, /x-text="key\.user_path \|\| '\\u2014'"/);
  assert.match(indexTemplate, /X-GoModel-User-Path/);
  assert.match(indexTemplate, /:disabled="authKeyFormSubmitting"/);
  assert.match(
    indexTemplate,
    /@click="if \(!authKeyFormSubmitting\) openAuthKeyForm\(\)"/,
  );
  assert.match(
    indexTemplate,
    /x-show="authKeys\.length === 0 && !authKeysLoading && !authError && !authKeyError && authKeysAvailable"/,
  );

  const authKeyEditorRule = readCSSRule(css, ".auth-key-editor");
  assert.match(
    authKeyEditorRule,
    /background:\s*color-mix\(in srgb, var\(--bg-surface\) 82%, var\(--bg\) 18%\)/,
  );

  const baseInputRule = readCSSRule(
    css,
    'input:is([type="text"], [type="date"], [type="number"])',
  );
  assert.match(baseInputRule, /background:\s*var\(--bg-surface\)/);
  assert.match(baseInputRule, /width:\s*100%/);
  assert.doesNotMatch(css, /\.auth-key-editor \.filter-input\s*\{/);

  const textareaRule = readCSSRule(css, "textarea");
  assert.match(textareaRule, /background:\s*var\(--bg-surface\)/);
  assert.match(textareaRule, /min-height:\s*60px/);
  assert.doesNotMatch(css, /\.auth-key-editor \.form-textarea\s*\{/);
  assert.doesNotMatch(css, /\.form-textarea\s*\{/);
  assert.doesNotMatch(css, /\.settings-guardrail-textarea\s*\{/);

  const authKeyFieldSpacingRule = readCSSRule(
    css,
    ".auth-key-form-fields > .form-field",
  );
  assert.match(authKeyFieldSpacingRule, /margin-bottom:\s*4px/);

  const paginationBtnWithIconRule = readCSSRule(
    css,
    ".pagination-btn-with-icon",
  );
  assert.match(paginationBtnWithIconRule, /display:\s*inline-flex/);
  assert.match(paginationBtnWithIconRule, /gap:\s*8px/);
});

test("workflow guardrail warning links directly to the top-level guardrails page", () => {
  const indexTemplate = readDashboardTemplateSource();

  assert.match(
    indexTemplate,
    /No named guardrails are currently registered on this deployment\./,
  );
  assert.match(
    indexTemplate,
    /class="alert alert-warning alert-inline-actions" x-show="guardrailRefs\.length === 0"/,
  );
  assert.match(
    indexTemplate,
    /@click="navigate\('guardrails'\)">Open Guardrails<\/button>/,
  );
  assert.match(
    indexTemplate,
    /id="guardrail-filter"[^>]*aria-label="Guardrail filter"[^>]*x-model="guardrailFilter"/,
  );
});

test("usage request log toolbar puts search above the remaining filters", () => {
  const indexTemplate = readDashboardTemplateSource();
  const css = readFixture("../../css/dashboard.css");

  assert.match(
    indexTemplate,
    /<div class="usage-log-toolbar">\s*<div class="usage-filter-row usage-filter-row-search">[\s\S]*aria-label="Search by request ID, model, provider"[\s\S]*x-model="usageLogSearch"[\s\S]*<\/div>\s*<\/div>\s*<div class="usage-filter-row usage-filter-row-controls">[\s\S]*x-model="usageLogModel"[\s\S]*x-model="usageLogProvider"[\s\S]*x-model="usageLogUserPath"/,
  );
  assert.match(
    indexTemplate,
    /class="usage-filter-row usage-filter-row-search"[\s\S]*placeholder="Search by request ID, model, provider\.\.\."/,
  );
  assert.match(
    indexTemplate,
    /class="usage-filter-row usage-filter-row-controls"[\s\S]*placeholder="User path \/team\/alpha"/,
  );

  const toolbarRule = readCSSRule(css, ".usage-log-toolbar");
  assert.match(toolbarRule, /display:\s*grid/);

  const searchRule = readCSSRule(
    css,
    ".usage-filter-row-search .filter-input-wrap",
  );
  assert.match(searchRule, /grid-column:\s*1\s*\/\s*-1/);

  const selectRule = readCSSRule(
    css,
    ".usage-filter-row-controls .usage-log-select",
  );
  assert.match(selectRule, /grid-column:\s*span 3/);

  const userPathRule = readCSSRule(
    css,
    ".usage-filter-row-controls .filter-input-wrap",
  );
  assert.match(userPathRule, /grid-column:\s*span 6/);
});

test("audit toolbar uses a full-width search row above the select row with a right-aligned clear button", () => {
  const indexTemplate = readDashboardTemplateSource();
  const iconTemplate = readFixture("../../../templates/x-icon.html");
  const css = readFixture("../../css/dashboard.css");

  assert.match(
    indexTemplate,
    /<div class="audit-filter-row audit-filter-row-search">[\s\S]*id="audit-filter-search"[\s\S]*<\/div>\s*<div class="audit-filter-row audit-filter-row-controls">[\s\S]*id="audit-filter-method"[\s\S]*id="audit-filter-status"[\s\S]*id="audit-filter-stream"[\s\S]*class="pagination-btn audit-clear-btn" @click="clearAuditFilters\(\)"/,
  );
  assert.match(
    indexTemplate,
    /id="audit-filter-search"[^>]*placeholder="Search by request ID, model, provider, path, user path, or error\.\.\."/,
  );
  assert.match(
    indexTemplate,
    /class="audit-filter-row audit-filter-row-search"[\s\S]*data-lucide="search" class="filter-input-icon"[\s\S]*id="audit-filter-search"/,
  );
  assert.match(
    indexTemplate,
    /id="audit-filter-status"[\s\S]*<option value="504">504<\/option>/,
  );
  assert.doesNotMatch(indexTemplate, /id="audit-filter-model"/);
  assert.doesNotMatch(indexTemplate, /id="audit-filter-provider"/);
  assert.doesNotMatch(indexTemplate, /id="audit-filter-path"/);
  assert.doesNotMatch(indexTemplate, /id="audit-filter-user-path"/);
  assert.match(
    indexTemplate,
    /class="pagination-btn audit-clear-btn" @click="clearAuditFilters\(\)">[\s\S]*{{template "x-icon"}}[\s\S]*<span>Clear<\/span>/,
  );
  assert.match(iconTemplate, /{{define "x-icon"}}/);

  const clearRule = readCSSRule(css, ".audit-clear-btn");
  assert.match(clearRule, /background:\s*#fff/);
  assert.match(clearRule, /color:\s*#111110/);

  const searchRule = readCSSRule(
    css,
    ".audit-filter-row-search .filter-input-wrap",
  );
  assert.match(searchRule, /grid-column:\s*1\s*\/\s*-1/);

  const selectRule = readCSSRule(css, ".usage-log-select");
  assert.match(selectRule, /appearance:\s*none/);
  assert.match(selectRule, /-webkit-appearance:\s*none/);
  assert.match(selectRule, /padding:\s*8px 34px 8px 12px/);
  assert.match(selectRule, /background-image:[\s\S]*currentcolor/);
  assert.match(selectRule, /cursor:\s*pointer/);

  const disabledSelectRule = readCSSRule(css, ".usage-log-select:disabled");
  assert.match(disabledSelectRule, /cursor:\s*default/);

  const controlsRule = readCSSRule(
    css,
    ".audit-filter-row-controls .pagination-btn",
  );
  assert.match(controlsRule, /grid-column:\s*11\s*\/\s*-1/);
  assert.match(controlsRule, /justify-self:\s*end/);
});

test("audit entry metadata is rendered as a labeled pill row at the bottom of the expanded entry", () => {
  const indexTemplate = readDashboardTemplateSource();
  const css = readFixture("../../css/dashboard.css");
  const detailsStart = indexTemplate.indexOf(
    '<div class="audit-entry-details">',
  );
  const detailsEnd = indexTemplate.indexOf("</template>", detailsStart);

  assert.notEqual(detailsStart, -1, "Expected audit entry details block");
  assert.notEqual(detailsEnd, -1, "Expected lazy audit entry details wrapper");

  const auditEntry = indexTemplate.slice(detailsStart, detailsEnd);
  const requestResponseIndex = auditEntry.indexOf(
    '<div class="audit-request-response">',
  );
  const metadataIndex = auditEntry.indexOf(
    '<div class="audit-entry-metadata">',
  );

  assert.notEqual(requestResponseIndex, -1);
  assert.notEqual(metadataIndex, -1);
  assert.ok(requestResponseIndex < metadataIndex);
  assert.match(
    auditEntry,
    /<span class="audit-entry-metadata-label">Metadata:<\/span>/,
  );
  assert.match(
    auditEntry,
    /<span class="provider-badge" x-text="providerDisplayValue\(entry\) \|\| '-'"><\/span>/,
  );
  assert.match(
    auditEntry,
    /<span class="provider-badge mono" x-text="entry\.requested_model \|\| entry\.model \|\| '-'"><\/span>/,
  );
  assert.match(
    auditEntry,
    /<span class="provider-badge mono" x-text="'request_id: ' \+ \(entry\.request_id \|\| '-'\)"><\/span>/,
  );
  assert.match(
    auditEntry,
    /<span class="provider-badge mono" x-show="entry\.client_ip" x-text="'ip: ' \+ entry\.client_ip"><\/span>/,
  );
  assert.match(
    auditEntry,
    /<span class="provider-badge mono" x-show="workflowFailoverTarget\(entry\)" x-text="'failover: ' \+ workflowFailoverTarget\(entry\)"><\/span>/,
  );

  const requestResponseRule = readCSSRule(css, ".audit-request-response");
  assert.match(
    requestResponseRule,
    /grid-template-columns:\s*minmax\(0,\s*1fr\) minmax\(0,\s*1fr\)/,
  );
  assert.match(requestResponseRule, /overflow-x:\s*auto/);

  const paneRule = readCSSRule(css, ".audit-pane");
  assert.match(paneRule, /min-width:\s*0/);

  const paneBlockRule = readCSSRule(css, ".audit-pane-block");
  assert.match(paneBlockRule, /min-width:\s*0/);

  const auditJSONRule = readCSSRule(css, ".audit-json");
  assert.match(auditJSONRule, /max-width:\s*100%/);
  assert.match(auditJSONRule, /overflow-x:\s*auto/);

  const auditErrorRule = readCSSRule(css, ".audit-pane-error-message");
  assert.match(auditErrorRule, /color:\s*var\(--danger\)/);

  const metadataRule = readCSSRule(css, ".audit-entry-metadata");
  assert.match(metadataRule, /display:\s*flex/);
  assert.match(metadataRule, /align-items:\s*center/);
  assert.match(metadataRule, /margin-top:\s*12px/);
  assert.match(metadataRule, /padding-top:\s*12px/);
  assert.match(metadataRule, /border-top:\s*1px solid var\(--border\)/);

  const metadataLabelRule = readCSSRule(css, ".audit-entry-metadata-label");
  assert.match(metadataLabelRule, /text-transform:\s*uppercase/);
  assert.match(metadataLabelRule, /letter-spacing:\s*0\.08em/);

  const metadataContextRule = readCSSRule(css, ".audit-entry-context");
  assert.match(metadataContextRule, /flex-wrap:\s*wrap/);
});

test("audit entry details show prompt caching inside the request body pane", () => {
  const indexTemplate = readDashboardTemplateSource();
  const auditPaneTemplate = readFixture("../../../templates/audit-pane.html");
  const css = readFixture("../../css/dashboard.css");
  const detailsStart = indexTemplate.indexOf(
    '<div class="audit-entry-details">',
  );
  const detailsEnd = indexTemplate.indexOf("</template>", detailsStart);

  assert.notEqual(detailsStart, -1, "Expected audit entry details block");
  assert.notEqual(detailsEnd, -1, "Expected lazy audit entry details wrapper");

  const auditEntry = indexTemplate.slice(detailsStart, detailsEnd);
  const requestResponseIndex = auditEntry.indexOf(
    '<div class="audit-request-response">',
  );
  const metadataIndex = auditEntry.indexOf(
    '<div class="audit-entry-metadata">',
  );

  assert.ok(requestResponseIndex < metadataIndex);
  assert.doesNotMatch(auditEntry, /audit-cache-panel/);
  assert.match(
    auditPaneTemplate,
    /<h5>Body<\/h5>[\s\S]*<span class="audit-prompt-cache-pill mono" x-show="pane\.bodyCacheRatioLabel" x-text="pane\.bodyCacheRatioLabel"><\/span>/,
  );

  const pillRule = readCSSRule(css, ".audit-prompt-cache-pill");
  assert.match(pillRule, /color:\s*var\(--prompt-cache-color\)/);
  assert.match(pillRule, /border-radius:\s*999px/);

  const highlightRule = readCSSRule(css, ".audit-prompt-cache-highlight");
  assert.match(highlightRule, /color:\s*var\(--prompt-cache-color\)/);
  assert.match(highlightRule, /font-weight:\s*700/);
});

test("model category tables lazy mount only the active table body", () => {
  const indexTemplate = readDashboardTemplateSource();
  const css = readFixture("../../css/dashboard.css");
  const modelsStart = indexTemplate.indexOf("<!-- Models Page -->");
  const workflowsStart = indexTemplate.indexOf("<!-- Workflows Page -->");

  assert.notEqual(modelsStart, -1, "Expected models page block");
  assert.notEqual(workflowsStart, -1, "Expected workflows page block");

  const modelsBlock = indexTemplate.slice(modelsStart, workflowsStart);
  const lazyTableMounts =
    modelsBlock.match(
      /<template x-if="\([^"]*activeCategory[^"]*">\s*<div class="table-wrapper">/g,
    ) || [];

  assert.equal(lazyTableMounts.length, 6);
  assert.doesNotMatch(
    modelsBlock,
    /<div class="table-wrapper" x-show="\([^"]*activeCategory/,
  );
  assert.match(
    modelsBlock,
    /activeCategory === 'embedding'[\s\S]*{{template "model-table-body" \.}}/,
  );
  assert.match(
    modelsBlock,
    /activeCategory === 'utility'[\s\S]*{{template "model-table-body" \.}}/,
  );
  assert.match(
    modelsBlock,
    /class="loading-state" x-show="modelsLoading && !authError" role="status" aria-live="polite"/,
  );
  assert.match(
    modelsBlock,
    /x-text="displayModels\.length > 0 \? 'Refreshing models\.\.\.' : 'Loading models\.\.\.'"/,
  );
  assert.match(
    modelsBlock,
    /class="filter-input-wrap"[\s\S]*data-lucide="search" class="filter-input-icon"[\s\S]*x-model="modelFilter"/,
  );
  assert.match(
    modelsBlock,
    /placeholder="Filter by provider, provider\/model, alias, or owner\.\.\." aria-label="Filter models by provider, provider\/model, alias, or owner" x-model="modelFilter" class="filter-input"/,
  );
  assert.match(
    modelsBlock,
    /class="pagination-btn pagination-btn-primary pagination-btn-with-icon alias-create-btn"[\s\S]*@click="openAliasCreate\(\)"[\s\S]*data-lucide="plus" class="alias-create-icon"[\s\S]*<span>Create(?:&nbsp;| )Alias<\/span>/,
  );
  assert.match(
    modelsBlock,
    /class="pagination-btn pagination-btn-primary pagination-btn-with-icon alias-submit-btn"[\s\S]*:disabled="aliasSubmitting"[\s\S]*data-lucide="plus" class="form-action-icon" x-show="aliasFormMode !== 'edit'"[\s\S]*data-lucide="save" class="form-action-icon" x-show="aliasFormMode === 'edit'"[\s\S]*x-text="aliasSubmitting \? 'Saving\.\.\.' : \(aliasFormMode === 'edit' \? 'Save Alias' : 'Create Alias'\)"/,
  );
  assert.match(
    modelsBlock,
    /class="pagination-btn pagination-btn-primary pagination-btn-with-icon model-access-submit-btn"[\s\S]*:disabled="modelOverrideSubmitting"[\s\S]*data-lucide="save" class="form-action-icon"[\s\S]*x-text="modelOverrideSubmitting \? 'Saving\.\.\.' : 'Save Access'"/,
  );
  assert.match(
    modelsBlock,
    /The selector uses <code>\/<\/code> for all providers and models, <code>\{provider_name\}\/<\/code> for one provider, or <code>\{provider_name\}\/\{model\}<\/code> for one model\.[\s\S]*managed API key <code>user_path<\/code> when present, otherwise the <code>X-GoModel-User-Path<\/code> request header\./,
  );

  const loadingRule = readCSSRule(css, ".loading-state");
  assert.match(loadingRule, /display:\s*flex/);
  assert.match(loadingRule, /min-height:\s*64px/);

  const filterInputRule = readCSSRule(css, ".filter-input");
  assert.match(filterInputRule, /min-width:\s*min\(400px,\s*100%\)/);
  assert.doesNotMatch(filterInputRule, /background:/);
  assert.doesNotMatch(filterInputRule, /border:/);

  const filterInputWrapRule = readCSSRule(css, ".filter-input-wrap");
  assert.match(filterInputWrapRule, /position:\s*relative/);
  assert.match(filterInputWrapRule, /min-width:\s*min\(400px,\s*100%\)/);

  const filterInputIconRule = readCSSRule(css, ".filter-input-icon");
  assert.match(filterInputIconRule, /pointer-events:\s*none/);

  const spinnerRule = readCSSRule(css, ".loading-spinner");
  assert.match(spinnerRule, /animation:\s*loading-spin 0\.8s linear infinite/);
});

test("alias rows use a shared icon-only edit action", () => {
  const indexTemplate = readDashboardTemplateSource();
  const modelTableTemplate = readFixture(
    "../../../templates/model-table-body.html",
  );
  const editIconTemplate = readFixture("../../../templates/edit-icon.html");

  assert.match(
    modelTableTemplate,
    /class="table-action-btn table-icon-btn"[\s\S]*:aria-label="'Edit alias ' \+ row\.alias\.name"[\s\S]*@click="openAliasEdit\(row\.alias\)"[\s\S]*{{template "edit-icon"}}/,
  );
  assert.match(indexTemplate, /{{template "model-table-body" \.}}/);
  assert.match(
    indexTemplate,
    /x-show="modelOverrideFormOpen" x-ref="modelOverrideEditor"/,
  );
  assert.doesNotMatch(
    indexTemplate,
    /Model overrides feature is unavailable\./,
  );
  assert.doesNotMatch(indexTemplate, /!modelOverridesAvailable && !authError/);
  assert.match(editIconTemplate, /{{define "edit-icon"}}/);
});

test("mobile modal editor headers keep the close action beside the title", () => {
  const indexTemplate = readDashboardTemplateSource();
  const css = readFixture("../../css/dashboard.css");

  assert.match(
    indexTemplate,
    /<h3>Create API Key<\/h3>[\s\S]*class="dialog-close-btn"[\s\S]*@click="closeAuthKeyForm\(\)"/,
  );

  const modalHeaderRule = readCSSRule(css, ".model-editor .editor-header");
  assert.match(modalHeaderRule, /flex-direction:\s*row/);
  assert.match(modalHeaderRule, /align-items:\s*flex-start/);

  const modalHeaderTitleRule = readCSSRule(
    css,
    ".model-editor .editor-header > :first-child",
  );
  assert.match(modalHeaderTitleRule, /flex:\s*1/);
  assert.match(modalHeaderTitleRule, /min-width:\s*0/);

  const modalCloseRule = readCSSRule(
    css,
    ".model-editor .editor-header .dialog-close-btn",
  );
  assert.match(modalCloseRule, /flex:\s*0 0 32px/);
  assert.match(modalCloseRule, /width:\s*32px/);
  assert.match(modalCloseRule, /min-width:\s*32px/);
});

test("modal and conversation close controls use the shared dialog close style", () => {
  const shellTemplate = readDashboardShellTemplate();
  const indexTemplate = readDashboardTemplateSource();
  const css = readFixture("../../css/dashboard.css");

  assert.match(
    shellTemplate,
    /class="auth-dialog-close dialog-close-btn"[\s\S]*@click="closeAuthDialog\(\)"[\s\S]*{{template "x-icon"}}/,
  );
  assert.match(indexTemplate, /class="dialog-close-btn" aria-label="Close alias editor"[\s\S]*{{template "x-icon"}}/);
  assert.match(indexTemplate, /class="dialog-close-btn" aria-label="Close model access editor"[\s\S]*{{template "x-icon"}}/);
  assert.match(indexTemplate, /class="dialog-close-btn" aria-label="Close workflow editor"[\s\S]*{{template "x-icon"}}/);
  assert.match(indexTemplate, /class="dialog-close-btn" aria-label="Close guardrail editor"[\s\S]*{{template "x-icon"}}/);
  assert.match(indexTemplate, /class="dialog-close-btn"[\s\S]*@click="closeAuthKeyForm\(\)"[\s\S]*{{template "x-icon"}}/);
  assert.match(indexTemplate, /class="dialog-close-btn" x-ref="conversationCloseBtn" aria-label="Close interactions"[\s\S]*{{template "x-icon"}}/);
  assert.doesNotMatch(indexTemplate, /alias-close-btn/);
  assert.doesNotMatch(indexTemplate, /x-ref="conversationCloseBtn"[\s\S]*>Close<\/button>/);

  const closeRule = readCSSRule(css, ".dialog-close-btn");
  assert.match(closeRule, /width:\s*32px/);
  assert.match(closeRule, /height:\s*32px/);
  assert.match(closeRule, /border-radius:\s*6px/);
  assert.match(closeRule, /color:\s*var\(--text-muted\)/);
});

test("modal escape handlers do not close editors behind the auth dialog", () => {
  const indexTemplate = readDashboardTemplateSource();

  assert.match(indexTemplate, /@keydown\.escape\.window="aliasFormOpen && !authDialogOpen && closeAliasForm\(\)"/);
  assert.match(indexTemplate, /@keydown\.escape\.window="modelOverrideFormOpen && !authDialogOpen && closeModelOverrideForm\(\)"/);
  assert.match(indexTemplate, /@keydown\.escape\.window="workflowFormOpen && !authDialogOpen && closeWorkflowForm\(\)"/);
  assert.match(indexTemplate, /@keydown\.escape\.window="guardrailFormOpen && !authDialogOpen && closeGuardrailForm\(\)"/);
  assert.match(indexTemplate, /@keydown\.escape\.window="authKeyFormOpen && !authDialogOpen && closeAuthKeyForm\(\)"/);
});

test("overview interval controls are explicit non-submit buttons", () => {
  const indexTemplate = readDashboardTemplateSource();

  assert.match(indexTemplate, /<button type="button" class="interval-btn"[^>]*@click="setInterval\('daily'\)">Daily<\/button>/);
  assert.match(indexTemplate, /<button type="button" class="interval-btn"[^>]*@click="setInterval\('weekly'\)">Weekly<\/button>/);
  assert.match(indexTemplate, /<button type="button" class="interval-btn"[^>]*@click="setInterval\('monthly'\)">Monthly<\/button>/);
  assert.match(indexTemplate, /<button type="button" class="interval-btn"[^>]*@click="setInterval\('yearly'\)">Yearly<\/button>/);
});

test("usage mode controls are explicit non-submit buttons", () => {
  const indexTemplate = readDashboardTemplateSource();

  assert.match(indexTemplate, /<button type="button" class="usage-mode-btn"[^>]*@click="toggleUsageMode\('tokens'\)">Tokens<\/button>/);
  assert.match(indexTemplate, /<button type="button" class="usage-mode-btn"[^>]*@click="toggleUsageMode\('costs'\)">Costs<\/button>/);
});

test("model category tabs are explicit non-submit buttons", () => {
  const indexTemplate = readDashboardTemplateSource();

  assert.match(indexTemplate, /<button type="button" class="category-tab"[^>]*@click="selectCategory\(cat\.category\)">/);
});

test("settings controls describe their inline helper copy", () => {
  const indexTemplate = readDashboardTemplateSource();

  assert.match(
    indexTemplate,
    /id="timezone-override-select"[\s\S]*aria-describedby="timezone-help-copy"/,
  );
  assert.match(
    indexTemplate,
    /class="pagination-btn pagination-btn-primary pagination-btn-with-icon settings-refresh-btn"[\s\S]*aria-describedby="runtime-refresh-help-copy"/,
  );
});

test("usage and audit pages reuse a shared pagination template", () => {
  const indexTemplate = readDashboardTemplateSource();
  const paginationTemplate = readFixture("../../../templates/pagination.html");

  assert.match(
    paginationTemplate,
    /{{define "pagination"}}[\s\S]*x-show="{{\.}}\.total > 0"[\s\S]*type="button"[\s\S]*@click="{{\.}}PrevPage\(\)"[\s\S]*type="button"[\s\S]*@click="{{\.}}NextPage\(\)"[\s\S]*{{end}}/,
  );
  assert.match(indexTemplate, /{{template "pagination" "usageLog"}}/);
  assert.match(indexTemplate, /{{template "pagination" "auditLog"}}/);
  assert.doesNotMatch(
    indexTemplate,
    /<div class="pagination" x-show="usageLog\.total > 0">[\s\S]*usageLogPrevPage\(\)[\s\S]*<\/div>/,
  );
  assert.doesNotMatch(
    indexTemplate,
    /<div class="pagination" x-show="auditLog\.total > 0">[\s\S]*auditLogPrevPage\(\)[\s\S]*<\/div>/,
  );
});

test("usage charts can switch between chart and table views", () => {
  const indexTemplate = readDashboardTemplateSource();

  assert.match(
    indexTemplate,
    /class="chart-view-toggle"[\s\S]*@click="toggleUsageChartView\('model', 'chart'\)"[\s\S]*@click="toggleUsageChartView\('model', 'table'\)"/,
  );
  assert.match(
    indexTemplate,
    /<div class="bar-chart-wrap" x-show="modelUsageView === 'chart'">[\s\S]*<canvas id="usageBarChart"><\/canvas>/,
  );
  assert.match(
    indexTemplate,
    /<template x-if="modelUsageView === 'table'">[\s\S]*modelUsageTableRows\(\)/,
  );
  assert.match(
    indexTemplate,
    /class="chart-view-toggle"[\s\S]*@click="toggleUsageChartView\('userPath', 'chart'\)"[\s\S]*@click="toggleUsageChartView\('userPath', 'table'\)"/,
  );
  assert.match(
    indexTemplate,
    /<div class="model-chart-section" x-show="userPathUsageChartVisible\(\)">[\s\S]*<h3 x-text="usageMode === 'costs' \? 'Cost by User Path' : 'Usage by User Path'"><\/h3>/,
  );
  assert.match(
    indexTemplate,
    /<div class="bar-chart-wrap" x-show="userPathUsageView === 'chart'">[\s\S]*<canvas id="usageUserPathChart"><\/canvas>/,
  );
  assert.match(
    indexTemplate,
    /<template x-if="userPathUsageView === 'table'">[\s\S]*userPathUsageTableRows\(\)/,
  );
});

test("audit request and response sections reuse a shared audit pane template", () => {
  const indexTemplate = readDashboardTemplateSource();
  const auditPaneTemplate = readFixture("../../../templates/audit-pane.html");

  assert.match(
    auditPaneTemplate,
    /{{define "audit-pane"}}[\s\S]*x-data="auditPaneState\({{\.\}}\)"[\s\S]*x-text="pane\.title"[\s\S]*x-show="pane\.showHeaders"[\s\S]*@click\.prevent="copyHeaders\(\)"[\s\S]*x-text="formattedHeaders"[\s\S]*x-show="pane\.showBody"[\s\S]*@click\.prevent="copyBody\(\)"[\s\S]*x-html="renderedBody"[\s\S]*x-text="pane\.emptyMessage"[\s\S]*x-text="pane\.tooLargeMessage"[\s\S]*{{end}}/,
  );
  assert.match(
    auditPaneTemplate,
    /aria-live="polite" aria-atomic="true" x-text="copyHeadersState\.error \? 'Copy failed' : \(copyHeadersState\.copied \? 'Copied' : 'Copy Headers'\)"/,
  );
  assert.match(
    auditPaneTemplate,
    /aria-live="polite" aria-atomic="true" x-text="copyBodyState\.error \? 'Copy failed' : \(copyBodyState\.copied \? 'Copied' : 'Copy Body'\)"/,
  );
  assert.match(
    auditPaneTemplate,
    /<pre class="audit-json audit-pane-error-message" x-text="pane\.errorMessage"><\/pre>/,
  );
  assert.match(
    indexTemplate,
    /{{template "audit-pane" "auditRequestPane\(entry\)"}}/,
  );
  assert.match(
    indexTemplate,
    /{{template "audit-pane" "auditResponsePane\(entry\)"}}/,
  );
  assert.doesNotMatch(indexTemplate, /audit-error-summary/);
  assert.doesNotMatch(
    indexTemplate,
    /<section class="audit-pane">[\s\S]*<h4>Request<\/h4>/,
  );
  assert.doesNotMatch(
    indexTemplate,
    /<section class="audit-pane">[\s\S]*<h4>Response<\/h4>/,
  );
});
