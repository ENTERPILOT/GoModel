(function(global) {
    function dashboardGuardrailsModule() {
        return {
            guardrails: [],
            guardrailTypes: [],
            guardrailsAvailable: true,
            guardrailsLoading: false,
            guardrailTypesLoading: false,
            guardrailError: '',
            guardrailNotice: '',
            guardrailFilter: '',
            guardrailFormOpen: false,
            guardrailFormSubmitting: false,
            guardrailDeletingName: '',
            guardrailFormMode: 'create',
            guardrailFormOriginalName: '',
            guardrailForm: {
                name: '',
                type: '',
                description: '',
                config: {}
            },

            cloneGuardrailJSON(value) {
                try {
                    const cloned = JSON.parse(JSON.stringify(value || {}));
                    return cloned && typeof cloned === 'object' && !Array.isArray(cloned) ? cloned : {};
                } catch (_) {
                    return {};
                }
            },

            defaultGuardrailType() {
                if (Array.isArray(this.guardrailTypes) && this.guardrailTypes.length > 0) {
                    return String(this.guardrailTypes[0].type || '').trim() || 'system_prompt';
                }
                return 'system_prompt';
            },

            resolvedGuardrailType(type) {
                const normalized = String(type || '').trim();
                if (normalized && this.guardrailTypeDefinition(normalized)) {
                    return normalized;
                }
                return this.defaultGuardrailType();
            },

            normalizeGuardrailArrayValue(value) {
                if (Array.isArray(value)) {
                    return value
                        .map((item) => String(item || '').trim())
                        .filter((item) => item);
                }
                if (value === null || value === undefined) {
                    return [];
                }
                return String(value)
                    .split(',')
                    .map((item) => item.trim())
                    .filter((item) => item);
            },

            guardrailTypeDefinition(type) {
                const normalized = String(type || '').trim();
                return (this.guardrailTypes || []).find((item) => String(item && item.type || '').trim() === normalized) || null;
            },

            defaultGuardrailConfig(type) {
                const definition = this.guardrailTypeDefinition(type);
                if (!definition || !definition.defaults) {
                    return {};
                }
                return this.cloneGuardrailJSON(definition.defaults);
            },

            normalizeGuardrailConfig(config, type) {
                return {
                    ...this.defaultGuardrailConfig(type),
                    ...this.cloneGuardrailJSON(config)
                };
            },

            defaultGuardrailForm(type) {
                const resolvedType = this.resolvedGuardrailType(type);
                return {
                    name: '',
                    type: resolvedType,
                    description: '',
                    user_path: '',
                    config: this.defaultGuardrailConfig(resolvedType)
                };
            },

            get filteredGuardrails() {
                if (!this.guardrailFilter) {
                    return this.guardrails;
                }
                const filter = this.guardrailFilter.toLowerCase();
                return (this.guardrails || []).filter((guardrail) => {
                    const fields = [
                        guardrail.name,
                        guardrail.type,
                        guardrail.user_path,
                        guardrail.description,
                        guardrail.summary
                    ];
                    return fields.some((value) => String(value || '').toLowerCase().includes(filter));
                });
            },

            guardrailTypeLabel(type) {
                const definition = this.guardrailTypeDefinition(type);
                return definition && definition.label ? definition.label : (type || 'Unknown');
            },

            guardrailTypeFields(type) {
                const definition = this.guardrailTypeDefinition(type);
                return Array.isArray(definition && definition.fields) ? definition.fields : [];
            },

            guardrailFieldValue(field) {
                if (!field || !this.guardrailForm || !this.guardrailForm.config) {
                    return '';
                }
                const value = this.guardrailForm.config[field.key];
                if (value === null || value === undefined) {
                    return field.input === 'checkboxes' ? [] : '';
                }
                if (field.input === 'checkboxes') {
                    return this.normalizeGuardrailArrayValue(value);
                }
                return value;
            },

            setGuardrailFieldValue(field, value) {
                if (!field) {
                    return;
                }
                const nextConfig = this.cloneGuardrailJSON(this.guardrailForm.config);
                if (field.input === 'number') {
                    const trimmed = String(value || '').trim();
                    if (trimmed === '') {
                        delete nextConfig[field.key];
                    } else {
                        const parsed = Number(trimmed);
                        nextConfig[field.key] = Number.isFinite(parsed) ? parsed : trimmed;
                    }
                } else if (field.input === 'checkboxes') {
                    nextConfig[field.key] = this.normalizeGuardrailArrayValue(value);
                } else {
                    nextConfig[field.key] = value;
                }
                this.guardrailForm = {
                    ...this.guardrailForm,
                    config: nextConfig
                };
            },

            syncGuardrailTypeSelectValue() {
                const select = this.$refs && this.$refs.guardrailTypeSelect;
                if (!select) {
                    return;
                }

                const resolvedType = this.resolvedGuardrailType(this.guardrailForm && this.guardrailForm.type);
                if (select.value !== resolvedType) {
                    select.value = resolvedType;
                }
            },

            guardrailArrayFieldSelected(field, optionValue) {
                return this.guardrailFieldValue(field).includes(String(optionValue || '').trim());
            },

            toggleGuardrailArrayFieldValue(field, optionValue, checked) {
                const selected = this.normalizeGuardrailArrayValue(this.guardrailFieldValue(field));
                const normalizedValue = String(optionValue || '').trim();
                if (!normalizedValue) {
                    return;
                }

                const next = checked
                    ? Array.from(new Set([...selected, normalizedValue]))
                    : selected.filter((item) => item !== normalizedValue);
                this.setGuardrailFieldValue(field, next);
            },

            guardrailsRuntimeEnabled() {
                if (typeof this.workflowRuntimeBooleanFlag === 'function') {
                    return this.workflowRuntimeBooleanFlag('GUARDRAILS_ENABLED', true);
                }
                return true;
            },

            focusGuardrailForm() {
                const focus = () => {
                    const refs = this.$refs || {};
                    const editor = refs.guardrailEditor || null;
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

            openGuardrailCreate() {
                this.guardrailFormMode = 'create';
                this.guardrailFormOriginalName = '';
                this.guardrailError = '';
                this.guardrailNotice = '';
                this.guardrailForm = this.defaultGuardrailForm(this.defaultGuardrailType());
                this.guardrailFormOpen = true;
                this.focusGuardrailForm();
            },

            openGuardrailEdit(guardrail) {
                const resolvedType = this.resolvedGuardrailType(guardrail && guardrail.type);
                this.guardrailFormMode = 'edit';
                this.guardrailFormOriginalName = String(guardrail && guardrail.name || '').trim();
                this.guardrailError = '';
                this.guardrailNotice = '';
                this.guardrailForm = {
                    name: this.guardrailFormOriginalName,
                    type: resolvedType,
                    description: String(guardrail && guardrail.description || '').trim(),
                    user_path: String(guardrail && guardrail.user_path || '').trim(),
                    config: this.normalizeGuardrailConfig(guardrail && guardrail.config, resolvedType)
                };
                this.guardrailFormOpen = true;
                this.focusGuardrailForm();
            },

            closeGuardrailForm() {
                this.guardrailFormOpen = false;
                this.guardrailFormMode = 'create';
                this.guardrailFormOriginalName = '';
                this.guardrailError = '';
                this.guardrailForm = this.defaultGuardrailForm(this.defaultGuardrailType());
            },

            onGuardrailTypeChange() {
                const resolvedType = this.resolvedGuardrailType(this.guardrailForm.type);
                this.guardrailForm = {
                    ...this.guardrailForm,
                    type: resolvedType,
                    config: this.defaultGuardrailConfig(resolvedType)
                };
            },

            async guardrailResponseMessage(res, fallback) {
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

            async fetchGuardrailTypes() {
                this.guardrailTypesLoading = true;
                try {
                    const request = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    const res = await fetch('/admin/api/v1/guardrails/types', request);
                    if (res.status === 503) {
                        this.guardrailsAvailable = false;
                        this.guardrailTypes = [];
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'guardrail types', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    this.guardrailsAvailable = true;
                    if (!handled) {
                        this.guardrailTypes = [];
                        return;
                    }
                    const payload = await res.json();
                    this.guardrailTypes = Array.isArray(payload) ? payload : [];
                    const resolvedType = this.resolvedGuardrailType(this.guardrailForm.type);
                    this.guardrailForm = {
                        ...this.guardrailForm,
                        type: resolvedType,
                        config: this.normalizeGuardrailConfig(this.guardrailForm.config, resolvedType)
                    };
                } catch (e) {
                    console.error('Failed to fetch guardrail types:', e);
                    this.guardrailTypes = [];
                    this.guardrailError = 'Unable to load guardrail types.';
                } finally {
                    this.guardrailTypesLoading = false;
                }
            },

            async fetchGuardrails() {
                this.guardrailsLoading = true;
                this.guardrailError = '';
                try {
                    const request = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    const res = await fetch('/admin/api/v1/guardrails', request);
                    if (res.status === 503) {
                        this.guardrailsAvailable = false;
                        this.guardrails = [];
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'guardrails', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    this.guardrailsAvailable = true;
                    if (!handled) {
                        this.guardrails = [];
                        return;
                    }
                    const payload = await res.json();
                    this.guardrails = Array.isArray(payload) ? payload : [];
                } catch (e) {
                    console.error('Failed to fetch guardrails:', e);
                    this.guardrails = [];
                    this.guardrailError = 'Unable to load guardrails.';
                } finally {
                    this.guardrailsLoading = false;
                }
            },

            async fetchGuardrailsPage() {
                await Promise.all([
                    this.fetchGuardrailTypes(),
                    this.fetchGuardrails()
                ]);
            },

            async submitGuardrailForm() {
                const name = String(this.guardrailForm.name || '').trim();
                const type = String(this.guardrailForm.type || '').trim();
                if (!name) {
                    this.guardrailError = 'Name is required.';
                    return;
                }
                if (!type) {
                    this.guardrailError = 'Type is required.';
                    return;
                }

                this.guardrailError = '';
                this.guardrailNotice = '';
                this.guardrailFormSubmitting = true;

                const payload = {
                    type,
                    description: String(this.guardrailForm.description || '').trim() || undefined,
                    user_path: String(this.guardrailForm.user_path || '').trim() || undefined,
                    config: this.cloneGuardrailJSON(this.guardrailForm.config)
                };

                try {
                    const res = await fetch('/admin/api/v1/guardrails/' + encodeURIComponent(name), {
                        method: 'PUT',
                        headers: this.headers(),
                        body: JSON.stringify(payload)
                    });
                    if (res.status === 503) {
                        this.guardrailsAvailable = false;
                        this.guardrailError = 'Guardrails feature is unavailable.';
                        return;
                    }
                    if (res.status === 401) {
                        this.authError = true;
                        this.needsAuth = true;
                        this.guardrailError = 'Authentication required.';
                        return;
                    }
                    if (res.status !== 200) {
                        this.guardrailError = await this.guardrailResponseMessage(res, 'Failed to save guardrail.');
                        console.error('Failed to save guardrail:', res.status, res.statusText, this.guardrailError);
                        return;
                    }

                    await this.fetchGuardrails();
                    if (typeof this.fetchWorkflowGuardrails === 'function') {
                        this.fetchWorkflowGuardrails();
                    }
                    this.guardrailNotice = 'Guardrail "' + name + '" saved.';
                    this.closeGuardrailForm();
                } catch (e) {
                    console.error('Failed to save guardrail:', e);
                    this.guardrailError = 'Failed to save guardrail.';
                } finally {
                    this.guardrailFormSubmitting = false;
                }
            },

            async deleteGuardrail(guardrail) {
                const name = String(guardrail && guardrail.name || '').trim();
                if (!name || this.guardrailDeletingName) {
                    return;
                }
                if (!window.confirm('Delete guardrail "' + name + '"? Workflows that still reference it must be updated first.')) {
                    return;
                }

                this.guardrailDeletingName = name;
                this.guardrailError = '';
                this.guardrailNotice = '';

                try {
                    const res = await fetch('/admin/api/v1/guardrails/' + encodeURIComponent(name), {
                        method: 'DELETE',
                        headers: this.headers()
                    });
                    if (res.status === 503) {
                        this.guardrailsAvailable = false;
                        this.guardrailError = 'Guardrails feature is unavailable.';
                        return;
                    }
                    if (res.status === 401) {
                        this.authError = true;
                        this.needsAuth = true;
                        this.guardrailError = 'Authentication required.';
                        return;
                    }
                    if (res.status !== 204) {
                        this.guardrailError = await this.guardrailResponseMessage(res, 'Failed to delete guardrail.');
                        console.error('Failed to delete guardrail:', res.status, res.statusText, this.guardrailError);
                        return;
                    }

                    await this.fetchGuardrails();
                    if (typeof this.fetchWorkflowGuardrails === 'function') {
                        this.fetchWorkflowGuardrails();
                    }
                    if (this.guardrailFormOpen && this.guardrailFormOriginalName === name) {
                        this.closeGuardrailForm();
                    }
                    this.guardrailNotice = 'Guardrail "' + name + '" deleted.';
                } catch (e) {
                    console.error('Failed to delete guardrail:', e);
                    this.guardrailError = 'Failed to delete guardrail.';
                } finally {
                    this.guardrailDeletingName = '';
                }
            }
        };
    }

    global.dashboardGuardrailsModule = dashboardGuardrailsModule;
})(window);
