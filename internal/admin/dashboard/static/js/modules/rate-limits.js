(function(global) {
    function dashboardRateLimitsModule() {
        return {
            rateLimits: [],
            rateLimitsAvailable: true,
            rateLimitsLoading: false,
            rateLimitFetchPromise: null,
            rateLimitFilter: '',
            rateLimitError: '',
            rateLimitNotice: '',
            rateLimitFormOpen: false,
            rateLimitFormSubmitting: false,
            rateLimitFormError: '',
            rateLimitEditing: false,
            rateLimitResettingKey: '',
            rateLimitDeletingKey: '',
            rateLimitForm: {
                user_path: '/',
                period: 'minute',
                period_seconds: 60,
                max_requests: '',
                max_tokens: '',
                source: 'manual'
            },

            rateLimitsEnabled() {
                return typeof this.workflowRuntimeBooleanFlag === 'function'
                    ? this.workflowRuntimeBooleanFlag('RATE_LIMITS_ENABLED', true)
                    : true;
            },

            defaultRateLimitForm() {
                return {
                    user_path: '/',
                    period: 'minute',
                    period_seconds: 60,
                    max_requests: '',
                    max_tokens: '',
                    source: 'manual'
                };
            },

            rateLimitPeriodOptions() {
                return [
                    { value: 'minute', label: 'Per minute' },
                    { value: 'hour', label: 'Per hour' },
                    { value: 'day', label: 'Per day' },
                    { value: 'concurrent', label: 'Concurrent (in-flight)' },
                    { value: 'custom', label: 'Custom seconds' }
                ];
            },

            rateLimitPeriodSeconds(period) {
                switch (String(period || '').trim().toLowerCase()) {
                case 'minute':
                    return 60;
                case 'hour':
                    return 3600;
                case 'day':
                    return 86400;
                case 'concurrent':
                    return 0;
                default:
                    return -1;
                }
            },

            rateLimitPeriodFromSeconds(seconds) {
                switch (Number(seconds || 0)) {
                case 60:
                    return 'minute';
                case 3600:
                    return 'hour';
                case 86400:
                    return 'day';
                case 0:
                    return 'concurrent';
                default:
                    return 'custom';
                }
            },

            syncRateLimitPeriodSeconds() {
                const period = String(this.rateLimitForm && this.rateLimitForm.period || '').trim();
                const seconds = this.rateLimitPeriodSeconds(period);
                if (seconds >= 0) {
                    this.rateLimitForm.period_seconds = seconds;
                }
            },

            rateLimitKey(item) {
                return String(item && item.user_path || '') + ':' + String(item && item.period_seconds || '0');
            },

            rateLimitIsConcurrent(item) {
                return Number(item && item.period_seconds || 0) === 0;
            },

            rateLimitPeriodLabel(item) {
                const label = String(item && item.period_label || '').trim();
                if (label) {
                    return label;
                }
                return this.rateLimitPeriodFromSeconds(Number(item && item.period_seconds || 0));
            },

            rateLimitSourceLabel(item) {
                return String(item && item.source || '') === 'config' ? 'config' : 'manual';
            },

            rateLimitIsReadOnly(item) {
                return String(item && item.source || '') === 'config';
            },

            formatRateLimitNumber(value) {
                const numeric = Number(value);
                if (!Number.isFinite(numeric)) {
                    return '0';
                }
                return numeric.toLocaleString();
            },

            rateLimitUsagePercent(used, limit) {
                const usedNum = Number(used);
                const limitNum = Number(limit);
                if (!Number.isFinite(usedNum) || !Number.isFinite(limitNum) || limitNum <= 0) {
                    return 0;
                }
                const percent = Math.round((usedNum / limitNum) * 100);
                return Math.min(Math.max(percent, 0), 100);
            },

            filteredRateLimits() {
                const filter = String(this.rateLimitFilter || '').trim().toLowerCase();
                const items = Array.isArray(this.rateLimits) ? this.rateLimits.slice() : [];
                items.sort((a, b) => {
                    const pathCompare = String(a.user_path || '').localeCompare(String(b.user_path || ''));
                    if (pathCompare !== 0) {
                        return pathCompare;
                    }
                    return Number(a.period_seconds || 0) - Number(b.period_seconds || 0);
                });
                if (!filter) {
                    return items;
                }
                return items.filter((item) => {
                    const path = String(item.user_path || '').toLowerCase();
                    const period = this.rateLimitPeriodLabel(item).toLowerCase();
                    return path.includes(filter) || period.includes(filter);
                });
            },

            normalizeRateLimitListPayload(payload) {
                if (!payload || !Array.isArray(payload.rate_limits)) {
                    return [];
                }
                return payload.rate_limits;
            },

            async fetchRateLimitsPage() {
                if (!this.rateLimitsEnabled()) {
                    this.rateLimits = [];
                    this.rateLimitsAvailable = false;
                    this.rateLimitError = '';
                    return;
                }
                if (this.rateLimitFetchPromise) {
                    return this.rateLimitFetchPromise;
                }
                this.rateLimitFetchPromise = this.fetchRateLimits().finally(() => {
                    this.rateLimitFetchPromise = null;
                });
                return this.rateLimitFetchPromise;
            },

            async fetchRateLimits() {
                this.rateLimitsLoading = true;
                this.rateLimitError = '';
                try {
                    const request = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    const res = await fetch('/admin/rate-limits', request);
                    if (res.status === 503) {
                        this.rateLimitsAvailable = false;
                        this.rateLimits = [];
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'rate limits', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    this.rateLimitsAvailable = true;
                    if (!handled) {
                        this.rateLimitError = 'Unable to load rate limits.';
                        return;
                    }
                    this.rateLimits = this.normalizeRateLimitListPayload(await res.json());
                    if (typeof this.renderIconsAfterUpdate === 'function') {
                        this.renderIconsAfterUpdate();
                    }
                } catch (e) {
                    console.error('Failed to fetch rate limits:', e);
                    this.rateLimits = [];
                    this.rateLimitError = 'Unable to load rate limits.';
                } finally {
                    this.rateLimitsLoading = false;
                }
            },

            openRateLimitForm(item) {
                this.rateLimitEditing = !!item;
                this.rateLimitFormError = '';
                this.rateLimitError = '';
                this.rateLimitNotice = '';
                if (item) {
                    const periodSeconds = Number(item.period_seconds || 0);
                    this.rateLimitForm = {
                        user_path: String(item.user_path || ''),
                        period: this.rateLimitPeriodFromSeconds(periodSeconds),
                        period_seconds: periodSeconds,
                        max_requests: item.max_requests === null || item.max_requests === undefined ? '' : String(item.max_requests),
                        max_tokens: item.max_tokens === null || item.max_tokens === undefined ? '' : String(item.max_tokens),
                        source: String(item.source || 'manual')
                    };
                } else {
                    this.rateLimitForm = this.defaultRateLimitForm();
                }
                this.rateLimitFormOpen = true;
                if (typeof this.renderIconsAfterUpdate === 'function') {
                    this.renderIconsAfterUpdate();
                }
                if (typeof this.$nextTick === 'function') {
                    this.$nextTick(() => {
                        const refs = this.$refs || {};
                        const input = this.rateLimitEditing ? refs.rateLimitMaxRequestsInput : refs.rateLimitUserPathInput;
                        if (input && typeof input.focus === 'function') {
                            input.focus({ preventScroll: true });
                        }
                    });
                }
            },

            closeRateLimitForm() {
                this.rateLimitFormOpen = false;
                this.rateLimitFormSubmitting = false;
                this.rateLimitFormError = '';
                this.rateLimitEditing = false;
                this.rateLimitForm = this.defaultRateLimitForm();
            },

            setRateLimitFormUserPath(value) {
                this.rateLimitForm.user_path = String(value || '');
            },

            rateLimitFormPayload() {
                const form = this.rateLimitForm || {};
                const isConcurrent = String(form.period || '') === 'concurrent';
                // Reject blank custom seconds before Number(): Number('') is 0,
                // which would silently submit a concurrent rule.
                const rawPeriodSeconds = form.period_seconds;
                if (rawPeriodSeconds === '' || rawPeriodSeconds === null || rawPeriodSeconds === undefined) {
                    return { error: 'Period seconds is required.' };
                }
                const periodSeconds = Number(rawPeriodSeconds);
                if (!Number.isInteger(periodSeconds) || periodSeconds < 0 || (periodSeconds === 0 && !isConcurrent)) {
                    return { error: 'Period seconds must be a positive integer (0 only for the concurrent period).' };
                }
                const maxRequests = String(form.max_requests === undefined || form.max_requests === null ? '' : form.max_requests).trim();
                const maxTokens = String(form.max_tokens === undefined || form.max_tokens === null ? '' : form.max_tokens).trim();
                if (!maxRequests && !maxTokens) {
                    return { error: 'Set max requests, max tokens, or both.' };
                }
                if (isConcurrent && maxTokens) {
                    return { error: 'Token limits are not valid for the concurrent period.' };
                }
                const payload = {
                    user_path: String(form.user_path || '').trim() || '/',
                    limit_key: { period_seconds: periodSeconds }
                };
                if (maxRequests) {
                    const parsed = Number(maxRequests);
                    if (!Number.isInteger(parsed) || parsed <= 0) {
                        return { error: 'Max requests must be a positive integer.' };
                    }
                    payload.max_requests = parsed;
                }
                if (maxTokens) {
                    const parsed = Number(maxTokens);
                    if (!Number.isInteger(parsed) || parsed <= 0) {
                        return { error: 'Max tokens must be a positive integer.' };
                    }
                    payload.max_tokens = parsed;
                }
                return { payload };
            },

            async submitRateLimitForm() {
                if (this.rateLimitFormSubmitting) {
                    return;
                }
                const { payload, error } = this.rateLimitFormPayload();
                if (error) {
                    this.rateLimitFormError = error;
                    return;
                }
                this.rateLimitFormSubmitting = true;
                this.rateLimitFormError = '';
                try {
                    const request = this.requestOptions({
                        method: 'PUT',
                        body: JSON.stringify(payload)
                    });
                    const res = await fetch('/admin/rate-limits', request);
                    const handled = this.handleFetchResponse(res, 'rate limit save', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.rateLimitFormError = await this.rateLimitResponseError(res, 'Unable to save rate limit.');
                        return;
                    }
                    this.rateLimits = this.normalizeRateLimitListPayload(await res.json());
                    this.closeRateLimitForm();
                    this.rateLimitNotice = 'Rate limit saved.';
                    if (typeof this.renderIconsAfterUpdate === 'function') {
                        this.renderIconsAfterUpdate();
                    }
                } catch (e) {
                    console.error('Failed to save rate limit:', e);
                    this.rateLimitFormError = 'Unable to save rate limit.';
                } finally {
                    this.rateLimitFormSubmitting = false;
                }
            },

            async deleteRateLimit(item) {
                const key = this.rateLimitKey(item);
                if (this.rateLimitDeletingKey === key) {
                    return;
                }
                this.rateLimitDeletingKey = key;
                this.rateLimitError = '';
                this.rateLimitNotice = '';
                try {
                    const request = this.requestOptions({
                        method: 'DELETE',
                        body: JSON.stringify({
                            user_path: item.user_path,
                            limit_key: { period_seconds: Number(item.period_seconds || 0) }
                        })
                    });
                    const res = await fetch('/admin/rate-limits', request);
                    const handled = this.handleFetchResponse(res, 'rate limit delete', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.rateLimitError = await this.rateLimitResponseError(res, 'Unable to delete rate limit.');
                        return;
                    }
                    this.rateLimits = this.normalizeRateLimitListPayload(await res.json());
                    this.rateLimitNotice = 'Rate limit deleted.';
                    if (typeof this.renderIconsAfterUpdate === 'function') {
                        this.renderIconsAfterUpdate();
                    }
                } catch (e) {
                    console.error('Failed to delete rate limit:', e);
                    this.rateLimitError = 'Unable to delete rate limit.';
                } finally {
                    this.rateLimitDeletingKey = '';
                }
            },

            async resetRateLimit(item) {
                const key = this.rateLimitKey(item);
                if (this.rateLimitResettingKey === key) {
                    return;
                }
                this.rateLimitResettingKey = key;
                this.rateLimitError = '';
                this.rateLimitNotice = '';
                try {
                    const request = this.requestOptions({
                        method: 'POST',
                        body: JSON.stringify({
                            user_path: item.user_path,
                            period_seconds: Number(item.period_seconds || 0)
                        })
                    });
                    const res = await fetch('/admin/rate-limits/reset-one', request);
                    const handled = this.handleFetchResponse(res, 'rate limit reset', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.rateLimitError = await this.rateLimitResponseError(res, 'Unable to reset rate limit.');
                        return;
                    }
                    this.rateLimits = this.normalizeRateLimitListPayload(await res.json());
                    this.rateLimitNotice = 'Rate limit counters reset.';
                    if (typeof this.renderIconsAfterUpdate === 'function') {
                        this.renderIconsAfterUpdate();
                    }
                } catch (e) {
                    console.error('Failed to reset rate limit:', e);
                    this.rateLimitError = 'Unable to reset rate limit.';
                } finally {
                    this.rateLimitResettingKey = '';
                }
            },

            async rateLimitResponseError(res, fallback) {
                try {
                    const body = await res.json();
                    const message = body && body.error && body.error.message;
                    return message ? String(message) : fallback;
                } catch (_) {
                    return fallback;
                }
            }
        };
    }

    global.dashboardRateLimitsModule = dashboardRateLimitsModule;
})(typeof window !== 'undefined' ? window : globalThis);
