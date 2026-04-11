(function(global) {
    function dashboardAliasesModule() {
        return {
            aliases: [],
            aliasesAvailable: true,
            modelOverridesAvailable: true,
            modelOverrideViews: [],
            displayModels: [],
            aliasLoading: false,
            aliasError: '',
            aliasFormError: '',
            aliasNotice: '',
            aliasFormOpen: false,
            aliasSubmitting: false,
            aliasTogglingName: '',
            aliasDeletingName: '',
            aliasFormMode: 'create',
            aliasFormOriginalName: '',
            aliasForm: {
                name: '',
                target_model: '',
                description: '',
                enabled: true
            },
            modelOverrideFormOpen: false,
            modelOverrideSubmitting: false,
            modelOverrideError: '',
            modelOverrideNotice: '',
            modelOverrideFormHasExistingOverride: false,
            modelOverrideFormDefaultEnabled: true,
            modelOverrideFormEffectiveEnabled: true,
            modelOverrideFormDisplayName: '',
            modelOverrideForm: {
                selector: '',
                user_paths: ''
            },

            buildDisplayModels() {
                const rows = this.models.map((model) => ({
                    key: 'model:' + this.qualifiedModelName(model),
                    display_name: this.qualifiedModelName(model),
                    secondary_name: '',
                    provider_name: model.provider_name || '',
                    provider_type: model.provider_type || '',
                    model: model.model,
                    is_alias: false,
                    alias: null,
                    access: model && model.access ? model.access : null,
                    kind_badge: '',
                    masking_alias: null,
                    alias_state_class: '',
                    alias_state_text: ''
                }));

                if (!this.aliasesAvailable) {
                    return rows;
                }

                const maskingAliases = new Map();
                for (const alias of this.aliases) {
                    const aliasName = String(alias && alias.name || '').trim().toLowerCase();
                    if (!aliasName || alias.enabled === false || !alias.valid) {
                        continue;
                    }
                    maskingAliases.set(aliasName, alias);
                }

                for (const row of rows) {
                    for (const key of this.modelIdentifierKeys(
                        row.model && row.model.id,
                        row.provider_type,
                        row.provider_name,
                        row.display_name
                    )) {
                        if (maskingAliases.has(key)) {
                            row.masking_alias = maskingAliases.get(key);
                            break;
                        }
                    }
                }

                for (const alias of this.aliases) {
                    const targetModel = this.findConcreteModelForAlias(alias);
                    if (!targetModel && this.activeCategory && this.activeCategory !== 'all') {
                        continue;
                    }

                    rows.push({
                        key: 'alias:' + alias.name,
                        display_name: alias.name,
                        secondary_name: this.aliasTargetLabel(alias),
                        provider_name: targetModel ? (targetModel.provider_name || '') : '',
                        provider_type: targetModel ? (targetModel.provider_type || alias.provider_type || '') : (alias.provider_type || ''),
                        model: targetModel ? targetModel.model : { id: alias.name, object: 'model' },
                        is_alias: true,
                        alias,
                        access: null,
                        kind_badge: 'Alias',
                        masking_alias: null,
                        alias_state_class: this.aliasStateClass(alias),
                        alias_state_text: this.aliasStateText(alias)
                    });
                }

                return rows.sort((a, b) => {
                    if (a.is_alias !== b.is_alias) {
                        return a.is_alias ? -1 : 1;
                    }
                    return String(a.display_name || '').localeCompare(String(b.display_name || ''));
                });
            },

            syncDisplayModels() {
                this.displayModels = this.buildDisplayModels();
            },

            get filteredDisplayModels() {
                if (!this.modelFilter) return this.displayModels;
                const filter = this.modelFilter.toLowerCase();
                return this.displayModels.filter((row) => {
                    const fields = [
                        row.display_name,
                        row.secondary_name,
                        row.provider_name,
                        row.provider_type,
                        row.model && row.model.owned_by,
                        row.alias && row.alias.description,
                        row.alias && row.alias_state_text,
                        row.model && row.model.metadata && row.model.metadata.modes ? row.model.metadata.modes.join(',') : '',
                        row.model && row.model.metadata && row.model.metadata.categories ? row.model.metadata.categories.join(',') : ''
                    ];
                    return fields.some((value) => String(value || '').toLowerCase().includes(filter));
                });
            },

            get filteredDisplayModelGroups() {
                return this.groupDisplayModels(this.filteredDisplayModels);
            },

            defaultAliasForm() {
                return {
                    name: '',
                    target_model: '',
                    description: '',
                    enabled: true
                };
            },

            async fetchAliases() {
                this.aliasLoading = true;
                this.aliasError = '';
                try {
                    const res = await fetch('/admin/api/v1/aliases', { headers: this.headers() });
                    if (res.status === 503) {
                        this.aliasesAvailable = false;
                        this.aliases = [];
                        this.syncDisplayModels();
                        return;
                    }
                    this.aliasesAvailable = true;
                    if (!this.handleFetchResponse(res, 'aliases')) {
                        this.aliases = [];
                        this.syncDisplayModels();
                        return;
                    }
                    const payload = await res.json();
                    this.aliases = Array.isArray(payload) ? payload : [];
                    this.syncDisplayModels();
                } catch (e) {
                    console.error('Failed to fetch aliases:', e);
                    this.aliases = [];
                    this.aliasError = 'Unable to load aliases.';
                    this.syncDisplayModels();
                } finally {
                    this.aliasLoading = false;
                }
            },

            async fetchModelOverrides() {
                this.modelOverrideError = '';
                try {
                    const res = await fetch('/admin/api/v1/model-overrides', { headers: this.headers() });
                    if (res.status === 503) {
                        this.modelOverridesAvailable = false;
                        this.modelOverrideViews = [];
                        this.syncDisplayModels();
                        return;
                    }
                    this.modelOverridesAvailable = true;
                    if (!this.handleFetchResponse(res, 'model overrides')) {
                        this.modelOverrideViews = [];
                        this.syncDisplayModels();
                        return;
                    }
                    const payload = await res.json();
                    this.modelOverrideViews = Array.isArray(payload) ? payload : [];
                    this.syncDisplayModels();
                } catch (e) {
                    console.error('Failed to fetch model overrides:', e);
                    this.modelOverrideViews = [];
                    this.modelOverrideError = 'Unable to load model overrides.';
                    this.syncDisplayModels();
                }
            },

            groupDisplayModels(rows) {
                if (!Array.isArray(rows) || rows.length === 0) {
                    return [];
                }

                const overridesBySelector = new Map();
                for (const override of this.modelOverrideViews) {
                    const selector = String(override && override.selector || '').trim();
                    if (selector) {
                        overridesBySelector.set(selector, override);
                    }
                }

                const groups = new Map();
                for (const row of rows) {
                    const providerName = String(row && row.provider_name || '').trim();
                    const providerType = String(row && row.provider_type || '').trim();
                    const key = providerName || providerType || 'unassigned';
                    if (!groups.has(key)) {
                        groups.set(key, {
                            key: 'provider-group:' + key,
                            provider_name: providerName,
                            provider_type: providerType,
                            display_name: this.providerGroupDisplayName(providerName, providerType),
                            type_label: this.providerGroupTypeLabel(providerName, providerType),
                            rows: []
                        });
                    }

                    const group = groups.get(key);
                    if (!group.provider_name && providerName) {
                        group.provider_name = providerName;
                    }
                    if (!group.provider_type && providerType) {
                        group.provider_type = providerType;
                    }
                    group.display_name = this.providerGroupDisplayName(group.provider_name, group.provider_type);
                    group.type_label = this.providerGroupTypeLabel(group.provider_name, group.provider_type);
                    group.rows.push(row);
                }

                return Array.from(groups.values())
                    .map((group) => {
                        const access = this.providerGroupAccess(group.provider_name, group.provider_type, overridesBySelector);
                        return {
                            ...group,
                            access,
                            access_summary: this.modelAccessSummary(access),
                            item_count_label: this.providerGroupItemCountLabel(group.rows)
                        };
                    })
                    .sort((a, b) => String(a.display_name || '').localeCompare(String(b.display_name || '')));
            },

            providerGroupDisplayName(providerName, providerType) {
                const normalizedProviderName = String(providerName || '').trim();
                if (normalizedProviderName) {
                    return normalizedProviderName;
                }
                const normalizedProviderType = String(providerType || '').trim();
                if (normalizedProviderType) {
                    return normalizedProviderType;
                }
                return 'Unassigned';
            },

            providerGroupTypeLabel(providerName, providerType) {
                const normalizedProviderName = String(providerName || '').trim();
                const normalizedProviderType = String(providerType || '').trim();
                if (!normalizedProviderType || normalizedProviderType === normalizedProviderName) {
                    return '';
                }
                return normalizedProviderType;
            },

            providerOverrideSelector(providerName) {
                const normalizedProviderName = String(providerName || '').trim();
                if (!normalizedProviderName) {
                    return '';
                }
                return normalizedProviderName + '/';
            },

            globalOverrideSelector() {
                return '/';
            },

            hasGlobalModelOverride() {
                return Boolean(this.findModelOverrideView(this.globalOverrideSelector()));
            },

            providerGroupDefaultEnabled(providerName, providerType) {
                const normalizedProviderName = String(providerName || '').trim();
                const normalizedProviderType = String(providerType || '').trim();
                for (const model of this.models) {
                    const modelProviderName = String(model && model.provider_name || '').trim();
                    const modelProviderType = String(model && model.provider_type || '').trim();
                    if (normalizedProviderName && modelProviderName !== normalizedProviderName) {
                        continue;
                    }
                    if (!normalizedProviderName && normalizedProviderType && modelProviderType !== normalizedProviderType) {
                        continue;
                    }
                    if (model && model.access) {
                        return model.access.default_enabled !== false;
                    }
                }
                return true;
            },

            modelOverridesDefaultEnabled() {
                for (const model of this.models) {
                    if (model && model.access) {
                        return model.access.default_enabled !== false;
                    }
                }
                return true;
            },

            findModelOverrideView(selector) {
                const normalizedSelector = String(selector || '').trim();
                if (!normalizedSelector) {
                    return null;
                }
                for (const override of this.modelOverrideViews) {
                    if (String(override && override.selector || '').trim() === normalizedSelector) {
                        return override;
                    }
                }
                return null;
            },

            hasAccessOverride(access) {
                return Boolean(access && access.override);
            },

            modelOverrideEditButtonClass(hasOverride) {
                return hasOverride ? 'table-action-btn-active' : '';
            },

            modelOverrideEditButtonLabel(subject, hasOverride) {
                const base = 'Edit ' + String(subject || 'model access');
                return hasOverride ? base + ' (override exists)' : base;
            },

            providerGroupAccess(providerName, providerType, overridesBySelector) {
                const selector = this.providerOverrideSelector(providerName);
                const globalOverride = overridesBySelector ? (overridesBySelector.get(this.globalOverrideSelector()) || null) : null;
                const override = selector && overridesBySelector ? (overridesBySelector.get(selector) || null) : null;
                const defaultEnabled = this.providerGroupDefaultEnabled(providerName, providerType);
                const inheritedOverride = override || globalOverride;
                const userPaths = inheritedOverride && Array.isArray(inheritedOverride.user_paths)
                    ? Array.from(new Set(inheritedOverride.user_paths)).sort()
                    : [];

                return {
                    selector,
                    default_enabled: defaultEnabled,
                    effective_enabled: Boolean(inheritedOverride) || defaultEnabled,
                    user_paths: userPaths,
                    override
                };
            },

            providerGroupItemCountLabel(rows) {
                const safeRows = Array.isArray(rows) ? rows : [];
                const modelCount = safeRows.filter((row) => row && !row.is_alias).length;
                const aliasCount = safeRows.filter((row) => row && row.is_alias).length;
                const parts = [];
                if (modelCount > 0) {
                    parts.push(modelCount + (modelCount === 1 ? ' model' : ' models'));
                }
                if (aliasCount > 0) {
                    parts.push(aliasCount + (aliasCount === 1 ? ' alias' : ' aliases'));
                }
                return parts.join(' · ');
            },

            qualifiedModelName(model) {
                if (!model) {
                    return '';
                }
                const selector = String(model.selector || '').trim();
                if (selector) {
                    return selector;
                }
                if (!model.model || !model.model.id) {
                    return '';
                }
                const modelID = String(model.model.id || '').trim();
                const providerName = String(model.provider_name || '').trim();
                if (providerName) {
                    return providerName + '/' + modelID;
                }
                const providerType = String(model.provider_type || '').trim();
                if (!providerType || modelID.includes('/')) {
                    return modelID;
                }
                return providerType + '/' + modelID;
            },

            rowAccessSelector(row) {
                if (!row) {
                    return '';
                }
                const accessSelector = String(row.access && row.access.selector || '').trim();
                if (accessSelector) {
                    return accessSelector;
                }
                const overrideSelector = String(row.override_selector || '').trim();
                if (overrideSelector) {
                    return overrideSelector;
                }
                return this.qualifiedModelName(row);
            },

            displayRowClass(row) {
                if (!row) return '';
                const classes = [];
                if (row.is_alias) {
                    classes.push('alias-row', this.aliasStateClass(row.alias));
                }
                if (!row.is_alias && row.masking_alias) {
                    classes.push('masked-model-row');
                }
                if (!row.is_alias && row.access && row.access.effective_enabled === false) {
                    classes.push('model-access-disabled-row');
                }
                return classes.join(' ');
            },

            rowAnchorID(row) {
                if (!row) return '';
                if (row.is_alias && row.alias && row.alias.name) {
                    return 'alias-row-' + String(row.alias.name).replace(/[^a-zA-Z0-9_-]+/g, '-');
                }
                return '';
            },

            filterByAlias(aliasName) {
                this.modelFilter = String(aliasName || '').trim();
            },

            openAliasCreate(model) {
                this.aliasFormOpen = true;
                this.aliasFormMode = 'create';
                this.aliasFormOriginalName = '';
                this.aliasFormError = '';
                this.aliasNotice = '';
                this.aliasForm = this.defaultAliasForm();
                if (model && model.model && model.model.id) {
                    this.aliasForm.target_model = this.qualifiedModelName(model);
                }
            },

            openAliasEdit(alias) {
                this.aliasFormOpen = true;
                this.aliasFormMode = 'edit';
                this.aliasFormOriginalName = alias.name || '';
                this.aliasFormError = '';
                this.aliasNotice = '';
                this.aliasForm = {
                    name: alias.name || '',
                    target_model: alias.target_provider ? alias.target_provider + '/' + alias.target_model : (alias.target_model || ''),
                    description: alias.description || '',
                    enabled: alias.enabled !== false
                };
            },

            closeAliasForm() {
                this.aliasFormOpen = false;
                this.aliasFormMode = 'create';
                this.aliasFormOriginalName = '';
                this.aliasFormError = '';
                this.aliasForm = this.defaultAliasForm();
            },

            defaultModelOverrideForm() {
                return {
                    selector: '',
                    user_paths: ''
                };
            },

            normalizeModelOverridePaths(raw) {
                return String(raw || '')
                    .split(/\r?\n|,/)
                    .map((value) => String(value || '').trim())
                    .filter(Boolean);
            },

            scrollToModelOverrideForm() {
                const scroll = () => {
                    const refs = this.$refs || {};
                    const editor = refs.modelOverrideEditor || null;
                    if (!editor || typeof editor.scrollIntoView !== 'function') {
                        return;
                    }
                    editor.scrollIntoView({ behavior: 'smooth', block: 'start' });
                };

                const scrollAfterPaint = () => {
                    if (typeof global.requestAnimationFrame === 'function') {
                        global.requestAnimationFrame(scroll);
                        return;
                    }
                    scroll();
                };

                if (typeof this.$nextTick === 'function') {
                    this.$nextTick(scrollAfterPaint);
                    return;
                }
                scrollAfterPaint();
            },

            openModelOverrideEdit(row) {
                if (!row || row.is_alias) {
                    return;
                }

                const access = row.access || {};
                const override = access.override || null;
                const userPaths = override && Array.isArray(override.user_paths)
                    ? override.user_paths
                    : (Array.isArray(access.user_paths) ? access.user_paths : []);

                this.modelOverrideFormOpen = true;
                this.modelOverrideError = '';
                this.modelOverrideNotice = '';
                this.modelOverrideFormHasExistingOverride = Boolean(override);
                this.modelOverrideFormDefaultEnabled = access.default_enabled !== false;
                this.modelOverrideFormEffectiveEnabled = access.effective_enabled !== false;
                this.modelOverrideFormDisplayName = row.access_display_name || row.display_name || this.qualifiedModelName(row) || '';
                this.modelOverrideForm = {
                    selector: this.rowAccessSelector(row),
                    user_paths: userPaths.join('\n')
                };
                this.scrollToModelOverrideForm();
            },

            openGlobalModelOverrideEdit() {
                const selector = this.globalOverrideSelector();
                const override = this.findModelOverrideView(selector);
                const userPaths = override && Array.isArray(override.user_paths)
                    ? override.user_paths
                    : [];
                const defaultEnabled = this.modelOverridesDefaultEnabled();

                this.modelOverrideFormOpen = true;
                this.modelOverrideError = '';
                this.modelOverrideNotice = '';
                this.modelOverrideFormHasExistingOverride = Boolean(override);
                this.modelOverrideFormDefaultEnabled = defaultEnabled;
                this.modelOverrideFormEffectiveEnabled = Boolean(override) || defaultEnabled;
                this.modelOverrideFormDisplayName = 'All providers and models';
                this.modelOverrideForm = {
                    selector,
                    user_paths: userPaths.join('\n')
                };
                this.scrollToModelOverrideForm();
            },

            openProviderOverrideEdit(group) {
                if (!group || !group.access || !group.access.selector) {
                    return;
                }

                this.openModelOverrideEdit({
                    display_name: group.display_name,
                    access_display_name: 'All models in ' + group.display_name,
                    provider_name: group.provider_name,
                    provider_type: group.provider_type,
                    access: group.access,
                    override_selector: group.access.selector,
                    is_alias: false
                });
            },

            closeModelOverrideForm() {
                this.modelOverrideFormOpen = false;
                this.modelOverrideSubmitting = false;
                this.modelOverrideError = '';
                this.modelOverrideFormHasExistingOverride = false;
                this.modelOverrideFormDefaultEnabled = true;
                this.modelOverrideFormEffectiveEnabled = true;
                this.modelOverrideFormDisplayName = '';
                this.modelOverrideForm = this.defaultModelOverrideForm();
            },

            async aliasResponseMessage(res, fallback) {
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

            aliasTargetLabel(alias) {
                if (!alias) return '\u2014';
                if (alias.resolved_model) return alias.resolved_model;
                if (alias.target_provider) return alias.target_provider + '/' + alias.target_model;
                return alias.target_model || '\u2014';
            },

            aliasStateClass(alias) {
                if (alias.enabled === false) return 'is-disabled';
                if (!alias.valid) return 'is-invalid';
                return 'is-valid';
            },

            aliasStateText(alias) {
                if (alias.enabled === false) return 'Disabled';
                if (!alias.valid) return 'Invalid';
                return 'Active';
            },

            modelAccessUserPathsRestrict(paths) {
                return Array.isArray(paths) && paths.length > 0 && paths.indexOf('/') === -1;
            },

            modelAccessStateText(access) {
                if (!this.modelOverridesAvailable) return '';
                if (!access) return 'Default';
                if (access.effective_enabled === false) {
                    return access.default_enabled === false ? 'Disabled by Default' : 'Disabled';
                }
                if (this.modelAccessUserPathsRestrict(access.user_paths)) {
                    return 'Restricted';
                }
                return 'Enabled';
            },

            modelAccessStateClass(access) {
                if (!this.modelOverridesAvailable) return '';
                if (!access) return '';
                if (access.effective_enabled === false) return 'is-disabled';
                if (this.modelAccessUserPathsRestrict(access.user_paths)) {
                    return 'is-restricted';
                }
                return 'is-enabled';
            },

            modelAccessSummary(access) {
                if (!access) {
                    return '';
                }

                const parts = [];
                if (access.effective_enabled === false) {
                    parts.push(access.default_enabled === false ? 'Disabled by default' : 'Disabled');
                }

                const userPaths = Array.isArray(access.user_paths) ? access.user_paths : [];
                if (userPaths.length > 0) {
                    parts.push('Allowed for ' + userPaths.join(', '));
                }

                return parts.join(' · ');
            },

            async toggleAliasEnabled(alias) {
                if (!alias || !alias.name || this.aliasTogglingName === alias.name) {
                    return;
                }

                this.aliasTogglingName = alias.name;
                this.aliasError = '';
                this.aliasNotice = '';
                this.aliasFormError = '';

                const payload = {
                    target_model: alias.target_provider ? alias.target_provider + '/' + alias.target_model : alias.target_model,
                    description: String(alias.description || '').trim(),
                    enabled: alias.enabled === false
                };

                try {
                    const res = await fetch('/admin/api/v1/aliases/' + encodeURIComponent(alias.name), {
                        method: 'PUT',
                        headers: this.headers(),
                        body: JSON.stringify(payload)
                    });
                    if (res.status === 503) {
                        this.aliasesAvailable = false;
                        this.aliasError = 'Aliases feature is unavailable.';
                        return;
                    }
                    if (res.status === 401) {
                        this.authError = true;
                        this.needsAuth = true;
                        this.aliasError = 'Authentication required.';
                        return;
                    }
                    if (!res.ok) {
                        this.aliasError = await this.aliasResponseMessage(res, 'Failed to update alias state.');
                        return;
                    }

                    await this.fetchAliases();
                    this.aliasNotice = payload.enabled ? 'Alias enabled.' : 'Alias disabled.';
                    if (this.aliasFormOpen && this.aliasFormOriginalName === alias.name) {
                        this.closeAliasForm();
                    }
                } catch (e) {
                    console.error('Failed to toggle alias state:', e);
                    this.aliasError = 'Failed to update alias state.';
                } finally {
                    this.aliasTogglingName = '';
                }
            },

            modelKeys(model) {
                return this.modelIdentifierKeys(
                    model && model.model ? model.model.id : '',
                    model ? model.provider_type : '',
                    model ? model.provider_name : '',
                    model ? model.selector : ''
                );
            },

            modelIdentifierKeys(modelID, providerType, providerName, selector) {
                const keys = new Set();
                const normalizedModelID = String(modelID || '').trim().toLowerCase();
                const provider = String(providerType || '').trim().toLowerCase();
                const providerLabel = String(providerName || '').trim().toLowerCase();
                const normalizedSelector = String(selector || '').trim().toLowerCase();
                if (normalizedSelector) {
                    keys.add(normalizedSelector);
                }
                if (!normalizedModelID) {
                    return keys;
                }

                keys.add(normalizedModelID);
                if (providerLabel) {
                    keys.add(providerLabel + '/' + normalizedModelID);
                }
                if (provider && !normalizedModelID.includes('/')) {
                    keys.add(provider + '/' + normalizedModelID);
                }

                const parts = normalizedModelID.split('/');
                if (parts.length === 2 && parts[1]) {
                    keys.add(parts[1]);
                }

                return keys;
            },

            aliasKeys(alias) {
                const keys = new Set();
                const resolved = String(alias.resolved_model || '').trim().toLowerCase();
                const targetModel = String(alias.target_model || '').trim().toLowerCase();
                const targetProvider = String(alias.target_provider || '').trim().toLowerCase();
                if (resolved) {
                    keys.add(resolved);
                    const resolvedParts = resolved.split('/');
                    if (resolvedParts.length === 2 && resolvedParts[1]) {
                        keys.add(resolvedParts[1]);
                    }
                }
                if (targetModel) {
                    keys.add(targetModel);
                    const targetParts = targetModel.split('/');
                    if (targetParts.length === 2 && targetParts[1]) {
                        keys.add(targetParts[1]);
                    }
                }
                if (targetModel && targetProvider) {
                    keys.add(targetProvider + '/' + targetModel);
                }
                return keys;
            },

            findConcreteModelForAlias(alias) {
                for (const model of this.models) {
                    const modelKeys = this.modelKeys(model);
                    for (const key of this.aliasKeys(alias)) {
                        if (modelKeys.has(key)) {
                            return model;
                        }
                    }
                }
                return null;
            },

            normalizedAliasName(name) {
                return String(name || '').trim().toLowerCase();
            },

            sameAliasName(left, right) {
                const normalizedLeft = this.normalizedAliasName(left);
                const normalizedRight = this.normalizedAliasName(right);
                return normalizedLeft !== '' && normalizedLeft === normalizedRight;
            },

            findExistingAliasByName(name) {
                const normalizedName = this.normalizedAliasName(name);
                if (!normalizedName) {
                    return null;
                }

                for (const alias of this.aliases) {
                    if (this.sameAliasName(alias && alias.name, normalizedName)) {
                        return alias;
                    }
                }
                return null;
            },

            findConcreteModelByName(name) {
                const normalizedName = this.normalizedAliasName(name);
                if (!normalizedName) {
                    return null;
                }

                for (const model of this.models) {
                    if (this.modelKeys(model).has(normalizedName)) {
                        return model;
                    }
                }
                return null;
            },

            async submitAliasForm() {
                const name = String(this.aliasForm.name || '').trim();
                const targetModel = String(this.aliasForm.target_model || '').trim();
                let originalName = String(this.aliasFormOriginalName || '').trim();

                if (!name) {
                    this.aliasFormError = 'Alias name is required.';
                    return;
                }
                if (!targetModel) {
                    this.aliasFormError = 'Target model is required.';
                    return;
                }

                this.aliasFormError = '';
                this.aliasError = '';
                this.aliasNotice = '';

                const existingAlias = this.findExistingAliasByName(name);
                if (existingAlias && !this.sameAliasName(existingAlias.name, originalName)) {
                    const overwriteMessage = originalName
                        ? 'An alias named "' + existingAlias.name + '" already exists. Saving will overwrite it and remove "' + originalName + '". Continue?'
                        : 'An alias named "' + existingAlias.name + '" already exists. Saving will update that alias. Continue?';
                    if (!window.confirm(overwriteMessage)) {
                        this.aliasFormError = originalName
                            ? 'Choose a different alias name to avoid overwriting an existing alias.'
                            : 'Choose a different alias name or use Edit on the existing alias.';
                        return;
                    }
                    if (!originalName) {
                        this.aliasFormMode = 'edit';
                        this.aliasFormOriginalName = existingAlias.name || name;
                        originalName = this.aliasFormOriginalName;
                    }
                }

                const matchingModel = this.findConcreteModelByName(name);
                if (!existingAlias && matchingModel && !this.sameAliasName(name, originalName)) {
                    const modelName = this.qualifiedModelName(matchingModel) || String(matchingModel.model && matchingModel.model.id || '').trim();
                    if (!window.confirm('A model named "' + modelName + '" already exists. Creating this alias will mask that model in the list. Continue?')) {
                        this.aliasFormError = 'Choose a different alias name to avoid masking an existing model.';
                        return;
                    }
                }

                this.aliasSubmitting = true;

                const payload = {
                    target_model: targetModel,
                    description: String(this.aliasForm.description || '').trim(),
                    enabled: Boolean(this.aliasForm.enabled)
                };

                try {
                    const saveRes = await fetch('/admin/api/v1/aliases/' + encodeURIComponent(name), {
                        method: 'PUT',
                        headers: this.headers(),
                        body: JSON.stringify(payload)
                    });

                    if (saveRes.status === 503) {
                        this.aliasesAvailable = false;
                        this.aliasFormError = 'Aliases feature is unavailable.';
                        return;
                    }
                    if (saveRes.status === 401) {
                        this.authError = true;
                        this.needsAuth = true;
                        this.aliasFormError = 'Authentication required.';
                        return;
                    }
                    if (!saveRes.ok) {
                        this.aliasFormError = await this.aliasResponseMessage(saveRes, 'Failed to save alias.');
                        return;
                    }

                    if (originalName && originalName !== name) {
                        const deleteRes = await fetch('/admin/api/v1/aliases/' + encodeURIComponent(originalName), {
                            method: 'DELETE',
                            headers: this.headers()
                        });
                        if (deleteRes.status !== 404 && !deleteRes.ok) {
                            this.aliasFormError = await this.aliasResponseMessage(deleteRes, 'The alias was saved with the new name, but the previous alias could not be removed.');
                            await this.fetchAliases();
                            return;
                        }
                    }

                    await this.fetchAliases();
                    this.closeAliasForm();
                    this.aliasNotice = originalName && originalName !== name ? 'Alias renamed.' : 'Alias saved.';
                } catch (e) {
                    console.error('Failed to save alias:', e);
                    this.aliasFormError = 'Failed to save alias.';
                } finally {
                    this.aliasSubmitting = false;
                }
            },

            async deleteAlias(alias) {
                if (!alias || !alias.name) return;
                if (!window.confirm('Delete alias "' + alias.name + '"? This cannot be undone.')) {
                    return;
                }

                this.aliasDeletingName = alias.name;
                this.aliasError = '';
                this.aliasNotice = '';
                this.aliasFormError = '';

                try {
                    const res = await fetch('/admin/api/v1/aliases/' + encodeURIComponent(alias.name), {
                        method: 'DELETE',
                        headers: this.headers()
                    });
                    if (res.status === 503) {
                        this.aliasesAvailable = false;
                        this.aliasError = 'Aliases feature is unavailable.';
                        return;
                    }
                    if (res.status === 401) {
                        this.authError = true;
                        this.needsAuth = true;
                        this.aliasError = 'Authentication required.';
                        return;
                    }
                    if (!res.ok) {
                        this.aliasError = await this.aliasResponseMessage(res, 'Failed to remove alias.');
                        return;
                    }

                    await this.fetchAliases();
                    if (this.aliasFormOriginalName === alias.name) {
                        this.closeAliasForm();
                    }
                    this.aliasNotice = 'Alias removed.';
                } catch (e) {
                    console.error('Failed to delete alias:', e);
                    this.aliasError = 'Failed to remove alias.';
                } finally {
                    this.aliasDeletingName = '';
                }
            },

            async submitModelOverrideForm() {
                const selector = String(this.modelOverrideForm.selector || '').trim();
                const userPaths = this.normalizeModelOverridePaths(this.modelOverrideForm.user_paths);

                if (!selector) {
                    this.modelOverrideError = 'Model selector is required.';
                    return;
                }
                if (userPaths.length === 0) {
                    this.modelOverrideError = this.modelOverrideFormHasExistingOverride
                        ? 'Enter at least one user path or remove the override.'
                        : 'Enter at least one user path before saving.';
                    return;
                }

                this.modelOverrideSubmitting = true;
                this.modelOverrideError = '';
                this.modelOverrideNotice = '';

                const payload = { user_paths: userPaths };

                try {
                    const res = await fetch('/admin/api/v1/model-overrides/' + encodeURIComponent(selector), {
                        method: 'PUT',
                        headers: this.headers(),
                        body: JSON.stringify(payload)
                    });
                    if (res.status === 503) {
                        this.modelOverridesAvailable = false;
                        this.modelOverrideError = 'Model overrides feature is unavailable.';
                        return;
                    }
                    this.modelOverridesAvailable = true;
                    if (res.status === 401) {
                        this.authError = true;
                        this.needsAuth = true;
                        this.modelOverrideError = 'Authentication required.';
                        return;
                    }
                    if (!res.ok) {
                        this.modelOverrideError = await this.aliasResponseMessage(res, 'Failed to save model access.');
                        return;
                    }

                    await Promise.all([this.fetchModels(), this.fetchModelOverrides()]);
                    this.closeModelOverrideForm();
                    this.modelOverrideNotice = 'Model access saved.';
                } catch (e) {
                    console.error('Failed to save model override:', e);
                    this.modelOverrideError = 'Failed to save model access.';
                } finally {
                    this.modelOverrideSubmitting = false;
                }
            },

            async deleteModelOverride() {
                const selector = String(this.modelOverrideForm.selector || '').trim();
                if (!selector || !this.modelOverrideFormHasExistingOverride) {
                    return;
                }
                if (!window.confirm('Remove the model override for "' + selector + '"? This will revert access to inherited/default behavior.')) {
                    return;
                }

                this.modelOverrideSubmitting = true;
                this.modelOverrideError = '';
                this.modelOverrideNotice = '';

                try {
                    const res = await fetch('/admin/api/v1/model-overrides/' + encodeURIComponent(selector), {
                        method: 'DELETE',
                        headers: this.headers()
                    });
                    if (res.status === 503) {
                        this.modelOverridesAvailable = false;
                        this.modelOverrideError = 'Model overrides feature is unavailable.';
                        return;
                    }
                    this.modelOverridesAvailable = true;
                    if (res.status === 401) {
                        this.authError = true;
                        this.needsAuth = true;
                        this.modelOverrideError = 'Authentication required.';
                        return;
                    }
                    if (res.status !== 404 && !res.ok) {
                        this.modelOverrideError = await this.aliasResponseMessage(res, 'Failed to remove model override.');
                        return;
                    }

                    await Promise.all([this.fetchModels(), this.fetchModelOverrides()]);
                    this.closeModelOverrideForm();
                    this.modelOverrideNotice = 'Model override removed.';
                } catch (e) {
                    console.error('Failed to delete model override:', e);
                    this.modelOverrideError = 'Failed to remove model override.';
                } finally {
                    this.modelOverrideSubmitting = false;
                }
            }
        };
    }

    global.dashboardAliasesModule = dashboardAliasesModule;
})(window);
