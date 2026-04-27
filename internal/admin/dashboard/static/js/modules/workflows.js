(function(global) {
    const DRAFT_WORKFLOW_PREVIEW_ID = 'draft-workflow-preview';

    function dashboardWorkflowsModule() {
        const clipboardModuleFactory = typeof global.dashboardClipboardModule === 'function'
            ? global.dashboardClipboardModule
            : null;
        const clipboard = clipboardModuleFactory
            ? clipboardModuleFactory()
            : null;

        function createWorkflowIDCopyState() {
            if (clipboard && typeof clipboard.createClipboardButtonState === 'function') {
                return clipboard.createClipboardButtonState({
                    logPrefix: 'Failed to copy workflow ID:'
                });
            }
            return {
                copied: false,
                error: false,
                async copy() {}
            };
        }

        return {
            workflows: [],
            workflowVersionsByID: {},
            workflowVersionRequests: {},
            workflowsAvailable: true,
            workflowsLoading: false,
            workflowRuntimeConfig: {},
            workflowError: '',
            workflowNotice: '',
            workflowFilter: '',
            workflowFormOpen: false,
            workflowSubmitting: false,
            workflowDeactivatingID: '',
            workflowFormError: '',
            workflowFormHydrated: false,
            workflowHydratedScope: {
                scope_provider: '',
                scope_model: '',
                scope_user_path: ''
            },
            guardrailRefs: [],
            workflowForm: {
                scope_provider: '',
                scope_model: '',
                scope_user_path: '',
                name: '',
                description: '',
                features: {
                    cache: true,
                    audit: true,
                    usage: true,
                    budget: true,
                    guardrails: false,
                    fallback: true
                },
                guardrails: []
            },

            defaultWorkflowForm() {
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
                        budget: true,
                        guardrails: false,
                        fallback: true
                    },
                    guardrails: []
                };
            },

	            workflowRuntimeConfigKeys() {
	                return [
	                    'FEATURE_FALLBACK_MODE',
	                    'LOGGING_ENABLED',
	                    'USAGE_ENABLED',
	                    'BUDGETS_ENABLED',
	                    'GUARDRAILS_ENABLED',
	                    'CACHE_ENABLED',
	                    'REDIS_URL',
	                    'SEMANTIC_CACHE_ENABLED'
	                ];
	            },

	            workflowRuntimeFlag(name) {
	                const value = this.workflowRuntimeConfig && this.workflowRuntimeConfig[name];
	                return String(value || '').trim().toLowerCase();
	            },

	            workflowRuntimeBooleanFlag(name, defaultValue) {
	                const value = this.workflowRuntimeFlag(name);
	                if (value === '') {
	                    return !!defaultValue;
	                }
	                return value === 'on' || value === 'true' || value === '1';
	            },

	            workflowCacheVisible() {
	                const explicit = this.workflowRuntimeFlag('CACHE_ENABLED');
	                if (explicit !== '') {
	                    return this.workflowRuntimeBooleanFlag('CACHE_ENABLED', false);
	                }
	                const redis = this.workflowRuntimeFlag('REDIS_URL');
	                const semantic = this.workflowRuntimeFlag('SEMANTIC_CACHE_ENABLED');
	                if (redis === '' && semantic === '') {
	                    return true;
	                }
	                return this.workflowRuntimeBooleanFlag('REDIS_URL', false)
	                    || this.workflowRuntimeBooleanFlag('SEMANTIC_CACHE_ENABLED', false);
	            },

	            workflowAuditVisible() {
	                return this.workflowRuntimeBooleanFlag('LOGGING_ENABLED', true);
	            },

	            workflowUsageVisible() {
	                return this.workflowRuntimeBooleanFlag('USAGE_ENABLED', true);
	            },

	            workflowBudgetVisible() {
	                return this.workflowRuntimeBooleanFlag('BUDGETS_ENABLED', true);
	            },

	            workflowGuardrailsVisible() {
	                return this.workflowRuntimeBooleanFlag('GUARDRAILS_ENABLED', true);
	            },

	            workflowFeatureCaps() {
	                return {
	                    cache: this.workflowCacheVisible(),
	                    audit: this.workflowAuditVisible(),
	                    usage: this.workflowUsageVisible(),
	                    budget: this.workflowBudgetVisible(),
	                    guardrails: this.workflowGuardrailsVisible()
	                };
	            },

	            workflowReadFeatureFlag(raw, key, defaultValue) {
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

	            workflowHasDefinedFeatureFlag(raw, key) {
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

	            workflowNormalizedFeatures(raw) {
	                return {
	                    cache: !!this.workflowReadFeatureFlag(raw, 'cache', false),
	                    audit: !!this.workflowReadFeatureFlag(raw, 'audit', false),
	                    usage: !!this.workflowReadFeatureFlag(raw, 'usage', false),
	                    budget: this.workflowReadFeatureFlag(raw, 'budget', true) !== false,
	                    guardrails: !!this.workflowReadFeatureFlag(raw, 'guardrails', false),
	                    fallback: this.workflowReadFeatureFlag(raw, 'fallback', true) !== false
	                };
	            },

	            workflowApplyGlobalFeatureCaps(raw) {
	                const features = this.workflowNormalizedFeatures(raw);
	                const caps = this.workflowFeatureCaps();
	                const usage = features.usage && caps.usage;
	                return {
	                    cache: features.cache && caps.cache,
	                    audit: features.audit && caps.audit,
	                    usage,
	                    budget: usage && features.budget && caps.budget,
	                    guardrails: features.guardrails && caps.guardrails,
	                    fallback: features.fallback
	                };
	            },

	            workflowFailoverVisible() {
	                const mode = this.workflowRuntimeFlag('FEATURE_FALLBACK_MODE');
	                return mode !== '' && mode !== 'off';
	            },

            workflowFallbackLabel(source) {
                return this.workflowSourceFeatures(source).fallback ? 'On' : 'Off';
            },

            defaultWorkflowGuardrailStep(step) {
                return {
                    ref: '',
                    step: Number.isFinite(step) ? step : 10
                };
            },

            parseWorkflowGuardrailStep(rawStep) {
                const trimmedStep = rawStep === null || rawStep === undefined ? '' : String(rawStep).trim();
                if (trimmedStep === '') {
                    return Number.NaN;
                }
                const parsedStep = Number(trimmedStep);
                return Number.isFinite(parsedStep) ? parsedStep : Number.NaN;
            },

            get filteredWorkflows() {
                if (!this.workflowFilter) {
                    return this.workflows;
                }
                const filter = this.workflowFilter.toLowerCase();
                return this.workflows.filter((workflow) => {
                    const fields = [
                        workflow.name,
                        workflow.description,
                        workflow.scope_display,
                        workflow.scope_type,
                        this.workflowScopeProviderValue(workflow && workflow.scope),
                        workflow.scope && workflow.scope.scope_model,
                        workflow.scope && workflow.scope.scope_user_path,
                        workflow.workflow_hash,
                        ...(Array.isArray(workflow.workflow_payload && workflow.workflow_payload.guardrails)
                            ? workflow.workflow_payload.guardrails.map((step) => step.ref)
                            : [])
                    ];
                    return fields.some((value) => String(value || '').toLowerCase().includes(filter));
                });
            },

            workflowScopeProviderValue(scope) {
                return String(
                    scope && (scope.scope_provider_name || scope.scope_provider) || ''
                ).trim();
            },

            workflowModelProviderValue(model) {
                return String(
                    model && (model.provider_name || model.provider_type) || ''
                ).trim();
            },

            workflowProviderOptions() {
                const options = new Set();
                const preservedProvider = String(this.workflowHydratedScope && this.workflowHydratedScope.scope_provider || '').trim();
                if (preservedProvider) {
                    options.add(preservedProvider);
                }
                const models = Array.isArray(this.models) ? this.models : [];
                models.forEach((model) => {
                    const providerName = this.workflowModelProviderValue(model);
                    if (providerName) {
                        options.add(providerName);
                    }
                });
                return [...options].sort();
            },

            workflowModelOptions(providerName) {
                const wantedProvider = String(providerName || '').trim();
                const options = new Set();
                const preservedProvider = String(this.workflowHydratedScope && this.workflowHydratedScope.scope_provider || '').trim();
                const preservedModel = String(this.workflowHydratedScope && this.workflowHydratedScope.scope_model || '').trim();
                if (wantedProvider && wantedProvider === preservedProvider && preservedModel) {
                    options.add(preservedModel);
                }
                const models = Array.isArray(this.models) ? this.models : [];
                models.forEach((model) => {
                    if (wantedProvider && this.workflowModelProviderValue(model) !== wantedProvider) {
                        return;
                    }
                    const modelID = String(model && model.model && model.model.id || '').trim();
                    if (modelID) {
                        options.add(modelID);
                    }
                });
                return [...options].sort();
            },

            workflowScopeTypeLabel(workflow) {
                const scopeType = String(workflow && workflow.scope_type || '').trim();
                if (scopeType === 'provider_model') return 'Provider Name + Model';
                if (scopeType === 'provider_model_path') return 'Provider Name + Model + Path';
                if (scopeType === 'provider_path') return 'Provider Name + Path';
                if (scopeType === 'path') return 'Path';
                if (scopeType === 'provider') return 'Provider Name';
                return 'Global';
            },

            workflowScopeLabel(workflow) {
                return String(workflow && workflow.scope_display || 'global').trim() || 'global';
            },

            workflowDisplayName(workflow) {
                const explicitName = String(workflow && workflow.name || '').trim();
                if (explicitName) {
                    return explicitName;
                }
                const scopeLabel = this.workflowScopeLabel(workflow);
                if (scopeLabel === 'global') {
                    return 'All models';
                }
                return scopeLabel;
            },

            workflowCurrentScope() {
                const form = this.workflowForm || this.defaultWorkflowForm();
                const provider = String(form.scope_provider || '').trim();
                const userPath = this.normalizeWorkflowScopeUserPath(form.scope_user_path);
                return {
                    scope_provider: provider,
                    scope_model: provider ? String(form.scope_model || '').trim() : '',
                    scope_user_path: userPath
                };
            },

            workflowScopeType(scope) {
                const provider = String(scope && scope.scope_provider || '').trim();
                const model = provider ? String(scope && scope.scope_model || '').trim() : '';
                const userPath = this.normalizeWorkflowScopeUserPath(scope && scope.scope_user_path);
                if (!provider && !userPath) return 'global';
                if (!provider && userPath) return 'path';
                if (!model && !userPath) return 'provider';
                if (!model && userPath) return 'provider_path';
                if (userPath) return 'provider_model_path';
                return 'provider_model';
            },

            workflowScopeDisplay(scope) {
                const provider = String(scope && scope.scope_provider || '').trim();
                const model = provider ? String(scope && scope.scope_model || '').trim() : '';
                const userPath = this.normalizeWorkflowScopeUserPath(scope && scope.scope_user_path);
                const scopeType = this.workflowScopeType({
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

            workflowScopeMatches(workflow, scope) {
                const normalized = scope || { scope_provider: '', scope_model: '', scope_user_path: '' };
                const provider = this.workflowScopeProviderValue(workflow && workflow.scope);
                const model = provider ? String(workflow && workflow.scope && workflow.scope.scope_model || '').trim() : '';
                const userPath = this.normalizeWorkflowScopeUserPath(workflow && workflow.scope && workflow.scope.scope_user_path);
                return provider === String(normalized.scope_provider || '').trim()
                    && model === String(normalized.scope_model || '').trim()
                    && userPath === this.normalizeWorkflowScopeUserPath(normalized.scope_user_path);
            },

            workflowActiveScopeMatch() {
                const scope = this.workflowCurrentScope();
                const hasScopedSelection = scope.scope_provider !== ''
                    || scope.scope_model !== ''
                    || scope.scope_user_path !== '';
                if (!hasScopedSelection && !this.workflowFormHydrated) {
                    return null;
                }
                const workflows = Array.isArray(this.workflows) ? this.workflows : [];
                return workflows.find((workflow) => this.workflowScopeMatches(workflow, scope)) || null;
            },

            workflowSubmitMode() {
                return this.workflowActiveScopeMatch() ? 'save' : 'create';
            },

            workflowSubmitLabel() {
                return this.workflowSubmitMode() === 'save' ? 'Save' : 'Create';
            },

            workflowSubmittingLabel() {
                return this.workflowSubmitMode() === 'save' ? 'Saving...' : 'Creating...';
            },

            workflowPreview() {
                const form = this.workflowForm || this.defaultWorkflowForm();
                const scope = this.workflowCurrentScope();
                const rawFeatures = this.workflowNormalizedFeatures(form.features || {});
                const features = this.workflowApplyGlobalFeatureCaps(rawFeatures);
                features.fallback = rawFeatures.fallback;
                const guardrailsEnabled = !!features.guardrails;
                const guardrails = guardrailsEnabled ? this.workflowSourceGuardrails(form) : [];
                const scopeType = this.workflowScopeType(scope);
                const scopeDisplay = this.workflowScopeDisplay(scope);

                return {
                    id: DRAFT_WORKFLOW_PREVIEW_ID,
                    scope_type: scopeType,
                    scope_display: scopeDisplay,
                    scope: {
                        scope_provider_name: scope.scope_provider,
                        scope_model: scope.scope_model,
                        ...(scope.scope_user_path ? { scope_user_path: scope.scope_user_path } : {})
                    },
                    name: String(form.name || '').trim(),
                    description: String(form.description || '').trim(),
                    workflow_payload: {
                        schema_version: 1,
                        features: {
                            cache: !!features.cache,
                            audit: !!features.audit,
                            usage: !!features.usage,
                            budget: !!features.budget,
                            guardrails: guardrailsEnabled,
                            fallback: !!features.fallback
                        },
                        guardrails
                    }
                };
            },

	            workflowSourceFeatures(source) {
	                const raw = source && source.workflow_payload && source.workflow_payload.features
	                    ? source.workflow_payload.features
	                    : source && source.features
	                        ? source.features
	                        : {};
	                const effective = source && source.effective_features && typeof source.effective_features === 'object' && !Array.isArray(source.effective_features)
	                    ? source.effective_features
	                    : null;
	                const features = this.workflowApplyGlobalFeatureCaps(effective || raw);
	                return {
	                    ...features,
	                    fallback: this.workflowNormalizedFeatures(raw).fallback
	                };
	            },

            workflowEntryFeatures(entry) {
                const raw = entry && entry.data && entry.data.workflow_features;
                if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
                    return null;
                }
                return this.workflowNormalizedFeatures(raw);
            },

            workflowEntryFailover(entry) {
                const raw = entry && entry.data && entry.data.failover;
                if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
                    return null;
                }

                const targetModel = String(raw.target_model || raw.targetModel || '').trim() || null;
                if (!targetModel) {
                    return null;
                }

                return {
                    targetModel
                };
            },

            workflowFailoverTarget(entry) {
                const failover = this.workflowEntryFailover(entry);
                return failover && failover.targetModel ? failover.targetModel : null;
            },

            workflowSourceGuardrails(source) {
                const raw = Array.isArray(source && source.workflow_payload && source.workflow_payload.guardrails)
                    ? source.workflow_payload.guardrails
                    : Array.isArray(source && source.guardrails)
                        ? source.guardrails
                        : [];
                return raw
                    .map((step) => ({
                        ref: String(step && step.ref || '').trim(),
                        step: this.parseWorkflowGuardrailStep(step && step.step)
                    }))
                    .filter((step) => Number.isInteger(step.step) && step.step >= 0);
            },

            canDeactivateWorkflow(workflow) {
                return String(workflow && workflow.scope_type || '').trim() !== 'global';
            },

            focusWorkflowForm() {
                const focus = () => {
                    const refs = this.$refs || {};
                    const editor = refs.workflowEditor || null;
                    if (!editor || typeof editor.querySelector !== 'function') {
                        return;
                    }
                    const field = editor.querySelector('[data-modal-autofocus], input:not([type="hidden"]):not([disabled]), textarea:not([disabled]), select:not([disabled]), button:not([disabled])');
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

	            workflowGuardrails(workflow) {
	                if (!this.workflowSourceFeatures(workflow).guardrails) {
	                    return [];
	                }
	                return Array.isArray(workflow && workflow.workflow_payload && workflow.workflow_payload.guardrails)
	                    ? workflow.workflow_payload.guardrails
	                    : [];
	            },

            shortHash(value) {
                const hash = String(value || '').trim();
                if (!hash) return '\u2014';
                if (hash.length <= 14) return hash;
                return hash.slice(0, 12) + '\u2026';
            },

            openWorkflowCreate(workflow) {
                this.workflowFormOpen = true;
                this.workflowSubmitting = false;
                this.workflowFormError = '';
                this.workflowNotice = '';

                if (!workflow) {
                    this.workflowFormHydrated = false;
                    this.workflowHydratedScope = {
                        scope_provider: '',
                        scope_model: '',
                        scope_user_path: ''
                    };
                    this.workflowForm = this.defaultWorkflowForm();
                    this.focusWorkflowForm();
                    return;
                }

                this.workflowFormHydrated = true;
                this.workflowHydratedScope = {
                    scope_provider: this.workflowScopeProviderValue(workflow.scope),
                    scope_model: String(workflow.scope && workflow.scope.scope_model || '').trim(),
                    scope_user_path: String(workflow.scope && workflow.scope.scope_user_path || '').trim()
                };
                const storedFeatures = workflow.workflow_payload && workflow.workflow_payload.features
                    ? this.workflowNormalizedFeatures(workflow.workflow_payload.features)
                    : this.workflowSourceFeatures(workflow);
                const storedGuardrails = Array.isArray(workflow.workflow_payload && workflow.workflow_payload.guardrails)
                    ? workflow.workflow_payload.guardrails
                        .map((step) => ({
                            ref: String(step && step.ref || '').trim(),
                            step: this.parseWorkflowGuardrailStep(step && step.step)
                        }))
                        .filter((step) => Number.isInteger(step.step) && step.step >= 0)
                    : this.workflowSourceGuardrails(workflow);
                this.workflowForm = {
                    scope_provider: this.workflowScopeProviderValue(workflow.scope),
                    scope_model: String(workflow.scope && workflow.scope.scope_model || ''),
                    scope_user_path: String(workflow.scope && workflow.scope.scope_user_path || ''),
                    name: String(workflow.name || ''),
                    description: String(workflow.description || ''),
                    features: {
                        cache: !!storedFeatures.cache,
                        audit: !!storedFeatures.audit,
                        usage: !!storedFeatures.usage,
                        budget: !!storedFeatures.budget,
                        guardrails: !!storedFeatures.guardrails,
                        fallback: !!storedFeatures.fallback
                    },
                    guardrails: storedGuardrails.map((step) => ({
                        ref: String(step && step.ref || ''),
                        step: Number.isFinite(step && step.step) ? step.step : 10
                    }))
                };
                this.focusWorkflowForm();
            },

            closeWorkflowForm() {
                this.workflowFormOpen = false;
                this.workflowSubmitting = false;
                this.workflowFormError = '';
                this.workflowFormHydrated = false;
                this.workflowHydratedScope = {
                    scope_provider: '',
                    scope_model: '',
                    scope_user_path: ''
                };
                this.workflowForm = this.defaultWorkflowForm();
            },

            setWorkflowProvider(provider) {
                this.workflowForm.scope_provider = String(provider || '').trim();
                if (!this.workflowForm.scope_provider) {
                    this.workflowForm.scope_provider = '';
                    this.workflowForm.scope_model = '';
                    return;
                }
                const modelOptions = this.workflowModelOptions(this.workflowForm.scope_provider);
                if (!modelOptions.includes(String(this.workflowForm.scope_model || '').trim())) {
                    this.workflowForm.scope_model = '';
                }
            },

            addWorkflowGuardrailStep() {
                const steps = Array.isArray(this.workflowForm.guardrails) ? this.workflowForm.guardrails : [];
                const nextStep = steps.reduce((maxStep, step) => {
                    const parsed = Number(step && step.step);
                    return Number.isFinite(parsed) ? Math.max(maxStep, parsed) : maxStep;
                }, 0) + 10;
                this.workflowForm.guardrails.push(this.defaultWorkflowGuardrailStep(nextStep));
            },

            removeWorkflowGuardrailStep(index) {
                if (!Array.isArray(this.workflowForm.guardrails)) return;
                this.workflowForm.guardrails.splice(index, 1);
            },

            workflowScopeUserPathValidationError(value) {
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

            normalizeWorkflowScopeUserPath(value) {
                if (this.workflowScopeUserPathValidationError(value)) {
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

            buildWorkflowRequest() {
                const form = this.workflowForm || this.defaultWorkflowForm();
                const provider = String(form.scope_provider || '').trim();
                const model = provider ? String(form.scope_model || '').trim() : '';
                const userPath = this.normalizeWorkflowScopeUserPath(form.scope_user_path);
                const rawFeatures = this.workflowNormalizedFeatures(form.features || {});
                const features = this.workflowApplyGlobalFeatureCaps(rawFeatures);
                const activeScopeMatch = this.workflowActiveScopeMatch();
                const activeScopeFeatures = activeScopeMatch && activeScopeMatch.workflow_payload && activeScopeMatch.workflow_payload.features;
                const activeScopeHasFallback = this.workflowHasDefinedFeatureFlag(activeScopeFeatures, 'fallback');
                const preservedActiveFallback = activeScopeHasFallback
                    ? this.workflowReadFeatureFlag(activeScopeFeatures, 'fallback', true) !== false
                    : null;
                const hydratedScope = this.workflowHydratedScope || {
                    scope_provider: '',
                    scope_model: '',
                    scope_user_path: ''
                };
                const sameHydratedScope = String(hydratedScope.scope_provider || '').trim() === provider
                    && String(hydratedScope.scope_model || '').trim() === model
                    && this.normalizeWorkflowScopeUserPath(hydratedScope.scope_user_path) === this.normalizeWorkflowScopeUserPath(userPath);
                const includeFallback = this.workflowFailoverVisible()
                    || (!!this.workflowFormHydrated
                        && sameHydratedScope
                        && Object.prototype.hasOwnProperty.call(rawFeatures, 'fallback'))
                    || (!this.workflowFormHydrated
                        && !!activeScopeMatch
                        && activeScopeHasFallback);

                const guardrails = !!features.guardrails
                    ? (Array.isArray(form.guardrails) ? form.guardrails : []).map((step) => {
                        return {
                            ref: String(step && step.ref || '').trim(),
                            step: this.parseWorkflowGuardrailStep(step && step.step)
                        };
                    })
                    : [];

                const payload = {
                    scope_provider_name: provider,
                    scope_model: model,
                    ...(userPath ? { scope_user_path: userPath } : {}),
                    name: String(form.name || '').trim(),
                    description: String(form.description || '').trim(),
                    workflow_payload: {
                        schema_version: 1,
                        features: {
                            cache: !!features.cache,
                            audit: !!features.audit,
                            usage: !!features.usage,
                            budget: !!features.budget,
                            guardrails: !!features.guardrails
                        },
                        guardrails
                    }
                };
                if (includeFallback) {
                    payload.workflow_payload.features.fallback = !this.workflowFailoverVisible()
                        && !this.workflowFormHydrated
                        && !!activeScopeMatch
                        && activeScopeHasFallback
                        ? preservedActiveFallback
                        : !!rawFeatures.fallback;
                }

                return payload;
            },

            async fetchWorkflowRuntimeConfig() {
                const controller = typeof AbortController === 'function' ? new AbortController() : null;
                const timeoutID = controller && typeof setTimeout === 'function'
                    ? setTimeout(() => controller.abort(), 10000)
                    : null;
                try {
                    const request = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    if (controller) {
                        request.signal = controller.signal;
                    }
                    const res = await fetch('/admin/api/v1/dashboard/config', request);
                    const handled = this.handleFetchResponse(res, 'dashboard config', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.workflowRuntimeConfig = {};
                        return;
                    }
                    const payload = await res.json();
                    const next = {};
                    const allowedKeys = this.workflowRuntimeConfigKeys();
                    for (const key of allowedKeys) {
                        if (payload && typeof payload === 'object' && !Array.isArray(payload) && payload[key] !== undefined && payload[key] !== null) {
                            next[key] = String(payload[key]).trim();
                        }
	                    }
	                    this.workflowRuntimeConfig = next;
	                    if (typeof this.fetchCacheOverview === 'function') {
	                        this.fetchCacheOverview();
	                    }
	                } catch (e) {
	                    console.error('Failed to fetch dashboard config:', e);
	                    this.workflowRuntimeConfig = {};
                } finally {
                    if (timeoutID !== null && typeof clearTimeout === 'function') {
                        clearTimeout(timeoutID);
                    }
                }
            },

            validateWorkflowRequest(payload) {
                const preservedProvider = String(this.workflowHydratedScope && this.workflowHydratedScope.scope_provider || '').trim();
                const preservedModel = String(this.workflowHydratedScope && this.workflowHydratedScope.scope_model || '').trim();
                const providerName = String(payload && (payload.scope_provider_name || payload.scope_provider) || '').trim();
                const scopeModel = String(payload && payload.scope_model || '').trim();

                if (providerName) {
                    const providers = this.workflowProviderOptions();
                    if (!providers.includes(providerName) && providerName !== preservedProvider) {
                        return 'Choose a registered provider name.';
                    }
                }
                if (scopeModel && !providerName) {
                    return 'Model selection requires a provider name.';
                }
                if (scopeModel) {
                    const models = this.workflowModelOptions(providerName);
                    const isPreservedModel = providerName === preservedProvider && scopeModel === preservedModel;
                    if (!models.includes(scopeModel) && !isPreservedModel) {
                        return 'Choose a registered model for the selected provider name.';
                    }
                }
                const userPathError = this.workflowScopeUserPathValidationError(payload.scope_user_path);
                if (userPathError) {
                    return userPathError;
                }
                const features = payload.workflow_payload && payload.workflow_payload.features ? payload.workflow_payload.features : {};
                const guardrails = Array.isArray(payload.workflow_payload && payload.workflow_payload.guardrails)
                    ? payload.workflow_payload.guardrails
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
                        return 'Each guardrail ref may appear only once in a workflow.';
                    }
                    seen.add(step.ref);
                }

                return '';
            },

            async workflowResponseMessage(res, fallback) {
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

            async fetchWorkflows() {
                this.workflowsLoading = true;
                this.workflowError = '';
                const controller = typeof AbortController === 'function' ? new AbortController() : null;
                const timeoutID = controller && typeof setTimeout === 'function'
                    ? setTimeout(() => controller.abort(), 10000)
                    : null;
                try {
                    const request = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    if (controller) {
                        request.signal = controller.signal;
                    }
                    const res = await fetch('/admin/api/v1/workflows', request);
                    if (res.status === 503) {
                        this.workflowsAvailable = false;
                        this.workflows = [];
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'workflows', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    this.workflowsAvailable = true;
                    if (!handled) {
                        this.workflows = [];
                        return;
                    }
                    const payload = await res.json();
                    this.workflows = Array.isArray(payload) ? payload : [];
                    this.cacheWorkflowVersions(this.workflows);
                } catch (e) {
                    console.error('Failed to fetch workflows:', e);
                    this.workflows = [];
                    this.workflowError = e && e.name === 'AbortError'
                        ? 'Loading workflows timed out.'
                        : 'Unable to load workflows.';
                } finally {
                    if (timeoutID !== null && typeof clearTimeout === 'function') {
                        clearTimeout(timeoutID);
                    }
                    this.workflowsLoading = false;
                }
            },

            async fetchWorkflowGuardrails() {
                try {
                    const request = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    const res = await fetch('/admin/api/v1/workflows/guardrails', request);
                    const handled = this.handleFetchResponse(res, 'workflow guardrails', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
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

            async fetchWorkflowsPage() {
                await Promise.all([
                    this.fetchWorkflowRuntimeConfig(),
                    this.fetchWorkflows(),
                    this.fetchWorkflowGuardrails()
                ]);
            },

            cacheWorkflowVersion(workflow) {
                const workflowID = String(workflow && workflow.id || '').trim();
                if (!workflowID) {
                    return null;
                }
                this.workflowVersionsByID = {
                    ...(this.workflowVersionsByID || {}),
                    [workflowID]: workflow
                };
                return workflow;
            },

            cacheWorkflowVersions(workflows) {
                if (!Array.isArray(workflows) || workflows.length === 0) {
                    return;
                }
                const next = {
                    ...(this.workflowVersionsByID || {})
                };
                workflows.forEach((workflow) => {
                    const workflowID = String(workflow && workflow.id || '').trim();
                    if (workflowID) {
                        next[workflowID] = workflow;
                    }
                });
                this.workflowVersionsByID = next;
            },

            cacheMissingWorkflowVersion(workflowID) {
                const normalizedID = String(workflowID || '').trim();
                if (!normalizedID) {
                    return;
                }
                this.workflowVersionsByID = {
                    ...(this.workflowVersionsByID || {}),
                    [normalizedID]: null
                };
            },

            workflowVersionCacheHas(workflowID) {
                return Object.prototype.hasOwnProperty.call(this.workflowVersionsByID || {}, String(workflowID || '').trim());
            },

            workflowVersionByID(workflowID) {
                const normalizedID = String(workflowID || '').trim();
                if (!normalizedID) {
                    return null;
                }
                if (this.workflowVersionCacheHas(normalizedID)) {
                    return this.workflowVersionsByID[normalizedID];
                }
                const workflows = Array.isArray(this.workflows) ? this.workflows : [];
                const activeMatch = workflows.find((workflow) => String(workflow && workflow.id || '').trim() === normalizedID) || null;
                if (activeMatch) {
                    this.cacheWorkflowVersion(activeMatch);
                }
                return activeMatch;
            },

            async fetchWorkflowVersion(workflowID) {
                const normalizedID = String(workflowID || '').trim();
                if (!normalizedID) {
                    return null;
                }
                if (this.workflowVersionCacheHas(normalizedID)) {
                    return this.workflowVersionsByID[normalizedID];
                }
                if (this.workflowVersionRequests && this.workflowVersionRequests[normalizedID]) {
                    return this.workflowVersionRequests[normalizedID];
                }

	                const request = (async () => {
	                    const controller = typeof AbortController === 'function' ? new AbortController() : null;
	                    const timeoutID = controller && typeof setTimeout === 'function'
	                        ? setTimeout(() => controller.abort(), 10000)
	                        : null;
	                    try {
	                        const options = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
	                        if (controller) {
	                            options.signal = controller.signal;
	                        }
	                        const res = await fetch('/admin/api/v1/workflows/' + encodeURIComponent(normalizedID), options);
	                        if (res.status === 404) {
	                            this.cacheMissingWorkflowVersion(normalizedID);
	                            return null;
                        }
                        if (res.status === 401) {
                            if (typeof this.handleFetchResponse === 'function') {
                                this.handleFetchResponse(res, 'workflow', options);
                            }
                            return null;
                        }
                        if (!res.ok) {
                            if (typeof this.handleFetchResponse === 'function') {
                                this.handleFetchResponse(res, 'workflow', options);
                            }
                            return null;
                        }

                        const payload = await res.json();
                        if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
                            this.cacheMissingWorkflowVersion(normalizedID);
                            return null;
	                        }
	                        return this.cacheWorkflowVersion(payload);
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
	                        if (this.workflowVersionRequests) {
	                            delete this.workflowVersionRequests[normalizedID];
	                        }
                    }
                })();

                this.workflowVersionRequests = {
                    ...(this.workflowVersionRequests || {}),
                    [normalizedID]: request
                };
                return request;
            },

            async prefetchAuditWorkflows(entries) {
                const uniqueWorkflowIDs = [...new Set(
                    (Array.isArray(entries) ? entries : [])
                        .map((entry) => String(entry && entry.workflow_version_id || '').trim())
                        .filter(Boolean)
                )];
                if (uniqueWorkflowIDs.length === 0) {
                    return;
                }
                await Promise.all(uniqueWorkflowIDs.map((workflowID) => this.fetchWorkflowVersion(workflowID)));
            },

            auditEntryWorkflow(entry) {
                const workflowID = String(entry && entry.workflow_version_id || '').trim();
                if (!workflowID) {
                    return null;
                }
                return this.workflowVersionByID(workflowID);
            },

            async submitWorkflowForm() {
                if (this.workflowSubmitting) {
                    return;
                }
                this.workflowFormError = '';
                this.workflowNotice = '';

                const payload = this.buildWorkflowRequest();
                const validationError = this.validateWorkflowRequest(payload);
                if (validationError) {
                    this.workflowFormError = validationError;
                    return;
                }

                this.workflowSubmitting = true;
                try {
                    const request = typeof this.requestOptions === 'function'
                        ? this.requestOptions({
                            method: 'POST',
                            body: JSON.stringify(payload)
                        })
                        : {
                            method: 'POST',
                            headers: this.headers(),
                            body: JSON.stringify(payload)
                        };
                    const res = await fetch('/admin/api/v1/workflows', request);

                    if (res.status === 401) {
                        this.handleFetchResponse(res, 'create workflow', request);
                        return;
                    }
                    if (!res.ok) {
                        this.workflowFormError = await this.workflowResponseMessage(res, 'Unable to create workflow.');
                        console.error('Failed to create workflow:', res.status, res.statusText, this.workflowFormError);
                        return;
                    }

                    this.workflowNotice = 'Workflow created and activated.';
                    this.closeWorkflowForm();
                    await this.fetchWorkflowsPage();
                } catch (e) {
                    console.error('Failed to create workflow:', e);
                    this.workflowFormError = 'Unable to create workflow.';
                } finally {
                    this.workflowSubmitting = false;
                }
            },

            // ─── Workflow Pipeline helpers ───

            workflowHasGuardrails(source) {
                return !!this.workflowSourceFeatures(source).guardrails;
            },

            workflowHasCache(source) {
                return !!this.workflowSourceFeatures(source).cache;
            },

            workflowHasAudit(source) {
                return !!this.workflowSourceFeatures(source).audit;
            },

            workflowHasUsage(source) {
                return !!this.workflowSourceFeatures(source).usage;
            },

            workflowHasAsync(source) {
                const f = this.workflowSourceFeatures(source);
                return !!(f.audit || f.usage);
            },

            workflowGuardrailLabel(source) {
                const count = this.workflowSourceGuardrails(source).length;
                if (count === 0) return '';
                return count === 1 ? '1 step' : count + ' steps';
            },

            workflowAiLabel(source, runtime) {
                if (runtime && runtime.provider) return runtime.provider;
                const provider = this.workflowScopeProviderValue(source && source.scope);
                return provider || 'AI';
            },

	            workflowAiSublabel(source, runtime) {
	                if (runtime && runtime.model) return runtime.model;
	                return source && source.scope && source.scope.scope_model || null;
	            },

            workflowChartWorkflowID(source, entry) {
                const sourceID = String(source && source.id || '').trim();
                if (sourceID && sourceID !== DRAFT_WORKFLOW_PREVIEW_ID) {
                    return sourceID;
                }
                const entryID = String(entry && entry.workflow_version_id || '').trim();
                if (entryID && entryID !== DRAFT_WORKFLOW_PREVIEW_ID) {
                    return entryID;
                }
                return null;
            },

            workflowIDChip(workflowID) {
                const normalizedWorkflowID = String(workflowID || '').trim();
                return {
                    workflowID: normalizedWorkflowID,
                    copyState: createWorkflowIDCopyState(),

                    copyTitle() {
                        if (this.copyState.error) return 'Unable to copy workflow ID';
                        if (this.copyState.copied) return 'Workflow ID copied';
                        return 'Copy workflow ID';
                    },

                    copyAriaLabel() {
                        if (!this.workflowID) return 'Copy workflow ID';
                        if (this.copyState.error) return 'Unable to copy workflow ID ' + this.workflowID;
                        if (this.copyState.copied) return 'Workflow ID copied ' + this.workflowID;
                        return 'Copy workflow ID ' + this.workflowID;
                    },

                    setWorkflowID(workflowID) {
                        const nextWorkflowID = String(workflowID || '').trim();
                        if (this.workflowID === nextWorkflowID) return;

                        this.workflowID = nextWorkflowID;
                        this.copyState.copied = false;
                        this.copyState.error = false;
                    },

                    async copyWorkflowID() {
                        if (!this.workflowID) {
                            return;
                        }
                        await this.copyState.copy(this.workflowID);
                    }
                };
            },

            workflowChartModel(source, runtime, options) {
                const config = options || {};
                const features = config.features && typeof config.features === 'object' && !Array.isArray(config.features)
                    ? this.workflowNormalizedFeatures(config.features)
                    : this.workflowSourceFeatures(source);
                const forceAudit = !!config.forceAudit;
                const highlightAsyncPresent = !!config.highlightAsyncPresent;
                const showBudget = !!features.budget || this.workflowRuntimeBudgetExceeded(runtime);
                const showGuardrails = !!features.guardrails;
                const showUsage = !!features.usage;
                const showAudit = forceAudit || !!features.audit;
                const showAsync = !!config.forceAsync || !!(showUsage || showAudit);
                const showFailover = !!features.fallback || this.workflowRuntimeUsedFailover(runtime);
                const workflowID = this.workflowChartWorkflowID(source, config.entry);
                return {
                    showBudget,
                    budgetNodeClass: this.workflowBudgetNodeClass(showBudget, runtime, highlightAsyncPresent),
                    budgetStatusLabel: this.workflowBudgetStatusLabel(runtime),
                    showGuardrails,
                    guardrailLabel: showGuardrails ? this.workflowGuardrailLabel(source) : '',
                    showCache: !!config.forceCache || !!features.cache || this.workflowRuntimeHasCache(runtime),
                    cacheNodeClass: this.workflowCacheNodeClass(runtime),
                    cacheConnClass: this.workflowCacheConnClass(runtime),
                    cacheStatusLabel: this.workflowCacheStatusLabel(runtime),
                    showFailover,
                    failoverNodeClass: showFailover ? this.workflowFailoverNodeClass(runtime) : '',
                    failoverConnClass: showFailover ? this.workflowFailoverConnClass(runtime) : '',
                    failoverStatusLabel: showFailover ? this.workflowFailoverStatusLabel(runtime) : null,
                    failoverTargetLabel: showFailover ? this.workflowFailoverTargetLabel(runtime) : null,
                    aiLabel: this.workflowAiLabel(source, runtime),
                    aiSublabel: this.workflowAiSublabel(source, runtime),
                    aiConnClass: this.workflowAiConnClass(runtime),
                    aiNodeClass: this.workflowAiNodeClass(runtime),
                    responseConnClass: this.workflowResponseConnClass(runtime),
                    responseNodeClass: this.workflowResponseNodeClass(runtime),
                    responseNodeSublabel: this.workflowResponseNodeSublabel(runtime),
                    authNodeClass: this.workflowAuthNodeClass(runtime),
                    authNodeSublabel: this.workflowAuthNodeSublabel(runtime),
                    usageNodeClass: this.workflowAsyncNodeClass(showUsage, highlightAsyncPresent),
                    auditNodeClass: this.workflowAsyncNodeClass(showAudit, highlightAsyncPresent),
                    showAsync,
                    showUsage,
                    showAudit,
                    workflowID
                };
            },

            workflowChart(source) {
                return this.workflowChartModel(source, null, { forceCache: false });
            },

            workflowAuditChart(entry) {
                const source = this.auditEntryWorkflow(entry);
                const runtime = this.workflowRuntimeFromEntry(entry, source);
                const features = this.workflowEntryFeatures(entry)
                    || (source
                        ? this.workflowSourceFeatures(source)
                        : {
                            cache: false,
                            audit: false,
                            usage: false,
                            budget: false,
                            guardrails: false,
                            fallback: false
                        });
                return this.workflowChartModel(source, runtime, {
                    entry,
                    features,
                    forceAudit: true,
                    forceAsync: true,
                    highlightAsyncPresent: true
                });
            },

            // runtime shape: {
            //   cacheHit: bool,
            //   cacheType: 'exact'|'semantic'|null,
            //   failoverTarget: string|null,
            //   provider,
            //   model,
            //   statusCode: number|null,
            //   responseSuccess: bool,
            //   aiSuccess: bool,
            //   budgetExceeded: bool
            // }
            workflowRuntimeHasCache(runtime) {
                return !!(runtime && runtime.cacheHit);
            },

            workflowRuntimeUsedFailover(runtime) {
                return !!(runtime && runtime.failoverTarget);
            },

            workflowRuntimeBudgetExceeded(runtime) {
                return !!(runtime && runtime.budgetExceeded);
            },

            workflowShowCacheStep(source, runtime) {
                return this.workflowHasCache(source) || this.workflowRuntimeHasCache(runtime);
            },

            workflowCacheNodeClass(runtime) {
                return runtime && runtime.cacheHit ? 'workflow-node-success' : '';
            },

            workflowCacheConnClass(runtime) {
                return runtime && runtime.cacheHit ? 'workflow-conn-hit' : '';
            },

            workflowCacheStatusLabel(runtime) {
                if (!runtime || !runtime.cacheHit) return null;
                if (runtime.cacheType === 'semantic') return 'Hit (Semantic)';
                return 'Hit (Exact)';
            },

            workflowBudgetNodeClass(visible, runtime, highlightPresent) {
                if (!visible) return '';
                if (this.workflowRuntimeBudgetExceeded(runtime)) return 'workflow-node-error';
                return highlightPresent ? 'workflow-node-success' : '';
            },

            workflowBudgetStatusLabel(runtime) {
                return this.workflowRuntimeBudgetExceeded(runtime) ? 'Exceeded' : null;
            },

            workflowFailoverNodeClass(runtime) {
                if (runtime && runtime.cacheHit) return 'workflow-node-skipped';
                return runtime && runtime.failoverTarget ? 'workflow-node-success' : '';
            },

            workflowFailoverConnClass(runtime) {
                if (runtime && runtime.cacheHit) return 'workflow-conn-dim';
                return runtime && runtime.failoverTarget ? 'workflow-conn-hit' : '';
            },

            workflowFailoverStatusLabel(runtime) {
                return runtime && runtime.failoverTarget ? 'Redirected' : null;
            },

            workflowFailoverTargetLabel(runtime) {
                return runtime && runtime.failoverTarget ? runtime.failoverTarget : null;
            },

            workflowAiConnClass(runtime) {
                if (!runtime) return '';
                if (runtime.cacheHit) return 'workflow-conn-dim';
                return '';
            },

            workflowAiNodeClass(runtime) {
                if (!runtime) return '';
                if (runtime.cacheHit) return 'workflow-node-skipped';
                return runtime.aiSuccess ? 'workflow-node-success' : '';
            },

            workflowResponseConnClass(runtime) {
                if (!runtime) return '';
                if (runtime.cacheHit) return 'workflow-conn-dim';
                return '';
            },

            workflowResponseNodeClass(runtime) {
                if (!runtime) return '';
                const statusCode = runtime.statusCode;
                if (!Number.isFinite(statusCode)) return '';
                if (statusCode >= 500) return 'workflow-node-error';
                if (statusCode >= 400) return 'workflow-node-warning';
                if (statusCode >= 300) return 'workflow-node-neutral';
                if (statusCode >= 200) return 'workflow-node-success';
                return '';
            },

            workflowResponseNodeSublabel(runtime) {
                if (!runtime || !Number.isFinite(runtime.statusCode)) return null;
                return String(runtime.statusCode);
            },

            workflowAuthNodeClass(runtime) {
                if (!runtime) return '';
                if (runtime.authError) return 'workflow-node-error';
                if (runtime.authMethod === 'api_key' || runtime.authMethod === 'master_key') return 'workflow-node-success';
                return '';
            },

            workflowAuthNodeSublabel(runtime) {
                if (!runtime || !runtime.authMethod) return null;
                return runtime.authMethod;
            },

            workflowEntryErrorCode(entry) {
                const data = entry && entry.data && typeof entry.data === 'object' && !Array.isArray(entry.data)
                    ? entry.data
                    : {};
                const direct = String(data.error_code || data.errorCode || '').trim();
                if (direct) return direct;
                return this.workflowNestedErrorCode(data.response_body);
            },

            workflowNestedErrorCode(value, depth = 0) {
                if (depth > 4 || value === null || value === undefined) {
                    return '';
                }
                if (typeof value === 'string') {
                    const trimmed = value.trim();
                    if (!trimmed || (trimmed[0] !== '{' && trimmed[0] !== '[')) {
                        return '';
                    }
                    try {
                        return this.workflowNestedErrorCode(JSON.parse(trimmed), depth + 1);
                    } catch (_) {
                        return '';
                    }
                }
                if (Array.isArray(value)) {
                    for (const item of value) {
                        const code = this.workflowNestedErrorCode(item, depth + 1);
                        if (code) return code;
                    }
                    return '';
                }
                if (typeof value !== 'object') {
                    return '';
                }
                const code = String(value.code || '').trim();
                if (code) return code;
                if (value.error !== undefined) {
                    return this.workflowNestedErrorCode(value.error, depth + 1);
                }
                return '';
            },

            workflowAsyncNodeClass(visible, highlightPresent) {
                return visible && highlightPresent ? 'workflow-node-success' : '';
            },

            workflowQualifiedSelectorParts(selector) {
                const raw = String(selector || '').trim();
                if (!raw) return null;
                const slashIndex = raw.indexOf('/');
                if (slashIndex <= 0 || slashIndex >= raw.length - 1) {
                    return null;
                }
                return {
                    provider: raw.slice(0, slashIndex),
                    model: raw.slice(slashIndex + 1)
                };
            },

            workflowPrimaryRouteFromEntry(entry, source) {
                const requestedModel = String(entry && (entry.requested_model || entry.model) || '').trim();
                const failover = this.workflowEntryFailover(entry);
                if (!(failover && failover.targetModel)) {
                    return {
                        provider: String(entry && entry.provider || '').trim() || null,
                        model: requestedModel || null
                    };
                }

                const qualifiedRequested = this.workflowQualifiedSelectorParts(requestedModel);
                if (qualifiedRequested) {
                    return qualifiedRequested;
                }

                const scopeProvider = this.workflowScopeProviderValue(source && source.scope);
                const scopeModel = scopeProvider
                    ? String(source && source.scope && source.scope.scope_model || '').trim()
                    : '';
                if (scopeProvider || scopeModel) {
                    return {
                        provider: scopeProvider || null,
                        model: scopeModel || requestedModel || null
                    };
                }

                return {
                    provider: null,
                    model: requestedModel || null
                };
            },

            workflowRuntimeFromEntry(entry, source) {
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
                const failover = this.workflowEntryFailover(entry);
                const primaryRoute = this.workflowPrimaryRouteFromEntry(entry, source);
                const responseSuccess = Number.isFinite(statusCode) && statusCode >= 200 && statusCode < 300;
                const authError = String(entry.error_type || '').trim().toLowerCase() === 'authentication_error';
                const authMethod = String(entry.auth_method || '').trim().toLowerCase() || null;
                const budgetExceeded = this.workflowEntryErrorCode(entry).toLowerCase() === 'budget_exceeded';
                return {
                    cacheHit,
                    cacheType: normalizedCacheType || null,
                    failoverTarget: failover && failover.targetModel ? failover.targetModel : null,
                    provider: primaryRoute.provider,
                    model: primaryRoute.model,
                    statusCode,
                    responseSuccess,
                    aiSuccess: responseSuccess && !cacheHit,
                    authError,
                    authMethod,
                    budgetExceeded
                };
            },

            async deactivateWorkflow(workflow) {
                const workflowID = String(workflow && workflow.id || '').trim();
                if (!workflowID || this.workflowDeactivatingID || !this.canDeactivateWorkflow(workflow)) {
                    return;
                }
                const workflowName = this.workflowDisplayName(workflow);
                if (typeof global.confirm === 'function' && !global.confirm(
                    'Deactivate workflow "' + workflowName + '"? Requests will fall back to the next active workflow for this scope.'
                )) {
                    return;
                }

                this.workflowError = '';
                this.workflowNotice = '';
                this.workflowDeactivatingID = workflowID;
                try {
                    const request = typeof this.requestOptions === 'function'
                        ? this.requestOptions({
                            method: 'POST',
                        })
                        : {
                            method: 'POST',
                            headers: this.headers()
                        };
                    const res = await fetch('/admin/api/v1/workflows/' + encodeURIComponent(workflowID) + '/deactivate', request);

                    if (res.status === 401) {
                        this.handleFetchResponse(res, 'deactivate workflow', request);
                        return;
                    }
                    if (!res.ok) {
                        this.workflowError = await this.workflowResponseMessage(res, 'Unable to deactivate workflow.');
                        console.error('Failed to deactivate workflow:', res.status, res.statusText, this.workflowError);
                        return;
                    }

                    this.workflowNotice = 'Workflow deactivated.';
                    await this.fetchWorkflowsPage();
                } catch (e) {
                    console.error('Failed to deactivate workflow:', e);
                    this.workflowError = 'Unable to deactivate workflow.';
                } finally {
                    this.workflowDeactivatingID = '';
                }
            }
        };
    }

    global.dashboardWorkflowsModule = dashboardWorkflowsModule;
})(window);
