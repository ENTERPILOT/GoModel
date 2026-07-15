(function(global) {
    function dashboardMcpServersModule() {
        return {
            mcpServers: [],
            mcpServersAvailable: true,
            mcpServersLoading: false,
            mcpServerError: '',
            mcpServerNotice: '',
            mcpServerFilter: '',
            mcpServerFormOpen: false,
            mcpServerFormSubmitting: false,
            mcpServerFormMode: 'create',
            mcpServerSlugEdited: false,
            mcpServerAdvancedOpen: false,
            mcpServerDeletingName: '',
            mcpServerReconnectingName: '',
            mcpCatalogOpen: false,
            mcpCatalogLoading: false,
            mcpCatalogError: '',
            mcpCatalog: {
                server: '',
                status: '',
                instructions: '',
                tools: [],
                prompts: [],
                resources: [],
                templates: []
            },
            mcpServerForm: {
                name: '',
                slug: '',
                url: '',
                transport: 'http',
                description: '',
                enabled: true,
                headers: [],
                allowed_tools: '',
                disallowed_tools: '',
                user_paths: '',
                tool_timeout_seconds: ''
            },

            defaultMcpServerForm() {
                return {
                    name: '',
                    slug: '',
                    url: '',
                    transport: 'http',
                    description: '',
                    enabled: true,
                    headers: [],
                    allowed_tools: '',
                    disallowed_tools: '',
                    user_paths: '',
                    tool_timeout_seconds: ''
                };
            },

            get filteredMcpServers() {
                if (!this.mcpServerFilter) {
                    return this.mcpServers;
                }
                const filter = this.mcpServerFilter.toLowerCase();
                return (this.mcpServers || []).filter((server) => {
                    const fields = [
                        server.name,
                        server.slug,
                        server.url,
                        server.transport,
                        server.description,
                        server.status
                    ];
                    return fields.some((value) => String(value || '').toLowerCase().includes(filter));
                });
            },

            mcpServerStatus(server) {
                return String(server && server.status || '').trim() || 'connecting';
            },

            mcpServerStatusClass(server) {
                switch (this.mcpServerStatus(server)) {
                    case 'connected':
                        return 'status-success';
                    case 'degraded':
                        return String(server && server.last_error || '').trim()
                            ? 'status-error'
                            : 'status-warning';
                    case 'connecting':
                        return 'status-neutral';
                    default:
                        // "disabled" and anything unexpected render gray.
                        return 'status-unknown';
                }
            },

            mcpServerStatusTitle(server) {
                const status = this.mcpServerStatus(server);
                const lastError = String(server && server.last_error || '').trim();
                if (lastError && status !== 'connected') {
                    return lastError;
                }
                if (status === 'connected' && server && server.connected_at) {
                    return 'Connected since ' + this.formatTimestamp(server.connected_at);
                }
                return '';
            },

            mcpServerEndpointLabel(server) {
                if (String(server && server.transport || '') === 'stdio') {
                    return 'local command';
                }
                return String(server && server.url || '').trim() || '—';
            },

            mcpServerSubCountsLabel(server) {
                const prompts = Number(server && server.prompt_count || 0);
                const resources = Number(server && server.resource_count || 0);
                return prompts + ' prompts · ' + resources + ' resources';
            },

            mcpServerSlug(server) {
                return String(server && (server.slug || server.name) || '').trim();
            },

            deriveMcpServerSlug(name) {
                const normalized = String(name || '')
                    .normalize('NFKD')
                    .toLowerCase();
                const slug = normalized
                    .replace(/[\u0300-\u036f]/g, '')
                    .replace(/[^a-z0-9]+/g, '-')
                    .replace(/^-+|-+$/g, '')
                    .slice(0, 64)
                    .replace(/-+$/g, '');
                if (slug) {
                    return slug;
                }
                let hash = 2166136261;
                for (const character of normalized) {
                    hash = Math.imul((hash ^ character.codePointAt(0)) >>> 0, 16777619) >>> 0;
                }
                return 'mcp-' + hash.toString(16).padStart(8, '0');
            },

            syncMcpServerSlugFromName() {
                if (this.mcpServerFormMode === 'create' && !this.mcpServerSlugEdited) {
                    this.mcpServerForm.slug = this.deriveMcpServerSlug(this.mcpServerForm.name);
                }
            },

            markMcpServerSlugEdited() {
                if (this.mcpServerFormMode === 'create') {
                    this.mcpServerSlugEdited = true;
                }
            },

            normalizeMcpCommaList(value) {
                return String(value || '')
                    .split(',')
                    .map((item) => item.trim())
                    .filter((item) => item);
            },

            normalizeMcpUserPaths(value) {
                return String(value || '')
                    .split('\n')
                    .map((item) => item.trim())
                    .filter((item) => item);
            },

            mcpHeadersToRows(headers) {
                if (!headers || typeof headers !== 'object' || Array.isArray(headers)) {
                    return [];
                }
                return Object.keys(headers)
                    .sort()
                    .map((name) => ({ name, value: String(headers[name] || '') }));
            },

            mcpHeaderRowsToObject(rows) {
                const headers = {};
                (Array.isArray(rows) ? rows : []).forEach((row) => {
                    const name = String(row && row.name || '').trim();
                    if (!name) {
                        return;
                    }
                    headers[name] = String(row && row.value || '');
                });
                return headers;
            },

            addMcpServerHeader() {
                this.mcpServerForm.headers.push({ name: '', value: '' });
            },

            removeMcpServerHeader(index) {
                this.mcpServerForm.headers.splice(index, 1);
            },

            focusMcpServerForm() {
                const focus = () => {
                    const refs = this.$refs || {};
                    const editor = refs.mcpServerEditor || null;
                    if (!editor || typeof editor.querySelector !== 'function') {
                        return;
                    }
                    const field = editor.querySelector('[data-modal-autofocus]:not([disabled]), input:not([type="hidden"]):not([disabled]), textarea:not([disabled]), select:not([disabled]), button:not([disabled])');
                    if (!field || typeof field.focus !== 'function') {
                        return;
                    }
                    field.focus({ preventScroll: true });
                };

                const focusAfterPaint = () => {
                    if (typeof global.requestAnimationFrame === 'function') {
                        global.requestAnimationFrame(focus);
                        return;
                    }
                    focus();
                };

                if (typeof this.$nextTick === 'function') {
                    this.$nextTick(focusAfterPaint);
                    return;
                }
                focusAfterPaint();
            },

            openMcpServerCreate() {
                this.mcpServerFormMode = 'create';
                this.mcpServerSlugEdited = false;
                this.mcpServerAdvancedOpen = false;
                this.mcpServerError = '';
                this.mcpServerNotice = '';
                this.mcpServerForm = this.defaultMcpServerForm();
                this.mcpServerFormOpen = true;
                this.focusMcpServerForm();
                if (typeof this.renderIconsAfterUpdate === 'function') {
                    this.renderIconsAfterUpdate();
                }
            },

            openMcpServerEdit(server) {
                if (!server || server.managed) {
                    return;
                }
                this.mcpServerFormMode = 'edit';
                this.mcpServerSlugEdited = true;
                this.mcpServerAdvancedOpen = false;
                this.mcpServerError = '';
                this.mcpServerNotice = '';
                this.mcpServerForm = {
                    name: String(server.name || '').trim(),
                    slug: this.mcpServerSlug(server),
                    url: String(server.url || '').trim(),
                    transport: server.transport === 'sse' ? 'sse' : 'http',
                    description: String(server.description || '').trim(),
                    enabled: server.enabled !== false,
                    headers: this.mcpHeadersToRows(server.headers),
                    allowed_tools: (Array.isArray(server.allowed_tools) ? server.allowed_tools : []).join(', '),
                    disallowed_tools: (Array.isArray(server.disallowed_tools) ? server.disallowed_tools : []).join(', '),
                    user_paths: (Array.isArray(server.user_paths) ? server.user_paths : []).join('\n'),
                    tool_timeout_seconds: server.tool_timeout_seconds ? String(server.tool_timeout_seconds) : ''
                };
                this.mcpServerFormOpen = true;
                this.focusMcpServerForm();
                if (typeof this.renderIconsAfterUpdate === 'function') {
                    this.renderIconsAfterUpdate();
                }
            },

            closeMcpServerForm() {
                this.mcpServerFormOpen = false;
                this.mcpServerFormMode = 'create';
                this.mcpServerSlugEdited = false;
                this.mcpServerAdvancedOpen = false;
                this.mcpServerError = '';
                this.mcpServerForm = this.defaultMcpServerForm();
            },

            async mcpServerResponseMessage(res, fallback) {
                try {
                    const payload = await res.json();
                    if (payload && payload.error && payload.error.message) {
                        return payload.error.message;
                    }
                } catch (_) {
                    // Ignore invalid or empty responses and return the fallback message.
                }
                return fallback;
            },

            async fetchMcpServersPage() {
                this.mcpServersLoading = true;
                this.mcpServerError = '';
                try {
                    const request = this.requestOptions();
                    const res = await fetch('/admin/mcp-servers', request);
                    if (res.status === 503 || res.status === 404) {
                        this.mcpServersAvailable = false;
                        this.mcpServers = [];
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'mcp servers', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    this.mcpServersAvailable = true;
                    if (!handled) {
                        this.mcpServers = [];
                        if (res.status !== 401) {
                            this.mcpServerError = await this.mcpServerResponseMessage(res, 'Failed to load MCP servers.');
                        }
                        return;
                    }
                    const payload = await res.json();
                    this.mcpServers = Array.isArray(payload) ? payload : [];
                } catch (e) {
                    console.error('Failed to fetch MCP servers:', e);
                    this.mcpServers = [];
                    this.mcpServerError = 'Unable to load MCP servers.';
                } finally {
                    this.mcpServersLoading = false;
                    if (typeof this.renderIconsAfterUpdate === 'function') {
                        this.renderIconsAfterUpdate();
                    }
                }
            },

            async submitMcpServerForm() {
                const name = String(this.mcpServerForm.name || '').trim();
                const slug = String(this.mcpServerForm.slug || this.deriveMcpServerSlug(name)).trim().toLowerCase();
                const url = String(this.mcpServerForm.url || '').trim();
                const transport = this.mcpServerForm.transport === 'sse' ? 'sse' : 'http';
                if (!name) {
                    this.mcpServerError = 'Name is required.';
                    return;
                }
                if (!/^[a-z0-9][a-z0-9_-]{0,63}$/.test(slug)) {
                    this.mcpServerError = 'Slug must use 1–64 lowercase ASCII letters, numbers, hyphens, or underscores.';
                    return;
                }
                if (this.mcpServerFormMode === 'create' && (this.mcpServers || []).some((server) => this.mcpServerSlug(server) === slug)) {
                    this.mcpServerError = 'Slug "' + slug + '" is already in use.';
                    return;
                }
                if (!url) {
                    this.mcpServerError = 'URL is required.';
                    return;
                }
                let toolTimeoutSeconds;
                const rawTimeout = String(this.mcpServerForm.tool_timeout_seconds || '').trim();
                if (rawTimeout !== '') {
                    const parsed = Number(rawTimeout);
                    if (!Number.isSafeInteger(parsed) || parsed < 0) {
                        this.mcpServerError = 'Tool timeout must be a non-negative whole number of seconds.';
                        return;
                    }
                    toolTimeoutSeconds = parsed;
                }

                this.mcpServerError = '';
                this.mcpServerNotice = '';
                this.mcpServerFormSubmitting = true;

                const payload = {
                    name,
                    slug,
                    url,
                    transport,
                    headers: this.mcpHeaderRowsToObject(this.mcpServerForm.headers),
                    description: String(this.mcpServerForm.description || '').trim(),
                    enabled: Boolean(this.mcpServerForm.enabled),
                    allowed_tools: this.normalizeMcpCommaList(this.mcpServerForm.allowed_tools),
                    disallowed_tools: this.normalizeMcpCommaList(this.mcpServerForm.disallowed_tools),
                    user_paths: this.normalizeMcpUserPaths(this.mcpServerForm.user_paths),
                    tool_timeout_seconds: toolTimeoutSeconds
                };

                try {
                    const request = this.requestOptions({
                        method: 'PUT',
                        body: JSON.stringify(payload)
                    });
                    const res = await fetch('/admin/mcp-servers', request);
                    if (res.status === 503) {
                        this.mcpServersAvailable = false;
                        this.mcpServerError = 'MCP server management is unavailable.';
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'save mcp server', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        if (res.status === 401) {
                            this.mcpServerError = 'Authentication required.';
                            return;
                        }
                        this.mcpServerError = await this.mcpServerResponseMessage(res, 'Failed to save MCP server.');
                        return;
                    }

                    await this.fetchMcpServersPage();
                    this.mcpServerNotice = 'MCP server "' + name + '" saved.';
                    this.closeMcpServerForm();
                } catch (e) {
                    console.error('Failed to save MCP server:', e);
                    this.mcpServerError = 'Failed to save MCP server.';
                } finally {
                    this.mcpServerFormSubmitting = false;
                }
            },

            mcpConfirmAction(message) {
                if (typeof global.confirm === 'function') {
                    return global.confirm(message);
                }
                return true;
            },

            async deleteMcpServer(server) {
                const name = String(server && server.name || '').trim();
                const slug = this.mcpServerSlug(server);
                if (!slug || this.mcpServerDeletingName || (server && server.managed)) {
                    return;
                }
                if (!this.mcpConfirmAction('Delete MCP server "' + name + '"? Clients lose access to its tools immediately.')) {
                    return;
                }

                this.mcpServerDeletingName = slug;
                this.mcpServerError = '';
                this.mcpServerNotice = '';

                try {
                    const request = this.requestOptions({ method: 'DELETE' });
                    const res = await fetch('/admin/mcp-servers/' + encodeURIComponent(slug), request);
                    if (res.status === 503) {
                        this.mcpServersAvailable = false;
                        this.mcpServerError = 'MCP server management is unavailable.';
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'delete mcp server', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        if (res.status === 401) {
                            this.mcpServerError = 'Authentication required.';
                            return;
                        }
                        this.mcpServerError = await this.mcpServerResponseMessage(res, 'Failed to delete MCP server.');
                        return;
                    }

                    await this.fetchMcpServersPage();
                    if (this.mcpServerFormOpen && this.mcpServerForm.slug === slug) {
                        this.closeMcpServerForm();
                    }
                    this.mcpServerNotice = 'MCP server "' + name + '" deleted.';
                } catch (e) {
                    console.error('Failed to delete MCP server:', e);
                    this.mcpServerError = 'Failed to delete MCP server.';
                } finally {
                    this.mcpServerDeletingName = '';
                }
            },

            async reconnectMcpServer(server) {
                const name = String(server && server.name || '').trim();
                const slug = this.mcpServerSlug(server);
                if (!slug || this.mcpServerReconnectingName) {
                    return;
                }

                this.mcpServerReconnectingName = slug;
                this.mcpServerError = '';
                this.mcpServerNotice = '';

                try {
                    const request = this.requestOptions({ method: 'POST' });
                    const res = await fetch('/admin/mcp-servers/' + encodeURIComponent(slug) + '/reconnect', request);
                    if (res.status === 503) {
                        this.mcpServersAvailable = false;
                        this.mcpServerError = 'MCP server management is unavailable.';
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'reconnect mcp server', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        if (res.status === 401) {
                            this.mcpServerError = 'Authentication required.';
                            return;
                        }
                        this.mcpServerError = await this.mcpServerResponseMessage(res, 'Failed to reconnect MCP server.');
                        return;
                    }

                    const refreshed = await res.json();
                    if (refreshed && refreshed.name) {
                        this.mcpServers = (this.mcpServers || []).map((item) =>
                            this.mcpServerSlug(item) === this.mcpServerSlug(refreshed) ? refreshed : item
                        );
                    } else {
                        await this.fetchMcpServersPage();
                    }
                    const status = this.mcpServerStatus(refreshed);
                    if (status === 'connected') {
                        this.mcpServerNotice = 'MCP server "' + name + '" reconnected.';
                    } else if (status === 'disabled') {
                        this.mcpServerNotice = 'MCP server "' + name + '" is disabled; no connection was attempted.';
                    } else {
                        this.mcpServerError = 'Reconnect attempted, but MCP server "' + name + '" is still ' + status + '.';
                    }
                    if (typeof this.renderIconsAfterUpdate === 'function') {
                        this.renderIconsAfterUpdate();
                    }
                } catch (e) {
                    console.error('Failed to reconnect MCP server:', e);
                    this.mcpServerError = 'Failed to reconnect MCP server.';
                } finally {
                    this.mcpServerReconnectingName = '';
                }
            },

            defaultMcpCatalog() {
                return {
                    server: '',
                    status: '',
                    instructions: '',
                    tools: [],
                    prompts: [],
                    resources: [],
                    templates: []
                };
            },

            normalizeMcpCatalog(name, payload) {
                const source = payload && typeof payload === 'object' && !Array.isArray(payload) ? payload : {};
                const list = (value) => (Array.isArray(value) ? value : []).filter((item) => item && typeof item === 'object');
                return {
                    server: String(source.server || name || '').trim(),
                    status: String(source.status || '').trim(),
                    instructions: String(source.instructions || '').trim(),
                    tools: list(source.tools),
                    prompts: list(source.prompts),
                    resources: list(source.resources),
                    templates: list(source.templates)
                };
            },

            // mcpNamespacedName is the aggregated /mcp endpoint form of one tool
            // or prompt name; the catalog reports the upstream originals.
            mcpNamespacedName(name) {
                return String(this.mcpCatalog && this.mcpCatalog.server || '') + '_' + String(name || '');
            },

            mcpCatalogSections() {
                const catalog = this.mcpCatalog || this.defaultMcpCatalog();
                const describe = (name, description) => {
                    const label = String(name || '').trim();
                    const copy = String(description || '').trim();
                    if (label && copy) {
                        return label + ' — ' + copy;
                    }
                    return copy || label;
                };
                const feature = (kind) => (item) => ({
                    key: kind + ':' + String(item.name || ''),
                    name: String(item.name || ''),
                    aggregated: this.mcpNamespacedName(item.name),
                    description: String(item.description || '').trim()
                });
                const sections = [
                    { key: 'tools', title: 'Tools', items: (catalog.tools || []).map(feature('tool')) },
                    { key: 'prompts', title: 'Prompts', items: (catalog.prompts || []).map(feature('prompt')) },
                    {
                        key: 'resources',
                        title: 'Resources',
                        items: (catalog.resources || []).map((item) => ({
                            key: 'resource:' + String(item.uri || ''),
                            name: String(item.uri || ''),
                            aggregated: '',
                            description: describe(item.name, item.description)
                        }))
                    },
                    {
                        key: 'templates',
                        title: 'Resource templates',
                        items: (catalog.templates || []).map((item) => ({
                            key: 'template:' + String(item.uri_template || ''),
                            name: String(item.uri_template || ''),
                            aggregated: '',
                            description: describe(item.name, item.description)
                        }))
                    }
                ];
                return sections.filter((section) => section.items.length > 0);
            },

            mcpCatalogIsEmpty() {
                return this.mcpCatalogSections().length === 0;
            },

            async openMcpServerCatalog(server) {
                const name = String(server && server.name || '').trim();
                const slug = this.mcpServerSlug(server);
                if (!slug) {
                    return;
                }

                this.mcpCatalogOpen = true;
                this.mcpCatalogLoading = true;
                this.mcpCatalogError = '';
                this.mcpCatalog = {
                    ...this.defaultMcpCatalog(),
                    server: slug,
                    status: this.mcpServerStatus(server)
                };
                if (typeof this.renderIconsAfterUpdate === 'function') {
                    this.renderIconsAfterUpdate();
                }

                try {
                    const request = this.requestOptions();
                    const res = await fetch('/admin/mcp-servers/' + encodeURIComponent(slug) + '/catalog', request);
                    if (res.status === 503) {
                        this.mcpServersAvailable = false;
                        this.mcpCatalogError = 'MCP server management is unavailable.';
                        return;
                    }
                    if (res.status === 404) {
                        this.mcpCatalogError = 'MCP server "' + name + '" was not found.';
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'mcp server catalog', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        if (res.status === 401) {
                            this.mcpCatalogError = 'Authentication required.';
                            return;
                        }
                        this.mcpCatalogError = await this.mcpServerResponseMessage(res, 'Failed to load MCP server catalog.');
                        return;
                    }
                    const payload = await res.json();
                    this.mcpCatalog = this.normalizeMcpCatalog(slug, payload);
                } catch (e) {
                    console.error('Failed to load MCP server catalog:', e);
                    this.mcpCatalogError = 'Failed to load MCP server catalog.';
                } finally {
                    this.mcpCatalogLoading = false;
                }
            },

            closeMcpServerCatalog() {
                this.mcpCatalogOpen = false;
                this.mcpCatalogLoading = false;
                this.mcpCatalogError = '';
                this.mcpCatalog = this.defaultMcpCatalog();
            },

            // Overview-card helpers. The card is hidden entirely when the
            // feature is unavailable or no servers are configured.
            mcpOverviewTotal() {
                return (this.mcpServers || []).length;
            },

            mcpOverviewConnectedCount() {
                return (this.mcpServers || []).filter((server) => this.mcpServerStatus(server) === 'connected').length;
            },

            mcpOverviewDegradedCount() {
                return (this.mcpServers || []).filter((server) =>
                    server && server.enabled !== false && this.mcpServerStatus(server) === 'degraded'
                ).length;
            },

            mcpOverviewVisible() {
                return this.mcpServersAvailable && this.mcpOverviewTotal() > 0;
            },

            mcpOverviewRatioText() {
                return String(this.mcpOverviewConnectedCount()) + '/' + String(this.mcpOverviewTotal());
            },

            // Mirrors providerStatusSummaryClass: the card gets a warning accent
            // while any enabled server is degraded.
            mcpOverviewSummaryClass() {
                return this.mcpOverviewDegradedCount() > 0 ? 'is-degraded' : 'is-healthy';
            },

            mcpOverviewSummaryText() {
                const degraded = this.mcpOverviewDegradedCount();
                if (degraded > 0) {
                    return String(degraded) + ' server' + (degraded === 1 ? '' : 's') + ' need' + (degraded === 1 ? 's' : '') + ' attention';
                }
                const total = this.mcpOverviewTotal();
                const connected = this.mcpOverviewConnectedCount();
                if (total > 0 && connected === total) {
                    return 'All MCP servers connected';
                }
                return String(connected) + ' of ' + String(total) + ' server' + (total === 1 ? '' : 's') + ' connected';
            }
        };
    }

    global.dashboardMcpServersModule = dashboardMcpServersModule;
})(window);
