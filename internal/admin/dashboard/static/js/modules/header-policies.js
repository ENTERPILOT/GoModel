(function(global) {
    function dashboardHeaderPoliciesModule() {
        return {
            headerPolicies: [],
            headerPoliciesAvailable: true,
            headerPoliciesLoading: false,
            headerPolicyError: '',
            headerPolicyNotice: '',
            headerPolicyFilter: '',
            headerPolicyFormOpen: false,
            headerPolicyFormSubmitting: false,
            headerPolicyDeletingName: '',
            headerPolicyFormMode: 'create',
            headerPolicyFormOriginalName: '',
            headerPolicyForm: {
                name: '', description: '', methods: [], paths: '', when: [],
                actions: [{ action: 'set', header: '', value: '' }]
            },

            defaultHeaderPolicyForm() {
                return { name: '', description: '', methods: [], paths: '', when: [], actions: [{ action: 'set', header: '', value: '' }] };
            },

            get filteredHeaderPolicies() {
                const filter = String(this.headerPolicyFilter || '').trim().toLowerCase();
                if (!filter) return this.headerPolicies;
                return (this.headerPolicies || []).filter((policy) => [policy.name, policy.description, policy.summary]
                    .some((value) => String(value || '').toLowerCase().includes(filter)));
            },

            headerPoliciesRuntimeEnabled() {
                return typeof this.workflowRuntimeBooleanFlag === 'function'
                    ? this.workflowRuntimeBooleanFlag('HEADER_POLICIES_ENABLED', true)
                    : true;
            },

            headerPolicyMethodSelected(method) {
                return (this.headerPolicyForm.methods || []).includes(method);
            },

            toggleHeaderPolicyMethod(method, checked) {
                const current = Array.isArray(this.headerPolicyForm.methods) ? this.headerPolicyForm.methods : [];
                this.headerPolicyForm.methods = checked
                    ? Array.from(new Set([...current, method]))
                    : current.filter((item) => item !== method);
            },

            addHeaderPolicyCondition() {
                this.headerPolicyForm.when.push({ header: '' });
            },

            removeHeaderPolicyCondition(index) {
                this.headerPolicyForm.when.splice(index, 1);
            },

            headerPolicyConditionMode(row) {
                if (row && row.matches !== undefined) return 'matches';
                if (row && row.equals !== undefined) return 'equals';
                if (row && row.present === false) return 'absent';
                return 'present';
            },

            setHeaderPolicyConditionMode(index, mode) {
                const current = this.headerPolicyForm.when[index] || {};
                const next = { header: current.header || '' };
                if (mode === 'matches') next.matches = '';
                if (mode === 'equals') next.equals = '';
                if (mode === 'absent') next.present = false;
                this.headerPolicyForm.when.splice(index, 1, next);
            },

            setHeaderPolicyConditionValue(index, value) {
                const row = this.headerPolicyForm.when[index];
                const mode = this.headerPolicyConditionMode(row);
                if (mode === 'equals' || mode === 'matches') row[mode] = value;
            },

            addHeaderPolicyAction() {
                this.headerPolicyForm.actions.push({ action: 'set', header: '', value: '' });
            },

            removeHeaderPolicyAction(index) {
                this.headerPolicyForm.actions.splice(index, 1);
            },

            headerPolicyActionMode(row) {
                if (row && row.action === 'remove') return 'remove';
                if (row && row.from_header !== undefined) return 'copy';
                return 'set';
            },

            setHeaderPolicyActionMode(index, mode) {
                const header = (this.headerPolicyForm.actions[index] || {}).header || '';
                const next = mode === 'remove'
                    ? { action: 'remove', header }
                    : mode === 'copy'
                        ? { action: 'set', header, from_header: '' }
                        : { action: 'set', header, value: '' };
                this.headerPolicyForm.actions.splice(index, 1, next);
            },

            setHeaderPolicyActionValue(index, value) {
                const row = this.headerPolicyForm.actions[index];
                if (this.headerPolicyActionMode(row) === 'copy') row.from_header = value;
                else row.value = value;
            },

            openHeaderPolicyCreate() {
                this.headerPolicyFormMode = 'create';
                this.headerPolicyFormOriginalName = '';
                this.headerPolicyError = '';
                this.headerPolicyNotice = '';
                this.headerPolicyForm = this.defaultHeaderPolicyForm();
                this.headerPolicyFormOpen = true;
            },

            openHeaderPolicyEdit(policy) {
                this.headerPolicyFormMode = 'edit';
                this.headerPolicyFormOriginalName = String(policy && policy.name || '').trim();
                this.headerPolicyError = '';
                this.headerPolicyNotice = '';
                this.headerPolicyForm = {
                    name: this.headerPolicyFormOriginalName,
                    description: String(policy && policy.description || '').trim(),
                    methods: Array.isArray(policy && policy.methods) ? [...policy.methods] : [],
                    paths: Array.isArray(policy && policy.paths) ? policy.paths.join(', ') : '',
                    when: Array.isArray(policy && policy.when) ? JSON.parse(JSON.stringify(policy.when)) : [],
                    actions: Array.isArray(policy && policy.actions) ? JSON.parse(JSON.stringify(policy.actions)) : []
                };
                this.headerPolicyFormOpen = true;
            },

            closeHeaderPolicyForm() {
                this.headerPolicyFormOpen = false;
                this.headerPolicyFormMode = 'create';
                this.headerPolicyFormOriginalName = '';
                this.headerPolicyError = '';
                this.headerPolicyForm = this.defaultHeaderPolicyForm();
            },

            headerPolicyPayload() {
                const paths = String(this.headerPolicyForm.paths || '').split(',').map((item) => item.trim()).filter(Boolean);
                const when = (this.headerPolicyForm.when || [])
                    .filter((row) => String(row && row.header || '').trim())
                    .map((row) => {
                        const next = { header: String(row.header).trim() };
                        const mode = this.headerPolicyConditionMode(row);
                        if (mode === 'absent') next.present = false;
                        if (mode === 'equals' || mode === 'matches') next[mode] = String(row[mode] ?? '');
                        return next;
                    });
                const actions = (this.headerPolicyForm.actions || [])
                    .filter((row) => String(row && row.header || '').trim())
                    .map((row) => {
                        const mode = this.headerPolicyActionMode(row);
                        const next = { action: mode === 'remove' ? 'remove' : 'set', header: String(row.header).trim() };
                        if (mode === 'copy') next.from_header = String(row.from_header || '').trim();
                        if (mode === 'set') next.value = String(row.value ?? '');
                        return next;
                    });
                return {
                    name: String(this.headerPolicyForm.name || '').trim(),
                    description: String(this.headerPolicyForm.description || '').trim() || undefined,
                    methods: (this.headerPolicyForm.methods || []).length ? this.headerPolicyForm.methods : undefined,
                    paths: paths.length ? paths : undefined,
                    when: when.length ? when : undefined,
                    actions
                };
            },

            async headerPolicyResponseMessage(res, fallback) {
                try {
                    const payload = await res.json();
                    if (payload && payload.error && payload.error.message) return payload.error.message;
                } catch (_) {
                    // Ignore invalid or empty responses and return the fallback.
                }
                return fallback;
            },

            async fetchHeaderPolicies() {
                this.headerPoliciesLoading = true;
                this.headerPolicyError = '';
                try {
                    const request = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    const res = await fetch('/admin/header-policies', request);
                    if (res.status === 503) {
                        this.headerPoliciesAvailable = false;
                        this.headerPolicies = [];
                        return;
                    }
                    const handled = this.handleFetchResponse(res, 'header policies', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) return;
                    this.headerPoliciesAvailable = true;
                    if (!handled) {
                        this.headerPolicies = [];
                        return;
                    }
                    const payload = await res.json();
                    this.headerPolicies = Array.isArray(payload) ? payload : [];
                } catch (e) {
                    console.error('Failed to fetch header policies:', e);
                    this.headerPolicies = [];
                    this.headerPolicyError = 'Unable to load header policies.';
                } finally {
                    this.headerPoliciesLoading = false;
                }
            },

            async submitHeaderPolicyForm() {
                const payload = this.headerPolicyPayload();
                if (!payload.name) {
                    this.headerPolicyError = 'Name is required.';
                    return;
                }
                if (!payload.actions.length) {
                    this.headerPolicyError = 'At least one complete action is required.';
                    return;
                }
                this.headerPolicyFormSubmitting = true;
                this.headerPolicyError = '';
                this.headerPolicyNotice = '';
                try {
                    const request = this.requestOptions({ method: 'PUT', body: JSON.stringify(payload) });
                    const res = await fetch('/admin/header-policies', request);
                    const handled = this.handleFetchResponse(res, 'save header policy', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) return;
                    if (!handled) {
                        this.headerPolicyError = await this.headerPolicyResponseMessage(res, 'Failed to save header policy.');
                        return;
                    }
                    await this.fetchHeaderPolicies();
                    if (typeof this.fetchWorkflowHeaderPolicies === 'function') this.fetchWorkflowHeaderPolicies();
                    this.headerPolicyNotice = 'Header policy "' + payload.name + '" saved.';
                    this.closeHeaderPolicyForm();
                } catch (e) {
                    console.error('Failed to save header policy:', e);
                    this.headerPolicyError = 'Failed to save header policy.';
                } finally {
                    this.headerPolicyFormSubmitting = false;
                }
            },

            async deleteHeaderPolicy(policy) {
                const name = String(policy && policy.name || '').trim();
                if (!name || this.headerPolicyDeletingName) return;
                if (!window.confirm('Delete header policy "' + name + '"? Workflows that reference it must be updated first.')) return;
                this.headerPolicyDeletingName = name;
                this.headerPolicyError = '';
                try {
                    const request = this.requestOptions({ method: 'DELETE', body: JSON.stringify({ name }) });
                    const res = await fetch('/admin/header-policies', request);
                    const handled = this.handleFetchResponse(res, 'delete header policy', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) return;
                    if (!handled) {
                        this.headerPolicyError = await this.headerPolicyResponseMessage(res, 'Failed to delete header policy.');
                        return;
                    }
                    await this.fetchHeaderPolicies();
                    if (typeof this.fetchWorkflowHeaderPolicies === 'function') this.fetchWorkflowHeaderPolicies();
                    this.headerPolicyNotice = 'Header policy "' + name + '" deleted.';
                } catch (e) {
                    console.error('Failed to delete header policy:', e);
                    this.headerPolicyError = 'Failed to delete header policy.';
                } finally {
                    this.headerPolicyDeletingName = '';
                }
            }
        };
    }

    global.dashboardHeaderPoliciesModule = dashboardHeaderPoliciesModule;
})(window);
