(function(global) {
    function dashboardBudgetsModule() {
        return {
            budgetSettings: {
                daily_reset_hour: 0,
                daily_reset_minute: 0,
                weekly_reset_weekday: 1,
                weekly_reset_hour: 0,
                weekly_reset_minute: 0,
                monthly_reset_day: 1,
                monthly_reset_hour: 0,
                monthly_reset_minute: 0
            },
            budgetSettingsLoading: false,
            budgetSettingsSaving: false,
            budgetSettingsNotice: '',
            budgetSettingsError: '',
            budgetResetDialogOpen: false,
            budgetResetConfirmation: '',
            budgetResetLoading: false,

            budgetManagementEnabled() {
                return typeof this.workflowRuntimeBooleanFlag === 'function'
                    ? this.workflowRuntimeBooleanFlag('BUDGETS_ENABLED', true)
                    : true;
            },

            budgetWeekdays() {
                return [
                    { value: 0, label: 'Sunday' },
                    { value: 1, label: 'Monday' },
                    { value: 2, label: 'Tuesday' },
                    { value: 3, label: 'Wednesday' },
                    { value: 4, label: 'Thursday' },
                    { value: 5, label: 'Friday' },
                    { value: 6, label: 'Saturday' }
                ];
            },

            normalizeBudgetSettings(payload) {
                const current = this.budgetSettings || {};
                const numberValue = (key, fallback) => {
                    const parsed = Number(payload && payload[key]);
                    return Number.isFinite(parsed) ? parsed : fallback;
                };
                return {
                    daily_reset_hour: numberValue('daily_reset_hour', Number(current.daily_reset_hour || 0)),
                    daily_reset_minute: numberValue('daily_reset_minute', Number(current.daily_reset_minute || 0)),
                    weekly_reset_weekday: numberValue('weekly_reset_weekday', Number(current.weekly_reset_weekday || 1)),
                    weekly_reset_hour: numberValue('weekly_reset_hour', Number(current.weekly_reset_hour || 0)),
                    weekly_reset_minute: numberValue('weekly_reset_minute', Number(current.weekly_reset_minute || 0)),
                    monthly_reset_day: numberValue('monthly_reset_day', Number(current.monthly_reset_day || 1)),
                    monthly_reset_hour: numberValue('monthly_reset_hour', Number(current.monthly_reset_hour || 0)),
                    monthly_reset_minute: numberValue('monthly_reset_minute', Number(current.monthly_reset_minute || 0))
                };
            },

            budgetSettingsPayload() {
                return this.normalizeBudgetSettings(this.budgetSettings);
            },

            async fetchBudgetSettings() {
                if (!this.budgetManagementEnabled()) {
                    this.budgetSettingsError = '';
                    return;
                }
                this.budgetSettingsLoading = true;
                this.budgetSettingsError = '';
                try {
                    const request = this.requestOptions();
                    const res = await fetch('/admin/api/v1/budgets/settings', request);
                    const handled = this.handleFetchResponse(res, 'budget settings', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.budgetSettingsError = 'Unable to load budget settings.';
                        return;
                    }
                    this.budgetSettings = this.normalizeBudgetSettings(await res.json());
                } catch (e) {
                    console.error('Failed to fetch budget settings:', e);
                    this.budgetSettingsError = 'Unable to load budget settings.';
                } finally {
                    this.budgetSettingsLoading = false;
                }
            },

            async saveBudgetSettings() {
                if (this.budgetSettingsSaving) {
                    return;
                }
                this.budgetSettingsSaving = true;
                this.budgetSettingsNotice = '';
                this.budgetSettingsError = '';
                try {
                    const request = this.requestOptions({
                        method: 'PUT',
                        body: JSON.stringify(this.budgetSettingsPayload())
                    });
                    const res = await fetch('/admin/api/v1/budgets/settings', request);
                    const handled = this.handleFetchResponse(res, 'budget settings', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.budgetSettingsError = 'Unable to save budget settings.';
                        return;
                    }
                    this.budgetSettings = this.normalizeBudgetSettings(await res.json());
                    this.budgetSettingsNotice = 'Budget settings saved.';
                } catch (e) {
                    console.error('Failed to save budget settings:', e);
                    this.budgetSettingsError = 'Unable to save budget settings.';
                } finally {
                    this.budgetSettingsSaving = false;
                }
            },

            openBudgetResetDialog() {
                this.budgetResetConfirmation = '';
                this.budgetSettingsError = '';
                this.budgetResetDialogOpen = true;
                setTimeout(() => {
                    const input = document.getElementById('budget-reset-confirmation');
                    if (input && typeof input.focus === 'function') {
                        input.focus();
                    }
                }, 0);
            },

            closeBudgetResetDialog() {
                this.budgetResetDialogOpen = false;
                this.budgetResetConfirmation = '';
            },

            async resetBudgets() {
                if (this.budgetResetLoading) {
                    return;
                }
                if (String(this.budgetResetConfirmation || '').trim().toLowerCase() !== 'reset') {
                    this.budgetSettingsError = 'Type reset to confirm.';
                    return;
                }
                this.budgetResetLoading = true;
                this.budgetSettingsNotice = '';
                this.budgetSettingsError = '';
                try {
                    const request = this.requestOptions({
                        method: 'POST',
                        body: JSON.stringify({ confirmation: 'reset' })
                    });
                    const res = await fetch('/admin/api/v1/budgets/reset', request);
                    const handled = this.handleFetchResponse(res, 'budget reset', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.budgetSettingsError = 'Unable to reset budgets.';
                        return;
                    }
                    this.closeBudgetResetDialog();
                    this.budgetSettingsNotice = 'Budgets reset.';
                } catch (e) {
                    console.error('Failed to reset budgets:', e);
                    this.budgetSettingsError = 'Unable to reset budgets.';
                } finally {
                    this.budgetResetLoading = false;
                }
            }
        };
    }

    global.dashboardBudgetsModule = dashboardBudgetsModule;
})(typeof window !== 'undefined' ? window : globalThis);
