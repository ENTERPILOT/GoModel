const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadWorkflowsModuleFactory(overrides = {}) {
    const clipboardSource = fs.readFileSync(path.join(__dirname, 'clipboard.js'), 'utf8');
    const source = fs.readFileSync(path.join(__dirname, 'workflows.js'), 'utf8');
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
    return context.window.dashboardWorkflowsModule;
}

function createWorkflowsModule(overrides) {
    const factory = loadWorkflowsModuleFactory(overrides);
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

test('workflowProviderOptions returns unique sorted provider names', () => {
    const module = createWorkflowsModule();
    module.models = [
        { provider_type: 'anthropic', model: { id: 'claude-3-7' } },
        { provider_type: 'openai', model: { id: 'gpt-5' } },
        { provider_type: 'openai', model: { id: 'gpt-4o-mini' } }
    ];

    assert.equal(
        JSON.stringify(module.workflowProviderOptions()),
        JSON.stringify(['anthropic', 'openai'])
    );
});

test('defaultWorkflowForm starts fallback enabled for new workflows', () => {
    const module = createWorkflowsModule();

    assert.equal(module.workflowForm.features.fallback, true);
    assert.equal(module.workflowForm.features.budget, true);
    assert.equal(module.defaultWorkflowForm().features.fallback, true);
    assert.equal(module.defaultWorkflowForm().features.budget, true);
});

test('workflowPreview mirrors the draft workflow card state from the editor form', () => {
    const module = createWorkflowsModule();
    module.workflowForm = {
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
        JSON.stringify(module.workflowPreview()),
        JSON.stringify({
            id: 'draft-workflow-preview',
            scope_type: 'provider_model',
            scope_display: 'openai/gpt-5',
            scope: {
                scope_provider_name: 'openai',
                scope_model: 'gpt-5'
            },
            name: 'Draft workflow',
            description: 'Live preview of the edited workflow',
            workflow_payload: {
                schema_version: 1,
                features: {
                    cache: true,
                    audit: false,
                    usage: true,
                    budget: true,
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

test('workflowPreview renders path-scoped draft labels using canonical scope display', () => {
    const module = createWorkflowsModule();
    module.workflowForm = {
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
        JSON.stringify(module.workflowPreview()),
        JSON.stringify({
            id: 'draft-workflow-preview',
            scope_type: 'provider_model_path',
            scope_display: 'openai/gpt-5 @ /team/alpha',
            scope: {
                scope_provider_name: 'openai',
                scope_model: 'gpt-5',
                scope_user_path: '/team/alpha'
            },
            name: 'Path workflow',
            description: 'Preview should include the canonical path scope',
            workflow_payload: {
                schema_version: 1,
                features: {
                    cache: true,
                    audit: true,
                    usage: true,
                    budget: true,
                    guardrails: false,
                    fallback: false
                },
                guardrails: []
            }
        })
    );
});

test('workflowPreview does not coerce blank guardrail steps into step zero', () => {
    const module = createWorkflowsModule();
    module.workflowForm = {
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
        JSON.stringify(module.workflowPreview().workflow_payload.guardrails),
        JSON.stringify([])
    );
});

test('workflowChart returns the shared chart contract for workflow sources', () => {
    const module = createWorkflowsModule();

    assert.equal(
        JSON.stringify(module.workflowChart({
            id: 'workflow-openai-gpt-5-v7',
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            },
            workflow_payload: {
                features: {
                    cache: true,
                    audit: true,
                    usage: false,
                    budget: true,
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
            showBudget: false,
            budgetNodeClass: '',
            budgetStatusLabel: null,
            showGuardrails: true,
            guardrailLabel: '2 steps',
            showCache: true,
            cacheNodeClass: '',
            cacheConnClass: '',
            cacheStatusLabel: null,
            showFailover: true,
            failoverNodeClass: '',
            failoverConnClass: '',
            failoverStatusLabel: null,
            failoverTargetLabel: null,
            aiLabel: 'openai',
            aiSublabel: 'gpt-5',
            aiConnClass: '',
            aiNodeClass: '',
            responseConnClass: '',
            responseNodeClass: '',
            responseNodeSublabel: null,
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

test('workflowChart masks globally disabled workflow features from persisted workflows', () => {
    const module = createWorkflowsModule();
    module.workflowRuntimeConfig = {
        FEATURE_FALLBACK_MODE: 'off',
        LOGGING_ENABLED: 'off',
        USAGE_ENABLED: 'off',
        BUDGETS_ENABLED: 'off',
        GUARDRAILS_ENABLED: 'off',
        REDIS_URL: 'off',
        SEMANTIC_CACHE_ENABLED: 'off'
    };

    assert.equal(
        JSON.stringify(module.workflowChart({
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            },
            workflow_payload: {
                features: {
                    cache: true,
                    audit: true,
                    usage: true,
                    budget: true,
                    guardrails: true,
                    fallback: true
                },
                guardrails: [
                    { ref: 'policy-system', step: 10 }
                ]
            }
        })),
        JSON.stringify({
            showBudget: false,
            budgetNodeClass: '',
            budgetStatusLabel: null,
            showGuardrails: false,
            guardrailLabel: '',
            showCache: false,
            cacheNodeClass: '',
            cacheConnClass: '',
            cacheStatusLabel: null,
            showFailover: true,
            failoverNodeClass: '',
            failoverConnClass: '',
            failoverStatusLabel: null,
            failoverTargetLabel: null,
            aiLabel: 'openai',
            aiSublabel: 'gpt-5',
            aiConnClass: '',
            aiNodeClass: '',
            responseConnClass: '',
            responseNodeClass: '',
            responseNodeSublabel: null,
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

test('workflowChartWorkflowID ignores the draft workflow preview sentinel and falls back to stored entry ids', () => {
    const module = createWorkflowsModule();

    assert.equal(
        module.workflowChartWorkflowID(
            { id: 'draft-workflow-preview' },
            { workflow_version_id: 'historical-v1' }
        ),
        'historical-v1'
    );
    assert.equal(
        module.workflowChartWorkflowID(
            { id: 'draft-workflow-preview' },
            { workflow_version_id: 'draft-workflow-preview' }
        ),
        null
    );
});

test('workflowIDChip copies the raw workflow id and resets copied feedback', async () => {
    const timers = createTimerHarness();
    const clipboardWrites = [];
    const module = createWorkflowsModule({
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

    const chip = module.workflowIDChip('workflow-openai-gpt-5-v7');

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

test('workflowIDChip marks clipboard failures as errors', async () => {
    const timers = createTimerHarness();
    const module = createWorkflowsModule({
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

    const chip = module.workflowIDChip('workflow-openai-gpt-5-v7');

    await chip.copyWorkflowID();

    assert.equal(chip.copyState.copied, false);
    assert.equal(chip.copyState.error, true);
    assert.equal(chip.copyTitle(), 'Unable to copy workflow ID');
    assert.equal(chip.copyAriaLabel(), 'Unable to copy workflow ID workflow-openai-gpt-5-v7');

    timers.runAll();

    assert.equal(chip.copyState.error, false);
});

test('workflowAuditChart returns the shared chart contract for audit runtime entries', () => {
    const module = createWorkflowsModule();
    module.workflowVersionsByID = {
        'historical-v1': {
            id: 'historical-v1',
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            },
            workflow_payload: {
                features: {
                    cache: false,
                    audit: true,
                    usage: true,
                    budget: true,
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
        JSON.stringify(module.workflowAuditChart({
            workflow_version_id: 'historical-v1',
            cache_type: 'semantic',
            provider: 'openai',
            model: 'gpt-5',
            status_code: 200
        })),
        JSON.stringify({
            showBudget: true,
            budgetNodeClass: 'workflow-node-success',
            budgetStatusLabel: null,
            showGuardrails: true,
            guardrailLabel: '1 step',
            showCache: true,
            cacheNodeClass: 'workflow-node-success',
            cacheConnClass: 'workflow-conn-hit',
            cacheStatusLabel: 'Hit (Semantic)',
            showFailover: true,
            failoverNodeClass: 'workflow-node-skipped',
            failoverConnClass: 'workflow-conn-dim',
            failoverStatusLabel: null,
            failoverTargetLabel: null,
            aiLabel: 'openai',
            aiSublabel: 'gpt-5',
            aiConnClass: 'workflow-conn-dim',
            aiNodeClass: 'workflow-node-skipped',
            responseConnClass: 'workflow-conn-dim',
            responseNodeClass: 'workflow-node-success',
            responseNodeSublabel: '200',
            authNodeClass: '',
            authNodeSublabel: null,
            usageNodeClass: 'workflow-node-success',
            auditNodeClass: 'workflow-node-success',
            showAsync: true,
            showUsage: true,
            showAudit: true,
            workflowID: 'historical-v1'
        })
    );
});

test('workflowAuditChart forces audit nodes even when the workflow version cannot be resolved', () => {
    const module = createWorkflowsModule();

    assert.equal(
        JSON.stringify(module.workflowAuditChart({
            workflow_version_id: 'missing-workflow',
            cache_type: 'exact',
            provider: 'openai',
            model: 'gpt-5',
            status_code: 200
        })),
        JSON.stringify({
            showBudget: false,
            budgetNodeClass: '',
            budgetStatusLabel: null,
            showGuardrails: false,
            guardrailLabel: '',
            showCache: true,
            cacheNodeClass: 'workflow-node-success',
            cacheConnClass: 'workflow-conn-hit',
            cacheStatusLabel: 'Hit (Exact)',
            showFailover: false,
            failoverNodeClass: '',
            failoverConnClass: '',
            failoverStatusLabel: null,
            failoverTargetLabel: null,
            aiLabel: 'openai',
            aiSublabel: 'gpt-5',
            aiConnClass: 'workflow-conn-dim',
            aiNodeClass: 'workflow-node-skipped',
            responseConnClass: 'workflow-conn-dim',
            responseNodeClass: 'workflow-node-success',
            responseNodeSublabel: '200',
            authNodeClass: '',
            authNodeSublabel: null,
            usageNodeClass: '',
            auditNodeClass: 'workflow-node-success',
            showAsync: true,
            showUsage: false,
            showAudit: true,
            workflowID: 'missing-workflow'
        })
    );
});

test('workflowAuditChart prefers request-time workflow features over current workflow state', () => {
    const module = createWorkflowsModule();
    module.workflowVersionsByID = {
        'historical-v2': {
            id: 'historical-v2',
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            },
            workflow_payload: {
                features: {
                    cache: true,
                    audit: true,
                    usage: true,
                    budget: true,
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
        JSON.stringify(module.workflowAuditChart({
            workflow_version_id: 'historical-v2',
            provider: 'openai',
            model: 'gpt-5',
            status_code: 200,
            data: {
                workflow_features: {
                    cache: false,
                    audit: true,
                    usage: false,
                    budget: false,
                    guardrails: false,
                    fallback: true
                }
            }
        })),
        JSON.stringify({
            showBudget: false,
            budgetNodeClass: '',
            budgetStatusLabel: null,
            showGuardrails: false,
            guardrailLabel: '',
            showCache: false,
            cacheNodeClass: '',
            cacheConnClass: '',
            cacheStatusLabel: null,
            showFailover: true,
            failoverNodeClass: '',
            failoverConnClass: '',
            failoverStatusLabel: null,
            failoverTargetLabel: null,
            aiLabel: 'openai',
            aiSublabel: 'gpt-5',
            aiConnClass: '',
            aiNodeClass: 'workflow-node-success',
            responseConnClass: '',
            responseNodeClass: 'workflow-node-success',
            responseNodeSublabel: '200',
            authNodeClass: '',
            authNodeSublabel: null,
            usageNodeClass: '',
            auditNodeClass: 'workflow-node-success',
            showAsync: true,
            showUsage: false,
            showAudit: true,
            workflowID: 'historical-v2'
        })
    );
});

test('workflowAuditChart highlights configured failover redirects and exposes the selected target', () => {
    const module = createWorkflowsModule();
    module.workflowVersionsByID = {
        'historical-v3': {
            id: 'historical-v3',
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            },
            workflow_payload: {
                features: {
                    cache: false,
                    audit: true,
                    usage: true,
                    budget: true,
                    guardrails: false,
                    fallback: true
                },
                guardrails: []
            }
        }
    };

    assert.equal(
        JSON.stringify(module.workflowAuditChart({
            workflow_version_id: 'historical-v3',
            provider: 'azure',
            requested_model: 'gpt-5',
            status_code: 200,
            data: {
                workflow_features: {
                    cache: false,
                    audit: true,
                    usage: true,
                    budget: true,
                    guardrails: false,
                    fallback: true
                },
                failover: {
                    target_model: 'azure/gpt-4o'
                }
            }
        })),
        JSON.stringify({
            showBudget: true,
            budgetNodeClass: 'workflow-node-success',
            budgetStatusLabel: null,
            showGuardrails: false,
            guardrailLabel: '',
            showCache: false,
            cacheNodeClass: '',
            cacheConnClass: '',
            cacheStatusLabel: null,
            showFailover: true,
            failoverNodeClass: 'workflow-node-success',
            failoverConnClass: 'workflow-conn-hit',
            failoverStatusLabel: 'Redirected',
            failoverTargetLabel: 'azure/gpt-4o',
            aiLabel: 'openai',
            aiSublabel: 'gpt-5',
            aiConnClass: '',
            aiNodeClass: 'workflow-node-success',
            responseConnClass: '',
            responseNodeClass: 'workflow-node-success',
            responseNodeSublabel: '200',
            authNodeClass: '',
            authNodeSublabel: null,
            usageNodeClass: 'workflow-node-success',
            auditNodeClass: 'workflow-node-success',
            showAsync: true,
            showUsage: true,
            showAudit: true,
            workflowID: 'historical-v3'
        })
    );
    assert.equal(module.workflowFailoverTarget({
        data: {
            failover: {
                target_model: 'azure/gpt-4o'
            }
        }
    }), 'azure/gpt-4o');
});

test('workflowRuntimeFromEntry preserves the primary route for cross-provider failover entries', () => {
    const module = createWorkflowsModule();

    assert.equal(
        JSON.stringify(module.workflowRuntimeFromEntry({
            provider: 'azure',
            requested_model: 'gpt-5',
            status_code: 200,
            data: {
                failover: {
                    target_model: 'azure/gpt-4o'
                }
            }
        }, {
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            }
        })),
        JSON.stringify({
            cacheHit: false,
            cacheType: null,
            failoverTarget: 'azure/gpt-4o',
            provider: 'openai',
            model: 'gpt-5',
            statusCode: 200,
            responseSuccess: true,
            aiSuccess: true,
            authError: false,
            authMethod: null,
            budgetExceeded: false
        })
    );
});

test('workflowAsyncNodeClass only marks async nodes green when the audit-log override is enabled', () => {
    const module = createWorkflowsModule();

    assert.equal(module.workflowAsyncNodeClass(true, false), '');
    assert.equal(module.workflowAsyncNodeClass(false, true), '');
    assert.equal(module.workflowAsyncNodeClass(true, true), 'workflow-node-success');
});

test('workflowSubmitMode switches to save when an active workflow already matches the selected scope', () => {
    const module = createWorkflowsModule();
    module.workflows = [
        {
            id: 'global-workflow',
            scope: {
                scope_provider: '',
                scope_model: ''
            }
        },
        {
            id: 'openai-gpt-5-workflow',
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            }
        }
    ];
    module.workflowForm = {
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

    assert.equal(module.workflowActiveScopeMatch().id, 'openai-gpt-5-workflow');
    assert.equal(module.workflowSubmitMode(), 'save');
    assert.equal(module.workflowSubmitLabel(), 'Save');
    assert.equal(module.workflowSubmittingLabel(), 'Saving...');

    module.workflowForm.scope_model = 'gpt-4o-mini';
    assert.equal(module.workflowActiveScopeMatch(), null);
    assert.equal(module.workflowSubmitMode(), 'create');
    assert.equal(module.workflowSubmitLabel(), 'Create');
    assert.equal(module.workflowSubmittingLabel(), 'Creating...');
});

test('workflowActiveScopeMatch treats path-only selections as scoped', () => {
    const module = createWorkflowsModule();
    module.workflows = [
        {
            id: 'global-workflow',
            scope: {
                scope_provider: '',
                scope_model: ''
            }
        },
        {
            id: 'team-alpha-workflow',
            scope: {
                scope_provider: '',
                scope_model: '',
                scope_user_path: '/team/alpha'
            }
        }
    ];
    module.workflowForm = module.defaultWorkflowForm();
    module.workflowForm.scope_user_path = 'team/alpha';

    assert.equal(module.workflowActiveScopeMatch().id, 'team-alpha-workflow');
    assert.equal(module.workflowSubmitMode(), 'save');
});

test('buildWorkflowRequest emits provider-model payload and strips guardrails when disabled', () => {
    const module = createWorkflowsModule();
    module.workflowRuntimeConfig = {
        FEATURE_FALLBACK_MODE: 'manual'
    };
    module.workflowForm = {
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
        JSON.stringify(module.buildWorkflowRequest()),
        JSON.stringify({
            scope_provider_name: 'openai',
            scope_model: 'gpt-5',
            scope_user_path: '/team/alpha',
            name: 'OpenAI GPT-5',
            description: 'Primary translated requests',
            workflow_payload: {
                schema_version: 1,
                features: {
                    cache: true,
                    audit: true,
                    usage: true,
                    budget: true,
                    guardrails: false,
                    fallback: false
                },
                guardrails: []
            }
        })
    );
});

test('buildWorkflowRequest disables budget when usage is disabled in the form', () => {
    const module = createWorkflowsModule();
    module.workflowRuntimeConfig = {
        USAGE_ENABLED: 'on',
        BUDGETS_ENABLED: 'on'
    };
    module.workflowForm = {
        scope_provider: 'openai',
        scope_model: 'gpt-5',
        name: 'OpenAI GPT-5',
        description: 'Usage disabled',
        features: {
            cache: true,
            audit: true,
            usage: false,
            budget: true,
            guardrails: false,
            fallback: true
        },
        guardrails: []
    };

    const features = module.buildWorkflowRequest().workflow_payload.features;

    assert.equal(features.usage, false);
    assert.equal(features.budget, false);
});

test('openWorkflowCreate hydrates saved features and guardrails from payload', () => {
    const module = createWorkflowsModule();
    module.workflowSourceFeatures = () => ({
        cache: false,
        audit: false,
        usage: true,
        budget: false,
        guardrails: true,
        fallback: false
    });
    module.workflowSourceGuardrails = () => ([
        { ref: 'policy-system', step: 30 }
    ]);
    module.focusWorkflowForm = () => {};

    module.openWorkflowCreate({
        scope: {
            scope_provider: 'openai',
            scope_model: 'gpt-5',
            scope_user_path: '/team/alpha'
        },
        name: 'Hydrated workflow',
        description: 'Uses helper normalization',
        workflow_payload: {
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
        JSON.stringify(module.workflowForm.features),
        JSON.stringify({
            cache: true,
            audit: true,
            usage: false,
            budget: true,
            guardrails: false,
            fallback: true
        })
    );
    assert.equal(module.workflowFormHydrated, true);
    assert.equal(
        JSON.stringify(module.workflowForm.guardrails),
        JSON.stringify([{ ref: 'wrong-source', step: 10 }])
    );
    assert.equal(module.workflowForm.scope_user_path, '/team/alpha');
});

test('openWorkflowCreate drops blank guardrail steps instead of hydrating them as step zero', () => {
    const module = createWorkflowsModule();
    module.focusWorkflowForm = () => {};

    module.openWorkflowCreate({
        scope: {
            scope_provider: 'openai',
            scope_model: 'gpt-5'
        },
        name: 'Hydrated workflow',
        description: 'Whitespace steps should stay invalid',
        workflow_payload: {
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
        JSON.stringify(module.workflowForm.guardrails),
        JSON.stringify([])
    );
});

test('workflowSourceGuardrails keeps step zero but drops negative and fractional steps from previews', () => {
    const module = createWorkflowsModule();

    assert.equal(
        JSON.stringify(module.workflowSourceGuardrails({
            workflow_payload: {
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
    const module = createWorkflowsModule();
    module.models = [
        { provider_type: 'openai', model: { id: 'gpt-5' } }
    ];
    module.focusWorkflowForm = () => {};

    module.openWorkflowCreate({
        scope: {
            scope_provider: 'anthropic',
            scope_model: 'claude-retired'
        },
        name: 'Retired workflow',
        description: 'Cloned from an older deployment',
        workflow_payload: {
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
        JSON.stringify(module.workflowProviderOptions()),
        JSON.stringify(['anthropic', 'openai'])
    );
    assert.equal(
        JSON.stringify(module.workflowModelOptions('anthropic')),
        JSON.stringify(['claude-retired'])
    );
    assert.equal(module.validateWorkflowRequest(module.buildWorkflowRequest()), '');

    const invalidPayload = module.buildWorkflowRequest();
    invalidPayload.scope_model = 'different-retired-model';
    assert.equal(
        module.validateWorkflowRequest(invalidPayload),
        'Choose a registered model for the selected provider name.'
    );
});

test('openWorkflowCreate focuses the workflow editor after opening', () => {
    let querySelectorCalls = 0;
    let nextTickCallback = null;
    let animationFrameCallback = null;
    const calls = [];
    const module = createWorkflowsModule({
        window: {
            requestAnimationFrame(callback) {
                animationFrameCallback = callback;
            }
        }
    });
    module.$refs = {
        workflowEditor: {
            querySelector() {
                querySelectorCalls++;
                return {
                    focus(options) {
                        calls.push(options);
                    }
                };
            }
        }
    };
    module.$nextTick = (callback) => {
        nextTickCallback = callback;
    };

    module.openWorkflowCreate();

    assert.equal(module.workflowFormOpen, true);
    assert.deepEqual(calls, []);
    assert.equal(typeof nextTickCallback, 'function');

    nextTickCallback();

    assert.equal(typeof animationFrameCallback, 'function');
    assert.deepEqual(calls, []);

    animationFrameCallback();

    assert.equal(querySelectorCalls, 1);
    assert.deepEqual(JSON.parse(JSON.stringify(calls)), [
        { preventScroll: true }
    ]);
});

test('buildWorkflowRequest preserves blank guardrail steps as invalid so validation rejects them', () => {
    const module = createWorkflowsModule();
    module.workflowRuntimeConfig = {
        FEATURE_FALLBACK_MODE: 'manual'
    };
    module.models = [
        { provider_type: 'openai', model: { id: 'gpt-5' } }
    ];
    module.workflowForm = {
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

    const payload = module.buildWorkflowRequest();

    assert.ok(Number.isNaN(payload.workflow_payload.guardrails[0].step));
    assert.equal(
        module.validateWorkflowRequest(payload),
        'Each guardrail step must use a non-negative integer step number.'
    );
});

test('workflowSourceFeatures defaults fallback to true when omitted', () => {
    const module = createWorkflowsModule();

    assert.equal(
        JSON.stringify(module.workflowSourceFeatures({
            workflow_payload: {
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
            budget: true,
            guardrails: false,
            fallback: true
        })
    );
});

test('workflowSourceFeatures respects effective runtime features for persisted workflows', () => {
    const module = createWorkflowsModule();

    assert.equal(
        JSON.stringify(module.workflowSourceFeatures({
            workflow_payload: {
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
                budget: true,
                guardrails: false,
                fallback: false
            }
        })),
        JSON.stringify({
            cache: false,
            audit: false,
            usage: true,
            budget: true,
            guardrails: false,
            fallback: true
        })
    );
});

test('workflowSourceFeatures masks raw workflow features by global runtime config when effective features are unavailable', () => {
    const module = createWorkflowsModule();
    module.workflowRuntimeConfig = {
        FEATURE_FALLBACK_MODE: 'off',
        LOGGING_ENABLED: 'off',
        USAGE_ENABLED: 'off',
        BUDGETS_ENABLED: 'off',
        GUARDRAILS_ENABLED: 'off',
        REDIS_URL: 'off',
        SEMANTIC_CACHE_ENABLED: 'off'
    };

    assert.equal(
        JSON.stringify(module.workflowSourceFeatures({
            workflow_payload: {
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
            budget: false,
            guardrails: false,
            fallback: true
        })
    );
});

test('fetchWorkflowRuntimeConfig loads FEATURE_FALLBACK_MODE from the admin config endpoint', async () => {
    const module = createWorkflowsModule({
        fetch(url, options) {
            assert.equal(url, '/admin/api/v1/dashboard/config');
            assert.equal(options.headers.authorization, 'Bearer token');
            return Promise.resolve({
                ok: true,
                json: async () => ({
                    FEATURE_FALLBACK_MODE: 'manual',
                    LOGGING_ENABLED: 'on',
                    USAGE_ENABLED: 'off',
                    BUDGETS_ENABLED: 'on',
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

    await module.fetchWorkflowRuntimeConfig();

    assert.equal(
        JSON.stringify(module.workflowRuntimeConfig),
        JSON.stringify({
            FEATURE_FALLBACK_MODE: 'manual',
            LOGGING_ENABLED: 'on',
            USAGE_ENABLED: 'off',
            BUDGETS_ENABLED: 'on',
            GUARDRAILS_ENABLED: 'on',
            REDIS_URL: 'on',
            SEMANTIC_CACHE_ENABLED: 'off'
        })
    );
});

test('fetchWorkflowRuntimeConfig delegates cache overview refresh after loading runtime config', async () => {
    let cacheOverviewCalls = 0;
    const module = createWorkflowsModule({
        fetch() {
            return Promise.resolve({
                ok: true,
                json: async () => ({
                    CACHE_ENABLED: 'on'
                })
            });
        }
    });
    module.handleFetchResponse = () => true;
    module.headers = () => ({});
    module.fetchCacheOverview = () => {
        cacheOverviewCalls++;
    };

    await module.fetchWorkflowRuntimeConfig();
    assert.equal(cacheOverviewCalls, 1);
});

test('fetchWorkflowRuntimeConfig aborts hung requests and clears the timeout', async () => {
    let timeoutCleared = false;
    class AbortControllerStub {
        constructor() {
            this.signal = { aborted: false };
        }

        abort() {
            this.signal.aborted = true;
        }
    }

    const module = createWorkflowsModule({
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

    await module.fetchWorkflowRuntimeConfig();

    assert.equal(JSON.stringify(module.workflowRuntimeConfig), JSON.stringify({}));
    assert.equal(timeoutCleared, true);
});

test('buildWorkflowRequest omits fallback for new workflows when the control is hidden', () => {
    const module = createWorkflowsModule();
    module.workflowRuntimeConfig = {
        FEATURE_FALLBACK_MODE: 'off',
        LOGGING_ENABLED: 'on',
        USAGE_ENABLED: 'on',
        GUARDRAILS_ENABLED: 'off',
        REDIS_URL: 'on',
        SEMANTIC_CACHE_ENABLED: 'off'
    };
    module.workflowForm = {
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
        JSON.stringify(module.buildWorkflowRequest().workflow_payload.features),
        JSON.stringify({
            cache: true,
            audit: true,
            usage: true,
            budget: true,
            guardrails: false
        })
    );
});

test('buildWorkflowRequest preserves fallback state for hydrated workflows even when the control is hidden', () => {
    const module = createWorkflowsModule();
    module.workflowRuntimeConfig = {
        FEATURE_FALLBACK_MODE: 'off',
        LOGGING_ENABLED: 'on',
        USAGE_ENABLED: 'on',
        GUARDRAILS_ENABLED: 'off',
        REDIS_URL: 'on',
        SEMANTIC_CACHE_ENABLED: 'off'
    };
    module.workflowFormHydrated = true;
    module.workflowHydratedScope = {
        scope_provider: 'openai',
        scope_model: 'gpt-5'
    };
    module.workflowForm = {
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
        JSON.stringify(module.buildWorkflowRequest().workflow_payload.features),
        JSON.stringify({
            cache: true,
            audit: true,
            usage: true,
            budget: true,
            guardrails: false,
            fallback: false
        })
    );
});

test('buildWorkflowRequest preserves hidden fallback for fresh save flows that match an active workflow', () => {
    const module = createWorkflowsModule();
    module.workflowRuntimeConfig = {
        FEATURE_FALLBACK_MODE: 'off',
        LOGGING_ENABLED: 'on',
        USAGE_ENABLED: 'on',
        GUARDRAILS_ENABLED: 'off',
        REDIS_URL: 'on',
        SEMANTIC_CACHE_ENABLED: 'off'
    };
    module.workflows = [
        {
            id: 'openai-gpt-5-workflow',
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            },
            workflow_payload: {
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
    module.workflowFormHydrated = false;
    module.workflowForm = {
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

    assert.equal(module.workflowSubmitMode(), 'save');
    assert.equal(
        JSON.stringify(module.buildWorkflowRequest().workflow_payload.features),
        JSON.stringify({
            cache: true,
            audit: true,
            usage: true,
            budget: true,
            guardrails: false,
            fallback: false
        })
    );
});

test('buildWorkflowRequest omits hidden fallback when a hydrated workflow is retargeted to a new scope', () => {
    const module = createWorkflowsModule();
    module.workflowRuntimeConfig = {
        FEATURE_FALLBACK_MODE: 'off',
        LOGGING_ENABLED: 'on',
        USAGE_ENABLED: 'on',
        GUARDRAILS_ENABLED: 'off',
        REDIS_URL: 'on',
        SEMANTIC_CACHE_ENABLED: 'off'
    };
    module.workflowFormHydrated = true;
    module.workflowHydratedScope = {
        scope_provider: 'openai',
        scope_model: 'gpt-5'
    };
    module.workflowForm = {
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
        JSON.stringify(module.buildWorkflowRequest().workflow_payload.features),
        JSON.stringify({
            cache: true,
            audit: true,
            usage: true,
            budget: true,
            guardrails: false
        })
    );
});

test('buildWorkflowRequest clamps globally disabled workflow features off even when the form has them enabled', () => {
    const module = createWorkflowsModule();
    module.workflowRuntimeConfig = {
        FEATURE_FALLBACK_MODE: 'off',
        LOGGING_ENABLED: 'off',
        USAGE_ENABLED: 'off',
        BUDGETS_ENABLED: 'off',
        GUARDRAILS_ENABLED: 'off',
        REDIS_URL: 'off',
        SEMANTIC_CACHE_ENABLED: 'off'
    };
    module.workflowForm = {
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
        JSON.stringify(module.buildWorkflowRequest()),
        JSON.stringify({
            scope_provider_name: 'openai',
            scope_model: 'gpt-5',
            name: 'OpenAI GPT-5',
            description: 'Globally disabled features should be forced off',
            workflow_payload: {
                schema_version: 1,
                features: {
                    cache: false,
                    audit: false,
                    usage: false,
                    budget: false,
                    guardrails: false
                },
                guardrails: []
            }
        })
    );
});

test('validateWorkflowRequest rejects negative guardrail step numbers', () => {
    const module = createWorkflowsModule();
    const payload = {
        scope_provider: '',
        scope_model: '',
        name: 'Global',
        workflow_payload: {
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
        module.validateWorkflowRequest(payload),
        'Each guardrail step must use a non-negative integer step number.'
    );
});

test('validateWorkflowRequest rejects duplicate guardrail refs', () => {
    const module = createWorkflowsModule();
    const payload = {
        scope_provider: '',
        scope_model: '',
        name: 'Global',
        workflow_payload: {
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
        module.validateWorkflowRequest(payload),
        'Each guardrail ref may appear only once in a workflow.'
    );
});

test('validateWorkflowRequest accepts slashless scope_user_path values', () => {
    const module = createWorkflowsModule();
    module.models = [
        { provider_type: 'openai', model: { id: 'gpt-5' } }
    ];
    module.workflows = [
        {
            id: 'openai-gpt-5-team-alpha',
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5',
                scope_user_path: '/team/alpha'
            }
        }
    ];
    module.workflowForm = module.defaultWorkflowForm();
    module.workflowForm.scope_provider = 'openai';
    module.workflowForm.scope_model = 'gpt-5';
    module.workflowForm.scope_user_path = 'team/alpha';

    const payload = {
        scope_provider: 'openai',
        scope_model: 'gpt-5',
        scope_user_path: '/team/alpha',
        name: 'Scoped workflow',
        workflow_payload: {
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

    assert.equal(module.validateWorkflowRequest(payload), '');
    assert.equal(module.workflowActiveScopeMatch().id, 'openai-gpt-5-team-alpha');
    assert.equal(module.workflowSubmitMode(), 'save');
});

test('validateWorkflowRequest rejects invalid scope_user_path segments', () => {
    const module = createWorkflowsModule();

    assert.equal(
        module.validateWorkflowRequest({
            scope_provider: '',
            scope_model: '',
            scope_user_path: '/team/../alpha',
            workflow_payload: {
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
        module.validateWorkflowRequest({
            scope_provider: '',
            scope_model: '',
            scope_user_path: '/team:alpha',
            workflow_payload: {
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

test('setWorkflowProvider clears model when provider changes', () => {
    const module = createWorkflowsModule();
    module.models = [
        { provider_type: 'openai', model: { id: 'gpt-5' } },
        { provider_type: 'anthropic', model: { id: 'claude-3-7' } }
    ];
    module.workflowForm = module.defaultWorkflowForm();
    module.workflowForm.scope_provider = 'openai';
    module.workflowForm.scope_model = 'gpt-5';

    module.setWorkflowProvider('anthropic');

    assert.equal(module.workflowForm.scope_provider, 'anthropic');
    assert.equal(module.workflowForm.scope_model, '');
});

test('validateWorkflowRequest rejects unregistered provider-model selections', () => {
    const module = createWorkflowsModule();
    module.models = [
        { provider_type: 'openai', model: { id: 'gpt-5' } }
    ];

    assert.equal(
        module.validateWorkflowRequest({
            scope_provider: 'anthropic',
            scope_model: '',
            workflow_payload: {
                schema_version: 1,
                features: { cache: true, audit: true, usage: true, guardrails: false },
                guardrails: []
            }
        }),
        'Choose a registered provider name.'
    );

    assert.equal(
        module.validateWorkflowRequest({
            scope_provider: 'openai',
            scope_model: 'gpt-4o-mini',
            workflow_payload: {
                schema_version: 1,
                features: { cache: true, audit: true, usage: true, guardrails: false },
                guardrails: []
            }
        }),
        'Choose a registered model for the selected provider name.'
    );
});

test('workflowDisplayName falls back to scope label or All models', () => {
    const module = createWorkflowsModule();

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

test('workflowGuardrailLabel only shows a sublabel when guardrail steps exist', () => {
    const module = createWorkflowsModule();

    assert.equal(
        module.workflowGuardrailLabel({
            workflow_payload: {
                guardrails: []
            }
        }),
        ''
    );

    assert.equal(
        module.workflowGuardrailLabel({
            workflow_payload: {
                guardrails: [{ ref: 'policy-system', step: 10 }]
            }
        }),
        '1 step'
    );

    assert.equal(
        module.workflowGuardrailLabel({
            workflow_payload: {
                guardrails: [
                    { ref: 'policy-system', step: 10 },
                    { ref: 'pii', step: 20 }
                ]
            }
        }),
        '2 steps'
    );
});

test('deactivateWorkflow requires confirmation before posting', async () => {
    let fetchCalled = false;
    const module = createWorkflowsModule({
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

    await module.deactivateWorkflow({
        id: 'workflow-1',
        name: 'Primary workflow',
        scope_type: 'provider'
    });

    assert.equal(fetchCalled, false);
    assert.equal(module.workflowDeactivatingID, '');
});

test('deactivateWorkflow ignores duplicate clicks while another deactivation is in flight', async () => {
    let confirmCalled = false;
    let fetchCalled = false;
    const module = createWorkflowsModule({
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
    module.workflowDeactivatingID = 'workflow-1';
    module.headers = () => ({});

    await module.deactivateWorkflow({
        id: 'workflow-1',
        name: 'Primary workflow',
        scope_type: 'provider'
    });

    assert.equal(confirmCalled, false);
    assert.equal(fetchCalled, false);
    assert.equal(module.workflowDeactivatingID, 'workflow-1');
});

test('fetchWorkflows aborts hung requests and clears loading state', async () => {
    let timeoutCleared = false;
    class AbortControllerStub {
        constructor() {
            this.signal = { aborted: false };
        }

        abort() {
            this.signal.aborted = true;
        }
    }

    const module = createWorkflowsModule({
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

    await module.fetchWorkflows();

    assert.equal(JSON.stringify(module.workflows), JSON.stringify([]));
    assert.equal(module.workflowError, 'Loading workflows timed out.');
    assert.equal(module.workflowsLoading, false);
    assert.equal(timeoutCleared, true);
});

test('submitWorkflowForm ignores duplicate submissions while a request is already in flight', async () => {
    let fetchCalled = false;
    const module = createWorkflowsModule({
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
    module.workflowSubmitting = true;
    module.workflowForm = {
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
    module.closeWorkflowForm = () => {};
    module.fetchWorkflowsPage = async () => {};

    await module.submitWorkflowForm();

    assert.equal(fetchCalled, false);
    assert.equal(module.workflowSubmitting, true);
});

test('submitWorkflowForm logs non-auth HTTP failures before surfacing the UI error', async () => {
    const errors = [];
    const module = createWorkflowsModule({
        console: {
            error(...args) {
                errors.push(args.join(' '));
            }
        },
        fetch() {
            return Promise.resolve({
                ok: false,
                status: 500,
                statusText: 'Internal Server Error',
                json: async() => ({
                    error: {
                        message: 'guardrail catalog refresh failed'
                    }
                })
            });
        }
    });
    module.models = [
        { provider_type: 'openai', model: { id: 'gpt-5' } }
    ];
    module.workflowForm = {
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
    module.closeWorkflowForm = () => {};
    module.fetchWorkflowsPage = async () => {};

    await module.submitWorkflowForm();

    assert.equal(module.workflowFormError, 'guardrail catalog refresh failed');
    assert.equal(errors.length, 1);
    assert.match(errors[0], /Failed to create workflow: 500 Internal Server Error guardrail catalog refresh failed/);
});

test('workflowRuntimeFromEntry derives cache hit state from cache_type without relying on headers', () => {
    const module = createWorkflowsModule();

    assert.equal(
        JSON.stringify(module.workflowRuntimeFromEntry({ cache_type: 'semantic', provider: 'openai', model: 'gpt-5' })),
        JSON.stringify({
            cacheHit: true,
            cacheType: 'semantic',
            failoverTarget: null,
            provider: 'openai',
            model: 'gpt-5',
            statusCode: null,
            responseSuccess: false,
            aiSuccess: false,
            authError: false,
            authMethod: null,
            budgetExceeded: false
        })
    );

    assert.equal(
        JSON.stringify(module.workflowRuntimeFromEntry({ cache_type: 'exact' })),
        JSON.stringify({
            cacheHit: true,
            cacheType: 'exact',
            failoverTarget: null,
            provider: null,
            model: null,
            statusCode: null,
            responseSuccess: false,
            aiSuccess: false,
            authError: false,
            authMethod: null,
            budgetExceeded: false
        })
    );

    assert.equal(
        JSON.stringify(module.workflowRuntimeFromEntry({})),
        JSON.stringify({
            cacheHit: false,
            cacheType: null,
            failoverTarget: null,
            provider: null,
            model: null,
            statusCode: null,
            responseSuccess: false,
            aiSuccess: false,
            authError: false,
            authMethod: null,
            budgetExceeded: false
        })
    );
});

test('audit runtime uses explicit cache-hit labels and highlights the uncached 200 path', () => {
    const module = createWorkflowsModule();

    const semanticHit = module.workflowRuntimeFromEntry({
        cache_type: 'semantic',
        status_code: 200
    });
    assert.equal(module.workflowCacheNodeClass(semanticHit), 'workflow-node-success');
    assert.equal(module.workflowCacheConnClass(semanticHit), 'workflow-conn-hit');
    assert.equal(module.workflowCacheStatusLabel(semanticHit), 'Hit (Semantic)');
    assert.equal(module.workflowFailoverNodeClass(semanticHit), 'workflow-node-skipped');
    assert.equal(module.workflowFailoverConnClass(semanticHit), 'workflow-conn-dim');
    assert.equal(module.workflowAiConnClass(semanticHit), 'workflow-conn-dim');
    assert.equal(module.workflowAiNodeClass(semanticHit), 'workflow-node-skipped');
    assert.equal(module.workflowResponseConnClass(semanticHit), 'workflow-conn-dim');
    assert.equal(module.workflowResponseNodeClass(semanticHit), 'workflow-node-success');
    assert.equal(module.workflowResponseNodeSublabel(semanticHit), '200');

    const uncachedSuccess = module.workflowRuntimeFromEntry({
        provider: 'openai',
        model: 'gpt-5',
        status_code: 200
    });
    assert.equal(uncachedSuccess.cacheHit, false);
    assert.equal(module.workflowCacheNodeClass(uncachedSuccess), '');
    assert.equal(module.workflowCacheStatusLabel(uncachedSuccess), null);
    assert.equal(module.workflowAiConnClass(uncachedSuccess), '');
    assert.equal(module.workflowAiNodeClass(uncachedSuccess), 'workflow-node-success');
    assert.equal(module.workflowResponseConnClass(uncachedSuccess), '');
    assert.equal(module.workflowResponseNodeClass(uncachedSuccess), 'workflow-node-success');
    assert.equal(module.workflowResponseNodeSublabel(uncachedSuccess), '200');
});

test('response runtime maps 3xx and 4xx statuses to neutral and warning chart colors', () => {
    const module = createWorkflowsModule();

    const redirect = module.workflowRuntimeFromEntry({
        provider: 'openai',
        model: 'gpt-5',
        status_code: 304
    });
    assert.equal(module.workflowResponseNodeClass(redirect), 'workflow-node-neutral');
    assert.equal(module.workflowResponseNodeSublabel(redirect), '304');

    const clientError = module.workflowRuntimeFromEntry({
        provider: 'openai',
        model: 'gpt-5',
        status_code: 429
    });
    assert.equal(module.workflowResponseNodeClass(clientError), 'workflow-node-warning');
    assert.equal(module.workflowResponseNodeSublabel(clientError), '429');
});

test('response runtime maps 5xx statuses to the error chart color', () => {
    const module = createWorkflowsModule();

    const serverError = module.workflowRuntimeFromEntry({
        provider: 'openai',
        model: 'gpt-5',
        status_code: 503
    });
    assert.equal(module.workflowResponseNodeClass(serverError), 'workflow-node-error');
    assert.equal(module.workflowResponseNodeSublabel(serverError), '503');
});

test('workflowRuntimeFromEntry treats any uncached 2xx status as a successful AI and response path', () => {
    const module = createWorkflowsModule();

    assert.equal(
        JSON.stringify(module.workflowRuntimeFromEntry({
            provider: 'openai',
            model: 'gpt-5',
            status_code: 204
        })),
        JSON.stringify({
            cacheHit: false,
            cacheType: null,
            failoverTarget: null,
            provider: 'openai',
            model: 'gpt-5',
            statusCode: 204,
            responseSuccess: true,
            aiSuccess: true,
            authError: false,
            authMethod: null,
            budgetExceeded: false
        })
    );
});

test('auth runtime highlights auth node state from audit entries', () => {
    const module = createWorkflowsModule();

    const failedAuth = module.workflowRuntimeFromEntry({
        auth_method: 'api_key',
        error_type: 'authentication_error'
    });
    assert.equal(module.workflowAuthNodeClass(failedAuth), 'workflow-node-error');
    assert.equal(module.workflowAuthNodeSublabel(failedAuth), 'api_key');

    const masterKeyAuth = module.workflowRuntimeFromEntry({
        auth_method: 'master_key',
        status_code: 200
    });
    assert.equal(module.workflowAuthNodeClass(masterKeyAuth), 'workflow-node-success');
    assert.equal(module.workflowAuthNodeSublabel(masterKeyAuth), 'master_key');
});

test('budget runtime highlights audit budget node success and exceeded states', () => {
    const module = createWorkflowsModule();

    const successfulBudget = module.workflowRuntimeFromEntry({
        status_code: 200,
        data: {
            workflow_features: {
                budget: true
            }
        }
    });
    assert.equal(module.workflowBudgetNodeClass(true, successfulBudget, true), 'workflow-node-success');
    assert.equal(module.workflowBudgetStatusLabel(successfulBudget), null);

    const exceededBudget = module.workflowRuntimeFromEntry({
        status_code: 429,
        data: {
            error_code: 'budget_exceeded',
            workflow_features: {
                budget: true
            }
        }
    });
    assert.equal(exceededBudget.budgetExceeded, true);
    assert.equal(module.workflowBudgetNodeClass(true, exceededBudget, true), 'workflow-node-error');
    assert.equal(module.workflowBudgetStatusLabel(exceededBudget), 'Exceeded');

    const chart = module.workflowAuditChart({
        workflow_version_id: 'missing-budget-workflow',
        status_code: 429,
        data: {
            error_code: 'budget_exceeded'
        }
    });
    assert.equal(chart.showBudget, true);
    assert.equal(chart.budgetNodeClass, 'workflow-node-error');
    assert.equal(chart.budgetStatusLabel, 'Exceeded');
});

test('auditEntryWorkflow prefers an exact historical workflow version cache over active workflows', () => {
    const module = createWorkflowsModule();
    module.workflows = [
        {
            id: 'active-current',
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            },
            workflow_payload: {
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
    module.workflowVersionsByID = {
        'historical-v1': {
            id: 'historical-v1',
            active: false,
            scope: {
                scope_provider: 'openai',
                scope_model: 'gpt-5'
            },
            workflow_payload: {
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

    const resolved = module.auditEntryWorkflow({
        workflow_version_id: 'historical-v1'
    });

    assert.equal(resolved.id, 'historical-v1');
    assert.equal(module.workflowHasUsage(resolved), true);
    assert.equal(module.workflowHasAudit(resolved), true);
    assert.equal(module.workflowHasGuardrails(resolved), true);
});

test('fetchWorkflowVersion loads a historical workflow version once and caches misses', async () => {
    const fetchCalls = [];
    const module = createWorkflowsModule({
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
                        workflow_payload: {
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

    const loaded = await module.fetchWorkflowVersion('historical-v2');
    const repeated = await module.fetchWorkflowVersion('historical-v2');
    const missing = await module.fetchWorkflowVersion('missing-workflow');
    const missingAgain = await module.fetchWorkflowVersion('missing-workflow');

    assert.equal(loaded.id, 'historical-v2');
    assert.equal(repeated.id, 'historical-v2');
    assert.equal(missing, null);
    assert.equal(missingAgain, null);
    assert.deepEqual(fetchCalls, [
        '/admin/api/v1/workflows/historical-v2',
        '/admin/api/v1/workflows/missing-workflow'
    ]);
});

test('fetchWorkflowVersion aborts hung requests, clears the timeout, and cleans up in-flight state', async () => {
    let timeoutCleared = false;
    class AbortControllerStub {
        constructor() {
            this.signal = { aborted: false };
        }

        abort() {
            this.signal.aborted = true;
        }
    }

    const module = createWorkflowsModule({
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

    const result = await module.fetchWorkflowVersion('historical-timeout');

    assert.equal(result, null);
    assert.equal(timeoutCleared, true);
    assert.equal(
        Object.prototype.hasOwnProperty.call(module.workflowVersionRequests || {}, 'historical-timeout'),
        false
    );
});
