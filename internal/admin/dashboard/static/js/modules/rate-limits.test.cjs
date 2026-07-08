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

test('fetchRateLimitsPage waits for the runtime flags before calling a disabled endpoint', async () => {
    const calls = [];
    const module = createRateLimitsModule({
        fetch(url) {
            calls.push(url);
            return Promise.resolve({ ok: true, json: async () => ({ rate_limits: [] }) });
        }
    });
    module.headers = () => ({});
    module.handleFetchResponse = () => true;

    // Mirror the dashboard composition: the flag is unknown until the runtime
    // config lands, and only then reports the feature as disabled. Reading the
    // gate too early falls back to its default and hits /admin/rate-limits,
    // which 503s when the feature is off.
    module.workflowRuntimeConfig = {};
    module.workflowRuntimeBooleanFlag = (name, fallback) => {
        const value = String(module.workflowRuntimeConfig[name] || '').trim().toLowerCase();
        return value === '' ? !!fallback : value === 'on' || value === 'true' || value === '1';
    };
    module.ensureWorkflowRuntimeConfig = async () => {
        await Promise.resolve();
        module.workflowRuntimeConfig = { RATE_LIMITS_ENABLED: 'off' };
    };

    await module.fetchRateLimitsPage();

    assert.deepEqual(calls, [], 'no request should be issued while rate limits are disabled');
    assert.equal(module.rateLimitsAvailable, false);
    assert.equal(module.rateLimits.length, 0);
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

test('rateLimitNormalizedIdentity mirrors server normalization per scope', () => {
    const module = createRateLimitsModule();
    assert.equal(
        module.rateLimitNormalizedIdentity('provider', ' OpenAI ', 60),
        module.rateLimitNormalizedIdentity('provider', 'openai', 60)
    );
    assert.equal(
        module.rateLimitNormalizedIdentity('model', 'OpenAI/GPT-4o', 60),
        module.rateLimitNormalizedIdentity('model', 'openai/gpt-4o', 60)
    );
    assert.equal(
        module.rateLimitNormalizedIdentity('user_path', 'team/alpha/', 60),
        module.rateLimitNormalizedIdentity('user_path', '/team/alpha', 60)
    );
    assert.notEqual(
        module.rateLimitNormalizedIdentity('user_path', '/team', 60),
        module.rateLimitNormalizedIdentity('user_path', '/team', 3600)
    );
});

test('rateLimitIdentityMoved detects real moves but not respellings', () => {
    const module = createRateLimitsModule();
    module.rateLimitEditingOriginal = { scope: 'model', subject: 'openai/gpt-4o', period_seconds: 60 };

    const payload = (subject, seconds) => ({ scope: 'model', subject, limit_key: { period_seconds: seconds } });
    assert.equal(module.rateLimitIdentityMoved(payload('OpenAI/GPT-4o', 60)), false);
    assert.equal(module.rateLimitIdentityMoved(payload('openai/gpt-4o-mini', 60)), true);
    assert.equal(module.rateLimitIdentityMoved(payload('openai/gpt-4o', 3600)), true);

    module.rateLimitEditingOriginal = null;
    assert.equal(module.rateLimitIdentityMoved(payload('anything', 60)), false);
});

test('submitRateLimitForm moves a rule by creating the new key then deleting the old', async () => {
    const calls = [];
    const module = createRateLimitsModule({
        fetch: async (url, options) => {
            calls.push({ url, method: options.method, body: JSON.parse(options.body) });
            return { status: 200, json: async () => ({ rate_limits: [] }) };
        }
    });
    module.requestOptions = (options) => options;
    module.handleFetchResponse = () => true;

    module.rateLimitFormOpen = true;
    module.rateLimitEditing = true;
    module.rateLimitEditingOriginal = { scope: 'user_path', subject: '/team', period_seconds: 60 };
    module.rateLimitForm = { scope: 'user_path', subject: '/team', period: 'hour', period_seconds: 3600, max_requests: '5', max_tokens: '' };
    await module.submitRateLimitForm();

    assert.equal(calls.length, 2);
    assert.equal(calls[0].method, 'PUT');
    assert.equal(calls[0].body.limit_key.period_seconds, 3600);
    assert.equal(calls[1].method, 'DELETE');
    assert.equal(JSON.stringify(calls[1].body), JSON.stringify({
        scope: 'user_path',
        subject: '/team',
        limit_key: { period_seconds: 60 }
    }));
    assert.equal(module.rateLimitFormOpen, false);
    assert.match(module.rateLimitNotice, /moved/i);
});

test('submitRateLimitForm updates in place when the identity is unchanged', async () => {
    const calls = [];
    const module = createRateLimitsModule({
        fetch: async (url, options) => {
            calls.push({ method: options.method });
            return { status: 200, json: async () => ({ rate_limits: [] }) };
        }
    });
    module.requestOptions = (options) => options;
    module.handleFetchResponse = () => true;

    module.rateLimitEditing = true;
    module.rateLimitEditingOriginal = { scope: 'user_path', subject: '/team', period_seconds: 60 };
    module.rateLimitForm = { scope: 'user_path', subject: '/team', period: 'minute', period_seconds: 60, max_requests: '9', max_tokens: '' };
    await module.submitRateLimitForm();

    assert.deepEqual(calls.map((call) => call.method), ['PUT']);
    assert.match(module.rateLimitNotice, /saved/i);
});

test('submitRateLimitForm keeps the form open when removing the old rule fails', async () => {
    const module = createRateLimitsModule({
        fetch: async (url, options) => {
            if (options.method === 'DELETE') {
                return { status: 500, json: async () => ({ error: { message: 'boom' } }) };
            }
            return { status: 200, json: async () => ({ rate_limits: [] }) };
        }
    });
    module.requestOptions = (options) => options;
    module.handleFetchResponse = (res) => res.status === 200;

    module.rateLimitFormOpen = true;
    module.rateLimitEditing = true;
    module.rateLimitEditingOriginal = { scope: 'user_path', subject: '/team', period_seconds: 60 };
    module.rateLimitForm = { scope: 'user_path', subject: '/other', period: 'minute', period_seconds: 60, max_requests: '9', max_tokens: '' };
    await module.submitRateLimitForm();

    assert.equal(module.rateLimitFormOpen, true);
    assert.match(module.rateLimitFormError, /boom/);
});

test('inspector sections group model, provider, and global rules', () => {
    const module = createRateLimitsModule();
    module.rateLimits = [
        { scope: 'model', subject: 'openai/gpt-4o', period_seconds: 60 },
        { scope: 'model', subject: 'gpt-4o', period_seconds: 3600 },
        { scope: 'model', subject: 'gpt-4o-mini', period_seconds: 60 },
        { scope: 'provider', subject: 'openai', period_seconds: 0 },
        { scope: 'provider', subject: 'anthropic', period_seconds: 60 },
        { scope: 'user_path', subject: '/', user_path: '/', period_seconds: 60 },
        { scope: 'user_path', subject: '/team', user_path: '/team', period_seconds: 60 }
    ];

    module.rateLimitInspector = { kind: 'model', provider: 'openai', model: 'GPT-4o', title: 'gpt-4o' };
    const sections = module.rateLimitInspectorSections();
    assert.equal(JSON.stringify(sections.map((section) => section.key)), JSON.stringify(['model', 'provider', 'global']));
    // Both the qualified and the bare rule cover this model; the other model does not.
    assert.equal(JSON.stringify(sections[0].items.map((item) => item.subject)), JSON.stringify(['openai/gpt-4o', 'gpt-4o']));
    assert.equal(sections[0].subject, 'openai/GPT-4o');
    assert.equal(JSON.stringify(sections[1].items.map((item) => item.subject)), JSON.stringify(['openai']));
    // Only root user-path rules are global; /team is per-consumer.
    assert.equal(JSON.stringify(sections[2].items.map((item) => item.subject)), JSON.stringify(['/']));

    module.rateLimitInspector = { kind: 'provider', provider: 'anthropic', model: '', title: 'anthropic' };
    const providerSections = module.rateLimitInspectorSections();
    assert.equal(JSON.stringify(providerSections.map((section) => section.key)), JSON.stringify(['provider', 'global']));
    assert.equal(JSON.stringify(providerSections[0].items.map((item) => item.subject)), JSON.stringify(['anthropic']));
});

test('inspector opens the editor prefilled and returns on close', () => {
    const module = createRateLimitsModule();
    module.rateLimitInspectorOpen = true;

    module.openRateLimitFormFromInspector('provider', 'openai');
    assert.equal(module.rateLimitInspectorOpen, false);
    assert.equal(module.rateLimitFormOpen, true);
    assert.equal(module.rateLimitForm.scope, 'provider');
    assert.equal(module.rateLimitForm.subject, 'openai');
    assert.equal(module.rateLimitEditing, false);

    module.closeRateLimitForm();
    assert.equal(module.rateLimitFormOpen, false);
    assert.equal(module.rateLimitInspectorOpen, true);

    // Editing an existing rule from the inspector keeps its values.
    module.openRateLimitFormFromInspector(null, null, { scope: 'provider', subject: 'openai', period_seconds: 60, max_requests: 5 });
    assert.equal(module.rateLimitEditing, true);
    assert.equal(module.rateLimitForm.max_requests, '5');
    assert.equal(JSON.stringify(module.rateLimitEditingOriginal), JSON.stringify({ scope: 'provider', subject: 'openai', period_seconds: 60 }));
});

test('pressure percent, style, and class ramp with usage', () => {
    const module = createRateLimitsModule();

    const low = { period_seconds: 60, max_requests: 100, requests_used: 10, max_tokens: 1000, tokens_used: 200 };
    assert.equal(module.rateLimitPressurePercent(low), 20);
    assert.equal(module.rateLimitPressureStyle(low), '--rate-limit-pressure: 20%');
    assert.equal(module.rateLimitPressureClass(low), 'rate-limit-pressure-row');

    const high = { period_seconds: 60, max_requests: 100, requests_used: 80 };
    assert.equal(module.rateLimitPressureClass(high), 'rate-limit-pressure-row rate-limit-pressure-high');

    const full = { period_seconds: 60, max_tokens: 100, tokens_used: 250 };
    assert.equal(module.rateLimitPressurePercent(full), 100);
    assert.equal(module.rateLimitPressureClass(full), 'rate-limit-pressure-row rate-limit-pressure-full');

    const concurrent = { period_seconds: 0, max_requests: 4, in_flight: 3 };
    assert.equal(module.rateLimitPressurePercent(concurrent), 75);
    assert.equal(module.rateLimitPressureClass(concurrent), 'rate-limit-pressure-row rate-limit-pressure-high');
});

test('gauge indicator distinguishes direct, inherited, and no limits', () => {
    const module = createRateLimitsModule();
    module.rateLimits = [
        { scope: 'model', subject: 'openai/gpt-4o', period_seconds: 60 },
        { scope: 'provider', subject: 'openai', period_seconds: 60 }
    ];

    const gpt4o = { provider_name: 'OpenAI', model: { id: 'gpt-4o' } };
    const gpt4oMini = { provider_name: 'openai', model: { id: 'gpt-4o-mini' } };
    const claude = { provider_name: 'anthropic', model: { id: 'claude' } };

    // Direct model rule → fully painted.
    assert.equal(module.rateLimitGaugeClassForModel(gpt4o), 'table-action-btn-active');
    // Only the provider rule throttles this model → half painted.
    assert.equal(module.rateLimitGaugeClassForModel(gpt4oMini), 'rate-limit-gauge-inherited');
    // Nothing applies.
    assert.equal(module.rateLimitGaugeClassForModel(claude), '');

    assert.equal(module.rateLimitGaugeClassForProvider({ provider_name: 'openai' }), 'table-action-btn-active');
    assert.equal(module.rateLimitGaugeClassForProvider({ provider_name: 'anthropic' }), '');

    // A root user-path rule throttles everything → inherited for all. The
    // rules list is replaced (never mutated) exactly like after a fetch, which
    // is what invalidates the per-row gauge memo.
    module.rateLimits = module.rateLimits.concat([{ scope: 'user_path', subject: '/', user_path: '/', period_seconds: 60 }]);
    assert.equal(module.rateLimitGaugeClassForModel(claude), 'rate-limit-gauge-inherited');
    assert.equal(module.rateLimitGaugeClassForProvider({ provider_name: 'anthropic' }), 'rate-limit-gauge-inherited');

    assert.match(module.rateLimitGaugeTitle('gpt-4o', 'table-action-btn-active'), /direct limits configured/);
    assert.match(module.rateLimitGaugeTitle('claude', 'rate-limit-gauge-inherited'), /inherited limits apply/);
    assert.equal(module.rateLimitGaugeTitle('claude', ''), 'Rate limits for claude');
});
