const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

function createModule(overrides = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'header-policies.js'), 'utf8');
    const context = {
        window: { confirm: () => true },
        console,
        fetch: async () => ({ status: 200, json: async () => [] })
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
