const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadAuditListModuleFactory(overrides = {}) {
    const clipboardSource = fs.readFileSync(path.join(__dirname, 'clipboard.js'), 'utf8');
    const source = fs.readFileSync(path.join(__dirname, 'audit-list.js'), 'utf8');
    const window = {
        ...(overrides.window || {})
    };
    const context = {
        console,
        ...overrides,
        window
    };
    vm.createContext(context);
    vm.runInContext(clipboardSource, context);
    vm.runInContext(source, context);
    return context.window.dashboardAuditListModule;
}

function createAuditListModule(overrides) {
    const factory = loadAuditListModuleFactory(overrides);
    return factory();
}

test('auditRequestPane returns the shared request-pane contract', () => {
    const module = createAuditListModule();
    const entry = {
        data: {
            request_headers: { authorization: 'Bearer redacted' },
            request_body: { model: 'gpt-5', stream: false },
            request_body_too_big_to_handle: true
        }
    };

    const pane = module.auditRequestPane(entry);

    assert.equal(pane.title, 'Request');
    assert.equal(pane.entry, entry);
    assert.equal(JSON.stringify(pane.copyBody), JSON.stringify(entry.data.request_body));
    assert.equal(pane.showErrorMessage, false);
    assert.equal(pane.errorMessage, null);
    assert.equal(pane.showHeaders, true);
    assert.equal(JSON.stringify(pane.headers), JSON.stringify(entry.data.request_headers));
    assert.equal(pane.showBody, true);
    assert.equal(JSON.stringify(pane.body), JSON.stringify(entry.data.request_body));
    assert.equal(pane.showEmpty, false);
    assert.equal(pane.emptyMessage, 'Request details were not captured.');
    assert.equal(pane.showTooLarge, true);
    assert.equal(pane.tooLargeMessage, 'Request body was too large to capture.');
});

test('auditResponsePane returns the shared response-pane contract', () => {
    const module = createAuditListModule();
    const entry = {
        data: {
            error_message: 'provider timeout',
            response_headers: { 'x-request-id': 'abc123' },
            response_body: { id: 'resp_123' },
            response_body_too_big_to_handle: false
        }
    };

    const pane = module.auditResponsePane(entry);

    assert.equal(pane.title, 'Response');
    assert.equal(pane.entry, entry);
    assert.equal(JSON.stringify(pane.copyBody), JSON.stringify(entry.data.response_body));
    assert.equal(pane.showErrorMessage, true);
    assert.equal(pane.errorMessage, 'provider timeout');
    assert.equal(pane.showHeaders, true);
    assert.equal(JSON.stringify(pane.headers), JSON.stringify(entry.data.response_headers));
    assert.equal(pane.showBody, true);
    assert.equal(JSON.stringify(pane.body), JSON.stringify(entry.data.response_body));
    assert.equal(pane.showEmpty, false);
    assert.equal(pane.emptyMessage, 'Response details were not captured.');
    assert.equal(pane.showTooLarge, false);
    assert.equal(pane.tooLargeMessage, 'Response body was too large to capture.');
});

test('fetchAuditLog preserves a successful payload when workflow prefetch fails', async () => {
    const loggedErrors = [];
    const module = createAuditListModule({
        console: {
            error(...args) {
                loggedErrors.push(args);
            }
        },
        fetch() {
            return Promise.resolve({
                ok: true,
                json: async () => ({
                    entries: [{ id: 'audit-1', execution_plan_version_id: 'workflow-1' }],
                    total: 1,
                    limit: 25,
                    offset: 0
                })
            });
        }
    });
    module.auditFetchToken = 0;
    module.auditLog = { entries: [], total: 0, limit: 25, offset: 0 };
    module.days = 7;
    module.headers = () => ({ authorization: 'Bearer token' });
    module.handleFetchResponse = () => true;
    module.prefetchAuditExecutionPlans = async () => {
        throw new Error('prefetch failed');
    };

    await module.fetchAuditLog(true);

    assert.equal(
        JSON.stringify(module.auditLog),
        JSON.stringify({
            entries: [{ id: 'audit-1', execution_plan_version_id: 'workflow-1' }],
            total: 1,
            limit: 25,
            offset: 0
        })
    );
    assert.equal(loggedErrors.length, 1);
    assert.match(String(loggedErrors[0][0]), /Failed to prefetch audit workflows:/);
});

test('fetchAuditLog sends the consolidated audit search and select filters only', async () => {
    const requests = [];
    const module = createAuditListModule({
        fetch(url) {
            requests.push(url);
            return Promise.resolve({
                ok: true,
                json: async () => ({
                    entries: [],
                    total: 0,
                    limit: 25,
                    offset: 0
                })
            });
        }
    });
    module.auditFetchToken = 0;
    module.auditLog = { entries: [], total: 0, limit: 25, offset: 0 };
    module.days = 30;
    module.auditSearch = 'team/alpha';
    module.auditMethod = 'POST';
    module.auditStatusCode = '500';
    module.auditStream = 'true';
    module.headers = () => ({});
    module.handleFetchResponse = () => true;

    await module.fetchAuditLog(true);

    assert.equal(requests.length, 1);
    assert.match(requests[0], /search=team%2Falpha/);
    assert.match(requests[0], /method=POST/);
    assert.match(requests[0], /status_code=500/);
    assert.match(requests[0], /stream=true/);
    assert.doesNotMatch(requests[0], /[?&](model|provider|path|user_path)=/);
});

test('clearAuditFilters resets the consolidated audit controls', () => {
    const module = createAuditListModule();
    let fetchCalled = false;
    module.auditSearch = 'req_123';
    module.auditMethod = 'DELETE';
    module.auditStatusCode = '404';
    module.auditStream = 'false';
    module.fetchAuditLog = (resetOffset) => {
        fetchCalled = resetOffset === true;
    };

    module.clearAuditFilters();

    assert.equal(module.auditSearch, '');
    assert.equal(module.auditMethod, '');
    assert.equal(module.auditStatusCode, '');
    assert.equal(module.auditStream, '');
    assert.equal(fetchCalled, true);
});

test('auditPaneState copies the formatted body and resets success feedback', async () => {
    let resetCallback = null;
    const writes = [];
    const module = createAuditListModule({
        setTimeout(callback) {
            resetCallback = callback;
            return 1;
        },
        clearTimeout() {},
        window: {
            navigator: {
                clipboard: {
                    writeText(value) {
                        writes.push(value);
                        return Promise.resolve();
                    }
                }
            }
        }
    });

    const paneState = module.auditPaneState({
        copyBody: { model: 'gpt-5', stream: false }
    });

    await paneState.copyBody();

    assert.deepEqual(writes, ['{\n  "model": "gpt-5",\n  "stream": false\n}']);
    assert.equal(paneState.copyState.copied, true);
    assert.equal(paneState.copyState.error, false);

    assert.equal(typeof resetCallback, 'function');
    resetCallback();

    assert.equal(paneState.copyState.copied, false);
    assert.equal(paneState.copyState.error, false);
});

test('auditPaneState marks copy failures and clears the error after reset', async () => {
    let resetCallback = null;
    const module = createAuditListModule({
        console: {
            error() {}
        },
        setTimeout(callback) {
            resetCallback = callback;
            return 1;
        },
        clearTimeout() {},
        window: {
            navigator: {
                clipboard: {
                    writeText() {
                        return Promise.reject(new Error('denied'));
                    }
                }
            }
        }
    });

    const paneState = module.auditPaneState({
        copyBody: { id: 'resp_123' }
    });

    await paneState.copyBody();

    assert.equal(paneState.copyState.copied, false);
    assert.equal(paneState.copyState.error, true);

    assert.equal(typeof resetCallback, 'function');
    resetCallback();

    assert.equal(paneState.copyState.copied, false);
    assert.equal(paneState.copyState.error, false);
});
