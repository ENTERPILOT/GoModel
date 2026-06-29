(function(global) {
    function dashboardFailoverModule() {
        return {
            failoverAvailable: true,
            failoverRules: [],
            failoverLoading: false,
            failoverSaving: false,
            failoverError: '',
            failoverNotice: '',
            failoverGeneratedRules: [],
            failoverFormOpen: false,
            failoverFormMode: 'create',
            failoverFormManaged: false,
            failoverFormOriginalSource: '',
            failoverForm: {
                source: '',
                target_model: '',
                targets: [],
                enabled: true
            },

            failoverEnabled() {
                return typeof this.workflowRuntimeBooleanFlag === 'function'
                    ? this.workflowRuntimeBooleanFlag('FAILOVER_ENABLED', true)
                    : true;
            },

            async fetchFailoverRules() {
                this.failoverLoading = true;
                this.failoverError = '';
                try {
                    const request = this.adminRequestOptions();
                    const res = await fetch('/admin/failover', request);
                    if (res.status === 503) {
                        this.failoverAvailable = false;
                        this.failoverRules = [];
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'failover mappings', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    this.failoverAvailable = true;
                    if (!handled) {
                        this.failoverRules = [];
                        return;
                    }
                    const payload = await res.json();
                    this.failoverRules = this.normalizeFailoverRules(payload);
                } catch (e) {
                    console.error('Failed to fetch failover mappings:', e);
                    this.failoverRules = [];
                    this.failoverError = 'Unable to load failover mappings.';
                } finally {
                    this.failoverLoading = false;
                }
            },

            resetFailoverForm() {
                this.failoverFormMode = 'create';
                this.failoverFormManaged = false;
                this.failoverFormOriginalSource = '';
                this.failoverForm = {
                    source: '',
                    target_model: '',
                    targets: [],
                    enabled: true
                };
            },

            openFailoverCreate() {
                this.resetFailoverForm();
                this.failoverFormOpen = true;
                this.focusFailoverEditor();
            },

            openFailoverEdit(rule) {
                if (!rule) return;
                this.resetFailoverForm();
                this.failoverFormMode = 'edit';
                this.failoverFormOpen = true;
                this.failoverFormManaged = Boolean(rule.managed);
                const source = this.failoverPrimaryModel(rule);
                this.failoverFormOriginalSource = source;
                const targets = this.failoverTargets(rule);
                this.failoverForm = {
                    source,
                    target_model: targets[0] || '',
                    targets: targets.slice(1).map((model) => ({ model })),
                    enabled: rule.enabled !== false
                };
                this.focusFailoverEditor();
            },

            openFailoverGenerated(rule) {
                if (!rule) return;
                this.openFailoverEdit(rule);
                this.failoverFormMode = 'create';
            },

            openFailoverForModel(row) {
                if (!row || row.is_alias) return;
                const source = this.qualifiedModelName(row);
                const existing = this.failoverRules.find((rule) => this.failoverPrimaryModel(rule) === source);
                if (existing) {
                    this.openFailoverEdit(existing);
                    return;
                }
                this.resetFailoverForm();
                this.failoverFormMode = 'create';
                this.failoverFormOpen = true;
                this.failoverForm.source = source;
                this.focusFailoverEditor();
            },

            closeFailoverForm() {
                this.failoverFormOpen = false;
            },

            failoverFormTargets() {
                const values = [this.failoverForm.target_model];
                const rows = Array.isArray(this.failoverForm.targets) ? this.failoverForm.targets : [];
                rows.forEach((target) => values.push(target && target.model));
                return values.map((value) => String(value || '').trim()).filter(Boolean);
            },

            addFailoverTarget() {
                if (!Array.isArray(this.failoverForm.targets)) {
                    this.failoverForm.targets = [];
                }
                this.failoverForm.targets.push({ model: '' });
                this.focusFailoverEditor();
            },

            removeFailoverTarget(index) {
                if (!Array.isArray(this.failoverForm.targets)) {
                    this.failoverForm.targets = [];
                    return;
                }
                this.failoverForm.targets.splice(index, 1);
            },

            removePrimaryFailoverTarget() {
                const rows = Array.isArray(this.failoverForm.targets) ? this.failoverForm.targets : [];
                if (rows.length > 0) {
                    const next = rows.shift();
                    this.failoverForm.target_model = next && next.model ? next.model : '';
                    this.failoverForm.targets = rows;
                    return;
                }
                this.failoverForm.target_model = '';
            },

            failoverRulePayload() {
                return {
                    primary_model: String(this.failoverForm.source || '').trim(),
                    fallback_models: this.failoverFormTargets(),
                    enabled: this.failoverForm.enabled !== false
                };
            },

            async submitFailoverForm() {
                if (this.failoverSaving || this.failoverFormManaged) return;
                const payload = this.failoverRulePayload();
                if (!payload.primary_model) {
                    this.failoverError = 'Primary model is required.';
                    return;
                }
                if (payload.enabled && payload.fallback_models.length === 0) {
                    this.failoverError = 'Add at least one failover target.';
                    return;
                }
                this.failoverSaving = true;
                this.failoverError = '';
                this.failoverNotice = '';
                try {
                    const request = this.adminRequestOptions({
                        method: 'PUT',
                        body: JSON.stringify(payload)
                    });
                    const res = await fetch('/admin/failover', request);
                    const handled = this.handleFetchResponse(res, 'failover mapping', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.failoverError = 'Failed to save failover mapping.';
                        return;
                    }
                    this.failoverNotice = 'Failover mapping saved.';
                    this.closeFailoverForm();
                    await this.fetchFailoverRules();
                } catch (e) {
                    console.error('Failed to save failover mapping:', e);
                    this.failoverError = 'Failed to save failover mapping.';
                } finally {
                    this.failoverSaving = false;
                }
            },

            async deleteFailoverRule(rule) {
                const source = String((rule && this.failoverPrimaryModel(rule)) || this.failoverForm.source || '').trim();
                if (!source || this.failoverSaving) return;
                if (!this.confirmAction('Remove failover mapping for "' + source + '"?')) return;
                this.failoverSaving = true;
                this.failoverError = '';
                try {
                    const request = this.adminRequestOptions({
                        method: 'DELETE',
                        body: JSON.stringify({ primary_model: source })
                    });
                    const res = await fetch('/admin/failover', request);
                    const handled = this.handleFetchResponse(res, 'failover mapping', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.failoverError = 'Failed to remove failover mapping.';
                        return;
                    }
                    this.failoverNotice = 'Failover mapping removed.';
                    this.closeFailoverForm();
                    await this.fetchFailoverRules();
                } catch (e) {
                    console.error('Failed to remove failover mapping:', e);
                    this.failoverError = 'Failed to remove failover mapping.';
                } finally {
                    this.failoverSaving = false;
                }
            },

            openFailoverResetDialog() {
                this.openTypedConfirmationDialog({
                    title: 'Reset failover models',
                    titleId: 'failoverResetDialogTitle',
                    inputId: 'failover-reset-confirmation',
                    message: 'Remove every dashboard-managed failover mapping. Configuration-managed mappings remain active.',
                    requiredText: 'reset',
                    confirmLabel: 'Reset Failover',
                    icon: 'rotate-ccw',
                    dialogClass: 'budget-reset-dialog',
                    loadingKey: 'failoverSaving',
                    errorKey: 'failoverError',
                    onConfirm: async function() {
                        await this.resetFailoverRules();
                    }
                });
            },

            async resetFailoverRules() {
                if (this.failoverSaving) return;
                this.failoverSaving = true;
                this.failoverError = '';
                this.failoverNotice = '';
                try {
                    const request = this.adminRequestOptions({ method: 'POST' });
                    const res = await fetch('/admin/failover/reset', request);
                    const handled = this.handleFetchResponse(res, 'failover reset', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.failoverError = 'Failed to reset failover mappings.';
                        return;
                    }
                    const payload = await res.json();
                    this.failoverRules = this.normalizeFailoverRules(payload);
                    this.failoverGeneratedRules = [];
                    this.failoverNotice = 'Dashboard-managed failover mappings reset.';
                    this.closeTypedConfirmationDialog();
                } catch (e) {
                    console.error('Failed to reset failover mappings:', e);
                    this.failoverError = 'Failed to reset failover mappings.';
                } finally {
                    this.failoverSaving = false;
                }
            },

            async generateFailoverRules() {
                if (this.failoverSaving) return;
                this.failoverSaving = true;
                this.failoverError = '';
                this.failoverNotice = '';
                this.failoverGeneratedRules = [];
                try {
                    const request = this.adminRequestOptions({ method: 'POST' });
                    const res = await fetch('/admin/failover/generate', request);
                    const handled = this.handleFetchResponse(res, 'failover generation', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.failoverError = 'Failed to generate failover mappings.';
                        return;
                    }
                    const payload = await res.json();
                    this.failoverGeneratedRules = this.normalizeFailoverRules(payload);
                    this.failoverNotice = this.failoverGeneratedRules.length
                        ? 'Generated ' + this.failoverGeneratedRules.length + ' failover mapping drafts.'
                        : 'No failover suggestions were generated.';
                } catch (e) {
                    console.error('Failed to generate failover mappings:', e);
                    this.failoverError = 'Failed to generate failover mappings.';
                } finally {
                    this.failoverSaving = false;
                }
            },

            focusFailoverEditor() {
                setTimeout(() => {
                    const refs = this.$refs || {};
                    const editor = refs.failoverEditor || null;
                    const field = editor && editor.querySelector
                        ? editor.querySelector('[data-modal-autofocus], input:not([disabled]), textarea:not([disabled]), button:not([disabled])')
                        : null;
                    if (field && typeof field.focus === 'function') {
                        field.focus({ preventScroll: true });
                    }
                    if (typeof this.renderIconsAfterUpdate === 'function') {
                        this.renderIconsAfterUpdate();
                    }
                }, 0);
            },

            failoverTargetLabel(rule) {
                const targets = this.failoverTargets(rule);
                if (targets.length === 0) return '-';
                return targets.join(', ');
            },

            failoverPrimaryModel(rule) {
                return String((rule && (rule.primary_model || rule.source)) || '').trim();
            },

            failoverTargets(rule) {
                if (Array.isArray(rule && rule.fallback_models)) return rule.fallback_models;
                if (Array.isArray(rule && rule.targets)) return rule.targets;
                return [];
            },

            findFailoverMapping(source) {
                const primary = String(source || '').trim();
                if (!primary) return null;
                return this.failoverRules.find((rule) => this.failoverPrimaryModel(rule) === primary) || null;
            },

            hasActiveFailoverMapping(row) {
                if (!row || row.is_alias) return false;
                const mapping = this.findFailoverMapping(this.qualifiedModelName(row));
                return Boolean(mapping && mapping.enabled !== false && this.failoverTargets(mapping).length > 0);
            },

            failoverButtonClass(row) {
                return this.hasActiveFailoverMapping(row) ? 'table-action-btn-failover-active' : '';
            },

            failoverButtonLabel(row) {
                const label = row && row.display_name ? row.display_name : 'model';
                const base = 'Edit failover for ' + label;
                return this.hasActiveFailoverMapping(row) ? base + ' (active)' : base;
            },

            normalizeFailoverRules(payload) {
                if (!Array.isArray(payload)) return [];
                return payload.map((rule) => ({
                    ...rule,
                    source: this.failoverPrimaryModel(rule),
                    targets: this.failoverTargets(rule)
                }));
            },

            failoverRuleStatus(rule) {
                if (rule && rule.enabled === false) return 'Off';
                if (rule && rule.managed) return 'Config';
                return 'On';
            }
        };
    }

    global.dashboardFailoverModule = dashboardFailoverModule;
})(window);
