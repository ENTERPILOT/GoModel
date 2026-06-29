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
                targets: '',
                description: '',
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
                    const handled = this.handleFetchResponse(res, 'failover rules', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    this.failoverAvailable = true;
                    if (!handled) {
                        this.failoverRules = [];
                        return;
                    }
                    const payload = await res.json();
                    this.failoverRules = Array.isArray(payload) ? payload : [];
                } catch (e) {
                    console.error('Failed to fetch failover rules:', e);
                    this.failoverRules = [];
                    this.failoverError = 'Unable to load failover rules.';
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
                    targets: '',
                    description: '',
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
                this.failoverFormOriginalSource = rule.source || '';
                this.failoverForm = {
                    source: rule.source || '',
                    targets: (Array.isArray(rule.targets) ? rule.targets : []).join('\n'),
                    description: rule.description || '',
                    enabled: rule.enabled !== false
                };
                this.focusFailoverEditor();
            },

            openFailoverForModel(row) {
                if (!row || row.is_alias) return;
                const source = this.qualifiedModelName(row);
                const existing = this.failoverRules.find((rule) => String(rule.source || '') === source);
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
                return String(this.failoverForm.targets || '')
                    .split(/\r?\n|,/)
                    .map((value) => value.trim())
                    .filter(Boolean);
            },

            failoverRulePayload() {
                return {
                    source: String(this.failoverForm.source || '').trim(),
                    targets: this.failoverFormTargets(),
                    description: String(this.failoverForm.description || '').trim(),
                    enabled: this.failoverForm.enabled !== false
                };
            },

            async submitFailoverForm() {
                if (this.failoverSaving || this.failoverFormManaged) return;
                const payload = this.failoverRulePayload();
                if (!payload.source) {
                    this.failoverError = 'Source is required.';
                    return;
                }
                if (payload.enabled && payload.targets.length === 0) {
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
                    const handled = this.handleFetchResponse(res, 'failover rule', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.failoverError = 'Failed to save failover rule.';
                        return;
                    }
                    this.failoverNotice = 'Failover rule saved.';
                    this.closeFailoverForm();
                    await this.fetchFailoverRules();
                } catch (e) {
                    console.error('Failed to save failover rule:', e);
                    this.failoverError = 'Failed to save failover rule.';
                } finally {
                    this.failoverSaving = false;
                }
            },

            async deleteFailoverRule(rule) {
                const source = String((rule && rule.source) || this.failoverForm.source || '').trim();
                if (!source || this.failoverSaving) return;
                if (!this.confirmAction('Remove failover rule for "' + source + '"?')) return;
                this.failoverSaving = true;
                this.failoverError = '';
                try {
                    const request = this.adminRequestOptions({
                        method: 'DELETE',
                        body: JSON.stringify({ source })
                    });
                    const res = await fetch('/admin/failover', request);
                    const handled = this.handleFetchResponse(res, 'failover rule', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.failoverError = 'Failed to remove failover rule.';
                        return;
                    }
                    this.failoverNotice = 'Failover rule removed.';
                    this.closeFailoverForm();
                    await this.fetchFailoverRules();
                } catch (e) {
                    console.error('Failed to remove failover rule:', e);
                    this.failoverError = 'Failed to remove failover rule.';
                } finally {
                    this.failoverSaving = false;
                }
            },

            openFailoverResetDialog() {
                this.openTypedConfirmationDialog({
                    title: 'Reset failover models',
                    titleId: 'failoverResetDialogTitle',
                    inputId: 'failover-reset-confirmation',
                    message: 'Remove every dashboard-managed failover rule. Configuration-managed rules remain active.',
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
                        this.failoverError = 'Failed to reset failover rules.';
                        return;
                    }
                    const payload = await res.json();
                    this.failoverRules = Array.isArray(payload) ? payload : [];
                    this.failoverGeneratedRules = [];
                    this.failoverNotice = 'Dashboard-managed failover rules reset.';
                    this.closeTypedConfirmationDialog();
                } catch (e) {
                    console.error('Failed to reset failover rules:', e);
                    this.failoverError = 'Failed to reset failover rules.';
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
                        this.failoverError = 'Failed to generate failover rules.';
                        return;
                    }
                    const payload = await res.json();
                    this.failoverGeneratedRules = Array.isArray(payload) ? payload : [];
                    this.failoverNotice = this.failoverGeneratedRules.length
                        ? 'Generated ' + this.failoverGeneratedRules.length + ' failover rule drafts.'
                        : 'No failover suggestions were generated.';
                } catch (e) {
                    console.error('Failed to generate failover rules:', e);
                    this.failoverError = 'Failed to generate failover rules.';
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
                const targets = Array.isArray(rule && rule.targets) ? rule.targets : [];
                if (targets.length === 0) return '-';
                return targets.join(', ');
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
