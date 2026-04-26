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
    assert.equal(module.providerTypeLabel({ name: 'openai-primary', type: 'openai' }), 'openai');
    assert.equal(module.providerTypeLabel({ name: 'openai', type: 'openai' }), '');
    assert.equal(module.providerTypeLabel({ name: 'azure-east', config: { type: 'azure' } }), 'azure');
});

test('provider detail toggle persists in browser storage and last check formatting uses time-only text', () => {
    const storage = {
        values: new Map([['gomodel_provider_status_details_expanded', 'true']]),
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
    assert.equal(module.providerStatusDetailsExpanded, true);
    assert.equal(module.providerStatusDetailsToggleLabel(), 'Hide Details');

    module.toggleProviderStatusDetails();
    assert.equal(module.providerStatusDetailsExpanded, false);
    assert.equal(storage.getItem('gomodel_provider_status_details_expanded'), 'false');

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
