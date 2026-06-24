const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadUsageModuleFactory(overrides = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'usage.js'), 'utf8');
    const window = {
        ...(overrides.window || {})
    };
    const context = {
        console,
        ...overrides,
        window
    };
    vm.createContext(context);
    vm.runInContext(source, context);
    return context.window.dashboardUsageModule;
}

function createUsageModule(overrides) {
    const factory = loadUsageModuleFactory(overrides);
    return factory();
}

test('usesOpenRouterCreditPricing detects OpenRouter credit cost source', () => {
    const module = createUsageModule();

    assert.equal(module.usesOpenRouterCreditPricing({ cost_source: 'openrouter_credits' }), true);
    assert.equal(module.usesOpenRouterCreditPricing({ cost_source: 'xai_cost_in_usd_ticks' }), false);
    assert.equal(module.usesOpenRouterCreditPricing({ cost_source: 'model_pricing' }), false);
    assert.equal(module.usesOpenRouterCreditPricing({}), false);
});

test('usesResponseCostPricing detects provider-reported costs', () => {
    const module = createUsageModule();

    assert.equal(module.usesResponseCostPricing({ cost_source: 'openrouter_credits' }), true);
    assert.equal(module.usesResponseCostPricing({ cost_source: 'xai_cost_in_usd_ticks' }), true);
    assert.equal(module.usesResponseCostPricing({ cost_source: 'model_pricing' }), false);
    assert.equal(module.usesResponseCostPricing({}), false);
});

test('usageEntryCached detects exact and semantic cache types and ignores others', () => {
    const module = createUsageModule();

    assert.equal(module.usageEntryCached({ cache_type: 'exact' }), true);
    assert.equal(module.usageEntryCached({ cache_type: ' Semantic ' }), true);
    assert.equal(module.usageEntryCached({ cache_type: '' }), false);
    assert.equal(module.usageEntryCached({}), false);
    assert.equal(module.usageEntryCached({ cache_type: 'other' }), false);
});

test('usageEntryCacheLabel returns capitalized cache type or dash', () => {
    const module = createUsageModule();

    assert.equal(module.usageEntryCacheLabel({ cache_type: 'exact' }), 'Exact');
    assert.equal(module.usageEntryCacheLabel({ cache_type: 'SEMANTIC' }), 'Semantic');
    assert.equal(module.usageEntryCacheLabel({}), '-');
    assert.equal(module.usageEntryCacheLabel({ cache_type: 'other' }), '-');
});

test('summaryTotalTokens uses total tokens and falls back to input plus output', () => {
    const module = createUsageModule();

    module.summary = {
        total_input_tokens: 120,
        total_output_tokens: 30,
        total_tokens: 155
    };
    assert.equal(module.summaryTotalTokens(), 155);

    module.summary = {
        total_input_tokens: '120',
        total_output_tokens: 30,
        total_tokens: null
    };
    assert.equal(module.summaryTotalTokens(), 150);

    module.summary = null;
    assert.equal(module.summaryTotalTokens(), 0);
});

test('cacheOverviewTotalTokens sums local cache input and output tokens', () => {
    const module = createUsageModule();

    module.cacheOverview = {
        summary: {
            total_input_tokens: '120',
            total_output_tokens: 30
        }
    };

    assert.equal(module.cacheOverviewTotalTokens(), 150);

    module.cacheOverview = { summary: { total_input_tokens: null, total_output_tokens: 'bad' } };
    assert.equal(module.cacheOverviewTotalTokens(), 0);

    module.cacheOverview = null;
    assert.equal(module.cacheOverviewTotalTokens(), 0);
});

test('hasProviderCache detects positive cached_input_tokens', () => {
    const module = createUsageModule();

    assert.equal(module.hasProviderCache({ cached_input_tokens: 100 }), true);
    assert.equal(module.hasProviderCache({ cached_input_tokens: 0 }), false);
    assert.equal(module.hasProviderCache({}), false);
    assert.equal(module.hasProviderCache(null), false);
});

test('providerCacheLabel renders percentage with one decimal', () => {
    const module = createUsageModule();

    assert.equal(module.providerCacheLabel({ cached_input_tokens: 50, cached_input_ratio: 0.25 }), '25.0%');
    assert.equal(module.providerCacheLabel({ cached_input_tokens: 1, cached_input_ratio: 0.1234 }), '12.3%');
    assert.equal(module.providerCacheLabel({}), '');
});

test('providerCacheTitle reports cached and total input tokens, with cache write when present', () => {
    const module = Object.assign({ formatNumber: (n) => String(n) }, createUsageModule());

    assert.equal(
        module.providerCacheTitle({ cached_input_tokens: 90, uncached_input_tokens: 50, cache_write_input_tokens: 0 }),
        '90 cached / 140 input tokens'
    );
    assert.equal(
        module.providerCacheTitle({ cached_input_tokens: 90, uncached_input_tokens: 50, cache_write_input_tokens: 30 }),
        '90 cached / 170 input tokens\n30 cache write'
    );
    assert.equal(module.providerCacheTitle({}), '');
});

test('cachedCostTitle prepends savings note for cached entries and passes through otherwise', () => {
    const module = createUsageModule();

    assert.equal(
        module.cachedCostTitle({ cache_type: 'exact' }, '12 tokens'),
        'Saved by cache — not charged\n12 tokens'
    );
    assert.equal(
        module.cachedCostTitle({ cache_type: 'semantic' }, ''),
        'Saved by cache — not charged'
    );
    assert.equal(module.cachedCostTitle({}, '12 tokens'), '12 tokens');
    assert.equal(module.cachedCostTitle({}, ''), '');
});

test('costSourceTooltip explains provider-reported costs', () => {
    const module = createUsageModule();

    assert.equal(
        module.costSourceTooltip({ cost_source: 'openrouter_credits' }),
        'Costs from OpenRouter USD-based credits.'
    );
    assert.equal(
        module.costSourceTooltip({ cost_source: 'xai_cost_in_usd_ticks' }),
        'Costs from xAI usage.cost_in_usd_ticks.'
    );
    assert.equal(module.costSourceTooltip({ cost_source: 'model_pricing' }), '');
});

function createUsageLogApp(overrides = {}) {
    const fetchCalls = [];
    const fetch = async (url, options) => {
        fetchCalls.push({ url, options });
        return {
            async json() {
                return { entries: [], total: 0, limit: 50, offset: 0 };
            }
        };
    };
    const factory = loadUsageModuleFactory({ fetch });
    const app = {
        days: '30',
        interval: 'daily',
        customStartDate: null,
        customEndDate: null,
        usageLog: { entries: [], total: 0, limit: 50, offset: 0 },
        usageLogSearch: '',
        usageLogModel: '',
        usageLogProvider: '',
        usageLogUserPath: '',
        usageLogHideCached: false,
        _formatDate(date) {
            return date.toISOString().slice(0, 10);
        },
        requestOptions() {
            return { headers: {} };
        },
        handleFetchResponse() {
            return true;
        },
        ...overrides,
        ...factory()
    };
    return { app, fetchCalls };
}

test('fetchUsageLog includes cache_mode=all by default so cached records are returned', async () => {
    const { app, fetchCalls } = createUsageLogApp();

    await app.fetchUsageLog(true);

    assert.equal(fetchCalls.length, 1);
    assert.match(fetchCalls[0].url, /cache_mode=all/);
    assert.doesNotMatch(fetchCalls[0].url, /cache_mode=uncached/);
});

test('fetchUsageLog switches to cache_mode=uncached when hide-cached toggle is on', async () => {
    const { app, fetchCalls } = createUsageLogApp({ usageLogHideCached: true });

    await app.fetchUsageLog(true);

    assert.equal(fetchCalls.length, 1);
    assert.match(fetchCalls[0].url, /cache_mode=uncached/);
});
