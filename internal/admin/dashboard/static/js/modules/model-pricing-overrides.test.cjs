const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadModelPricingOverridesModuleFactory(overrides = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'model-pricing-overrides.js'), 'utf8');
    const window = {
        ...(overrides.window || {})
    };
    const context = {
        console,
        ...overrides.context,
        window
    };
    vm.createContext(context);
    vm.runInContext(source, context);
    return context.window.dashboardModelPricingOverridesModule;
}

function createModule(overrides) {
    const factory = loadModelPricingOverridesModuleFactory(overrides);
    return factory();
}

test('modelRowPricing applies exact override over inherited/base pricing', () => {
    const module = createModule();
    const row = {
        provider_name: 'openai-main',
        model: {
            id: 'gpt-4o',
            metadata: {
                pricing: {
                    input_per_mtok: 1,
                    output_per_mtok: 2
                }
            }
        }
    };
    module.modelPricingOverrideViews = [
        { selector: '/', pricing: { input_per_mtok: 10 } },
        { selector: 'openai-main/', pricing: { input_per_mtok: 20 } },
        { selector: 'gpt-4o', pricing: { input_per_mtok: 30 } },
        { selector: 'openai-main/gpt-4o', pricing: { input_per_mtok: 40 } }
    ];

    const pricing = module.modelRowPricing(row);

    assert.equal(pricing.input_per_mtok, 40);
    assert.equal(pricing.output_per_mtok, 2);
});

test('effective preview reports per-field pricing sources', () => {
    const module = createModule();
    const row = {
        provider_name: 'openai-main',
        model: {
            id: 'gpt-4o',
            metadata: {
                pricing: {
                    input_per_mtok: 1,
                    output_per_mtok: 2,
                    cached_input_per_mtok: 0.5
                },
                pricing_sources: {
                    input_per_mtok: 'config_yaml',
                    output_per_mtok: 'model_registry'
                }
            }
        }
    };
    module.modelPricingOverrideViews = [
        { selector: 'openai-main/', pricing: { output_per_mtok: 20 } }
    ];

    const state = module.modelRowPricingState(row, 'openai-main/gpt-4o');
    assert.equal(state.pricing.output_per_mtok, 20);
    assert.equal(state.sources.input_per_mtok, 'config.yaml');
    assert.equal(state.sources.output_per_mtok, 'Dashboard/API override (openai-main/)');
    assert.equal(state.sources.cached_input_per_mtok, 'Model registry');

    module.modelPricingOverrideFormBasePricing = state.pricing;
    module.modelPricingOverrideFormBasePricingSources = state.sources;
    module.modelPricingOverrideRows = [
        { id: 'draft', field: 'input_per_mtok', value: '10' }
    ];

    const rows = module.modelPricingEffectivePreviewRows();
    assert.equal(rows.find((item) => item.field === 'input_per_mtok').source, 'Form/API value');
    assert.equal(rows.find((item) => item.field === 'output_per_mtok').source, 'Dashboard/API override (openai-main/)');
    assert.equal(rows.find((item) => item.field === 'cached_input_per_mtok').source, 'Model registry');
});

test('payload validates duplicate and negative pricing rows', () => {
    const module = createModule();
    module.modelPricingOverrideRows = [
        { id: '1', field: 'input_per_mtok', value: '1' },
        { id: '2', field: 'input_per_mtok', value: '2' }
    ];

    assert.match(module.modelPricingOverridePayload().error, /only be used once/);

    module.modelPricingOverrideRows = [
        { id: '1', field: 'input_per_mtok', value: '-1' }
    ];
    assert.match(module.modelPricingOverridePayload().error, /greater than or equal to 0/);
});

test('payload preserves tiered pricing when scalar rows are absent', () => {
    const module = createModule();
    module.modelPricingOverrideRows = [];
    module.modelPricingOverrideFormPreservedTiers = [
        { up_to_mtok: 1, input_per_mtok: 0.5 }
    ];

    assert.equal(JSON.stringify(module.modelPricingOverridePayload()), JSON.stringify({
        pricing: {
            tiers: [
                { up_to_mtok: 1, input_per_mtok: 0.5 }
            ]
        }
    }));
});

test('model pricing override mutations send selector in JSON body', async() => {
    const requests = [];
    const module = createModule({
        context: {
            fetch: async(url, request) => {
                requests.push({ url, request });
                return {
                    ok: true,
                    status: 200,
                    json: async() => ({})
                };
            }
        },
        window: {
            confirm: () => true
        }
    });

    Object.assign(module, {
        modelPricingOverrideForm: {
            selector: 'openrouter/meta-llama/llama-3.1-8b-instruct'
        },
        modelPricingOverrideFormHasExistingOverride: true,
        modelPricingOverrideRows: [
            { id: '1', field: 'input_per_mtok', value: '1.25' }
        ],
        adminRequestOptions(options) {
            return {
                ...(options || {}),
                headers: {}
            };
        },
        handleFetchResponse() {
            return true;
        },
        fetchModelPricingOverrides: async() => {},
        syncDisplayModels() {}
    });

    await module.submitModelPricingOverrideForm();
    module.modelPricingOverrideForm = {
        selector: 'openrouter/meta-llama/llama-3.1-8b-instruct'
    };
    module.modelPricingOverrideFormHasExistingOverride = true;
    await module.deleteModelPricingOverride();

    assert.equal(requests.length, 2);
    assert.equal(requests[0].url, '/admin/model-pricing-overrides');
    assert.equal(requests[1].url, '/admin/model-pricing-overrides');
    assert.deepEqual(JSON.parse(requests[0].request.body), {
        selector: 'openrouter/meta-llama/llama-3.1-8b-instruct',
        pricing: {
            input_per_mtok: 1.25
        }
    });
    assert.deepEqual(JSON.parse(requests[1].request.body), {
        selector: 'openrouter/meta-llama/llama-3.1-8b-instruct'
    });
});
