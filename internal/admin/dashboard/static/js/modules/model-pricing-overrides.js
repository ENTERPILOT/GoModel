(function(global) {
    const PRICE_FIELDS = [
        { value: 'input_per_mtok', label: 'Input $/MTok', group: 'Tokens' },
        { value: 'output_per_mtok', label: 'Output $/MTok', group: 'Tokens' },
        { value: 'cached_input_per_mtok', label: 'Cached input $/MTok', group: 'Tokens' },
        { value: 'cache_write_per_mtok', label: 'Cache write $/MTok', group: 'Tokens' },
        { value: 'reasoning_output_per_mtok', label: 'Reasoning output $/MTok', group: 'Tokens' },
        { value: 'batch_input_per_mtok', label: 'Batch input $/MTok', group: 'Batch' },
        { value: 'batch_output_per_mtok', label: 'Batch output $/MTok', group: 'Batch' },
        { value: 'audio_input_per_mtok', label: 'Audio input $/MTok', group: 'Audio' },
        { value: 'audio_output_per_mtok', label: 'Audio output $/MTok', group: 'Audio' },
        { value: 'per_image', label: '$/Image', group: 'Image' },
        { value: 'input_per_image', label: 'Input $/Image', group: 'Image' },
        { value: 'per_second_input', label: 'Input $/Second', group: 'Audio/Video' },
        { value: 'per_second_output', label: 'Output $/Second', group: 'Video' },
        { value: 'per_character_input', label: '$/Character', group: 'Audio' },
        { value: 'per_page', label: '$/Page', group: 'Utility' },
        { value: 'per_request', label: '$/Request', group: 'Utility' }
    ];

    function dashboardModelPricingOverridesModule() {
        return {
            modelPricingOverridesAvailable: true,
            modelPricingOverrideViews: [],
            modelPricingOverrideError: '',
            modelPricingOverrideNotice: '',
            modelPricingOverrideFormOpen: false,
            modelPricingOverrideSubmitting: false,
            modelPricingOverrideFormHasExistingOverride: false,
            modelPricingOverrideFormDisplayName: '',
            modelPricingOverrideFormScope: '',
            modelPricingOverrideFormScopeOptions: [],
            modelPricingOverrideFormRow: null,
            modelPricingOverrideFormBasePricing: null,
            modelPricingOverrideFormBasePricingSources: null,
            modelPricingOverrideFormPreservedTiers: [],
            modelPricingOverrideRows: [],
            modelPricingOverrideForm: {
                selector: ''
            },

            pricingFieldOptions() {
                return PRICE_FIELDS;
            },

            pricingFieldLabel(field) {
                const option = PRICE_FIELDS.find((item) => item.value === field);
                return option ? option.label : String(field || '').replace(/_/g, ' ');
            },

            async fetchModelPricingOverrides() {
                this.modelPricingOverrideError = '';
                try {
                    const request = typeof this.adminRequestOptions === 'function'
                        ? this.adminRequestOptions()
                        : this.requestOptions();
                    const res = await fetch('/admin/api/v1/model-pricing-overrides', request);
                    if (res.status === 503) {
                        this.modelPricingOverridesAvailable = false;
                        this.modelPricingOverrideViews = [];
                        if (typeof this.syncDisplayModels === 'function') {
                            this.syncDisplayModels();
                        }
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'model pricing overrides', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    this.modelPricingOverridesAvailable = true;
                    if (!handled) {
                        this.modelPricingOverrideViews = [];
                        if (typeof this.syncDisplayModels === 'function') {
                            this.syncDisplayModels();
                        }
                        return;
                    }
                    const payload = await res.json();
                    this.modelPricingOverrideViews = Array.isArray(payload) ? payload : [];
                    if (typeof this.syncDisplayModels === 'function') {
                        this.syncDisplayModels();
                    }
                } catch (e) {
                    console.error('Failed to fetch model pricing overrides:', e);
                    this.modelPricingOverrideViews = [];
                    this.modelPricingOverrideError = 'Unable to load model pricing overrides.';
                    if (typeof this.syncDisplayModels === 'function') {
                        this.syncDisplayModels();
                    }
                }
            },

            modelPricingOverrideMap() {
                const out = new Map();
                for (const override of this.modelPricingOverrideViews) {
                    const selector = String(override && override.selector || '').trim();
                    if (selector) {
                        out.set(selector, override);
                    }
                }
                return out;
            },

            globalPricingOverrideSelector() {
                return '/';
            },

            providerPricingOverrideSelector(providerName) {
                const name = String(providerName || '').trim();
                return name ? name + '/' : '';
            },

            modelPricingModelID(row) {
                return String(row && row.model && row.model.id || '').trim();
            },

            modelPricingExactSelector(row) {
                const providerName = String(row && row.provider_name || '').trim();
                const modelID = this.modelPricingModelID(row);
                if (providerName && modelID) {
                    return providerName + '/' + modelID;
                }
                return modelID;
            },

            modelPricingModelWideSelector(row) {
                return this.modelPricingModelID(row);
            },

            findModelPricingOverrideView(selector) {
                const normalized = String(selector || '').trim();
                if (!normalized) {
                    return null;
                }
                return this.modelPricingOverrideMap().get(normalized) || null;
            },

            hasGlobalPricingOverride() {
                return Boolean(this.findModelPricingOverrideView(this.globalPricingOverrideSelector()));
            },

            hasProviderPricingOverride(group) {
                return Boolean(this.findModelPricingOverrideView(this.providerPricingOverrideSelector(group && group.provider_name)));
            },

            hasModelPricingOverride(row) {
                return Boolean(this.findModelPricingOverrideView(this.modelPricingExactSelector(row)));
            },

            modelPricingButtonClass(hasOverride) {
                return hasOverride ? 'table-action-btn-active' : '';
            },

            modelPricingButtonLabel(subject, hasOverride) {
                const base = 'Edit ' + String(subject || 'model pricing');
                return hasOverride ? base + ' (override exists)' : base;
            },

            matchingModelPricingOverride(row, ignoredSelector) {
                const overrides = this.modelPricingOverrideMap();
                const exact = this.modelPricingExactSelector(row);
                const modelWide = this.modelPricingModelWideSelector(row);
                const providerWide = this.providerPricingOverrideSelector(row && row.provider_name);
                const globalSelector = this.globalPricingOverrideSelector();
                const ignored = String(ignoredSelector || '').trim();
                for (const selector of [exact, modelWide, providerWide, globalSelector]) {
                    if (!selector || selector === ignored) {
                        continue;
                    }
                    const override = overrides.get(selector);
                    if (override) {
                        return override;
                    }
                }
                return null;
            },

            clonePricing(pricing) {
                return pricing && typeof pricing === 'object'
                    ? JSON.parse(JSON.stringify(pricing))
                    : {};
            },

            mergePricing(base, override) {
                const out = this.clonePricing(base);
                const patch = override && override.pricing ? override.pricing : override;
                if (!patch || typeof patch !== 'object') {
                    return out;
                }
                for (const option of PRICE_FIELDS) {
                    if (patch[option.value] !== null && patch[option.value] !== undefined) {
                        out[option.value] = Number(patch[option.value]);
                    }
                }
                if (Array.isArray(patch.tiers) && patch.tiers.length > 0) {
                    out.tiers = this.clonePricing(patch.tiers);
                }
                return out;
            },

            pricingSourcesFromMetadata(metadata) {
                const pricing = metadata && metadata.pricing ? metadata.pricing : {};
                const rawSources = metadata && metadata.pricing_sources && typeof metadata.pricing_sources === 'object'
                    ? metadata.pricing_sources
                    : {};
                const sources = {};
                for (const option of PRICE_FIELDS) {
                    if (pricing[option.value] !== null && pricing[option.value] !== undefined) {
                        sources[option.value] = this.modelPricingSourceLabel(rawSources[option.value] || 'model_registry');
                    }
                }
                return sources;
            },

            modelPricingSourceLabel(source) {
                switch (String(source || '').trim()) {
                case 'config_yaml':
                    return 'config.yaml';
                case 'model_registry':
                    return 'Model registry';
                default:
                    return source ? String(source) : 'Unknown';
                }
            },

            modelPricingOverrideSourceLabel(override) {
                const selector = String(override && override.selector || '').trim();
                return selector ? 'Dashboard/API override (' + selector + ')' : 'Dashboard/API override';
            },

            modelRowPricingState(row, ignoredSelector) {
                const metadata = row && row.model && row.model.metadata ? row.model.metadata : null;
                const pricing = this.clonePricing(metadata && metadata.pricing);
                const sources = this.pricingSourcesFromMetadata(metadata);
                const override = this.matchingModelPricingOverride(row, ignoredSelector);
                const patch = override && override.pricing ? override.pricing : null;
                if (patch) {
                    const overrideSource = this.modelPricingOverrideSourceLabel(override);
                    for (const option of PRICE_FIELDS) {
                        if (patch[option.value] !== null && patch[option.value] !== undefined) {
                            pricing[option.value] = Number(patch[option.value]);
                            sources[option.value] = overrideSource;
                        }
                    }
                    if (Array.isArray(patch.tiers) && patch.tiers.length > 0) {
                        pricing.tiers = this.clonePricing(patch.tiers);
                        sources.tiers = overrideSource;
                    }
                }
                return { pricing, sources };
            },

            modelRowPricing(row) {
                return this.modelRowPricingState(row).pricing;
            },

            modelRowPricingIgnoring(row, ignoredSelector) {
                return this.modelRowPricingState(row, ignoredSelector).pricing;
            },

            openGlobalPricingOverrideEdit() {
                this.openModelPricingOverrideForm({
                    displayName: 'All providers and models',
                    selector: this.globalPricingOverrideSelector(),
                    scope: 'global',
                    scopeOptions: [{ value: 'global', label: 'All providers and models', selector: this.globalPricingOverrideSelector() }],
                    row: null
                });
            },

            openProviderPricingOverrideEdit(group) {
                const selector = this.providerPricingOverrideSelector(group && group.provider_name);
                if (!selector) {
                    return;
                }
                this.openModelPricingOverrideForm({
                    displayName: 'All models in ' + (group.display_name || group.provider_name || selector),
                    selector,
                    scope: 'provider',
                    scopeOptions: [{ value: 'provider', label: 'Provider', selector }],
                    row: null
                });
            },

            openModelPricingOverrideEdit(row) {
                if (!row || row.is_alias) {
                    return;
                }
                const exact = this.modelPricingExactSelector(row);
                const modelWide = this.modelPricingModelWideSelector(row);
                const scopeOptions = [{ value: 'exact', label: 'This provider and model', selector: exact }];
                if (modelWide && modelWide !== exact) {
                    scopeOptions.push({ value: 'model', label: 'This model across providers', selector: modelWide });
                }
                this.openModelPricingOverrideForm({
                    displayName: row.display_name || exact,
                    selector: exact,
                    scope: 'exact',
                    scopeOptions,
                    row
                });
            },

            openModelPricingOverrideForm(options) {
                const opts = options || {};
                this.modelPricingOverrideFormOpen = true;
                this.modelPricingOverrideError = '';
                this.modelPricingOverrideNotice = '';
                this.modelPricingOverrideFormDisplayName = opts.displayName || opts.selector || 'Pricing';
                this.modelPricingOverrideFormScope = opts.scope || '';
                this.modelPricingOverrideFormScopeOptions = Array.isArray(opts.scopeOptions) ? opts.scopeOptions : [];
                this.modelPricingOverrideFormRow = opts.row || null;
                this.modelPricingOverrideForm = { selector: opts.selector || '' };
                this.loadModelPricingOverrideFormSelector(opts.selector || '');
                if (typeof this.focusEditorField === 'function') {
                    this.focusEditorField('modelPricingOverrideEditor');
                }
            },

            loadModelPricingOverrideFormSelector(selector) {
                selector = String(selector || '').trim();
                const override = this.findModelPricingOverrideView(selector);
                this.modelPricingOverrideFormHasExistingOverride = Boolean(override);
                this.modelPricingOverrideRows = this.pricingRowsFromOverride(override);
                this.modelPricingOverrideFormPreservedTiers = override && override.pricing && Array.isArray(override.pricing.tiers)
                    ? this.clonePricing(override.pricing.tiers)
                    : [];
                if (this.modelPricingOverrideRows.length === 0 && this.modelPricingOverrideFormPreservedTiers.length === 0) {
                    this.addModelPricingOverrideRow();
                }
                const row = this.modelPricingOverrideFormRow;
                const state = row ? this.modelRowPricingState(row, selector) : { pricing: {}, sources: {} };
                this.modelPricingOverrideFormBasePricing = state.pricing;
                this.modelPricingOverrideFormBasePricingSources = state.sources;
            },

            setModelPricingOverrideScope(scope) {
                this.modelPricingOverrideFormScope = scope;
                const option = this.modelPricingOverrideFormScopeOptions.find((item) => item.value === scope);
                if (!option) {
                    return;
                }
                this.modelPricingOverrideForm.selector = option.selector;
                this.loadModelPricingOverrideFormSelector(option.selector);
            },

            pricingRowsFromOverride(override) {
                const pricing = override && override.pricing ? override.pricing : {};
                const rows = [];
                for (const option of PRICE_FIELDS) {
                    if (pricing[option.value] !== null && pricing[option.value] !== undefined) {
                        rows.push({
                            id: this.nextModelPricingOverrideRowID(),
                            field: option.value,
                            value: String(pricing[option.value])
                        });
                    }
                }
                return rows;
            },

            nextModelPricingOverrideRowID() {
                this._modelPricingOverrideRowID = (this._modelPricingOverrideRowID || 0) + 1;
                return 'pricing-row-' + this._modelPricingOverrideRowID;
            },

            selectedPricingFields(exceptID) {
                const fields = new Set();
                for (const row of this.modelPricingOverrideRows) {
                    if (exceptID && row.id === exceptID) {
                        continue;
                    }
                    const field = String(row.field || '').trim();
                    if (field) {
                        fields.add(field);
                    }
                }
                return fields;
            },

            availablePricingFieldOptions(row) {
                const selected = this.selectedPricingFields(row && row.id);
                return PRICE_FIELDS.filter((option) => option.value === (row && row.field) || !selected.has(option.value));
            },

            addModelPricingOverrideRow() {
                const selected = this.selectedPricingFields();
                const option = PRICE_FIELDS.find((item) => !selected.has(item.value)) || PRICE_FIELDS[0];
                if (!option) {
                    return;
                }
                this.modelPricingOverrideRows.push({
                    id: this.nextModelPricingOverrideRowID(),
                    field: option.value,
                    value: ''
                });
            },

            removeModelPricingOverrideRow(row) {
                this.modelPricingOverrideRows = this.modelPricingOverrideRows.filter((item) => item.id !== row.id);
                if (this.modelPricingOverrideRows.length === 0 && this.modelPricingOverrideFormPreservedTiers.length === 0) {
                    this.addModelPricingOverrideRow();
                }
            },

            modelPricingOverridePayload() {
                const pricing = {};
                const seen = new Set();
                for (const row of this.modelPricingOverrideRows) {
                    const field = String(row.field || '').trim();
                    if (!field) {
                        return { error: 'Choose a price type for every row.' };
                    }
                    if (seen.has(field)) {
                        return { error: 'Each price type can only be used once.' };
                    }
                    seen.add(field);
                    const raw = String(row.value || '').trim();
                    if (raw === '') {
                        return { error: 'Enter a value for ' + this.pricingFieldLabel(field) + '.' };
                    }
                    const value = Number(raw);
                    if (!Number.isFinite(value) || value < 0) {
                        return { error: 'Pricing values must be numbers greater than or equal to 0.' };
                    }
                    pricing[field] = value;
                }
                if (this.modelPricingOverrideFormPreservedTiers.length > 0) {
                    pricing.tiers = this.clonePricing(this.modelPricingOverrideFormPreservedTiers);
                }
                if (Object.keys(pricing).length === 0) {
                    return { error: 'Add at least one pricing field before saving.' };
                }
                return { pricing };
            },

            modelPricingOverrideDraftPricing() {
                const payload = this.modelPricingOverridePayload();
                return payload && payload.pricing ? payload.pricing : {};
            },

            modelPricingEffectivePreviewRows() {
                const base = this.modelPricingOverrideFormBasePricing || {};
                const baseSources = this.modelPricingOverrideFormBasePricingSources || {};
                const draft = this.modelPricingOverrideDraftPricing();
                const effective = this.mergePricing(base, draft);
                return PRICE_FIELDS.map((option) => {
                    const hasDraft = draft[option.value] !== null && draft[option.value] !== undefined;
                    const hasBase = base[option.value] !== null && base[option.value] !== undefined;
                    return {
                        field: option.value,
                        label: option.label,
                        value: effective[option.value],
                        source: hasDraft ? 'Form/API value' : (hasBase ? (baseSources[option.value] || 'Model registry') : 'Unset')
                    };
                }).filter((row) => row.source !== 'Unset' || row.value !== undefined);
            },

            closeModelPricingOverrideForm() {
                this.modelPricingOverrideFormOpen = false;
                this.modelPricingOverrideSubmitting = false;
                this.modelPricingOverrideError = '';
                this.modelPricingOverrideFormHasExistingOverride = false;
                this.modelPricingOverrideFormDisplayName = '';
                this.modelPricingOverrideFormScope = '';
                this.modelPricingOverrideFormScopeOptions = [];
                this.modelPricingOverrideFormRow = null;
                this.modelPricingOverrideFormBasePricing = null;
                this.modelPricingOverrideFormBasePricingSources = null;
                this.modelPricingOverrideFormPreservedTiers = [];
                this.modelPricingOverrideRows = [];
                this.modelPricingOverrideForm = { selector: '' };
            },

            async submitModelPricingOverrideForm() {
                const selector = String(this.modelPricingOverrideForm.selector || '').trim();
                if (!selector) {
                    this.modelPricingOverrideError = 'Model pricing selector is required.';
                    return;
                }
                const payload = this.modelPricingOverridePayload();
                if (payload.error) {
                    this.modelPricingOverrideError = payload.error;
                    return;
                }
                const requestPayload = { selector, ...payload };

                this.modelPricingOverrideSubmitting = true;
                this.modelPricingOverrideError = '';
                this.modelPricingOverrideNotice = '';
                try {
                    const request = typeof this.adminRequestOptions === 'function'
                        ? this.adminRequestOptions({ method: 'PUT', body: JSON.stringify(requestPayload) })
                        : this.requestOptions({ method: 'PUT', body: JSON.stringify(requestPayload) });
                    const res = await fetch('/admin/api/v1/model-pricing-overrides', request);
                    if (res.status === 503) {
                        this.modelPricingOverridesAvailable = false;
                        this.modelPricingOverrideError = 'Model pricing overrides feature is unavailable.';
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'model pricing override', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.modelPricingOverrideError = res.status === 401
                            ? 'Authentication required.'
                            : await this.aliasResponseMessage(res, 'Failed to save model pricing.');
                        return;
                    }
                    this.modelPricingOverridesAvailable = true;
                    await this.fetchModelPricingOverrides();
                    if (typeof this.syncDisplayModels === 'function') {
                        this.syncDisplayModels();
                    }
                    this.closeModelPricingOverrideForm();
                    this.modelPricingOverrideNotice = 'Model pricing saved.';
                } catch (e) {
                    console.error('Failed to save model pricing override:', e);
                    this.modelPricingOverrideError = 'Failed to save model pricing.';
                } finally {
                    this.modelPricingOverrideSubmitting = false;
                }
            },

            async deleteModelPricingOverride() {
                const selector = String(this.modelPricingOverrideForm.selector || '').trim();
                if (!selector || !this.modelPricingOverrideFormHasExistingOverride) {
                    return;
                }
                if (!window.confirm('Remove the model pricing override for "' + selector + '"?')) {
                    return;
                }

                this.modelPricingOverrideSubmitting = true;
                this.modelPricingOverrideError = '';
                this.modelPricingOverrideNotice = '';
                try {
                    const request = typeof this.adminRequestOptions === 'function'
                        ? this.adminRequestOptions({ method: 'DELETE', body: JSON.stringify({ selector }) })
                        : this.requestOptions({ method: 'DELETE', body: JSON.stringify({ selector }) });
                    const res = await fetch('/admin/api/v1/model-pricing-overrides', request);
                    if (res.status === 503) {
                        this.modelPricingOverridesAvailable = false;
                        this.modelPricingOverrideError = 'Model pricing overrides feature is unavailable.';
                        return;
                    }
                    if (res.status !== 404) {
                        const handled = this.handleFetchResponse(res, 'model pricing override', request);
                        if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                            return;
                        }
                        if (!handled) {
                            this.modelPricingOverrideError = res.status === 401
                                ? 'Authentication required.'
                                : await this.aliasResponseMessage(res, 'Failed to remove model pricing override.');
                            return;
                        }
                    }
                    this.modelPricingOverridesAvailable = true;
                    await this.fetchModelPricingOverrides();
                    if (typeof this.syncDisplayModels === 'function') {
                        this.syncDisplayModels();
                    }
                    this.closeModelPricingOverrideForm();
                    this.modelPricingOverrideNotice = 'Model pricing override removed.';
                } catch (e) {
                    console.error('Failed to delete model pricing override:', e);
                    this.modelPricingOverrideError = 'Failed to remove model pricing override.';
                } finally {
                    this.modelPricingOverrideSubmitting = false;
                }
            }
        };
    }

    global.dashboardModelPricingOverridesModule = dashboardModelPricingOverridesModule;
})(typeof window !== 'undefined' ? window : globalThis);
