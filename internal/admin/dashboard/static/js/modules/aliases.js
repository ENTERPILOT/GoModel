(function(global) {
    function dashboardAliasesModule() {
        return {
            aliases: [],
            aliasesAvailable: true,
            modelOverridesAvailable: true,
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
                enabled: false,
                force_disabled: false,
                allowed_only_for_user_paths: ''
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
                    enabled: false,
                    force_disabled: false,
                    allowed_only_for_user_paths: ''
                };
            },

            normalizeModelOverridePaths(raw) {
                return String(raw || '')
                    .split(/\r?\n|,/)
                    .map((value) => String(value || '').trim())
                    .filter(Boolean);
            },

            openModelOverrideEdit(row) {
                if (!row || row.is_alias) {
                    return;
                }

                const access = row.access || {};
                const override = access.override || null;
                const allowedPaths = override && Array.isArray(override.allowed_only_for_user_paths)
                    ? override.allowed_only_for_user_paths
                    : (Array.isArray(access.allowed_only_for_user_paths) ? access.allowed_only_for_user_paths : []);

                this.modelOverrideFormOpen = true;
                this.modelOverrideError = '';
                this.modelOverrideNotice = '';
                this.modelOverrideFormHasExistingOverride = Boolean(override);
                this.modelOverrideFormDefaultEnabled = access.default_enabled !== false;
                this.modelOverrideFormEffectiveEnabled = access.effective_enabled !== false;
                this.modelOverrideFormDisplayName = row.display_name || this.qualifiedModelName(row) || '';
                this.modelOverrideForm = {
                    selector: access.selector || this.qualifiedModelName(row),
                    enabled: Boolean(override && override.enabled === true),
                    force_disabled: Boolean(override && override.force_disabled),
                    allowed_only_for_user_paths: allowedPaths.join('\n')
                };
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

            modelAccessStateText(access) {
                if (!access) return 'Default';
                if (access.force_disabled) return 'Force Disabled';
                if (access.effective_enabled === false) {
                    return access.default_enabled === false ? 'Disabled by Default' : 'Disabled';
                }
                if (access.override && access.override.enabled === true && access.default_enabled === false) {
                    return 'Explicitly Enabled';
                }
                if (Array.isArray(access.allowed_only_for_user_paths) && access.allowed_only_for_user_paths.length > 0) {
                    return 'Restricted';
                }
                return 'Enabled';
            },

            modelAccessStateClass(access) {
                if (!access) return '';
                if (access.force_disabled || access.effective_enabled === false) return 'is-disabled';
                if (Array.isArray(access.allowed_only_for_user_paths) && access.allowed_only_for_user_paths.length > 0) {
                    return 'is-restricted';
                }
                return 'is-enabled';
            },

            modelAccessSummary(access) {
                if (!access) {
                    return '';
                }

                const parts = [];
                if (access.force_disabled) {
                    parts.push('Force disabled globally');
                } else if (access.effective_enabled === false) {
                    parts.push(access.default_enabled === false ? 'Disabled until explicitly enabled' : 'Disabled');
                } else if (access.override && access.override.enabled === true && access.default_enabled === false) {
                    parts.push('Explicitly enabled');
                }

                const allowed = Array.isArray(access.allowed_only_for_user_paths) ? access.allowed_only_for_user_paths : [];
                if (allowed.length > 0) {
                    parts.push('Allowed only for ' + allowed.join(', '));
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
                const allowedOnlyForUserPaths = this.normalizeModelOverridePaths(this.modelOverrideForm.allowed_only_for_user_paths);
                const forceDisabled = Boolean(this.modelOverrideForm.force_disabled);
                const enabled = Boolean(this.modelOverrideForm.enabled) && !forceDisabled;

                if (!selector) {
                    this.modelOverrideError = 'Model selector is required.';
                    return;
                }
                if (!enabled && !forceDisabled && allowedOnlyForUserPaths.length === 0) {
                    this.modelOverrideError = this.modelOverrideFormHasExistingOverride
                        ? 'Choose an access policy or remove the override.'
                        : 'Choose an access policy before saving.';
                    return;
                }

                this.modelOverrideSubmitting = true;
                this.modelOverrideError = '';
                this.modelOverrideNotice = '';

                const payload = {
                    force_disabled: forceDisabled,
                    allowed_only_for_user_paths: allowedOnlyForUserPaths
                };
                if (enabled) {
                    payload.enabled = true;
                }

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

                    await this.fetchModels();
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
                if (!window.confirm('Remove the model override for "' + selector + '"?')) {
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

                    await this.fetchModels();
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
