(function(global) {
    function dashboardAuthKeysModule() {
        const clipboardModuleFactory = typeof global.dashboardClipboardModule === 'function'
            ? global.dashboardClipboardModule
            : null;
        const clipboard = clipboardModuleFactory
            ? clipboardModuleFactory()
            : null;

        return {
            authKeys: [],
            authKeysAvailable: true,
            authKeysLoading: false,
            authKeyError: '',
            authKeyNotice: '',
            authKeyFormOpen: false,
            authKeyFormSubmitting: false,
            authKeyIssuedValue: '',
            authKeyDeactivatingID: '',
            authKeyCopyState: clipboard
                ? clipboard.createClipboardButtonState({
                    logPrefix: 'Failed to copy auth key:'
                })
                : {
                    copied: false,
                    error: false,
                    resetFeedback() {},
                    copy() {
                        return Promise.resolve();
                    }
                },
            authKeyForm: {
                name: '',
                description: '',
                user_path: '',
                expires_at: ''
            },

            defaultAuthKeyForm() {
                return { name: '', description: '', user_path: '', expires_at: '' };
            },

            authKeyUserPathValidationError(value) {
                const trimmed = String(value || '').trim();
                if (!trimmed) {
                    return '';
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

            normalizeAuthKeyUserPath(value) {
                if (this.authKeyUserPathValidationError(value)) {
                    return '';
                }
                const trimmed = String(value || '').trim();
                if (!trimmed) {
                    return '';
                }
                const raw = trimmed.startsWith('/') ? trimmed : '/' + trimmed;
                const segments = raw.split('/');
                const canonical = [];
                for (const part of segments) {
                    const segment = String(part || '').trim();
                    if (!segment) {
                        continue;
                    }
                    canonical.push(segment);
                }
                if (!canonical.length) {
                    return '/';
                }
                return '/' + canonical.join('/');
            },

            async fetchAuthKeys() {
                this.authKeysLoading = true;
                this.authKeyError = '';
                try {
                    const request = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    const res = await fetch('/admin/api/v1/auth-keys', request);
                    if (res.status === 503) {
                        this.authKeysAvailable = false;
                        this.authKeys = [];
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'auth keys', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    this.authKeysAvailable = true;
                    if (!handled) {
                        if (res.status !== 401) {
                            this.authKeyError = await this._authKeyResponseMessage(res, 'Unable to load API keys.');
                        }
                        return;
                    }
                    const payload = await res.json();
                    this.authKeys = Array.isArray(payload) ? payload : [];
                } catch (e) {
                    console.error('Failed to fetch auth keys:', e);
                    this.authKeys = [];
                    this.authKeyError = 'Unable to load API keys.';
                } finally {
                    this.authKeysLoading = false;
                }
            },

            openAuthKeyForm() {
                if (this.authKeyFormSubmitting || this.authKeyFormOpen) {
                    return;
                }
                this.authKeyFormOpen = true;
                this.authKeyError = '';
                this.authKeyNotice = '';
                if (!this.authKeyIssuedValue) {
                    this.authKeyCopyState.resetFeedback();
                    this.authKeyForm = this.defaultAuthKeyForm();
                    if (typeof this.$nextTick === 'function') {
                        this.$nextTick(() => {
                            const refs = this.$refs || {};
                            const input = refs.authKeyNameInput || null;
                            if (input && typeof input.focus === 'function') {
                                input.focus({ preventScroll: true });
                            }
                        });
                    }
                }
            },

            closeAuthKeyForm() {
                if (!this.authKeyFormOpen) {
                    return;
                }
                this.authKeyFormOpen = false;
                this.authKeyError = '';
                this.authKeyCopyState.resetFeedback();
                if (!this.authKeyFormSubmitting && !this.authKeyIssuedValue) {
                    this.authKeyForm = this.defaultAuthKeyForm();
                }
            },

            copyAuthKeyValue() {
                return this.authKeyCopyState.copy(this.authKeyIssuedValue);
            },

            dismissIssuedKey() {
                this.authKeyIssuedValue = '';
                this.authKeyCopyState.resetFeedback();
                this.authKeyForm = this.defaultAuthKeyForm();
            },

            async _authKeyResponseMessage(res, fallback) {
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

            async submitAuthKeyForm() {
                const name = String(this.authKeyForm.name || '').trim();
                if (!name) {
                    this.authKeyError = 'Name is required.';
                    return;
                }
                const userPathError = this.authKeyUserPathValidationError(this.authKeyForm.user_path);
                if (userPathError) {
                    this.authKeyError = userPathError;
                    return;
                }

                this.authKeyError = '';
                this.authKeyNotice = '';
                this.authKeyFormSubmitting = true;
                const userPath = this.normalizeAuthKeyUserPath(this.authKeyForm.user_path);

                const payload = {
                    name,
                    description: String(this.authKeyForm.description || '').trim() || undefined,
                    user_path: userPath || undefined
                };
                if (this.authKeyForm.expires_at) {
                    payload.expires_at = this.authKeyForm.expires_at + 'T23:59:59Z';
                }

                try {
                    const request = typeof this.requestOptions === 'function'
                        ? this.requestOptions({
                            method: 'POST',
                            body: JSON.stringify(payload)
                        })
                        : {
                            method: 'POST',
                            headers: this.headers(),
                            body: JSON.stringify(payload)
                        };
                    const res = await fetch('/admin/api/v1/auth-keys', request);
                    if (res.status === 503) {
                        this.authKeysAvailable = false;
                        this.authKeyError = 'Auth keys feature is unavailable.';
                        return;
                    }
                    if (typeof this.handleFetchResponse === 'function') {
                        const handled = this.handleFetchResponse(res, 'create API key', request);
                        if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                            return;
                        }
                        if (!handled) {
                            if (res.status === 401) {
                                this.authKeyError = 'Authentication required.';
                                return;
                            }
                            this.authKeyError = await this._authKeyResponseMessage(res, 'Failed to create API key.');
                            console.error('Failed to create API key:', res.status, res.statusText, this.authKeyError);
                            return;
                        }
                    } else if (res.status === 401) {
                        this.authError = true;
                        this.needsAuth = true;
                        this.authKeyError = 'Authentication required.';
                        return;
                    } else if (res.status !== 201) {
                        this.authKeyError = await this._authKeyResponseMessage(res, 'Failed to create API key.');
                        console.error('Failed to create API key:', res.status, res.statusText, this.authKeyError);
                        return;
                    }
                    const issued = await res.json();
                    this.authKeyIssuedValue = issued.value || '';
                    this.authKeyFormOpen = true;
                    this.authKeyCopyState.resetFeedback();
                    this.authKeyForm = this.defaultAuthKeyForm();
                    await this.fetchAuthKeys();
                } catch (e) {
                    console.error('Failed to issue auth key:', e);
                    this.authKeyError = 'Failed to create API key.';
                } finally {
                    this.authKeyFormSubmitting = false;
                }
            },

            async deactivateAuthKey(key) {
                if (!key || !key.active) {
                    return;
                }
                if (!window.confirm('Deactivate key "' + key.name + '"? This cannot be undone.')) {
                    return;
                }

                this.authKeyDeactivatingID = key.id;
                this.authKeyError = '';
                this.authKeyNotice = '';

                try {
                    const request = typeof this.requestOptions === 'function'
                        ? this.requestOptions({
                            method: 'POST'
                        })
                        : {
                            method: 'POST',
                            headers: this.headers()
                        };
                    const res = await fetch('/admin/api/v1/auth-keys/' + encodeURIComponent(key.id) + '/deactivate', request);
                    if (res.status === 503) {
                        this.authKeysAvailable = false;
                        this.authKeyError = 'Auth keys feature is unavailable.';
                        return;
                    }
                    if (typeof this.handleFetchResponse === 'function') {
                        const handled = this.handleFetchResponse(res, 'deactivate API key', request);
                        if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                            return;
                        }
                        if (!handled) {
                            if (res.status === 401) {
                                this.authKeyError = 'Authentication required.';
                                return;
                            }
                            this.authKeyError = await this._authKeyResponseMessage(res, 'Failed to deactivate key.');
                            console.error('Failed to deactivate auth key:', res.status, res.statusText, this.authKeyError);
                            return;
                        }
                    } else if (res.status === 401) {
                        this.authError = true;
                        this.needsAuth = true;
                        this.authKeyError = 'Authentication required.';
                        return;
                    } else if (res.status !== 204) {
                        this.authKeyError = await this._authKeyResponseMessage(res, 'Failed to deactivate key.');
                        console.error('Failed to deactivate auth key:', res.status, res.statusText, this.authKeyError);
                        return;
                    }
                    await this.fetchAuthKeys();
                    this.authKeyNotice = 'Key "' + key.name + '" deactivated.';
                } catch (e) {
                    console.error('Failed to deactivate auth key:', e);
                    this.authKeyError = 'Failed to deactivate key.';
                } finally {
                    this.authKeyDeactivatingID = '';
                }
            }
        };
    }

    global.dashboardAuthKeysModule = dashboardAuthKeysModule;
})(window);
