const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadMcpServersModuleFactory(overrides = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'mcp-servers.js'), 'utf8');
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
    return context.window.dashboardMcpServersModule;
}

function createMcpServersModule(overrides) {
    const factory = loadMcpServersModuleFactory(overrides);
    return factory();
}

test('mcpHeadersToRows and mcpHeaderRowsToObject round-trip, preserving masked secrets', () => {
    const module = createMcpServersModule();

    const rows = module.mcpHeadersToRows({ 'X-Extra': 'plain', Authorization: '***' });
    assert.equal(JSON.stringify(rows), JSON.stringify([
        { name: 'Authorization', value: '***' },
        { name: 'X-Extra', value: 'plain' }
    ]));

    rows.push({ name: '  ', value: 'dropped: empty name' });
    rows.push({ name: 'X-New', value: 'fresh-secret' });
    assert.equal(JSON.stringify(module.mcpHeaderRowsToObject(rows)), JSON.stringify({
        Authorization: '***',
        'X-Extra': 'plain',
        'X-New': 'fresh-secret'
    }));
});

test('mcpHeadersToRows tolerates missing or malformed header payloads', () => {
    const module = createMcpServersModule();
    assert.equal(JSON.stringify(module.mcpHeadersToRows(null)), '[]');
    assert.equal(JSON.stringify(module.mcpHeadersToRows(['not', 'an', 'object'])), '[]');
});

test('mcpServerStatusClass maps statuses to badge classes', () => {
    const module = createMcpServersModule();
    assert.equal(module.mcpServerStatusClass({ status: 'connected' }), 'status-success');
    assert.equal(module.mcpServerStatusClass({ status: 'degraded', last_error: 'boom' }), 'status-error');
    assert.equal(module.mcpServerStatusClass({ status: 'degraded' }), 'status-warning');
    assert.equal(module.mcpServerStatusClass({ status: 'connecting' }), 'status-neutral');
    assert.equal(module.mcpServerStatusClass({ status: 'disabled' }), 'status-unknown');
    assert.equal(module.mcpServerStatusClass({}), 'status-neutral');
});

test('mcpServerStatusTitle surfaces last_error for degraded servers', () => {
    const module = createMcpServersModule();
    assert.equal(module.mcpServerStatusTitle({ status: 'degraded', last_error: 'dial tcp: refused' }), 'dial tcp: refused');
    module.formatTimestamp = (ts) => 'formatted:' + ts;
    assert.equal(
        module.mcpServerStatusTitle({ status: 'connected', connected_at: '2026-07-07T00:00:00Z' }),
        'Connected since formatted:2026-07-07T00:00:00Z'
    );
    assert.equal(module.mcpServerStatusTitle({ status: 'connecting' }), '');
});

test('mcpServerEndpointLabel shows a command indicator for stdio servers', () => {
    const module = createMcpServersModule();
    assert.equal(module.mcpServerEndpointLabel({ transport: 'stdio', url: '' }), 'local command');
    assert.equal(module.mcpServerEndpointLabel({ transport: 'http', url: 'https://mcp.example.com/mcp' }), 'https://mcp.example.com/mcp');
    assert.equal(module.mcpServerEndpointLabel({ transport: 'http' }), '—');
});

test('list normalization splits comma tools and newline user paths', () => {
    const module = createMcpServersModule();
    assert.equal(JSON.stringify(module.normalizeMcpCommaList(' search_issues, get_file ,, ')), JSON.stringify(['search_issues', 'get_file']));
    assert.equal(JSON.stringify(module.normalizeMcpUserPaths('/\n /team/alpha \n\n')), JSON.stringify(['/', '/team/alpha']));
});

test('fetchMcpServersPage marks the feature unavailable on 404 and 503', async () => {
    for (const status of [404, 503]) {
        const module = createMcpServersModule({
            fetch: async () => ({ status, statusText: 'nope' })
        });
        Object.assign(module, {
            requestOptions: (options) => ({ ...(options || {}), headers: {} }),
            handleFetchResponse: () => true
        });

        await module.fetchMcpServersPage();

        assert.equal(module.mcpServersAvailable, false, String(status));
        assert.equal(module.mcpServers.length, 0, String(status));
        assert.equal(module.mcpServersLoading, false, String(status));
    }
});

test('fetchMcpServersPage stores the returned server list', async () => {
    const servers = [
        { name: 'github', transport: 'http', status: 'connected', managed: false },
        { name: 'local-tools', transport: 'stdio', status: 'connected', managed: true }
    ];
    const module = createMcpServersModule({
        fetch: async () => ({
            status: 200,
            statusText: 'OK',
            json: async () => servers
        })
    });
    Object.assign(module, {
        requestOptions: (options) => ({ ...(options || {}), headers: {} }),
        handleFetchResponse: () => true
    });

    await module.fetchMcpServersPage();

    assert.equal(module.mcpServersAvailable, true);
    assert.deepEqual(module.mcpServers, servers);
});

test('submitMcpServerForm sends a normalized PUT payload', async () => {
    const requests = [];
    const module = createMcpServersModule({
        fetch: async (url, request) => {
            requests.push({ url, request });
            return { status: 200, statusText: 'OK' };
        }
    });
    Object.assign(module, {
        requestOptions: (options) => ({ ...(options || {}), headers: {} }),
        handleFetchResponse: () => true,
        fetchMcpServersPage: async () => {}
    });
    module.mcpServerForm = {
        name: ' github ',
        url: ' https://mcp.example.com/mcp ',
        transport: 'sse',
        description: ' Issue tools ',
        enabled: true,
        headers: [
            { name: 'Authorization', value: '***' },
            { name: '', value: 'ignored' }
        ],
        allowed_tools: 'search_issues, get_file',
        disallowed_tools: '',
        user_paths: '/team/alpha\n/team/beta',
        tool_timeout_seconds: '45'
    };

    await module.submitMcpServerForm();

    assert.equal(requests.length, 1);
    assert.equal(requests[0].url, '/admin/mcp-servers');
    assert.equal(requests[0].request.method, 'PUT');
    assert.deepEqual(JSON.parse(requests[0].request.body), {
        name: 'github',
        url: 'https://mcp.example.com/mcp',
        transport: 'sse',
        headers: { Authorization: '***' },
        description: 'Issue tools',
        enabled: true,
        allowed_tools: ['search_issues', 'get_file'],
        disallowed_tools: [],
        user_paths: ['/team/alpha', '/team/beta'],
        tool_timeout_seconds: 45
    });
    assert.equal(module.mcpServerNotice, 'MCP server "github" saved.');
    assert.equal(module.mcpServerFormOpen, false);
});

test('submitMcpServerForm validates required fields and timeout without fetching', async () => {
    const module = createMcpServersModule({
        fetch: async () => {
            throw new Error('fetch should not run for invalid forms');
        }
    });

    module.mcpServerForm = module.defaultMcpServerForm();
    await module.submitMcpServerForm();
    assert.equal(module.mcpServerError, 'Name is required.');

    module.mcpServerForm = { ...module.defaultMcpServerForm(), name: 'github' };
    await module.submitMcpServerForm();
    assert.equal(module.mcpServerError, 'URL is required.');

    module.mcpServerForm = {
        ...module.defaultMcpServerForm(),
        name: 'github',
        url: 'https://mcp.example.com/mcp',
        tool_timeout_seconds: '-3'
    };
    await module.submitMcpServerForm();
    assert.equal(module.mcpServerError, 'Tool timeout must be a non-negative number of seconds.');
});

test('deleteMcpServer targets the encoded name and skips managed rows', async () => {
    const requests = [];
    const module = createMcpServersModule({
        fetch: async (url, request) => {
            requests.push({ url, request });
            return { status: 200, statusText: 'OK' };
        },
        window: {
            confirm: () => true
        }
    });
    Object.assign(module, {
        requestOptions: (options) => ({ ...(options || {}), headers: {} }),
        handleFetchResponse: () => true,
        fetchMcpServersPage: async () => {}
    });

    await module.deleteMcpServer({ name: 'team/tools', managed: true });
    assert.equal(requests.length, 0);

    await module.deleteMcpServer({ name: 'team/tools', managed: false });
    assert.equal(requests.length, 1);
    assert.equal(requests[0].url, '/admin/mcp-servers/team%2Ftools');
    assert.equal(requests[0].request.method, 'DELETE');
    assert.equal(module.mcpServerNotice, 'MCP server "team/tools" deleted.');
});

test('deleteMcpServer aborts when the confirmation is dismissed', async () => {
    const module = createMcpServersModule({
        fetch: async () => {
            throw new Error('fetch should not run when confirm is declined');
        },
        window: {
            confirm: () => false
        }
    });

    await module.deleteMcpServer({ name: 'github', managed: false });

    assert.equal(module.mcpServerDeletingName, '');
    assert.equal(module.mcpServerError, '');
});

test('reconnectMcpServer replaces the refreshed row in place', async () => {
    const refreshed = { name: 'github', status: 'connected', tool_count: 12 };
    const requests = [];
    const module = createMcpServersModule({
        fetch: async (url, request) => {
            requests.push({ url, request });
            return {
                status: 200,
                statusText: 'OK',
                json: async () => refreshed
            };
        }
    });
    Object.assign(module, {
        requestOptions: (options) => ({ ...(options || {}), headers: {} }),
        handleFetchResponse: () => true
    });
    module.mcpServers = [
        { name: 'github', status: 'degraded', tool_count: 0 },
        { name: 'other', status: 'connected', tool_count: 3 }
    ];

    await module.reconnectMcpServer({ name: 'github' });

    assert.equal(requests.length, 1);
    assert.equal(requests[0].url, '/admin/mcp-servers/github/reconnect');
    assert.equal(requests[0].request.method, 'POST');
    assert.deepEqual(module.mcpServers[0], refreshed);
    assert.equal(module.mcpServers[1].name, 'other');
    assert.equal(module.mcpServerNotice, 'MCP server "github" reconnected.');
});

test('openMcpServerEdit prefills the form and refuses managed servers', () => {
    const module = createMcpServersModule();

    module.openMcpServerEdit({ name: 'managed-one', managed: true });
    assert.equal(module.mcpServerFormOpen, false);

    module.openMcpServerEdit({
        name: 'github',
        url: 'https://mcp.example.com/mcp',
        transport: 'sse',
        description: 'Issue tools',
        enabled: false,
        managed: false,
        headers: { Authorization: '***' },
        allowed_tools: ['search_issues'],
        disallowed_tools: ['delete_repo'],
        user_paths: ['/team/alpha'],
        tool_timeout_seconds: 45
    });

    assert.equal(module.mcpServerFormOpen, true);
    assert.equal(module.mcpServerFormMode, 'edit');
    assert.equal(JSON.stringify(module.mcpServerForm), JSON.stringify({
        name: 'github',
        url: 'https://mcp.example.com/mcp',
        transport: 'sse',
        description: 'Issue tools',
        enabled: false,
        headers: [{ name: 'Authorization', value: '***' }],
        allowed_tools: 'search_issues',
        disallowed_tools: 'delete_repo',
        user_paths: '/team/alpha',
        tool_timeout_seconds: '45'
    }));
});

test('filteredMcpServers matches name, url, transport, and status', () => {
    const module = createMcpServersModule();
    module.mcpServers = [
        { name: 'github', url: 'https://mcp.github.com', transport: 'http', status: 'connected' },
        { name: 'search', url: 'https://mcp.example.com', transport: 'sse', status: 'degraded' }
    ];

    module.mcpServerFilter = 'sse';
    assert.equal(JSON.stringify(module.filteredMcpServers.map((s) => s.name)), JSON.stringify(['search']));

    module.mcpServerFilter = 'github';
    assert.equal(JSON.stringify(module.filteredMcpServers.map((s) => s.name)), JSON.stringify(['github']));

    module.mcpServerFilter = '';
    assert.equal(module.filteredMcpServers.length, 2);
});
