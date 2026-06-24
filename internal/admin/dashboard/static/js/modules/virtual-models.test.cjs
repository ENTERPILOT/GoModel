const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadAliasesModuleFactory(overrides = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'virtual-models.js'), 'utf8');
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
    return context.window.dashboardVirtualModelsModule;
}

function createAliasesModule(overrides) {
    const factory = loadAliasesModuleFactory(overrides);
    return factory();
}

function stubRequests(module, requests, response) {
    Object.assign(module, {
        requestOptions(options) {
            return { ...(options || {}), headers: {} };
        },
        headers() {
            return {};
        },
        handleFetchResponse() {
            return true;
        },
        isStaleAuthFetchResult() {
            return false;
        }
    });
    return response;
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
    module.virtualModelsAvailable = true;
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

test('fetchVirtualModels parses redirect and policy Views into aliases and overrides', async() => {
    const views = [
        {
            source: 'smart',
            kind: 'redirect',
            targets: [{ provider: 'openai', model: 'gpt-4o' }],
            description: 'Primary chat alias',
            enabled: true,
            resolved_model: 'openai/gpt-4o',
            provider_type: 'openai',
            valid: true,
            user_paths: ['/team/alpha']
        },
        {
            source: 'openai/gpt-4o',
            kind: 'policy',
            provider_name: 'openai',
            model: 'gpt-4o',
            user_paths: ['/team/alpha'],
            enabled: true,
            scope_kind: 'model'
        }
    ];
    const requests = [];
    const module = createAliasesModule({
        context: {
            fetch: async(url, request) => {
                requests.push({ url, request });
                return { ok: true, status: 200, json: async() => views };
            }
        }
    });
    stubRequests(module);
    module.models = [];

    await module.fetchVirtualModels();

    assert.equal(requests.length, 1);
    assert.equal(requests[0].url, '/admin/virtual-models');
    assert.equal(module.aliases.length, 1);
    assert.deepEqual(JSON.parse(JSON.stringify(module.aliases[0])), {
        name: 'smart',
        target_provider: 'openai',
        target_model: 'gpt-4o',
        description: 'Primary chat alias',
        enabled: true,
        valid: true,
        resolved_model: 'openai/gpt-4o',
        provider_type: 'openai',
        user_paths: ['/team/alpha']
    });
    assert.equal(module.modelOverrideViews.length, 1);
    assert.equal(module.modelOverrideViews[0].selector, 'openai/gpt-4o');
    assert.deepEqual(module.modelOverrideViews[0].user_paths, ['/team/alpha']);
    assert.equal(module.virtualModelsAvailable, true);
    assert.equal(module.virtualModelsAvailable, true);
});

test('fetchVirtualModels marks the feature unavailable on 503', async() => {
    const module = createAliasesModule({
        context: {
            fetch: async() => ({ ok: false, status: 503, json: async() => ({}) })
        }
    });
    stubRequests(module);
    module.models = [];

    await module.fetchVirtualModels();

    assert.equal(module.virtualModelsAvailable, false);
    assert.equal(module.virtualModelsAvailable, false);
    assert.equal(module.aliases.length, 0);
    assert.equal(module.modelOverrideViews.length, 0);
});

test('toggleRowEnabled disabling a real model sends a policy PUT with enabled false', async() => {
    const requests = [];
    const fetched = [];
    const module = createAliasesModule({
        context: {
            fetch: async(url, request) => {
                requests.push({ url, request });
                return { ok: true, status: 200, json: async() => ({}) };
            }
        }
    });
    stubRequests(module);
    module.models = [];
    module.modelOverrideViews = [];
    module.fetchModels = async() => { fetched.push('models'); };
    module.fetchVirtualModels = async() => { fetched.push('virtual'); };

    const row = {
        key: 'model:openai/gpt-4o',
        is_alias: false,
        display_name: 'openai/gpt-4o',
        access: {
            selector: 'openai/gpt-4o',
            default_enabled: true,
            effective_enabled: true
        }
    };

    await module.toggleRowEnabled(row);

    assert.equal(requests.length, 1);
    assert.equal(requests[0].url, '/admin/virtual-models');
    assert.equal(requests[0].request.method, 'PUT');
    assert.deepEqual(JSON.parse(requests[0].request.body), {
        source: 'openai/gpt-4o',
        enabled: false,
        user_paths: []
    });
    assert.deepEqual(fetched, ['models', 'virtual']);
});

test('toggleRowEnabled enabling a real model with an existing path-less policy sends DELETE', async() => {
    const requests = [];
    const module = createAliasesModule({
        context: {
            fetch: async(url, request) => {
                requests.push({ url, request });
                return { ok: true, status: 200, json: async() => ({}) };
            }
        }
    });
    stubRequests(module);
    module.models = [];
    module.modelOverrideViews = [
        { selector: 'openai/gpt-4o', user_paths: [], enabled: false }
    ];
    module.fetchModels = async() => {};
    module.fetchVirtualModels = async() => {};

    const row = {
        key: 'model:openai/gpt-4o',
        is_alias: false,
        display_name: 'openai/gpt-4o',
        access: {
            selector: 'openai/gpt-4o',
            default_enabled: true,
            effective_enabled: false
        }
    };

    await module.toggleRowEnabled(row);

    assert.equal(requests.length, 1);
    assert.equal(requests[0].request.method, 'DELETE');
    assert.deepEqual(JSON.parse(requests[0].request.body), { source: 'openai/gpt-4o' });
});

test('toggleRowEnabled enabling a real model with restricted paths sends PUT enabled true', async() => {
    const requests = [];
    const module = createAliasesModule({
        context: {
            fetch: async(url, request) => {
                requests.push({ url, request });
                return { ok: true, status: 200, json: async() => ({}) };
            }
        }
    });
    stubRequests(module);
    module.models = [];
    module.modelOverrideViews = [
        { selector: 'openai/gpt-4o', user_paths: ['/team/alpha'], enabled: false }
    ];
    module.fetchModels = async() => {};
    module.fetchVirtualModels = async() => {};

    const row = {
        key: 'model:openai/gpt-4o',
        is_alias: false,
        display_name: 'openai/gpt-4o',
        access: {
            selector: 'openai/gpt-4o',
            default_enabled: true,
            effective_enabled: false
        }
    };

    await module.toggleRowEnabled(row);

    assert.equal(requests.length, 1);
    assert.equal(requests[0].request.method, 'PUT');
    assert.deepEqual(JSON.parse(requests[0].request.body), {
        source: 'openai/gpt-4o',
        enabled: true,
        user_paths: ['/team/alpha']
    });
});

test('toggleRowEnabled flips an alias enabled flag via PUT preserving target', async() => {
    const requests = [];
    const module = createAliasesModule({
        context: {
            fetch: async(url, request) => {
                requests.push({ url, request });
                return { ok: true, status: 200, json: async() => ({}) };
            }
        }
    });
    stubRequests(module);
    module.fetchVirtualModels = async() => {};

    const row = {
        key: 'alias:smart',
        is_alias: true,
        display_name: 'smart',
        alias: {
            name: 'smart',
            target_provider: 'openai',
            target_model: 'gpt-4o',
            description: 'chat',
            enabled: true,
            user_paths: ['/team/alpha']
        }
    };

    await module.toggleRowEnabled(row);

    assert.equal(requests.length, 1);
    assert.equal(requests[0].url, '/admin/virtual-models');
    assert.equal(requests[0].request.method, 'PUT');
    assert.deepEqual(JSON.parse(requests[0].request.body), {
        source: 'smart',
        target_model: 'openai/gpt-4o',
        description: 'chat',
        user_paths: ['/team/alpha'],
        enabled: false
    });
});

test('openVirtualModelCreate keeps Source editable and seeds the target from a row', () => {
    const module = createAliasesModule();
    module.openVirtualModelCreate({
        provider_name: 'openai',
        provider_type: 'openai',
        model: { id: 'gpt-4o' }
    });

    assert.equal(module.vmFormOpen, true);
    assert.equal(module.vmFormMode, 'create');
    assert.equal(module.vmFormSourceLocked, false);
    assert.equal(module.vmForm.source, '');
    assert.equal(module.vmForm.target_model, 'openai/gpt-4o');
});

test('openVirtualModelEditModel locks and prefills the Source with the selector', () => {
    const module = createAliasesModule();
    module.openVirtualModelEditModel({
        display_name: 'openai/gpt-4o',
        is_alias: false,
        access: {
            selector: 'openai/gpt-4o',
            default_enabled: true,
            effective_enabled: false,
            override: { selector: 'openai/gpt-4o', user_paths: ['/team/alpha'], description: 'team only' }
        },
        model: { id: 'gpt-4o' }
    });

    assert.equal(module.vmFormOpen, true);
    assert.equal(module.vmFormMode, 'edit');
    assert.equal(module.vmFormSourceLocked, true);
    assert.equal(module.vmFormHasExisting, true);
    assert.equal(module.vmForm.source, 'openai/gpt-4o');
    assert.equal(module.vmForm.target_model, '');
    assert.equal(module.vmForm.user_paths, '/team/alpha');
    assert.equal(module.vmForm.description, 'team only');
});

test('openVirtualModelEditAlias locks and prefills the Source from the alias', () => {
    const module = createAliasesModule();
    module.openVirtualModelEditAlias({
        name: 'smart',
        target_provider: 'openai',
        target_model: 'gpt-4o',
        description: 'Primary chat alias',
        enabled: true,
        user_paths: ['/team/alpha']
    });

    assert.equal(module.vmFormOpen, true);
    assert.equal(module.vmFormMode, 'edit');
    assert.equal(module.vmFormSourceLocked, true);
    assert.equal(module.vmForm.source, 'smart');
    assert.equal(module.vmForm.target_model, 'openai/gpt-4o');
    assert.equal(module.vmForm.description, 'Primary chat alias');
    assert.equal(module.vmForm.user_paths, '/team/alpha');
    assert.equal(module.vmForm.enabled, true);
});

test('submitVirtualModelForm sends a redirect payload when target_model is filled', async() => {
    const requests = [];
    const module = createAliasesModule({
        context: {
            fetch: async(url, request) => {
                requests.push({ url, request });
                return { ok: true, status: 200, json: async() => ({}) };
            }
        },
        window: { confirm: () => true }
    });
    stubRequests(module);
    module.aliases = [];
    module.models = [];
    module.fetchModels = async() => {};
    module.fetchVirtualModels = async() => {};

    module.vmFormMode = 'create';
    module.vmForm = {
        source: 'smart',
        target_model: 'openai/gpt-4o',
        user_paths: '/team/alpha\n/team/beta',
        description: 'chat',
        enabled: true
    };

    await module.submitVirtualModelForm();

    assert.equal(requests.length, 1);
    assert.equal(requests[0].url, '/admin/virtual-models');
    assert.equal(requests[0].request.method, 'PUT');
    assert.deepEqual(JSON.parse(requests[0].request.body), {
        source: 'smart',
        user_paths: ['/team/alpha', '/team/beta'],
        description: 'chat',
        enabled: true,
        target_model: 'openai/gpt-4o'
    });
});

test('submitVirtualModelForm names existing aliases as virtual models in overwrite confirmation', async() => {
    const requests = [];
    const confirms = [];
    const module = createAliasesModule({
        context: {
            fetch: async(url, request) => {
                requests.push({ url, request });
                return { ok: true, status: 200, json: async() => ({}) };
            }
        },
        window: {
            confirm(message) {
                confirms.push(message);
                return false;
            }
        }
    });
    stubRequests(module);
    module.aliases = [{ name: 'smart', enabled: true, valid: true }];
    module.models = [];
    module.vmFormMode = 'create';
    module.vmForm = {
        source: 'smart',
        target_model: 'openai/gpt-4o',
        user_paths: '',
        description: '',
        enabled: true
    };

    await module.submitVirtualModelForm();

    assert.deepEqual(confirms, [
        'A virtual model named "smart" already exists. Saving will update that virtual model. Continue?'
    ]);
    assert.equal(requests.length, 0);
    assert.equal(module.vmFormError, 'Choose a different source or edit the existing virtual model.');
});

test('submitVirtualModelForm sends a policy payload when target_model is empty', async() => {
    const requests = [];
    const module = createAliasesModule({
        context: {
            fetch: async(url, request) => {
                requests.push({ url, request });
                return { ok: true, status: 200, json: async() => ({}) };
            }
        }
    });
    stubRequests(module);
    module.aliases = [];
    module.models = [];
    module.fetchModels = async() => {};
    module.fetchVirtualModels = async() => {};

    module.vmFormMode = 'edit';
    module.vmForm = {
        source: 'openai/gpt-4o',
        target_model: '',
        user_paths: '/team/alpha',
        description: '',
        enabled: false
    };

    await module.submitVirtualModelForm();

    assert.equal(requests.length, 1);
    const body = JSON.parse(requests[0].request.body);
    assert.equal(body.source, 'openai/gpt-4o');
    assert.equal(body.enabled, false);
    assert.deepEqual(body.user_paths, ['/team/alpha']);
    assert.equal(Object.prototype.hasOwnProperty.call(body, 'target_model'), false);
});

test('deleteVirtualModel removes the editor source', async() => {
    const requests = [];
    const module = createAliasesModule({
        context: {
            fetch: async(url, request) => {
                requests.push({ url, request });
                return { ok: true, status: 200, json: async() => ({}) };
            }
        },
        window: { confirm: () => true }
    });
    stubRequests(module);
    module.models = [];
    module.fetchModels = async() => {};
    module.fetchVirtualModels = async() => {};
    module.vmForm = { source: 'openai/gpt-4o', target_model: '', user_paths: '', description: '', enabled: true };
    module.vmFormHasExisting = true;

    await module.deleteVirtualModel();

    assert.equal(requests.length, 1);
    assert.equal(requests[0].request.method, 'DELETE');
    assert.deepEqual(JSON.parse(requests[0].request.body), { source: 'openai/gpt-4o' });
});

test('displayRowClass renders real models carrying a virtual model as alias-like', () => {
    const module = createAliasesModule();

    const overrideRow = {
        is_alias: false,
        has_virtual_model: true,
        access: { effective_enabled: true }
    };
    assert.equal(module.displayRowClass(overrideRow), 'alias-row is-valid');
    assert.equal(module.rowVirtualBadge(overrideRow), 'Override');

    const redirectRow = {
        is_alias: false,
        has_virtual_model: true,
        masking_alias: { name: 'openai/gpt-4o' },
        access: { effective_enabled: true }
    };
    assert.equal(module.displayRowClass(redirectRow), 'alias-row is-valid masked-model-row');
    assert.equal(module.rowVirtualBadge(redirectRow), 'Redirect');
    assert.equal(module.rowRedirectCanRemove(redirectRow), true);

    const plainRow = { is_alias: false, has_virtual_model: false, access: { effective_enabled: true } };
    assert.equal(module.displayRowClass(plainRow), '');
    assert.equal(module.rowVirtualBadge(plainRow), '');

    const aliasRow = { is_alias: true, alias: { enabled: true, valid: true } };
    assert.equal(module.displayRowClass(aliasRow), 'alias-row is-valid');
    assert.equal(module.rowVirtualBadge(aliasRow), '');
    assert.equal(module.aliasRowCanRemove({
        is_alias: true,
        source_model_exists: true,
        alias: { name: 'openai/gpt-4o' }
    }), true);
});

test('buildDisplayModels flags model rows with a policy override as carrying a virtual model', () => {
    const module = createAliasesModule();
    module.models = [
        {
            provider_name: 'openai',
            provider_type: 'openai',
            access: {
                selector: 'openai/gpt-4o',
                default_enabled: true,
                effective_enabled: true,
                override: { selector: 'openai/gpt-4o' }
            },
            model: { id: 'gpt-4o', object: 'model' }
        },
        {
            provider_name: 'openai',
            provider_type: 'openai',
            access: { selector: 'openai/gpt-4o-mini', default_enabled: true, effective_enabled: true },
            model: { id: 'gpt-4o-mini', object: 'model' }
        }
    ];
    module.aliases = [];
    module.virtualModelsAvailable = true;
    module.syncDisplayModels();

    const withOverride = module.displayModels.find((row) => row.display_name === 'openai/gpt-4o');
    const without = module.displayModels.find((row) => row.display_name === 'openai/gpt-4o-mini');

    assert.equal(withOverride.has_virtual_model, true);
    assert.equal(without.has_virtual_model, false);
});

test('buildDisplayModels combines source-backed redirects with the concrete model row', () => {
    const module = createAliasesModule();
    module.models = [
        {
            provider_name: 'openai',
            provider_type: 'openai',
            selector: 'openai/gpt-4o',
            model: { id: 'gpt-4o', object: 'model' }
        },
        {
            provider_name: 'openrouter',
            provider_type: 'openrouter',
            selector: 'openrouter/anthropic/claude-fable-5',
            model: { id: 'anthropic/claude-fable-5', object: 'model' }
        }
    ];
    module.aliases = [
        {
            name: 'smart',
            target_provider: 'openai',
            target_model: 'gpt-4o',
            enabled: true,
            valid: true
        },
        {
            name: 'gpt-4o',
            target_provider: 'openai',
            target_model: 'gpt-4o',
            enabled: true,
            valid: true
        },
        {
            name: 'anthropic/claude-fable-5',
            target_provider: 'openrouter',
            target_model: 'anthropic/claude-fable-5',
            resolved_model: 'openrouter/anthropic/claude-fable-5',
            enabled: true,
            valid: true
        }
    ];
    module.virtualModelsAvailable = true;
    module.syncDisplayModels();

    const aliasOnly = module.displayModels.find((row) => row.key === 'alias:smart');
    const modelBackedAliasRow = module.displayModels.find((row) => row.key === 'alias:gpt-4o');
    const modelBacked = module.displayModels.find((row) => row.key === 'model:openai/gpt-4o');
    const nestedTargetAlias = module.displayModels.find((row) => row.key === 'alias:anthropic/claude-fable-5');
    const nestedTargetModel = module.displayModels.find((row) => row.key === 'model:openrouter/anthropic/claude-fable-5');

    assert.equal(aliasOnly.kind_badge, 'Virtual Model');
    assert.equal(aliasOnly.source_model_exists, false);
    assert.equal(module.aliasRowCanRemove(aliasOnly), true);
    assert.equal(modelBackedAliasRow, undefined);
    assert.equal(modelBacked.masking_alias.name, 'gpt-4o');
    assert.equal(module.rowVirtualBadge(modelBacked), 'Redirect');
    assert.equal(module.rowRedirectCanRemove(modelBacked), true);
    assert.equal(nestedTargetAlias, undefined);
    assert.equal(nestedTargetModel.masking_alias.name, 'anthropic/claude-fable-5');
    assert.equal(module.rowVirtualBadge(nestedTargetModel), 'Redirect');
    assert.equal(module.rowRedirectCanRemove(nestedTargetModel), true);
});

test('removeAliasRow confirms and deletes an alias-only virtual model', async() => {
    const requests = [];
    const confirms = [];
    const fetched = [];
    const module = createAliasesModule({
        context: {
            fetch: async(url, request) => {
                requests.push({ url, request });
                return { ok: true, status: 200, json: async() => ({}) };
            }
        },
        window: {
            confirm(message) {
                confirms.push(message);
                return true;
            }
        }
    });
    stubRequests(module);
    module.models = [];
    module.fetchModels = async() => { fetched.push('models'); };
    module.fetchVirtualModels = async() => { fetched.push('virtual'); };
    module.syncDisplayModels = () => { fetched.push('sync'); };

    await module.removeAliasRow({
        key: 'alias:smart',
        is_alias: true,
        source_model_exists: false,
        alias: { name: 'smart' }
    });

    assert.deepEqual(confirms, ['Remove the virtual model alias "smart"?']);
    assert.equal(requests.length, 1);
    assert.equal(requests[0].url, '/admin/virtual-models');
    assert.equal(requests[0].request.method, 'DELETE');
    assert.deepEqual(JSON.parse(requests[0].request.body), { source: 'smart' });
    assert.deepEqual(fetched, ['models', 'virtual', 'sync']);
    assert.equal(module.aliasNotice, 'Virtual model removed.');
    assert.equal(module.rowDeletingKey, '');
});

test('removeRedirectRow confirms and deletes a source-backed redirect', async() => {
    const requests = [];
    const confirms = [];
    const fetched = [];
    const module = createAliasesModule({
        context: {
            fetch: async(url, request) => {
                requests.push({ url, request });
                return { ok: true, status: 200, json: async() => ({}) };
            }
        },
        window: {
            confirm(message) {
                confirms.push(message);
                return true;
            }
        }
    });
    stubRequests(module);
    module.fetchModels = async() => { fetched.push('models'); };
    module.fetchVirtualModels = async() => { fetched.push('virtual'); };
    module.syncDisplayModels = () => { fetched.push('sync'); };

    await module.removeRedirectRow({
        key: 'model:openai/gpt-4o',
        is_alias: false,
        display_name: 'openai/gpt-4o',
        masking_alias: { name: 'openai/gpt-4o' }
    });

    assert.deepEqual(confirms, ['Remove the redirect for "openai/gpt-4o"?']);
    assert.equal(requests.length, 1);
    assert.equal(requests[0].url, '/admin/virtual-models');
    assert.equal(requests[0].request.method, 'DELETE');
    assert.deepEqual(JSON.parse(requests[0].request.body), { source: 'openai/gpt-4o' });
    assert.deepEqual(fetched, ['models', 'virtual', 'sync']);
    assert.equal(module.aliasNotice, 'Virtual model removed.');
    assert.equal(module.rowDeletingKey, '');
});

test('row virtual model deletes are serialized while a delete is in flight', async() => {
    const requests = [];
    let finishFirstDelete;
    const module = createAliasesModule({
        context: {
            fetch: async(url, request) => {
                requests.push({ url, request });
                await new Promise((resolve) => { finishFirstDelete = resolve; });
                return { ok: true, status: 200, json: async() => ({}) };
            }
        },
        window: { confirm: () => true }
    });
    stubRequests(module);
    module.fetchModels = async() => {};
    module.fetchVirtualModels = async() => {};
    module.syncDisplayModels = () => {};

    const firstDelete = module.removeAliasRow({
        key: 'alias:first',
        is_alias: true,
        alias: { name: 'first' }
    });
    await Promise.resolve();

    await module.removeAliasRow({
        key: 'alias:second',
        is_alias: true,
        alias: { name: 'second' }
    });

    assert.equal(requests.length, 1);
    assert.deepEqual(JSON.parse(requests[0].request.body), { source: 'first' });

    finishFirstDelete();
    await firstDelete;
    assert.equal(module.rowDeletingKey, '');
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
    module.virtualModelsAvailable = true;
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
    module.virtualModelsAvailable = true;
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
        'Edit global model access (virtual model exists)'
    );
    assert.equal(module.modelAccessStateClass({ effective_enabled: true }), 'is-enabled');

    module.virtualModelsAvailable = false;

    assert.equal(module.modelAccessStateClass({ effective_enabled: true }), '');
});

test('openProviderOverrideEdit opens the unified editor with provider_name slash selector', () => {
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

    assert.equal(module.vmFormOpen, true);
    assert.equal(module.vmFormSourceLocked, true);
    assert.equal(module.vmForm.source, 'openai-primary/');
    assert.equal(module.vmFormDisplayName, 'All models in openai-primary');
});

test('openVirtualModelEditModel focuses the editor after opening', () => {
    let querySelectorCalls = 0;
    const module = createAliasesModule();
    const calls = [];
    let nextTickCallback = null;
    module.$refs = {
        virtualModelEditor: {
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

    module.openVirtualModelEditModel({
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

    assert.equal(module.vmFormOpen, true);
    assert.deepEqual(calls, []);

    nextTickCallback();

    assert.equal(querySelectorCalls, 1);
    assert.deepEqual(JSON.parse(JSON.stringify(calls)), [
        { preventScroll: true }
    ]);
});

test('openVirtualModelEditAlias focuses the editor after opening', () => {
    let querySelectorCalls = 0;
    const module = createAliasesModule();
    const calls = [];
    let nextTickCallback = null;
    module.$refs = {
        virtualModelEditor: {
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

    module.openVirtualModelEditAlias({
        name: 'smart',
        target_provider: 'openai',
        target_model: 'gpt-4o',
        description: 'Primary chat alias',
        enabled: true
    });

    assert.equal(module.vmFormOpen, true);
    assert.equal(module.vmFormMode, 'edit');
    assert.deepEqual(calls, []);

    nextTickCallback();

    assert.equal(querySelectorCalls, 1);
    assert.deepEqual(JSON.parse(JSON.stringify(calls)), [
        { preventScroll: true }
    ]);
});

test('openGlobalModelOverrideEdit opens the unified editor with slash selector', () => {
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
            user_paths: ['/team/alpha'],
            enabled: true
        }
    ];

    module.openGlobalModelOverrideEdit();

    assert.equal(module.vmFormOpen, true);
    assert.equal(module.vmFormSourceLocked, true);
    assert.equal(module.vmForm.source, '/');
    assert.equal(module.vmFormDisplayName, 'All providers and models');
    assert.equal(module.vmFormDefaultEnabled, false);
    assert.equal(module.vmFormEffectiveEnabled, true);
    assert.equal(module.vmForm.user_paths, '/team/alpha');
});

test('virtual model write paths use generation-aware request handling for stale auth responses', async() => {
    const scenarios = [
        {
            name: 'toggleRowEnabled-alias',
            run(module) {
                return module.toggleRowEnabled({
                    key: 'alias:short',
                    is_alias: true,
                    display_name: 'short',
                    alias: {
                        name: 'short',
                        target_model: 'openai/gpt-4o',
                        description: '',
                        enabled: true
                    }
                });
            },
            errorKey: 'aliasError'
        },
        {
            name: 'toggleRowEnabled-model',
            run(module) {
                return module.toggleRowEnabled({
                    key: 'model:openai/gpt-4o',
                    is_alias: false,
                    display_name: 'openai/gpt-4o',
                    access: { selector: 'openai/gpt-4o', default_enabled: true, effective_enabled: true }
                });
            },
            errorKey: 'aliasError'
        },
        {
            name: 'submitVirtualModelForm',
            setup(module) {
                module.vmForm = {
                    source: 'short',
                    target_model: 'openai/gpt-4o',
                    user_paths: '',
                    description: '',
                    enabled: true
                };
                module.vmFormMode = 'edit';
            },
            run(module) {
                return module.submitVirtualModelForm();
            },
            errorKey: 'vmFormError'
        },
        {
            name: 'deleteVirtualModel',
            setup(module) {
                module.vmForm = {
                    source: 'openai/gpt-4o',
                    target_model: '',
                    user_paths: '/team/alpha',
                    description: '',
                    enabled: true
                };
                module.vmFormHasExisting = true;
            },
            run(module) {
                return module.deleteVirtualModel();
            },
            errorKey: 'vmFormError'
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
            virtualModelsAvailable: true,
            virtualModelsAvailable: true,
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
            fetchVirtualModels() {
                throw new Error('fetchVirtualModels should not run for stale auth in ' + scenario.name);
            },
            fetchModels() {
                throw new Error('fetchModels should not run for stale auth in ' + scenario.name);
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

test('virtual model editor surfaces nested HTTP error payloads', async() => {
    const scenarios = [
        {
            name: 'submitVirtualModelForm',
            message: 'target model is required',
            errorKey: 'vmFormError',
            setup(module) {
                module.vmForm = {
                    source: 'short',
                    target_model: 'openai/gpt-4o',
                    user_paths: '',
                    description: '',
                    enabled: true
                };
                module.vmFormMode = 'edit';
            },
            run(module) {
                return module.submitVirtualModelForm();
            }
        }
    ];

    for (const scenario of scenarios) {
        const module = createAliasesModule({
            context: {
                fetch: async() => ({
                    ok: false,
                    status: 400,
                    statusText: 'Bad Request',
                    json: async() => ({
                        error: {
                            message: scenario.message
                        }
                    })
                })
            }
        });

        Object.assign(module, {
            aliases: [],
            models: [],
            modelOverrideViews: [],
            virtualModelsAvailable: true,
            virtualModelsAvailable: true,
            requestOptions(options) {
                return {
                    ...(options || {}),
                    headers: {}
                };
            },
            headers() {
                return {};
            },
            handleFetchResponse() {
                return false;
            },
            isStaleAuthFetchResult() {
                return false;
            },
            fetchVirtualModels() {
                throw new Error('fetchVirtualModels should not run for ' + scenario.name);
            },
            fetchModels() {
                throw new Error('fetchModels should not run for ' + scenario.name);
            }
        });

        scenario.setup(module);
        await scenario.run(module);

        assert.equal(module[scenario.errorKey], scenario.message, scenario.name);
    }
});
