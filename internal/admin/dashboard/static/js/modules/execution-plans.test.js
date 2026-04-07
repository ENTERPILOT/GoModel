const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadExecutionPlansModuleFactory(overrides = {}) {
    const clipboardSource = fs.readFileSync(path.join(__dirname, 'clipboard.js'), 'utf8');
    const source = fs.readFileSync(path.join(__dirname, 'execution-plans.js'), 'utf8');
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
    vm.runInContext(clipboardSource, context);
    vm.runInContext(source, context);
    return context.window.dashboardExecutionPlansModule;
}

function createExecutionPlansModule(overrides) {
    const factory = loadExecutionPlansModuleFactory(overrides);
    return factory();
}

function createTimerHarness() {
    let nextID = 1;
    const timers = new Map();
    return {
        setTimeout(callback, _delay) {
            const id = nextID++;
            timers.set(id, callback);
            return id;
        },
        clearTimeout(id) {
            timers.delete(id);
        },
        runAll() {
            const callbacks = Array.from(timers.values());
            timers.clear();
            callbacks.forEach((callback) => callback());
        }
    };
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

test('defaultExecutionPlanForm starts fallback enabled for new workflows', () => {
    const module = createExecutionPlansModule();

    assert.equal(module.executionPlanForm.features.fallback, true);
    assert.equal(module.defaultExecutionPlanForm().features.fallback, true);
});

test('executionPlanPreview mirrors the draft workflow card state from the editor form', () => {
    const module = createExecutionPlansModule();
    module.executionPlanForm = {
        scope_provider: 'openai',
        scope_model: 'gpt-5',
        name: 'Draft workflow',
        description: 'Live preview of the edited workflow',
        features: {
            cache: true,
            audit: false,
            usage: true,
            guardrails: true,
            fallback: false
        },
        guardrails: [
            { ref: 'policy-system', step: 10 }
        ]
    };

    assert.equal(
        JSON.stringify(module.executionPlanPreview()),
        JSON.stringify({
            id: 'draft-workflow-preview',
            scope_type: 'provider_model',
            scope_display: 'openai/gpt-5',
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            },
            name: 'Draft workflow',
            description: 'Live preview of the edited workflow',
            plan_payload: {
                schema_version: 1,
                features: {
                    cache: true,
                    audit: false,
                    usage: true,
                    guardrails: true,
                    fallback: false
                },
                guardrails: [
                    { ref: 'policy-system', step: 10 }
                ]
            }
        })
    );
});

test('executionPlanPreview renders path-scoped draft labels using canonical scope display', () => {
    const module = createExecutionPlansModule();
    module.executionPlanForm = {
        scope_provider: 'openai',
        scope_model: 'gpt-5',
        scope_user_path: ' team//alpha/ ',
        name: 'Path workflow',
        description: 'Preview should include the canonical path scope',
        features: {
            cache: true,
            audit: true,
            usage: true,
            guardrails: false,
            fallback: false
        },
        guardrails: []
    };

    assert.equal(
        JSON.stringify(module.executionPlanPreview()),
        JSON.stringify({
            id: 'draft-workflow-preview',
            scope_type: 'provider_model_path',
            scope_display: 'openai/gpt-5 @ /team/alpha',
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5',
                scope_user_path: '/team/alpha'
            },
            name: 'Path workflow',
            description: 'Preview should include the canonical path scope',
            plan_payload: {
                schema_version: 1,
                features: {
                    cache: true,
                    audit: true,
                    usage: true,
                    guardrails: false,
                    fallback: false
                },
                guardrails: []
            }
        })
    );
});

test('executionPlanPreview does not coerce blank guardrail steps into step zero', () => {
    const module = createExecutionPlansModule();
    module.executionPlanForm = {
        scope_provider: 'openai',
        scope_model: 'gpt-5',
        name: 'Draft workflow',
        description: 'Preview should not invent step zero',
        features: {
            cache: true,
            audit: true,
            usage: true,
            guardrails: true,
            fallback: false
        },
        guardrails: [
            { ref: 'policy-system', step: '   ' }
        ]
    };

    assert.equal(
        JSON.stringify(module.executionPlanPreview().plan_payload.guardrails),
        JSON.stringify([])
    );
});

test('executionPlanWorkflowChart returns the shared chart contract for workflow sources', () => {
    const module = createExecutionPlansModule();

    assert.equal(
        JSON.stringify(module.executionPlanWorkflowChart({
            id: 'workflow-openai-gpt-5-v7',
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            },
            plan_payload: {
                features: {
                    cache: true,
                    audit: true,
                    usage: false,
                    guardrails: true,
                    fallback: true
                },
                guardrails: [
                    { ref: 'policy-system', step: 10 },
                    { ref: 'pii', step: 20 }
                ]
            }
        })),
        JSON.stringify({
            showGuardrails: true,
            guardrailLabel: '2 steps',
            showCache: true,
            cacheNodeClass: '',
            cacheConnClass: '',
            cacheStatusLabel: null,
            aiLabel: 'openai',
            aiSublabel: 'gpt-5',
            aiConnClass: '',
            aiNodeClass: '',
            responseConnClass: '',
            responseNodeClass: '',
            authNodeClass: '',
            authNodeSublabel: null,
            usageNodeClass: '',
            auditNodeClass: '',
            showAsync: true,
            showUsage: false,
            showAudit: true,
            workflowID: 'workflow-openai-gpt-5-v7'
        })
    );
});

test('executionPlanWorkflowChart masks globally disabled workflow features from persisted plans', () => {
    const module = createExecutionPlansModule();
    module.executionPlanRuntimeConfig = {
        FEATURE_FALLBACK_MODE: 'off',
        LOGGING_ENABLED: 'off',
        USAGE_ENABLED: 'off',
        GUARDRAILS_ENABLED: 'off',
        REDIS_URL: 'off',
        SEMANTIC_CACHE_ENABLED: 'off'
    };

    assert.equal(
        JSON.stringify(module.executionPlanWorkflowChart({
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            },
            plan_payload: {
                features: {
                    cache: true,
                    audit: true,
                    usage: true,
                    guardrails: true,
                    fallback: true
                },
                guardrails: [
                    { ref: 'policy-system', step: 10 }
                ]
            }
        })),
        JSON.stringify({
            showGuardrails: false,
            guardrailLabel: '',
            showCache: false,
            cacheNodeClass: '',
            cacheConnClass: '',
            cacheStatusLabel: null,
            aiLabel: 'openai',
            aiSublabel: 'gpt-5',
            aiConnClass: '',
            aiNodeClass: '',
            responseConnClass: '',
            responseNodeClass: '',
            authNodeClass: '',
            authNodeSublabel: null,
            usageNodeClass: '',
            auditNodeClass: '',
            showAsync: false,
            showUsage: false,
            showAudit: false,
            workflowID: null
        })
    );
});

test('executionPlanChartWorkflowID ignores the draft workflow preview sentinel and falls back to stored entry ids', () => {
    const module = createExecutionPlansModule();

    assert.equal(
        module.executionPlanChartWorkflowID(
            { id: 'draft-workflow-preview' },
            { execution_plan_version_id: 'historical-v1' }
        ),
        'historical-v1'
    );
    assert.equal(
        module.executionPlanChartWorkflowID(
            { id: 'draft-workflow-preview' },
            { execution_plan_version_id: 'draft-workflow-preview' }
        ),
        null
    );
});

test('executionPlanWorkflowIDChip copies the raw workflow id and resets copied feedback', async () => {
    const timers = createTimerHarness();
    const clipboardWrites = [];
    const module = createExecutionPlansModule({
        setTimeout: timers.setTimeout.bind(timers),
        clearTimeout: timers.clearTimeout.bind(timers),
        window: {
            navigator: {
                clipboard: {
                    writeText(value) {
                        clipboardWrites.push(value);
                        return Promise.resolve();
                    }
                }
            }
        }
    });

    const chip = module.executionPlanWorkflowIDChip('workflow-openai-gpt-5-v7');

    await chip.copyWorkflowID();

    assert.equal(
        JSON.stringify(clipboardWrites),
        JSON.stringify(['workflow-openai-gpt-5-v7'])
    );
    assert.equal(chip.copyState.copied, true);
    assert.equal(chip.copyState.error, false);
    assert.equal(chip.copyTitle(), 'Workflow ID copied');
    assert.equal(chip.copyAriaLabel(), 'Workflow ID copied workflow-openai-gpt-5-v7');

    timers.runAll();

    assert.equal(chip.copyState.copied, false);
    assert.equal(chip.copyState.error, false);
    assert.equal(chip.copyTitle(), 'Copy workflow ID');
});

test('executionPlanWorkflowIDChip marks clipboard failures as errors', async () => {
    const timers = createTimerHarness();
    const module = createExecutionPlansModule({
        setTimeout: timers.setTimeout.bind(timers),
        clearTimeout: timers.clearTimeout.bind(timers),
        window: {
            navigator: {
                clipboard: {
                    writeText() {
                        return Promise.reject(new Error('clipboard rejected'));
                    }
                }
            }
        }
    });

    const chip = module.executionPlanWorkflowIDChip('workflow-openai-gpt-5-v7');

    await chip.copyWorkflowID();

    assert.equal(chip.copyState.copied, false);
    assert.equal(chip.copyState.error, true);
    assert.equal(chip.copyTitle(), 'Unable to copy workflow ID');
    assert.equal(chip.copyAriaLabel(), 'Unable to copy workflow ID workflow-openai-gpt-5-v7');

    timers.runAll();

    assert.equal(chip.copyState.error, false);
});

test('executionPlanAuditChart returns the shared chart contract for audit runtime entries', () => {
    const module = createExecutionPlansModule();
    module.executionPlanVersionsByID = {
        'historical-v1': {
            id: 'historical-v1',
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            },
            plan_payload: {
                features: {
                    cache: false,
                    audit: true,
                    usage: true,
                    guardrails: true,
                    fallback: true
                },
                guardrails: [
                    { ref: 'policy-system', step: 10 }
                ]
            }
        }
    };

    assert.equal(
        JSON.stringify(module.executionPlanAuditChart({
            execution_plan_version_id: 'historical-v1',
            cache_type: 'semantic',
            provider: 'openai',
            model: 'gpt-5',
            status_code: 200
        })),
        JSON.stringify({
            showGuardrails: true,
            guardrailLabel: '1 step',
            showCache: true,
            cacheNodeClass: 'ep-node-success',
            cacheConnClass: 'ep-conn-hit',
            cacheStatusLabel: 'Hit (Semantic)',
            aiLabel: 'openai',
            aiSublabel: 'gpt-5',
            aiConnClass: 'ep-conn-dim',
            aiNodeClass: 'ep-node-skipped',
            responseConnClass: 'ep-conn-dim',
            responseNodeClass: 'ep-node-success',
            authNodeClass: '',
            authNodeSublabel: null,
            usageNodeClass: 'ep-node-success',
            auditNodeClass: 'ep-node-success',
            showAsync: true,
            showUsage: true,
            showAudit: true,
            workflowID: 'historical-v1'
        })
    );
});

test('executionPlanAuditChart forces audit nodes even when the workflow version cannot be resolved', () => {
    const module = createExecutionPlansModule();

    assert.equal(
        JSON.stringify(module.executionPlanAuditChart({
            execution_plan_version_id: 'missing-plan',
            cache_type: 'exact',
            provider: 'openai',
            model: 'gpt-5',
            status_code: 200
        })),
        JSON.stringify({
            showGuardrails: false,
            guardrailLabel: '',
            showCache: true,
            cacheNodeClass: 'ep-node-success',
            cacheConnClass: 'ep-conn-hit',
            cacheStatusLabel: 'Hit (Exact)',
            aiLabel: 'openai',
            aiSublabel: 'gpt-5',
            aiConnClass: 'ep-conn-dim',
            aiNodeClass: 'ep-node-skipped',
            responseConnClass: 'ep-conn-dim',
            responseNodeClass: 'ep-node-success',
            authNodeClass: '',
            authNodeSublabel: null,
            usageNodeClass: '',
            auditNodeClass: 'ep-node-success',
            showAsync: true,
            showUsage: false,
            showAudit: true,
            workflowID: 'missing-plan'
        })
    );
});

test('executionPlanAuditChart prefers request-time execution features over current workflow state', () => {
    const module = createExecutionPlansModule();
    module.executionPlanVersionsByID = {
        'historical-v2': {
            id: 'historical-v2',
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            },
            plan_payload: {
                features: {
                    cache: true,
                    audit: true,
                    usage: true,
                    guardrails: true,
                    fallback: true
                },
                guardrails: [
                    { ref: 'policy-system', step: 10 }
                ]
            }
        }
    };

    assert.equal(
        JSON.stringify(module.executionPlanAuditChart({
            execution_plan_version_id: 'historical-v2',
            provider: 'openai',
            model: 'gpt-5',
            status_code: 200,
            data: {
                execution_features: {
                    cache: false,
                    audit: true,
                    usage: false,
                    guardrails: false,
                    fallback: true
                }
            }
        })),
        JSON.stringify({
            showGuardrails: false,
            guardrailLabel: '',
            showCache: false,
            cacheNodeClass: '',
            cacheConnClass: '',
            cacheStatusLabel: null,
            aiLabel: 'openai',
            aiSublabel: 'gpt-5',
            aiConnClass: '',
            aiNodeClass: 'ep-node-success',
            responseConnClass: '',
            responseNodeClass: 'ep-node-success',
            authNodeClass: '',
            authNodeSublabel: null,
            usageNodeClass: '',
            auditNodeClass: 'ep-node-success',
            showAsync: true,
            showUsage: false,
            showAudit: true,
            workflowID: 'historical-v2'
        })
    );
});

test('epAsyncNodeClass only marks async nodes green when the audit-log override is enabled', () => {
    const module = createExecutionPlansModule();

    assert.equal(module.epAsyncNodeClass(true, false), '');
    assert.equal(module.epAsyncNodeClass(false, true), '');
    assert.equal(module.epAsyncNodeClass(true, true), 'ep-node-success');
});

test('executionPlanSubmitMode switches to save when an active workflow already matches the selected scope', () => {
    const module = createExecutionPlansModule();
    module.executionPlans = [
        {
            id: 'global-plan',
            scope: {
                scope_provider: '',
                scope_model: ''
            }
        },
        {
            id: 'openai-gpt-5-plan',
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            }
        }
    ];
    module.executionPlanForm = {
        scope_provider: 'openai',
        scope_model: 'gpt-5',
        name: '',
        description: '',
        features: {
            cache: true,
            audit: true,
            usage: true,
            guardrails: false,
            fallback: false
        },
        guardrails: []
    };

    assert.equal(module.executionPlanActiveScopeMatch().id, 'openai-gpt-5-plan');
    assert.equal(module.executionPlanSubmitMode(), 'save');
    assert.equal(module.executionPlanSubmitLabel(), 'Save');
    assert.equal(module.executionPlanSubmittingLabel(), 'Saving...');

    module.executionPlanForm.scope_model = 'gpt-4o-mini';
    assert.equal(module.executionPlanActiveScopeMatch(), null);
    assert.equal(module.executionPlanSubmitMode(), 'create');
    assert.equal(module.executionPlanSubmitLabel(), 'Create');
    assert.equal(module.executionPlanSubmittingLabel(), 'Creating...');
});

test('executionPlanActiveScopeMatch treats path-only selections as scoped', () => {
    const module = createExecutionPlansModule();
    module.executionPlans = [
        {
            id: 'global-plan',
            scope: {
                scope_provider: '',
                scope_model: ''
            }
        },
        {
            id: 'team-alpha-plan',
            scope: {
                scope_provider: '',
                scope_model: '',
                scope_user_path: '/team/alpha'
            }
        }
    ];
    module.executionPlanForm = module.defaultExecutionPlanForm();
    module.executionPlanForm.scope_user_path = 'team/alpha';

    assert.equal(module.executionPlanActiveScopeMatch().id, 'team-alpha-plan');
    assert.equal(module.executionPlanSubmitMode(), 'save');
});

test('buildExecutionPlanRequest emits provider-model payload and strips guardrails when disabled', () => {
    const module = createExecutionPlansModule();
    module.executionPlanRuntimeConfig = {
        FEATURE_FALLBACK_MODE: 'manual'
    };
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
            guardrails: false,
            fallback: false
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
                    guardrails: false,
                    fallback: false
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
        guardrails: true,
        fallback: false
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
            guardrails: true,
            fallback: false
        })
    );
    assert.equal(module.executionPlanFormHydrated, true);
    assert.equal(
        JSON.stringify(module.executionPlanForm.guardrails),
        JSON.stringify([{ ref: 'policy-system', step: 30 }])
    );
    assert.equal(module.executionPlanForm.scope_user_path, '/team/alpha');
});

test('openExecutionPlanCreate drops blank guardrail steps instead of hydrating them as step zero', () => {
    const module = createExecutionPlansModule();
    module.scrollExecutionPlanFormIntoView = () => {};

    module.openExecutionPlanCreate({
        scope: {
            scope_provider: 'openai',
            scope_model: 'gpt-5'
        },
        name: 'Hydrated workflow',
        description: 'Whitespace steps should stay invalid',
        plan_payload: {
            features: {
                cache: true,
                audit: true,
                usage: true,
                guardrails: true,
                fallback: false
            },
            guardrails: [
                { ref: 'policy-system', step: '   ' }
            ]
        }
    });

    assert.equal(
        JSON.stringify(module.executionPlanForm.guardrails),
        JSON.stringify([])
    );
});

test('executionPlanSourceGuardrails keeps step zero but drops negative and fractional steps from previews', () => {
    const module = createExecutionPlansModule();

    assert.equal(
        JSON.stringify(module.executionPlanSourceGuardrails({
            plan_payload: {
                guardrails: [
                    { ref: 'zero-step', step: 0 },
                    { ref: 'fractional', step: 1.5 },
                    { ref: 'negative', step: -1 },
                    { ref: 'valid', step: 10 }
                ]
            }
        })),
        JSON.stringify([
            { ref: 'zero-step', step: 0 },
            { ref: 'valid', step: 10 }
        ])
    );
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
                guardrails: false,
                fallback: true
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
    module.executionPlanRuntimeConfig = {
        FEATURE_FALLBACK_MODE: 'manual'
    };
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
            guardrails: true,
            fallback: true
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

test('executionPlanSourceFeatures defaults fallback to true when omitted', () => {
    const module = createExecutionPlansModule();

    assert.equal(
        JSON.stringify(module.executionPlanSourceFeatures({
            plan_payload: {
                features: {
                    cache: true,
                    audit: false,
                    usage: true,
                    guardrails: false
                }
            }
        })),
        JSON.stringify({
            cache: true,
            audit: false,
            usage: true,
            guardrails: false,
            fallback: true
        })
    );
});

test('executionPlanSourceFeatures respects effective runtime features for persisted plans', () => {
    const module = createExecutionPlansModule();

    assert.equal(
        JSON.stringify(module.executionPlanSourceFeatures({
            plan_payload: {
                features: {
                    cache: true,
                    audit: true,
                    usage: true,
                    guardrails: true,
                    fallback: true
                }
            },
            effective_features: {
                cache: false,
                audit: false,
                usage: true,
                guardrails: false,
                fallback: false
            }
        })),
        JSON.stringify({
            cache: false,
            audit: false,
            usage: true,
            guardrails: false,
            fallback: true
        })
    );
});

test('executionPlanSourceFeatures masks raw workflow features by global runtime config when effective features are unavailable', () => {
    const module = createExecutionPlansModule();
    module.executionPlanRuntimeConfig = {
        FEATURE_FALLBACK_MODE: 'off',
        LOGGING_ENABLED: 'off',
        USAGE_ENABLED: 'off',
        GUARDRAILS_ENABLED: 'off',
        REDIS_URL: 'off',
        SEMANTIC_CACHE_ENABLED: 'off'
    };

    assert.equal(
        JSON.stringify(module.executionPlanSourceFeatures({
            plan_payload: {
                features: {
                    cache: true,
                    audit: true,
                    usage: true,
                    guardrails: true,
                    fallback: true
                }
            }
        })),
        JSON.stringify({
            cache: false,
            audit: false,
            usage: false,
            guardrails: false,
            fallback: true
        })
    );
});

test('fetchExecutionPlanRuntimeConfig loads FEATURE_FALLBACK_MODE from the admin config endpoint', async () => {
    const module = createExecutionPlansModule({
        fetch(url, options) {
            assert.equal(url, '/admin/api/v1/dashboard/config');
            assert.equal(options.headers.authorization, 'Bearer token');
            return Promise.resolve({
                ok: true,
                json: async () => ({
                    FEATURE_FALLBACK_MODE: 'manual',
                    LOGGING_ENABLED: 'on',
                    USAGE_ENABLED: 'off',
                    GUARDRAILS_ENABLED: 'on',
                    REDIS_URL: 'on',
                    SEMANTIC_CACHE_ENABLED: 'off',
                    UNRELATED_FLAG: 'ignored'
                })
            });
        }
    });
    module.headers = () => ({ authorization: 'Bearer token' });
    module.handleFetchResponse = () => true;

    await module.fetchExecutionPlanRuntimeConfig();

    assert.equal(
        JSON.stringify(module.executionPlanRuntimeConfig),
        JSON.stringify({
            FEATURE_FALLBACK_MODE: 'manual',
            LOGGING_ENABLED: 'on',
            USAGE_ENABLED: 'off',
            GUARDRAILS_ENABLED: 'on',
            REDIS_URL: 'on',
            SEMANTIC_CACHE_ENABLED: 'off'
        })
    );
});

test('fetchExecutionPlanRuntimeConfig aborts hung requests and clears the timeout', async () => {
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
    module.handleFetchResponse = () => true;

    await module.fetchExecutionPlanRuntimeConfig();

    assert.equal(JSON.stringify(module.executionPlanRuntimeConfig), JSON.stringify({}));
    assert.equal(timeoutCleared, true);
});

test('buildExecutionPlanRequest omits fallback for new plans when the control is hidden', () => {
    const module = createExecutionPlansModule();
    module.executionPlanRuntimeConfig = {
        FEATURE_FALLBACK_MODE: 'off',
        LOGGING_ENABLED: 'on',
        USAGE_ENABLED: 'on',
        GUARDRAILS_ENABLED: 'off',
        REDIS_URL: 'on',
        SEMANTIC_CACHE_ENABLED: 'off'
    };
    module.executionPlanForm = {
        scope_provider: 'openai',
        scope_model: 'gpt-5',
        name: 'OpenAI GPT-5',
        description: 'Preserve hidden fallback state',
        features: {
            cache: true,
            audit: true,
            usage: true,
            guardrails: false,
            fallback: false
        },
        guardrails: []
    };

    assert.equal(
        JSON.stringify(module.buildExecutionPlanRequest().plan_payload.features),
        JSON.stringify({
            cache: true,
            audit: true,
            usage: true,
            guardrails: false
        })
    );
});

test('buildExecutionPlanRequest preserves fallback state for hydrated plans even when the control is hidden', () => {
    const module = createExecutionPlansModule();
    module.executionPlanRuntimeConfig = {
        FEATURE_FALLBACK_MODE: 'off',
        LOGGING_ENABLED: 'on',
        USAGE_ENABLED: 'on',
        GUARDRAILS_ENABLED: 'off',
        REDIS_URL: 'on',
        SEMANTIC_CACHE_ENABLED: 'off'
    };
    module.executionPlanFormHydrated = true;
    module.executionPlanHydratedScope = {
        scope_provider: 'openai',
        scope_model: 'gpt-5'
    };
    module.executionPlanForm = {
        scope_provider: 'openai',
        scope_model: 'gpt-5',
        name: 'OpenAI GPT-5',
        description: 'Preserve hidden fallback state',
        features: {
            cache: true,
            audit: true,
            usage: true,
            guardrails: false,
            fallback: false
        },
        guardrails: []
    };

    assert.equal(
        JSON.stringify(module.buildExecutionPlanRequest().plan_payload.features),
        JSON.stringify({
            cache: true,
            audit: true,
            usage: true,
            guardrails: false,
            fallback: false
        })
    );
});

test('buildExecutionPlanRequest preserves hidden fallback for fresh save flows that match an active workflow', () => {
    const module = createExecutionPlansModule();
    module.executionPlanRuntimeConfig = {
        FEATURE_FALLBACK_MODE: 'off',
        LOGGING_ENABLED: 'on',
        USAGE_ENABLED: 'on',
        GUARDRAILS_ENABLED: 'off',
        REDIS_URL: 'on',
        SEMANTIC_CACHE_ENABLED: 'off'
    };
    module.executionPlans = [
        {
            id: 'openai-gpt-5-plan',
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            },
            plan_payload: {
                features: {
                    cache: true,
                    audit: true,
                    usage: true,
                    guardrails: false,
                    fallback: false
                },
                guardrails: []
            }
        }
    ];
    module.executionPlanFormHydrated = false;
    module.executionPlanForm = {
        scope_provider: 'openai',
        scope_model: 'gpt-5',
        name: 'OpenAI GPT-5',
        description: 'Preserve hidden fallback from the active workflow',
        features: {
            cache: true,
            audit: true,
            usage: true,
            guardrails: false,
            fallback: true
        },
        guardrails: []
    };

    assert.equal(module.executionPlanSubmitMode(), 'save');
    assert.equal(
        JSON.stringify(module.buildExecutionPlanRequest().plan_payload.features),
        JSON.stringify({
            cache: true,
            audit: true,
            usage: true,
            guardrails: false,
            fallback: false
        })
    );
});

test('buildExecutionPlanRequest omits hidden fallback when a hydrated workflow is retargeted to a new scope', () => {
    const module = createExecutionPlansModule();
    module.executionPlanRuntimeConfig = {
        FEATURE_FALLBACK_MODE: 'off',
        LOGGING_ENABLED: 'on',
        USAGE_ENABLED: 'on',
        GUARDRAILS_ENABLED: 'off',
        REDIS_URL: 'on',
        SEMANTIC_CACHE_ENABLED: 'off'
    };
    module.executionPlanFormHydrated = true;
    module.executionPlanHydratedScope = {
        scope_provider: 'openai',
        scope_model: 'gpt-5'
    };
    module.executionPlanForm = {
        scope_provider: 'openai',
        scope_model: 'gpt-4o-mini',
        name: 'OpenAI GPT-4o mini',
        description: 'Retargeted hidden fallback should not carry over',
        features: {
            cache: true,
            audit: true,
            usage: true,
            guardrails: false,
            fallback: true
        },
        guardrails: []
    };

    assert.equal(
        JSON.stringify(module.buildExecutionPlanRequest().plan_payload.features),
        JSON.stringify({
            cache: true,
            audit: true,
            usage: true,
            guardrails: false
        })
    );
});

test('buildExecutionPlanRequest clamps globally disabled workflow features off even when the form has them enabled', () => {
    const module = createExecutionPlansModule();
    module.executionPlanRuntimeConfig = {
        FEATURE_FALLBACK_MODE: 'off',
        LOGGING_ENABLED: 'off',
        USAGE_ENABLED: 'off',
        GUARDRAILS_ENABLED: 'off',
        REDIS_URL: 'off',
        SEMANTIC_CACHE_ENABLED: 'off'
    };
    module.executionPlanForm = {
        scope_provider: 'openai',
        scope_model: 'gpt-5',
        name: 'OpenAI GPT-5',
        description: 'Globally disabled features should be forced off',
        features: {
            cache: true,
            audit: true,
            usage: true,
            guardrails: true,
            fallback: true
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
            description: 'Globally disabled features should be forced off',
            plan_payload: {
                schema_version: 1,
                features: {
                    cache: false,
                    audit: false,
                    usage: false,
                    guardrails: false
                },
                guardrails: []
            }
        })
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

test('validateExecutionPlanRequest accepts slashless scope_user_path values', () => {
    const module = createExecutionPlansModule();
    module.models = [
        { provider_type: 'openai', model: { id: 'gpt-5' } }
    ];
    module.executionPlans = [
        {
            id: 'openai-gpt-5-team-alpha',
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5',
                scope_user_path: '/team/alpha'
            }
        }
    ];
    module.executionPlanForm = module.defaultExecutionPlanForm();
    module.executionPlanForm.scope_provider = 'openai';
    module.executionPlanForm.scope_model = 'gpt-5';
    module.executionPlanForm.scope_user_path = 'team/alpha';

    const payload = {
        scope_provider: 'openai',
        scope_model: 'gpt-5',
        scope_user_path: '/team/alpha',
        name: 'Scoped workflow',
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
    };

    assert.equal(module.validateExecutionPlanRequest(payload), '');
    assert.equal(module.executionPlanActiveScopeMatch().id, 'openai-gpt-5-team-alpha');
    assert.equal(module.executionPlanSubmitMode(), 'save');
});

test('validateExecutionPlanRequest rejects invalid scope_user_path segments', () => {
    const module = createExecutionPlansModule();

    assert.equal(
        module.validateExecutionPlanRequest({
            scope_provider: '',
            scope_model: '',
            scope_user_path: '/team/../alpha',
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
        }),
        'User path cannot contain "." or ".." segments.'
    );
    assert.equal(
        module.validateExecutionPlanRequest({
            scope_provider: '',
            scope_model: '',
            scope_user_path: '/team:alpha',
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
        }),
        'User path cannot contain ":" segments.'
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

test('epRuntimeFromEntry derives cache hit state from cache_type without relying on headers', () => {
    const module = createExecutionPlansModule();

    assert.equal(
        JSON.stringify(module.epRuntimeFromEntry({ cache_type: 'semantic', provider: 'openai', model: 'gpt-5' })),
        JSON.stringify({
            cacheHit: true,
            cacheType: 'semantic',
            provider: 'openai',
            model: 'gpt-5',
            statusCode: null,
            responseSuccess: false,
            aiSuccess: false,
            authError: false,
            authMethod: null
        })
    );

    assert.equal(
        JSON.stringify(module.epRuntimeFromEntry({ cache_type: 'exact' })),
        JSON.stringify({
            cacheHit: true,
            cacheType: 'exact',
            provider: null,
            model: null,
            statusCode: null,
            responseSuccess: false,
            aiSuccess: false,
            authError: false,
            authMethod: null
        })
    );

    assert.equal(
        JSON.stringify(module.epRuntimeFromEntry({})),
        JSON.stringify({
            cacheHit: false,
            cacheType: null,
            provider: null,
            model: null,
            statusCode: null,
            responseSuccess: false,
            aiSuccess: false,
            authError: false,
            authMethod: null
        })
    );
});

test('audit runtime uses explicit cache-hit labels and highlights the uncached 200 path', () => {
    const module = createExecutionPlansModule();

    const semanticHit = module.epRuntimeFromEntry({
        cache_type: 'semantic',
        status_code: 200
    });
    assert.equal(module.epCacheNodeClass(semanticHit), 'ep-node-success');
    assert.equal(module.epCacheConnClass(semanticHit), 'ep-conn-hit');
    assert.equal(module.epCacheStatusLabel(semanticHit), 'Hit (Semantic)');
    assert.equal(module.epAiConnClass(semanticHit), 'ep-conn-dim');
    assert.equal(module.epAiNodeClass(semanticHit), 'ep-node-skipped');
    assert.equal(module.epResponseConnClass(semanticHit), 'ep-conn-dim');
    assert.equal(module.epResponseNodeClass(semanticHit), 'ep-node-success');

    const uncachedSuccess = module.epRuntimeFromEntry({
        provider: 'openai',
        model: 'gpt-5',
        status_code: 200
    });
    assert.equal(uncachedSuccess.cacheHit, false);
    assert.equal(module.epCacheNodeClass(uncachedSuccess), '');
    assert.equal(module.epCacheStatusLabel(uncachedSuccess), null);
    assert.equal(module.epAiConnClass(uncachedSuccess), '');
    assert.equal(module.epAiNodeClass(uncachedSuccess), 'ep-node-success');
    assert.equal(module.epResponseConnClass(uncachedSuccess), '');
    assert.equal(module.epResponseNodeClass(uncachedSuccess), 'ep-node-success');
});

test('epRuntimeFromEntry treats any uncached 2xx status as a successful AI and response path', () => {
    const module = createExecutionPlansModule();

    assert.equal(
        JSON.stringify(module.epRuntimeFromEntry({
            provider: 'openai',
            model: 'gpt-5',
            status_code: 204
        })),
        JSON.stringify({
            cacheHit: false,
            cacheType: null,
            provider: 'openai',
            model: 'gpt-5',
            statusCode: 204,
            responseSuccess: true,
            aiSuccess: true,
            authError: false,
            authMethod: null
        })
    );
});

test('auth runtime highlights auth node state from audit entries', () => {
    const module = createExecutionPlansModule();

    const failedAuth = module.epRuntimeFromEntry({
        auth_method: 'api_key',
        error_type: 'authentication_error'
    });
    assert.equal(module.epAuthNodeClass(failedAuth), 'ep-node-error');
    assert.equal(module.epAuthNodeSublabel(failedAuth), 'api_key');

    const masterKeyAuth = module.epRuntimeFromEntry({
        auth_method: 'master_key',
        status_code: 200
    });
    assert.equal(module.epAuthNodeClass(masterKeyAuth), 'ep-node-success');
    assert.equal(module.epAuthNodeSublabel(masterKeyAuth), 'master_key');
});

test('auditEntryExecutionPlan prefers an exact historical workflow version cache over active workflows', () => {
    const module = createExecutionPlansModule();
    module.executionPlans = [
        {
            id: 'active-current',
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            },
            plan_payload: {
                features: {
                    cache: false,
                    audit: false,
                    usage: false,
                    guardrails: false,
                    fallback: true
                },
                guardrails: []
            }
        }
    ];
    module.executionPlanVersionsByID = {
        'historical-v1': {
            id: 'historical-v1',
            active: false,
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            },
            plan_payload: {
                features: {
                    cache: true,
                    audit: true,
                    usage: true,
                    guardrails: true,
                    fallback: true
                },
                guardrails: [
                    { ref: 'policy-system', step: 10 }
                ]
            }
        }
    };

    const resolved = module.auditEntryExecutionPlan({
        execution_plan_version_id: 'historical-v1'
    });

    assert.equal(resolved.id, 'historical-v1');
    assert.equal(module.epHasUsage(resolved), true);
    assert.equal(module.epHasAudit(resolved), true);
    assert.equal(module.epHasGuardrails(resolved), true);
});

test('fetchExecutionPlanVersion loads a historical workflow version once and caches misses', async () => {
    const fetchCalls = [];
    const module = createExecutionPlansModule({
        fetch(url) {
            fetchCalls.push(url);
            if (url.endsWith('/historical-v2')) {
                return Promise.resolve({
                    ok: true,
                    status: 200,
                    json: async () => ({
                        id: 'historical-v2',
                        active: false,
                        scope: {
                            scope_provider: 'openai',
                            scope_model: 'gpt-5'
                        },
                        plan_payload: {
                            features: {
                                cache: true,
                                audit: true,
                                usage: true,
                                guardrails: false,
                                fallback: true
                            },
                            guardrails: []
                        }
                    })
                });
            }
            return Promise.resolve({
                ok: false,
                status: 404,
                statusText: 'Not Found'
            });
        }
    });
    module.headers = () => ({ authorization: 'Bearer token' });

    const loaded = await module.fetchExecutionPlanVersion('historical-v2');
    const repeated = await module.fetchExecutionPlanVersion('historical-v2');
    const missing = await module.fetchExecutionPlanVersion('missing-plan');
    const missingAgain = await module.fetchExecutionPlanVersion('missing-plan');

    assert.equal(loaded.id, 'historical-v2');
    assert.equal(repeated.id, 'historical-v2');
    assert.equal(missing, null);
    assert.equal(missingAgain, null);
    assert.deepEqual(fetchCalls, [
        '/admin/api/v1/execution-plans/historical-v2',
        '/admin/api/v1/execution-plans/missing-plan'
    ]);
});

test('fetchExecutionPlanVersion aborts hung requests, clears the timeout, and cleans up in-flight state', async () => {
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
            return 7;
        },
        clearTimeout(id) {
            assert.equal(id, 7);
            timeoutCleared = true;
        },
        fetch(_url, options) {
            assert.equal(options.signal.aborted, true);
            return Promise.reject(Object.assign(new Error('timed out'), { name: 'AbortError' }));
        }
    });
    module.headers = () => ({ authorization: 'Bearer token' });
    module.handleFetchResponse = () => true;

    const result = await module.fetchExecutionPlanVersion('historical-timeout');

    assert.equal(result, null);
    assert.equal(timeoutCleared, true);
    assert.equal(
        Object.prototype.hasOwnProperty.call(module.executionPlanVersionRequests || {}, 'historical-timeout'),
        false
    );
});
