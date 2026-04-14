const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadAliasesModuleFactory(overrides = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'aliases.js'), 'utf8');
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
    return context.window.dashboardAliasesModule;
}

function createAliasesModule(overrides) {
    const factory = loadAliasesModuleFactory(overrides);
    return factory();
}

test('filteredDisplayModels returns stable rows when filter is empty', () => {
    const module = createAliasesModule();
    module.models = [
        {
            provider_type: 'openai',
            model: {
                id: 'davinci-002',
                object: 'model',
                owned_by: 'openai',
                metadata: {
                    modes: ['chat'],
                    categories: ['text_generation']
                }
            }
        }
    ];
    module.aliases = [];
    module.aliasesAvailable = true;
    module.modelFilter = '';
    module.syncDisplayModels();

    const first = module.filteredDisplayModels;
    const second = module.filteredDisplayModels;

    assert.equal(first.length, 1);
    assert.strictEqual(second, first);
    assert.strictEqual(second[0], first[0]);
    assert.equal(first[0].key, 'model:openai/davinci-002');
});

test('qualifiedModelName prefers selector when available', () => {
    const module = createAliasesModule();
    const model = {
        selector: 'openrouter/openai/gpt-3.5-turbo',
        provider_name: 'openrouter',
        provider_type: 'openrouter',
        model: {
            id: 'openai/gpt-3.5-turbo',
            object: 'model',
            owned_by: 'openai'
        }
    };

    assert.equal(module.qualifiedModelName(model), 'openrouter/openai/gpt-3.5-turbo');
});

test('filteredDisplayModelGroups groups rows by provider_name and applies provider-wide overrides', () => {
    const module = createAliasesModule();
    module.models = [
        {
            provider_name: 'openai-backup',
            provider_type: 'openai',
            access: {
                selector: 'openai-backup/gpt-4.1-mini',
                default_enabled: true,
                effective_enabled: true
            },
            model: {
                id: 'gpt-4.1-mini',
                object: 'model',
                owned_by: 'openai',
                metadata: {
                    modes: ['chat'],
                    categories: ['text_generation']
                }
            }
        },
        {
            provider_name: 'openai-primary',
            provider_type: 'openai',
            access: {
                selector: 'openai-primary/gpt-4.1',
                default_enabled: false,
                effective_enabled: true
            },
            model: {
                id: 'gpt-4.1',
                object: 'model',
                owned_by: 'openai',
                metadata: {
                    modes: ['chat'],
                    categories: ['text_generation']
                }
            }
        }
    ];
    module.modelOverrideViews = [
        {
            selector: 'openai-backup/',
            provider_name: 'openai-backup',
            user_paths: ['/non-existing']
        },
        {
            selector: 'openai-primary/',
            provider_name: 'openai-primary',
            user_paths: ['/team/alpha']
        }
    ];
    module.aliases = [];
    module.aliasesAvailable = true;
    module.modelFilter = '';
    module.syncDisplayModels();

    const groups = module.filteredDisplayModelGroups;
    const primary = groups.find((group) => group.provider_name === 'openai-primary');
    const backup = groups.find((group) => group.provider_name === 'openai-backup');

    assert.equal(groups.length, 2);
    assert.equal(primary.type_label, 'openai');
    assert.equal(primary.access.selector, 'openai-primary/');
    assert.equal(primary.access.default_enabled, false);
    assert.equal(primary.access.effective_enabled, true);
    assert.deepEqual(Array.from(primary.access.user_paths), ['/team/alpha']);
    assert.equal(primary.item_count_label, '1 model');
    assert.equal(backup.access.selector, 'openai-backup/');
    assert.equal(backup.access.effective_enabled, true);
    assert.deepEqual(Array.from(backup.access.user_paths), ['/non-existing']);
});

test('filteredDisplayModelGroups lets provider-wide overrides replace global paths', () => {
    const module = createAliasesModule();
    module.models = [
        {
            provider_name: 'anthropic-primary',
            provider_type: 'anthropic',
            access: {
                selector: 'anthropic-primary/claude-3-7-sonnet',
                default_enabled: false,
                effective_enabled: false
            },
            model: {
                id: 'claude-3-7-sonnet',
                object: 'model',
                owned_by: 'anthropic'
            }
        },
        {
            provider_name: 'openai-primary',
            provider_type: 'openai',
            access: {
                selector: 'openai-primary/gpt-4.1',
                default_enabled: false,
                effective_enabled: false
            },
            model: {
                id: 'gpt-4.1',
                object: 'model',
                owned_by: 'openai'
            }
        }
    ];
    module.modelOverrideViews = [
        {
            selector: '/',
            user_paths: ['/team/alpha']
        },
        {
            selector: 'openai-primary/',
            provider_name: 'openai-primary',
            user_paths: ['/team/openai']
        }
    ];
    module.aliases = [];
    module.aliasesAvailable = true;
    module.modelFilter = '';
    module.syncDisplayModels();

    const groups = module.filteredDisplayModelGroups;
    const anthropic = groups.find((group) => group.provider_name === 'anthropic-primary');
    const openai = groups.find((group) => group.provider_name === 'openai-primary');

    assert.equal(anthropic.access.effective_enabled, true);
    assert.deepEqual(Array.from(anthropic.access.user_paths), ['/team/alpha']);
    assert.equal(openai.access.effective_enabled, true);
    assert.deepEqual(Array.from(openai.access.user_paths), ['/team/openai']);
});

test('override button helpers mark configured selectors', () => {
    const module = createAliasesModule();
    module.modelOverrideViews = [
        {
            selector: '/'
        }
    ];

    assert.equal(module.hasGlobalModelOverride(), true);
    assert.equal(module.hasAccessOverride({ override: { selector: 'openai/' } }), true);
    assert.equal(module.hasAccessOverride({}), false);
    assert.equal(module.modelOverrideEditButtonClass(true), 'table-action-btn-active');
    assert.equal(module.modelOverrideEditButtonClass(false), '');
    assert.equal(
        module.modelOverrideEditButtonLabel('global model access', true),
        'Edit global model access (override exists)'
    );
    assert.equal(module.modelAccessStateText({ effective_enabled: true }), 'Enabled');
    assert.equal(module.modelAccessStateClass({ effective_enabled: true }), 'is-enabled');

    module.modelOverridesAvailable = false;

    assert.equal(module.modelAccessStateText({ effective_enabled: true }), '');
    assert.equal(module.modelAccessStateClass({ effective_enabled: true }), '');
});

test('openProviderOverrideEdit opens the access editor with provider_name slash selector', () => {
    const module = createAliasesModule();

    module.openProviderOverrideEdit({
        display_name: 'openai-primary',
        provider_name: 'openai-primary',
        provider_type: 'openai',
        access: {
            selector: 'openai-primary/',
            default_enabled: true,
            effective_enabled: true,
            user_paths: [],
            override: null
        }
    });

    assert.equal(module.modelOverrideFormOpen, true);
    assert.equal(module.modelOverrideForm.selector, 'openai-primary/');
    assert.equal(module.modelOverrideFormDisplayName, 'All models in openai-primary');
});

test('openModelOverrideEdit scrolls to the access editor after opening', () => {
    const module = createAliasesModule();
    const calls = [];
    let nextTickCallback = null;
    module.$refs = {
        modelOverrideEditor: {
            scrollIntoView(options) {
                calls.push(options);
            }
        }
    };
    module.$nextTick = (callback) => {
        nextTickCallback = callback;
    };

    module.openModelOverrideEdit({
        display_name: 'openai/gpt-4o',
        is_alias: false,
        access: {
            selector: 'openai/gpt-4o',
            default_enabled: true,
            effective_enabled: true,
            override: null
        },
        model: {
            id: 'gpt-4o'
        }
    });

    assert.equal(module.modelOverrideFormOpen, true);
    assert.deepEqual(calls, []);

    nextTickCallback();

    assert.deepEqual(JSON.parse(JSON.stringify(calls)), [
        { behavior: 'smooth', block: 'start' }
    ]);
});

test('openGlobalModelOverrideEdit opens the access editor with slash selector', () => {
    const module = createAliasesModule();
    module.models = [
        {
            access: {
                default_enabled: false
            }
        }
    ];
    module.modelOverrideViews = [
        {
            selector: '/',
            user_paths: ['/team/alpha']
        }
    ];

    module.openGlobalModelOverrideEdit();

    assert.equal(module.modelOverrideFormOpen, true);
    assert.equal(module.modelOverrideForm.selector, '/');
    assert.equal(module.modelOverrideFormDisplayName, 'All providers and models');
    assert.equal(module.modelOverrideFormDefaultEnabled, false);
    assert.equal(module.modelOverrideFormEffectiveEnabled, true);
    assert.equal(module.modelOverrideForm.user_paths, '/team/alpha');
});

test('alias write paths use generation-aware request handling for stale auth responses', async() => {
    const scenarios = [
        {
            name: 'toggleAliasEnabled',
            run(module) {
                return module.toggleAliasEnabled({
                    name: 'short',
                    target_model: 'openai/gpt-4o',
                    description: '',
                    enabled: true
                });
            },
            errorKey: 'aliasError'
        },
        {
            name: 'submitAliasForm',
            setup(module) {
                module.aliasForm = {
                    name: 'short',
                    target_model: 'openai/gpt-4o',
                    description: '',
                    enabled: true
                };
                module.aliasFormOriginalName = '';
            },
            run(module) {
                return module.submitAliasForm();
            },
            errorKey: 'aliasFormError'
        },
        {
            name: 'submitModelOverrideForm',
            setup(module) {
                module.modelOverrideForm = {
                    selector: 'openai/gpt-4o',
                    user_paths: '/team/alpha'
                };
            },
            run(module) {
                return module.submitModelOverrideForm();
            },
            errorKey: 'modelOverrideError'
        },
        {
            name: 'deleteModelOverride',
            setup(module) {
                module.modelOverrideForm = {
                    selector: 'openai/gpt-4o',
                    user_paths: '/team/alpha'
                };
                module.modelOverrideFormHasExistingOverride = true;
            },
            run(module) {
                return module.deleteModelOverride();
            },
            errorKey: 'modelOverrideError'
        }
    ];

    for (const scenario of scenarios) {
        const fetchCalls = [];
        const handledCalls = [];
        const module = createAliasesModule({
            context: {
                fetch: async(url, request) => {
                    fetchCalls.push({ url, request });
                    return {
                        ok: false,
                        status: 401,
                        statusText: 'Unauthorized',
                        json: async() => ({})
                    };
                }
            },
            window: {
                confirm: () => true
            }
        });

        Object.assign(module, {
            aliases: [],
            models: [],
            modelOverrideViews: [],
            aliasesAvailable: true,
            modelOverridesAvailable: true,
            needsAuth: false,
            authError: false,
            requestOptions(options) {
                return {
                    ...(options || {}),
                    headers: { Authorization: 'Bearer current-token' },
                    authGeneration: 3
                };
            },
            headers() {
                return { Authorization: 'Bearer current-token' };
            },
            handleFetchResponse(res, label, request) {
                handledCalls.push({ res, label, request });
                return 'STALE_AUTH';
            },
            isStaleAuthFetchResult(result) {
                return result === 'STALE_AUTH';
            },
            fetchAliases() {
                throw new Error('fetchAliases should not run for stale auth in ' + scenario.name);
            },
            fetchModels() {
                throw new Error('fetchModels should not run for stale auth in ' + scenario.name);
            },
            fetchModelOverrides() {
                throw new Error('fetchModelOverrides should not run for stale auth in ' + scenario.name);
            }
        });
        if (scenario.setup) {
            scenario.setup(module);
        }

        await scenario.run(module);

        assert.equal(fetchCalls.length, 1, scenario.name);
        assert.equal(handledCalls.length, 1, scenario.name);
        assert.strictEqual(handledCalls[0].request, fetchCalls[0].request, scenario.name);
        assert.equal(fetchCalls[0].request.authGeneration, 3, scenario.name);
        assert.equal(module.needsAuth, false, scenario.name);
        assert.equal(module.authError, false, scenario.name);
        assert.equal(module[scenario.errorKey], '', scenario.name);
    }
});
