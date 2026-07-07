const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadAuditStatsModuleFactory(overrides = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'audit-stats.js'), 'utf8');
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
    return context.window.dashboardAuditStatsModule;
}

class FakeChart {
    constructor(canvas, config) {
        this.canvas = canvas;
        this.config = config;
        this.data = config.data;
        this.options = config.options;
        this.destroyCalls = 0;
        FakeChart.instances.push(this);
    }

    destroy() {
        this.destroyCalls++;
    }
}

FakeChart.instances = [];

function createModule(overrides = {}, contextOverrides = {}) {
    const canvas = { offsetWidth: 800 };
    const factory = loadAuditStatsModuleFactory({
        Chart: FakeChart,
        document: {
            getElementById() {
                return canvas;
            }
        },
        ...contextOverrides
    });
    const module = factory();

    module.$nextTick = (callback) => callback();
    module.page = 'overview';
    module.days = '30';
    module.customStartDate = null;
    module.customEndDate = null;
    module.auditStats = module.emptyAuditStats();
    module.auditStatsFetchToken = 0;
    module.auditStatusChart = null;
    module.auditLatencyChart = null;
    module.chartColors = () => ({
        grid: '#111',
        text: '#222',
        tooltipBg: '#333',
        tooltipBorder: '#444',
        tooltipText: '#555'
    });
    module.formatNumber = (n) => String(n);
    module.formatTokensShort = (n) => String(n);
    module.formatTimestamp = (ts) => 'ts:' + ts;
    module._barColors = () => ['#c2845a', '#7a9e7e', '#d4a574'];
    module._resolveLiveTokenColor = (expr) => expr;
    module._chartTickFont = () => ({ size: 11 });
    module._chartTooltip = (colors, callbacks) => ({ callbacks });
    module.headers = () => ({});
    module.handleFetchResponse = (res) => res && res.ok;

    Object.assign(module, overrides);
    return { module, canvas };
}

test('normalizeAuditStats tolerates malformed payloads', () => {
    const { module } = createModule();

    const empty = module.normalizeAuditStats(null);
    assert.equal(empty.interval, 'day');
    assert.equal(empty.buckets.length, 0);
    assert.equal(empty.provider_latency.length, 0);

    const normalized = module.normalizeAuditStats({
        interval: 'hour',
        buckets: [{ start: '2026-01-16T10:00:00Z', requests: 3 }],
        summary: { requests: 3 },
        provider_latency: 'nope'
    });
    assert.equal(normalized.interval, 'hour');
    assert.equal(normalized.buckets.length, 1);
    assert.equal(normalized.provider_latency.length, 0);
});

test('auditStatsHasData follows the summary request count', () => {
    const { module } = createModule();
    assert.equal(module.auditStatsHasData(), false);

    module.auditStats.summary = { requests: 12 };
    assert.equal(module.auditStatsHasData(), true);
});

test('auditStatsSuccessRateText formats the ratio and its absence', () => {
    const { module } = createModule();
    assert.equal(module.auditStatsSuccessRateText(), '—');

    module.auditStats.summary = { requests: 4, success_rate: 0.9944 };
    assert.equal(module.auditStatsSuccessRateText(), '99.4%');
});

test('formatDurationMs scales through ms, s, and min', () => {
    const { module } = createModule();
    assert.equal(module.formatDurationMs(812.4), '812 ms');
    assert.equal(module.formatDurationMs(2350), '2.35 s');
    assert.equal(module.formatDurationMs(90000), '1.5 min');
    assert.equal(module.formatDurationMs('nope'), '-');
});

test('status chart stacks 2xx/4xx/5xx and adds Other only when present', () => {
    const { module } = createModule();
    module.auditStats = module.normalizeAuditStats({
        interval: 'day',
        buckets: [
            { start: '2026-01-16T00:00:00Z', requests: 5, status_2xx: 3, status_4xx: 1, status_5xx: 1, status_other: 0 },
            { start: '2026-01-17T00:00:00Z', requests: 2, status_2xx: 2, status_4xx: 0, status_5xx: 0, status_other: 0 }
        ],
        summary: { requests: 7 }
    });

    const config = module._auditStatusChartConfig(module.chartColors(), module.auditStats.buckets);
    assert.equal(config.type, 'bar');
    assert.equal(config.data.datasets.length, 3);
    assert.deepEqual([...config.data.datasets.map((d) => d.label)], ['2xx', '4xx', '5xx']);
    assert.deepEqual([...config.data.datasets[0].data], [3, 2]);
    assert.equal(config.options.scales.x.stacked, true);
    assert.equal(config.options.scales.y.stacked, true);

    module.auditStats.buckets[0].status_other = 2;
    const withOther = module._auditStatusChartConfig(module.chartColors(), module.auditStats.buckets);
    assert.equal(withOther.data.datasets.length, 4);
    assert.equal(withOther.data.datasets[3].label, 'Other');
});

test('latency chart keeps nil buckets as gaps and colors by provider identity', () => {
    const { module } = createModule();
    module.auditStats = module.normalizeAuditStats({
        interval: 'hour',
        buckets: [
            { start: '2026-01-16T10:00:00Z' },
            { start: '2026-01-16T11:00:00Z' }
        ],
        summary: { requests: 3 },
        provider_latency: [
            { provider: 'openai', requests: [2, 0], avg_duration_ms: [200.5, null] },
            { provider: 'anthropic', requests: [1, 1], avg_duration_ms: [400, 410] }
        ]
    });

    const config = module._auditLatencyChartConfig(module.chartColors(), module.auditStats.buckets, module.auditStats.provider_latency);
    assert.equal(config.type, 'line');
    assert.equal(config.data.datasets.length, 2);
    assert.deepEqual([...config.data.datasets[0].data], [200.5, null]);
    // Distinct palette colors in first-seen order, stable across re-renders.
    assert.equal(config.data.datasets[0].borderColor, '#c2845a');
    assert.equal(config.data.datasets[1].borderColor, '#7a9e7e');
    assert.equal(module.auditProviderColor('openai'), '#c2845a');
});

test('hourly labels mark local midnight with the short date', () => {
    const { module } = createModule();
    module.auditStats.interval = 'hour';

    const midnight = new Date(2026, 0, 16, 0, 0, 0);
    const afternoon = new Date(2026, 0, 16, 14, 0, 0);
    assert.equal(module._auditStatsBucketLabel({ start: midnight.toISOString() }), 'Jan 16');
    assert.equal(module._auditStatsBucketLabel({ start: afternoon.toISOString() }), '14:00');

    module.auditStats.interval = 'day';
    assert.equal(module._auditStatsBucketLabel({ start: afternoon.toISOString() }), 'Jan 16');
});

test('labels follow the dashboard effective timezone, not the browser locale', () => {
    const { module } = createModule();
    module.auditStats.interval = 'hour';

    // Midnight UTC is 09:00 in Tokyo — an hourly bucket must label the hour
    // in the dashboard's timezone, and the day flip must follow it too.
    module.effectiveTimezone = () => 'Asia/Tokyo';
    assert.equal(module._auditStatsBucketLabel({ start: '2026-01-16T00:00:00Z' }), '09:00');
    assert.equal(module._auditStatsBucketLabel({ start: '2026-01-15T15:00:00Z' }), 'Jan 16');

    module.effectiveTimezone = () => 'UTC';
    assert.equal(module._auditStatsBucketLabel({ start: '2026-01-16T00:00:00Z' }), 'Jan 16');

    module.auditStats.interval = 'day';
    assert.equal(module._auditStatsTooltipTitle({ start: '2026-01-16T00:00:00Z' }), 'Jan 16, 2026');
});

test('renderAuditStatsCharts destroys charts when leaving the page', () => {
    FakeChart.instances = [];
    const { module } = createModule();
    module.auditStats = module.normalizeAuditStats({
        interval: 'day',
        buckets: [{ start: '2026-01-16T00:00:00Z', requests: 5, status_2xx: 5 }],
        summary: { requests: 5 },
        provider_latency: [{ provider: 'openai', requests: [5], avg_duration_ms: [100] }]
    });

    module.renderAuditStatsCharts();
    assert.equal(FakeChart.instances.length, 2);
    assert.ok(module.auditStatusChart);
    assert.ok(module.auditLatencyChart);

    module.page = 'audit-logs';
    module.renderAuditStatsCharts();
    assert.equal(module.auditStatusChart, null);
    assert.equal(module.auditLatencyChart, null);
    assert.equal(FakeChart.instances[0].destroyCalls, 1);
    assert.equal(FakeChart.instances[1].destroyCalls, 1);
});

test('fetchAuditStats stores normalized payloads and renders', async () => {
    const payload = {
        interval: 'hour',
        buckets: [{ start: '2026-01-16T10:00:00Z', requests: 2, status_2xx: 2 }],
        summary: { requests: 2, success_rate: 1 },
        provider_latency: []
    };
    let requestedUrl = null;
    const { module } = createModule({}, {
        fetch(url) {
            requestedUrl = url;
            return Promise.resolve({ ok: true, json: async () => payload });
        }
    });
    let rendered = 0;
    module.renderAuditStatsCharts = () => { rendered++; };
    module.requestOptions = () => ({ headers: {} });

    await module.fetchAuditStats();

    assert.ok(String(requestedUrl).includes('/admin/audit/stats?days=30'));
    assert.equal(module.auditStats.interval, 'hour');
    assert.equal(module.auditStats.summary.requests, 2);
    assert.equal(rendered, 1);
});

test('fetchAuditStats resets to empty stats on failure', async () => {
    const loggedErrors = [];
    const { module } = createModule({}, {
        console: {
            error(...args) {
                loggedErrors.push(args);
            }
        },
        fetch() {
            return Promise.reject(new Error('network down'));
        }
    });
    let rendered = 0;
    module.renderAuditStatsCharts = () => { rendered++; };
    module.requestOptions = () => ({ headers: {} });
    module.auditStats = module.normalizeAuditStats({
        interval: 'hour',
        buckets: [{ start: '2026-01-16T10:00:00Z', requests: 2 }],
        summary: { requests: 2 },
        provider_latency: []
    });

    await module.fetchAuditStats();

    assert.equal(module.auditStats.summary.requests, 0);
    assert.equal(module.auditStats.buckets.length, 0);
    assert.equal(rendered, 1);
    assert.equal(loggedErrors.length, 1);
});
