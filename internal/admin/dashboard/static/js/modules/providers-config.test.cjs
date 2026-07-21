const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadProvidersConfigModuleFactory(overrides = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'providers-config.js'), 'utf8');
    const window = {
        ...(overrides.window || {})
    };
    const context = {
        window,
        console,
        ...overrides
    };
    vm.createContext(context);
    vm.runInContext(source, context);
    return context.window.dashboardProvidersConfigModule;
}

function createProvidersConfigModule(overrides) {
    const factory = loadProvidersConfigModuleFactory(overrides);
    return factory();
}

function stubRequestHelpers(module, handled = true) {
    Object.assign(module, {
        requestOptions: (options) => ({ ...(options || {}), headers: {} }),
        handleFetchResponse: () => handled
    });
}

test('fetchProviderCredentialsPage stores the returned list and marks unavailable on 404/503', async () => {
    for (const status of [404, 503]) {
        const module = createProvidersConfigModule({
            fetch: async () => ({ status, statusText: 'nope' })
        });
        stubRequestHelpers(module);

        await module.fetchProviderCredentialsPage();

        assert.equal(module.providerCredentialsAvailable, false, String(status));
        assert.equal(module.providerCredentials.length, 0, String(status));
        assert.equal(module.providerCredentialsLoading, false, String(status));
    }
});

test('fetchProviderCredentialsPage stores rows and lazily loads types once', async () => {
    const rows = [
        { name: 'my-openai', type: 'openai', api_keys: ['***'], enabled: true, managed: false },
        { name: 'config-anthropic', type: 'anthropic', enabled: true, managed: true }
    ];
    const requests = [];
    const module = createProvidersConfigModule({
        fetch: async (url) => {
            requests.push(url);
            if (url === '/admin/provider-credentials') {
                return { status: 200, statusText: 'OK', json: async () => rows };
            }
            if (url === '/admin/provider-credentials/types') {
                return { status: 200, statusText: 'OK', json: async () => ['openai', 'anthropic', 'ollama'] };
            }
            throw new Error('unexpected url ' + url);
        }
    });
    stubRequestHelpers(module);

    await module.fetchProviderCredentialsPage();

    assert.equal(module.providerCredentialsAvailable, true);
    assert.deepEqual(module.providerCredentials, rows);
    assert.equal(module.providerCredentialTypesLoaded, true);
    assert.deepEqual(module.providerCredentialTypes, ['openai', 'anthropic', 'ollama']);
    assert.deepEqual(requests, ['/admin/provider-credentials', '/admin/provider-credentials/types']);

    // A second fetch should not re-request the (already cached) types list.
    await module.fetchProviderCredentialsPage();
    assert.deepEqual(requests, [
        '/admin/provider-credentials',
        '/admin/provider-credentials/types',
        '/admin/provider-credentials'
    ]);
});

test('fetchProviderCredentialsPage surfaces generic API failures', async () => {
    const module = createProvidersConfigModule({
        fetch: async () => ({
            status: 500,
            statusText: 'Internal Server Error',
            json: async () => ({ error: { message: 'storage unavailable' } })
        })
    });
    stubRequestHelpers(module, false);

    await module.fetchProviderCredentialsPage();

    assert.equal(module.providerCredentialsAvailable, true);
    assert.equal(module.providerCredentials.length, 0);
    assert.equal(module.providerCredentialError, 'storage unavailable');
});

test('submitProviderCredentialForm sends a normalized PUT payload on create', async () => {
    const requests = [];
    const module = createProvidersConfigModule({
        fetch: async (url, request) => {
            requests.push({ url, request });
            return { status: 200, statusText: 'OK' };
        }
    });
    stubRequestHelpers(module);
    module.fetchProviderCredentialsPage = async () => {};

    module.providerCredentialForm = {
        ...module.defaultProviderCredentialForm(),
        name: ' my-openai ',
        type: 'openai',
        api_keys: [{ value: ' sk-live-123 ' }],
        base_url: ' https://api.openai.com/v1 ',
        models: ' gpt-4o, gpt-4o-mini ,,',
        enabled: true
    };

    await module.submitProviderCredentialForm();

    assert.equal(requests.length, 1);
    assert.equal(requests[0].url, '/admin/provider-credentials');
    assert.equal(requests[0].request.method, 'PUT');
    const body = JSON.parse(requests[0].request.body);
    assert.equal(body.name, 'my-openai');
    assert.equal(body.type, 'openai');
    assert.deepEqual(body.api_keys, [' sk-live-123 ']);
    assert.equal(body.base_url, 'https://api.openai.com/v1');
    assert.deepEqual(body.models, ['gpt-4o', 'gpt-4o-mini']);
    assert.equal(body.enabled, true);
    assert.equal(module.providerCredentialNotice, 'Provider "my-openai" saved.');
    assert.equal(module.providerCredentialFormOpen, false);
});

test('submitProviderCredentialForm preserves untouched API key positions as "***" on edit', async () => {
    const requests = [];
    const module = createProvidersConfigModule({
        fetch: async (url, request) => {
            requests.push({ url, request });
            return { status: 200, statusText: 'OK' };
        }
    });
    stubRequestHelpers(module);
    module.fetchProviderCredentialsPage = async () => {};
    // Types are already cached, so opening the edit form does not trigger an
    // extra /types request that would pollute the captured request list.
    module.providerCredentialTypesLoaded = true;

    const existing = {
        name: 'my-openai',
        type: 'openai',
        api_keys: ['***', '***'],
        base_url: 'https://api.openai.com/v1',
        models: ['gpt-4o'],
        enabled: true,
        managed: false
    };
    module.providerCredentials = [existing];
    module.openProviderCredentialEdit(existing);

    // Untouched: both rows stay "***". Only a newly added third key is real.
    module.providerCredentialForm.api_keys.push({ value: 'sk-new-key' });

    await module.submitProviderCredentialForm();

    assert.equal(requests.length, 1);
    const body = JSON.parse(requests[0].request.body);
    assert.deepEqual(body.api_keys, ['***', '***', 'sk-new-key']);
    assert.equal(body.name, 'my-openai');
});

test('submitProviderCredentialForm validates required fields and blank key rows without fetching', async () => {
    const module = createProvidersConfigModule({
        fetch: async () => {
            throw new Error('fetch should not run for invalid forms');
        }
    });

    module.providerCredentialForm = module.defaultProviderCredentialForm();
    await module.submitProviderCredentialForm();
    assert.equal(module.providerCredentialError, 'Name is required.');

    module.providerCredentialForm = { ...module.defaultProviderCredentialForm(), name: 'my-openai' };
    await module.submitProviderCredentialForm();
    assert.equal(module.providerCredentialError, 'Type is required.');

    module.providerCredentialForm = {
        ...module.defaultProviderCredentialForm(),
        name: 'my-openai',
        type: 'openai',
        api_keys: [{ value: '   ' }]
    };
    await module.submitProviderCredentialForm();
    assert.equal(module.providerCredentialError, 'API key rows cannot be empty. Remove the row instead of leaving it blank.');
});

test('deleteProviderCredential opens a typed confirmation dialog requiring the provider name', async () => {
    const requests = [];
    const module = createProvidersConfigModule({
        fetch: async (url, request) => {
            requests.push({ url, request });
            return { status: 204, statusText: 'No Content' };
        }
    });
    stubRequestHelpers(module);
    module.fetchProviderCredentialsPage = async () => {};
    module.providerCredentials = [{ name: 'my-openai', type: 'openai', managed: false }];

    let capturedOptions = null;
    module.openTypedConfirmationDialog = (options) => {
        capturedOptions = options;
    };
    module.closeTypedConfirmationDialog = () => {};

    module.deleteProviderCredential('my-openai');

    assert.ok(capturedOptions);
    assert.equal(capturedOptions.requiredText, 'my-openai');
    assert.equal(requests.length, 0, 'no network call before confirmation');

    // Simulate the operator typing the name and submitting.
    await capturedOptions.onConfirm.call(module);

    assert.equal(requests.length, 1);
    assert.equal(requests[0].url, '/admin/provider-credentials/my-openai');
    assert.equal(requests[0].request.method, 'DELETE');
    assert.equal(module.providerCredentialNotice, 'Provider "my-openai" deleted.');
});

test('deleteProviderCredential URL-encodes the provider name', async () => {
    const requests = [];
    const module = createProvidersConfigModule({
        fetch: async (url, request) => {
            requests.push({ url, request });
            return { status: 204, statusText: 'No Content' };
        }
    });
    stubRequestHelpers(module);
    module.fetchProviderCredentialsPage = async () => {};
    module.providerCredentials = [{ name: 'team/openai', type: 'openai', managed: false }];

    let capturedOptions = null;
    module.openTypedConfirmationDialog = (options) => {
        capturedOptions = options;
    };
    module.closeTypedConfirmationDialog = () => {};

    module.deleteProviderCredential('team/openai');
    await capturedOptions.onConfirm.call(module);

    assert.equal(requests[0].url, '/admin/provider-credentials/team%2Fopenai');
});

test('managed rows cannot be edited or deleted client-side', async () => {
    const module = createProvidersConfigModule({
        fetch: async () => {
            throw new Error('fetch should not run for a managed row');
        }
    });
    module.providerCredentials = [{ name: 'config-anthropic', type: 'anthropic', managed: true }];

    let dialogOpened = false;
    module.openTypedConfirmationDialog = () => {
        dialogOpened = true;
    };

    // Edit: opening the form for a managed row must be a no-op.
    module.openProviderCredentialEdit({ name: 'config-anthropic', type: 'anthropic', managed: true });
    assert.equal(module.providerCredentialFormOpen, false);

    // Delete: the confirmation dialog must never open for a managed row.
    module.deleteProviderCredential('config-anthropic');
    assert.equal(dialogOpened, false);
});

test('filteredProviderCredentials matches name, type, and base URL', () => {
    const module = createProvidersConfigModule();
    module.providerCredentials = [
        { name: 'my-openai', type: 'openai', base_url: 'https://api.openai.com/v1' },
        { name: 'local-ollama', type: 'ollama', base_url: 'http://localhost:11434' }
    ];

    module.providerCredentialFilter = 'ollama';
    assert.deepEqual(module.filteredProviderCredentials.map((row) => row.name), ['local-ollama']);

    module.providerCredentialFilter = 'openai.com';
    assert.deepEqual(module.filteredProviderCredentials.map((row) => row.name), ['my-openai']);

    module.providerCredentialFilter = '';
    assert.equal(module.filteredProviderCredentials.length, 2);
});

test('providerCredentialAuthLabel infers auth mode from populated fields', () => {
    const module = createProvidersConfigModule();
    assert.equal(module.providerCredentialAuthLabel({ api_keys: ['***'] }), '1 key');
    assert.equal(module.providerCredentialAuthLabel({ api_keys: ['***', '***'] }), '2 keys');
    assert.equal(module.providerCredentialAuthLabel({ service_account_json: '***' }), 'service account');
    assert.equal(module.providerCredentialAuthLabel({ service_account_file: '/etc/gcp.json' }), 'service account');
    assert.equal(module.providerCredentialAuthLabel({ vertex_project: 'my-project' }), 'ADC');
    assert.equal(module.providerCredentialAuthLabel({}), 'keyless');
});

test('providerCredentialModelsLabel reports counts or auto-discovery', () => {
    const module = createProvidersConfigModule();
    assert.equal(module.providerCredentialModelsLabel({ models: [] }), 'auto-discovered');
    assert.equal(module.providerCredentialModelsLabel({}), 'auto-discovered');
    assert.equal(module.providerCredentialModelsLabel({ models: ['gpt-4o'] }), '1 model');
    assert.equal(module.providerCredentialModelsLabel({ models: ['gpt-4o', 'gpt-4o-mini'] }), '2 models');
});

test('openProviderCredentialEdit prefills the form from a view row', () => {
    const module = createProvidersConfigModule();
    module.providerCredentialTypesLoaded = true;

    module.openProviderCredentialEdit({
        name: 'my-openai',
        type: 'openai',
        api_keys: ['***'],
        base_url: 'https://api.openai.com/v1',
        models: ['gpt-4o', 'gpt-4o-mini'],
        enabled: false,
        managed: false
    });

    assert.equal(module.providerCredentialFormOpen, true);
    assert.equal(module.providerCredentialFormMode, 'edit');
    assert.equal(module.providerCredentialForm.name, 'my-openai');
    assert.equal(module.providerCredentialForm.type, 'openai');
    // Built inside the module's vm realm: compare structurally via JSON
    // rather than assert.deepEqual, which (under node:assert/strict) also
    // checks prototype identity and cross-realm plain objects fail that.
    assert.equal(JSON.stringify(module.providerCredentialForm.api_keys), JSON.stringify([{ value: '***' }]));
    assert.equal(module.providerCredentialForm.models, 'gpt-4o, gpt-4o-mini');
    assert.equal(module.providerCredentialForm.enabled, false);
});

test('suggestProviderCredentialName prefers the bare type name when free', () => {
    const module = createProvidersConfigModule();
    module.providerCredentials = [{ name: 'anthropic' }];

    assert.equal(module.suggestProviderCredentialName('openai'), 'openai');
});

test('suggestProviderCredentialName falls back to "{type}-N" when the bare name is taken', () => {
    const module = createProvidersConfigModule();
    module.providerCredentials = [{ name: 'openai' }, { name: 'openai-1' }, { name: 'other' }];

    // openai and openai-1 are both taken (one declared/config, one dashboard-managed).
    assert.equal(module.suggestProviderCredentialName('openai'), 'openai-2');
});

test('suggestProviderCredentialName returns empty for an empty type', () => {
    const module = createProvidersConfigModule();
    module.providerCredentials = [{ name: 'openai' }];

    assert.equal(module.suggestProviderCredentialName(''), '');
    assert.equal(module.suggestProviderCredentialName('   '), '');
});

test('onProviderCredentialTypeChange resets the name suggestion only while creating', () => {
    const module = createProvidersConfigModule();
    module.providerCredentials = [{ name: 'openai' }];
    module.providerCredentialForm = { ...module.providerCredentialForm, type: 'openai', name: 'stale-custom-name' };

    module.providerCredentialFormMode = 'edit';
    module.onProviderCredentialTypeChange();
    assert.equal(module.providerCredentialForm.name, 'stale-custom-name', 'edit mode must not reset the immutable name');

    module.providerCredentialFormMode = 'create';
    module.onProviderCredentialTypeChange();
    assert.equal(module.providerCredentialForm.name, 'openai-1', 'create mode resets to a fresh suggestion, ignoring the prior custom name');
});
