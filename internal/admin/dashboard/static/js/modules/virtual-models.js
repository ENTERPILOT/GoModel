(function(global) {
    function dashboardVirtualModelsModule() {
        return {
            // Unified virtual-models state. `aliases` holds redirect Views mapped to
            // the renderer shape; `modelOverrideViews` holds policy Views.
            virtualModelsAvailable: true,
            aliases: [],
            modelOverrideViews: [],
            displayModels: [],
            aliasLoading: false,
            aliasError: '',
            aliasNotice: '',
            rowTogglingKey: '',

            // Unified editor (replaces the old alias modal and access-override modal).
            vmFormOpen: false,
            vmFormMode: 'create',
            vmFormError: '',
            vmSubmitting: false,
            vmDeleting: false,
            vmFormHasExisting: false,
            vmFormDefaultEnabled: true,
            vmFormEffectiveEnabled: true,
            vmFormDisplayName: '',
            vmFormSourceLocked: false,
            vmFormOriginalSource: '',
            vmForm: {
                source: '',
                target_model: '',
                user_paths: '',
                description: '',
                enabled: true
            },

            buildDisplayModels() {
                const rows = this.models.map((model) => ({
                    key: 'model:' + this.qualifiedModelName(model),
                    display_name: this.qualifiedModelName(model),
                    secondary_name: '',
                    provider_name: model.provider_name || '',
                    provider_type: model.provider_type || '',
                    model: model.model,
                    selector: model.selector || '',
                    is_alias: false,
                    alias: null,
                    access: model && model.access ? model.access : null,
                    kind_badge: '',
                    masking_alias: null,
                    has_virtual_model: false,
                    alias_state_class: '',
                    alias_state_text: ''
                }));

                if (!this.virtualModelsAvailable) {
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
                    // A real model row carries a virtual model when an access policy
                    // override exists for its selector.
                    if (row.access && row.access.override) {
                        row.has_virtual_model = true;
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
                        selector: '',
                        is_alias: true,
                        alias,
                        access: null,
                        kind_badge: 'Alias',
                        masking_alias: null,
                        has_virtual_model: true,
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

            defaultVirtualModelForm() {
                return {
                    source: '',
                    target_model: '',
                    user_paths: '',
                    description: '',
                    enabled: true
                };
            },

            adminRequestOptions(options) {
                return typeof this.requestOptions === 'function'
                    ? this.requestOptions(options)
                    : { ...(options || {}), headers: this.headers() };
            },

            // mapRedirectView maps a redirect View into the shape the renderer needs.
            mapRedirectView(view) {
                const target = Array.isArray(view.targets) && view.targets.length > 0 ? view.targets[0] : {};
                return {
                    name: view.source,
                    target_provider: target.provider || '',
                    target_model: target.model || '',
                    description: view.description || '',
                    enabled: view.enabled !== false,
                    valid: Boolean(view.valid),
                    resolved_model: view.resolved_model || '',
                    provider_type: view.provider_type || '',
                    user_paths: Array.isArray(view.user_paths) ? view.user_paths : []
                };
            },

            applyVirtualModelViews(views) {
                const safeViews = Array.isArray(views) ? views : [];
                const aliases = [];
                const policies = [];
                for (const view of safeViews) {
                    if (!view || typeof view !== 'object') {
                        continue;
                    }
                    if (view.kind === 'redirect') {
                        aliases.push(this.mapRedirectView(view));
                    } else if (view.kind === 'policy') {
                        policies.push({
                            selector: view.source,
                            provider_name: view.provider_name || '',
                            model: view.model || '',
                            user_paths: Array.isArray(view.user_paths) ? view.user_paths : [],
                            description: view.description || '',
                            enabled: view.enabled !== false,
                            scope_kind: view.scope_kind || ''
                        });
                    }
                }
                this.aliases = aliases;
                this.modelOverrideViews = policies;
            },

            async fetchVirtualModels() {
                this.aliasLoading = true;
                this.aliasError = '';
                try {
                    const request = this.adminRequestOptions();
                    const res = await fetch('/admin/virtual-models', request);
                    if (res.status === 503) {
                        this.setVirtualModelsAvailable(false);
                        this.aliases = [];
                        this.modelOverrideViews = [];
                        this.syncDisplayModels();
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'virtual models', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    this.setVirtualModelsAvailable(true);
                    if (!handled) {
                        this.aliases = [];
                        this.modelOverrideViews = [];
                        this.syncDisplayModels();
                        return;
                    }
                    const payload = await res.json();
                    this.applyVirtualModelViews(payload);
                    this.syncDisplayModels();
                } catch (e) {
                    console.error('Failed to fetch virtual models:', e);
                    this.aliases = [];
                    this.modelOverrideViews = [];
                    this.aliasError = 'Unable to load virtual models.';
                    this.syncDisplayModels();
                } finally {
                    this.aliasLoading = false;
                }
            },

            setVirtualModelsAvailable(available) {
                const value = Boolean(available);
                this.virtualModelsAvailable = value;
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

            // globalScopeRow exposes the global "/" scope as a toggle row, so the
            // global level reuses the same enable/restrict/disable switch as models,
            // aliases, and provider groups.
            get globalScopeRow() {
                const override = this.findModelOverrideView(this.globalOverrideSelector());
                const defaultEnabled = this.modelOverridesDefaultEnabled();
                const userPaths = override && Array.isArray(override.user_paths) ? override.user_paths : [];
                return {
                    key: 'scope-global',
                    is_alias: false,
                    display_name: 'all providers and models',
                    access: {
                        selector: this.globalOverrideSelector(),
                        default_enabled: defaultEnabled,
                        effective_enabled: override ? (override.enabled !== false) : defaultEnabled,
                        user_paths: userPaths,
                        override
                    }
                };
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
                return hasOverride ? base + ' (virtual model exists)' : base;
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
                    // Honor the override's enabled VALUE, not just its presence: a
                    // disabled policy turns the selector off even though an override
                    // exists.
                    effective_enabled: inheritedOverride ? (inheritedOverride.enabled !== false) : defaultEnabled,
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
                } else if (row.has_virtual_model) {
                    // Real model rows carrying a virtual model render alias-like so
                    // operators can spot them at a glance.
                    classes.push('alias-row', 'is-valid');
                }
                if (!row.is_alias && row.masking_alias) {
                    classes.push('masked-model-row');
                }
                if (!row.is_alias && row.access && row.access.effective_enabled === false) {
                    classes.push('model-access-disabled-row');
                }
                return classes.join(' ');
            },

            // rowVirtualBadge returns the small badge label shown on a real model row
            // that carries a virtual model (empty for plain rows and alias rows, which
            // already show their own Alias badge).
            rowVirtualBadge(row) {
                if (!row || row.is_alias || !row.has_virtual_model) {
                    return '';
                }
                return 'Override';
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

            focusEditorField(refName) {
                const focus = () => {
                    const refs = this.$refs || {};
                    const editor = refs[refName] || null;
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

            // ---- Unified virtual-model editor ----

            // openVirtualModelCreate opens the editor in create mode with an editable
            // Source. Optionally prefills the Target model from a model row.
            openVirtualModelCreate(model) {
                this.resetVirtualModelForm();
                this.vmFormOpen = true;
                this.vmFormMode = 'create';
                this.vmFormSourceLocked = false;
                this.vmFormDisplayName = 'New virtual model';
                if (model && model.model && model.model.id) {
                    this.vmForm.target_model = this.qualifiedModelName(model);
                }
                this.focusEditorField('virtualModelEditor');
            },

            // openVirtualModelEditAlias edits an existing redirect/alias. Source is
            // locked (renaming happens only through create flows).
            openVirtualModelEditAlias(alias) {
                if (!alias) {
                    return;
                }
                this.resetVirtualModelForm();
                this.vmFormOpen = true;
                this.vmFormMode = 'edit';
                this.vmFormSourceLocked = true;
                this.vmFormHasExisting = true;
                this.vmFormOriginalSource = alias.name || '';
                this.vmFormDisplayName = alias.name || '';
                this.vmForm = {
                    source: alias.name || '',
                    target_model: alias.target_provider ? alias.target_provider + '/' + alias.target_model : (alias.target_model || ''),
                    user_paths: (Array.isArray(alias.user_paths) ? alias.user_paths : []).join('\n'),
                    description: alias.description || '',
                    enabled: alias.enabled !== false
                };
                this.focusEditorField('virtualModelEditor');
            },

            // openVirtualModelEditModel edits the virtual model attached to a real
            // model row (or seeds a policy for it). Source is locked and prefilled
            // with the model selector.
            openVirtualModelEditModel(row) {
                if (!row || row.is_alias) {
                    return;
                }
                const access = row.access || {};
                const override = access.override || null;
                const userPaths = override && Array.isArray(override.user_paths)
                    ? override.user_paths
                    : (Array.isArray(access.user_paths) ? access.user_paths : []);
                const selector = this.rowAccessSelector(row);

                this.resetVirtualModelForm();
                this.vmFormOpen = true;
                this.vmFormMode = 'edit';
                this.vmFormSourceLocked = true;
                this.vmFormHasExisting = Boolean(override);
                this.vmFormOriginalSource = selector;
                // Prefer the override's own enabled value; fall back to the backend
                // effective state only when no override row exists for this selector.
                const overrideEnabled = override ? (override.enabled !== false) : (access.effective_enabled !== false);
                this.vmFormDefaultEnabled = access.default_enabled !== false;
                this.vmFormEffectiveEnabled = overrideEnabled;
                this.vmFormDisplayName = row.access_display_name || row.display_name || selector || '';
                this.vmForm = {
                    source: selector,
                    target_model: '',
                    user_paths: userPaths.join('\n'),
                    description: override && override.description ? override.description : '',
                    enabled: overrideEnabled
                };
                this.focusEditorField('virtualModelEditor');
            },

            openGlobalModelOverrideEdit() {
                const selector = this.globalOverrideSelector();
                const override = this.findModelOverrideView(selector);
                const userPaths = override && Array.isArray(override.user_paths)
                    ? override.user_paths
                    : [];
                const defaultEnabled = this.modelOverridesDefaultEnabled();

                this.resetVirtualModelForm();
                this.vmFormOpen = true;
                this.vmFormMode = 'edit';
                this.vmFormSourceLocked = true;
                this.vmFormHasExisting = Boolean(override);
                this.vmFormOriginalSource = selector;
                this.vmFormDefaultEnabled = defaultEnabled;
                this.vmFormEffectiveEnabled = override ? (override.enabled !== false) : defaultEnabled;
                this.vmFormDisplayName = 'All providers and models';
                this.vmForm = {
                    source: selector,
                    target_model: '',
                    user_paths: userPaths.join('\n'),
                    description: override && override.description ? override.description : '',
                    enabled: override ? override.enabled !== false : defaultEnabled
                };
                this.focusEditorField('virtualModelEditor');
            },

            openProviderOverrideEdit(group) {
                if (!group || !group.access || !group.access.selector) {
                    return;
                }
                this.openVirtualModelEditModel({
                    display_name: group.display_name,
                    access_display_name: 'All models in ' + group.display_name,
                    provider_name: group.provider_name,
                    provider_type: group.provider_type,
                    access: group.access,
                    override_selector: group.access.selector,
                    is_alias: false
                });
            },

            resetVirtualModelForm() {
                this.vmFormError = '';
                this.aliasNotice = '';
                this.aliasError = '';
                this.vmSubmitting = false;
                this.vmDeleting = false;
                this.vmFormHasExisting = false;
                this.vmFormDefaultEnabled = true;
                this.vmFormEffectiveEnabled = true;
                this.vmFormDisplayName = '';
                this.vmFormSourceLocked = false;
                this.vmFormOriginalSource = '';
                this.vmForm = this.defaultVirtualModelForm();
            },

            closeVirtualModelForm() {
                this.vmFormOpen = false;
                this.resetVirtualModelForm();
            },

            normalizeUserPaths(raw) {
                return String(raw || '')
                    .split(/\r?\n|,/)
                    .map((value) => String(value || '').trim())
                    .filter(Boolean);
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
                if (!alias) return '—';
                if (alias.resolved_model) return alias.resolved_model;
                if (alias.target_provider) return alias.target_provider + '/' + alias.target_model;
                return alias.target_model || '—';
            },

            aliasStateClass(alias) {
                if (!alias) return 'is-invalid';
                if (alias.enabled === false) return 'is-disabled';
                if (!alias.valid) return 'is-invalid';
                return 'is-valid';
            },

            aliasStateText(alias) {
                if (!alias) return 'Invalid';
                if (alias.enabled === false) return 'Disabled';
                if (!alias.valid) return 'Invalid';
                return 'Active';
            },

            modelAccessUserPathsRestrict(paths) {
                return Array.isArray(paths) && paths.length > 0 && paths.indexOf('/') === -1;
            },

            modelAccessStateClass(access) {
                if (!this.virtualModelsAvailable) return '';
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

            // ---- Row enable/disable toggle (real models and aliases) ----

            rowToggleEnabled(row) {
                if (!row) {
                    return false;
                }
                if (row.is_alias) {
                    return row.alias && row.alias.enabled !== false;
                }
                return Boolean(row.access && row.access.effective_enabled !== false);
            },

            rowToggleLabel(row) {
                if (this.rowTogglingKey && this.rowTogglingKey === row.key) {
                    return 'Updating...';
                }
                if (this.rowToggleRestricted(row)) {
                    return 'Restricted';
                }
                return this.rowToggleEnabled(row) ? 'Enabled' : 'Disabled';
            },

            // A model that is enabled but restricted to specific user paths shows
            // "Restricted" (in the accent color) on the toggle itself, so no
            // separate access-state pill is needed.
            rowToggleRestricted(row) {
                return Boolean(row) && !row.is_alias && this.modelAccessStateClass(row.access) === 'is-restricted';
            },

            // rowToggleAriaLabel describes the toggle for any scope: alias, model
            // row, provider group, or the global "/" scope.
            rowToggleAriaLabel(row) {
                if (!row) {
                    return '';
                }
                const action = this.rowToggleEnabled(row) ? 'Disable ' : 'Enable ';
                let subject;
                if (row.is_alias) {
                    subject = 'alias ' + String(row.alias && row.alias.name || '');
                } else {
                    subject = String(row.display_name || (row.access && row.access.selector) || 'model');
                }
                return action + subject.trim();
            },

            // The edit-modal status switch reuses the same .alias-toggle component
            // and three states, derived from the form's own fields: it is
            // Restricted when enabled and scoped to non-global user paths.
            vmFormToggleRestricted() {
                return Boolean(this.vmForm && this.vmForm.enabled)
                    && this.modelAccessUserPathsRestrict(this.normalizeUserPaths(this.vmForm.user_paths));
            },

            vmFormToggleLabel() {
                if (!this.vmForm || !this.vmForm.enabled) {
                    return 'Disabled';
                }
                return this.vmFormToggleRestricted() ? 'Restricted' : 'Enabled';
            },

            async toggleRowEnabled(row) {
                if (!this.virtualModelsAvailable) {
                    return;
                }
                if (!row || this.rowTogglingKey === row.key) {
                    return;
                }
                if (row.is_alias) {
                    await this.toggleAliasRow(row);
                    return;
                }
                await this.toggleModelRow(row);
            },

            async toggleAliasRow(row) {
                const alias = row.alias;
                if (!alias || !alias.name) {
                    return;
                }

                this.rowTogglingKey = row.key;
                this.aliasError = '';
                this.aliasNotice = '';

                const targetModel = alias.target_provider
                    ? alias.target_provider + '/' + alias.target_model
                    : alias.target_model;
                const payload = {
                    source: alias.name,
                    target_model: targetModel,
                    description: String(alias.description || '').trim(),
                    user_paths: Array.isArray(alias.user_paths) ? alias.user_paths : [],
                    enabled: alias.enabled === false
                };

                try {
                    const request = this.adminRequestOptions({
                        method: 'PUT',
                        body: JSON.stringify(payload)
                    });
                    const res = await fetch('/admin/virtual-models', request);
                    if (res.status === 503) {
                        this.setVirtualModelsAvailable(false);
                        this.aliasError = 'Virtual models feature is unavailable.';
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'alias state', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.aliasError = res.status === 401
                            ? 'Authentication required.'
                            : await this.aliasResponseMessage(res, 'Failed to update alias state.');
                        return;
                    }

                    await this.fetchVirtualModels();
                    this.aliasNotice = payload.enabled ? 'Alias enabled.' : 'Alias disabled.';
                } catch (e) {
                    console.error('Failed to toggle alias state:', e);
                    this.aliasError = 'Failed to update alias state.';
                } finally {
                    this.rowTogglingKey = '';
                }
            },

            async toggleModelRow(row) {
                const access = row.access || {};
                const selector = this.rowAccessSelector(row);
                if (!selector) {
                    return;
                }
                const existingPolicy = this.findModelOverrideView(selector);
                const desired = !(access.effective_enabled !== false);
                const existingPaths = existingPolicy && Array.isArray(existingPolicy.user_paths)
                    ? existingPolicy.user_paths
                    : [];

                this.rowTogglingKey = row.key;
                this.aliasError = '';
                this.aliasNotice = '';

                let method = 'PUT';
                let payload;
                if (desired === false) {
                    payload = { source: selector, enabled: false, user_paths: existingPaths };
                } else if (existingPolicy && existingPaths.length === 0 && access.default_enabled !== false) {
                    // Removing a path-less policy only enables the model when the
                    // default is on; in a default-disabled deployment we must keep an
                    // explicit enabled policy instead of falling back to the default.
                    method = 'DELETE';
                    payload = { source: selector };
                } else {
                    payload = { source: selector, enabled: true, user_paths: existingPaths };
                }

                try {
                    const request = this.adminRequestOptions({
                        method,
                        body: JSON.stringify(payload)
                    });
                    const res = await fetch('/admin/virtual-models', request);
                    if (res.status === 503) {
                        this.setVirtualModelsAvailable(false);
                        this.aliasError = 'Virtual models feature is unavailable.';
                        return;
                    }
                    if (!(method === 'DELETE' && res.status === 404)) {
                        const handled = this.handleFetchResponse(res, 'model access', request);
                        if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                            return;
                        }
                        if (!handled) {
                            this.aliasError = res.status === 401
                                ? 'Authentication required.'
                                : await this.aliasResponseMessage(res, 'Failed to update model access.');
                            return;
                        }
                    }

                    await Promise.all([this.fetchModels(), this.fetchVirtualModels()]);
                    this.syncDisplayModels();
                    this.aliasNotice = desired ? 'Model enabled.' : 'Model disabled.';
                } catch (e) {
                    console.error('Failed to toggle model access:', e);
                    this.aliasError = 'Failed to update model access.';
                } finally {
                    this.rowTogglingKey = '';
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

            // submitVirtualModelForm saves the unified editor: a filled Target model
            // makes a redirect/alias, an empty one makes an access policy.
            async submitVirtualModelForm() {
                const source = String(this.vmForm.source || '').trim();
                const targetModel = String(this.vmForm.target_model || '').trim();
                const userPaths = this.normalizeUserPaths(this.vmForm.user_paths);

                if (!source) {
                    this.vmFormError = 'Source is required.';
                    return;
                }

                this.vmFormError = '';
                this.aliasError = '';
                this.aliasNotice = '';

                // In create mode warn about clobbering an existing virtual model
                // (alias or access policy) on the same source, or masking a concrete
                // model. Edit mode locks the source so none of these apply. The
                // overwrite check must run even with an empty target, since that is a
                // policy upsert that can still replace an existing redirect/policy row.
                if (this.vmFormMode !== 'edit') {
                    const existingAlias = this.findExistingAliasByName(source);
                    const existingPolicy = existingAlias ? null : this.findModelOverrideView(source);
                    if (existingAlias || existingPolicy) {
                        const overwriteMessage = existingAlias
                            ? 'An alias named "' + existingAlias.name + '" already exists. Saving will update that virtual model. Continue?'
                            : 'An access policy for "' + source + '" already exists. Saving will update that virtual model. Continue?';
                        if (!this.confirmAction(overwriteMessage)) {
                            this.vmFormError = 'Choose a different source or edit the existing virtual model.';
                            return;
                        }
                    } else if (targetModel) {
                        const matchingModel = this.findConcreteModelByName(source);
                        if (matchingModel) {
                            const modelName = this.qualifiedModelName(matchingModel) || String(matchingModel.model && matchingModel.model.id || '').trim();
                            if (!this.confirmAction('A model named "' + modelName + '" already exists. Creating this alias will mask that model in the list. Continue?')) {
                                this.vmFormError = 'Choose a different source to avoid masking an existing model.';
                                return;
                            }
                        }
                    }
                }

                this.vmSubmitting = true;

                const payload = {
                    source,
                    user_paths: userPaths,
                    description: String(this.vmForm.description || '').trim(),
                    enabled: Boolean(this.vmForm.enabled)
                };
                if (targetModel) {
                    payload.target_model = targetModel;
                }

                try {
                    const request = this.adminRequestOptions({
                        method: 'PUT',
                        body: JSON.stringify(payload)
                    });
                    const res = await fetch('/admin/virtual-models', request);
                    if (res.status === 503) {
                        this.setVirtualModelsAvailable(false);
                        this.vmFormError = 'Virtual models feature is unavailable.';
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'virtual model', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.vmFormError = res.status === 401
                            ? 'Authentication required.'
                            : await this.aliasResponseMessage(res, 'Failed to save virtual model.');
                        return;
                    }
                    this.setVirtualModelsAvailable(true);

                    await Promise.all([this.fetchModels(), this.fetchVirtualModels()]);
                    this.syncDisplayModels();
                    this.closeVirtualModelForm();
                    this.aliasNotice = targetModel ? 'Alias saved.' : 'Model access saved.';
                } catch (e) {
                    console.error('Failed to save virtual model:', e);
                    this.vmFormError = 'Failed to save virtual model.';
                } finally {
                    this.vmSubmitting = false;
                }
            },

            // deleteVirtualModel removes the virtual model for the editor's source.
            async deleteVirtualModel() {
                const source = String(this.vmForm.source || this.vmFormOriginalSource || '').trim();
                if (!source || !this.vmFormHasExisting) {
                    return;
                }
                if (!this.confirmAction('Remove the virtual model for "' + source + '"? This reverts to inherited/default behavior.')) {
                    return;
                }

                this.vmDeleting = true;
                this.vmFormError = '';
                this.aliasError = '';
                this.aliasNotice = '';

                try {
                    const request = this.adminRequestOptions({
                        method: 'DELETE',
                        body: JSON.stringify({ source })
                    });
                    const res = await fetch('/admin/virtual-models', request);
                    if (res.status === 503) {
                        this.setVirtualModelsAvailable(false);
                        this.vmFormError = 'Virtual models feature is unavailable.';
                        return;
                    }
                    if (res.status !== 404) {
                        const handled = this.handleFetchResponse(res, 'virtual model', request);
                        if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                            return;
                        }
                        if (!handled) {
                            this.vmFormError = res.status === 401
                                ? 'Authentication required.'
                                : await this.aliasResponseMessage(res, 'Failed to remove virtual model.');
                            return;
                        }
                    }
                    this.setVirtualModelsAvailable(true);

                    await Promise.all([this.fetchModels(), this.fetchVirtualModels()]);
                    this.syncDisplayModels();
                    this.closeVirtualModelForm();
                    this.aliasNotice = 'Virtual model removed.';
                } catch (e) {
                    console.error('Failed to delete virtual model:', e);
                    this.vmFormError = 'Failed to remove virtual model.';
                } finally {
                    this.vmDeleting = false;
                }
            },

            confirmAction(message) {
                if (typeof global.confirm === 'function') {
                    return global.confirm(message);
                }
                return true;
            }
        };
    }

    global.dashboardVirtualModelsModule = dashboardVirtualModelsModule;
})(window);
