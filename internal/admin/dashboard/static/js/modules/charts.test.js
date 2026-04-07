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
