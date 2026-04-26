const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadBudgetsModuleFactory(overrides = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'budgets.js'), 'utf8');
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
    return context.window.dashboardBudgetsModule;
}

function createBudgetsModule(overrides) {
    const factory = loadBudgetsModuleFactory(overrides);
    return factory();
}

test('budgetManagementEnabled defaults on and respects the runtime flag', () => {
    const module = createBudgetsModule();
    assert.equal(module.budgetManagementEnabled(), true);

    module.workflowRuntimeBooleanFlag = (key, fallback) => {
        assert.equal(key, 'BUDGETS_ENABLED');
        assert.equal(fallback, true);
        return false;
    };
    assert.equal(module.budgetManagementEnabled(), false);
});

test('budgetSettingsPayload normalizes numeric values before saving', () => {
    const module = createBudgetsModule();
    module.budgetSettings = {
        daily_reset_hour: '7',
        daily_reset_minute: '15',
        weekly_reset_weekday: '3',
        weekly_reset_hour: '8',
        weekly_reset_minute: '45',
        monthly_reset_day: '31',
        monthly_reset_hour: '9',
        monthly_reset_minute: '30'
    };

    assert.equal(JSON.stringify(module.budgetSettingsPayload()), JSON.stringify({
        daily_reset_hour: 7,
        daily_reset_minute: 15,
        weekly_reset_weekday: 3,
        weekly_reset_hour: 8,
        weekly_reset_minute: 45,
        monthly_reset_day: 31,
        monthly_reset_hour: 9,
        monthly_reset_minute: 30
    }));
});

test('budgetFormPayload normalizes user path and standard periods', () => {
    const module = createBudgetsModule();
    module.budgetForm = {
        user_path: 'team/alpha',
        period: 'weekly',
        period_seconds: 0,
        amount: '12.3456',
        source: ''
    };

    assert.equal(JSON.stringify(module.budgetFormPayload()), JSON.stringify({
        user_path: '/team/alpha',
        period_seconds: 604800,
        amount: 12.3456,
        source: 'manual'
    }));
});

test('fetchBudgets reads budget rows from the list envelope', async () => {
    let renderIconsCalls = 0;
    const module = createBudgetsModule({
        fetch(url) {
            assert.equal(url, '/admin/api/v1/budgets');
            return Promise.resolve({
                status: 200,
                ok: true,
                json: () => Promise.resolve({
                    budgets: [
                        { user_path: '/team', period_seconds: 86400, amount: 10 }
                    ]
                })
            });
        }
    });
    module.requestOptions = () => ({});
    module.handleFetchResponse = () => true;
    module.renderIconsAfterUpdate = () => {
        renderIconsCalls++;
    };

    await module.fetchBudgets();

    assert.equal(module.budgetsAvailable, true);
    assert.equal(module.budgets.length, 1);
    assert.equal(module.budgets[0].user_path, '/team');
    assert.equal(renderIconsCalls, 1);
});

test('filteredBudgets filters only by user path', () => {
    const module = createBudgetsModule();
    module.budgets = [
        { user_path: '/team/alpha', period_label: 'daily' },
        { user_path: '/team/beta', period_label: 'weekly' },
        { user_path: '/platform', period_label: 'team' }
    ];

    module.budgetFilter = 'TEAM/A';
    assert.equal(JSON.stringify(module.filteredBudgets()), JSON.stringify([
        { user_path: '/team/alpha', period_label: 'daily' }
    ]));

    module.budgetFilter = 'weekly';
    assert.equal(JSON.stringify(module.filteredBudgets()), JSON.stringify([]));

    module.budgetFilter = '';
    assert.equal(module.filteredBudgets(), module.budgets);
});

test('budgetSourceTitle explains manual and config sources', () => {
    const module = createBudgetsModule();

    assert.equal(module.budgetSourceTitle({ source: 'manual' }), 'Created from the dashboard.');
    assert.equal(module.budgetSourceTitle({ source: 'config' }), 'Loaded from configuration.');
    assert.equal(module.budgetSourceTitle({ source: 'import' }), 'Budget source: import');
});

test('budgetPeriodLabel and class distinguish standard and custom periods', () => {
    const module = createBudgetsModule();

    assert.equal(module.budgetPeriodLabel({ period_seconds: 3600 }), 'Hourly');
    assert.equal(module.budgetPeriodClass({ period_seconds: 3600 }), 'budget-period-label-hourly');
    assert.equal(module.budgetPeriodLabel({ period_seconds: 86400 }), 'Daily');
    assert.equal(module.budgetPeriodClass({ period_seconds: 86400 }), 'budget-period-label-daily');
    assert.equal(module.budgetPeriodLabel({ period_seconds: 604800 }), 'Weekly');
    assert.equal(module.budgetPeriodClass({ period_seconds: 604800 }), 'budget-period-label-weekly');
    assert.equal(module.budgetPeriodLabel({ period_seconds: 2592000 }), 'Monthly');
    assert.equal(module.budgetPeriodClass({ period_seconds: 2592000 }), 'budget-period-label-monthly');
    assert.equal(module.budgetPeriodLabel({ period_seconds: 7200, period_label: '7200s' }), 'Custom 7200s');
    assert.equal(module.budgetPeriodClass({ period_seconds: 7200 }), 'budget-period-label-custom');
});

test('deleteBudget posts the selected budget key and refreshes from the response envelope', async () => {
    const requests = [];
    const module = createBudgetsModule({
        confirm(message) {
            assert.match(message, /Delete budget "\/team Daily"\? This cannot be undone\./);
            return true;
        },
        fetch(url, request) {
            requests.push({ url, request });
            return Promise.resolve({
                status: 200,
                ok: true,
                json: () => Promise.resolve({
                    budgets: [
                        { user_path: '/other', period_seconds: 86400, amount: 5 }
                    ]
                })
            });
        }
    });
    module.requestOptions = (options) => options || {};
    module.handleFetchResponse = () => true;

    await module.deleteBudget({ user_path: '/team', period_seconds: 86400, period_label: 'daily' });

    assert.equal(requests.length, 1);
    assert.equal(requests[0].url, '/admin/api/v1/budgets');
    assert.equal(requests[0].request.method, 'DELETE');
    assert.equal(requests[0].request.body, JSON.stringify({
        user_path: '/team',
        period_seconds: 86400
    }));
    assert.equal(module.budgetDeletingKey, '');
    assert.equal(module.budgetNotice, 'Budget deleted.');
    assert.equal(JSON.stringify(module.budgets), JSON.stringify([
        { user_path: '/other', period_seconds: 86400, amount: 5 }
    ]));
});

test('resetBudgets requires the typed reset confirmation before posting', async () => {
    const module = createBudgetsModule({
        fetch() {
            throw new Error('fetch should not be called');
        }
    });
    module.requestOptions = (options) => options || {};
    module.handleFetchResponse = () => true;
    module.budgetResetConfirmation = 'nope';

    await module.resetBudgets();

    assert.equal(module.budgetSettingsError, 'Type reset to confirm.');
    assert.equal(module.budgetResetLoading, false);
});
