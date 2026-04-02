(function(global) {
    function dashboardExecutionPlansModule() {
        return {
            executionPlans: [],
            executionPlanVersionsByID: {},
            executionPlanVersionRequests: {},
            executionPlansAvailable: true,
            executionPlansLoading: false,
            executionPlanRuntimeConfig: {},
            executionPlanError: '',
            executionPlanNotice: '',
            executionPlanFilter: '',
            executionPlanFormOpen: false,
            executionPlanSubmitting: false,
            executionPlanDeactivatingID: '',
            executionPlanFormError: '',
            executionPlanFormHydrated: false,
            executionPlanHydratedScope: {
                scope_provider: '',
                scope_model: '',
                scope_user_path: ''
            },
            guardrailRefs: [],
            executionPlanForm: {
                scope_provider: '',
                scope_model: '',
                scope_user_path: '',
                name: '',
                description: '',
                features: {
                    cache: true,
                    audit: true,
                    usage: true,
                    guardrails: false,
                    fallback: false
                },
                guardrails: []
            },

            defaultExecutionPlanForm() {
                return {
                    scope_provider: '',
                    scope_model: '',
                    scope_user_path: '',
                    name: '',
                    description: '',
                    features: {
                        cache: true,
                        audit: true,
                        usage: true,
                        guardrails: false,
                        fallback: false
                    },
                    guardrails: []
                };
            },

	            executionPlanRuntimeConfigKeys() {
	                return [
	                    'FEATURE_FALLBACK_MODE',
	                    'LOGGING_ENABLED',
	                    'USAGE_ENABLED',
	                    'GUARDRAILS_ENABLED',
	                    'CACHE_ENABLED',
	                    'REDIS_URL',
	                    'SEMANTIC_CACHE_ENABLED'
	                ];
	            },

	            executionPlanRuntimeFlag(name) {
	                const value = this.executionPlanRuntimeConfig && this.executionPlanRuntimeConfig[name];
	                return String(value || '').trim().toLowerCase();
	            },

	            executionPlanRuntimeBooleanFlag(name, defaultValue) {
	                const value = this.executionPlanRuntimeFlag(name);
	                if (value === '') {
	                    return !!defaultValue;
	                }
	                return value === 'on' || value === 'true' || value === '1';
	            },

	            executionPlanCacheVisible() {
	                const explicit = this.executionPlanRuntimeFlag('CACHE_ENABLED');
	                if (explicit !== '') {
	                    return this.executionPlanRuntimeBooleanFlag('CACHE_ENABLED', false);
	                }
	                const redis = this.executionPlanRuntimeFlag('REDIS_URL');
	                const semantic = this.executionPlanRuntimeFlag('SEMANTIC_CACHE_ENABLED');
	                if (redis === '' && semantic === '') {
	                    return true;
	                }
	                return this.executionPlanRuntimeBooleanFlag('REDIS_URL', false)
	                    || this.executionPlanRuntimeBooleanFlag('SEMANTIC_CACHE_ENABLED', false);
	            },

	            executionPlanAuditVisible() {
	                return this.executionPlanRuntimeBooleanFlag('LOGGING_ENABLED', true);
	            },

	            executionPlanUsageVisible() {
	                return this.executionPlanRuntimeBooleanFlag('USAGE_ENABLED', true);
	            },

	            executionPlanGuardrailsVisible() {
	                return this.executionPlanRuntimeBooleanFlag('GUARDRAILS_ENABLED', true);
	            },

	            executionPlanFeatureCaps() {
	                return {
	                    cache: this.executionPlanCacheVisible(),
	                    audit: this.executionPlanAuditVisible(),
	                    usage: this.executionPlanUsageVisible(),
	                    guardrails: this.executionPlanGuardrailsVisible()
	                };
	            },

	            executionPlanReadFeatureFlag(raw, key, defaultValue) {
	                if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
	                    return defaultValue;
	                }
	                const capitalizedKey = key.charAt(0).toUpperCase() + key.slice(1);
	                for (const candidate of [key, capitalizedKey]) {
	                    if (Object.prototype.hasOwnProperty.call(raw, candidate) && raw[candidate] !== null && raw[candidate] !== undefined) {
	                        return raw[candidate];
	                    }
	                }
	                return defaultValue;
	            },

	            executionPlanHasDefinedFeatureFlag(raw, key) {
	                if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
	                    return false;
	                }
	                const capitalizedKey = key.charAt(0).toUpperCase() + key.slice(1);
	                return [key, capitalizedKey].some((candidate) => {
	                    return Object.prototype.hasOwnProperty.call(raw, candidate)
	                        && raw[candidate] !== null
	                        && raw[candidate] !== undefined;
	                });
	            },

	            executionPlanNormalizedFeatures(raw) {
	                return {
	                    cache: !!this.executionPlanReadFeatureFlag(raw, 'cache', false),
	                    audit: !!this.executionPlanReadFeatureFlag(raw, 'audit', false),
	                    usage: !!this.executionPlanReadFeatureFlag(raw, 'usage', false),
	                    guardrails: !!this.executionPlanReadFeatureFlag(raw, 'guardrails', false),
	                    fallback: this.executionPlanReadFeatureFlag(raw, 'fallback', true) !== false
	                };
	            },

	            executionPlanApplyGlobalFeatureCaps(raw) {
	                const features = this.executionPlanNormalizedFeatures(raw);
	                const caps = this.executionPlanFeatureCaps();
	                return {
	                    cache: features.cache && caps.cache,
	                    audit: features.audit && caps.audit,
	                    usage: features.usage && caps.usage,
	                    guardrails: features.guardrails && caps.guardrails,
	                    fallback: features.fallback
	                };
	            },

	            executionPlanFailoverVisible() {
	                const mode = this.executionPlanRuntimeFlag('FEATURE_FALLBACK_MODE');
	                return mode !== '' && mode !== 'off';
	            },

            executionPlanFallbackLabel(source) {
                return this.executionPlanSourceFeatures(source).fallback ? 'On' : 'Off';
            },

            defaultExecutionPlanGuardrailStep(step) {
                return {
                    ref: '',
                    step: Number.isFinite(step) ? step : 10
                };
            },

            parseExecutionPlanGuardrailStep(rawStep) {
                const trimmedStep = rawStep === null || rawStep === undefined ? '' : String(rawStep).trim();
                if (trimmedStep === '') {
                    return Number.NaN;
                }
                const parsedStep = Number(trimmedStep);
                return Number.isFinite(parsedStep) ? parsedStep : Number.NaN;
            },

            get filteredExecutionPlans() {
                if (!this.executionPlanFilter) {
                    return this.executionPlans;
                }
                const filter = this.executionPlanFilter.toLowerCase();
                return this.executionPlans.filter((plan) => {
                    const fields = [
                        plan.name,
                        plan.description,
                        plan.scope_display,
                        plan.scope_type,
                        plan.scope && plan.scope.scope_provider,
                        plan.scope && plan.scope.scope_model,
                        plan.scope && plan.scope.scope_user_path,
                        plan.plan_hash,
                        ...(Array.isArray(plan.plan_payload && plan.plan_payload.guardrails)
                            ? plan.plan_payload.guardrails.map((step) => step.ref)
                            : [])
                    ];
                    return fields.some((value) => String(value || '').toLowerCase().includes(filter));
                });
            },

            executionPlanProviderOptions() {
                const options = new Set();
                const preservedProvider = String(this.executionPlanHydratedScope && this.executionPlanHydratedScope.scope_provider || '').trim();
                if (preservedProvider) {
                    options.add(preservedProvider);
                }
                const models = Array.isArray(this.models) ? this.models : [];
                models.forEach((model) => {
                    const providerType = String(model && model.provider_type || '').trim();
                    if (providerType) {
                        options.add(providerType);
                    }
                });
                return [...options].sort();
            },

            executionPlanModelOptions(providerType) {
                const wantedProvider = String(providerType || '').trim();
                const options = new Set();
                const preservedProvider = String(this.executionPlanHydratedScope && this.executionPlanHydratedScope.scope_provider || '').trim();
                const preservedModel = String(this.executionPlanHydratedScope && this.executionPlanHydratedScope.scope_model || '').trim();
                if (wantedProvider && wantedProvider === preservedProvider && preservedModel) {
                    options.add(preservedModel);
                }
                const models = Array.isArray(this.models) ? this.models : [];
                models.forEach((model) => {
                    if (wantedProvider && String(model && model.provider_type || '').trim() !== wantedProvider) {
                        return;
                    }
                    const modelID = String(model && model.model && model.model.id || '').trim();
                    if (modelID) {
                        options.add(modelID);
                    }
                });
                return [...options].sort();
            },

            planScopeTypeLabel(plan) {
                const scopeType = String(plan && plan.scope_type || '').trim();
                if (scopeType === 'provider_model') return 'Provider + Model';
                if (scopeType === 'provider_model_path') return 'Provider + Model + Path';
                if (scopeType === 'provider_path') return 'Provider + Path';
                if (scopeType === 'path') return 'Path';
                if (scopeType === 'provider') return 'Provider';
                return 'Global';
            },

            planScopeLabel(plan) {
                return String(plan && plan.scope_display || 'global').trim() || 'global';
            },

            workflowDisplayName(plan) {
                const explicitName = String(plan && plan.name || '').trim();
                if (explicitName) {
                    return explicitName;
                }
                const scopeLabel = this.planScopeLabel(plan);
                if (scopeLabel === 'global') {
                    return 'All models';
                }
                return scopeLabel;
            },

            executionPlanCurrentScope() {
                const form = this.executionPlanForm || this.defaultExecutionPlanForm();
                const provider = String(form.scope_provider || '').trim();
                const userPath = this.normalizeExecutionPlanScopeUserPath(form.scope_user_path);
                return {
                    scope_provider: provider,
                    scope_model: provider ? String(form.scope_model || '').trim() : '',
                    scope_user_path: userPath
                };
            },

            executionPlanScopeType(scope) {
                const provider = String(scope && scope.scope_provider || '').trim();
                const model = provider ? String(scope && scope.scope_model || '').trim() : '';
                const userPath = this.normalizeExecutionPlanScopeUserPath(scope && scope.scope_user_path);
                if (!provider && !userPath) return 'global';
                if (!provider && userPath) return 'path';
                if (!model && !userPath) return 'provider';
                if (!model && userPath) return 'provider_path';
                if (userPath) return 'provider_model_path';
                return 'provider_model';
            },

            executionPlanScopeDisplay(scope) {
                const provider = String(scope && scope.scope_provider || '').trim();
                const model = provider ? String(scope && scope.scope_model || '').trim() : '';
                const userPath = this.normalizeExecutionPlanScopeUserPath(scope && scope.scope_user_path);
                const scopeType = this.executionPlanScopeType({
                    scope_provider: provider,
                    scope_model: model,
                    scope_user_path: userPath
                });
                if (scopeType === 'global') return 'global';
                if (scopeType === 'path') return userPath;
                if (scopeType === 'provider') return provider;
                if (scopeType === 'provider_path') return provider + ' @ ' + userPath;
                if (scopeType === 'provider_model_path') return provider + '/' + model + ' @ ' + userPath;
                return provider + '/' + model;
            },

            executionPlanScopeMatches(plan, scope) {
                const normalized = scope || { scope_provider: '', scope_model: '', scope_user_path: '' };
                const provider = String(plan && plan.scope && plan.scope.scope_provider || '').trim();
                const model = provider ? String(plan && plan.scope && plan.scope.scope_model || '').trim() : '';
                const userPath = this.normalizeExecutionPlanScopeUserPath(plan && plan.scope && plan.scope.scope_user_path);
                return provider === String(normalized.scope_provider || '').trim()
                    && model === String(normalized.scope_model || '').trim()
                    && userPath === this.normalizeExecutionPlanScopeUserPath(normalized.scope_user_path);
            },

            executionPlanActiveScopeMatch() {
                const scope = this.executionPlanCurrentScope();
                const hasScopedSelection = scope.scope_provider !== ''
                    || scope.scope_model !== ''
                    || scope.scope_user_path !== '';
                if (!hasScopedSelection && !this.executionPlanFormHydrated) {
                    return null;
                }
                const plans = Array.isArray(this.executionPlans) ? this.executionPlans : [];
                return plans.find((plan) => this.executionPlanScopeMatches(plan, scope)) || null;
            },

            executionPlanSubmitMode() {
                return this.executionPlanActiveScopeMatch() ? 'save' : 'create';
            },

            executionPlanSubmitLabel() {
                return this.executionPlanSubmitMode() === 'save' ? 'Save' : 'Create';
            },

            executionPlanSubmittingLabel() {
                return this.executionPlanSubmitMode() === 'save' ? 'Saving...' : 'Creating...';
            },

            executionPlanPreview() {
                const form = this.executionPlanForm || this.defaultExecutionPlanForm();
                const scope = this.executionPlanCurrentScope();
                const rawFeatures = this.executionPlanNormalizedFeatures(form.features || {});
                const features = this.executionPlanApplyGlobalFeatureCaps(rawFeatures);
                features.fallback = rawFeatures.fallback;
                const guardrailsEnabled = !!features.guardrails;
                const guardrails = guardrailsEnabled ? this.executionPlanSourceGuardrails(form) : [];
                const scopeType = this.executionPlanScopeType(scope);
                const scopeDisplay = this.executionPlanScopeDisplay(scope);

                return {
                    id: 'draft-workflow-preview',
                    scope_type: scopeType,
                    scope_display: scopeDisplay,
                    scope: {
                        scope_provider: scope.scope_provider,
                        scope_model: scope.scope_model,
                        ...(scope.scope_user_path ? { scope_user_path: scope.scope_user_path } : {})
                    },
                    name: String(form.name || '').trim(),
                    description: String(form.description || '').trim(),
                    plan_payload: {
                        schema_version: 1,
                        features: {
                            cache: !!features.cache,
                            audit: !!features.audit,
                            usage: !!features.usage,
                            guardrails: guardrailsEnabled,
                            fallback: !!features.fallback
                        },
                        guardrails
                    }
                };
            },

	            executionPlanSourceFeatures(source) {
	                const raw = source && source.plan_payload && source.plan_payload.features
	                    ? source.plan_payload.features
	                    : source && source.features
	                        ? source.features
	                        : {};
	                const effective = source && source.effective_features && typeof source.effective_features === 'object' && !Array.isArray(source.effective_features)
	                    ? source.effective_features
	                    : null;
	                const features = this.executionPlanApplyGlobalFeatureCaps(effective || raw);
	                return {
	                    ...features,
	                    fallback: this.executionPlanNormalizedFeatures(raw).fallback
	                };
	            },

            executionPlanEntryFeatures(entry) {
                const raw = entry && entry.data && entry.data.execution_features;
                if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
                    return null;
                }
                return this.executionPlanNormalizedFeatures(raw);
            },

            executionPlanSourceGuardrails(source) {
                const raw = Array.isArray(source && source.plan_payload && source.plan_payload.guardrails)
                    ? source.plan_payload.guardrails
                    : Array.isArray(source && source.guardrails)
                        ? source.guardrails
                        : [];
                return raw
                    .map((step) => ({
                        ref: String(step && step.ref || '').trim(),
                        step: this.parseExecutionPlanGuardrailStep(step && step.step)
                    }))
                    .filter((step) => Number.isInteger(step.step) && step.step >= 0);
            },

            canDeactivateExecutionPlan(plan) {
                return String(plan && plan.scope_type || '').trim() !== 'global';
            },

            executionPlanEditorScrollTarget() {
                if (!global.document || typeof global.document.querySelector !== 'function') {
                    return null;
                }
                return global.document.querySelector('.execution-plan-editor');
            },

            scrollExecutionPlanFormIntoView() {
                const scroll = () => {
                    const editor = this.executionPlanEditorScrollTarget();
                    if (editor && typeof editor.scrollIntoView === 'function') {
                        editor.scrollIntoView({ behavior: 'smooth', block: 'start' });
                    }
                };
                if (typeof global.requestAnimationFrame === 'function') {
                    global.requestAnimationFrame(scroll);
                    return;
                }
                scroll();
            },

	            planGuardrails(plan) {
	                if (!this.executionPlanSourceFeatures(plan).guardrails) {
	                    return [];
	                }
	                return Array.isArray(plan && plan.plan_payload && plan.plan_payload.guardrails)
	                    ? plan.plan_payload.guardrails
	                    : [];
	            },

            shortHash(value) {
                const hash = String(value || '').trim();
                if (!hash) return '\u2014';
                if (hash.length <= 14) return hash;
                return hash.slice(0, 12) + '\u2026';
            },

            openExecutionPlanCreate(plan) {
                this.executionPlanFormOpen = true;
                this.executionPlanSubmitting = false;
                this.executionPlanFormError = '';
                this.executionPlanNotice = '';

                if (!plan) {
                    this.executionPlanFormHydrated = false;
                    this.executionPlanHydratedScope = {
                        scope_provider: '',
                        scope_model: '',
                        scope_user_path: ''
                    };
                    this.executionPlanForm = this.defaultExecutionPlanForm();
                    this.scrollExecutionPlanFormIntoView();
                    return;
                }

                this.executionPlanFormHydrated = true;
                this.executionPlanHydratedScope = {
                    scope_provider: String(plan.scope && plan.scope.scope_provider || '').trim(),
                    scope_model: String(plan.scope && plan.scope.scope_model || '').trim(),
                    scope_user_path: String(plan.scope && plan.scope.scope_user_path || '').trim()
                };
                const features = this.executionPlanSourceFeatures(plan);
                const guardrails = this.executionPlanSourceGuardrails(plan);
                this.executionPlanForm = {
                    scope_provider: String(plan.scope && plan.scope.scope_provider || ''),
                    scope_model: String(plan.scope && plan.scope.scope_model || ''),
                    scope_user_path: String(plan.scope && plan.scope.scope_user_path || ''),
                    name: String(plan.name || ''),
                    description: String(plan.description || ''),
                    features: {
                        cache: !!features.cache,
                        audit: !!features.audit,
                        usage: !!features.usage,
                        guardrails: !!features.guardrails,
                        fallback: !!features.fallback
                    },
                    guardrails: guardrails.map((step) => ({
                        ref: String(step && step.ref || ''),
                        step: Number.isFinite(step && step.step) ? step.step : 10
                    }))
                };
                this.scrollExecutionPlanFormIntoView();
            },

            closeExecutionPlanForm() {
                this.executionPlanFormOpen = false;
                this.executionPlanSubmitting = false;
                this.executionPlanFormError = '';
                this.executionPlanFormHydrated = false;
                this.executionPlanHydratedScope = {
                    scope_provider: '',
                    scope_model: '',
                    scope_user_path: ''
                };
                this.executionPlanForm = this.defaultExecutionPlanForm();
            },

            setExecutionPlanProvider(provider) {
                this.executionPlanForm.scope_provider = String(provider || '').trim();
                if (!this.executionPlanForm.scope_provider) {
                    this.executionPlanForm.scope_provider = '';
                    this.executionPlanForm.scope_model = '';
                    return;
                }
                const modelOptions = this.executionPlanModelOptions(this.executionPlanForm.scope_provider);
                if (!modelOptions.includes(String(this.executionPlanForm.scope_model || '').trim())) {
                    this.executionPlanForm.scope_model = '';
                }
            },

            addExecutionPlanGuardrailStep() {
                const steps = Array.isArray(this.executionPlanForm.guardrails) ? this.executionPlanForm.guardrails : [];
                const nextStep = steps.reduce((maxStep, step) => {
                    const parsed = Number(step && step.step);
                    return Number.isFinite(parsed) ? Math.max(maxStep, parsed) : maxStep;
                }, 0) + 10;
                this.executionPlanForm.guardrails.push(this.defaultExecutionPlanGuardrailStep(nextStep));
            },

            removeExecutionPlanGuardrailStep(index) {
                if (!Array.isArray(this.executionPlanForm.guardrails)) return;
                this.executionPlanForm.guardrails.splice(index, 1);
            },

            executionPlanScopeUserPathValidationError(value) {
                const trimmed = String(value || '').trim();
                if (!trimmed) {
                    return '';
                }
                const raw = trimmed.startsWith('/') ? trimmed : '/' + trimmed;
                const segments = raw.split('/');
                for (const part of segments) {
                    const segment = String(part || '').trim();
                    if (!segment) {
                        continue;
                    }
                    if (segment === '.' || segment === '..') {
                        return 'User path cannot contain "." or ".." segments.';
                    }
                    if (segment.includes(':')) {
                        return 'User path cannot contain ":" segments.';
                    }
                }
                return '';
            },

            normalizeExecutionPlanScopeUserPath(value) {
                if (this.executionPlanScopeUserPathValidationError(value)) {
                    return '';
                }
                const trimmed = String(value || '').trim();
                if (!trimmed) {
                    return '';
                }
                const raw = trimmed.startsWith('/') ? trimmed : '/' + trimmed;
                const segments = raw.split('/');
                const canonical = [];
                for (const part of segments) {
                    const segment = String(part || '').trim();
                    if (!segment) {
                        continue;
                    }
                    canonical.push(segment);
                }
                if (!canonical.length) {
                    return '/';
                }
                return '/' + canonical.join('/');
            },

            buildExecutionPlanRequest() {
                const form = this.executionPlanForm || this.defaultExecutionPlanForm();
                const provider = String(form.scope_provider || '').trim();
                const model = provider ? String(form.scope_model || '').trim() : '';
                const userPath = this.normalizeExecutionPlanScopeUserPath(form.scope_user_path);
                const rawFeatures = this.executionPlanNormalizedFeatures(form.features || {});
                const features = this.executionPlanApplyGlobalFeatureCaps(rawFeatures);
                const activeScopeMatch = this.executionPlanActiveScopeMatch();
                const activeScopeFeatures = activeScopeMatch && activeScopeMatch.plan_payload && activeScopeMatch.plan_payload.features;
                const activeScopeHasFallback = this.executionPlanHasDefinedFeatureFlag(activeScopeFeatures, 'fallback');
                const preservedActiveFallback = activeScopeHasFallback
                    ? this.executionPlanReadFeatureFlag(activeScopeFeatures, 'fallback', true) !== false
                    : null;
                const hydratedScope = this.executionPlanHydratedScope || {
                    scope_provider: '',
                    scope_model: '',
                    scope_user_path: ''
                };
                const sameHydratedScope = String(hydratedScope.scope_provider || '').trim() === provider
                    && String(hydratedScope.scope_model || '').trim() === model
                    && this.normalizeExecutionPlanScopeUserPath(hydratedScope.scope_user_path) === this.normalizeExecutionPlanScopeUserPath(userPath);
                const includeFallback = this.executionPlanFailoverVisible()
                    || (!!this.executionPlanFormHydrated
                        && sameHydratedScope
                        && Object.prototype.hasOwnProperty.call(rawFeatures, 'fallback'))
                    || (!this.executionPlanFormHydrated
                        && !!activeScopeMatch
                        && activeScopeHasFallback);

                const guardrails = !!features.guardrails
                    ? (Array.isArray(form.guardrails) ? form.guardrails : []).map((step) => {
                        return {
                            ref: String(step && step.ref || '').trim(),
                            step: this.parseExecutionPlanGuardrailStep(step && step.step)
                        };
                    })
                    : [];

                const payload = {
                    scope_provider: provider,
                    scope_model: model,
                    ...(userPath ? { scope_user_path: userPath } : {}),
                    name: String(form.name || '').trim(),
                    description: String(form.description || '').trim(),
                    plan_payload: {
                        schema_version: 1,
                        features: {
                            cache: !!features.cache,
                            audit: !!features.audit,
                            usage: !!features.usage,
                            guardrails: !!features.guardrails
                        },
                        guardrails
                    }
                };
                if (includeFallback) {
                    payload.plan_payload.features.fallback = !this.executionPlanFailoverVisible()
                        && !this.executionPlanFormHydrated
                        && !!activeScopeMatch
                        && activeScopeHasFallback
                        ? preservedActiveFallback
                        : !!rawFeatures.fallback;
                }

                return payload;
            },

            async fetchExecutionPlanRuntimeConfig() {
                const controller = typeof AbortController === 'function' ? new AbortController() : null;
                const timeoutID = controller && typeof setTimeout === 'function'
                    ? setTimeout(() => controller.abort(), 10000)
                    : null;
                try {
                    const request = { headers: this.headers() };
                    if (controller) {
                        request.signal = controller.signal;
                    }
                    const res = await fetch('/admin/api/v1/dashboard/config', request);
                    if (!this.handleFetchResponse(res, 'dashboard config')) {
                        this.executionPlanRuntimeConfig = {};
                        return;
                    }
                    const payload = await res.json();
                    const next = {};
                    const allowedKeys = this.executionPlanRuntimeConfigKeys();
                    for (const key of allowedKeys) {
                        if (payload && typeof payload === 'object' && !Array.isArray(payload) && payload[key] !== undefined && payload[key] !== null) {
                            next[key] = String(payload[key]).trim();
                        }
	                    }
	                    this.executionPlanRuntimeConfig = next;
	                    if (typeof this.fetchCacheOverview === 'function') {
	                        if (this.executionPlanCacheVisible()) {
	                            this.fetchCacheOverview();
	                        } else {
	                            this.cacheOverview = {
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
	                            };
	                            if (typeof this.renderChart === 'function') {
	                                this.renderChart();
	                            }
	                        }
	                    }
	                } catch (e) {
	                    console.error('Failed to fetch dashboard config:', e);
	                    this.executionPlanRuntimeConfig = {};
                } finally {
                    if (timeoutID !== null && typeof clearTimeout === 'function') {
                        clearTimeout(timeoutID);
                    }
                }
            },

            validateExecutionPlanRequest(payload) {
                const preservedProvider = String(this.executionPlanHydratedScope && this.executionPlanHydratedScope.scope_provider || '').trim();
                const preservedModel = String(this.executionPlanHydratedScope && this.executionPlanHydratedScope.scope_model || '').trim();

                if (payload.scope_provider) {
                    const providers = this.executionPlanProviderOptions();
                    if (!providers.includes(payload.scope_provider) && payload.scope_provider !== preservedProvider) {
                        return 'Choose a registered provider.';
                    }
                }
                if (payload.scope_model && !payload.scope_provider) {
                    return 'Model selection requires a provider.';
                }
                if (payload.scope_model) {
                    const models = this.executionPlanModelOptions(payload.scope_provider);
                    const isPreservedModel = payload.scope_provider === preservedProvider && payload.scope_model === preservedModel;
                    if (!models.includes(payload.scope_model) && !isPreservedModel) {
                        return 'Choose a registered model for the selected provider.';
                    }
                }
                const userPathError = this.executionPlanScopeUserPathValidationError(payload.scope_user_path);
                if (userPathError) {
                    return userPathError;
                }
                const features = payload.plan_payload && payload.plan_payload.features ? payload.plan_payload.features : {};
                const guardrails = Array.isArray(payload.plan_payload && payload.plan_payload.guardrails)
                    ? payload.plan_payload.guardrails
                    : [];
                if (!features.guardrails) {
                    return '';
                }

                const seen = new Set();
                for (const step of guardrails) {
                    if (!step.ref) {
                        return 'Each guardrail step needs a guardrail ref.';
                    }
                    if (!Number.isInteger(step.step) || step.step < 0) {
                        return 'Each guardrail step must use a non-negative integer step number.';
                    }
                    if (seen.has(step.ref)) {
                        return 'Each guardrail ref may appear only once in a plan.';
                    }
                    seen.add(step.ref);
                }

                return '';
            },

            async executionPlanResponseMessage(res, fallback) {
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

            async fetchExecutionPlans() {
                this.executionPlansLoading = true;
                this.executionPlanError = '';
                const controller = typeof AbortController === 'function' ? new AbortController() : null;
                const timeoutID = controller && typeof setTimeout === 'function'
                    ? setTimeout(() => controller.abort(), 10000)
                    : null;
                try {
                    const request = { headers: this.headers() };
                    if (controller) {
                        request.signal = controller.signal;
                    }
                    const res = await fetch('/admin/api/v1/execution-plans', request);
                    if (res.status === 503) {
                        this.executionPlansAvailable = false;
                        this.executionPlans = [];
                        return;
                    }
                    this.executionPlansAvailable = true;
                    if (!this.handleFetchResponse(res, 'workflows')) {
                        this.executionPlans = [];
                        return;
                    }
                    const payload = await res.json();
                    this.executionPlans = Array.isArray(payload) ? payload : [];
                    this.cacheExecutionPlanVersions(this.executionPlans);
                } catch (e) {
                    console.error('Failed to fetch workflows:', e);
                    this.executionPlans = [];
                    this.executionPlanError = e && e.name === 'AbortError'
                        ? 'Loading workflows timed out.'
                        : 'Unable to load workflows.';
                } finally {
                    if (timeoutID !== null && typeof clearTimeout === 'function') {
                        clearTimeout(timeoutID);
                    }
                    this.executionPlansLoading = false;
                }
            },

            async fetchExecutionPlanGuardrails() {
                try {
                    const res = await fetch('/admin/api/v1/execution-plans/guardrails', { headers: this.headers() });
                    if (!this.handleFetchResponse(res, 'workflow guardrails')) {
                        this.guardrailRefs = [];
                        return;
                    }
                    const payload = await res.json();
                    this.guardrailRefs = Array.isArray(payload) ? payload : [];
                } catch (e) {
                    console.error('Failed to fetch workflow guardrails:', e);
                    this.guardrailRefs = [];
                }
            },

            async fetchExecutionPlansPage() {
                await Promise.all([
                    this.fetchExecutionPlanRuntimeConfig(),
                    this.fetchExecutionPlans(),
                    this.fetchExecutionPlanGuardrails()
                ]);
            },

            cacheExecutionPlanVersion(plan) {
                const planID = String(plan && plan.id || '').trim();
                if (!planID) {
                    return null;
                }
                this.executionPlanVersionsByID = {
                    ...(this.executionPlanVersionsByID || {}),
                    [planID]: plan
                };
                return plan;
            },

            cacheExecutionPlanVersions(plans) {
                if (!Array.isArray(plans) || plans.length === 0) {
                    return;
                }
                const next = {
                    ...(this.executionPlanVersionsByID || {})
                };
                plans.forEach((plan) => {
                    const planID = String(plan && plan.id || '').trim();
                    if (planID) {
                        next[planID] = plan;
                    }
                });
                this.executionPlanVersionsByID = next;
            },

            cacheMissingExecutionPlanVersion(planID) {
                const normalizedID = String(planID || '').trim();
                if (!normalizedID) {
                    return;
                }
                this.executionPlanVersionsByID = {
                    ...(this.executionPlanVersionsByID || {}),
                    [normalizedID]: null
                };
            },

            executionPlanVersionCacheHas(planID) {
                return Object.prototype.hasOwnProperty.call(this.executionPlanVersionsByID || {}, String(planID || '').trim());
            },

            executionPlanVersionByID(planID) {
                const normalizedID = String(planID || '').trim();
                if (!normalizedID) {
                    return null;
                }
                if (this.executionPlanVersionCacheHas(normalizedID)) {
                    return this.executionPlanVersionsByID[normalizedID];
                }
                const plans = Array.isArray(this.executionPlans) ? this.executionPlans : [];
                const activeMatch = plans.find((plan) => String(plan && plan.id || '').trim() === normalizedID) || null;
                if (activeMatch) {
                    this.cacheExecutionPlanVersion(activeMatch);
                }
                return activeMatch;
            },

            async fetchExecutionPlanVersion(planID) {
                const normalizedID = String(planID || '').trim();
                if (!normalizedID) {
                    return null;
                }
                if (this.executionPlanVersionCacheHas(normalizedID)) {
                    return this.executionPlanVersionsByID[normalizedID];
                }
                if (this.executionPlanVersionRequests && this.executionPlanVersionRequests[normalizedID]) {
                    return this.executionPlanVersionRequests[normalizedID];
                }

	                const request = (async () => {
	                    const controller = typeof AbortController === 'function' ? new AbortController() : null;
	                    const timeoutID = controller && typeof setTimeout === 'function'
	                        ? setTimeout(() => controller.abort(), 10000)
	                        : null;
	                    try {
	                        const options = { headers: this.headers() };
	                        if (controller) {
	                            options.signal = controller.signal;
	                        }
	                        const res = await fetch('/admin/api/v1/execution-plans/' + encodeURIComponent(normalizedID), options);
	                        if (res.status === 404) {
	                            this.cacheMissingExecutionPlanVersion(normalizedID);
	                            return null;
                        }
                        if (res.status === 401) {
                            if (typeof this.handleFetchResponse === 'function') {
                                this.handleFetchResponse(res, 'workflow');
                            }
                            return null;
                        }
                        if (!res.ok) {
                            if (typeof this.handleFetchResponse === 'function') {
                                this.handleFetchResponse(res, 'workflow');
                            }
                            return null;
                        }

                        const payload = await res.json();
                        if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
                            this.cacheMissingExecutionPlanVersion(normalizedID);
                            return null;
	                        }
	                        return this.cacheExecutionPlanVersion(payload);
	                    } catch (e) {
	                        if (e && e.name === 'AbortError') {
	                            return null;
	                        }
	                        console.error('Failed to fetch workflow version:', e);
	                        return null;
	                    } finally {
	                        if (timeoutID !== null && typeof clearTimeout === 'function') {
	                            clearTimeout(timeoutID);
	                        }
	                        if (this.executionPlanVersionRequests) {
	                            delete this.executionPlanVersionRequests[normalizedID];
	                        }
                    }
                })();

                this.executionPlanVersionRequests = {
                    ...(this.executionPlanVersionRequests || {}),
                    [normalizedID]: request
                };
                return request;
            },

            async prefetchAuditExecutionPlans(entries) {
                const uniquePlanIDs = [...new Set(
                    (Array.isArray(entries) ? entries : [])
                        .map((entry) => String(entry && entry.execution_plan_version_id || '').trim())
                        .filter(Boolean)
                )];
                if (uniquePlanIDs.length === 0) {
                    return;
                }
                await Promise.all(uniquePlanIDs.map((planID) => this.fetchExecutionPlanVersion(planID)));
            },

            auditEntryExecutionPlan(entry) {
                const planID = String(entry && entry.execution_plan_version_id || '').trim();
                if (!planID) {
                    return null;
                }
                return this.executionPlanVersionByID(planID);
            },

            async submitExecutionPlanForm() {
                if (this.executionPlanSubmitting) {
                    return;
                }
                this.executionPlanFormError = '';
                this.executionPlanNotice = '';

                const payload = this.buildExecutionPlanRequest();
                const validationError = this.validateExecutionPlanRequest(payload);
                if (validationError) {
                    this.executionPlanFormError = validationError;
                    return;
                }

                this.executionPlanSubmitting = true;
                try {
                    const res = await fetch('/admin/api/v1/execution-plans', {
                        method: 'POST',
                        headers: this.headers(),
                        body: JSON.stringify(payload)
                    });

                    if (res.status === 401) {
                        this.handleFetchResponse(res, 'create workflow');
                        return;
                    }
                    if (!res.ok) {
                        this.executionPlanFormError = await this.executionPlanResponseMessage(res, 'Unable to create workflow.');
                        return;
                    }

                    this.executionPlanNotice = 'Workflow created and activated.';
                    this.closeExecutionPlanForm();
                    await this.fetchExecutionPlansPage();
                } catch (e) {
                    console.error('Failed to create workflow:', e);
                    this.executionPlanFormError = 'Unable to create workflow.';
                } finally {
                    this.executionPlanSubmitting = false;
                }
            },

            // ─── Execution Pipeline helpers ───

            epHasGuardrails(source) {
                return !!this.executionPlanSourceFeatures(source).guardrails;
            },

            epHasCache(source) {
                return !!this.executionPlanSourceFeatures(source).cache;
            },

            epHasAudit(source) {
                return !!this.executionPlanSourceFeatures(source).audit;
            },

            epHasUsage(source) {
                return !!this.executionPlanSourceFeatures(source).usage;
            },

            epHasAsync(source) {
                const f = this.executionPlanSourceFeatures(source);
                return !!(f.audit || f.usage);
            },

            epGuardrailLabel(source) {
                const count = this.executionPlanSourceGuardrails(source).length;
                if (count === 0) return '';
                return count === 1 ? '1 step' : count + ' steps';
            },

            epAiLabel(source, runtime) {
                if (runtime && runtime.provider) return runtime.provider;
                const provider = source && source.scope && source.scope.scope_provider;
                return provider || 'AI';
            },

	            epAiSublabel(source, runtime) {
	                if (runtime && runtime.model) return runtime.model;
	                return source && source.scope && source.scope.scope_model || null;
	            },

            executionPlanChartWorkflowID(source, entry) {
                const sourceID = String(source && source.id || '').trim();
                if (sourceID) {
                    return sourceID;
                }
                const entryID = String(entry && entry.execution_plan_version_id || '').trim();
                return entryID || null;
            },

	            executionPlanChartModel(source, runtime, options) {
	                const config = options || {};
	                const features = config.features && typeof config.features === 'object' && !Array.isArray(config.features)
                    ? this.executionPlanNormalizedFeatures(config.features)
                    : this.executionPlanSourceFeatures(source);
	                const forceAudit = !!config.forceAudit;
	                const showGuardrails = !!features.guardrails;
	                const showUsage = !!features.usage;
	                const showAudit = forceAudit || !!features.audit;
	                const showAsync = !!config.forceAsync || !!(showUsage || showAudit);
                    const workflowID = this.executionPlanChartWorkflowID(source, config.entry);
	                return {
	                    showGuardrails,
	                    guardrailLabel: showGuardrails ? this.epGuardrailLabel(source) : '',
	                    showCache: !!config.forceCache || !!features.cache || this.epRuntimeHasCache(runtime),
                    cacheNodeClass: this.epCacheNodeClass(runtime),
                    cacheConnClass: this.epCacheConnClass(runtime),
                    cacheStatusLabel: this.epCacheStatusLabel(runtime),
                    aiLabel: this.epAiLabel(source, runtime),
                    aiSublabel: this.epAiSublabel(source, runtime),
	                    aiConnClass: this.epAiConnClass(runtime),
	                    aiNodeClass: this.epAiNodeClass(runtime),
	                    responseConnClass: this.epResponseConnClass(runtime),
	                    responseNodeClass: this.epResponseNodeClass(runtime),
				    authNodeClass: this.epAuthNodeClass(runtime),
				    authNodeSublabel: this.epAuthNodeSublabel(runtime),
	                    showAsync,
	                    showUsage,
	                    showAudit,
                        workflowID
	                };
	            },

            executionPlanWorkflowChart(source) {
                return this.executionPlanChartModel(source, null, { forceCache: false });
            },

            executionPlanAuditChart(entry) {
                const source = this.auditEntryExecutionPlan(entry);
                const runtime = this.epRuntimeFromEntry(entry);
                const features = this.executionPlanEntryFeatures(entry) || this.executionPlanSourceFeatures(source);
                return this.executionPlanChartModel(source, runtime, {
                    entry,
                    features,
                    forceAudit: true,
                    forceAsync: true
                });
            },

            // runtime shape: {
            //   cacheHit: bool,
            //   cacheType: 'exact'|'semantic'|null,
            //   provider,
            //   model,
            //   statusCode: number|null,
            //   responseSuccess: bool,
            //   aiSuccess: bool
            // }
            epRuntimeHasCache(runtime) {
                return !!(runtime && runtime.cacheHit);
            },

            epShowCacheStep(source, runtime) {
                return this.epHasCache(source) || this.epRuntimeHasCache(runtime);
            },

            epCacheNodeClass(runtime) {
                if (!runtime || !runtime.cacheHit) return '';
                if (runtime.cacheHit && runtime.cacheType === 'semantic') return 'ep-node-cache-semantic';
                if (runtime.cacheHit) return 'ep-node-cache-hit';
                return '';
            },

            epCacheConnClass(runtime) {
                return runtime && runtime.cacheHit ? 'ep-conn-hit' : '';
            },

            epCacheStatusLabel(runtime) {
                if (!runtime || !runtime.cacheHit) return null;
                if (runtime.cacheType === 'semantic') return 'Hit (Semantic)';
                return 'Hit (Exact)';
            },

            epAiConnClass(runtime) {
                if (!runtime) return '';
                if (runtime.cacheHit) return 'ep-conn-dim';
                return '';
            },

            epAiNodeClass(runtime) {
                if (!runtime) return '';
                if (runtime.cacheHit) return 'ep-node-ai-skipped';
                return runtime.aiSuccess ? 'ep-node-ai-success' : '';
            },

            epResponseConnClass(runtime) {
                if (!runtime) return '';
                if (runtime.cacheHit) return 'ep-conn-dim';
                return '';
            },

            epResponseNodeClass(runtime) {
                if (!runtime) return '';
                return runtime.responseSuccess ? 'ep-node-endpoint-success' : '';
            },

            epAuthNodeClass(runtime) {
                if (!runtime) return '';
                if (runtime.authError) return 'ep-node-auth-error';
                if (runtime.authMethod === 'api_key' || runtime.authMethod === 'master_key') return 'ep-node-auth-success';
                return '';
            },

            epAuthNodeSublabel(runtime) {
                if (!runtime || !runtime.authMethod) return null;
                return runtime.authMethod;
            },

            epRuntimeFromEntry(entry) {
                if (!entry) return null;
                const normalizedCacheType = (() => {
                    const value = String(entry.cache_type || '').trim().toLowerCase();
                    if (value === 'exact' || value === 'semantic') return value;
                    return null;
                })();
                const statusCode = (() => {
                    if (entry.status_code === undefined || entry.status_code === null) return null;
                    const raw = String(entry.status_code).trim();
                    if (!raw) return null;
                    const value = Number(raw);
                    return Number.isFinite(value) ? value : null;
                })();
                const cacheHit = normalizedCacheType
                    ? true
                    : (entry.cache_hit !== undefined && entry.cache_hit !== null)
                        ? !!entry.cache_hit
                        : false;
                const responseSuccess = Number.isFinite(statusCode) && statusCode >= 200 && statusCode < 300;
                const authError = String(entry.error_type || '').trim().toLowerCase() === 'authentication_error';
                const authMethod = String(entry.auth_method || '').trim().toLowerCase() || null;
                return {
                    cacheHit,
                    cacheType: normalizedCacheType || null,
                    provider: entry.provider || null,
                    model: entry.model || null,
                    statusCode,
                    responseSuccess,
                    aiSuccess: responseSuccess && !cacheHit,
                    authError,
                    authMethod
                };
            },

            async deactivateExecutionPlan(plan) {
                const planID = String(plan && plan.id || '').trim();
                if (!planID || this.executionPlanDeactivatingID || !this.canDeactivateExecutionPlan(plan)) {
                    return;
                }
                const workflowName = this.workflowDisplayName(plan);
                if (typeof global.confirm === 'function' && !global.confirm(
                    'Deactivate workflow "' + workflowName + '"? Requests will fall back to the next active workflow for this scope.'
                )) {
                    return;
                }

                this.executionPlanError = '';
                this.executionPlanNotice = '';
                this.executionPlanDeactivatingID = planID;
                try {
                    const res = await fetch('/admin/api/v1/execution-plans/' + encodeURIComponent(planID) + '/deactivate', {
                        method: 'POST',
                        headers: this.headers()
                    });

                    if (res.status === 401) {
                        this.handleFetchResponse(res, 'deactivate workflow');
                        return;
                    }
                    if (!res.ok) {
                        this.executionPlanError = await this.executionPlanResponseMessage(res, 'Unable to deactivate workflow.');
                        return;
                    }

                    this.executionPlanNotice = 'Workflow deactivated.';
                    await this.fetchExecutionPlansPage();
                } catch (e) {
                    console.error('Failed to deactivate workflow:', e);
                    this.executionPlanError = 'Unable to deactivate workflow.';
                } finally {
                    this.executionPlanDeactivatingID = '';
                }
            }
        };
    }

    global.dashboardExecutionPlansModule = dashboardExecutionPlansModule;
})(window);
