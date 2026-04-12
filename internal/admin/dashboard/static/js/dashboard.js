// GOModel Dashboard — Alpine.js + Chart.js logic

function dashboard() {
    function resolveModuleFactory(factory, windowName) {
        if (typeof factory === 'function') {
            return factory;
        }
        if (typeof window !== 'undefined' && typeof window[windowName] === 'function') {
            return window[windowName];
        }
        return null;
    }

    const timezoneModuleFactory = resolveModuleFactory(
        typeof dashboardTimezoneModule === 'function' ? dashboardTimezoneModule : null,
        'dashboardTimezoneModule'
    );
    const calendarModuleFactory = resolveModuleFactory(
        typeof dashboardContributionCalendarModule === 'function' ? dashboardContributionCalendarModule : null,
        'dashboardContributionCalendarModule'
    );

    const base = {
        // State
        page: 'overview',
        days: '30',
        loading: false,
        authError: false,
        needsAuth: false,
        apiKey: '',
        theme: 'system',
        sidebarCollapsed: false,
        settingsSubpage: 'general',
        runtimeRefreshLoading: false,
        runtimeRefreshNotice: '',
        runtimeRefreshError: '',
        runtimeRefreshReport: null,

        // Date picker
        datePickerOpen: false,
        selectedPreset: '30',
        customStartDate: null,
        customEndDate: null,
        selectingDate: 'start',
        calendarMonth: new Date(),
        cursorHint: { show: false, x: 0, y: 0 },

        // Interval
        interval: 'daily',

        // Data
        summary: { total_requests: 0, total_input_tokens: 0, total_output_tokens: 0, total_tokens: 0, total_input_cost: null, total_output_cost: null, total_cost: null },
        daily: [],
        cacheOverview: {
            summary: {
                total_hits: 0,
                exact_hits: 0,
                semantic_hits: 0,
                total_input_tokens: 0,
                total_output_tokens: 0,
                total_tokens: 0,
                total_saved_cost: null
            },
            daily: []
        },
        providerStatus: {
            summary: { total: 0, healthy: 0, degraded: 0, unhealthy: 0, overall_status: 'degraded' },
            providers: []
        },
        models: [],
        modelsLoading: false,
        categories: [],
        activeCategory: 'all',
        hasCalendarModule: calendarModuleFactory !== null,

        // Filters
        modelFilter: '',

        // Chart
        chart: null,

        // Usage page state
        usageMode: 'tokens',
        modelUsage: [],
        usageLog: { entries: [], total: 0, limit: 50, offset: 0 },
        usageLogSearch: '',
        usageLogModel: '',
        usageLogProvider: '',
        usageLogUserPath: '',
        usageBarChart: null,

        // Audit page state
        auditLog: { entries: [], total: 0, limit: 25, offset: 0 },
        auditSearch: '',
        auditMethod: '',
        auditStatusCode: '',
        auditStream: '',
        auditFetchToken: 0,
        auditExpandedEntries: {},

        // Conversation drawer state
        conversationOpen: false,
        conversationLoading: false,
        conversationError: '',
        conversationAnchorID: '',
        conversationEntries: [],
        conversationMessages: [],
        conversationRequestToken: 0,
        conversationReturnFocusEl: null,
        bodyPointerStart: null,

        _parseRoute(pathname) {
            const path = pathname.replace(/\/$/, '');
            const rest = path.replace('/admin/dashboard', '').replace(/^\//, '');
            const parts = rest.split('/');
            let page = parts[0];
            if (page === 'execution-plans') {
                page = 'workflows';
            }
            if (page === 'audit') {
                page = 'audit-logs';
            }
            const sub = parts[1] || null;
            if (page === 'settings' && sub === 'guardrails') {
                return { page: 'guardrails', sub: null };
            }
            page = (['overview', 'usage', 'models', 'workflows', 'audit-logs', 'guardrails', 'auth-keys', 'settings'].includes(page)) ? page : 'overview';
            return { page, sub };
        },

        _normalizeSettingsSubpage(subpage) {
            return 'general';
        },

        _settingsPath(subpage) {
            return '/admin/dashboard/settings';
        },

        _applyRoute(page, sub) {
            this.page = page;
            this.settingsSubpage = page === 'settings'
                ? this._normalizeSettingsSubpage(sub)
                : 'general';

            if (page === 'usage' && sub === 'costs') this.usageMode = 'costs';
            if (page === 'usage' && sub !== 'costs') this.usageMode = 'tokens';
            if (page === 'audit-logs') this.fetchAuditLog(true);
            if (page === 'auth-keys' && typeof this.fetchAuthKeys === 'function') this.fetchAuthKeys();
            if (page === 'workflows' && typeof this.fetchExecutionPlansPage === 'function') {
                this.fetchExecutionPlansPage();
            }
            if (page === 'guardrails' && typeof this.fetchGuardrailsPage === 'function') {
                this.fetchGuardrailsPage();
            }
            if (page === 'settings') {
                if (typeof this.ensureTimezoneOptions === 'function') {
                    this.ensureTimezoneOptions();
                }
            }
            if (page === 'overview') this.renderChart();
            if (page === 'usage') this.fetchUsagePage();
        },

        init() {
            if (typeof this.initTimeZoneState === 'function') {
                this.initTimeZoneState();
            }
            if (typeof this.initProviderStatusPreferences === 'function') {
                this.initProviderStatusPreferences();
            }
            this.apiKey = localStorage.getItem('gomodel_api_key') || '';
            this.theme = localStorage.getItem('gomodel_theme') || 'system';
            this.sidebarCollapsed = localStorage.getItem('gomodel_sidebar_collapsed') === 'true';
            this.applyTheme();

            const { page, sub } = this._parseRoute(window.location.pathname);
            this._applyRoute(page, sub);

            window.addEventListener('popstate', () => {
                const { page: p, sub: s } = this._parseRoute(window.location.pathname);
                this._applyRoute(p, s);
            });

            window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
                if (this.theme === 'system') {
                    this.renderChart();
                }
            });

            this.fetchAll();
        },

        toggleSidebar() {
            this.sidebarCollapsed = !this.sidebarCollapsed;
            localStorage.setItem('gomodel_sidebar_collapsed', this.sidebarCollapsed);
            setTimeout(() => this.renderChart(), 220);
        },

        navigate(page) {
            if (page === 'settings') {
                this.navigateSettings('general');
                return;
            }

            history.pushState(null, '', '/admin/dashboard/' + page);
            this._applyRoute(page, null);
        },

        navigateSettings(subpage) {
            const normalized = this._normalizeSettingsSubpage(subpage);
            history.pushState(null, '', this._settingsPath(normalized));
            this._applyRoute('settings', normalized);
        },

        guardrailsPageVisible() {
            return typeof this.executionPlanRuntimeBooleanFlag === 'function'
                ? this.executionPlanRuntimeBooleanFlag('GUARDRAILS_ENABLED', true)
                : true;
        },

        setTheme(t) {
            this.theme = t;
            localStorage.setItem('gomodel_theme', t);
            this.applyTheme();
            this.renderChart();
            this.renderBarChart();
        },

        toggleTheme() {
            const order = ['light', 'system', 'dark'];
            this.setTheme(order[(order.indexOf(this.theme) + 1) % order.length]);
        },

        applyTheme() {
            const root = document.documentElement;
            if (this.theme === 'system') {
                root.removeAttribute('data-theme');
            } else {
                root.setAttribute('data-theme', this.theme);
            }
        },

        cssVar(name) {
            return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
        },

        chartColors() {
            return {
                grid: this.cssVar('--chart-grid'),
                text: this.cssVar('--chart-text'),
                tooltipBg: this.cssVar('--chart-tooltip-bg'),
                tooltipBorder: this.cssVar('--chart-tooltip-border'),
                tooltipText: this.cssVar('--chart-tooltip-text')
            };
        },

        saveApiKey() {
            if (this.apiKey) {
                localStorage.setItem('gomodel_api_key', this.apiKey);
            } else {
                localStorage.removeItem('gomodel_api_key');
            }
        },

        headers() {
            const h = { 'Content-Type': 'application/json' };
            if (this.apiKey) {
                h.Authorization = 'Bearer ' + this.apiKey;
            }
            if (typeof this.effectiveTimezone === 'function') {
                h['X-GoModel-Timezone'] = this.effectiveTimezone();
            }
            return h;
        },

        _startAbortableRequest(controllerKey) {
            const current = this[controllerKey];
            if (current && typeof current.abort === 'function') {
                current.abort();
            }

            if (typeof AbortController !== 'function') {
                this[controllerKey] = null;
                return null;
            }

            const controller = new AbortController();
            this[controllerKey] = controller;
            return controller;
        },

        _clearAbortableRequest(controllerKey, controller) {
            if (this[controllerKey] === controller) {
                this[controllerKey] = null;
            }
        },

        _isCurrentAbortableRequest(controllerKey, controller) {
            if (!controller) {
                return true;
            }
            return this[controllerKey] === controller && !controller.signal.aborted;
        },

        _isAbortError(error) {
            return Boolean(error) && (error.name === 'AbortError' || error.code === 20);
        },

        dashboardDataFetches() {
            const requests = [this.fetchUsage(), this.fetchModels(), this.fetchCategories()];
            if (typeof this.fetchProviderStatus === 'function') {
                requests.push(this.fetchProviderStatus());
            }
            if (typeof this.fetchAliases === 'function') {
                requests.push(this.fetchAliases());
            }
            if (typeof this.fetchModelOverrides === 'function') {
                requests.push(this.fetchModelOverrides());
            }
            if (typeof this.fetchExecutionPlansPage === 'function') {
                requests.push(this.fetchExecutionPlansPage());
            }
            if (this.hasCalendarModule && typeof this.fetchCalendarData === 'function') {
                requests.push(this.fetchCalendarData());
            }
            return requests;
        },

        async fetchAll() {
            this.loading = true;
            this.authError = false;
            this.needsAuth = false;
            const requests = this.dashboardDataFetches();
            await Promise.all(requests);
            this.loading = false;
        },

        async refreshRuntime() {
            if (this.runtimeRefreshLoading) {
                return;
            }
            this.runtimeRefreshLoading = true;
            this.runtimeRefreshNotice = '';
            this.runtimeRefreshError = '';
            this.runtimeRefreshReport = null;

            try {
                const res = await fetch('/admin/api/v1/runtime/refresh', {
                    method: 'POST',
                    headers: this.headers()
                });
                if (!this.handleFetchResponse(res, 'runtime refresh')) {
                    this.runtimeRefreshNotice = 'Runtime refresh failed.';
                    this.runtimeRefreshError = this.runtimeRefreshNotice;
                    return;
                }

                const payload = await res.json();
                this.runtimeRefreshReport = payload && typeof payload === 'object' ? payload : null;
                this.runtimeRefreshNotice = this.runtimeRefreshSummary();
                await this.refreshDashboardDataAfterRuntimeRefresh();
            } catch (e) {
                console.error('Failed to refresh runtime:', e);
                this.runtimeRefreshNotice = 'Runtime refresh failed.';
                this.runtimeRefreshError = this.runtimeRefreshNotice;
            } finally {
                this.runtimeRefreshLoading = false;
            }
        },

        async refreshDashboardDataAfterRuntimeRefresh() {
            const requests = this.dashboardDataFetches();
            if (this.page === 'audit-logs' && typeof this.fetchAuditLog === 'function') {
                requests.push(this.fetchAuditLog(true));
            }
            if (this.page === 'auth-keys' && typeof this.fetchAuthKeys === 'function') {
                requests.push(this.fetchAuthKeys());
            }
            if (this.page === 'guardrails' && typeof this.fetchGuardrailsPage === 'function') {
                requests.push(this.fetchGuardrailsPage());
            }
            if (this.page === 'usage' && typeof this.fetchUsagePage === 'function') {
                requests.push(this.fetchUsagePage());
            }
            await Promise.all(requests);
        },

        runtimeRefreshStatus() {
            const report = this.runtimeRefreshReport;
            return String((report && report.status) || 'ok').toLowerCase();
        },

        runtimeRefreshSummary() {
            const report = this.runtimeRefreshReport;
            if (!report || typeof report !== 'object') {
                return 'Runtime refresh completed.';
            }
            const modelCount = Number(report.model_count || 0);
            const providerCount = Number(report.provider_count || 0);
            const status = this.runtimeRefreshStatus();
            const prefix = status === 'ok'
                ? 'Runtime refreshed.'
                : status === 'partial'
                    ? 'Runtime refresh completed with warnings.'
                    : 'Runtime refresh failed.';
            return prefix + ' ' + modelCount + ' model' + (modelCount === 1 ? '' : 's') +
                ' across ' + providerCount + ' provider' + (providerCount === 1 ? '' : 's') + '.';
        },

        runtimeRefreshSucceeded() {
            return Boolean(this.runtimeRefreshReport) && this.runtimeRefreshStatus() === 'ok';
        },

        runtimeRefreshWarnings() {
            return Boolean(this.runtimeRefreshReport) && this.runtimeRefreshStatus() !== 'ok';
        },

        runtimeRefreshStepLabel(step) {
            const name = String(step && step.name || '').replace(/_/g, ' ');
            const status = String(step && step.status || '').trim();
            const detail = String((step && (step.error || step.message)) || '').trim();
            if (!name) return detail || status || '';
            if (!detail) return name + ': ' + status;
            return name + ': ' + status + ' - ' + detail;
        },

        handleFetchResponse(res, label) {
            if (res.status === 401) {
                this.authError = true;
                this.needsAuth = true;
                return false;
            }
            if (!res.ok) {
                console.error(`Failed to fetch ${label}: ${res.status} ${res.statusText}`);
                return false;
            }
            return true;
        },

        _formatDate(date) {
            if (!date) return '';
            if (typeof date === 'string') return date;
            return date.getUTCFullYear() + '-' +
                String(date.getUTCMonth() + 1).padStart(2, '0') + '-' +
                String(date.getUTCDate()).padStart(2, '0');
        },

        async fetchModels() {
            const controller = this._startAbortableRequest('_modelsFetchController');
            const isCurrentRequest = () => this._isCurrentAbortableRequest('_modelsFetchController', controller);
            const options = { headers: this.headers() };
            if (controller) {
                options.signal = controller.signal;
            }

            this.modelsLoading = true;
            try {
                let url = '/admin/api/v1/models';
                if (this.activeCategory && this.activeCategory !== 'all') {
                    url += '?category=' + encodeURIComponent(this.activeCategory);
                }
                const res = await fetch(url, options);
                if (!isCurrentRequest()) {
                    return;
                }
                if (!this.handleFetchResponse(res, 'models')) {
                    if (!isCurrentRequest()) {
                        return;
                    }
                    this.models = [];
                    if (typeof this.syncDisplayModels === 'function') this.syncDisplayModels();
                    return;
                }
                const payload = await res.json();
                if (!isCurrentRequest()) {
                    return;
                }
                this.models = payload;
                if (typeof this.syncDisplayModels === 'function') this.syncDisplayModels();
            } catch (e) {
                if (this._isAbortError(e)) {
                    return;
                }
                if (!isCurrentRequest()) {
                    return;
                }
                console.error('Failed to fetch models:', e);
                this.models = [];
                if (typeof this.syncDisplayModels === 'function') this.syncDisplayModels();
            } finally {
                const currentRequest = isCurrentRequest();
                this._clearAbortableRequest('_modelsFetchController', controller);
                if (currentRequest) {
                    this.modelsLoading = false;
                }
            }
        },

        async fetchCategories() {
            try {
                const res = await fetch('/admin/api/v1/models/categories', { headers: this.headers() });
                if (!this.handleFetchResponse(res, 'categories')) {
                    this.categories = [];
                    return;
                }
                this.categories = await res.json();
            } catch (e) {
                console.error('Failed to fetch categories:', e);
                this.categories = [];
            }
        },

        selectCategory(cat) {
            this.activeCategory = cat;
            this.modelFilter = '';
            this.fetchModels();
        },

        get filteredModels() {
            if (!this.modelFilter) return this.models;
            const f = this.modelFilter.toLowerCase();
            return this.models.filter((m) =>
                (m.model?.id ?? '').toLowerCase().includes(f) ||
                (m.provider_name ?? '').toLowerCase().includes(f) ||
                (m.provider_type ?? '').toLowerCase().includes(f) ||
                (m.selector ?? '').toLowerCase().includes(f) ||
                (m.model?.owned_by ?? '').toLowerCase().includes(f) ||
                (m.model?.metadata?.modes ?? []).join(',').toLowerCase().includes(f) ||
                (m.model?.metadata?.categories ?? []).join(',').toLowerCase().includes(f)
            );
        },

        providerTypeValue(value) {
            return String(value && value.provider || '').trim();
        },

        providerDisplayValue(value) {
            const providerName = String(value && value.provider_name || '').trim();
            if (providerName) return providerName;
            return this.providerTypeValue(value);
        },

        qualifiedModelDisplay(value) {
            const model = String(value && value.model || '').trim();
            if (!model) return '-';
            if (model.includes('/')) return model;
            const provider = this.providerDisplayValue(value);
            if (!provider) return model;
            return provider + '/' + model;
        },

        qualifiedResolvedModelDisplay(value) {
            const model = String(value && value.resolved_model || '').trim();
            if (!model) return '-';
            if (model.includes('/')) return model;
            const provider = this.providerDisplayValue(value);
            if (!provider) return model;
            return provider + '/' + model;
        },

        formatNumber(n) {
            if (n == null || n === undefined) return '-';
            return n.toLocaleString();
        },

        formatCost(v) {
            if (v == null || v === undefined) return 'N/A';
            return '$' + v.toFixed(4);
        },

        formatCostTooltip(entry) {
            const lines = [];
            lines.push('Input: ' + this.formatCost(entry.input_cost));
            lines.push('Output: ' + this.formatCost(entry.output_cost));
            if (entry.raw_data) {
                lines.push('');
                for (const [key, value] of Object.entries(entry.raw_data)) {
                    const label = key.replace(/_/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase());
                    lines.push(label + ': ' + this.formatNumber(value));
                }
            }
            return lines.join('\n');
        },

        formatPrice(v) {
            if (v == null || v === undefined) return '\u2014';
            return '$' + v.toFixed(2);
        },

        formatPriceFine(v) {
            if (v == null || v === undefined) return '\u2014';
            if (v < 0.01) return '$' + v.toFixed(6);
            return '$' + v.toFixed(4);
        },

        categoryCount(cat) {
            const entry = this.categories.find((c) => c.category === cat);
            return entry ? entry.count : 0;
        },

        formatTokensShort(n) {
            if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
            if (n >= 1000) return (n / 1000).toFixed(1) + 'K';
            return String(n);
        },

        formatTimestamp(ts) {
            if (typeof this.formatTimestampInEffectiveTimeZone === 'function') {
                return this.formatTimestampInEffectiveTimeZone(ts);
            }
            if (!ts) return '-';
            const d = new Date(ts);
            if (Number.isNaN(d.getTime())) return '-';
            return d.getFullYear() + '-' +
                String(d.getMonth() + 1).padStart(2, '0') + '-' +
                String(d.getDate()).padStart(2, '0') + ' ' +
                String(d.getHours()).padStart(2, '0') + ':' +
                String(d.getMinutes()).padStart(2, '0') + ':' +
                String(d.getSeconds()).padStart(2, '0');
        },

        formatDateUTC(ts) {
            if (!ts) return '-';
            const d = new Date(ts);
            if (Number.isNaN(d.getTime())) return '-';
            return d.getUTCFullYear() + '-' +
                String(d.getUTCMonth() + 1).padStart(2, '0') + '-' +
                String(d.getUTCDate()).padStart(2, '0');
        },

        formatTimestampUTC(ts) {
            if (!ts) return '-';
            const d = new Date(ts);
            if (Number.isNaN(d.getTime())) return '-';
            return d.getUTCFullYear() + '-' +
                String(d.getUTCMonth() + 1).padStart(2, '0') + '-' +
                String(d.getUTCDate()).padStart(2, '0') + ' ' +
                String(d.getUTCHours()).padStart(2, '0') + ':' +
                String(d.getUTCMinutes()).padStart(2, '0') + ':' +
                String(d.getUTCSeconds()).padStart(2, '0') + ' UTC';
        }
    };

    const moduleFactories = [
        timezoneModuleFactory,
        resolveModuleFactory(
            typeof dashboardDatePickerModule === 'function' ? dashboardDatePickerModule : null,
            'dashboardDatePickerModule'
        ),
        resolveModuleFactory(
            typeof dashboardProvidersModule === 'function' ? dashboardProvidersModule : null,
            'dashboardProvidersModule'
        ),
        resolveModuleFactory(
            typeof dashboardUsageModule === 'function' ? dashboardUsageModule : null,
            'dashboardUsageModule'
        ),
        resolveModuleFactory(
            typeof dashboardAuditListModule === 'function' ? dashboardAuditListModule : null,
            'dashboardAuditListModule'
        ),
        resolveModuleFactory(
            typeof dashboardAliasesModule === 'function' ? dashboardAliasesModule : null,
            'dashboardAliasesModule'
        ),
        resolveModuleFactory(
            typeof dashboardAuthKeysModule === 'function' ? dashboardAuthKeysModule : null,
            'dashboardAuthKeysModule'
        ),
        resolveModuleFactory(
            typeof dashboardGuardrailsModule === 'function' ? dashboardGuardrailsModule : null,
            'dashboardGuardrailsModule'
        ),
        resolveModuleFactory(
            typeof dashboardExecutionPlansModule === 'function' ? dashboardExecutionPlansModule : null,
            'dashboardExecutionPlansModule'
        ),
        resolveModuleFactory(
            typeof dashboardConversationDrawerModule === 'function' ? dashboardConversationDrawerModule : null,
            'dashboardConversationDrawerModule'
        ),
        calendarModuleFactory,
        resolveModuleFactory(
            typeof dashboardChartsModule === 'function' ? dashboardChartsModule : null,
            'dashboardChartsModule'
        )
    ];

    return moduleFactories.reduce((app, factory) => {
        if (!factory) return app;
        Object.defineProperties(app, Object.getOwnPropertyDescriptors(factory()));
        return app;
    }, base);
}
