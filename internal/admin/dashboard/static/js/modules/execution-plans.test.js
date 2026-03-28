const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadExecutionPlansModuleFactory(overrides = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'execution-plans.js'), 'utf8');
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
    return context.window.dashboardExecutionPlansModule;
}

function createExecutionPlansModule(overrides) {
    const factory = loadExecutionPlansModuleFactory(overrides);
    return factory();
}

test('executionPlanProviderOptions returns unique sorted provider types', () => {
    const module = createExecutionPlansModule();
    module.models = [
        { provider_type: 'anthropic', model: { id: 'claude-3-7' } },
        { provider_type: 'openai', model: { id: 'gpt-5' } },
        { provider_type: 'openai', model: { id: 'gpt-4o-mini' } }
    ];

    assert.equal(
        JSON.stringify(module.executionPlanProviderOptions()),
        JSON.stringify(['anthropic', 'openai'])
    );
});

test('buildExecutionPlanRequest emits provider-model payload and strips guardrails when disabled', () => {
    const module = createExecutionPlansModule();
    module.executionPlanForm = {
        scope_provider: 'openai',
        scope_model: 'gpt-5',
        name: 'OpenAI GPT-5',
        description: 'Primary translated requests',
        features: {
            cache: true,
            audit: true,
            usage: true,
            guardrails: false
        },
        guardrails: [
            { ref: 'policy-system', step: 10 }
        ]
    };

    assert.equal(
        JSON.stringify(module.buildExecutionPlanRequest()),
        JSON.stringify({
            scope_provider: 'openai',
            scope_model: 'gpt-5',
            name: 'OpenAI GPT-5',
            description: 'Primary translated requests',
            plan_payload: {
                schema_version: 1,
                features: {
                    cache: true,
                    audit: true,
                    usage: true,
                    guardrails: false
                },
                guardrails: []
            }
        })
    );
});

test('validateExecutionPlanRequest rejects duplicate guardrail refs', () => {
    const module = createExecutionPlansModule();
    const payload = {
        scope_provider: '',
        scope_model: '',
        name: 'Global',
        plan_payload: {
            schema_version: 1,
            features: {
                cache: true,
                audit: true,
                usage: true,
                guardrails: true
            },
            guardrails: [
                { ref: 'policy-system', step: 10 },
                { ref: 'policy-system', step: 20 }
            ]
        }
    };

    assert.equal(
        module.validateExecutionPlanRequest(payload),
        'Each guardrail ref may appear only once in a plan.'
    );
});

test('setExecutionPlanProvider clears model when provider changes', () => {
    const module = createExecutionPlansModule();
    module.models = [
        { provider_type: 'openai', model: { id: 'gpt-5' } },
        { provider_type: 'anthropic', model: { id: 'claude-3-7' } }
    ];
    module.executionPlanForm = module.defaultExecutionPlanForm();
    module.executionPlanForm.scope_provider = 'openai';
    module.executionPlanForm.scope_model = 'gpt-5';

    module.setExecutionPlanProvider('anthropic');

    assert.equal(module.executionPlanForm.scope_provider, 'anthropic');
    assert.equal(module.executionPlanForm.scope_model, '');
});

test('validateExecutionPlanRequest rejects unregistered provider-model selections', () => {
    const module = createExecutionPlansModule();
    module.models = [
        { provider_type: 'openai', model: { id: 'gpt-5' } }
    ];

    assert.equal(
        module.validateExecutionPlanRequest({
            scope_provider: 'anthropic',
            scope_model: '',
            plan_payload: {
                schema_version: 1,
                features: { cache: true, audit: true, usage: true, guardrails: false },
                guardrails: []
            }
        }),
        'Choose a registered provider.'
    );

    assert.equal(
        module.validateExecutionPlanRequest({
            scope_provider: 'openai',
            scope_model: 'gpt-4o-mini',
            plan_payload: {
                schema_version: 1,
                features: { cache: true, audit: true, usage: true, guardrails: false },
                guardrails: []
            }
        }),
        'Choose a registered model for the selected provider.'
    );
});

test('workflowDisplayName falls back to scope label or All models', () => {
    const module = createExecutionPlansModule();

    assert.equal(
        module.workflowDisplayName({ name: '', scope_display: 'global' }),
        'All models'
    );
    assert.equal(
        module.workflowDisplayName({ name: '', scope_display: 'openai/gpt-5' }),
        'openai/gpt-5'
    );
    assert.equal(
        module.workflowDisplayName({ name: 'Primary workflow', scope_display: 'openai/gpt-5' }),
        'Primary workflow'
    );
});

test('epGuardrailLabel only shows a sublabel when guardrail steps exist', () => {
    const module = createExecutionPlansModule();

    assert.equal(
        module.epGuardrailLabel({
            plan_payload: {
                guardrails: []
            }
        }),
        ''
    );

    assert.equal(
        module.epGuardrailLabel({
            plan_payload: {
                guardrails: [{ ref: 'policy-system', step: 10 }]
            }
        }),
        '1 step'
    );

    assert.equal(
        module.epGuardrailLabel({
            plan_payload: {
                guardrails: [
                    { ref: 'policy-system', step: 10 },
                    { ref: 'pii', step: 20 }
                ]
            }
        }),
        '2 steps'
    );
});

test('deactivateExecutionPlan requires confirmation before posting', async () => {
    let fetchCalled = false;
    const module = createExecutionPlansModule({
        window: {
            confirm(message) {
                assert.match(message, /Deactivate workflow "Primary workflow"\?/);
                return false;
            }
        },
        fetch() {
            fetchCalled = true;
            throw new Error('fetch should not be called when deactivation is cancelled');
        }
    });
    module.headers = () => ({});

    await module.deactivateExecutionPlan({
        id: 'workflow-1',
        name: 'Primary workflow',
        scope_type: 'provider'
    });

    assert.equal(fetchCalled, false);
    assert.equal(module.executionPlanDeactivatingID, '');
});
