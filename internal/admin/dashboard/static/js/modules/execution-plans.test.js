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
        scope_user_path: '/team/alpha',
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
            scope_user_path: '/team/alpha',
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

test('openExecutionPlanCreate hydrates features and guardrails via shared normalizers', () => {
    const module = createExecutionPlansModule();
    module.executionPlanSourceFeatures = () => ({
        cache: false,
        audit: false,
        usage: true,
        guardrails: true
    });
    module.executionPlanSourceGuardrails = () => ([
        { ref: 'policy-system', step: 30 }
    ]);
    module.scrollExecutionPlanFormIntoView = () => {};

    module.openExecutionPlanCreate({
        scope: {
            scope_provider: 'openai',
            scope_model: 'gpt-5',
            scope_user_path: '/team/alpha'
        },
        name: 'Hydrated workflow',
        description: 'Uses helper normalization',
        plan_payload: {
            features: {
                cache: true,
                audit: true,
                usage: false,
                guardrails: false
            },
            guardrails: [
                { ref: 'wrong-source', step: 10 }
            ]
        }
    });

    assert.equal(
        JSON.stringify(module.executionPlanForm.features),
        JSON.stringify({
            cache: false,
            audit: false,
            usage: true,
            guardrails: true
        })
    );
    assert.equal(
        JSON.stringify(module.executionPlanForm.guardrails),
        JSON.stringify([{ ref: 'policy-system', step: 30 }])
    );
    assert.equal(module.executionPlanForm.scope_user_path, '/team/alpha');
});

test('editing a cloned workflow preserves retired provider and model options', () => {
    const module = createExecutionPlansModule();
    module.models = [
        { provider_type: 'openai', model: { id: 'gpt-5' } }
    ];
    module.scrollExecutionPlanFormIntoView = () => {};

    module.openExecutionPlanCreate({
        scope: {
            scope_provider: 'anthropic',
            scope_model: 'claude-retired'
        },
        name: 'Retired workflow',
        description: 'Cloned from an older deployment',
        plan_payload: {
            features: {
                cache: true,
                audit: true,
                usage: true,
                guardrails: false
            },
            guardrails: []
        }
    });

    assert.equal(
        JSON.stringify(module.executionPlanProviderOptions()),
        JSON.stringify(['anthropic', 'openai'])
    );
    assert.equal(
        JSON.stringify(module.executionPlanModelOptions('anthropic')),
        JSON.stringify(['claude-retired'])
    );
    assert.equal(module.validateExecutionPlanRequest(module.buildExecutionPlanRequest()), '');

    const invalidPayload = module.buildExecutionPlanRequest();
    invalidPayload.scope_model = 'different-retired-model';
    assert.equal(
        module.validateExecutionPlanRequest(invalidPayload),
        'Choose a registered model for the selected provider.'
    );
});

test('buildExecutionPlanRequest preserves blank guardrail steps as invalid so validation rejects them', () => {
    const module = createExecutionPlansModule();
    module.models = [
        { provider_type: 'openai', model: { id: 'gpt-5' } }
    ];
    module.executionPlanForm = {
        scope_provider: 'openai',
        scope_model: 'gpt-5',
        name: 'OpenAI GPT-5',
        description: 'Primary translated requests',
        features: {
            cache: true,
            audit: true,
            usage: true,
            guardrails: true
        },
        guardrails: [
            { ref: 'policy-system', step: '   ' }
        ]
    };

    const payload = module.buildExecutionPlanRequest();

    assert.ok(Number.isNaN(payload.plan_payload.guardrails[0].step));
    assert.equal(
        module.validateExecutionPlanRequest(payload),
        'Each guardrail step must use a non-negative integer step number.'
    );
});

test('validateExecutionPlanRequest rejects negative guardrail step numbers', () => {
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
                { ref: 'policy-system', step: -1 }
            ]
        }
    };

    assert.equal(
        module.validateExecutionPlanRequest(payload),
        'Each guardrail step must use a non-negative integer step number.'
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

test('deactivateExecutionPlan ignores duplicate clicks while another deactivation is in flight', async () => {
    let confirmCalled = false;
    let fetchCalled = false;
    const module = createExecutionPlansModule({
        window: {
            confirm() {
                confirmCalled = true;
                return true;
            }
        },
        fetch() {
            fetchCalled = true;
            throw new Error('fetch should not be called while another deactivation is already in flight');
        }
    });
    module.executionPlanDeactivatingID = 'workflow-1';
    module.headers = () => ({});

    await module.deactivateExecutionPlan({
        id: 'workflow-1',
        name: 'Primary workflow',
        scope_type: 'provider'
    });

    assert.equal(confirmCalled, false);
    assert.equal(fetchCalled, false);
    assert.equal(module.executionPlanDeactivatingID, 'workflow-1');
});

test('fetchExecutionPlans aborts hung requests and clears loading state', async () => {
    let timeoutCleared = false;
    class AbortControllerStub {
        constructor() {
            this.signal = { aborted: false };
        }

        abort() {
            this.signal.aborted = true;
        }
    }

    const module = createExecutionPlansModule({
        AbortController: AbortControllerStub,
        setTimeout(fn) {
            fn();
            return 42;
        },
        clearTimeout(id) {
            assert.equal(id, 42);
            timeoutCleared = true;
        },
        fetch(_url, options) {
            assert.equal(options.headers.authorization, 'Bearer token');
            assert.equal(options.signal.aborted, true);
            return Promise.reject(Object.assign(new Error('timed out'), { name: 'AbortError' }));
        }
    });
    module.headers = () => ({ authorization: 'Bearer token' });

    await module.fetchExecutionPlans();

    assert.equal(JSON.stringify(module.executionPlans), JSON.stringify([]));
    assert.equal(module.executionPlanError, 'Loading workflows timed out.');
    assert.equal(module.executionPlansLoading, false);
    assert.equal(timeoutCleared, true);
});

test('submitExecutionPlanForm ignores duplicate submissions while a request is already in flight', async () => {
    let fetchCalled = false;
    const module = createExecutionPlansModule({
        fetch() {
            fetchCalled = true;
            return Promise.resolve({
                ok: true,
                status: 201
            });
        }
    });
    module.models = [
        { provider_type: 'openai', model: { id: 'gpt-5' } }
    ];
    module.executionPlanSubmitting = true;
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
        guardrails: []
    };
    module.headers = () => ({});
    module.closeExecutionPlanForm = () => {};
    module.fetchExecutionPlansPage = async () => {};

    await module.submitExecutionPlanForm();

    assert.equal(fetchCalled, false);
    assert.equal(module.executionPlanSubmitting, true);
});
