const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadPricingModuleFactory(overrides = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'pricing.js'), 'utf8');
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
    return context.window.dashboardPricingModule;
}

function createPricingModule(overrides) {
    const factory = loadPricingModuleFactory(overrides);
    return factory();
}

test('pricingRecalculatePayload uses preset days and filter fields', () => {
    const module = createPricingModule();
    module.selectedPreset = '14';
    module.pricingRecalculateUserPath = ' /team/alpha ';
    module.pricingRecalculateSelector = ' openai/gpt-4o ';

    assert.equal(JSON.stringify(module.pricingRecalculatePayload('recalculate')), JSON.stringify({
        days: 14,
        user_path: '/team/alpha',
        selector: 'openai/gpt-4o',
        confirmation: 'recalculate'
    }));
});

test('pricingRecalculatePayload uses custom date range', () => {
    const module = createPricingModule();
    module.selectedPreset = null;
    module.customStartDate = new Date(Date.UTC(2026, 3, 1));
    module.customEndDate = new Date(Date.UTC(2026, 3, 2));
    module._formatDate = (date) => date.toISOString().slice(0, 10);

    const payload = module.pricingRecalculatePayload('recalculate');

    assert.equal(payload.start_date, '2026-04-01');
    assert.equal(payload.end_date, '2026-04-02');
});

test('recalculatePricing posts after typed confirmation', async () => {
    const requests = [];
    const module = createPricingModule({
        fetch(url, request) {
            requests.push({ url, request });
            return Promise.resolve({
                ok: true,
                status: 200,
                json: async () => ({
                    matched: 3,
                    recalculated: 3,
                    with_pricing: 2,
                    without_pricing: 1
                })
            });
        }
    });

    module.selectedPreset = '30';
    module.typedConfirmationDialog = { open: true, value: 'recalculate' };
    module.workflowRuntimeBooleanFlag = () => true;
    module.requestOptions = (options) => options || {};
    module.handleFetchResponse = () => true;
    module.closePricingRecalculateDialog = () => {
        module.pricingRecalculateDialogOpen = false;
    };
    module.refreshAfterPricingRecalculate = async () => {};

    await module.recalculatePricing();

    assert.equal(requests.length, 1);
    assert.equal(requests[0].url, '/admin/usage/recalculate-pricing');
    assert.equal(requests[0].request.method, 'POST');
    assert.equal(requests[0].request.body, JSON.stringify({
        days: 30,
        user_path: '',
        selector: '',
        confirmation: 'recalculate'
    }));
    assert.match(module.pricingRecalculateNotice, /Pricing recalculated for 3 of 3 usage records\./);
    assert.match(module.pricingRecalculateNotice, /1 usage record still lacks pricing metadata\./);
});

test('recalculatePricing does not post when feature flag is disabled', async () => {
    let calls = 0;
    const module = createPricingModule({
        fetch() {
            calls++;
            return Promise.resolve({ ok: true });
        }
    });

    module.workflowRuntimeBooleanFlag = () => false;
    module.typedConfirmationDialog = { open: true, value: 'recalculate' };

    await module.recalculatePricing();

    assert.equal(calls, 0);
    assert.equal(module.pricingRecalculateError, 'Usage pricing recalculation is unavailable.');
});
