const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadDashboardApp(overrides = {}) {
    const dashboardSource = fs.readFileSync(path.join(__dirname, '../dashboard.js'), 'utf8');
    const window = {
        localStorage: {
            getItem() {
                return null;
            },
            setItem() {},
            removeItem() {}
        },
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
