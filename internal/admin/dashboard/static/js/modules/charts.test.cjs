const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadChartsModuleFactory(overrides = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'charts.js'), 'utf8');
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
    return context.window.dashboardChartsModule;
}

function createChartsModule(overrides) {
    const factory = loadChartsModuleFactory(overrides);
    return factory();
}

class FakeChart {
    constructor(canvas, config) {
        this.canvas = canvas;
        this.data = config.data;
        this.options = config.options;
        this.destroyCalls = 0;
        this.updateCalls = [];
        FakeChart.instances.push(this);
    }

    destroy() {
        this.destroyCalls++;
    }

    update(mode) {
        this.updateCalls.push(mode);
    }
}

FakeChart.instances = [];

function createChartsContext() {
    const canvas = { offsetWidth: 800 };
    const module = createChartsModule({
        Chart: FakeChart,
        document: {
            getElementById(id) {
                // The prompt-cache gauge is a separate widget refreshed alongside
                // the overview chart; the chart tests don't exercise it, so give
                // it no canvas and it no-ops.
                return id === 'promptCacheGauge' ? null : canvas;
            }
        }
    });

    module.$nextTick = (callback) => callback();
    module.chartColors = () => ({
        grid: '#111',
        text: '#222',
        tooltipBg: '#333',
        tooltipBorder: '#444',
        tooltipText: '#555'
    });
    module.interval = 'weekly';
    module.page = 'overview';
    module.formatTokensShort = (value) => String(value);
    module.qualifiedModelDisplay = (value) => value && value.model ? value.model : '-';

    return { module, canvas };
}

test('renderChart recreates the overview chart instance on refresh', () => {
    FakeChart.instances = [];
    const { module } = createChartsContext();
    module.daily = [
        { date: '2026-03-28', input_tokens: 1, output_tokens: 2 },
        { date: '2026-03-29', input_tokens: 3, output_tokens: 4 }
    ];

    module.renderChart();

    assert.equal(FakeChart.instances.length, 1);
    const firstChart = module.chart;

    module.daily = [
        { date: '2026-03-29', input_tokens: 8, output_tokens: 13 }
    ];
    module.renderChart();

    assert.notStrictEqual(module.chart, firstChart);
    assert.equal(FakeChart.instances.length, 2);
    assert.equal(firstChart.destroyCalls, 1);
    assert.equal(JSON.stringify(module.chart.data.labels), JSON.stringify(['2026-03-29']));
    assert.equal(JSON.stringify(module.chart.data.datasets[0].data), JSON.stringify([8]));
    assert.equal(JSON.stringify(module.chart.data.datasets[1].data), JSON.stringify([13]));
    assert.equal(JSON.stringify(firstChart.updateCalls), JSON.stringify([]));
});

test('renderBarChart recreates the usage bar chart instance on refresh', () => {
    FakeChart.instances = [];
    const { module } = createChartsContext();
    module.page = 'usage';
    module.usageMode = 'tokens';
    module.modelUsage = [
        { model: 'gpt-4o', input_tokens: 5, output_tokens: 7, total_cost: 0.01 },
        { model: 'gpt-5', input_tokens: 11, output_tokens: 13, total_cost: 0.02 }
    ];

    module.renderBarChart();

    assert.equal(FakeChart.instances.length, 1);
    const firstChart = module.usageBarChart;
    assert.equal(JSON.stringify(firstChart.data.labels), JSON.stringify(['gpt-5', 'gpt-4o']));
    assert.equal(JSON.stringify(firstChart.data.datasets[0].data), JSON.stringify([-11, -5]));
    assert.equal(JSON.stringify(firstChart.data.datasets[1].data), JSON.stringify([13, 7]));

    module.modelUsage = [
        { model: 'gpt-5', input_tokens: 21, output_tokens: 34, total_cost: 0.03 }
    ];
    module.renderBarChart();

    assert.notStrictEqual(module.usageBarChart, firstChart);
    assert.equal(FakeChart.instances.length, 2);
    assert.equal(firstChart.destroyCalls, 1);
    assert.equal(JSON.stringify(module.usageBarChart.data.labels), JSON.stringify(['gpt-5']));
    assert.equal(JSON.stringify(module.usageBarChart.data.datasets[0].data), JSON.stringify([-21]));
    assert.equal(JSON.stringify(module.usageBarChart.data.datasets[1].data), JSON.stringify([34]));
    assert.equal(module.usageBarChart.options.indexAxis, 'y');
    assert.equal(JSON.stringify(firstChart.updateCalls), JSON.stringify([]));
});

test('renderBarChart splits input and output costs in costs mode', () => {
    FakeChart.instances = [];
    const { module } = createChartsContext();
    module.page = 'usage';
    module.usageMode = 'costs';
    module.modelUsage = [
        { model: 'gpt-4o', input_tokens: 5, output_tokens: 7, input_cost: 0.01, output_cost: 0.04, total_cost: 0.05 },
        { model: 'gpt-5', input_tokens: 11, output_tokens: 13, input_cost: 0.02, output_cost: 0.06, total_cost: 0.08 }
    ];

    module.renderBarChart();

    const chart = module.usageBarChart;
    assert.equal(JSON.stringify(chart.data.labels), JSON.stringify(['gpt-5', 'gpt-4o']));
    assert.equal(JSON.stringify(chart.data.datasets[0].data), JSON.stringify([-0.02, -0.01]));
    assert.equal(JSON.stringify(chart.data.datasets[1].data), JSON.stringify([0.06, 0.04]));
    assert.equal(chart.data.datasets[0].label, 'Input Cost');
    assert.equal(chart.data.datasets[1].label, 'Output Cost');
});

test('renderBarChart stacked view piles input and output rightward as positive values', () => {
    FakeChart.instances = [];
    const { module } = createChartsContext();
    module.page = 'usage';
    module.usageMode = 'tokens';
    module.modelUsageView = 'stacked';
    module.modelUsage = [
        { model: 'gpt-4o', input_tokens: 5, output_tokens: 7, total_cost: 0.01 },
        { model: 'gpt-5', input_tokens: 11, output_tokens: 13, total_cost: 0.02 }
    ];

    module.renderBarChart();

    const chart = module.usageBarChart;
    assert.notEqual(chart, null);
    assert.equal(JSON.stringify(chart.data.labels), JSON.stringify(['gpt-5', 'gpt-4o']));
    assert.equal(JSON.stringify(chart.data.datasets[0].data), JSON.stringify([11, 5]));
    assert.equal(JSON.stringify(chart.data.datasets[1].data), JSON.stringify([13, 7]));
    assert.equal(chart.options.indexAxis, 'y');
    assert.equal(chart.options.scales.x.stacked, true);
});

test('toggleUsageChartView switches between diverging and stacked chart views', () => {
    FakeChart.instances = [];
    const { module } = createChartsContext();
    module.page = 'usage';
    module.usageMode = 'tokens';
    module.modelUsageView = 'chart';
    module.modelUsage = [
        { model: 'gpt-5', input_tokens: 10, output_tokens: 20, total_cost: 0.01 }
    ];

    module.renderBarChart();
    const divergingChart = module.usageBarChart;
    assert.equal(JSON.stringify(divergingChart.data.datasets[0].data), JSON.stringify([-10]));

    module.toggleUsageChartView('model', 'stacked');

    assert.equal(module.modelUsageView, 'stacked');
    assert.equal(divergingChart.destroyCalls, 1);
    assert.equal(JSON.stringify(module.usageBarChart.data.datasets[0].data), JSON.stringify([10]));
});

test('renderBarChart adds cache series and splits paid input when cache data is present', () => {
    FakeChart.instances = [];
    const { module } = createChartsContext();
    module.page = 'usage';
    module.usageMode = 'tokens';
    module.modelUsage = [
        {
            model: 'gpt-5', input_tokens: 100, output_tokens: 20,
            uncached_input_tokens: 30, cached_input_tokens: 60, cache_write_input_tokens: 10,
            local_cached_input_tokens: 100, local_cached_output_tokens: 20, total_cost: 0.02
        }
    ];

    module.renderBarChart();

    const chart = module.usageBarChart;
    assert.equal(chart.data.datasets.length, 5);
    // Paid input = uncached + cache writes, on the left.
    assert.equal(chart.data.datasets[0].label, 'Input Tokens');
    assert.equal(JSON.stringify(chart.data.datasets[0].data), JSON.stringify([-40]));
    assert.equal(chart.data.datasets[1].label, 'Output Tokens');
    assert.equal(JSON.stringify(chart.data.datasets[1].data), JSON.stringify([20]));
    // Prompt-cache reads join the input side; local cache splits per side.
    assert.equal(chart.data.datasets[2].label, 'Prompt Cached');
    assert.equal(JSON.stringify(chart.data.datasets[2].data), JSON.stringify([-60]));
    assert.equal(chart.data.datasets[3].label, 'Locally Cached (Input)');
    assert.equal(JSON.stringify(chart.data.datasets[3].data), JSON.stringify([-100]));
    assert.equal(chart.data.datasets[4].label, 'Locally Cached (Output)');
    assert.equal(JSON.stringify(chart.data.datasets[4].data), JSON.stringify([20]));
});

test('renderBarChart keeps two series when rows carry no cache data', () => {
    FakeChart.instances = [];
    const { module } = createChartsContext();
    module.page = 'usage';
    module.usageMode = 'tokens';
    module.modelUsage = [
        { model: 'gpt-5', input_tokens: 100, output_tokens: 20, total_cost: 0.02 }
    ];

    module.renderBarChart();

    assert.equal(module.usageBarChart.data.datasets.length, 2);
    assert.equal(JSON.stringify(module.usageBarChart.data.datasets[0].data), JSON.stringify([-100]));
});

test('renderBarChart costs mode splits estimated cached cost out of input cost', () => {
    FakeChart.instances = [];
    const { module } = createChartsContext();
    module.page = 'usage';
    module.usageMode = 'costs';
    module.modelUsage = [
        {
            model: 'gpt-5', input_tokens: 100, output_tokens: 20,
            cached_input_tokens: 60, local_cached_input_tokens: 100, local_cached_output_tokens: 20,
            input_cost: 0.5, output_cost: 0.3, total_cost: 0.8, cached_input_cost: 0.125
        }
    ];

    module.renderBarChart();

    const chart = module.usageBarChart;
    assert.equal(chart.data.datasets.length, 3);
    assert.equal(chart.data.datasets[0].label, 'Input Cost');
    assert.equal(JSON.stringify(chart.data.datasets[0].data), JSON.stringify([-0.375]));
    assert.equal(chart.data.datasets[2].label, 'Prompt Cached Cost');
    assert.equal(JSON.stringify(chart.data.datasets[2].data), JSON.stringify([-0.125]));
    // Local cache hits cost nothing: no cost-mode series for them.
    assert.ok(!chart.data.datasets.some((d) => d.label.startsWith('Locally Cached')));
});

test('renderBarChart folds models past the top 10 into a combined Other row', () => {
    FakeChart.instances = [];
    const { module } = createChartsContext();
    module.page = 'usage';
    module.usageMode = 'tokens';
    module.modelUsage = Array.from({ length: 12 }, (_, i) => ({
        model: 'model-' + i,
        input_tokens: 100 - i,
        output_tokens: 200 - i,
        total_cost: 0.01
    }));

    module.renderBarChart();

    const chart = module.usageBarChart;
    assert.equal(chart.data.labels.length, 11);
    assert.equal(chart.data.labels[10], 'Other');
    // model-10 and model-11 fold together: inputs 90+89, outputs 190+189.
    assert.equal(chart.data.datasets[0].data[10], -179);
    assert.equal(chart.data.datasets[1].data[10], 379);
});

test('renderBarChart prefers provider_name in model labels when available', () => {
    FakeChart.instances = [];
    const { module } = createChartsContext();
    module.page = 'usage';
    module.usageMode = 'tokens';
    module.qualifiedModelDisplay = (value) => value.provider_name
        ? value.provider_name + '/' + value.model
        : value.model;
    module.modelUsage = [
        { model: 'gpt-4o', provider_name: 'primary-openai', input_tokens: 5, output_tokens: 7, total_cost: 0.01 }
    ];

    module.renderBarChart();

    assert.equal(JSON.stringify(module.usageBarChart.data.labels), JSON.stringify(['primary-openai/gpt-4o']));
});

test('renderUserPathChart recreates the user path chart and uses usage mode values', () => {
    FakeChart.instances = [];
    const { module } = createChartsContext();
    module.page = 'usage';
    module.usageMode = 'tokens';
    module.userPathUsage = [
        { user_path: '/team/alpha', input_tokens: 5, output_tokens: 7, total_tokens: 12, total_cost: 0.01 },
        { user_path: '/team/beta', input_tokens: 11, output_tokens: 13, total_tokens: 24, total_cost: 0.02 }
    ];

    module.renderUserPathChart();

    assert.equal(FakeChart.instances.length, 1);
    const firstChart = module.usageUserPathChart;
    assert.equal(JSON.stringify(firstChart.data.labels), JSON.stringify(['/team/beta', '/team/alpha']));
    assert.equal(JSON.stringify(firstChart.data.datasets[0].data), JSON.stringify([-11, -5]));
    assert.equal(JSON.stringify(firstChart.data.datasets[1].data), JSON.stringify([13, 7]));

    module.usageMode = 'costs';
    module.userPathUsage = [
        { user_path: '/team/alpha', input_tokens: 21, output_tokens: 34, total_tokens: 55, input_cost: 0.01, output_cost: 0.02, total_cost: 0.03 }
    ];
    module.renderUserPathChart();

    assert.notStrictEqual(module.usageUserPathChart, firstChart);
    assert.equal(FakeChart.instances.length, 2);
    assert.equal(firstChart.destroyCalls, 1);
    assert.equal(JSON.stringify(module.usageUserPathChart.data.labels), JSON.stringify(['/team/alpha']));
    assert.equal(JSON.stringify(module.usageUserPathChart.data.datasets[0].data), JSON.stringify([-0.01]));
    assert.equal(JSON.stringify(module.usageUserPathChart.data.datasets[1].data), JSON.stringify([0.02]));
});

test('user path usage chart hides when the only path is root', () => {
    FakeChart.instances = [];
    const { module } = createChartsContext();
    module.page = 'usage';
    module.usageUserPathChart = null;
    module.userPathUsage = [
        { user_path: '/', input_tokens: 21, output_tokens: 34, total_tokens: 55, total_cost: 0.03 }
    ];

    assert.equal(module.userPathUsageChartVisible(), false);
    module.renderUserPathChart();

    assert.equal(FakeChart.instances.length, 0);
    assert.equal(module.usageUserPathChart, null);

    module.userPathUsage = [
        { user_path: '/team/alpha', input_tokens: 21, output_tokens: 34, total_tokens: 55, total_cost: 0.03 }
    ];

    assert.equal(module.userPathUsageChartVisible(), true);
    module.renderUserPathChart();

    assert.equal(FakeChart.instances.length, 1);
    assert.notEqual(module.usageUserPathChart, null);
});

test('usage chart table rows use the same selected metric ordering as charts', () => {
    const { module } = createChartsContext();
    module.usageMode = 'tokens';
    module.modelUsage = [
        { model: 'small', input_tokens: 1, output_tokens: 2, total_cost: 9 },
        { model: 'large', input_tokens: 10, output_tokens: 20, total_cost: 1 }
    ];

    assert.equal(JSON.stringify(module.modelUsageTableRows().map((row) => row.model)), JSON.stringify(['large', 'small']));

    module.usageMode = 'costs';

    assert.equal(JSON.stringify(module.modelUsageTableRows().map((row) => row.model)), JSON.stringify(['small', 'large']));
});

test('toggleUsageChartView switches table and chart modes and rerenders chart views', () => {
    FakeChart.instances = [];
    const { module } = createChartsContext();
    module.page = 'usage';
    module.usageMode = 'tokens';
    module.modelUsageView = 'chart';
    module.userPathUsageView = 'chart';
    module.modelUsage = [
        { model: 'gpt-5', input_tokens: 10, output_tokens: 20, total_cost: 0.01 }
    ];
    module.userPathUsage = [
        { user_path: '/team/alpha', input_tokens: 10, output_tokens: 20, total_tokens: 30, total_cost: 0.01 }
    ];

    module.renderBarChart();
    module.renderUserPathChart();
    const modelChart = module.usageBarChart;
    const userPathChart = module.usageUserPathChart;
    assert.notEqual(modelChart, null);
    assert.notEqual(userPathChart, null);

    module.toggleUsageChartView('model', 'table');

    assert.equal(module.modelUsageView, 'table');
    assert.equal(modelChart.destroyCalls, 1);
    assert.equal(module.usageBarChart, null);

    module.toggleUsageChartView('model', 'chart');

    assert.equal(module.modelUsageView, 'chart');
    assert.notEqual(module.usageBarChart, null);
    assert.notStrictEqual(module.usageBarChart, modelChart);

    module.toggleUsageChartView('userPath', 'table');

    assert.equal(module.userPathUsageView, 'table');
    assert.equal(userPathChart.destroyCalls, 1);
    assert.equal(module.usageUserPathChart, null);

    module.toggleUsageChartView('userPath', 'chart');

    assert.equal(module.userPathUsageView, 'chart');
    assert.notEqual(module.usageUserPathChart, null);
    assert.notStrictEqual(module.usageUserPathChart, userPathChart);
});

test('labelColor is deterministic and drawn from the shared palette', () => {
    const { module } = createChartsContext();

    assert.equal(module.labelColor('team-alpha'), module.labelColor('team-alpha'));
    assert.ok(module._barColors().includes(module.labelColor('team-alpha')));
    assert.equal(module.labelChipStyle('x')['--label-color'], module.labelColor('x'));
});

test('renderLabelChart orders rows by selected metric and splits input and output', () => {
    FakeChart.instances = [];
    const { module } = createChartsContext();
    module.page = 'usage';
    module.usageMode = 'tokens';
    module.labelUsageView = 'chart';
    module.labelUsage = [
        { label: 'alpha', input_tokens: 5, output_tokens: 7, total_tokens: 12, total_cost: 0.01 },
        { label: 'prod', input_tokens: 11, output_tokens: 13, total_tokens: 24, total_cost: 0.02 }
    ];

    module.renderLabelChart();

    assert.equal(FakeChart.instances.length, 1);
    const chart = module.usageLabelChart;
    assert.equal(JSON.stringify(chart.data.labels), JSON.stringify(['prod', 'alpha']));
    assert.equal(JSON.stringify(chart.data.datasets[0].data), JSON.stringify([-11, -5]));
    assert.equal(JSON.stringify(chart.data.datasets[1].data), JSON.stringify([13, 7]));
    assert.equal(chart.options.indexAxis, 'y');

    module.labelUsage = [];
    module.renderLabelChart();

    assert.equal(chart.destroyCalls, 1);
    assert.equal(module.usageLabelChart, null);
});

test('toggleUsageChartView label target switches views and rerenders', () => {
    FakeChart.instances = [];
    const { module } = createChartsContext();
    module.page = 'usage';
    module.usageMode = 'tokens';
    module.labelUsageView = 'chart';
    module.labelUsage = [
        { label: 'alpha', input_tokens: 10, output_tokens: 20, total_tokens: 30, total_cost: 0.01 }
    ];

    module.renderLabelChart();
    const labelChart = module.usageLabelChart;
    assert.notEqual(labelChart, null);

    module.toggleUsageChartView('label', 'table');

    assert.equal(module.labelUsageView, 'table');
    assert.equal(labelChart.destroyCalls, 1);
    assert.equal(module.usageLabelChart, null);

    module.toggleUsageChartView('label', 'chart');

    assert.equal(module.labelUsageView, 'chart');
    assert.notEqual(module.usageLabelChart, null);
    assert.notStrictEqual(module.usageLabelChart, labelChart);
});
