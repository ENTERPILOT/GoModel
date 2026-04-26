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
            getElementById() {
                return canvas;
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

    module.modelUsage = [
        { model: 'gpt-5', input_tokens: 21, output_tokens: 34, total_cost: 0.03 }
    ];
    module.renderBarChart();

    assert.notStrictEqual(module.usageBarChart, firstChart);
    assert.equal(FakeChart.instances.length, 2);
    assert.equal(firstChart.destroyCalls, 1);
    assert.equal(JSON.stringify(module.usageBarChart.data.labels), JSON.stringify(['gpt-5']));
    assert.equal(JSON.stringify(module.usageBarChart.data.datasets[0].data), JSON.stringify([55]));
    assert.equal(JSON.stringify(firstChart.updateCalls), JSON.stringify([]));
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
    assert.equal(JSON.stringify(firstChart.data.datasets[0].data), JSON.stringify([24, 12]));

    module.usageMode = 'costs';
    module.userPathUsage = [
        { user_path: '/team/alpha', input_tokens: 21, output_tokens: 34, total_tokens: 55, total_cost: 0.03 }
    ];
    module.renderUserPathChart();

    assert.notStrictEqual(module.usageUserPathChart, firstChart);
    assert.equal(FakeChart.instances.length, 2);
    assert.equal(firstChart.destroyCalls, 1);
    assert.equal(JSON.stringify(module.usageUserPathChart.data.labels), JSON.stringify(['/team/alpha']));
    assert.equal(JSON.stringify(module.usageUserPathChart.data.datasets[0].data), JSON.stringify([0.03]));
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
