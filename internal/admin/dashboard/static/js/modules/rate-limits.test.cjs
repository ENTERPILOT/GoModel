const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadRateLimitsModuleFactory(overrides = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'rate-limits.js'), 'utf8');
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
    vm.runInContext(source, context);
    return context.window.dashboardRateLimitsModule;
}

function createRateLimitsModule(overrides) {
    const factory = loadRateLimitsModuleFactory(overrides);
    return factory();
}

test('rateLimitsEnabled defaults on and respects the runtime flag', () => {
    const module = createRateLimitsModule();
    assert.equal(module.rateLimitsEnabled(), true);

    module.workflowRuntimeBooleanFlag = (key, fallback) => {
        assert.equal(key, 'RATE_LIMITS_ENABLED');
        assert.equal(fallback, true);
        return false;
    };
    assert.equal(module.rateLimitsEnabled(), false);
});

test('period helpers map names and seconds both ways', () => {
    const module = createRateLimitsModule();
    assert.equal(module.rateLimitPeriodSeconds('minute'), 60);
    assert.equal(module.rateLimitPeriodSeconds('hour'), 3600);
    assert.equal(module.rateLimitPeriodSeconds('day'), 86400);
    assert.equal(module.rateLimitPeriodSeconds('concurrent'), 0);
    assert.equal(module.rateLimitPeriodSeconds('custom'), -1);

    assert.equal(module.rateLimitPeriodFromSeconds(60), 'minute');
    assert.equal(module.rateLimitPeriodFromSeconds(0), 'concurrent');
    assert.equal(module.rateLimitPeriodFromSeconds(7200), 'custom');
});

test('rateLimitFormPayload validates and builds the upsert payload', () => {
    const module = createRateLimitsModule();

    module.rateLimitForm = {
        user_path: ' /team/alpha ',
        period: 'minute',
        period_seconds: 60,
        max_requests: '100',
        max_tokens: '5000'
    };
    const { payload, error } = module.rateLimitFormPayload();
    assert.equal(error, undefined);
    assert.equal(JSON.stringify(payload), JSON.stringify({
        user_path: '/team/alpha',
        limit_key: { period_seconds: 60 },
        max_requests: 100,
        max_tokens: 5000
    }));

    module.rateLimitForm = { user_path: '/', period: 'minute', period_seconds: 60, max_requests: '', max_tokens: '' };
    assert.match(module.rateLimitFormPayload().error, /max requests, max tokens/i);

    module.rateLimitForm = { user_path: '/', period: 'concurrent', period_seconds: 0, max_requests: '5', max_tokens: '10' };
    assert.match(module.rateLimitFormPayload().error, /concurrent/i);

    module.rateLimitForm = { user_path: '/', period: 'minute', period_seconds: 60, max_requests: '-3', max_tokens: '' };
    assert.match(module.rateLimitFormPayload().error, /positive integer/i);

    module.rateLimitForm = { user_path: '/', period: 'custom', period_seconds: -5, max_requests: '5', max_tokens: '' };
    assert.match(module.rateLimitFormPayload().error, /period seconds/i);
});

test('filteredRateLimits sorts by path then period and filters', () => {
    const module = createRateLimitsModule();
    module.rateLimits = [
        { user_path: '/team', period_seconds: 86400, period_label: 'day' },
        { user_path: '/alpha', period_seconds: 60, period_label: 'minute' },
        { user_path: '/team', period_seconds: 0, period_label: 'concurrent' }
    ];
    const sorted = module.filteredRateLimits();
    assert.deepEqual(
        sorted.map((item) => module.rateLimitKey(item)),
        ['/alpha:60', '/team:0', '/team:86400']
    );

    module.rateLimitFilter = 'concurrent';
    const filtered = module.filteredRateLimits();
    assert.equal(filtered.length, 1);
    assert.equal(filtered[0].period_seconds, 0);
});

test('usage percent clamps to 0..100', () => {
    const module = createRateLimitsModule();
    assert.equal(module.rateLimitUsagePercent(50, 100), 50);
    assert.equal(module.rateLimitUsagePercent(200, 100), 100);
    assert.equal(module.rateLimitUsagePercent(-5, 100), 0);
    assert.equal(module.rateLimitUsagePercent(5, 0), 0);
    assert.equal(module.rateLimitUsagePercent('x', 100), 0);
});

test('config-sourced rules are read-only', () => {
    const module = createRateLimitsModule();
    assert.equal(module.rateLimitIsReadOnly({ source: 'config' }), true);
    assert.equal(module.rateLimitIsReadOnly({ source: 'manual' }), false);
    assert.equal(module.rateLimitSourceLabel({ source: 'config' }), 'config');
    assert.equal(module.rateLimitSourceLabel({}), 'manual');
});

test('fetchRateLimits handles 503 as feature unavailable', async () => {
    const module = createRateLimitsModule({
        fetch: async () => ({ status: 503 })
    });
    module.requestOptions = () => ({});
    module.handleFetchResponse = () => {
        throw new Error('must not be called for 503');
    };
    // Bind fetch through the module context: rely on global fetch override.
    global.fetch = async () => ({ status: 503 });
    try {
        await module.fetchRateLimits();
    } finally {
        delete global.fetch;
    }
    assert.equal(module.rateLimitsAvailable, false);
    assert.equal(JSON.stringify(module.rateLimits), '[]');
    assert.equal(module.rateLimitsLoading, false);
});
