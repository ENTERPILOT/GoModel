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
            budgets: [],
            budgetsAvailable: true,
            budgetsLoading: false,
            budgetFetchPromise: null,
            budgetFilter: '',
            budgetSortBy: 'user_path',
            budgetError: '',
            budgetNotice: '',
            budgetFormOpen: false,
            budgetFormSubmitting: false,
            budgetFormError: '',
            budgetEditing: false,
            budgetOverrideDialogOpen: false,
            budgetOverridePendingPayload: null,
            budgetOverrideExistingBudget: null,
            budgetResettingKey: '',
            budgetDeletingKey: '',
            budgetForm: {
                user_path: '/',
                period: 'daily',
                period_seconds: 86400,
                amount: '',
                source: 'manual'
            },

            budgetManagementEnabled() {
                return typeof this.workflowRuntimeBooleanFlag === 'function'
                    ? this.workflowRuntimeBooleanFlag('BUDGETS_ENABLED', true)
                    : true;
            },

            defaultBudgetForm() {
                return {
                    user_path: '/',
                    period: 'daily',
                    period_seconds: 86400,
                    amount: '',
                    source: 'manual'
                };
            },

            budgetPeriodOptions() {
                return [
                    { value: 'hourly', label: 'Hourly' },
                    { value: 'daily', label: 'Daily' },
                    { value: 'weekly', label: 'Weekly' },
                    { value: 'monthly', label: 'Monthly' },
                    { value: 'custom', label: 'Custom seconds' }
                ];
            },

            budgetPeriodSeconds(period) {
                switch (String(period || '').trim().toLowerCase()) {
                case 'hourly':
                    return 3600;
                case 'daily':
                    return 86400;
                case 'weekly':
                    return 604800;
                case 'monthly':
                    return 2592000;
                default:
                    return 0;
                }
            },

            budgetPeriodFromSeconds(seconds) {
                switch (Number(seconds || 0)) {
                case 3600:
                    return 'hourly';
                case 86400:
                    return 'daily';
                case 604800:
                    return 'weekly';
                case 2592000:
                    return 'monthly';
                default:
                    return 'custom';
                }
            },

            syncBudgetPeriodSeconds() {
                const period = String(this.budgetForm && this.budgetForm.period || '').trim();
                const seconds = this.budgetPeriodSeconds(period);
                if (seconds > 0) {
                    this.budgetForm.period_seconds = seconds;
                }
            },

            budgetKey(item) {
                return String(item && item.user_path || '') + ':' + String(item && item.period_seconds || '');
            },

            existingBudgetForPayload(payload) {
                if (!payload || !Array.isArray(this.budgets)) {
                    return null;
                }
                const key = this.budgetKey(payload);
                return this.budgets.find((item) => this.budgetKey(item) === key) || null;
            },

            budgetUserPathValidationError(value) {
                const trimmed = String(value || '').trim();
                if (!trimmed) {
                    return 'User path is required.';
                }
                const raw = trimmed.startsWith('/') ? trimmed : '/' + trimmed;
                const segments = raw.split('/');
                for (const part of segments) {
                    const segment = String(part || '').trim();
                    if (!segment) {
                        continue;
                    }
                    if (segment === '.' || segment === '..') {
                        return 'User path cannot contain "." or ".." segments.';
                    }
                    if (segment.includes(':')) {
                        return 'User path cannot contain ":" segments.';
                    }
                }
                return '';
            },

            normalizeBudgetUserPath(value) {
                if (this.budgetUserPathValidationError(value)) {
                    return '';
                }
                const trimmed = String(value || '').trim();
                const raw = trimmed.startsWith('/') ? trimmed : '/' + trimmed;
                const segments = raw.split('/');
                const canonical = [];
                for (const part of segments) {
                    const segment = String(part || '').trim();
                    if (segment) {
                        canonical.push(segment);
                    }
                }
                return canonical.length ? '/' + canonical.join('/') : '/';
            },

            budgetInputUserPath(value) {
                const body = String(value || '').trimStart().replace(/^\/+/, '');
                return '/' + body;
            },

            setBudgetFormUserPath(value) {
                if (!this.budgetForm) {
                    return;
                }
                this.budgetForm.user_path = this.budgetInputUserPath(value);
            },

            normalizeBudgetListPayload(payload) {
                if (Array.isArray(payload)) {
                    return payload;
                }
                if (payload && Array.isArray(payload.budgets)) {
                    return payload.budgets;
                }
                return [];
            },

            filteredBudgets() {
                const filter = String(this.budgetFilter || '').trim().toLowerCase();
                let items;
                if (!filter) {
                    items = this.budgets.slice();
                } else {
                    items = this.budgets.filter((item) => {
                        return this.budgetFilterText(item).includes(filter);
                    });
                }
                return this.sortBudgets(items);
            },

            budgetFilterText(item) {
                const seconds = Number(item && item.period_seconds || 0);
                return [
                    item && item.user_path,
                    this.budgetPeriodLabel(item),
                    this.budgetPeriodFromSeconds(seconds),
                    seconds ? String(seconds) + 's' : '',
                    seconds ? String(seconds) + ' seconds' : ''
                ].join(' ').toLowerCase();
            },

            sortBudgets(items) {
                const sorted = Array.isArray(items) ? items.slice() : [];
                const sortBy = String(this.budgetSortBy || 'user_path');
                sorted.sort((a, b) => {
                    const pathCompare = String(a && a.user_path || '').localeCompare(String(b && b.user_path || ''));
                    const periodCompare = Number(b && b.period_seconds || 0) - Number(a && a.period_seconds || 0);
                    if (sortBy === 'period') {
                        return periodCompare || pathCompare;
                    }
                    return pathCompare || periodCompare;
                });
                return sorted;
            },

            async budgetResponseMessage(res, fallback) {
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

            async fetchBudgetsPage() {
                if (!this.budgetManagementEnabled()) {
                    this.budgets = [];
                    this.budgetsAvailable = false;
                    this.budgetError = '';
                    return;
                }
                if (this.budgetFetchPromise) {
                    return this.budgetFetchPromise;
                }
                this.budgetFetchPromise = this.fetchBudgets().finally(() => {
                    this.budgetFetchPromise = null;
                });
                return this.budgetFetchPromise;
            },

            async fetchBudgets() {
                this.budgetsLoading = true;
                this.budgetError = '';
                try {
                    const request = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    const res = await fetch('/admin/api/v1/budgets', request);
                    if (res.status === 503) {
                        this.budgetsAvailable = false;
                        this.budgets = [];
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'budgets', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    this.budgetsAvailable = true;
                    if (!handled) {
                        this.budgetError = 'Unable to load budgets.';
                        return;
                    }
                    this.budgets = this.normalizeBudgetListPayload(await res.json());
                    if (typeof this.renderIconsAfterUpdate === 'function') {
                        this.renderIconsAfterUpdate();
                    }
                } catch (e) {
                    console.error('Failed to fetch budgets:', e);
                    this.budgets = [];
                    this.budgetError = 'Unable to load budgets.';
                } finally {
                    this.budgetsLoading = false;
                }
            },

            openBudgetForm(item) {
                this.budgetEditing = !!item;
                this.budgetFormError = '';
                this.budgetError = '';
                this.budgetNotice = '';
                if (item) {
                    const periodSeconds = Number(item.period_seconds || 0);
                    this.budgetForm = {
                        user_path: String(item.user_path || ''),
                        period: this.budgetPeriodFromSeconds(periodSeconds),
                        period_seconds: periodSeconds,
                        amount: String(item.amount || ''),
                        source: String(item.source || 'manual')
                    };
                } else {
                    this.budgetForm = this.defaultBudgetForm();
                }
                this.budgetFormOpen = true;
                if (typeof this.renderIconsAfterUpdate === 'function') {
                    this.renderIconsAfterUpdate();
                }
                if (typeof this.$nextTick === 'function') {
                    this.$nextTick(() => {
                        const refs = this.$refs || {};
                        const input = this.budgetEditing ? refs.budgetAmountInput : refs.budgetUserPathInput;
                        if (input && typeof input.focus === 'function') {
                            input.focus({ preventScroll: true });
                        }
                    });
                }
            },

            closeBudgetForm() {
                this.closeBudgetOverrideDialog();
                this.budgetFormOpen = false;
                this.budgetFormSubmitting = false;
                this.budgetFormError = '';
                this.budgetEditing = false;
                this.budgetForm = this.defaultBudgetForm();
            },

            budgetFormPayload() {
                const userPathError = this.budgetUserPathValidationError(this.budgetForm.user_path);
                if (userPathError) {
                    this.budgetFormError = userPathError;
                    return null;
                }
                const amount = Number(this.budgetForm.amount);
                if (!Number.isFinite(amount) || amount <= 0) {
                    this.budgetFormError = 'Amount must be greater than 0.';
                    return null;
                }
                const period = String(this.budgetForm.period || '').trim();
                let periodSeconds = this.budgetPeriodSeconds(period);
                if (period === 'custom') {
                    periodSeconds = Number(this.budgetForm.period_seconds);
                }
                if (!Number.isFinite(periodSeconds) || periodSeconds <= 0) {
                    this.budgetFormError = 'Period seconds must be greater than 0.';
                    return null;
                }
                return {
                    user_path: this.normalizeBudgetUserPath(this.budgetForm.user_path),
                    period_seconds: Math.trunc(periodSeconds),
                    amount,
                    source: String(this.budgetForm.source || 'manual').trim() || 'manual'
                };
            },

            budgetAPIPath(userPath, periodSeconds) {
                const encodedPath = encodeURIComponent(this.normalizeBudgetUserPath(userPath));
                const encodedPeriod = encodeURIComponent(String(Math.trunc(Number(periodSeconds || 0))));
                return '/admin/api/v1/budgets/' + encodedPath + '/' + encodedPeriod;
            },

            openBudgetOverrideDialog(existing, payload) {
                this.budgetOverrideExistingBudget = existing || null;
                this.budgetOverridePendingPayload = payload || null;
                this.budgetOverrideDialogOpen = true;
                if (typeof this.renderIconsAfterUpdate === 'function') {
                    this.renderIconsAfterUpdate();
                }
                if (typeof this.$nextTick === 'function') {
                    this.$nextTick(() => {
                        const refs = this.$refs || {};
                        const button = refs.budgetOverrideCancelButton;
                        if (button && typeof button.focus === 'function') {
                            button.focus({ preventScroll: true });
                        }
                    });
                }
            },

            closeBudgetOverrideDialog() {
                this.budgetOverrideDialogOpen = false;
                this.budgetOverridePendingPayload = null;
                this.budgetOverrideExistingBudget = null;
            },

            budgetAmountLabel(value) {
                const amount = Number(value);
                if (!Number.isFinite(amount)) {
                    return 'N/A';
                }
                if (typeof this.formatCost === 'function') {
                    return this.formatCost(amount);
                }
                return '$' + amount.toFixed(4);
            },

            budgetOverrideDialogMessage() {
                const payload = this.budgetOverridePendingPayload || {};
                const existing = this.budgetOverrideExistingBudget || {};
                const label = String(payload.user_path || existing.user_path || '') + ' ' + this.budgetPeriodLabel({
                    period_seconds: payload.period_seconds || existing.period_seconds,
                    period_label: existing.period_label
                });
                return 'A budget for "' + label + '" already exists. Saving will override the current '
                    + this.budgetAmountLabel(existing.amount) + ' limit with '
                    + this.budgetAmountLabel(payload.amount) + '.';
            },

            async confirmBudgetOverride() {
                if (!this.budgetOverridePendingPayload) {
                    this.closeBudgetOverrideDialog();
                    return;
                }
                const payload = this.budgetOverridePendingPayload;
                this.closeBudgetOverrideDialog();
                await this.saveBudgetPayload(payload);
            },

            async submitBudgetForm() {
                if (this.budgetFormSubmitting) {
                    return;
                }
                const payload = this.budgetFormPayload();
                if (!payload) {
                    return;
                }
                if (!this.budgetEditing) {
                    const existing = this.existingBudgetForPayload(payload);
                    if (existing) {
                        this.openBudgetOverrideDialog(existing, payload);
                        return;
                    }
                }
                await this.saveBudgetPayload(payload);
            },

            async saveBudgetPayload(payload) {
                if (this.budgetFormSubmitting || !payload) {
                    return;
                }
                this.budgetFormSubmitting = true;
                this.budgetFormError = '';
                this.budgetError = '';
                this.budgetNotice = '';
                try {
                    const request = typeof this.requestOptions === 'function'
                        ? this.requestOptions({
                            method: 'PUT',
                            body: JSON.stringify({ amount: payload.amount })
                        })
                        : {
                            method: 'PUT',
                            headers: this.headers(),
                            body: JSON.stringify({ amount: payload.amount })
                        };
                    const res = await fetch(this.budgetAPIPath(payload.user_path, payload.period_seconds), request);
                    if (res.status === 503) {
                        this.budgetsAvailable = false;
                        this.budgetFormError = 'Budget management is unavailable.';
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'budget', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.budgetFormError = await this.budgetResponseMessage(res, 'Unable to save budget.');
                        return;
                    }
                    this.closeBudgetForm();
                    await this.fetchBudgets();
                    this.budgetNotice = 'Budget saved.';
                } catch (e) {
                    console.error('Failed to save budget:', e);
                    this.budgetFormError = 'Unable to save budget.';
                } finally {
                    this.budgetFormSubmitting = false;
                }
            },

            async resetBudget(item) {
                if (!item) {
                    return;
                }
                const key = this.budgetKey(item);
                if (this.budgetResettingKey === key) {
                    return;
                }
                const label = String(item.user_path || '') + ' ' + this.budgetPeriodLabel(item);
                if (global.confirm && !global.confirm('Reset budget "' + label + '"?')) {
                    return;
                }
                this.budgetResettingKey = key;
                this.budgetError = '';
                this.budgetNotice = '';
                try {
                    const request = typeof this.requestOptions === 'function'
                        ? this.requestOptions({
                            method: 'POST',
                            body: JSON.stringify({
                                user_path: item.user_path,
                                period_seconds: item.period_seconds
                            })
                        })
                        : {
                            method: 'POST',
                            headers: this.headers(),
                            body: JSON.stringify({
                                user_path: item.user_path,
                                period_seconds: item.period_seconds
                            })
                        };
                    const res = await fetch('/admin/api/v1/budgets/reset-one', request);
                    if (res.status === 503) {
                        this.budgetsAvailable = false;
                        this.budgetError = 'Budget management is unavailable.';
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'budget reset', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.budgetError = await this.budgetResponseMessage(res, 'Unable to reset budget.');
                        return;
                    }
                    await this.fetchBudgets();
                    this.budgetNotice = 'Budget reset.';
                } catch (e) {
                    console.error('Failed to reset budget:', e);
                    this.budgetError = 'Unable to reset budget.';
                } finally {
                    this.budgetResettingKey = '';
                }
            },

            async deleteBudget(item) {
                if (!item) {
                    return;
                }
                const key = this.budgetKey(item);
                if (this.budgetDeletingKey === key) {
                    return;
                }
                const label = String(item.user_path || '') + ' ' + this.budgetPeriodLabel(item);
                if (global.confirm && !global.confirm('Delete budget "' + label + '"? This cannot be undone.')) {
                    return;
                }
                this.budgetDeletingKey = key;
                this.budgetError = '';
                this.budgetNotice = '';
                try {
                    const request = typeof this.requestOptions === 'function'
                        ? this.requestOptions({
                            method: 'DELETE'
                        })
                        : {
                            method: 'DELETE',
                            headers: this.headers()
                        };
                    const res = await fetch(this.budgetAPIPath(item.user_path, item.period_seconds), request);
                    if (res.status === 503) {
                        this.budgetsAvailable = false;
                        this.budgetError = 'Budget management is unavailable.';
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'budget delete', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.budgetError = await this.budgetResponseMessage(res, 'Unable to delete budget.');
                        return;
                    }
                    this.budgets = this.normalizeBudgetListPayload(await res.json());
                    if (typeof this.renderIconsAfterUpdate === 'function') {
                        this.renderIconsAfterUpdate();
                    }
                    this.budgetNotice = 'Budget deleted.';
                } catch (e) {
                    console.error('Failed to delete budget:', e);
                    this.budgetError = 'Unable to delete budget.';
                } finally {
                    this.budgetDeletingKey = '';
                }
            },

            budgetRatio(value) {
                const parsed = Number(value);
                if (!Number.isFinite(parsed) || parsed < 0) {
                    return 0;
                }
                return parsed;
            },

            budgetPercentFromRatio(value, clamp) {
                const ratio = this.budgetRatio(value);
                const bounded = clamp ? Math.min(ratio, 1) : ratio;
                return Math.round(bounded * 1000) / 10;
            },

            budgetUsageRatio(item) {
                return this.budgetRatio(item && item.usage_ratio);
            },

            budgetUsagePercent(item) {
                return this.budgetPercentFromRatio(this.budgetUsageRatio(item), true);
            },

            budgetPeriodPercent(item) {
                return this.budgetPercentFromRatio(item && item.period_ratio, true);
            },

            budgetUsagePercentLabel(item) {
                return this.budgetPercentFromRatio(this.budgetUsageRatio(item), false).toFixed(1).replace(/\.0$/, '') + '%';
            },

            budgetPeriodPercentLabel(item) {
                return this.budgetPeriodPercent(item).toFixed(1).replace(/\.0$/, '') + '%';
            },

            budgetPeriodLabel(item) {
                const seconds = Number(item && item.period_seconds || 0);
                switch (seconds) {
                case 3600:
                    return 'Hourly';
                case 86400:
                    return 'Daily';
                case 604800:
                    return 'Weekly';
                case 2592000:
                    return 'Monthly';
                default: {
                    const label = String(item && item.period_label || '').trim();
                    return label ? 'Custom ' + label : 'Custom ' + String(seconds || '') + 's';
                }
                }
            },

            budgetPeriodClass(item) {
                const seconds = Number(item && item.period_seconds || 0);
                switch (seconds) {
                case 3600:
                    return 'budget-period-label-hourly';
                case 86400:
                    return 'budget-period-label-daily';
                case 604800:
                    return 'budget-period-label-weekly';
                case 2592000:
                    return 'budget-period-label-monthly';
                default:
                    return 'budget-period-label-custom';
                }
            },

            budgetPeriodBarClass(item) {
                return this.budgetPeriodClass(item).replace('budget-period-label-', 'budget-bar-fill-period-');
            },

            budgetPeriodTrackClass(item) {
                return this.budgetPeriodClass(item).replace('budget-period-label-', 'budget-bar-track-period-');
            },

            budgetPeriodIcon(item) {
                const seconds = Number(item && item.period_seconds || 0);
                switch (seconds) {
                case 3600:
                    return 'clock';
                case 86400:
                    return 'sun';
                case 604800:
                    return 'calendar-days';
                case 2592000:
                    return 'calendar';
                default:
                    return 'settings-2';
                }
            },

            budgetPeriodDurationLabel(item) {
                const seconds = Number(item && item.period_seconds || 0);
                switch (seconds) {
                case 3600:
                    return '1 hour';
                case 86400:
                    return '1 day';
                case 604800:
                    return '1 week';
                case 2592000:
                    return '1 month';
                default:
                    return this.formatBudgetPeriodSeconds(seconds);
                }
            },

            formatBudgetPeriodSeconds(seconds) {
                const normalized = Math.max(0, Math.trunc(Number(seconds || 0)));
                return normalized + ' ' + (normalized === 1 ? 'second' : 'seconds');
            },

            budgetSourceLabel(item) {
                const source = String(item && item.source || '').trim();
                return source || 'manual';
            },

            budgetSourceTitle(item) {
                const source = this.budgetSourceLabel(item).toLowerCase();
                if (source === 'manual') {
                    return 'Created from the dashboard.';
                }
                if (source === 'config') {
                    return 'Loaded from configuration.';
                }
                return 'Budget source: ' + source;
            },

            budgetRemainingLabel(item) {
                const remaining = Number(item && item.remaining);
                if (!Number.isFinite(remaining)) {
                    return '';
                }
                if (remaining < 0) {
                    return this.formatCost(Math.abs(remaining)) + ' over';
                }
                return this.formatCost(remaining) + ' remaining';
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
                const integerValue = (value, fallback) => {
                    if (value === '') {
                        return fallback;
                    }
                    const parsed = Number(value);
                    return Number.isFinite(parsed) && Number.isInteger(parsed) ? Math.trunc(parsed) : fallback;
                };
                const currentValue = (key, fallback) => integerValue(current[key], fallback);
                const numberValue = (key, fallback) => {
                    if (!payload) {
                        return fallback;
                    }
                    return integerValue(payload[key], fallback);
                };
                return {
                    daily_reset_hour: numberValue('daily_reset_hour', currentValue('daily_reset_hour', 0)),
                    daily_reset_minute: numberValue('daily_reset_minute', currentValue('daily_reset_minute', 0)),
                    weekly_reset_weekday: numberValue('weekly_reset_weekday', currentValue('weekly_reset_weekday', 1)),
                    weekly_reset_hour: numberValue('weekly_reset_hour', currentValue('weekly_reset_hour', 0)),
                    weekly_reset_minute: numberValue('weekly_reset_minute', currentValue('weekly_reset_minute', 0)),
                    monthly_reset_day: numberValue('monthly_reset_day', currentValue('monthly_reset_day', 1)),
                    monthly_reset_hour: numberValue('monthly_reset_hour', currentValue('monthly_reset_hour', 0)),
                    monthly_reset_minute: numberValue('monthly_reset_minute', currentValue('monthly_reset_minute', 0))
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
                    if (this.page === 'budgets' && typeof this.fetchBudgets === 'function') {
                        await this.fetchBudgets();
                    }
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
