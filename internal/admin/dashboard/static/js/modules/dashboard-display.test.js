const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function createLocalStorage(initial = {}) {
    const values = { ...initial };
    return {
        getItem(key) {
            return Object.prototype.hasOwnProperty.call(values, key) ? values[key] : null;
        },
        setItem(key, value) {
            values[key] = String(value);
        },
        removeItem(key) {
            delete values[key];
        },
        values
    };
}

function loadDashboardApp(overrides = {}) {
    const dashboardSource = fs.readFileSync(path.join(__dirname, '../dashboard.js'), 'utf8');
    const window = {
        localStorage: createLocalStorage(),
        location: { pathname: '/admin/dashboard/usage' },
        matchMedia() {
            return { addEventListener() {} };
        },
        addEventListener() {},
        ...(overrides.window || {})
    };
    const context = {
        console,
        Date,
        Intl,
        setTimeout,
        clearTimeout,
        requestAnimationFrame(callback) {
            callback();
        },
        history: { pushState() {} },
        document: {
            documentElement: {
                removeAttribute() {},
                setAttribute() {}
            },
            getElementById() {
                return null;
            }
        },
        getComputedStyle() {
            return {
                getPropertyValue() {
                    return '';
                }
            };
        },
        localStorage: window.localStorage,
        ...overrides.context,
        window
    };

    vm.createContext(context);
    vm.runInContext(dashboardSource, context);
    return context.dashboard();
}

test('qualifiedModelDisplay keeps provider identity for nested provider model IDs', () => {
    const app = loadDashboardApp();

    assert.equal(
        app.qualifiedModelDisplay({ provider: 'openrouter', model: 'openai/gpt-5-nano' }),
        'openrouter/openai/gpt-5-nano'
    );
    assert.equal(
        app.qualifiedModelDisplay({ provider: 'openai', model: 'gpt-5-nano' }),
        'openai/gpt-5-nano'
    );
});

test('qualifiedModelDisplay does not duplicate an existing exact provider prefix', () => {
    const app = loadDashboardApp();

    assert.equal(
        app.qualifiedModelDisplay({ provider: 'openai', model: 'openai/gpt-5-nano' }),
        'openai/gpt-5-nano'
    );
    assert.equal(
        app.qualifiedResolvedModelDisplay({ provider_name: 'primary-openai', resolved_model: 'gpt-5-nano' }),
        'primary-openai/gpt-5-nano'
    );
});

test('system theme media changes rerender all dashboard charts', () => {
    let mediaChangeHandler = null;
    const app = loadDashboardApp({
        window: {
            location: { pathname: '/admin/dashboard/overview' },
            matchMedia() {
                return {
                    addEventListener(event, handler) {
                        if (event === 'change') {
                            mediaChangeHandler = handler;
                        }
                    }
                };
            }
        }
    });
    let overviewCalls = 0;
    let modelCalls = 0;
    let userPathCalls = 0;

    app.fetchAll = () => {};
    app.renderChart = () => {
        overviewCalls++;
    };
    app.renderBarChart = () => {
        modelCalls++;
    };
    app.renderUserPathChart = () => {
        userPathCalls++;
    };

    app.init();
    assert.equal(typeof mediaChangeHandler, 'function');

    overviewCalls = 0;
    modelCalls = 0;
    userPathCalls = 0;
    mediaChangeHandler();

    assert.equal(overviewCalls, 1);
    assert.equal(modelCalls, 1);
    assert.equal(userPathCalls, 1);

    app.theme = 'dark';
    mediaChangeHandler();

    assert.equal(overviewCalls, 1);
    assert.equal(modelCalls, 1);
    assert.equal(userPathCalls, 1);
});

test('unauthorized dashboard responses open the auth dialog', () => {
    const app = loadDashboardApp();
    const request = app.requestOptions();

    const handled = app.handleFetchResponse({
        ok: false,
        status: 401,
        statusText: 'Unauthorized'
    }, 'models', request);

    assert.equal(handled, false);
    assert.equal(app.authError, true);
    assert.equal(app.needsAuth, true);
    assert.equal(app.authDialogOpen, true);
});

test('stale unauthorized dashboard responses do not reopen the auth dialog', () => {
    const app = loadDashboardApp();
    const staleRequest = app.requestOptions();

    app.authRequestGeneration++;
    app.authError = false;
    app.needsAuth = false;
    app.authDialogOpen = false;

    const handled = app.handleFetchResponse({
        ok: false,
        status: 401,
        statusText: 'Unauthorized'
    }, 'categories', staleRequest);

    assert.equal(handled, app.staleAuthResponseResult());
    assert.equal(app.authError, false);
    assert.equal(app.needsAuth, false);
    assert.equal(app.authDialogOpen, false);
});

test('stale unauthorized category responses preserve existing categories', async () => {
    const existingCategories = [{ category: 'chat', count: 2 }];
    const app = loadDashboardApp({
        context: {
            fetch: async () => ({
                ok: false,
                status: 401,
                statusText: 'Unauthorized'
            })
        }
    });
    const staleRequest = app.requestOptions();
    app.requestOptions = () => staleRequest;
    app.categories = existingCategories;
    app.authRequestGeneration++;

    await app.fetchCategories();

    assert.equal(app.categories, existingCategories);
    assert.equal(app.authError, false);
    assert.equal(app.needsAuth, false);
    assert.equal(app.authDialogOpen, false);
});

test('init restores a persisted API key from browser storage', () => {
    const storage = createLocalStorage({
        gomodel_api_key: 'existing-token',
        gomodel_theme: 'dark'
    });
    const app = loadDashboardApp({
        window: {
            localStorage: storage,
            location: { pathname: '/admin/dashboard/overview' }
        }
    });

    app.fetchAll = () => {};
    app.renderChart = () => {};

    app.init();

    assert.equal(app.apiKey, 'existing-token');
    assert.equal(storage.getItem('gomodel_api_key'), 'existing-token');
    assert.equal(app.theme, 'dark');
});

test('submitApiKey trims bearer input, persists it, and refreshes dashboard data', () => {
    const storage = createLocalStorage();
    const app = loadDashboardApp({
        window: { localStorage: storage }
    });
    let fetches = 0;
    app.fetchAll = () => {
        fetches++;
    };

    app.authDialogOpen = true;
    app.apiKey = '  Bearer secret-token  ';
    app.submitApiKey();

    assert.equal(app.apiKey, 'secret-token');
    assert.equal(app.authRequestGeneration, 1);
    assert.equal(storage.getItem('gomodel_api_key'), 'secret-token');
    assert.equal(app.authDialogOpen, false);
    assert.equal(fetches, 1);
});

test('normalizeApiKey treats a bare bearer scheme as empty', () => {
    const app = loadDashboardApp();

    assert.equal(app.normalizeApiKey('Bearer'), '');
    assert.equal(app.normalizeApiKey('Bearer   '), '');
    assert.equal(app.normalizeApiKey('Bearer secret-token'), 'secret-token');
});

test('hasApiKey reflects trimmed bearer input for the sidebar change action', () => {
    const app = loadDashboardApp();

    app.apiKey = '';
    assert.equal(app.hasApiKey(), false);

    app.apiKey = '  Bearer secret-token  ';
    assert.equal(app.hasApiKey(), true);
});

test('submitApiKey rejects blank input without unlocking dashboard', () => {
    const storage = createLocalStorage();
    const app = loadDashboardApp({
        window: { localStorage: storage }
    });
    let fetches = 0;
    app.fetchAll = () => {
        fetches++;
    };

    app.authDialogOpen = true;
    app.apiKey = '   ';
    app.submitApiKey();

    assert.equal(app.apiKey, '');
    assert.equal(app.authRequestGeneration, 0);
    assert.equal(storage.getItem('gomodel_api_key'), null);
    assert.equal(app.authError, true);
    assert.equal(app.needsAuth, true);
    assert.equal(app.authDialogOpen, true);
    assert.equal(fetches, 0);
});

test('submitApiKey and headers reject a bare bearer scheme without sending authorization', () => {
    const storage = createLocalStorage();
    const app = loadDashboardApp({
        window: { localStorage: storage }
    });
    let fetches = 0;
    app.fetchAll = () => {
        fetches++;
    };

    app.authDialogOpen = true;
    app.apiKey = 'Bearer   ';
    app.submitApiKey();

    assert.equal(app.apiKey, '');
    assert.equal(app.authRequestGeneration, 0);
    assert.equal(storage.getItem('gomodel_api_key'), null);
    assert.equal(app.authError, true);
    assert.equal(app.needsAuth, true);
    assert.equal(app.authDialogOpen, true);
    assert.equal(fetches, 0);

    app.apiKey = 'Bearer';
    assert.equal(Object.prototype.hasOwnProperty.call(app.headers(), 'Authorization'), false);
});

test('headers accept a pasted bearer value without duplicating the prefix', () => {
    const app = loadDashboardApp();

    app.apiKey = 'Bearer secret-token';

    assert.equal(app.headers().Authorization, 'Bearer secret-token');
});
