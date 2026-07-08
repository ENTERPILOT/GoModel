(function(global) {
    function dashboardRateLimitsModule() {
        return {
            rateLimits: [],
            rateLimitsAvailable: true,
            rateLimitsLoading: false,
            rateLimitFetchPromise: null,
            rateLimitFilter: '',
            rateLimitError: '',
            rateLimitNotice: '',
            rateLimitFormOpen: false,
            rateLimitFormSubmitting: false,
            rateLimitFormError: '',
            rateLimitEditing: false,
            rateLimitEditingOriginal: null,
            rateLimitFormReturnToInspector: false,
            rateLimitResettingKey: '',
            rateLimitDeletingKey: '',
            rateLimitInspectorOpen: false,
            rateLimitInspector: { kind: '', provider: '', model: '', title: '' },
            rateLimitForm: {
                scope: 'user_path',
                subject: '/',
                period: 'minute',
                period_seconds: 60,
                max_requests: '',
                max_tokens: '',
                source: 'manual'
            },

            rateLimitsEnabled() {
                return typeof this.workflowRuntimeBooleanFlag === 'function'
                    ? this.workflowRuntimeBooleanFlag('RATE_LIMITS_ENABLED', true)
                    : true;
            },

            defaultRateLimitForm() {
                return {
                    scope: 'user_path',
                    subject: '/',
                    period: 'minute',
                    period_seconds: 60,
                    max_requests: '',
                    max_tokens: '',
                    source: 'manual'
                };
            },

            // One scope metadata table drives the select options, list chips,
            // and the subject field's label/placeholder.
            rateLimitScopeMeta(scope) {
                const meta = {
                    user_path: { label: 'User path', chip: 'user path', fieldLabel: 'User Path', placeholder: '/team/alpha' },
                    provider: { label: 'Provider', chip: 'provider', fieldLabel: 'Provider Name', placeholder: 'openai' },
                    model: { label: 'Model', chip: 'model', fieldLabel: 'Model', placeholder: 'openai/gpt-4o' }
                };
                return meta[scope] || meta.user_path;
            },

            rateLimitScopeOptions() {
                return ['user_path', 'provider', 'model'].map((scope) => ({
                    value: scope,
                    label: this.rateLimitScopeMeta(scope).label
                }));
            },

            rateLimitScope(item) {
                const scope = String(item && item.scope || '').trim();
                return scope || 'user_path';
            },

            rateLimitSubject(item) {
                const subject = String(item && item.subject || '').trim();
                return subject || String(item && item.user_path || '');
            },

            rateLimitScopeLabel(item) {
                return this.rateLimitScopeMeta(this.rateLimitScope(item)).chip;
            },

            rateLimitSubjectFieldLabel() {
                return this.rateLimitScopeMeta(String(this.rateLimitForm && this.rateLimitForm.scope || '')).fieldLabel;
            },

            rateLimitSubjectPlaceholder() {
                return this.rateLimitScopeMeta(String(this.rateLimitForm && this.rateLimitForm.scope || '')).placeholder;
            },

            // Changing scope resets the subject: a user path never carries
            // over to a provider or model rule.
            syncRateLimitScope() {
                const scope = String(this.rateLimitForm && this.rateLimitForm.scope || '');
                this.rateLimitForm.subject = scope === 'user_path' ? '/' : '';
            },

            rateLimitPeriodOptions() {
                return [
                    { value: 'minute', label: 'Per minute' },
                    { value: 'hour', label: 'Per hour' },
                    { value: 'day', label: 'Per day' },
                    { value: 'concurrent', label: 'Concurrent (in-flight)' },
                    { value: 'custom', label: 'Custom seconds' }
                ];
            },

            rateLimitPeriodSeconds(period) {
                switch (String(period || '').trim().toLowerCase()) {
                case 'minute':
                    return 60;
                case 'hour':
                    return 3600;
                case 'day':
                    return 86400;
                case 'concurrent':
                    return 0;
                default:
                    return -1;
                }
            },

            rateLimitPeriodFromSeconds(seconds) {
                switch (Number(seconds || 0)) {
                case 60:
                    return 'minute';
                case 3600:
                    return 'hour';
                case 86400:
                    return 'day';
                case 0:
                    return 'concurrent';
                default:
                    return 'custom';
                }
            },

            syncRateLimitPeriodSeconds() {
                const period = String(this.rateLimitForm && this.rateLimitForm.period || '').trim();
                const seconds = this.rateLimitPeriodSeconds(period);
                if (seconds >= 0) {
                    this.rateLimitForm.period_seconds = seconds;
                }
                // The tokens field is hidden for the concurrent period; drop any
                // value typed earlier so it cannot block the save invisibly.
                if (period === 'concurrent') {
                    this.rateLimitForm.max_tokens = '';
                }
            },

            rateLimitKey(item) {
                return this.rateLimitScope(item) + ':' + this.rateLimitSubject(item) + ':' + String(item && item.period_seconds || '0');
            },

            rateLimitIsConcurrent(item) {
                return Number(item && item.period_seconds || 0) === 0;
            },

            rateLimitPeriodLabel(item) {
                const label = String(item && item.period_label || '').trim();
                if (label) {
                    return label;
                }
                return this.rateLimitPeriodFromSeconds(Number(item && item.period_seconds || 0));
            },

            rateLimitSourceLabel(item) {
                return String(item && item.source || '') === 'config' ? 'config' : 'manual';
            },

            rateLimitIsReadOnly(item) {
                return String(item && item.source || '') === 'config';
            },

            formatRateLimitNumber(value) {
                const numeric = Number(value);
                if (!Number.isFinite(numeric)) {
                    return '0';
                }
                return numeric.toLocaleString();
            },

            rateLimitUsagePercent(used, limit) {
                const usedNum = Number(used);
                const limitNum = Number(limit);
                if (!Number.isFinite(usedNum) || !Number.isFinite(limitNum) || limitNum <= 0) {
                    return 0;
                }
                const percent = Math.round((usedNum / limitNum) * 100);
                return Math.min(Math.max(percent, 0), 100);
            },

            filteredRateLimits() {
                const filter = String(this.rateLimitFilter || '').trim().toLowerCase();
                const items = Array.isArray(this.rateLimits) ? this.rateLimits.slice() : [];
                const scopeOrder = { user_path: 0, provider: 1, model: 2 };
                items.sort((a, b) => {
                    const scopeCompare = (scopeOrder[this.rateLimitScope(a)] || 0) - (scopeOrder[this.rateLimitScope(b)] || 0);
                    if (scopeCompare !== 0) {
                        return scopeCompare;
                    }
                    const subjectCompare = this.rateLimitSubject(a).localeCompare(this.rateLimitSubject(b));
                    if (subjectCompare !== 0) {
                        return subjectCompare;
                    }
                    return Number(a.period_seconds || 0) - Number(b.period_seconds || 0);
                });
                if (!filter) {
                    return items;
                }
                return items.filter((item) => {
                    const subject = this.rateLimitSubject(item).toLowerCase();
                    const scope = this.rateLimitScopeLabel(item).toLowerCase();
                    const period = this.rateLimitPeriodLabel(item).toLowerCase();
                    return subject.includes(filter) || scope.includes(filter) || period.includes(filter);
                });
            },

            normalizeRateLimitListPayload(payload) {
                if (!payload || !Array.isArray(payload.rate_limits)) {
                    return [];
                }
                return payload.rate_limits;
            },

            async fetchRateLimitsPage() {
                if (typeof this.ensureWorkflowRuntimeConfig === 'function') {
                    await this.ensureWorkflowRuntimeConfig();
                }
                if (!this.rateLimitsEnabled()) {
                    this.rateLimits = [];
                    this.rateLimitsAvailable = false;
                    this.rateLimitError = '';
                    return;
                }
                if (this.rateLimitFetchPromise) {
                    return this.rateLimitFetchPromise;
                }
                this.rateLimitFetchPromise = this.fetchRateLimits().finally(() => {
                    this.rateLimitFetchPromise = null;
                });
                return this.rateLimitFetchPromise;
            },

            async fetchRateLimits() {
                this.rateLimitsLoading = true;
                this.rateLimitError = '';
                try {
                    const request = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    const res = await fetch('/admin/rate-limits', request);
                    if (res.status === 503) {
                        this.rateLimitsAvailable = false;
                        this.rateLimits = [];
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'rate limits', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    this.rateLimitsAvailable = true;
                    if (!handled) {
                        this.rateLimitError = 'Unable to load rate limits.';
                        return;
                    }
                    this.rateLimits = this.normalizeRateLimitListPayload(await res.json());
                    if (typeof this.renderIconsAfterUpdate === 'function') {
                        this.renderIconsAfterUpdate();
                    }
                } catch (e) {
                    console.error('Failed to fetch rate limits:', e);
                    this.rateLimits = [];
                    this.rateLimitError = 'Unable to load rate limits.';
                } finally {
                    this.rateLimitsLoading = false;
                }
            },

            openRateLimitForm(item) {
                this.rateLimitEditing = !!item;
                this.rateLimitFormError = '';
                this.rateLimitError = '';
                this.rateLimitNotice = '';
                if (item) {
                    const periodSeconds = Number(item.period_seconds || 0);
                    this.rateLimitEditingOriginal = {
                        scope: this.rateLimitScope(item),
                        subject: this.rateLimitSubject(item),
                        period_seconds: periodSeconds
                    };
                    this.rateLimitForm = {
                        scope: this.rateLimitScope(item),
                        subject: this.rateLimitSubject(item),
                        period: this.rateLimitPeriodFromSeconds(periodSeconds),
                        period_seconds: periodSeconds,
                        max_requests: item.max_requests === null || item.max_requests === undefined ? '' : String(item.max_requests),
                        max_tokens: item.max_tokens === null || item.max_tokens === undefined ? '' : String(item.max_tokens),
                        source: String(item.source || 'manual')
                    };
                } else {
                    this.rateLimitEditingOriginal = null;
                    this.rateLimitForm = this.defaultRateLimitForm();
                }
                this.rateLimitFormOpen = true;
                if (typeof this.renderIconsAfterUpdate === 'function') {
                    this.renderIconsAfterUpdate();
                }
                if (typeof this.$nextTick === 'function') {
                    this.$nextTick(() => {
                        const refs = this.$refs || {};
                        const input = this.rateLimitEditing ? refs.rateLimitMaxRequestsInput : refs.rateLimitSubjectInput;
                        if (input && typeof input.focus === 'function') {
                            input.focus({ preventScroll: true });
                        }
                    });
                }
            },

            closeRateLimitForm() {
                this.rateLimitFormOpen = false;
                this.rateLimitFormSubmitting = false;
                this.rateLimitFormError = '';
                this.rateLimitEditing = false;
                this.rateLimitEditingOriginal = null;
                this.rateLimitForm = this.defaultRateLimitForm();
                if (this.rateLimitFormReturnToInspector) {
                    this.rateLimitFormReturnToInspector = false;
                    this.rateLimitInspectorOpen = true;
                    if (typeof this.renderIconsAfterUpdate === 'function') {
                        this.renderIconsAfterUpdate();
                    }
                }
            },

            // Mirrors the server's per-scope subject normalization so an edit
            // that only respells the same identity (case, slashes) is treated
            // as an in-place update, never a move-plus-delete of itself.
            rateLimitNormalizedIdentity(scope, subject, periodSeconds) {
                let normalized = String(subject || '').trim();
                if (scope === 'provider' || scope === 'model') {
                    normalized = normalized.toLowerCase();
                } else {
                    const segments = normalized.split('/').map((part) => part.trim()).filter(Boolean);
                    normalized = '/' + segments.join('/');
                }
                return scope + ':' + normalized + ':' + Number(periodSeconds || 0);
            },

            rateLimitIdentityMoved(payload) {
                const original = this.rateLimitEditingOriginal;
                if (!original) {
                    return false;
                }
                return this.rateLimitNormalizedIdentity(payload.scope, payload.subject, payload.limit_key.period_seconds)
                    !== this.rateLimitNormalizedIdentity(original.scope, original.subject, original.period_seconds);
            },

            setRateLimitFormSubject(value) {
                this.rateLimitForm.subject = String(value || '');
            },

            rateLimitFormPayload() {
                const form = this.rateLimitForm || {};
                const scope = String(form.scope || 'user_path');
                const subject = String(form.subject || '').trim();
                if (scope !== 'user_path' && !subject) {
                    return { error: this.rateLimitSubjectFieldLabel() + ' is required.' };
                }
                const isConcurrent = String(form.period || '') === 'concurrent';
                // Reject blank custom seconds before Number(): Number('') is 0,
                // which would silently submit a concurrent rule.
                const rawPeriodSeconds = form.period_seconds;
                if (rawPeriodSeconds === '' || rawPeriodSeconds === null || rawPeriodSeconds === undefined) {
                    return { error: 'Period seconds is required.' };
                }
                const periodSeconds = Number(rawPeriodSeconds);
                if (!Number.isInteger(periodSeconds) || periodSeconds < 0 || (periodSeconds === 0 && !isConcurrent)) {
                    return { error: 'Period seconds must be a positive integer (0 only for the concurrent period).' };
                }
                const maxRequests = String(form.max_requests === undefined || form.max_requests === null ? '' : form.max_requests).trim();
                const maxTokens = String(form.max_tokens === undefined || form.max_tokens === null ? '' : form.max_tokens).trim();
                if (!maxRequests && !maxTokens) {
                    return { error: 'Set max requests, max tokens, or both.' };
                }
                if (isConcurrent && maxTokens) {
                    return { error: 'Token limits are not valid for the concurrent period.' };
                }
                const payload = {
                    scope: scope,
                    subject: subject || '/',
                    limit_key: { period_seconds: periodSeconds }
                };
                if (maxRequests) {
                    const parsed = Number(maxRequests);
                    if (!Number.isInteger(parsed) || parsed <= 0) {
                        return { error: 'Max requests must be a positive integer.' };
                    }
                    payload.max_requests = parsed;
                }
                if (maxTokens) {
                    const parsed = Number(maxTokens);
                    if (!Number.isInteger(parsed) || parsed <= 0) {
                        return { error: 'Max tokens must be a positive integer.' };
                    }
                    payload.max_tokens = parsed;
                }
                return { payload };
            },

            async submitRateLimitForm() {
                if (this.rateLimitFormSubmitting) {
                    return;
                }
                const { payload, error } = this.rateLimitFormPayload();
                if (error) {
                    this.rateLimitFormError = error;
                    return;
                }
                const moved = this.rateLimitIdentityMoved(payload);
                const original = this.rateLimitEditingOriginal;
                this.rateLimitFormSubmitting = true;
                this.rateLimitFormError = '';
                try {
                    const request = this.requestOptions({
                        method: 'PUT',
                        body: JSON.stringify(payload)
                    });
                    const res = await fetch('/admin/rate-limits', request);
                    const handled = this.handleFetchResponse(res, 'rate limit save', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.rateLimitFormError = await this.rateLimitResponseError(res, 'Unable to save rate limit.');
                        return;
                    }
                    this.rateLimits = this.normalizeRateLimitListPayload(await res.json());
                    // Identity change = move: the new rule exists, now drop
                    // the one it replaces. The new rule is created first so a
                    // failed delete can never lose the rule.
                    if (moved && !(await this.deleteMovedRateLimitOriginal(original))) {
                        return;
                    }
                    this.closeRateLimitForm();
                    this.rateLimitNotice = moved ? 'Rate limit moved; live counters restarted.' : 'Rate limit saved.';
                    if (typeof this.renderIconsAfterUpdate === 'function') {
                        this.renderIconsAfterUpdate();
                    }
                } catch (e) {
                    console.error('Failed to save rate limit:', e);
                    this.rateLimitFormError = 'Unable to save rate limit.';
                } finally {
                    this.rateLimitFormSubmitting = false;
                }
            },

            async deleteMovedRateLimitOriginal(original) {
                try {
                    const request = this.requestOptions({
                        method: 'DELETE',
                        body: JSON.stringify({
                            scope: original.scope,
                            subject: original.subject,
                            limit_key: { period_seconds: Number(original.period_seconds || 0) }
                        })
                    });
                    const res = await fetch('/admin/rate-limits', request);
                    const handled = this.handleFetchResponse(res, 'rate limit move', request);
                    if (!handled) {
                        this.rateLimitFormError = await this.rateLimitResponseError(res, 'The new rule was saved, but the previous one could not be removed. Delete it manually.');
                        return false;
                    }
                    this.rateLimits = this.normalizeRateLimitListPayload(await res.json());
                    return true;
                } catch (e) {
                    console.error('Failed to remove the moved rate limit:', e);
                    this.rateLimitFormError = 'The new rule was saved, but the previous one could not be removed. Delete it manually.';
                    return false;
                }
            },

            async deleteRateLimit(item) {
                const key = this.rateLimitKey(item);
                if (this.rateLimitDeletingKey === key) {
                    return;
                }
                this.rateLimitDeletingKey = key;
                this.rateLimitError = '';
                this.rateLimitNotice = '';
                try {
                    const request = this.requestOptions({
                        method: 'DELETE',
                        body: JSON.stringify({
                            scope: this.rateLimitScope(item),
                            subject: this.rateLimitSubject(item),
                            limit_key: { period_seconds: Number(item.period_seconds || 0) }
                        })
                    });
                    const res = await fetch('/admin/rate-limits', request);
                    const handled = this.handleFetchResponse(res, 'rate limit delete', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.rateLimitError = await this.rateLimitResponseError(res, 'Unable to delete rate limit.');
                        return;
                    }
                    this.rateLimits = this.normalizeRateLimitListPayload(await res.json());
                    this.rateLimitNotice = 'Rate limit deleted.';
                    if (typeof this.renderIconsAfterUpdate === 'function') {
                        this.renderIconsAfterUpdate();
                    }
                } catch (e) {
                    console.error('Failed to delete rate limit:', e);
                    this.rateLimitError = 'Unable to delete rate limit.';
                } finally {
                    this.rateLimitDeletingKey = '';
                }
            },

            async resetRateLimit(item) {
                const key = this.rateLimitKey(item);
                if (this.rateLimitResettingKey === key) {
                    return;
                }
                this.rateLimitResettingKey = key;
                this.rateLimitError = '';
                this.rateLimitNotice = '';
                try {
                    const request = this.requestOptions({
                        method: 'POST',
                        body: JSON.stringify({
                            scope: this.rateLimitScope(item),
                            subject: this.rateLimitSubject(item),
                            period_seconds: Number(item.period_seconds || 0)
                        })
                    });
                    const res = await fetch('/admin/rate-limits/reset-one', request);
                    const handled = this.handleFetchResponse(res, 'rate limit reset', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.rateLimitError = await this.rateLimitResponseError(res, 'Unable to reset rate limit.');
                        return;
                    }
                    this.rateLimits = this.normalizeRateLimitListPayload(await res.json());
                    this.rateLimitNotice = 'Rate limit counters reset.';
                    if (typeof this.renderIconsAfterUpdate === 'function') {
                        this.renderIconsAfterUpdate();
                    }
                } catch (e) {
                    console.error('Failed to reset rate limit:', e);
                    this.rateLimitError = 'Unable to reset rate limit.';
                } finally {
                    this.rateLimitResettingKey = '';
                }
            },

            async rateLimitResponseError(res, fallback) {
                try {
                    const body = await res.json();
                    const message = body && body.error && body.error.message;
                    return message ? String(message) : fallback;
                } catch (_) {
                    return fallback;
                }
            },

            // --- Effective-limits inspector (Models page) ---

            rateLimitInspectorModelID(row) {
                return String(row && row.model && row.model.id || '').trim();
            },

            openRateLimitInspectorForModel(row) {
                const model = this.rateLimitInspectorModelID(row);
                const provider = String(row && row.provider_name || '').trim().toLowerCase();
                this.rateLimitInspector = {
                    kind: 'model',
                    provider: provider,
                    model: model,
                    title: String(row && row.display_name || model)
                };
                this.showRateLimitInspector();
            },

            openRateLimitInspectorForProvider(group) {
                const provider = String(group && group.provider_name || '').trim().toLowerCase();
                this.rateLimitInspector = {
                    kind: 'provider',
                    provider: provider,
                    model: '',
                    title: String(group && group.display_name || provider)
                };
                this.showRateLimitInspector();
            },

            showRateLimitInspector() {
                this.rateLimitInspectorOpen = true;
                this.fetchRateLimitsPage();
                if (typeof this.renderIconsAfterUpdate === 'function') {
                    this.renderIconsAfterUpdate();
                }
            },

            closeRateLimitInspector() {
                this.rateLimitInspectorOpen = false;
            },

            rateLimitRuleMatchesModel(rule, provider, model) {
                if (this.rateLimitScope(rule) !== 'model') {
                    return false;
                }
                const subject = String(this.rateLimitSubject(rule)).toLowerCase();
                const bare = String(model || '').trim().toLowerCase();
                if (!bare) {
                    return false;
                }
                if (subject === bare) {
                    return true;
                }
                const prov = String(provider || '').trim().toLowerCase();
                if (!prov) {
                    return false;
                }
                if (subject === prov + '/' + bare) {
                    return true;
                }
                return bare.startsWith(prov + '/') && subject === bare.slice(prov.length + 1);
            },

            rateLimitRuleMatchesProvider(rule, provider) {
                return this.rateLimitScope(rule) === 'provider'
                    && String(this.rateLimitSubject(rule)).toLowerCase() === String(provider || '').trim().toLowerCase();
            },

            rateLimitInspectorQualifiedModel() {
                const inspector = this.rateLimitInspector || {};
                const model = String(inspector.model || '');
                const provider = String(inspector.provider || '');
                if (!provider || model.toLowerCase().startsWith(provider + '/')) {
                    return model;
                }
                return provider + '/' + model;
            },

            rateLimitInspectorSections() {
                const inspector = this.rateLimitInspector || {};
                const rules = Array.isArray(this.rateLimits) ? this.rateLimits : [];
                const sections = [];
                if (inspector.kind === 'model') {
                    sections.push({
                        key: 'model',
                        title: 'Model limits',
                        scope: 'model',
                        subject: this.rateLimitInspectorQualifiedModel(),
                        hint: '',
                        items: rules.filter((rule) => this.rateLimitRuleMatchesModel(rule, inspector.provider, inspector.model))
                    });
                }
                sections.push({
                    key: 'provider',
                    title: 'Provider limits (' + inspector.provider + ')',
                    scope: 'provider',
                    subject: inspector.provider,
                    hint: inspector.kind === 'model' ? 'Shared by every model routed to this provider.' : '',
                    items: rules.filter((rule) => this.rateLimitRuleMatchesProvider(rule, inspector.provider))
                });
                sections.push({
                    key: 'global',
                    title: 'Global limits',
                    scope: 'user_path',
                    subject: '/',
                    hint: 'Root user-path rules throttle all traffic. Narrower user-path rules also apply, per consumer.',
                    items: rules.filter((rule) => this.rateLimitScope(rule) === 'user_path' && this.rateLimitSubject(rule) === '/')
                });
                return sections;
            },

            // rateLimitPressurePercent reports how close a rule is to its most
            // constrained cap (0..100), across requests, tokens, and in-flight.
            rateLimitPressurePercent(item) {
                if (this.rateLimitIsConcurrent(item)) {
                    return this.rateLimitUsagePercent(item.in_flight, item.max_requests);
                }
                return Math.max(
                    this.rateLimitUsagePercent(item.requests_used, item.max_requests),
                    this.rateLimitUsagePercent(item.tokens_used, item.max_tokens)
                );
            },

            rateLimitPressureStyle(item) {
                return '--rate-limit-pressure: ' + this.rateLimitPressurePercent(item) + '%';
            },

            rateLimitPressureClass(item) {
                const percent = this.rateLimitPressurePercent(item);
                if (percent >= 100) {
                    return 'rate-limit-pressure-row rate-limit-pressure-full';
                }
                if (percent >= 75) {
                    return 'rate-limit-pressure-row rate-limit-pressure-high';
                }
                return 'rate-limit-pressure-row';
            },

            // Gauge indicator states on the Models page: fully painted when the
            // subject has its own rules, half painted when only provider or
            // global rules throttle it, plain otherwise. Alpine evaluates the
            // class/label bindings several times per row on every render, so
            // results are memoized until the rules list is replaced.
            rateLimitGaugeCache: { rules: null, states: {} },

            rateLimitGaugeMemo(key, compute) {
                if (this.rateLimitGaugeCache.rules !== this.rateLimits) {
                    this.rateLimitGaugeCache = { rules: this.rateLimits, states: {} };
                }
                const states = this.rateLimitGaugeCache.states;
                if (!(key in states)) {
                    states[key] = compute();
                }
                return states[key];
            },

            rateLimitGaugeClassForModel(row) {
                const model = this.rateLimitInspectorModelID(row);
                const provider = String(row && row.provider_name || '').trim().toLowerCase();
                return this.rateLimitGaugeMemo('model:' + provider + '/' + model, () => {
                    const rules = Array.isArray(this.rateLimits) ? this.rateLimits : [];
                    if (rules.some((rule) => this.rateLimitRuleMatchesModel(rule, provider, model))) {
                        return 'table-action-btn-active';
                    }
                    if (rules.some((rule) => this.rateLimitRuleMatchesProvider(rule, provider)) || this.hasGlobalRateLimits()) {
                        return 'rate-limit-gauge-inherited';
                    }
                    return '';
                });
            },

            rateLimitGaugeClassForProvider(group) {
                const provider = String(group && group.provider_name || '').trim().toLowerCase();
                return this.rateLimitGaugeMemo('provider:' + provider, () => {
                    const rules = Array.isArray(this.rateLimits) ? this.rateLimits : [];
                    if (rules.some((rule) => this.rateLimitRuleMatchesProvider(rule, provider))) {
                        return 'table-action-btn-active';
                    }
                    return this.hasGlobalRateLimits() ? 'rate-limit-gauge-inherited' : '';
                });
            },

            hasGlobalRateLimits() {
                return this.rateLimitGaugeMemo('global', () => {
                    const rules = Array.isArray(this.rateLimits) ? this.rateLimits : [];
                    return rules.some((rule) => this.rateLimitScope(rule) === 'user_path' && this.rateLimitSubject(rule) === '/');
                });
            },

            rateLimitGaugeTitle(subject, gaugeClass) {
                const base = 'Rate limits for ' + subject;
                if (gaugeClass === 'table-action-btn-active') {
                    return base + ' (direct limits configured)';
                }
                if (gaugeClass) {
                    return base + ' (inherited limits apply)';
                }
                return base;
            },

            rateLimitInspectorSummary(item) {
                if (this.rateLimitIsConcurrent(item)) {
                    return this.formatRateLimitNumber(item.in_flight) + ' of ' + this.formatRateLimitNumber(item.max_requests) + ' in flight';
                }
                const parts = [];
                if (item.max_requests !== null && item.max_requests !== undefined) {
                    parts.push(this.formatRateLimitNumber(item.requests_used) + '/' + this.formatRateLimitNumber(item.max_requests) + ' req');
                }
                if (item.max_tokens !== null && item.max_tokens !== undefined) {
                    parts.push(this.formatRateLimitNumber(item.tokens_used) + '/' + this.formatRateLimitNumber(item.max_tokens) + ' tok');
                }
                return parts.join(' · ');
            },

            openRateLimitFormFromInspector(scope, subject, item) {
                this.rateLimitInspectorOpen = false;
                this.rateLimitFormReturnToInspector = true;
                this.openRateLimitForm(item || undefined);
                if (!item) {
                    this.rateLimitForm.scope = scope;
                    this.rateLimitForm.subject = subject;
                }
            }
        };
    }

    global.dashboardRateLimitsModule = dashboardRateLimitsModule;
})(typeof window !== 'undefined' ? window : globalThis);
