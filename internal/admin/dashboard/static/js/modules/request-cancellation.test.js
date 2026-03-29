const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function createLocalStorage(seed = {}) {
    const data = new Map(Object.entries(seed));
    return {
        getItem(key) {
            return data.has(key) ? data.get(key) : null;
        },
        setItem(key, value) {
            data.set(key, String(value));
        },
        removeItem(key) {
            data.delete(key);
        }
    };
}

class FakeAbortSignal {
    constructor() {
        this.aborted = false;
        this.listeners = [];
    }

    addEventListener(type, listener) {
        if (type === 'abort') {
            this.listeners.push(listener);
        }
    }

    removeEventListener(type, listener) {
        if (type !== 'abort') {
            return;
        }
        this.listeners = this.listeners.filter((entry) => entry !== listener);
    }
}

class FakeAbortController {
    constructor() {
        this.signal = new FakeAbortSignal();
    }

    abort() {
        if (this.signal.aborted) {
            return;
        }
        this.signal.aborted = true;
        this.signal.listeners.slice().forEach((listener) => listener());
    }
}

function createPendingFetchQueue() {
    const requests = [];

    function fetch(url, options = {}) {
        return new Promise((resolve, reject) => {
            const request = {
                url,
                options,
                resolve,
                reject
            };
            requests.push(request);

            const signal = options.signal;
            if (signal && typeof signal.addEventListener === 'function') {
                signal.addEventListener('abort', () => {
                    const error = new Error('aborted');
                    error.name = 'AbortError';
                    reject(error);
                });
            }
        });
    }

    return { fetch, requests };
}

function createNonAbortableFetchQueue() {
    const requests = [];

    function fetch(url, options = {}) {
        return new Promise((resolve, reject) => {
            requests.push({
                url,
                options,
                resolve,
                reject
            });
        });
    }

    return { fetch, requests };
}

function jsonResponse(payload) {
    return {
        ok: true,
        status: 200,
        statusText: 'OK',
        json: async() => payload
    };
}

function loadDashboardApp(overrides = {}) {
    const sources = [
        fs.readFileSync(path.join(__dirname, 'usage.js'), 'utf8'),
        fs.readFileSync(path.join(__dirname, 'contribution-calendar.js'), 'utf8'),
        fs.readFileSync(path.join(__dirname, '../dashboard.js'), 'utf8')
    ];

    const window = {
        localStorage: createLocalStorage(),
        location: { pathname: '/admin/dashboard/overview' },
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
        AbortController: FakeAbortController,
        ...overrides,
        window
    };

    vm.createContext(context);
    sources.forEach((source) => vm.runInContext(source, context));
    return context.dashboard();
}

test('fetchUsage aborts stale in-flight requests before applying new data', async() => {
    const queue = createPendingFetchQueue();
    const app = loadDashboardApp({ fetch: queue.fetch });
    let renderChartCalls = 0;

    app.renderChart = () => {
        renderChartCalls++;
    };

    const firstFetch = app.fetchUsage();
    assert.equal(queue.requests.length, 2);

    const firstSignals = queue.requests.map((request) => request.options.signal);
    const secondFetch = app.fetchUsage();

    assert.equal(queue.requests.length, 4);
    firstSignals.forEach((signal) => assert.equal(signal.aborted, true));

    queue.requests[2].resolve(jsonResponse({ total_requests: 7 }));
    queue.requests[3].resolve(jsonResponse([{ date: '2026-03-29', input_tokens: 3, output_tokens: 4 }]));

    await Promise.all([firstFetch, secondFetch]);

    assert.equal(app.summary.total_requests, 7);
    assert.equal(JSON.stringify(app.daily), JSON.stringify([{ date: '2026-03-29', input_tokens: 3, output_tokens: 4 }]));
    assert.equal(renderChartCalls, 1);
});

test('fetchModels aborts stale in-flight requests before applying new data', async() => {
    const queue = createPendingFetchQueue();
    const app = loadDashboardApp({ fetch: queue.fetch });

    const firstFetch = app.fetchModels();
    assert.equal(queue.requests.length, 1);
    const firstSignal = queue.requests[0].options.signal;

    const secondFetch = app.fetchModels();
    assert.equal(firstSignal.aborted, true);
    assert.equal(queue.requests.length, 2);

    queue.requests[1].resolve(jsonResponse([{ provider_type: 'openai', model: { id: 'gpt-5' } }]));

    await Promise.all([firstFetch, secondFetch]);

    assert.equal(
        JSON.stringify(app.models),
        JSON.stringify([{ provider_type: 'openai', model: { id: 'gpt-5' } }])
    );
});

test('fetchModels ignores stale unauthorized responses from superseded requests', async() => {
    const queue = createNonAbortableFetchQueue();
    const app = loadDashboardApp({ fetch: queue.fetch });
    const originalModels = [{ provider_type: 'openai', model: { id: 'existing-model' } }];
    app.models = originalModels.slice();

    const firstFetch = app.fetchModels();
    assert.equal(queue.requests.length, 1);
    const firstSignal = queue.requests[0].options.signal;

    const secondFetch = app.fetchModels();
    assert.equal(queue.requests.length, 2);
    assert.equal(firstSignal.aborted, true);

    queue.requests[0].resolve({
        ok: false,
        status: 401,
        statusText: 'Unauthorized',
        json: async() => ({})
    });
    await firstFetch;

    assert.equal(app.authError, false);
    assert.equal(app.needsAuth, false);
    assert.equal(JSON.stringify(app.models), JSON.stringify(originalModels));

    queue.requests[1].resolve(jsonResponse([{ provider_type: 'openai', model: { id: 'gpt-5' } }]));
    await secondFetch;

    assert.equal(app.authError, false);
    assert.equal(app.needsAuth, false);
    assert.equal(
        JSON.stringify(app.models),
        JSON.stringify([{ provider_type: 'openai', model: { id: 'gpt-5' } }])
    );
});

test('fetchModels ignores stale errors from superseded requests', async() => {
    const queue = createNonAbortableFetchQueue();
    const app = loadDashboardApp({ fetch: queue.fetch });
    const originalModels = [{ provider_type: 'openai', model: { id: 'existing-model' } }];
    app.models = originalModels.slice();

    const firstFetch = app.fetchModels();
    assert.equal(queue.requests.length, 1);
    const firstSignal = queue.requests[0].options.signal;

    const secondFetch = app.fetchModels();
    assert.equal(queue.requests.length, 2);
    assert.equal(firstSignal.aborted, true);

    queue.requests[0].reject(new Error('stale models failure'));
    await firstFetch;

    assert.equal(JSON.stringify(app.models), JSON.stringify(originalModels));

    queue.requests[1].resolve(jsonResponse([{ provider_type: 'openai', model: { id: 'gpt-5' } }]));
    await secondFetch;

    assert.equal(
        JSON.stringify(app.models),
        JSON.stringify([{ provider_type: 'openai', model: { id: 'gpt-5' } }])
    );
});

test('fetchCalendarData aborts stale in-flight requests before applying new data', async() => {
    const queue = createPendingFetchQueue();
    const app = loadDashboardApp({ fetch: queue.fetch });

    const firstFetch = app.fetchCalendarData();
    assert.equal(queue.requests.length, 1);
    const firstSignal = queue.requests[0].options.signal;

    const secondFetch = app.fetchCalendarData();
    assert.equal(firstSignal.aborted, true);
    assert.equal(queue.requests.length, 2);

    queue.requests[1].resolve(jsonResponse([{ date: '2026-03-29', total_tokens: 11 }]));

    await Promise.all([firstFetch, secondFetch]);

    assert.equal(
        JSON.stringify(app.calendarData),
        JSON.stringify([{ date: '2026-03-29', total_tokens: 11 }])
    );
});

test('fetchCalendarData ignores stale unauthorized responses while a newer request is active', async() => {
    const queue = createNonAbortableFetchQueue();
    const app = loadDashboardApp({ fetch: queue.fetch });
    const originalCalendarData = [{ date: '2026-03-28', total_tokens: 3 }];
    app.calendarData = originalCalendarData.slice();

    const firstFetch = app.fetchCalendarData();
    assert.equal(queue.requests.length, 1);
    const firstSignal = queue.requests[0].options.signal;

    const secondFetch = app.fetchCalendarData();
    assert.equal(queue.requests.length, 2);
    assert.equal(firstSignal.aborted, true);

    queue.requests[0].resolve({
        ok: false,
        status: 401,
        statusText: 'Unauthorized',
        json: async() => ({})
    });
    await firstFetch;

    assert.equal(JSON.stringify(app.calendarData), JSON.stringify(originalCalendarData));
    assert.equal(app.calendarLoading, true);
    assert.notEqual(app._calendarFetchController, null);

    queue.requests[1].resolve(jsonResponse([{ date: '2026-03-29', total_tokens: 11 }]));
    await secondFetch;

    assert.equal(app.calendarLoading, false);
    assert.equal(
        JSON.stringify(app.calendarData),
        JSON.stringify([{ date: '2026-03-29', total_tokens: 11 }])
    );
});

test('fetchCalendarData ignores stale errors while a newer request is active', async() => {
    const queue = createNonAbortableFetchQueue();
    const app = loadDashboardApp({ fetch: queue.fetch });
    const originalCalendarData = [{ date: '2026-03-28', total_tokens: 3 }];
    app.calendarData = originalCalendarData.slice();

    const firstFetch = app.fetchCalendarData();
    assert.equal(queue.requests.length, 1);
    const firstSignal = queue.requests[0].options.signal;

    const secondFetch = app.fetchCalendarData();
    assert.equal(queue.requests.length, 2);
    assert.equal(firstSignal.aborted, true);

    queue.requests[0].reject(new Error('stale calendar failure'));
    await firstFetch;

    assert.equal(JSON.stringify(app.calendarData), JSON.stringify(originalCalendarData));
    assert.equal(app.calendarLoading, true);
    assert.notEqual(app._calendarFetchController, null);

    queue.requests[1].resolve(jsonResponse([{ date: '2026-03-29', total_tokens: 11 }]));
    await secondFetch;

    assert.equal(app.calendarLoading, false);
    assert.equal(
        JSON.stringify(app.calendarData),
        JSON.stringify([{ date: '2026-03-29', total_tokens: 11 }])
    );
});
