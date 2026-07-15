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

test('MCP server table remains horizontally accessible on narrow screens', () => {
    const template = fs.readFileSync(path.join(__dirname, '..', '..', '..', 'templates', 'page-mcp-servers.html'), 'utf8');
    const css = fs.readFileSync(path.join(__dirname, '..', '..', '..', 'static', 'css', 'dashboard.css'), 'utf8');

    assert.match(template, /table-wrapper mcp-server-table-wrapper/);
    assert.match(css, /\.mcp-server-table-wrapper\s*\{[^}]*overflow-x:\s*auto/s);
    assert.match(css, /\.mcp-server-table-wrapper \.data-table\s*\{[^}]*min-width:/s);
});

test('MCP server editor exposes connection fields and collapses advanced settings on each open', () => {
    const template = fs.readFileSync(path.join(__dirname, '..', '..', '..', 'templates', 'page-mcp-servers.html'), 'utf8');
    const module = createMcpServersModule();
    const advancedIndex = template.indexOf('<details class="mcp-server-advanced"');

    assert.match(template, /<details class="mcp-server-advanced"\s+:open="mcpServerAdvancedOpen"/s);
    assert.match(template, /<option value="http">Streamable HTTP<\/option>\s*<option value="sse">SSE \(legacy\)<\/option>/s);
    assert.ok(template.indexOf('id="mcp-server-transport"') < advancedIndex);
    assert.ok(template.indexOf('id="mcp-server-slug"') < advancedIndex);
    assert.ok(template.indexOf('@click="addMcpServerHeader()"') < advancedIndex);
    assert.ok(template.indexOf('id="mcp-server-description"') > advancedIndex);
    assert.equal(module.mcpServerAdvancedOpen, false);

    module.mcpServerAdvancedOpen = true;
    module.openMcpServerCreate();
    assert.equal(module.mcpServerAdvancedOpen, false);

    module.mcpServerAdvancedOpen = true;
    module.openMcpServerEdit({ name: 'github', url: 'https://mcp.example.com/mcp', managed: false });
    assert.equal(module.mcpServerAdvancedOpen, false);

    module.mcpServerAdvancedOpen = true;
    module.closeMcpServerForm();
    assert.equal(module.mcpServerAdvancedOpen, false);
});

test('MCP display name derives an editable creation slug and preserves it while editing', () => {
    const module = createMcpServersModule();
    assert.equal(module.deriveMcpServerSlug('  Linear MCP  '), 'linear-mcp');
    assert.equal(module.deriveMcpServerSlug('Café Tools'), 'cafe-tools');
    assert.equal(module.deriveMcpServerSlug('线性'), 'mcp-b7ccbb8b');

    module.openMcpServerCreate();
    module.mcpServerForm.name = 'Linear MCP';
    module.syncMcpServerSlugFromName();
    assert.equal(module.mcpServerForm.slug, 'linear-mcp');

    module.mcpServerForm.slug = 'linear';
    module.markMcpServerSlugEdited();
    module.mcpServerForm.name = 'Linear Issues';
    module.syncMcpServerSlugFromName();
    assert.equal(module.mcpServerForm.slug, 'linear');

    module.openMcpServerEdit({ name: '线性', slug: 'linear', url: 'https://mcp.linear.app/mcp', managed: false });
    assert.equal(module.mcpServerForm.name, '线性');
    assert.equal(module.mcpServerForm.slug, 'linear');
});

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

test('fetchMcpServersPage surfaces generic API failures', async () => {
    const module = createMcpServersModule({
        fetch: async () => ({
            status: 500,
            statusText: 'Internal Server Error',
            json: async () => ({ error: { message: 'storage unavailable' } })
        })
    });
    Object.assign(module, {
        requestOptions: (options) => ({ ...(options || {}), headers: {} }),
        handleFetchResponse: () => false
    });

    await module.fetchMcpServersPage();

    assert.equal(module.mcpServersAvailable, true);
    assert.equal(module.mcpServers.length, 0);
    assert.equal(module.mcpServerError, 'storage unavailable');
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
        slug: 'github',
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
        slug: 'github',
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
    assert.equal(module.mcpServerError, 'Tool timeout must be a non-negative whole number of seconds.');

    module.mcpServerForm.tool_timeout_seconds = '1.5';
    await module.submitMcpServerForm();
    assert.equal(module.mcpServerError, 'Tool timeout must be a non-negative whole number of seconds.');
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

test('reconnectMcpServer surfaces degraded and disabled outcomes accurately', async () => {
    for (const [status, expectedError, expectedNotice] of [
        ['degraded', 'Reconnect attempted, but MCP server "github" is still degraded.', ''],
        ['disabled', '', 'MCP server "github" is disabled; no connection was attempted.']
    ]) {
        const module = createMcpServersModule({
            fetch: async () => ({
                status: 200,
                statusText: 'OK',
                json: async () => ({ name: 'github', status })
            })
        });
        Object.assign(module, {
            requestOptions: (options) => ({ ...(options || {}), headers: {} }),
            handleFetchResponse: () => true
        });
        module.mcpServers = [{ name: 'github', status: 'connecting' }];

        await module.reconnectMcpServer({ name: 'github' });

        assert.equal(module.mcpServerError, expectedError, status);
        assert.equal(module.mcpServerNotice, expectedNotice, status);
    }
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
        slug: 'github',
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

test('openMcpServerCatalog populates the inspector, deriving aggregated /mcp names', async () => {
    const catalog = {
        server: 'github',
        status: 'connected',
        instructions: 'Use the issue tools first.',
        tools: [
            { name: 'create_issue', description: 'Create a GitHub issue' },
            { name: 'search_issues' }
        ],
        prompts: [{ name: 'triage', description: 'Triage an issue' }],
        resources: [{ uri: 'repo://readme', name: 'readme', description: 'Repository readme' }],
        templates: [{ uri_template: 'repo://{path}' }]
    };
    const requests = [];
    const module = createMcpServersModule({
        fetch: async (url, request) => {
            requests.push({ url, request });
            return { status: 200, statusText: 'OK', json: async () => catalog };
        }
    });
    Object.assign(module, {
        requestOptions: (options) => ({ ...(options || {}), headers: {} }),
        handleFetchResponse: () => true
    });

    await module.openMcpServerCatalog({ name: 'github', status: 'connected' });

    assert.equal(requests.length, 1);
    assert.equal(requests[0].url, '/admin/mcp-servers/github/catalog');
    assert.equal(module.mcpCatalogOpen, true);
    assert.equal(module.mcpCatalogLoading, false);
    assert.equal(module.mcpCatalogError, '');
    assert.equal(module.mcpCatalog.server, 'github');
    assert.equal(module.mcpCatalog.status, 'connected');
    assert.equal(module.mcpCatalog.instructions, 'Use the issue tools first.');
    assert.equal(module.mcpCatalogIsEmpty(), false);

    const sections = module.mcpCatalogSections();
    assert.equal(JSON.stringify(sections.map((section) => section.key)), JSON.stringify(['tools', 'prompts', 'resources', 'templates']));

    const tools = sections[0].items;
    assert.equal(JSON.stringify(tools[0]), JSON.stringify({
        key: 'tool:create_issue',
        name: 'create_issue',
        aggregated: 'github_create_issue',
        description: 'Create a GitHub issue'
    }));
    assert.equal(tools[1].aggregated, 'github_search_issues');
    assert.equal(tools[1].description, '');

    assert.equal(sections[1].items[0].aggregated, 'github_triage');

    // Resources and templates keep their URIs; only tools and prompts are
    // namespaced on the aggregated endpoint.
    assert.equal(JSON.stringify(sections[2].items[0]), JSON.stringify({
        key: 'resource:repo://readme',
        name: 'repo://readme',
        aggregated: '',
        description: 'readme — Repository readme'
    }));
    assert.equal(JSON.stringify(sections[3].items[0]), JSON.stringify({
        key: 'template:repo://{path}',
        name: 'repo://{path}',
        aggregated: '',
        description: ''
    }));

    module.closeMcpServerCatalog();
    assert.equal(module.mcpCatalogOpen, false);
    assert.equal(JSON.stringify(module.mcpCatalog), JSON.stringify(module.defaultMcpCatalog()));
});

test('openMcpServerCatalog surfaces 404, 503, and network failures as inspector errors', async () => {
    const notFound = createMcpServersModule({
        fetch: async () => ({ status: 404, statusText: 'Not Found' })
    });
    Object.assign(notFound, {
        requestOptions: (options) => ({ ...(options || {}), headers: {} }),
        handleFetchResponse: () => true
    });
    await notFound.openMcpServerCatalog({ name: 'github' });
    assert.equal(notFound.mcpCatalogOpen, true);
    assert.equal(notFound.mcpCatalogLoading, false);
    assert.equal(notFound.mcpCatalogError, 'MCP server "github" was not found.');
    assert.equal(notFound.mcpServersAvailable, true);

    const unavailable = createMcpServersModule({
        fetch: async () => ({ status: 503, statusText: 'Service Unavailable' })
    });
    Object.assign(unavailable, {
        requestOptions: (options) => ({ ...(options || {}), headers: {} }),
        handleFetchResponse: () => true
    });
    await unavailable.openMcpServerCatalog({ name: 'github' });
    assert.equal(unavailable.mcpCatalogError, 'MCP server management is unavailable.');
    assert.equal(unavailable.mcpServersAvailable, false);

    const failing = createMcpServersModule({
        fetch: async () => {
            throw new Error('network down');
        }
    });
    Object.assign(failing, {
        requestOptions: (options) => ({ ...(options || {}), headers: {} }),
        handleFetchResponse: () => true
    });
    await failing.openMcpServerCatalog({ name: 'github' });
    assert.equal(failing.mcpCatalogError, 'Failed to load MCP server catalog.');
    assert.equal(failing.mcpCatalogLoading, false);
});

test('empty catalog keeps the inspector open with the empty hint state', async () => {
    const module = createMcpServersModule({
        fetch: async () => ({
            status: 200,
            statusText: 'OK',
            json: async () => ({ server: 'github', status: 'connecting', tools: [], prompts: [], resources: [], templates: [] })
        })
    });
    Object.assign(module, {
        requestOptions: (options) => ({ ...(options || {}), headers: {} }),
        handleFetchResponse: () => true
    });

    await module.openMcpServerCatalog({ name: 'github', status: 'connecting' });

    assert.equal(module.mcpCatalogOpen, true);
    assert.equal(module.mcpCatalogError, '');
    assert.equal(module.mcpCatalogSections().length, 0);
    assert.equal(module.mcpCatalogIsEmpty(), true);
});

test('normalizeMcpCatalog tolerates missing lists and malformed payloads', () => {
    const module = createMcpServersModule();

    const fromNull = module.normalizeMcpCatalog('github', null);
    assert.equal(fromNull.server, 'github');
    assert.equal(JSON.stringify(fromNull.tools), '[]');
    assert.equal(JSON.stringify(fromNull.templates), '[]');

    const sparse = module.normalizeMcpCatalog('github', {
        status: 'degraded',
        tools: [{ name: 'ok' }, 'not-an-object', null]
    });
    assert.equal(sparse.status, 'degraded');
    assert.equal(JSON.stringify(sparse.tools), JSON.stringify([{ name: 'ok' }]));
    assert.equal(JSON.stringify(sparse.prompts), '[]');
});

test('overview card helpers summarize connected/total and degraded accents', () => {
    const module = createMcpServersModule();

    module.mcpServers = [];
    assert.equal(module.mcpOverviewVisible(), false);

    module.mcpServers = [
        { name: 'github', status: 'connected', enabled: true },
        { name: 'search', status: 'connected', enabled: true },
        { name: 'local', status: 'disabled', enabled: false }
    ];
    assert.equal(module.mcpOverviewVisible(), true);
    assert.equal(module.mcpOverviewRatioText(), '2/3');
    assert.equal(module.mcpOverviewSummaryClass(), 'is-healthy');
    assert.equal(module.mcpOverviewSummaryText(), '2 of 3 servers connected');

    module.mcpServers = [
        { name: 'github', status: 'connected', enabled: true },
        { name: 'search', status: 'degraded', enabled: true }
    ];
    assert.equal(module.mcpOverviewRatioText(), '1/2');
    assert.equal(module.mcpOverviewSummaryClass(), 'is-degraded');
    assert.equal(module.mcpOverviewSummaryText(), '1 server needs attention');

    // A degraded-but-disabled server never flips the accent.
    module.mcpServers = [
        { name: 'github', status: 'connected', enabled: true },
        { name: 'search', status: 'degraded', enabled: false }
    ];
    assert.equal(module.mcpOverviewSummaryClass(), 'is-healthy');

    module.mcpServers = [{ name: 'github', status: 'connected', enabled: true }];
    assert.equal(module.mcpOverviewSummaryText(), 'All MCP servers connected');

    // Feature absent (404/503 marked it unavailable): the card hides even if
    // stale rows linger.
    module.mcpServersAvailable = false;
    assert.equal(module.mcpOverviewVisible(), false);
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
