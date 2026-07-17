const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadProvidersModuleFactory(overrides = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'providers.js'), 'utf8');
    const context = {
        console,
        ...overrides,
        window: {
            ...(overrides.window || {})
        }
    };
    vm.createContext(context);
    vm.runInContext(source, context);
    return context.window.dashboardProvidersModule;
}

function createProvidersModule(overrides) {
    const factory = loadProvidersModuleFactory(overrides);
    return factory();
}

test('provider status summary and badge helpers map health states to stable classes', () => {
    const module = createProvidersModule();
    module.providerStatus = {
        summary: { total: 2, healthy: 1, degraded: 0, unhealthy: 1, overall_status: 'degraded' },
        providers: []
    };

    assert.equal(module.providerStatusSummaryClass(), 'is-degraded');
    assert.equal(module.providerStatusBadgeClass('healthy'), 'is-healthy');
    assert.equal(module.providerStatusBadgeClass('unhealthy'), 'is-unhealthy');
    assert.equal(module.providerStatusRatioText(), '1/2');
    assert.equal(module.providerStatusHasIssues(), true);
    assert.equal(module.providerStatusSummaryText(), '1 provider needs attention');

    module.providerStatus.summary = { total: 2, healthy: 2, degraded: 0, unhealthy: 0, overall_status: 'healthy' };
    assert.equal(module.providerStatusHasIssues(), false);
});

test('provider helper methods format configured models and resilience summaries', () => {
    const module = createProvidersModule();
    const provider = {
        config: {
            models: ['gpt-4o', 'gpt-4.1'],
            resilience: {
                retry: {
                    max_retries: 3,
                    initial_backoff: '1s',
                    max_backoff: '30s',
                    backoff_factor: 2,
                    jitter_factor: 0.1
                },
                circuit_breaker: {
                    failure_threshold: 5,
                    success_threshold: 2,
                    timeout: '30s'
                }
            }
        },
        runtime: {
            last_model_fetch_at: '2026-04-10T12:00:00Z',
            last_availability_check_at: '2026-04-10T11:55:00Z'
        }
    };

    assert.equal(module.providerModelsSummary(provider), 'gpt-4o, gpt-4.1');
    assert.equal(module.providerRetrySummary(provider), '3 retries, 1s initial, 30s max, factor 2, jitter 0.1');
    assert.equal(module.providerCircuitBreakerSummary(provider), '5 fail, 2 success, 30s timeout');
    assert.equal(module.providerLastChecked(provider), '2026-04-10T12:00:00Z');
    // The most recent of the two check timestamps wins, whichever side it is.
    assert.equal(module.providerLastChecked({
        runtime: {
            last_model_fetch_at: '2026-04-10T12:00:00Z',
            last_availability_check_at: '2026-04-10T12:30:00Z'
        }
    }), '2026-04-10T12:30:00Z');
    assert.equal(module.providerLastChecked({
        runtime: { last_availability_check_at: '2026-04-10T11:55:00Z' }
    }), '2026-04-10T11:55:00Z');
    assert.equal(module.providerTypeLabel({ name: 'openai-primary', type: 'openai' }), 'openai');
    assert.equal(module.providerTypeLabel({ name: 'openai', type: 'openai' }), '');
    assert.equal(module.providerTypeLabel({ name: 'azure-east', config: { type: 'azure' } }), 'azure');
});

test('providerDocUrl links provider types with docs and stays empty otherwise', () => {
    const module = createProvidersModule();

    // Types with a dedicated docs page.
    assert.equal(module.providerDocUrl({ type: 'anthropic' }), 'https://gomodel.enterpilot.io/docs/providers/anthropic');
    assert.equal(module.providerDocUrl({ config: { type: 'bedrock' } }), 'https://gomodel.enterpilot.io/docs/providers/bedrock');
    assert.equal(module.providerDocUrl({ type: 'bedrock-mantle' }), 'https://gomodel.enterpilot.io/docs/providers/bedrock-mantle');
    // Type slug differs from the docs slug.
    assert.equal(module.providerDocUrl({ type: 'opencode_go' }), 'https://gomodel.enterpilot.io/docs/providers/opencode-go');
    // Resolves even when the (type) label is hidden because name === type.
    assert.equal(module.providerDocUrl({ name: 'gemini', type: 'GEMINI' }), 'https://gomodel.enterpilot.io/docs/providers/gemini');
    // Types without a provider-specific doc → no link (no icon).
    assert.equal(module.providerDocUrl({ type: 'openai' }), '');
    assert.equal(module.providerDocUrl({ type: 'ollama' }), '');
    assert.equal(module.providerDocUrl({ name: 'mystery' }), '');
    assert.equal(module.providerDocUrl(null), '');
});

test('provider detail toggle starts collapsed, persists toggles, and last check formatting uses time-only text', () => {
    const storage = {
        values: new Map(),
        getItem(key) {
            return this.values.has(key) ? this.values.get(key) : null;
        },
        setItem(key, value) {
            this.values.set(key, String(value));
        }
    };
    const module = createProvidersModule({
        window: { localStorage: storage }
    });

    module.initProviderStatusPreferences();
    assert.equal(module.providerStatusDetailsExpanded, false);
    assert.equal(module.providerStatusDetailsToggleLabel(), 'Hide Details');
    assert.equal(storage.getItem('gomodel_provider_status_details_expanded'), 'false');

    module.toggleProviderStatusDetails();
    assert.equal(module.providerStatusDetailsExpanded, true);
    assert.equal(module.providerStatusDetailsToggleLabel(), 'Show Details');
    assert.equal(storage.getItem('gomodel_provider_status_details_expanded'), 'true');

    const reloadedModule = createProvidersModule({
        window: { localStorage: storage }
    });
    reloadedModule.initProviderStatusPreferences();
    assert.equal(reloadedModule.providerStatusDetailsExpanded, true);
    assert.equal(reloadedModule.providerStatusDetailsToggleLabel(), 'Show Details');
    assert.equal(storage.getItem('gomodel_provider_status_details_expanded'), 'true');

    module.formatTimestamp = (value) => value === '2026-04-10T12:00:00Z'
        ? '2026-04-10 14:00:00'
        : '-';

    const provider = {
        runtime: {
            last_model_fetch_at: '2026-04-10T12:00:00Z'
        }
    };

    assert.equal(module.providerLastCheckedTime(provider), '14:00:00');
    assert.equal(module.providerLastCheckedTitle(provider), '2026-04-10 14:00:00');
});

test('provider status polling refreshes while any provider is starting', async() => {
    const timers = [];
    const cleared = [];
    const responses = [
        {
            summary: { total: 1, healthy: 0, degraded: 1, unhealthy: 0, overall_status: 'degraded' },
            providers: [{ name: 'openai', status_label: 'Starting' }]
        },
        {
            summary: { total: 1, healthy: 1, degraded: 0, unhealthy: 0, overall_status: 'healthy' },
            providers: [{ name: 'openai', status_label: 'Healthy' }]
        }
    ];
    let fetches = 0;
    const module = createProvidersModule({
        window: {
            setTimeout(callback, ms) {
                const timer = { callback, ms };
                timers.push(timer);
                return timer;
            },
            clearTimeout(timer) {
                cleared.push(timer);
            }
        },
        fetch: async() => ({
            ok: true,
            status: 200,
            json: async() => responses[Math.min(fetches++, responses.length - 1)]
        })
    });

    module._startAbortableRequest = () => null;
    module._clearAbortableRequest = () => {};
    module.requestOptions = () => ({ headers: {} });
    module.handleFetchResponse = () => true;
    module.isStaleAuthFetchResult = () => false;

    await module.fetchProviderStatus();

    assert.equal(module.providerStatusNeedsPolling(), true);
    assert.equal(timers.length, 1);
    assert.equal(timers[0].ms, 3000);

    await timers[0].callback();

    assert.equal(fetches, 2);
    assert.equal(module.providerStatusNeedsPolling(), false);
    assert.equal(module.providerStatusPollTimer, null);
});

test('fetchProviderStatus ignores responses whose request signal was aborted', async() => {
    const signal = { aborted: false };
    let handled = 0;
    const existingStatus = {
        summary: { total: 1, healthy: 1, degraded: 0, unhealthy: 0, overall_status: 'healthy' },
        providers: [{ name: 'openai' }]
    };
    const module = createProvidersModule({
        fetch: async(_url, options) => {
            options.signal.aborted = true;
            return {
                ok: false,
                status: 401,
                statusText: 'Unauthorized',
                json: async() => ({})
            };
        }
    });

    module.providerStatus = existingStatus;
    module._startAbortableRequest = () => ({ signal });
    module._clearAbortableRequest = () => {};
    module.requestOptions = () => ({ headers: {} });
    module.handleFetchResponse = () => {
        handled++;
        return false;
    };
    module.isStaleAuthFetchResult = () => false;

    await module.fetchProviderStatus();

    assert.equal(handled, 0);
    assert.strictEqual(module.providerStatus, existingStatus);
});

test('provider status summary scrolls to providers overview section', () => {
    const calls = [];
    const section = {
        scrollIntoView(options) {
            calls.push(['scrollIntoView', options]);
        },
        focus(options) {
            calls.push(['focus', options]);
        }
    };
    const module = createProvidersModule({
        window: {
            document: {
                getElementById(id) {
                    return id === 'provider-status-section' ? section : null;
                }
            }
        }
    });

    module.scrollToProviderStatusSection();

    assert.deepEqual(JSON.parse(JSON.stringify(calls)), [
        ['scrollIntoView', { behavior: 'smooth', block: 'start' }],
        ['focus', { preventScroll: true }]
    ]);
});

test('request health helpers summarize windowed traffic and breaker state', () => {
    const module = createProvidersModule();
    const provider = {
        request_health: {
            circuit_state: 'half-open',
            window_seconds: 600,
            requests: 12,
            errors: 1,
            models: [
                {
                    model: 'qwen3.7-max',
                    requests: 4,
                    errors: 4,
                    flagged: true,
                    last_error: { status_code: 400, message: 'Error from provider' }
                },
                { model: 'gpt-5-nano', requests: 8, errors: 0, flagged: false }
            ]
        }
    };

    assert.equal(module.providerRequestHealth(provider), provider.request_health);
    assert.equal(module.providerBreakerState(provider), 'half-open');
    assert.equal(module.providerBreakerStateLabel(provider), 'Half-open');
    assert.equal(module.providerBreakerStateClass(provider), 'is-degraded');
    assert.equal(module.providerRecentTrafficSummary(provider), '12 requests · 1 error (last 10 min)');
    assert.equal(module.providerHealthModels(provider).length, 2);
    assert.equal(module.providerHealthModelStats(provider.request_health.models[0]), '4/4 failed');
    assert.equal(
        module.providerHealthModelTitle(provider.request_health.models[0]),
        'HTTP 400: Error from provider'
    );
    assert.equal(module.providerHealthModelTitle(provider.request_health.models[1]), '');
});

test('request health helpers tolerate providers without request health', () => {
    const module = createProvidersModule();
    const provider = { name: 'openai' };

    assert.equal(module.providerRequestHealth(provider), null);
    assert.equal(module.providerBreakerState(provider), '');
    assert.equal(module.providerBreakerStateLabel(provider), '');
    assert.equal(module.providerRecentTrafficSummary(provider), '');
    assert.equal(module.providerHealthModels(provider).length, 0);
    assert.equal(module.providerHealthModelStats(null), '');
    assert.equal(module.providerHealthModelTitle(null), '');
});

test('breaker state classes map open and closed states to pill palettes', () => {
    const module = createProvidersModule();
    const withState = (state) => ({ request_health: { circuit_state: state } });

    assert.equal(module.providerBreakerStateClass(withState('open')), 'is-unhealthy');
    assert.equal(module.providerBreakerStateClass(withState('closed')), 'is-healthy');
});

function createStorageStub() {
    return {
        values: new Map(),
        getItem(key) {
            return this.values.has(key) ? this.values.get(key) : null;
        },
        setItem(key, value) {
            this.values.set(key, String(value));
        }
    };
}

test('per-card toggle overrides the section-wide default and persists', () => {
    const storage = createStorageStub();
    const module = createProvidersModule({ window: { localStorage: storage } });
    module.initProviderStatusPreferences();

    const ollama = { name: 'ollama' };
    const openai = { name: 'openai' };

    // Cards follow the collapsed section default until toggled individually.
    assert.equal(module.providerCardExpanded(ollama), false);
    module.toggleProviderCard(ollama);
    assert.equal(module.providerCardExpanded(ollama), true);
    assert.equal(module.providerCardExpanded(openai), false);

    // Overrides survive a reload.
    const reloaded = createProvidersModule({ window: { localStorage: storage } });
    reloaded.initProviderStatusPreferences();
    assert.equal(reloaded.providerCardExpanded(ollama), true);
    assert.equal(reloaded.providerCardExpanded(openai), false);

    // Toggling back persists the collapsed override too.
    module.toggleProviderCard(ollama);
    assert.equal(module.providerCardExpanded(ollama), false);
});

test('section-wide toggle acts as master and clears per-card overrides', () => {
    const storage = createStorageStub();
    const module = createProvidersModule({ window: { localStorage: storage } });
    module.initProviderStatusPreferences();

    const ollama = { name: 'ollama' };
    module.toggleProviderCard(ollama);
    assert.equal(module.providerCardExpanded(ollama), true);

    module.toggleProviderStatusDetails();
    assert.equal(module.providerStatusDetailsExpanded, true);
    // The override was dropped, so the card follows the master state.
    assert.equal(module.providerCardExpanded(ollama), true);

    module.toggleProviderStatusDetails();
    assert.equal(module.providerCardExpanded(ollama), false);
    assert.equal(storage.getItem('gomodel_provider_card_expanded_overrides'), '{}');
});

test('per-card toggle tolerates corrupt storage and nameless providers', () => {
    const storage = createStorageStub();
    storage.setItem('gomodel_provider_card_expanded_overrides', 'not-json');
    const module = createProvidersModule({ window: { localStorage: storage } });
    module.initProviderStatusPreferences();

    assert.equal(module.providerCardExpanded({ name: 'ollama' }), false);
    module.toggleProviderCard(null);
    module.toggleProviderCard({});
    assert.equal(Object.keys(module.providerCardOverrides).length, 0);
});

test('status pill tooltip combines reason and last error', () => {
    const module = createProvidersModule();

    assert.equal(module.providerStatusPillTitle(null), '');
    assert.equal(module.providerStatusPillTitle({}), '');
    assert.equal(
        module.providerStatusPillTitle({ status_reason: 'configured and model discovery succeeded' }),
        'configured and model discovery succeeded'
    );
    assert.equal(
        module.providerStatusPillTitle({
            status_reason: 'recent requests are failing for: gpt-4o',
            last_error: 'gpt-4o: authentication_error'
        }),
        'recent requests are failing for: gpt-4o\n\nLast error: gpt-4o: authentication_error'
    );
});
