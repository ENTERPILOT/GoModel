(function(global) {
    function dashboardAuthKeysModule() {
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
            authKeyCopied: false,
            authKeyCopyError: false,
            authKeyCopyResetTimer: null,
            authKeyForm: {
                name: '',
                description: '',
                expires_at: ''
            },

            defaultAuthKeyForm() {
                return { name: '', description: '', expires_at: '' };
            },

            async fetchAuthKeys() {
                this.authKeysLoading = true;
                this.authKeyError = '';
                try {
                    const res = await fetch('/admin/api/v1/auth-keys', { headers: this.headers() });
                    if (res.status === 503) {
                        this.authKeysAvailable = false;
                        this.authKeys = [];
                        return;
                    }
                    this.authKeysAvailable = true;
                    if (!this.handleFetchResponse(res, 'auth keys')) {
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
                    this.resetAuthKeyCopyFeedback();
                    this.authKeyForm = this.defaultAuthKeyForm();
                }
            },

            closeAuthKeyForm() {
                if (!this.authKeyFormOpen) {
                    return;
                }
                this.authKeyFormOpen = false;
                this.authKeyError = '';
                this.resetAuthKeyCopyFeedback();
                if (!this.authKeyFormSubmitting && !this.authKeyIssuedValue) {
                    this.authKeyForm = this.defaultAuthKeyForm();
                }
            },

            clearAuthKeyCopyResetTimer() {
                if (this.authKeyCopyResetTimer !== null) {
                    clearTimeout(this.authKeyCopyResetTimer);
                    this.authKeyCopyResetTimer = null;
                }
            },

            scheduleAuthKeyCopyFeedbackReset() {
                this.clearAuthKeyCopyResetTimer();
                this.authKeyCopyResetTimer = setTimeout(() => {
                    this.authKeyCopied = false;
                    this.authKeyCopyError = false;
                    this.authKeyCopyResetTimer = null;
                }, 2000);
            },

            resetAuthKeyCopyFeedback() {
                this.clearAuthKeyCopyResetTimer();
                this.authKeyCopied = false;
                this.authKeyCopyError = false;
            },

            setAuthKeyCopyFeedback(copied, hasError) {
                this.authKeyCopied = copied;
                this.authKeyCopyError = hasError;
                this.scheduleAuthKeyCopyFeedbackReset();
            },

            copyAuthKeyValue() {
                const value = String(this.authKeyIssuedValue || '');
                const clipboard = global.navigator && global.navigator.clipboard;

                this.resetAuthKeyCopyFeedback();

                if (clipboard && typeof clipboard.writeText === 'function') {
                    return clipboard.writeText(value).then(() => {
                        this.setAuthKeyCopyFeedback(true, false);
                    }).catch((error) => {
                        console.error('Failed to copy auth key:', error);
                        this.setAuthKeyCopyFeedback(false, true);
                    });
                }

                const doc = global.document;
                if (!doc || !doc.body || typeof doc.createElement !== 'function' || typeof doc.execCommand !== 'function') {
                    this.setAuthKeyCopyFeedback(false, true);
                    return Promise.resolve();
                }

                const textarea = doc.createElement('textarea');
                textarea.value = value;
                textarea.setAttribute('readonly', '');
                textarea.style.position = 'fixed';
                textarea.style.top = '0';
                textarea.style.left = '0';
                textarea.style.opacity = '0';

                try {
                    doc.body.appendChild(textarea);
                    if (typeof textarea.focus === 'function') {
                        textarea.focus();
                    }
                    if (typeof textarea.select === 'function') {
                        textarea.select();
                    }
                    if (typeof textarea.setSelectionRange === 'function') {
                        textarea.setSelectionRange(0, textarea.value.length);
                    }
                    const copied = !!doc.execCommand('copy');
                    this.setAuthKeyCopyFeedback(copied, !copied);
                } catch (error) {
                    console.error('Failed to copy auth key:', error);
                    this.setAuthKeyCopyFeedback(false, true);
                } finally {
                    if (textarea.parentNode) {
                        textarea.parentNode.removeChild(textarea);
                    }
                }

                return Promise.resolve();
            },

            dismissIssuedKey() {
                this.authKeyIssuedValue = '';
                this.resetAuthKeyCopyFeedback();
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

                this.authKeyError = '';
                this.authKeyNotice = '';
                this.authKeyFormSubmitting = true;

                const payload = {
                    name,
                    description: String(this.authKeyForm.description || '').trim() || undefined
                };
                if (this.authKeyForm.expires_at) {
                    payload.expires_at = this.authKeyForm.expires_at + 'T23:59:59Z';
                }

                try {
                    const res = await fetch('/admin/api/v1/auth-keys', {
                        method: 'POST',
                        headers: this.headers(),
                        body: JSON.stringify(payload)
                    });
                    if (res.status === 503) {
                        this.authKeysAvailable = false;
                        this.authKeyError = 'Auth keys feature is unavailable.';
                        return;
                    }
                    if (res.status === 401) {
                        this.authError = true;
                        this.needsAuth = true;
                        this.authKeyError = 'Authentication required.';
                        return;
                    }
                    if (res.status !== 201) {
                        this.authKeyError = await this._authKeyResponseMessage(res, 'Failed to create API key.');
                        return;
                    }
                    const issued = await res.json();
                    this.authKeyIssuedValue = issued.value || '';
                    this.authKeyFormOpen = true;
                    this.resetAuthKeyCopyFeedback();
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
                    const res = await fetch('/admin/api/v1/auth-keys/' + encodeURIComponent(key.id) + '/deactivate', {
                        method: 'POST',
                        headers: this.headers()
                    });
                    if (res.status === 503) {
                        this.authKeysAvailable = false;
                        this.authKeyError = 'Auth keys feature is unavailable.';
                        return;
                    }
                    if (res.status === 401) {
                        this.authError = true;
                        this.needsAuth = true;
                        this.authKeyError = 'Authentication required.';
                        return;
                    }
                    if (res.status !== 204) {
                        this.authKeyError = await this._authKeyResponseMessage(res, 'Failed to deactivate key.');
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
