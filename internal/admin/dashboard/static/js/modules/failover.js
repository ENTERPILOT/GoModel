(function(global) {
    function dashboardFailoverModule() {
        return {
            failoverAvailable: true,
            failoverRules: [],
            failoverLoading: false,
            failoverSaving: false,
            failoverGenerating: false,
            failoverError: '',
            failoverNotice: '',
            failoverGeneratedRules: [],
            failoverDraftsOpen: false,
            failoverDraftSelections: {},
            failoverDraftFilter: '',
            failoverDraftSaving: false,
            failoverFormOpen: false,
            failoverFormMode: 'create',
            failoverFormManaged: false,
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
                if (!this.failoverEnabled()) {
                    this.failoverAvailable = false;
                    this.failoverRules = [];
                    this.failoverGeneratedRules = [];
                    this.failoverDraftSelections = {};
                    this.failoverDraftFilter = '';
                    this.failoverDraftsOpen = false;
                    this.failoverError = '';
                    this.failoverLoading = false;
                    return;
                }
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
                const targets = this.failoverTargets(rule);
                this.failoverForm = {
                    source,
                    target_model: targets[0] || '',
                    targets: targets.slice(1).map((model) => ({ model })),
                    enabled: rule.enabled !== false
                };
                this.focusFailoverEditor();
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

            closeFailoverDraftsModal() {
                if (this.failoverDraftSaving) return;
                this.failoverDraftsOpen = false;
            },

            failoverFormTargets() {
                const values = [this.failoverForm.target_model];
                const rows = Array.isArray(this.failoverForm.targets) ? this.failoverForm.targets : [];
                rows.forEach((target) => values.push(target && target.model));
                return values.map((value) => String(value || '').trim()).filter(Boolean);
            },

            setFailoverFormTargets(targets) {
                const values = Array.isArray(targets) ? targets.map((value) => String(value || '').trim()).filter(Boolean) : [];
                this.failoverForm.target_model = values[0] || '';
                this.failoverForm.targets = values.slice(1).map((model) => ({ model }));
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
                if (this.failoverSaving || this.failoverGenerating || this.failoverFormManaged) return;
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
                if (!source || this.failoverSaving || this.failoverGenerating) return;
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

            async generateFailoverForForm() {
                if (this.failoverGenerating || this.failoverSaving || this.failoverFormManaged) return;
                const source = String(this.failoverForm.source || '').trim();
                if (!source) {
                    this.failoverError = 'Primary model is required.';
                    return;
                }
                this.failoverGenerating = true;
                this.failoverError = '';
                this.failoverNotice = '';
                try {
                    const request = this.adminRequestOptions({
                        method: 'POST',
                        body: JSON.stringify({ primary_model: source })
                    });
                    const res = await fetch('/admin/failover/generate', request);
                    const handled = this.handleFetchResponse(res, 'failover generation', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.failoverError = 'Failed to generate failover mapping.';
                        return;
                    }
                    const suggestions = this.normalizeFailoverRules(await res.json());
                    const suggestion = suggestions.find((rule) => this.failoverPrimaryModel(rule) === source) || suggestions[0] || null;
                    const targets = this.failoverTargets(suggestion);
                    if (targets.length === 0) {
                        this.failoverError = 'No failover suggestions were generated for this model.';
                        return;
                    }
                    this.setFailoverFormTargets(targets);
                    this.failoverNotice = 'Generated ' + targets.length + ' fallback model' + (targets.length === 1 ? '.' : 's.');
                    this.focusFailoverEditor();
                } catch (e) {
                    console.error('Failed to generate failover mapping:', e);
                    this.failoverError = 'Failed to generate failover mapping.';
                } finally {
                    this.failoverGenerating = false;
                }
            },

            openFailoverResetDialog() {
                this.openTypedConfirmationDialog({
                    title: 'Remove failover models',
                    titleId: 'failoverResetDialogTitle',
                    inputId: 'failover-reset-confirmation',
                    message: 'Remove every dashboard-managed failover mapping. Configuration-managed mappings remain active.',
                    requiredText: 'remove',
                    confirmLabel: 'Remove Failover',
                    icon: 'trash-2',
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
                    const handled = this.handleFetchResponse(res, 'failover removal', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.failoverError = 'Failed to remove failover mappings.';
                        return;
                    }
                    const payload = await res.json();
                    this.failoverRules = this.normalizeFailoverRules(payload);
                    this.failoverGeneratedRules = [];
                    this.failoverDraftSelections = {};
                    this.failoverDraftFilter = '';
                    this.failoverDraftsOpen = false;
                    this.failoverNotice = 'Dashboard-managed failover mappings removed.';
                    this.closeTypedConfirmationDialog();
                } catch (e) {
                    console.error('Failed to remove failover mappings:', e);
                    this.failoverError = 'Failed to remove failover mappings.';
                } finally {
                    this.failoverSaving = false;
                }
            },

            async generateFailoverRules() {
                if (this.failoverGenerating || this.failoverDraftSaving) return;
                this.failoverGenerating = true;
                this.failoverError = '';
                this.failoverNotice = '';
                this.failoverGeneratedRules = [];
                this.failoverDraftSelections = {};
                this.failoverDraftFilter = '';
                this.failoverDraftsOpen = true;
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
                    this.selectAllFailoverDrafts(this.failoverGeneratedRules);
                    this.failoverNotice = this.failoverGeneratedRules.length
                        ? 'Generated ' + this.failoverGeneratedRules.length + ' failover mapping drafts.'
                        : 'No failover suggestions were generated.';
                } catch (e) {
                    console.error('Failed to generate failover mappings:', e);
                    this.failoverError = 'Failed to generate failover mappings.';
                } finally {
                    this.failoverGenerating = false;
                    if (typeof this.renderIconsAfterUpdate === 'function') {
                        this.renderIconsAfterUpdate();
                    }
                }
            },

            failoverDraftKey(rule) {
                return this.failoverPrimaryModel(rule);
            },

            selectAllFailoverDrafts(rules) {
                const selections = {};
                (Array.isArray(rules) ? rules : []).forEach((rule) => {
                    const key = this.failoverDraftKey(rule);
                    if (key) selections[key] = true;
                });
                this.failoverDraftSelections = selections;
            },

            failoverDraftSelected(rule) {
                const key = this.failoverDraftKey(rule);
                return Boolean(key && this.failoverDraftSelections[key]);
            },

            setFailoverDraftSelected(rule, selected) {
                const key = this.failoverDraftKey(rule);
                if (!key) return;
                this.failoverDraftSelections = {
                    ...this.failoverDraftSelections,
                    [key]: Boolean(selected)
                };
            },

            selectedFailoverDrafts() {
                return this.failoverGeneratedRules.filter((rule) => this.failoverDraftSelected(rule));
            },

            selectedFailoverDraftCount() {
                return this.selectedFailoverDrafts().length;
            },

            failoverDraftCountLabel() {
                return this.selectedFailoverDraftCount() + ' / ' + this.failoverGeneratedRules.length + ' selected';
            },

            allFailoverDraftsSelected() {
                return this.failoverGeneratedRules.length > 0 && this.selectedFailoverDraftCount() === this.failoverGeneratedRules.length;
            },

            toggleAllFailoverDrafts() {
                if (this.failoverDraftSaving || this.failoverGenerating || this.failoverGeneratedRules.length === 0) return;
                if (this.allFailoverDraftsSelected()) {
                    this.failoverDraftSelections = {};
                    return;
                }
                this.selectAllFailoverDrafts(this.failoverGeneratedRules);
            },

            failoverDraftSearchText(rule) {
                return [
                    this.failoverPrimaryModel(rule),
                    this.failoverTargets(rule).join(' ')
                ].join(' ').toLowerCase();
            },

            filteredFailoverDrafts() {
                const query = String(this.failoverDraftFilter || '').trim().toLowerCase();
                if (!query) return this.failoverGeneratedRules;
                return this.failoverGeneratedRules.filter((rule) => this.failoverDraftSearchText(rule).includes(query));
            },

            failoverDraftPayload(rule) {
                return {
                    primary_model: this.failoverPrimaryModel(rule),
                    fallback_models: this.failoverTargets(rule).map((model) => String(model || '').trim()).filter(Boolean),
                    enabled: rule && rule.enabled !== false
                };
            },

            async saveSelectedFailoverDrafts() {
                if (this.failoverDraftSaving || this.failoverGenerating) return;
                const drafts = this.selectedFailoverDrafts();
                if (drafts.length === 0) {
                    this.failoverError = 'Select at least one failover draft.';
                    return;
                }
                this.failoverDraftSaving = true;
                this.failoverError = '';
                this.failoverNotice = '';
                try {
                    for (const draft of drafts) {
                        const payload = this.failoverDraftPayload(draft);
                        if (!payload.primary_model || payload.fallback_models.length === 0) {
                            this.failoverError = 'Generated failover draft is missing model data.';
                            return;
                        }
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
                    }
                    this.failoverNotice = 'Saved ' + drafts.length + ' failover mapping' + (drafts.length === 1 ? '.' : 's.');
                    this.failoverDraftsOpen = false;
                    this.failoverGeneratedRules = [];
                    this.failoverDraftSelections = {};
                    this.failoverDraftFilter = '';
                    await this.fetchFailoverRules();
                } catch (e) {
                    console.error('Failed to save generated failover mappings:', e);
                    this.failoverError = 'Failed to save failover mappings.';
                } finally {
                    this.failoverDraftSaving = false;
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
