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

test('syncRateLimitPeriodSeconds maps the period and clears hidden token limits', () => {
    const module = createRateLimitsModule();

    module.rateLimitForm = { scope: 'user_path', subject: '/', period: 'hour', period_seconds: 60, max_requests: '5', max_tokens: '1000' };
    module.syncRateLimitPeriodSeconds();
    assert.equal(module.rateLimitForm.period_seconds, 3600);
    assert.equal(module.rateLimitForm.max_tokens, '1000');

    // Switching to concurrent must zero the period and drop the now-hidden
    // token limit so it cannot invisibly block the save.
    module.rateLimitForm.period = 'concurrent';
    module.syncRateLimitPeriodSeconds();
    assert.equal(module.rateLimitForm.period_seconds, 0);
    assert.equal(module.rateLimitForm.max_tokens, '');

    // Custom keeps whatever seconds are already set for the user to edit.
    module.rateLimitForm.period = 'custom';
    module.rateLimitForm.period_seconds = 300;
    module.syncRateLimitPeriodSeconds();
    assert.equal(module.rateLimitForm.period_seconds, 300);
});

test('rateLimitFormPayload validates and builds the upsert payload', () => {
    const module = createRateLimitsModule();

    module.rateLimitForm = {
        scope: 'user_path',
        subject: ' /team/alpha ',
        period: 'minute',
        period_seconds: 60,
        max_requests: '100',
        max_tokens: '5000'
    };
    const { payload, error } = module.rateLimitFormPayload();
    assert.equal(error, undefined);
    assert.equal(JSON.stringify(payload), JSON.stringify({
        scope: 'user_path',
        subject: '/team/alpha',
        limit_key: { period_seconds: 60 },
        max_requests: 100,
        max_tokens: 5000
    }));

    module.rateLimitForm = { scope: 'user_path', subject: '/', period: 'minute', period_seconds: 60, max_requests: '', max_tokens: '' };
    assert.match(module.rateLimitFormPayload().error, /max requests, max tokens/i);

    module.rateLimitForm = { scope: 'user_path', subject: '/', period: 'concurrent', period_seconds: 0, max_requests: '5', max_tokens: '10' };
    assert.match(module.rateLimitFormPayload().error, /concurrent/i);

    module.rateLimitForm = { scope: 'user_path', subject: '/', period: 'minute', period_seconds: 60, max_requests: '-3', max_tokens: '' };
    assert.match(module.rateLimitFormPayload().error, /positive integer/i);

    module.rateLimitForm = { scope: 'user_path', subject: '/', period: 'custom', period_seconds: -5, max_requests: '5', max_tokens: '' };
    assert.match(module.rateLimitFormPayload().error, /period seconds/i);

    // Blank custom seconds must not coerce to 0 and submit a concurrent rule.
    module.rateLimitForm = { scope: 'user_path', subject: '/', period: 'custom', period_seconds: '', max_requests: '5', max_tokens: '' };
    assert.match(module.rateLimitFormPayload().error, /period seconds is required/i);

    // Explicit 0 is only valid for the concurrent period.
    module.rateLimitForm = { scope: 'user_path', subject: '/', period: 'custom', period_seconds: 0, max_requests: '5', max_tokens: '' };
    assert.match(module.rateLimitFormPayload().error, /concurrent/i);

    module.rateLimitForm = { scope: 'user_path', subject: '/', period: 'concurrent', period_seconds: 0, max_requests: '5', max_tokens: '' };
    const concurrent = module.rateLimitFormPayload();
    assert.equal(concurrent.error, undefined);
    assert.equal(concurrent.payload.limit_key.period_seconds, 0);
});

test('rateLimitFormPayload handles provider and model scopes', () => {
    const module = createRateLimitsModule();

    module.rateLimitForm = { scope: 'provider', subject: 'openai', period: 'minute', period_seconds: 60, max_requests: '500', max_tokens: '' };
    const provider = module.rateLimitFormPayload();
    assert.equal(provider.error, undefined);
    assert.equal(provider.payload.scope, 'provider');
    assert.equal(provider.payload.subject, 'openai');

    // A provider or model rule cannot be saved without its subject.
    module.rateLimitForm = { scope: 'provider', subject: '  ', period: 'minute', period_seconds: 60, max_requests: '5', max_tokens: '' };
    assert.match(module.rateLimitFormPayload().error, /provider name is required/i);

    module.rateLimitForm = { scope: 'model', subject: 'openai/gpt-4o', period: 'minute', period_seconds: 60, max_requests: '', max_tokens: '100000' };
    const model = module.rateLimitFormPayload();
    assert.equal(model.error, undefined);
    assert.equal(model.payload.scope, 'model');
    assert.equal(model.payload.subject, 'openai/gpt-4o');
    assert.equal(model.payload.max_tokens, 100000);
});

test('syncRateLimitScope resets the subject per scope', () => {
    const module = createRateLimitsModule();
    module.rateLimitForm = module.defaultRateLimitForm();

    module.rateLimitForm.scope = 'provider';
    module.syncRateLimitScope();
    assert.equal(module.rateLimitForm.subject, '');
    assert.equal(module.rateLimitSubjectFieldLabel(), 'Provider Name');
    assert.equal(module.rateLimitSubjectPlaceholder(), 'openai');

    module.rateLimitForm.scope = 'user_path';
    module.syncRateLimitScope();
    assert.equal(module.rateLimitForm.subject, '/');
    assert.equal(module.rateLimitSubjectFieldLabel(), 'User Path');
});

test('filteredRateLimits sorts by scope, subject, period and filters', () => {
    const module = createRateLimitsModule();
    module.rateLimits = [
        { scope: 'user_path', subject: '/team', user_path: '/team', period_seconds: 86400, period_label: 'day' },
        { scope: 'model', subject: 'openai/gpt-4o', period_seconds: 60, period_label: 'minute' },
        { scope: 'user_path', subject: '/alpha', user_path: '/alpha', period_seconds: 60, period_label: 'minute' },
        { scope: 'provider', subject: 'openai', period_seconds: 0, period_label: 'concurrent' },
        { scope: 'user_path', subject: '/team', user_path: '/team', period_seconds: 0, period_label: 'concurrent' }
    ];
    const sorted = module.filteredRateLimits();
    assert.deepEqual(
        sorted.map((item) => module.rateLimitKey(item)),
        [
            'user_path:/alpha:60',
            'user_path:/team:0',
            'user_path:/team:86400',
            'provider:openai:0',
            'model:openai/gpt-4o:60'
        ]
    );

    module.rateLimitFilter = 'provider';
    const filtered = module.filteredRateLimits();
    assert.equal(filtered.length, 1);
    assert.equal(filtered[0].subject, 'openai');

    // Items from a pre-scope server still key off user_path.
    assert.equal(module.rateLimitKey({ user_path: '/legacy', period_seconds: 60 }), 'user_path:/legacy:60');
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
    // fetchRateLimits resolves fetch lexically from the vm context the module
    // was loaded into, so the injected override below is the one it calls.
    const module = createRateLimitsModule({
        fetch: async () => ({ status: 503 })
    });
    module.requestOptions = () => ({});
    module.handleFetchResponse = () => {
        throw new Error('must not be called for 503');
    };
    await module.fetchRateLimits();
    assert.equal(module.rateLimitsAvailable, false);
    assert.equal(JSON.stringify(module.rateLimits), '[]');
    assert.equal(module.rateLimitsLoading, false);
});
