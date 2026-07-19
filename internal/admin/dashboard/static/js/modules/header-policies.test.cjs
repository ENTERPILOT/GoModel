const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

function createModule(overrides = {}, globals = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'header-policies.js'), 'utf8');
    const context = {
        window: { confirm: () => true },
        console,
        fetch: globals.fetch || (async () => ({ status: 200, json: async () => [] }))
    };
    vm.runInNewContext(source, context);
    return {
        ...context.window.dashboardHeaderPoliciesModule(),
        requestOptions: (options = {}) => options,
        handleFetchResponse: (response) => response.status >= 200 && response.status < 300,
        headers: () => ({}),
        ...overrides
    };
}

test('headerPolicyPayload emits the dedicated API shape and preserves empty literals', () => {
    const module = createModule();
    module.headerPolicyForm = {
        name: ' pin-beta ',
        description: ' beta ',
        methods: ['POST'],
        paths: '/v1/chat/completions, /p/anthropic/*',
        when: [{ header: 'X-Empty', equals: '' }],
        actions: [{ action: 'set', header: 'X-Empty-Upstream', value: '' }]
    };
    const payload = module.headerPolicyPayload();
    assert.equal(JSON.stringify(payload), JSON.stringify({
        name: 'pin-beta',
        description: 'beta',
        methods: ['POST'],
        paths: ['/v1/chat/completions', '/p/anthropic/*'],
        when: [{ header: 'X-Empty', equals: '' }],
        actions: [{ action: 'set', header: 'X-Empty-Upstream', value: '' }]
    }));
});

for (const operation of ['save', 'delete']) {
    test(`${operation} surfaces the API validation message`, async () => {
        const response = {
            status: 400,
            statusText: 'Bad Request',
            json: async () => ({ error: { message: 'header Content-Type cannot be used in header policies' } })
        };
        const module = createModule({}, { fetch: async () => response });
        if (operation === 'save') {
            module.headerPolicyForm = {
                name: 'invalid', description: '', methods: [], paths: '', when: [],
                actions: [{ action: 'set', header: 'Content-Type', value: 'text/plain' }]
            };
            await module.submitHeaderPolicyForm();
        } else {
            await module.deleteHeaderPolicy({ name: 'in-use' });
        }
        assert.equal(module.headerPolicyError, 'header Content-Type cannot be used in header policies');
    });
}

test('openHeaderPolicyEdit hydrates the flattened definition shape', () => {
    const module = createModule();
    module.openHeaderPolicyEdit({
        name: 'pin-beta', methods: ['POST'], paths: ['/v1/*'],
        when: [{ header: 'User-Agent', matches: '^cline/' }],
        actions: [{ action: 'remove', header: 'X-Debug' }]
    });
    assert.equal(module.headerPolicyFormOpen, true);
    assert.equal(module.headerPolicyForm.paths, '/v1/*');
    assert.equal(module.headerPolicyForm.actions[0].action, 'remove');
});

test('fetchHeaderPolicies reads the dedicated endpoint', async () => {
    let requested = '';
    const originalFetch = global.fetch;
    global.fetch = async (url) => {
        requested = url;
        return { status: 200, json: async () => [{ name: 'headers' }] };
    };
    try {
        const source = fs.readFileSync(path.join(__dirname, 'header-policies.js'), 'utf8');
        const context = { window: { confirm: () => true }, console, fetch: global.fetch };
        vm.runInNewContext(source, context);
        const module = {
            ...context.window.dashboardHeaderPoliciesModule(),
            requestOptions: () => ({}),
            handleFetchResponse: () => true,
            headers: () => ({})
        };
        await module.fetchHeaderPolicies();
        assert.equal(requested, '/admin/header-policies');
        assert.equal(module.headerPolicies.length, 1);
    } finally {
        global.fetch = originalFetch;
    }
});
