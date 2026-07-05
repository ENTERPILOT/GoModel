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
        usageFilterModel: '',
        usageFilterProvider: '',
        usageFilterLabel: '',
        usageFilterUserPath: '',
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

test('fetchUsage clears stale cache overview when the summary refresh fails', async () => {
    const factory = loadUsageModuleFactory({
        fetch: async () => ({ async json() { return {}; } })
    });
    const app = Object.assign(
        {
            days: '30',
            interval: 'daily',
            page: 'overview',
            customStartDate: null,
            customEndDate: null,
            requestOptions() { return { headers: {} }; },
            handleFetchResponse() { return false; }, // simulate a failed refresh
            renderChart() {}
        },
        factory()
    );
    // Data left over from a previous, successful period.
    app.summary = { total_requests: 999, total_input_tokens: 5000 };
    app.cacheOverview = { summary: { total_hits: 42, total_input_tokens: 1000 }, daily: [{}] };

    await app.fetchUsage();

    assert.equal(app.summary.total_requests, 0);
    assert.equal(app.cacheOverview.summary.total_hits, 0);
    assert.equal(app.cacheOverview.summary.total_input_tokens, 0);
    assert.equal(app.cacheOverview.daily.length, 0);
});

test('fetchUsage clears stale data when the request throws (network error)', async () => {
    const factory = loadUsageModuleFactory({
        fetch: async () => { throw new Error('network down'); }
    });
    const app = Object.assign(
        {
            days: '30',
            interval: 'daily',
            page: 'overview',
            customStartDate: null,
            customEndDate: null,
            requestOptions() { return { headers: {} }; },
            handleFetchResponse() { return true; },
            renderChart() {}
        },
        factory()
    );
    app.summary = { total_requests: 999, total_input_tokens: 5000 };
    app.cacheOverview = { summary: { total_hits: 42, total_input_tokens: 1000 }, daily: [{}] };

    await app.fetchUsage();

    assert.equal(app.summary.total_requests, 0);
    assert.equal(app.cacheOverview.summary.total_hits, 0);
    assert.equal(app.cacheOverview.daily.length, 0);
});

test('fetchUsage clears the previous cache overview before reloading on a range switch', async () => {
    let cacheFetches = 0;
    const factory = loadUsageModuleFactory({
        fetch: async () => ({ async json() { return {}; } })
    });
    const app = Object.assign(
        {
            days: '7',
            interval: 'daily',
            page: 'overview',
            customStartDate: null,
            customEndDate: null,
            requestOptions() { return { headers: {} }; },
            handleFetchResponse() { return true; }, // success
            renderChart() {}
        },
        factory()
    );
    app.cacheAnalyticsEnabled = () => true;
    app.fetchCacheOverview = () => { cacheFetches++; }; // stub: does not repopulate
    // Previous period's cache overview still in place.
    app.cacheOverview = { summary: { total_hits: 42, total_input_tokens: 1000 }, daily: [{}] };

    await app.fetchUsage();

    // Cleared synchronously before the async reload, so the meter can't show the old period.
    assert.equal(app.cacheOverview.summary.total_hits, 0);
    assert.equal(cacheFetches, 1);
});

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

test('fetchUsageLog includes the label filter when set', async () => {
    const { app, fetchCalls } = createUsageLogApp({ usageFilterLabel: 'team alpha' });

    await app.fetchUsageLog(true);

    assert.equal(fetchCalls.length, 1);
    assert.match(fetchCalls[0].url, /label=team%20alpha/);
});

test('fetchCacheOverview follows page filters on the usage page but not on overview', async () => {
    const make = (page) => createUsageLogApp({
        page,
        usageFilterLabel: 'env:prod',
        usageFilterProvider: 'openai',
        workflowRuntimeBooleanFlag: () => true
    });

    const usage = make('usage');
    await usage.app.fetchCacheOverview();
    assert.equal(usage.fetchCalls.length, 1);
    assert.match(usage.fetchCalls[0].url, /label=env%3Aprod/);
    assert.match(usage.fetchCalls[0].url, /provider=openai/);

    const overview = make('overview');
    overview.app.renderChart = () => {};
    await overview.app.fetchCacheOverview();
    assert.equal(overview.fetchCalls.length, 1);
    assert.doesNotMatch(overview.fetchCalls[0].url, /label=/);
    assert.doesNotMatch(overview.fetchCalls[0].url, /provider=/);
});

test('page filters flow into every usage-page fetch', async () => {
    const { app, fetchCalls } = createUsageLogApp({
        usageFilterModel: 'gpt-5',
        usageFilterProvider: 'openai',
        usageFilterLabel: 'env:prod',
        usageFilterUserPath: '/team'
    });

    await app.fetchUsagePageSummary();
    await app.fetchModelUsage();
    await app.fetchLabelUsage();
    await app.fetchUsageLog(true);

    // The summary fetch issues one request per cache mode.
    assert.equal(fetchCalls.length, 5);
    for (const call of fetchCalls) {
        assert.match(call.url, /model=gpt-5/);
        assert.match(call.url, /provider=openai/);
        assert.match(call.url, /label=env%3Aprod/);
        assert.match(call.url, /user_path=%2Fteam/);
    }
    assert.match(fetchCalls[0].url, /\/admin\/usage\/summary\?/);
});

test('usage page stat cards follow the log cache scope and derive hits from the two summaries', () => {
    const module = createUsageModule();
    module.formatNumber = (n) => String(n);
    module.formatCost = (v) => (v === null || v === undefined ? '---' : '$' + Number(v).toFixed(2));
    module.usageSummary = { total_requests: 90, total_cost: 1.5, total_input_cost: 1.0, total_output_cost: 0.5 };
    module.usageSummaryAll = { total_requests: 100 };
    module.usageLogHideCached = false;

    // Default log view shows cached rows, so the card counts all rows —
    // independent of the cache-analytics flag.
    assert.equal(module.usagePageTotalRequests(), 100);
    assert.equal(module.usagePageRequestsTitle(), '90 to providers + 10 from cache');
    assert.equal(module.usagePageCostTitle(), '$1.00 input + $0.50 output');

    // Hiding cached rows narrows the card to provider requests, like the log.
    module.usageLogHideCached = true;
    assert.equal(module.usagePageTotalRequests(), 90);
    assert.equal(module.usagePageRequestsTitle(), '10 cached requests hidden');

    // No cached traffic: plain count, no tooltip.
    module.usageLogHideCached = false;
    module.usageSummaryAll = { total_requests: 90 };
    assert.equal(module.usagePageTotalRequests(), 90);
    assert.equal(module.usagePageRequestsTitle(), '');

    module.usageSummary = {};
    assert.equal(module.usagePageCostTitle(), '');
});

test('usage page rewrite savings cards show only when rewriters saved tokens', () => {
    const module = createUsageModule();
    module.formatNumber = (n) => String(n);

    // No savings reported: cards hidden, zero-safe values.
    module.usageSummary = { total_requests: 10 };
    assert.equal(module.usagePageRewriteSavingsVisible(), false);
    assert.equal(module.usagePageRewriteTokensSaved(), 0);
    assert.equal(module.usagePageRewriteCostSaved(), null);
    assert.equal(module.usagePageRewriteSavedTitle(), '');

    // Savings with pricing: both cards visible with tokens and cost.
    module.usageSummary = { rewrite_tokens_saved: 4200, rewrite_cost_saved: 0.0125 };
    assert.equal(module.usagePageRewriteSavingsVisible(), true);
    assert.equal(module.usagePageRewriteTokensSaved(), 4200);
    assert.equal(module.usagePageRewriteCostSaved(), 0.0125);
    assert.equal(
        module.usagePageRewriteSavedTitle(),
        '4200 prompt tokens removed by request rewriters before reaching providers'
    );

    // Savings without pricing: tokens card carries the value, cost stays null.
    module.usageSummary = { rewrite_tokens_saved: 100, rewrite_cost_saved: null };
    assert.equal(module.usagePageRewriteSavingsVisible(), true);
    assert.equal(module.usagePageRewriteCostSaved(), null);

    // Defensive: negative or garbage totals never surface.
    module.usageSummary = { rewrite_tokens_saved: -5 };
    assert.equal(module.usagePageRewriteSavingsVisible(), false);
    module.usageSummary = { rewrite_tokens_saved: 'NaN?' };
    assert.equal(module.usagePageRewriteSavingsVisible(), false);
});

test('fetchUsagePageSummary loads uncached and all cache modes with the page filters', async () => {
    const { app, fetchCalls } = createUsageLogApp({ usageFilterLabel: 'env:prod' });

    await app.fetchUsagePageSummary();

    assert.equal(fetchCalls.length, 2);
    const urls = fetchCalls.map((c) => c.url);
    assert.ok(urls.every((u) => u.includes('/admin/usage/summary?') && u.includes('label=env%3Aprod')), urls.join(' '));
    assert.ok(urls.some((u) => u.includes('cache_mode=uncached')), urls.join(' '));
    assert.ok(urls.some((u) => u.includes('cache_mode=all')), urls.join(' '));
});

test('facet option getters sort choices and keep a stale selection listed', () => {
    const module = createUsageModule();
    module.usageFacetOptions = { models: ['gpt-5'], providers: ['openai'], labels: ['prod', 'alpha'] };
    module.usageFilterModel = '';
    module.usageFilterProvider = '';
    module.usageFilterLabel = '';

    assert.equal(JSON.stringify(module.usageFilterLabelOptions()), JSON.stringify(['alpha', 'prod']));
    assert.equal(JSON.stringify(module.usageFilterModelOptions()), JSON.stringify(['gpt-5']));

    module.usageFilterLabel = 'removed';
    assert.equal(JSON.stringify(module.usageFilterLabelOptions()), JSON.stringify(['alpha', 'prod', 'removed']));
});

test('facet options honor every filter except their own', async () => {
    const { app, fetchCalls } = createUsageLogApp({
        usageFilterModel: 'gpt-5',
        usageFilterProvider: 'openai',
        usageFilterLabel: 'env:prod',
        usageFilterUserPath: '/team'
    });

    await app.fetchUsageFacetOptions();

    assert.equal(fetchCalls.length, 3);
    const byEndpoint = (endpoint, without, withs) => {
        const matches = fetchCalls.filter((c) => c.url.includes(endpoint) && !c.url.includes(without + '='));
        assert.equal(matches.length, 1, `expected one ${endpoint} call without ${without}: ${fetchCalls.map((c) => c.url).join(' ')}`);
        for (const param of withs) {
            assert.ok(matches[0].url.includes(param), `${matches[0].url} should include ${param}`);
        }
    };
    byEndpoint('/admin/usage/models', 'model', ['provider=openai', 'label=env%3Aprod', 'user_path=%2Fteam']);
    byEndpoint('/admin/usage/models', 'provider', ['model=gpt-5', 'label=env%3Aprod', 'user_path=%2Fteam']);
    byEndpoint('/admin/usage/labels', 'label', ['model=gpt-5', 'provider=openai', 'user_path=%2Fteam']);
});

test('facet options share one by-model query when neither model nor provider filters', async () => {
    const { app, fetchCalls } = createUsageLogApp({ usageFilterLabel: 'env:prod' });

    await app.fetchUsageFacetOptions();

    assert.equal(fetchCalls.length, 2);
    assert.equal(fetchCalls.filter((c) => c.url.includes('/admin/usage/models')).length, 1);
    assert.equal(fetchCalls.filter((c) => c.url.includes('/admin/usage/labels')).length, 1);
});

test('toggleUsageLabelFilter sets and clears the page label filter, refetching the page', () => {
    const module = createUsageModule();
    let pageFetches = 0;
    module.fetchUsagePage = () => { pageFetches++; };
    module.usageFilterLabel = '';

    module.toggleUsageLabelFilter('prod');
    assert.equal(module.usageFilterLabel, 'prod');

    module.toggleUsageLabelFilter('prod');
    assert.equal(module.usageFilterLabel, '');
    assert.equal(pageFetches, 2);
});

test('usageLogHasLabels reflects aggregates, active filter, or labelled entries', () => {
    const module = createUsageModule();
    module.labelUsage = [];
    module.usageFilterLabel = '';
    module.usageLog = { entries: [{ labels: null }, {}] };
    assert.equal(module.usageLogHasLabels(), false);

    module.usageLog = { entries: [{ labels: ['alpha'] }] };
    assert.equal(module.usageLogHasLabels(), true);

    module.usageLog = { entries: [] };
    module.usageFilterLabel = 'alpha';
    assert.equal(module.usageLogHasLabels(), true);

    module.usageFilterLabel = '';
    module.labelUsage = [{ label: 'alpha' }];
    assert.equal(module.usageLogHasLabels(), true);
});

function createCacheMeterModule({ summary = {}, cacheOverview = null, cacheEnabled = false } = {}) {
    const module = createUsageModule();
    module.formatNumber = (n) => String(n);
    module.workflowRuntimeBooleanFlag = (flag, def) => (flag === 'CACHE_ENABLED' ? cacheEnabled : def);
    module.summary = { ...module.emptyUsageSummary(), ...summary };
    module.cacheOverview = cacheOverview || module.emptyCacheOverview();
    return module;
}

test('cacheMeterSegments splits input tokens token-weighted and percents sum to 100', () => {
    const module = createCacheMeterModule({
        summary: { uncached_input_tokens: 600, cached_input_tokens: 300, cache_write_input_tokens: 0 },
        cacheOverview: { summary: { total_input_tokens: 100 }, daily: [] },
        cacheEnabled: true
    });

    const segments = module.cacheMeterSegments();
    const byKey = Object.fromEntries(segments.map((s) => [s.key, s]));

    assert.equal(module.cacheMeterTotal(), 1000);
    assert.equal(module.cacheMeterVisible(), true);
    assert.equal(byKey.uncached.pct, 60);
    assert.equal(byKey.local.pct, 10);
    assert.equal(byKey.prompt.pct, 30);
    assert.equal(byKey.uncached.tokens, 600);
    assert.equal(byKey.local.tokens, 100);
    assert.equal(byKey.prompt.tokens, 300);
    assert.equal(segments.reduce((sum, s) => sum + s.pct, 0), 100);
});

test('cacheMeterVisible is false and segments empty when there is no usage', () => {
    const module = createCacheMeterModule();

    assert.equal(module.cacheMeterTotal(), 0);
    assert.equal(module.cacheMeterVisible(), false);
    assert.equal(module.cacheMeterSegments().length, 0);
});

test('cacheMeterSegments folds cache-write tokens into the not-cached slice', () => {
    const module = createCacheMeterModule({
        summary: { uncached_input_tokens: 50, cached_input_tokens: 0, cache_write_input_tokens: 50 }
    });

    const segments = module.cacheMeterSegments();

    assert.equal(segments.length, 1);
    assert.equal(segments[0].key, 'uncached');
    assert.equal(segments[0].tokens, 100);
    assert.equal(segments[0].pct, 100);
    assert.match(module.cacheMeterSegmentTitle(segments[0]), /cache-write/);
});

test('cacheMeterSegments omits the local slice when cache analytics are disabled', () => {
    const module = createCacheMeterModule({
        summary: { uncached_input_tokens: 70, cached_input_tokens: 30 },
        cacheOverview: { summary: { total_input_tokens: 999 }, daily: [] },
        cacheEnabled: false
    });

    const keys = module.cacheMeterSegments().map((s) => s.key);

    assert.equal(keys.join(','), 'uncached,prompt');
    assert.equal(module.cacheMeterTotal(), 100);
});

test('cacheMeterSegments uses largest-remainder rounding so thirds still total 100', () => {
    const module = createCacheMeterModule({
        summary: { uncached_input_tokens: 1, cached_input_tokens: 1 },
        cacheOverview: { summary: { total_input_tokens: 1 }, daily: [] },
        cacheEnabled: true
    });

    const segments = module.cacheMeterSegments();

    assert.equal(segments.length, 3);
    assert.equal(segments.reduce((sum, s) => sum + s.pct, 0), 100);
});

test('cacheMeterCategories always returns all three categories at 0% when empty', () => {
    const module = createCacheMeterModule();

    const categories = module.cacheMeterCategories();

    assert.equal(categories.length, 3);
    assert.equal(categories.map((c) => c.key).join(','), 'uncached,prompt,local');
    assert.equal(categories.every((c) => c.pct === 0), true);
    // The bar stays empty while the legend key still renders all three.
    assert.equal(module.cacheMeterSegments().length, 0);
    assert.equal(module.cacheMeterVisible(), false);
});

test('summaryTotalRequests adds local cache hits when analytics enabled', () => {
    const module = createCacheMeterModule({
        summary: { total_requests: 40 },
        cacheOverview: { summary: { total_hits: 10, total_input_tokens: 0 }, daily: [] },
        cacheEnabled: true
    });

    assert.equal(module.summaryTotalRequests(), 50);
    assert.equal(module.summaryCacheHits(), 10);
    assert.match(module.summaryTotalRequestsTitle(), /40 to providers \+ 10 from cache/);
});

test('summaryTotalRequests excludes cache hits when analytics disabled', () => {
    const module = createCacheMeterModule({
        summary: { total_requests: 40 },
        cacheOverview: { summary: { total_hits: 10 }, daily: [] },
        cacheEnabled: false
    });

    assert.equal(module.summaryTotalRequests(), 40);
    assert.equal(module.summaryCacheHits(), 0);
    assert.equal(module.summaryTotalRequestsTitle(), '');
});

test('cacheMeterCategories keeps zero categories alongside non-zero ones', () => {
    const module = createCacheMeterModule({
        summary: { uncached_input_tokens: 70, cached_input_tokens: 30 },
        cacheEnabled: true
    });

    const byKey = Object.fromEntries(module.cacheMeterCategories().map((c) => [c.key, c]));

    assert.equal(byKey.uncached.pct, 70);
    assert.equal(byKey.prompt.pct, 30);
    assert.equal(byKey.local.pct, 0);
    assert.equal(module.cacheMeterCategories().reduce((sum, c) => sum + c.pct, 0), 100);
    // The zero local slice is in the legend but not in the rendered bar.
    assert.equal(module.cacheMeterSegments().length, 2);
});
