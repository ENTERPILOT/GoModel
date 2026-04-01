const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadAuthKeysModuleFactory(overrides = {}) {
    const clipboardSource = fs.readFileSync(path.join(__dirname, 'clipboard.js'), 'utf8');
    const source = fs.readFileSync(path.join(__dirname, 'auth-keys.js'), 'utf8');
    const window = {
        ...(overrides.window || {})
    };
    const context = {
        console,
        setTimeout,
        clearTimeout,
        ...overrides,
        window
    };
    vm.createContext(context);
    vm.runInContext(clipboardSource, context);
    vm.runInContext(source, context);
    return context.window.dashboardAuthKeysModule;
}

function createAuthKeysModule(overrides) {
    const factory = loadAuthKeysModuleFactory(overrides);
    return factory();
}

function createTimerHarness() {
    let nextID = 1;
    const timers = new Map();
    return {
        setTimeout(callback, _delay) {
            const id = nextID++;
            timers.set(id, callback);
            return id;
        },
        clearTimeout(id) {
            timers.delete(id);
        },
        runAll() {
            const callbacks = Array.from(timers.values());
            timers.clear();
            callbacks.forEach((callback) => callback());
        }
    };
}

test('submitAuthKeyForm serializes date-only expirations to the end of the selected UTC day', async () => {
    const requests = [];
    const module = createAuthKeysModule({
        fetch: async (url, options) => {
            requests.push({ url, options });
            return {
                status: 201,
                async json() {
                    return { value: 'sk_gom_test' };
                }
            };
        }
    });

    module.headers = () => ({ 'Content-Type': 'application/json' });
    module.fetchAuthKeys = async () => {};
    module.authKeyForm = {
        name: 'ci-deploy',
        description: '',
        user_path: '',
        expires_at: '2026-04-01'
    };

    await module.submitAuthKeyForm();

    assert.equal(requests.length, 1);
    assert.equal(requests[0].url, '/admin/api/v1/auth-keys');
    assert.equal(
        JSON.parse(requests[0].options.body).expires_at,
        '2026-04-01T23:59:59Z'
    );
});

test('submitAuthKeyForm normalizes user paths before sending them', async () => {
    const requests = [];
    const module = createAuthKeysModule({
        fetch: async (url, options) => {
            requests.push({ url, options });
            return {
                status: 201,
                async json() {
                    return { value: 'sk_gom_test' };
                }
            };
        }
    });

    module.headers = () => ({ 'Content-Type': 'application/json' });
    module.fetchAuthKeys = async () => {};
    module.authKeyForm = {
        name: 'ci-deploy',
        description: '',
        user_path: ' team//alpha/service/ ',
        expires_at: ''
    };

    await module.submitAuthKeyForm();

    assert.equal(requests.length, 1);
    assert.equal(
        JSON.parse(requests[0].options.body).user_path,
        '/team/alpha/service'
    );
});

test('submitAuthKeyForm rejects invalid user paths before sending the request', async () => {
    let called = false;
    const module = createAuthKeysModule({
        fetch: async () => {
            called = true;
            return {
                status: 201,
                async json() {
                    return { value: 'sk_gom_test' };
                }
            };
        }
    });

    module.headers = () => ({ 'Content-Type': 'application/json' });
    module.authKeyForm = {
        name: 'ci-deploy',
        description: '',
        user_path: '/team/../alpha',
        expires_at: ''
    };

    await module.submitAuthKeyForm();

    assert.equal(called, false);
    assert.equal(module.authKeyError, 'User path cannot contain "." or ".." segments.');
});

test('copyAuthKeyValue uses navigator.clipboard when available and resets feedback', async () => {
    const timers = createTimerHarness();
    const writes = [];
    const module = createAuthKeysModule({
        setTimeout: timers.setTimeout,
        clearTimeout: timers.clearTimeout,
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

    module.authKeyIssuedValue = 'sk_gom_test';

    await module.copyAuthKeyValue();

    assert.deepEqual(writes, ['sk_gom_test']);
    assert.equal(module.authKeyCopyState.copied, true);
    assert.equal(module.authKeyCopyState.error, false);

    timers.runAll();

    assert.equal(module.authKeyCopyState.copied, false);
    assert.equal(module.authKeyCopyState.error, false);
});

test('copyAuthKeyValue sets an error flag when navigator.clipboard rejects', async () => {
    const timers = createTimerHarness();
    const module = createAuthKeysModule({
        console: {
            error() {}
        },
        setTimeout: timers.setTimeout,
        clearTimeout: timers.clearTimeout,
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

    module.authKeyIssuedValue = 'sk_gom_test';

    await module.copyAuthKeyValue();

    assert.equal(module.authKeyCopyState.copied, false);
    assert.equal(module.authKeyCopyState.error, true);

    timers.runAll();

    assert.equal(module.authKeyCopyState.copied, false);
    assert.equal(module.authKeyCopyState.error, false);
});

test('copyAuthKeyValue falls back to document.execCommand when clipboard API is unavailable', async () => {
    const timers = createTimerHarness();
    const appended = [];
    const removed = [];
    const fakeBody = {
        appendChild(node) {
            node.parentNode = fakeBody;
            appended.push(node);
        },
        removeChild(node) {
            removed.push(node);
            node.parentNode = null;
        }
    };
    const fakeDocument = {
        body: fakeBody,
        createElement() {
            return {
                value: '',
                style: {},
                setAttribute() {},
                focus() {},
                select() {},
                setSelectionRange() {},
                parentNode: null
            };
        },
        execCommand(command) {
            assert.equal(command, 'copy');
            return true;
        }
    };
    const module = createAuthKeysModule({
        setTimeout: timers.setTimeout,
        clearTimeout: timers.clearTimeout,
        window: {
            document: fakeDocument
        }
    });

    module.authKeyIssuedValue = 'sk_gom_test';

    await module.copyAuthKeyValue();

    assert.equal(appended.length, 1);
    assert.equal(removed.length, 1);
    assert.equal(appended[0].value, 'sk_gom_test');
    assert.equal(module.authKeyCopyState.copied, true);
    assert.equal(module.authKeyCopyState.error, false);
});

test('fetchAuthKeys preserves existing rows and surfaces non-auth HTTP errors', async () => {
    const module = createAuthKeysModule({
        fetch: async () => ({
            status: 500,
            ok: false,
            statusText: 'Internal Server Error',
            async json() {
                return {
                    error: {
                        message: 'storage unavailable'
                    }
                };
            }
        }),
        console: {
            error() {}
        }
    });

    module.authKeys = [{ id: 'existing-key' }];
    module.headers = () => ({});
    module.handleFetchResponse = () => false;

    await module.fetchAuthKeys();

    assert.deepEqual(module.authKeys, [{ id: 'existing-key' }]);
    assert.equal(module.authKeyError, 'storage unavailable');
});

test('fetchAuthKeys preserves existing rows on authentication failures handled by handleFetchResponse', async () => {
    const module = createAuthKeysModule({
        fetch: async () => ({
            status: 401,
            ok: false,
            statusText: 'Unauthorized'
        })
    });

    module.authKeys = [{ id: 'existing-key' }];
    module.headers = () => ({});
    module.handleFetchResponse = (res) => {
        if (res.status === 401) {
            module.authError = true;
            module.needsAuth = true;
        }
        return false;
    };

    await module.fetchAuthKeys();

    assert.deepEqual(module.authKeys, [{ id: 'existing-key' }]);
    assert.equal(module.authKeyError, '');
    assert.equal(module.authError, true);
    assert.equal(module.needsAuth, true);
});

test('openAuthKeyForm and closeAuthKeyForm preserve an issued key instead of clearing it', () => {
    const module = createAuthKeysModule();
    module.authKeyIssuedValue = 'sk_gom_once';

    module.openAuthKeyForm();
    assert.equal(module.authKeyFormOpen, true);
    assert.equal(module.authKeyIssuedValue, 'sk_gom_once');

    module.closeAuthKeyForm();
    assert.equal(module.authKeyFormOpen, false);
    assert.equal(module.authKeyIssuedValue, 'sk_gom_once');
});

test('submitAuthKeyForm reopens the editor if issuance finishes after a manual close', async () => {
    let resolveResponse;
    const responsePromise = new Promise((resolve) => {
        resolveResponse = resolve;
    });
    const module = createAuthKeysModule({
        fetch: async () => responsePromise
    });

    module.headers = () => ({ 'Content-Type': 'application/json' });
    module.fetchAuthKeys = async () => {};
    module.authKeyFormOpen = true;
    module.authKeyForm = {
        name: 'ci-deploy',
        description: '',
        user_path: '',
        expires_at: ''
    };

    const submitPromise = module.submitAuthKeyForm();
    module.closeAuthKeyForm();

    assert.equal(module.authKeyFormOpen, false);
    assert.equal(module.authKeyFormSubmitting, true);

    resolveResponse({
        status: 201,
        async json() {
            return { value: 'sk_gom_async' };
        }
    });

    await submitPromise;

    assert.equal(module.authKeyFormOpen, true);
    assert.equal(module.authKeyIssuedValue, 'sk_gom_async');
    assert.equal(module.authKeyFormSubmitting, false);
});
