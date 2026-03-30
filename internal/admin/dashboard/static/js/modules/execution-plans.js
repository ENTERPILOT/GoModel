(function(global) {
    function dashboardExecutionPlansModule() {
        return {
            executionPlans: [],
            executionPlansAvailable: true,
            executionPlansLoading: false,
            executionPlanError: '',
            executionPlanNotice: '',
            executionPlanFilter: '',
            executionPlanFormOpen: false,
            executionPlanSubmitting: false,
            executionPlanDeactivatingID: '',
            executionPlanFormError: '',
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
                    guardrails: false
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
                        guardrails: false
                    },
                    guardrails: []
                };
            },

            defaultExecutionPlanGuardrailStep(step) {
                return {
                    ref: '',
                    step: Number.isFinite(step) ? step : 10
                };
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

            executionPlanSourceFeatures(source) {
                const raw = source && source.plan_payload && source.plan_payload.features
                    ? source.plan_payload.features
                    : source && source.features
                        ? source.features
                        : {};
                return {
                    cache: !!raw.cache,
                    audit: !!raw.audit,
                    usage: !!raw.usage,
                    guardrails: !!raw.guardrails
                };
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
                        step: Number(step && step.step)
                    }))
                    .filter((step) => Number.isFinite(step.step));
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
                    this.executionPlanHydratedScope = {
                        scope_provider: '',
                        scope_model: '',
                        scope_user_path: ''
                    };
                    this.executionPlanForm = this.defaultExecutionPlanForm();
                    this.scrollExecutionPlanFormIntoView();
                    return;
                }

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
                        guardrails: !!features.guardrails
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

            buildExecutionPlanRequest() {
                const form = this.executionPlanForm || this.defaultExecutionPlanForm();
                const provider = String(form.scope_provider || '').trim();
                const model = provider ? String(form.scope_model || '').trim() : '';
                const userPath = String(form.scope_user_path || '').trim();
                const features = form.features || {};

                const guardrails = !!features.guardrails
                    ? (Array.isArray(form.guardrails) ? form.guardrails : []).map((step) => {
                        const rawStep = step && step.step;
                        const trimmedStep = rawStep === null || rawStep === undefined ? '' : String(rawStep).trim();
                        const parsedStep = trimmedStep !== '' && Number.isFinite(Number(trimmedStep))
                            ? Number(trimmedStep)
                            : Number.NaN;
                        return {
                            ref: String(step && step.ref || '').trim(),
                            step: parsedStep
                        };
                    })
                    : [];

                return {
                    scope_provider: provider,
                    scope_model: model,
                    scope_user_path: userPath,
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
            },

            validateExecutionPlanRequest(payload) {
                const preservedProvider = String(this.executionPlanHydratedScope && this.executionPlanHydratedScope.scope_provider || '').trim();
                const preservedModel = String(this.executionPlanHydratedScope && this.executionPlanHydratedScope.scope_model || '').trim();
                const userPath = String(payload.scope_user_path || '').trim();

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
                if (userPath && !userPath.startsWith('/')) {
                    return 'Path scopes must start with /.';
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
                await Promise.all([this.fetchExecutionPlans(), this.fetchExecutionPlanGuardrails()]);
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

            // runtime shape: { cacheHit: bool|null, cacheType: 'exact'|'semantic'|null, provider, model }
            epRuntimeHasCache(runtime) {
                return !!(runtime && runtime.cacheHit !== null && runtime.cacheHit !== undefined);
            },

            epShowCacheStep(source, runtime) {
                return this.epHasCache(source) || this.epRuntimeHasCache(runtime);
            },

            epCacheNodeClass(runtime) {
                if (!this.epRuntimeHasCache(runtime)) return '';
                if (runtime.cacheHit && runtime.cacheType === 'semantic') return 'ep-node-cache-semantic';
                if (runtime.cacheHit) return 'ep-node-cache-hit';
                return 'ep-node-cache-miss';
            },

            epCacheConnClass(runtime) {
                if (!this.epRuntimeHasCache(runtime)) return '';
                return runtime.cacheHit ? 'ep-conn-hit' : '';
            },

            epCacheStatusLabel(runtime) {
                if (!this.epRuntimeHasCache(runtime)) return null;
                if (runtime.cacheHit && runtime.cacheType === 'semantic') return 'Semantic';
                return runtime.cacheHit ? 'Hit' : 'Miss';
            },

            epAiConnClass(runtime) {
                if (!this.epRuntimeHasCache(runtime)) return '';
                return runtime.cacheHit ? 'ep-conn-dim' : '';
            },

            epAiNodeClass(runtime) {
                if (!this.epRuntimeHasCache(runtime)) return '';
                return runtime.cacheHit ? 'ep-node-ai-skipped' : '';
            },

            epRuntimeFromEntry(entry) {
                if (!entry) return null;
                const cacheHit = (entry.cache_hit !== undefined && entry.cache_hit !== null)
                    ? !!entry.cache_hit
                    : null;
                return {
                    cacheHit,
                    cacheType: entry.cache_type || null,
                    provider: entry.provider || null,
                    model: entry.model || null
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
