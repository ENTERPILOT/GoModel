(function(global) {
    function dashboardTaggingModule() {
        return {
            taggingHeaders: [],
            taggingEditable: true,
            taggingLoading: false,
            taggingSaving: false,
            taggingNotice: '',
            taggingError: '',

            defaultTaggingHeader() {
                return {
                    header: '',
                    prefix: '',
                    do_not_pass: false,
                    delimiter: '',
                    managed: false
                };
            },

            normalizeTaggingHeaders(payload) {
                const headers = payload && Array.isArray(payload.headers) ? payload.headers : [];
                return headers.map((rule) => ({
                    header: typeof rule.header === 'string' ? rule.header : '',
                    prefix: typeof rule.prefix === 'string' ? rule.prefix : '',
                    do_not_pass: rule.do_not_pass === true,
                    delimiter: typeof rule.delimiter === 'string' && rule.delimiter !== ',' ? rule.delimiter : '',
                    managed: rule.managed === true
                }));
            },

            taggingSettingsPayload() {
                return {
                    headers: this.taggingHeaders
                        .filter((rule) => !rule.managed && rule.header.trim() !== '')
                        .map((rule) => ({
                            header: rule.header.trim(),
                            prefix: rule.prefix,
                            do_not_pass: rule.do_not_pass,
                            delimiter: rule.delimiter
                        }))
                };
            },

            addTaggingHeader() {
                this.taggingHeaders.push(this.defaultTaggingHeader());
            },

            removeTaggingHeader(index) {
                const rule = this.taggingHeaders[index];
                if (!rule || rule.managed) {
                    return;
                }
                this.taggingHeaders.splice(index, 1);
            },

            async fetchTaggingSettings() {
                this.taggingLoading = true;
                this.taggingError = '';
                try {
                    const request = this.requestOptions();
                    const res = await fetch('/admin/tagging/settings', request);
                    const handled = this.handleFetchResponse(res, 'tagging settings', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.taggingError = 'Unable to load tagging settings.';
                        return;
                    }
                    const payload = await res.json();
                    this.taggingHeaders = this.normalizeTaggingHeaders(payload);
                    this.taggingEditable = payload && payload.editable !== false;
                } catch (e) {
                    console.error('Failed to fetch tagging settings:', e);
                    this.taggingError = 'Unable to load tagging settings.';
                } finally {
                    this.taggingLoading = false;
                }
            },

            async saveTaggingSettings() {
                if (this.taggingSaving || !this.taggingEditable) {
                    return;
                }
                this.taggingSaving = true;
                this.taggingNotice = '';
                this.taggingError = '';
                try {
                    const request = this.requestOptions({
                        method: 'PUT',
                        body: JSON.stringify(this.taggingSettingsPayload())
                    });
                    const res = await fetch('/admin/tagging/settings', request);
                    if (res.status === 401) {
                        const handled = this.handleFetchResponse(res, 'tagging settings', request);
                        if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                            return;
                        }
                        this.taggingError = 'Unable to save tagging settings.';
                        return;
                    }
                    if (!res.ok) {
                        const message = await this.taggingErrorMessage(res);
                        this.taggingError = message || 'Unable to save tagging settings.';
                        return;
                    }
                    const payload = await res.json();
                    this.taggingHeaders = this.normalizeTaggingHeaders(payload);
                    this.taggingEditable = payload && payload.editable !== false;
                    this.taggingNotice = 'Tagging settings saved.';
                } catch (e) {
                    console.error('Failed to save tagging settings:', e);
                    this.taggingError = 'Unable to save tagging settings.';
                } finally {
                    this.taggingSaving = false;
                }
            },

            async taggingErrorMessage(res) {
                try {
                    const payload = await res.json();
                    if (payload && payload.error && payload.error.message) {
                        return payload.error.message;
                    }
                } catch (e) {
                    // Ignore parse failures; the generic message is used instead.
                }
                return '';
            }
        };
    }

    global.dashboardTaggingModule = dashboardTaggingModule;
})(typeof window !== 'undefined' ? window : globalThis);
