(function(global) {
    function dashboardProvidersConfigModule() {
        return {
            providerCredentials: [],
            providerCredentialsAvailable: true,
            providerCredentialsLoading: false,
            providerCredentialError: '',
            providerCredentialNotice: '',
            providerCredentialFilter: '',
            providerCredentialFormOpen: false,
            providerCredentialFormSubmitting: false,
            providerCredentialFormMode: 'create',
            providerCredentialAdvancedOpen: false,
            providerCredentialDeletingName: '',
            providerCredentialDeleteSubmitting: false,
            providerCredentialTypes: [],
            providerCredentialTypesLoaded: false,
            providerCredentialForm: {
                name: '',
                type: '',
                api_keys: [],
                base_url: '',
                api_version: '',
                backend: '',
                auth_type: '',
                api_mode: '',
                vertex_project: '',
                vertex_location: '',
                service_account_file: '',
                service_account_json: '',
                service_account_json_base64: '',
                gcp_scope: '',
                models: '',
                enabled: true
            },

            defaultProviderCredentialForm() {
                return {
                    name: '',
                    type: '',
                    api_keys: [],
                    base_url: '',
                    api_version: '',
                    backend: '',
                    auth_type: '',
                    api_mode: '',
                    vertex_project: '',
                    vertex_location: '',
                    service_account_file: '',
                    service_account_json: '',
                    service_account_json_base64: '',
                    gcp_scope: '',
                    models: '',
                    enabled: true
                };
            },

            get filteredProviderCredentials() {
                if (!this.providerCredentialFilter) {
                    return this.providerCredentials;
                }
                const filter = this.providerCredentialFilter.toLowerCase();
                return (this.providerCredentials || []).filter((row) => {
                    const fields = [row.name, row.type, row.base_url];
                    return fields.some((value) => String(value || '').toLowerCase().includes(filter));
                });
            },

            // providerCredentialTypeOptions ensures the currently selected type
            // always renders as an option, even if it isn't (yet, or anymore) in
            // the fetched /types list — e.g. while types are still loading.
            providerCredentialTypeOptions() {
                const types = Array.isArray(this.providerCredentialTypes) ? this.providerCredentialTypes.slice() : [];
                const current = String(this.providerCredentialForm && this.providerCredentialForm.type || '').trim();
                if (current && !types.includes(current)) {
                    types.push(current);
                }
                return types;
            },

            providerCredentialAuthLabel(row) {
                const keyCount = Array.isArray(row && row.api_keys) ? row.api_keys.length : 0;
                if (keyCount > 0) {
                    return keyCount + ' key' + (keyCount === 1 ? '' : 's');
                }
                if (String(row && row.service_account_json || '').trim()
                    || String(row && row.service_account_json_base64 || '').trim()
                    || String(row && row.service_account_file || '').trim()) {
                    return 'service account';
                }
                if (String(row && row.vertex_project || '').trim()) {
                    return 'ADC';
                }
                return 'keyless';
            },

            providerCredentialModelsLabel(row) {
                const models = Array.isArray(row && row.models) ? row.models : [];
                if (models.length === 0) {
                    return 'auto-discovered';
                }
                return models.length + ' model' + (models.length === 1 ? '' : 's');
            },

            providerCredentialKeysToRows(apiKeys) {
                return (Array.isArray(apiKeys) ? apiKeys : []).map((value) => ({ value: String(value || '') }));
            },

            providerCredentialKeyRowsToArray(rows) {
                return (Array.isArray(rows) ? rows : []).map((row) => String(row && row.value || ''));
            },

            // suggestProviderCredentialName proposes a free provider name for
            // the given type: the bare type name if no provider (declared or
            // dashboard-managed) already uses it, otherwise "{type}-1",
            // "{type}-2", ... picking the first unused suffix.
            suggestProviderCredentialName(type) {
                const base = String(type || '').trim();
                if (!base) {
                    return '';
                }
                const taken = new Set((this.providerCredentials || []).map((row) => String(row && row.name || '').trim()));
                if (!taken.has(base)) {
                    return base;
                }
                let n = 1;
                while (taken.has(base + '-' + n)) {
                    n += 1;
                }
                return base + '-' + n;
            },

            // onProviderCredentialTypeChange resets the Name field to a fresh
            // suggestion whenever the Type selection changes while creating a
            // provider (Type is immutable once a provider exists, so this
            // never runs in edit mode). The operator can still edit the
            // suggested name before saving.
            onProviderCredentialTypeChange() {
                if (this.providerCredentialFormMode !== 'create') {
                    return;
                }
                this.providerCredentialForm.name = this.suggestProviderCredentialName(this.providerCredentialForm.type);
            },

            addApiKeyRow() {
                this.providerCredentialForm.api_keys.push({ value: '' });
            },

            removeApiKeyRow(index) {
                this.providerCredentialForm.api_keys.splice(index, 1);
            },

            normalizeProviderCredentialCommaList(value) {
                return String(value || '')
                    .split(',')
                    .map((item) => item.trim())
                    .filter((item) => item);
            },

            focusProviderCredentialForm() {
                const focus = () => {
                    const refs = this.$refs || {};
                    const editor = refs.providerCredentialEditor || null;
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

            openProviderCredentialCreate() {
                this.providerCredentialFormMode = 'create';
                this.providerCredentialAdvancedOpen = false;
                this.providerCredentialError = '';
                this.providerCredentialNotice = '';
                this.providerCredentialForm = this.defaultProviderCredentialForm();
                this.providerCredentialFormOpen = true;
                this.focusProviderCredentialForm();
                if (!this.providerCredentialTypesLoaded) {
                    this.fetchProviderCredentialTypes();
                }
                if (typeof this.renderIconsAfterUpdate === 'function') {
                    this.renderIconsAfterUpdate();
                }
            },

            openProviderCredentialEdit(row) {
                if (!row || row.managed) {
                    return;
                }
                this.providerCredentialFormMode = 'edit';
                this.providerCredentialAdvancedOpen = false;
                this.providerCredentialError = '';
                this.providerCredentialNotice = '';
                this.providerCredentialForm = {
                    name: String(row.name || '').trim(),
                    type: String(row.type || '').trim(),
                    api_keys: this.providerCredentialKeysToRows(row.api_keys),
                    base_url: String(row.base_url || ''),
                    api_version: String(row.api_version || ''),
                    backend: String(row.backend || ''),
                    auth_type: String(row.auth_type || ''),
                    api_mode: String(row.api_mode || ''),
                    vertex_project: String(row.vertex_project || ''),
                    vertex_location: String(row.vertex_location || ''),
                    service_account_file: String(row.service_account_file || ''),
                    service_account_json: String(row.service_account_json || ''),
                    service_account_json_base64: String(row.service_account_json_base64 || ''),
                    gcp_scope: String(row.gcp_scope || ''),
                    models: (Array.isArray(row.models) ? row.models : []).join(', '),
                    enabled: row.enabled !== false
                };
                this.providerCredentialFormOpen = true;
                this.focusProviderCredentialForm();
                if (!this.providerCredentialTypesLoaded) {
                    this.fetchProviderCredentialTypes();
                }
                if (typeof this.renderIconsAfterUpdate === 'function') {
                    this.renderIconsAfterUpdate();
                }
            },

            closeProviderCredentialForm() {
                this.providerCredentialFormOpen = false;
                this.providerCredentialFormMode = 'create';
                this.providerCredentialAdvancedOpen = false;
                this.providerCredentialError = '';
                this.providerCredentialForm = this.defaultProviderCredentialForm();
            },

            async providerCredentialResponseMessage(res, fallback) {
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

            async fetchProviderCredentialTypes() {
                try {
                    const request = this.requestOptions();
                    const res = await fetch('/admin/provider-credentials/types', request);
                    if (res.status === 503 || res.status === 404) {
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'provider credential types', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        return;
                    }
                    const payload = await res.json();
                    this.providerCredentialTypes = Array.isArray(payload) ? payload : [];
                    this.providerCredentialTypesLoaded = true;
                } catch (e) {
                    console.error('Failed to fetch provider credential types:', e);
                }
            },

            async fetchProviderCredentialsPage() {
                this.providerCredentialsLoading = true;
                this.providerCredentialError = '';
                try {
                    const request = this.requestOptions();
                    const res = await fetch('/admin/provider-credentials', request);
                    if (res.status === 503 || res.status === 404) {
                        this.providerCredentialsAvailable = false;
                        this.providerCredentials = [];
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'provider credentials', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    this.providerCredentialsAvailable = true;
                    if (!handled) {
                        this.providerCredentials = [];
                        if (res.status !== 401) {
                            this.providerCredentialError = await this.providerCredentialResponseMessage(res, 'Failed to load provider credentials.');
                        }
                        return;
                    }
                    const payload = await res.json();
                    this.providerCredentials = Array.isArray(payload) ? payload : [];
                    if (!this.providerCredentialTypesLoaded) {
                        await this.fetchProviderCredentialTypes();
                    }
                } catch (e) {
                    console.error('Failed to fetch provider credentials:', e);
                    this.providerCredentials = [];
                    this.providerCredentialError = 'Unable to load provider credentials.';
                } finally {
                    this.providerCredentialsLoading = false;
                    if (typeof this.renderIconsAfterUpdate === 'function') {
                        this.renderIconsAfterUpdate();
                    }
                }
            },

            async saveProviderCredential() {
                return this.submitProviderCredentialForm();
            },

            async submitProviderCredentialForm() {
                const name = String(this.providerCredentialForm.name || '').trim();
                const type = String(this.providerCredentialForm.type || '').trim();
                if (!name) {
                    this.providerCredentialError = 'Name is required.';
                    return;
                }
                if (!type) {
                    this.providerCredentialError = 'Type is required.';
                    return;
                }
                if (this.providerCredentialFormMode === 'create' && (this.providerCredentials || []).some((row) => String(row.name || '').trim() === name)) {
                    this.providerCredentialError = 'Provider "' + name + '" already exists.';
                    return;
                }
                const apiKeys = this.providerCredentialKeyRowsToArray(this.providerCredentialForm.api_keys);
                if (apiKeys.some((value) => !value.trim())) {
                    this.providerCredentialError = 'API key rows cannot be empty. Remove the row instead of leaving it blank.';
                    return;
                }

                this.providerCredentialError = '';
                this.providerCredentialNotice = '';
                this.providerCredentialFormSubmitting = true;

                const payload = {
                    name,
                    type,
                    api_keys: apiKeys,
                    base_url: String(this.providerCredentialForm.base_url || '').trim(),
                    api_version: String(this.providerCredentialForm.api_version || '').trim(),
                    backend: String(this.providerCredentialForm.backend || '').trim(),
                    auth_type: String(this.providerCredentialForm.auth_type || '').trim(),
                    api_mode: String(this.providerCredentialForm.api_mode || '').trim(),
                    vertex_project: String(this.providerCredentialForm.vertex_project || '').trim(),
                    vertex_location: String(this.providerCredentialForm.vertex_location || '').trim(),
                    service_account_file: String(this.providerCredentialForm.service_account_file || '').trim(),
                    service_account_json: this.providerCredentialForm.service_account_json,
                    service_account_json_base64: String(this.providerCredentialForm.service_account_json_base64 || '').trim(),
                    gcp_scope: String(this.providerCredentialForm.gcp_scope || '').trim(),
                    models: this.normalizeProviderCredentialCommaList(this.providerCredentialForm.models),
                    enabled: Boolean(this.providerCredentialForm.enabled)
                };

                try {
                    const request = this.requestOptions({
                        method: 'PUT',
                        body: JSON.stringify(payload)
                    });
                    const res = await fetch('/admin/provider-credentials', request);
                    if (res.status === 503) {
                        this.providerCredentialsAvailable = false;
                        this.providerCredentialError = 'Provider credential management is unavailable.';
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'save provider credential', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        if (res.status === 401) {
                            this.providerCredentialError = 'Authentication required.';
                            return;
                        }
                        this.providerCredentialError = await this.providerCredentialResponseMessage(res, 'Failed to save provider credential.');
                        return;
                    }

                    await this.fetchProviderCredentialsPage();
                    this.providerCredentialNotice = 'Provider "' + name + '" saved.';
                    this.closeProviderCredentialForm();
                } catch (e) {
                    console.error('Failed to save provider credential:', e);
                    this.providerCredentialError = 'Failed to save provider credential.';
                } finally {
                    this.providerCredentialFormSubmitting = false;
                }
            },

            async _performDeleteProviderCredential(name) {
                this.providerCredentialDeleteSubmitting = true;
                this.providerCredentialDeletingName = name;
                this.providerCredentialError = '';
                this.providerCredentialNotice = '';

                try {
                    const request = this.requestOptions({ method: 'DELETE' });
                    const res = await fetch('/admin/provider-credentials/' + encodeURIComponent(name), request);
                    if (res.status === 503) {
                        this.providerCredentialsAvailable = false;
                        this.providerCredentialError = 'Provider credential management is unavailable.';
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'delete provider credential', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        if (res.status === 401) {
                            this.providerCredentialError = 'Authentication required.';
                            return;
                        }
                        this.providerCredentialError = await this.providerCredentialResponseMessage(res, 'Failed to delete provider credential.');
                        return;
                    }

                    await this.fetchProviderCredentialsPage();
                    if (this.providerCredentialFormOpen && this.providerCredentialForm.name === name) {
                        this.closeProviderCredentialForm();
                    }
                    this.providerCredentialNotice = 'Provider "' + name + '" deleted.';
                    if (typeof this.closeTypedConfirmationDialog === 'function') {
                        this.closeTypedConfirmationDialog();
                    }
                } catch (e) {
                    console.error('Failed to delete provider credential:', e);
                    this.providerCredentialError = 'Failed to delete provider credential.';
                } finally {
                    this.providerCredentialDeleteSubmitting = false;
                    this.providerCredentialDeletingName = '';
                }
            },

            // deleteProviderCredential drives the shared typed-confirmation
            // dialog (layout.html) rather than window.confirm: deleting a
            // provider credential can silently break every request routed to
            // it, so this asks the operator to type the provider name back.
            deleteProviderCredential(name) {
                const target = String(name || '').trim();
                if (!target || this.providerCredentialDeleteSubmitting) {
                    return;
                }
                const row = (this.providerCredentials || []).find((item) => String(item && item.name || '').trim() === target);
                if (row && row.managed) {
                    return;
                }
                this.providerCredentialError = '';
                this.providerCredentialNotice = '';
                if (typeof this.openTypedConfirmationDialog !== 'function') {
                    return;
                }
                this.openTypedConfirmationDialog({
                    title: 'Delete Provider',
                    titleId: 'providerCredentialDeleteDialogTitle',
                    inputId: 'provider-credential-delete-confirmation',
                    message: 'Type "' + target + '" to permanently delete this provider credential. Requests routed to it will fail until it is reconfigured.',
                    requiredText: target,
                    confirmLabel: 'Delete Provider',
                    icon: 'trash-2',
                    dialogClass: 'budget-reset-dialog',
                    loadingKey: 'providerCredentialDeleteSubmitting',
                    errorKey: 'providerCredentialError',
                    onConfirm: () => this._performDeleteProviderCredential(target)
                });
            }
        };
    }

    global.dashboardProvidersConfigModule = dashboardProvidersConfigModule;
})(window);
