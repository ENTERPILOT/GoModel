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
