(function(global) {
    function getHelpers() {
        return global.DashboardConversationHelpers || {};
    }

    function dashboardConversationDrawerModule() {
        return {
            canShowConversation(entry) {
                const h = getHelpers();
                return typeof h.canShowConversation === 'function' ? h.canShowConversation(entry) : false;
            },

            startBodyInteraction(event) {
                this.bodyPointerStart = {
                    x: event.clientX,
                    y: event.clientY
                };
            },

            _isBodyDrag(event) {
                if (!this.bodyPointerStart) return false;
                const dx = Math.abs(event.clientX - this.bodyPointerStart.x);
                const dy = Math.abs(event.clientY - this.bodyPointerStart.y);
                return dx > 4 || dy > 4;
            },

            _hasActiveSelection() {
                const selection = window.getSelection ? window.getSelection() : null;
                if (!selection) return false;
                if (selection.isCollapsed) return false;
                return String(selection.toString() || '').trim().length > 0;
            },

            handleBodyConversationClick(event, entry) {
                const wasDrag = this._isBodyDrag(event);
                this.bodyPointerStart = null;
                if (wasDrag) return;
                if (this._hasActiveSelection()) return;
                if (!this.canShowConversation(entry)) return;
                const el = event.target && event.target.closest ? event.target.closest('[data-conversation-trigger="1"]') : null;
                if (!el) return;
                event.preventDefault();
                event.stopPropagation();
                this.openConversation(entry, null, false, el);
            },

            renderBodyWithConversationHighlights(entry, value) {
                const h = getHelpers();
                if (typeof h.renderBodyWithConversationHighlights !== 'function') {
                    return this.formatJSON(value);
                }
                return h.renderBodyWithConversationHighlights(entry, value, {
                    formatJSON: (v) => this.formatJSON(v),
                    canShowConversation: (e) => this.canShowConversation(e)
                });
            },

            async openConversation(entry, detailsEl, expandEntry, triggerEl) {
                if (!entry || !entry.id || !this.canShowConversation(entry)) return;
                if (expandEntry && detailsEl && !detailsEl.open) {
                    detailsEl.open = true;
                }

                const activeEl = document.activeElement instanceof HTMLElement ? document.activeElement : null;
                if (triggerEl instanceof HTMLElement) {
                    this.conversationReturnFocusEl = triggerEl;
                } else if (activeEl && activeEl !== document.body) {
                    this.conversationReturnFocusEl = activeEl;
                }

                const requestToken = ++this.conversationRequestToken;
                this.conversationOpen = true;
                this.conversationLoading = true;
                this.conversationError = '';
                this.conversationAnchorID = entry.id;
                this.conversationEntries = [];
                this.conversationMessages = [];
                document.body.classList.add('conversation-drawer-open');
                requestAnimationFrame(() => this._focusConversationDrawer());
                await this.fetchConversation(entry.id, requestToken);
            },

            closeConversation() {
                this.conversationOpen = false;
                this.conversationRequestToken++;
                document.body.classList.remove('conversation-drawer-open');
                const returnFocusEl = this.conversationReturnFocusEl;
                this.conversationReturnFocusEl = null;
                if (returnFocusEl && typeof returnFocusEl.focus === 'function' && document.contains(returnFocusEl)) {
                    requestAnimationFrame(() => returnFocusEl.focus());
                }
            },

            _focusConversationDrawer() {
                if (!this.conversationOpen) return;
                const closeBtn = this.$refs && this.$refs.conversationCloseBtn;
                if (closeBtn && typeof closeBtn.focus === 'function') {
                    closeBtn.focus();
                    return;
                }
                const drawer = this.$refs && this.$refs.conversationDialog;
                if (drawer && typeof drawer.focus === 'function') {
                    drawer.focus();
                }
            },

            async fetchConversation(logID, requestToken) {
                try {
                    const qs = 'log_id=' + encodeURIComponent(logID) + '&limit=120';
                    const request = typeof this.requestOptions === 'function' ? this.requestOptions() : { headers: this.headers() };
                    const res = await fetch('/admin/api/v1/audit/conversation?' + qs, request);

                    if (requestToken !== this.conversationRequestToken) return;

                    const handled = this.handleFetchResponse(res, 'audit conversation', request);
                    if (typeof this.isStaleAuthFetchResult === 'function' && this.isStaleAuthFetchResult(handled)) {
                        return;
                    }
                    if (!handled) {
                        this.conversationError = 'Unable to load interactions.';
                        this.conversationEntries = [];
                        this.conversationMessages = [];
                        return;
                    }

                    const result = await res.json();
                    if (requestToken !== this.conversationRequestToken) return;

                    this.conversationAnchorID = result.anchor_id || logID;
                    this.conversationEntries = Array.isArray(result.entries) ? result.entries : [];
                    this.conversationMessages = this.buildConversationMessages(this.conversationEntries, this.conversationAnchorID);
                } catch (e) {
                    if (requestToken !== this.conversationRequestToken) return;
                    console.error('Failed to fetch audit conversation:', e);
                    this.conversationError = 'Failed to load interactions.';
                    this.conversationEntries = [];
                    this.conversationMessages = [];
                } finally {
                    if (requestToken === this.conversationRequestToken) {
                        this.conversationLoading = false;
                    }
                }
            },

            buildConversationMessages(entries, anchorID) {
                if (!Array.isArray(entries) || entries.length === 0) return [];

                const h = getHelpers();
                const extractText = typeof h.extractText === 'function' ? h.extractText : () => '';
                const extractResponsesInputMessages = typeof h.extractResponsesInputMessages === 'function' ? h.extractResponsesInputMessages : () => [];
                const extractResponsesOutputText = typeof h.extractResponsesOutputText === 'function' ? h.extractResponsesOutputText : () => '';
                const extractConversationErrorMessage = typeof h.extractConversationErrorMessage === 'function' ? h.extractConversationErrorMessage : () => '';

                const sorted = [...entries].sort((a, b) => new Date(a.timestamp) - new Date(b.timestamp));

                const callIdMap = {};
                sorted.forEach((entry) => {
                    const rb = entry.data && entry.data.request_body ? entry.data.request_body : null;
                    const rsb = entry.data && entry.data.response_body ? entry.data.response_body : null;
                    this._collectCallIds(callIdMap, rb, rsb);
                });

                const messages = [];
                let idx = 0;

                sorted.forEach((entry) => {
                    const isAnchor = entry.id === anchorID;
                    const ts = entry.timestamp;
                    const requestBody = entry.data && entry.data.request_body ? entry.data.request_body : null;
                    const responseBody = entry.data && entry.data.response_body ? entry.data.response_body : null;

                    if (requestBody && typeof requestBody.instructions === 'string' && requestBody.instructions.trim()) {
                        messages.push(this._conversationMessage('system', requestBody.instructions, ts, entry.id, isAnchor, ++idx));
                    }

                    if (requestBody && Array.isArray(requestBody.messages)) {
                        requestBody.messages.forEach((m) => {
                            if (!m) return;
                            const role = (m.role || 'user').toLowerCase();
                            if (role === 'tool') {
                                const text = extractText(m.content);
                                const fnName = m.name || callIdMap[m.tool_call_id] || '';
                                if (text) messages.push(this._conversationMessage('function_result', text, ts, entry.id, isAnchor, ++idx, [], fnName));
                                return;
                            }
                            if (role === 'assistant') {
                                const text = extractText(m.content);
                                const toolCalls = this._extractToolCalls(m.tool_calls);
                                if (text || toolCalls.length > 0) {
                                    messages.push(this._conversationMessage(role, text, ts, entry.id, isAnchor, ++idx, toolCalls));
                                }
                                return;
                            }
                            const text = extractText(m.content);
                            if (text) messages.push(this._conversationMessage(role, text, ts, entry.id, isAnchor, ++idx));
                        });
                    }

                    if (requestBody && requestBody.input !== undefined) {
                        if (Array.isArray(requestBody.input)) {
                            requestBody.input.forEach((item) => {
                                if (!item || typeof item !== 'object') return;
                                if (item.type === 'function_call_output') {
                                    const text = typeof item.output === 'string' ? item.output : extractText(item.output);
                                    if (text) messages.push(this._conversationMessage('function_result', text, ts, entry.id, isAnchor, ++idx, [], callIdMap[item.call_id] || ''));
                                } else if (item.type === 'function_call') {
                                    messages.push(this._conversationMessage('function_call', '', ts, entry.id, isAnchor, ++idx, [{name: item.name || '', arguments: item.arguments || ''}]));
                                } else if (item.role) {
                                    const role = String(item.role).toLowerCase();
                                    const text = extractText(item.content);
                                    if (text) messages.push(this._conversationMessage(role, text, ts, entry.id, isAnchor, ++idx));
                                }
                            });
                        } else {
                            extractResponsesInputMessages(requestBody.input).forEach((m) => {
                                if (m.text) messages.push(this._conversationMessage(m.role, m.text, ts, entry.id, isAnchor, ++idx));
                            });
                        }
                    }

                    if (responseBody && Array.isArray(responseBody.choices)) {
                        const first = responseBody.choices[0];
                        if (first && first.message) {
                            const role = (first.message.role || 'assistant').toLowerCase();
                            const text = extractText(first.message.content);
                            const toolCalls = this._extractToolCalls(first.message.tool_calls);
                            if (text || toolCalls.length > 0) {
                                messages.push(this._conversationMessage(role, text, ts, entry.id, isAnchor, ++idx, toolCalls));
                            }
                        }
                    }

                    if (responseBody && Array.isArray(responseBody.output)) {
                        responseBody.output.forEach((item) => {
                            if (!item) return;
                            if (item.type === 'function_call') {
                                messages.push(this._conversationMessage('function_call', '', ts, entry.id, isAnchor, ++idx, [{name: item.name || '', arguments: item.arguments || ''}]));
                                return;
                            }
                            const role = (item.role || 'assistant').toLowerCase();
                            const text = extractResponsesOutputText(item);
                            if (text) messages.push(this._conversationMessage(role, text, ts, entry.id, isAnchor, ++idx));
                        });
                    }

                    const errMsg = extractConversationErrorMessage(entry);
                    if (errMsg) {
                        messages.push(this._conversationMessage('error', errMsg, ts, entry.id, isAnchor, ++idx));
                    }
                });

                return messages;
            },

            _collectCallIds(map, requestBody, responseBody) {
                if (requestBody && Array.isArray(requestBody.messages)) {
                    requestBody.messages.forEach((m) => {
                        if (!m || !Array.isArray(m.tool_calls)) return;
                        m.tool_calls.forEach((tc) => {
                            if (!tc) return;
                            const id = tc.id || '';
                            const fn = tc.function || tc;
                            const name = fn.name || tc.name || '';
                            if (id && name) map[id] = name;
                        });
                    });
                }
                if (requestBody && Array.isArray(requestBody.input)) {
                    requestBody.input.forEach((item) => {
                        if (!item || typeof item !== 'object' || item.type !== 'function_call') return;
                        const id = item.id || item.call_id || '';
                        const name = item.name || '';
                        if (id && name) map[id] = name;
                    });
                }
                if (responseBody && Array.isArray(responseBody.choices)) {
                    const first = responseBody.choices[0];
                    if (first && first.message && Array.isArray(first.message.tool_calls)) {
                        first.message.tool_calls.forEach((tc) => {
                            if (!tc) return;
                            const id = tc.id || '';
                            const fn = tc.function || tc;
                            const name = fn.name || tc.name || '';
                            if (id && name) map[id] = name;
                        });
                    }
                }
                if (responseBody && Array.isArray(responseBody.output)) {
                    responseBody.output.forEach((item) => {
                        if (!item || item.type !== 'function_call') return;
                        const id = item.id || item.call_id || '';
                        const name = item.name || '';
                        if (id && name) map[id] = name;
                    });
                }
            },

            _extractToolCalls(toolCalls) {
                if (!Array.isArray(toolCalls)) return [];
                return toolCalls.map((tc) => {
                    if (!tc) return null;
                    const fn = tc.function || tc;
                    return {
                        name: fn.name || tc.name || '',
                        arguments: fn.arguments || tc.arguments || ''
                    };
                }).filter(Boolean);
            },

            _conversationMessage(role, text, timestamp, entryID, isAnchor, idx, toolCalls, functionName) {
                const normalized = this._roleMeta(role);
                return {
                    uid: entryID + '-' + idx,
                    entryID,
                    timestamp,
                    text,
                    role: normalized.role,
                    roleLabel: normalized.label,
                    roleClass: normalized.className,
                    isAnchor,
                    toolCalls: Array.isArray(toolCalls) && toolCalls.length > 0 ? toolCalls : null,
                    functionName: functionName || ''
                };
            },

            functionExpandedContent(msg) {
                if (msg.role === 'function_call') {
                    return (msg.toolCalls || []).map(function(tc) {
                        var args = tc.arguments || '';
                        try { args = JSON.stringify(JSON.parse(args), null, 2); } catch(e) {}
                        return tc.name + '(' + args + ')';
                    }).join('\n\n');
                }
                return msg.text || '';
            },

            _roleMeta(role) {
                const normalized = String(role || '').toLowerCase();
                if (normalized === 'system' || normalized === 'developer') {
                    return { role: 'system', label: 'System Prompt', className: 'role-system' };
                }
                if (normalized === 'assistant') {
                    return { role: 'assistant', label: 'Agent', className: 'role-assistant' };
                }
                if (normalized === 'error') {
                    return { role: 'error', label: 'Error', className: 'role-error' };
                }
                if (normalized === 'function_call') {
                    return { role: 'function_call', label: 'Function Call', className: 'role-function-call' };
                }
                if (normalized === 'function_result') {
                    return { role: 'function_result', label: 'Function Result', className: 'role-function-result' };
                }
                return { role: 'user', label: 'User', className: 'role-user' };
            }
        };
    }

    global.dashboardConversationDrawerModule = dashboardConversationDrawerModule;
})(window);
