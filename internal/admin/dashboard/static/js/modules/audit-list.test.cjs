const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadAuditListModuleFactory(overrides = {}) {
    const clipboardSource = fs.readFileSync(path.join(__dirname, 'clipboard.js'), 'utf8');
    const source = fs.readFileSync(path.join(__dirname, 'audit-list.js'), 'utf8');
    const window = {
        ...(overrides.window || {})
    };
    const context = {
        console,
        ...overrides,
        window
    };
    vm.createContext(context);
    vm.runInContext(clipboardSource, context);
    vm.runInContext(source, context);
    return context.window.dashboardAuditListModule;
}

function createAuditListModule(overrides) {
    const factory = loadAuditListModuleFactory(overrides);
    return factory();
}

function loadConversationHelpers() {
    const source = fs.readFileSync(path.join(__dirname, 'conversation-helpers.js'), 'utf8');
    const context = { window: {} };
    vm.createContext(context);
    vm.runInContext(source, context);
    return context.window.DashboardConversationHelpers;
}

test('auditRequestPane returns the shared request-pane contract', () => {
    const module = createAuditListModule();
    const entry = {
        data: {
            request_headers: { authorization: 'Bearer redacted' },
            request_body: { model: 'gpt-5', stream: false },
            request_body_too_big_to_handle: true
        }
    };

    const pane = module.auditRequestPane(entry);

    assert.equal(pane.title, 'Request');
    assert.equal(pane.entry, entry);
    assert.equal(JSON.stringify(pane.copyHeaders), JSON.stringify(entry.data.request_headers));
    assert.equal(JSON.stringify(pane.copyBody), JSON.stringify(entry.data.request_body));
    assert.equal(pane.showErrorMessage, false);
    assert.equal(pane.errorMessage, null);
    assert.equal(pane.showHeaders, true);
    assert.equal(JSON.stringify(pane.headers), JSON.stringify(entry.data.request_headers));
    assert.equal(pane.showBody, true);
    assert.equal(JSON.stringify(pane.body), JSON.stringify(entry.data.request_body));
    assert.equal(pane.showEmpty, false);
    assert.equal(pane.emptyMessage, 'Request details were not captured.');
    assert.equal(pane.showTooLarge, true);
    assert.equal(pane.tooLargeMessage, 'Request body was too large to capture.');
});

test('auditResponsePane returns the shared response-pane contract', () => {
    const module = createAuditListModule();
    const entry = {
        data: {
            error_message: 'provider timeout',
            response_headers: { 'x-request-id': 'abc123' },
            response_body: { id: 'resp_123' },
            response_body_too_big_to_handle: false
        }
    };

    const pane = module.auditResponsePane(entry);

    assert.equal(pane.title, 'Response');
    assert.equal(pane.entry, entry);
    assert.equal(JSON.stringify(pane.copyHeaders), JSON.stringify(entry.data.response_headers));
    assert.equal(JSON.stringify(pane.copyBody), JSON.stringify(entry.data.response_body));
    assert.equal(pane.showErrorMessage, true);
    assert.equal(pane.errorMessage, 'provider timeout');
    assert.equal(pane.showHeaders, true);
    assert.equal(JSON.stringify(pane.headers), JSON.stringify(entry.data.response_headers));
    assert.equal(pane.showBody, true);
    assert.equal(JSON.stringify(pane.body), JSON.stringify(entry.data.response_body));
    assert.equal(pane.showEmpty, false);
    assert.equal(pane.emptyMessage, 'Response details were not captured.');
    assert.equal(pane.showTooLarge, false);
    assert.equal(pane.tooLargeMessage, 'Response body was too large to capture.');
});

test('auditEntryLiveInProgress stays true while a partial response body is streaming', () => {
    const module = createAuditListModule();
    const streaming = {
        _live: true,
        _live_pending: true,
        _live_state: 'audit.stream',
        _response_partial: true,
        status_code: 200,
        data: { response_body: { choices: [] } }
    };

    assert.equal(module.auditEntryLiveInProgress(streaming), true);
    assert.equal(module.auditEntryLiveInProgress({
        ...streaming,
        _live_state: 'audit.completed',
        _response_partial: false,
        duration_ns: 1000
    }), false);
});

test('audit panes surface pending spinners and streaming badges for live rows', () => {
    const module = createAuditListModule();

    const waiting = { _live: true, _live_pending: true, _live_state: 'audit.updated', data: {} };
    const waitingResponse = module.auditResponsePane(waiting);
    assert.equal(waitingResponse.showPending, true);
    assert.equal(waitingResponse.showEmpty, false);
    assert.equal(waitingResponse.pendingMessage, 'Response in progress…');
    const waitingRequest = module.auditRequestPane(waiting);
    assert.equal(waitingRequest.showPending, true);
    assert.equal(waitingRequest.showEmpty, false);
    assert.equal(waitingRequest.pendingMessage, 'Waiting for request data…');

    const streaming = {
        _live: true,
        _live_pending: true,
        _live_state: 'audit.stream',
        _response_partial: true,
        status_code: 200,
        data: { response_body: { choices: [{ index: 0, message: { role: 'assistant', content: 'partial' } }] } }
    };
    const streamingPane = module.auditResponsePane(streaming);
    assert.equal(streamingPane.streaming, true);
    assert.equal(streamingPane.showBody, true);
    assert.equal(streamingPane.showPending, false);

    // A stale partial flag on an already-settled entry must not keep the badge.
    const settled = {
        ...streaming,
        _live_state: 'audit.completed',
        duration_ns: 1000
    };
    assert.equal(module.auditResponsePane(settled).streaming, false);

    const persisted = { data: {} };
    const persistedPane = module.auditResponsePane(persisted);
    assert.equal(persistedPane.showPending, false);
    assert.equal(persistedPane.showEmpty, true);
    assert.equal(persistedPane.streaming, false);
});

test('audit cache helpers summarize cached prompt usage and derive a preview from the request body', () => {
    const module = createAuditListModule({
        window: {
            DashboardConversationHelpers: {
                extractRequestPromptTextSegments(body) {
                    return [body.instructions, body.messages[0].content];
                }
            }
        }
    });
    module.formatNumber = (value) => String(value);

    const entry = {
        usage: {
            input_tokens: 200,
            cached_input_tokens: 150,
            cache_write_input_tokens: 32,
            estimated_cached_characters: 600
        },
        data: {
            request_body: {
                instructions: 'You are a meticulous assistant.',
                messages: [{ role: 'user', content: 'Summarize the attached policy memo.' }]
            }
        }
    };

    assert.equal(module.auditHasCachedTokens(entry), true);
    assert.equal(module.auditCacheSharePercent(entry), 75);
    assert.equal(module.auditCacheRatioLabel(entry), '75.0% cached');
    assert.equal(module.auditCacheRatioPillLabel(entry), '75.0% cached');
    assert.equal(JSON.stringify(module.auditPromptCacheHighlight(entry)), JSON.stringify({
        characters: 600,
        segments: ['You are a meticulous assistant.', 'Summarize the attached policy memo.']
    }));

    const pane = module.auditRequestPane(entry);
    assert.equal(pane.bodyCacheRatioLabel, '75.0% cached');
    assert.equal(JSON.stringify(pane.promptCacheHighlight), JSON.stringify({
        characters: 600,
        segments: ['You are a meticulous assistant.', 'Summarize the attached policy memo.']
    }));
});

test('conversation body rendering darkens the estimated cached prompt text in request JSON', () => {
    const helpers = loadConversationHelpers();
    const requestBody = {
        model: 'anthropic/claude-sonnet-4-5',
        messages: [{
            role: 'user',
            content: [
                {
                    type: 'text',
                    text: 'Reusable prefix for Anthropic prompt caching.'
                },
                {
                    type: 'text',
                    text: 'Fresh question.'
                }
            ]
        }]
    };
    const segments = helpers.extractRequestPromptTextSegments(requestBody);

    const rendered = helpers.renderBodyWithConversationHighlights({ path: '/v1/chat/completions' }, requestBody, {
        formatJSON: (value) => JSON.stringify(value, null, 2),
        canShowConversation: () => false,
        promptCacheHighlight: {
            characters: 18,
            segments
        }
    });

    assert.match(rendered, /<span class="audit-prompt-cache-highlight">Reusable prefix fo<\/span>r Anthropic prompt caching\./);
    assert.doesNotMatch(rendered, /Fresh question\.<\/span>/);
});

test('auditEntrySummaryClass marks only live rows still waiting for a response', () => {
    const module = createAuditListModule();

    assert.equal(
        module.auditEntrySummaryClass({
            _live: true,
            _live_pending: true,
            _live_state: 'audit.started'
        })['audit-entry-summary-live-in-progress'],
        true
    );

    assert.equal(
        module.auditEntrySummaryClass({
            _live: true,
            _live_pending: true,
            _live_state: 'audit.completed',
            status_code: 200,
            duration_ns: 123000000
        })['audit-entry-summary-live-in-progress'],
        false
    );

    assert.equal(
        module.auditEntrySummaryClass({
            _live: false,
            _live_pending: false
        })['audit-entry-summary-live-in-progress'],
        false
    );
});

test('formatDurationNs rejects non-finite values', () => {
    const module = createAuditListModule();

    assert.equal(module.formatDurationNs('not-a-number'), '-');
    assert.equal(module.formatDurationNs(Number.NaN), '-');
    assert.equal(module.formatDurationNs(Number.POSITIVE_INFINITY), '-');
    assert.equal(module.formatDurationNs(0), 'pending');
    assert.equal(module.formatDurationNs('1500'), '2 \u00b5s');
    assert.equal(module.formatDurationNs(1230000000), '1.23 s');
});


test('auditResponsePane surfaces error message from captured error body', () => {
    const module = createAuditListModule();
    const entry = {
        data: {
            response_body: {
                error: {
                    type: 'provider_error',
                    message: 'http2: timeout awaiting response headers'
                }
            }
        }
    };

    const pane = module.auditResponsePane(entry);

    assert.equal(module.auditEntryErrorMessage(entry), 'http2: timeout awaiting response headers');
    assert.equal(pane.showErrorMessage, true);
    assert.equal(pane.errorMessage, 'http2: timeout awaiting response headers');
    assert.equal(pane.showEmpty, false);
});

test('auditEntryErrorMessage extracts JSON encoded gateway error text', () => {
    const module = createAuditListModule();
    const entry = {
        data: {
            error_message: '{"error":{"message":"circuit breaker is open - provider temporarily unavailable"}}'
        }
    };

    assert.equal(
        module.auditEntryErrorMessage(entry),
        'circuit breaker is open - provider temporarily unavailable'
    );
});

test('auditEntryErrorMessage ignores successful response fields', () => {
    const module = createAuditListModule();
    const entry = {
        data: {
            response_body: {
                id: 'chatcmpl_123',
                choices: [{ message: { content: 'hello' } }]
            }
        }
    };

    const pane = module.auditResponsePane(entry);

    assert.equal(module.auditEntryErrorMessage(entry), '');
    assert.equal(pane.showErrorMessage, false);
});

test('auditEntryErrorMessage ignores nested error objects on successful responses without top-level error shape', () => {
    const module = createAuditListModule();
    const entry = {
        status_code: 200,
        data: {
            response_body: {
                output: {
                    error: {
                        message: 'should not be treated as a response error'
                    }
                }
            }
        }
    };

    assert.equal(module.auditEntryErrorMessage(entry), '');
});

test('auditEntryErrorMessage reads top-level provider error shapes without relying on status code', () => {
    const module = createAuditListModule();
    const entry = {
        data: {
            response_body: {
                message: 'provider timeout',
                type: 'provider_error'
            }
        }
    };

    assert.equal(module.auditEntryErrorMessage(entry), 'provider timeout');
});

test('fetchAuditLog preserves a successful payload when workflow prefetch fails', async () => {
    const loggedErrors = [];
    const module = createAuditListModule({
        console: {
            error(...args) {
                loggedErrors.push(args);
            }
        },
        fetch() {
            return Promise.resolve({
                ok: true,
                json: async () => ({
                    entries: [{ id: 'audit-1', workflow_version_id: 'workflow-1' }],
                    total: 1,
                    limit: 25,
                    offset: 0
                })
            });
        }
    });
    module.auditFetchToken = 0;
    module.auditLog = { entries: [], total: 0, limit: 25, offset: 0 };
    module.days = 7;
    module.headers = () => ({ authorization: 'Bearer token' });
    module.handleFetchResponse = () => true;
    module.prefetchAuditWorkflows = async () => {
        throw new Error('prefetch failed');
    };

    await module.fetchAuditLog(true);

    assert.equal(
        JSON.stringify(module.auditLog),
        JSON.stringify({
            entries: [{ id: 'audit-1', workflow_version_id: 'workflow-1' }],
            total: 1,
            limit: 25,
            offset: 0
        })
    );
    assert.equal(loggedErrors.length, 1);
    assert.match(String(loggedErrors[0][0]), /Failed to prefetch audit workflows:/);
});

test('fetchAuditLog preserves live preview rows that are not flushed yet', async () => {
    const module = createAuditListModule({
        fetch() {
            return Promise.resolve({
                ok: true,
                json: async () => ({
                    entries: [{ id: 'audit-db', request_id: 'req-db' }],
                    total: 1,
                    limit: 25,
                    offset: 0
                })
            });
        }
    });
    module.auditFetchToken = 0;
    module.auditLog = {
        entries: [{
            id: 'audit-live',
            request_id: 'req-live',
            _live: true,
            _live_pending: true,
            _audit_flushed: false
        }],
        total: 1,
        limit: 25,
        offset: 0
    };
    module.days = 7;
    module.auditSearch = '';
    module.auditMethod = '';
    module.auditStatusCode = '';
    module.auditStream = '';
    module.headers = () => ({ authorization: 'Bearer token' });
    module.handleFetchResponse = () => true;

    await module.fetchAuditLog(true);

    assert.equal(module.auditLog.entries.length, 2);
    assert.equal(module.auditLog.entries[0].id, 'audit-live');
    assert.equal(module.auditLog.entries[1].id, 'audit-db');
    assert.equal(module.auditLog.total, 2);
});

test('auditLogAllowsLiveEntries respects custom date ranges', () => {
    const module = createAuditListModule();
    const now = new Date();
    const yesterday = new Date(now);
    yesterday.setDate(now.getDate() - 1);
    const tomorrow = new Date(now);
    tomorrow.setDate(now.getDate() + 1);

    module.auditSearch = '';
    module.auditMethod = '';
    module.auditStatusCode = '';
    module.auditStream = '';

    assert.equal(module.auditLogAllowsLiveEntries({ offset: 0 }), true);

    module.customStartDate = tomorrow;
    module.customEndDate = null;
    assert.equal(module.auditLogAllowsLiveEntries({ offset: 0 }), false);

    module.customStartDate = null;
    module.customEndDate = yesterday;
    assert.equal(module.auditLogAllowsLiveEntries({ offset: 0 }), false);

    module.customStartDate = yesterday;
    module.customEndDate = tomorrow;
    assert.equal(module.auditLogAllowsLiveEntries({ offset: 0 }), true);
});

test('fetchAuditLog lets persisted rows replace matching live previews', async () => {
    const module = createAuditListModule({
        fetch() {
            return Promise.resolve({
                ok: true,
                json: async () => ({
                    entries: [{ id: 'audit-db', request_id: 'req-live' }],
                    total: 1,
                    limit: 25,
                    offset: 0
                })
            });
        }
    });
    module.auditFetchToken = 0;
    module.auditLog = {
        entries: [{
            id: 'audit-live',
            request_id: 'req-live',
            _live: true,
            _live_pending: true,
            _audit_flushed: false
        }],
        total: 1,
        limit: 25,
        offset: 0
    };
    module.days = 7;
    module.auditSearch = '';
    module.auditMethod = '';
    module.auditStatusCode = '';
    module.auditStream = '';
    module.headers = () => ({ authorization: 'Bearer token' });
    module.handleFetchResponse = () => true;

    await module.fetchAuditLog(true);

    assert.equal(module.auditLog.entries.length, 1);
    assert.equal(module.auditLog.entries[0].id, 'audit-db');
    assert.equal(module.auditLog.total, 1);
});

test('fetchAuditLog sends the consolidated audit search and select filters only', async () => {
    const requests = [];
    const module = createAuditListModule({
        fetch(url) {
            requests.push(url);
            return Promise.resolve({
                ok: true,
                json: async () => ({
                    entries: [],
                    total: 0,
                    limit: 25,
                    offset: 0
                })
            });
        }
    });
    module.auditFetchToken = 0;
    module.auditLog = { entries: [], total: 0, limit: 25, offset: 0 };
    module.days = 30;
    module.auditSearch = 'team/alpha';
    module.auditMethod = 'POST';
    module.auditStatusCode = '500';
    module.auditStream = 'true';
    module.headers = () => ({});
    module.handleFetchResponse = () => true;

    await module.fetchAuditLog(true);

    assert.equal(requests.length, 1);
    assert.match(requests[0], /search=team%2Falpha/);
    assert.match(requests[0], /method=POST/);
    assert.match(requests[0], /status_code=500/);
    assert.match(requests[0], /stream=true/);
    assert.doesNotMatch(requests[0], /[?&](model|provider|path|user_path)=/);
});

test('clearAuditFilters resets the consolidated audit controls', () => {
    const module = createAuditListModule();
    let fetchCalled = false;
    module.auditSearch = 'req_123';
    module.auditMethod = 'DELETE';
    module.auditStatusCode = '404';
    module.auditStream = 'false';
    module.fetchAuditLog = (resetOffset) => {
        fetchCalled = resetOffset === true;
    };

    module.clearAuditFilters();

    assert.equal(module.auditSearch, '');
    assert.equal(module.auditMethod, '');
    assert.equal(module.auditStatusCode, '');
    assert.equal(module.auditStream, '');
    assert.equal(fetchCalled, true);
});

test('handleAuditEntryToggle lazily marks an opened audit row for details rendering', () => {
    const module = createAuditListModule();
    module.auditExpandedEntries = {};

    module.handleAuditEntryToggle({ currentTarget: { open: true } }, { id: 'audit-1' });

    assert.equal(module.isAuditEntryExpanded({ id: 'audit-1' }), true);
    assert.equal(JSON.stringify(module.auditExpandedEntries), JSON.stringify({ 'audit-1': true }));
});

test('pruneAuditExpandedEntries drops expanded state for rows no longer on the page', () => {
    const module = createAuditListModule();
    module.auditExpandedEntries = {
        'audit-1': true,
        'audit-2': true
    };

    module.pruneAuditExpandedEntries([{ id: 'audit-2' }, { id: 'audit-3' }]);

    assert.equal(JSON.stringify(module.auditExpandedEntries), JSON.stringify({ 'audit-2': true }));
});

test('auditPaneState formats initial pane content for template rendering', () => {
    const module = createAuditListModule();
    const entry = { id: 'audit-1' };
    let renderCalls = 0;
    const promptCacheHighlight = {
        characters: 42,
        segments: ['cached prompt']
    };
    module.renderBodyWithConversationHighlights = (renderEntry, body, options) => {
        renderCalls++;
        assert.equal(renderEntry, entry);
        assert.equal(JSON.stringify(options), JSON.stringify({ promptCacheHighlight }));
        return 'rendered:' + body.id;
    };

    const paneState = module.auditPaneState({
        entry,
        showHeaders: true,
        headers: { authorization: 'Bearer redacted' },
        showBody: true,
        body: { id: 'body-1' },
        promptCacheHighlight
    });

    assert.equal(paneState.formattedHeaders, '{\n  "authorization": "Bearer redacted"\n}');
    assert.equal(paneState.renderedBody, 'rendered:body-1');
    assert.equal(renderCalls, 1);
});

test('auditPaneState syncs pane content when live detail data arrives', () => {
    const module = createAuditListModule();
    const entry = { id: 'audit-1' };
    let renderCalls = 0;
    module.renderBodyWithConversationHighlights = (renderEntry, body) => {
        renderCalls++;
        assert.equal(renderEntry, entry);
        return 'rendered:' + body.id;
    };

    const paneState = module.auditPaneState({
        title: 'Response',
        entry,
        showEmpty: true,
        emptyMessage: 'Response details were not captured.'
    });

    assert.equal(paneState.pane.showEmpty, true);
    assert.equal(paneState.formattedHeaders, '');
    assert.equal(paneState.renderedBody, '');

    paneState.syncPane({
        title: 'Response',
        entry,
        showHeaders: true,
        headers: { 'x-request-id': 'req-123' },
        copyHeaders: { 'x-request-id': 'req-123' },
        showBody: true,
        body: { id: 'resp-123' },
        copyBody: { id: 'resp-123' },
        showEmpty: false
    });

    assert.equal(paneState.pane.showEmpty, false);
    assert.equal(paneState.formattedHeaders, '{\n  "x-request-id": "req-123"\n}');
    assert.equal(paneState.renderedBody, 'rendered:resp-123');
    assert.equal(renderCalls, 1);
});

test('isAudioBody detects the audio body marker', () => {
    const helpers = loadConversationHelpers();
    assert.equal(helpers.isAudioBody({ __audio__: true, content_type: 'audio/mpeg' }), true);
    assert.equal(helpers.isAudioBody({ model: 'gpt-5' }), false);
    assert.equal(helpers.isAudioBody(null), false);
    assert.equal(helpers.isAudioBody('audio'), false);
});

test('renderAudioBody renders a player with a data URL when audio bytes are stored', () => {
    const helpers = loadConversationHelpers();
    const html = helpers.renderAudioBody({
        __audio__: true,
        content_type: 'audio/mpeg',
        bytes: 2048,
        encoding: 'base64',
        data: 'QUJD',
        stored: true
    });
    assert.match(html, /<audio[^>]+controls/);
    assert.match(html, /src="data:audio\/mpeg;base64,QUJD"/);
    assert.match(html, /2\.0 KB/);
});

test('renderAudioBody sanitizes content type and strips non-base64 characters', () => {
    const helpers = loadConversationHelpers();
    const html = helpers.renderAudioBody({
        __audio__: true,
        content_type: 'audio/mpeg" onerror=alert(1)',
        bytes: 10,
        encoding: 'base64',
        data: 'AB"><script>CD',
        stored: true
    });
    assert.ok(!html.includes('onerror'), 'content type must be sanitized');
    assert.ok(!html.includes('<script>'), 'base64 payload must be sanitized');
    // Dangerous characters (<, >, ") are stripped from the data URL; only the
    // valid base64 alphabet survives (the letters of "script" are harmless).
    assert.match(html, /src="data:audio\/mpeg;base64,ABscriptCD"/);
});

test('renderAudioBody renders a placeholder when audio is not stored', () => {
    const helpers = loadConversationHelpers();
    const html = helpers.renderAudioBody({
        __audio__: true,
        content_type: 'audio/mpeg',
        bytes: 61056,
        stored: false
    });
    assert.ok(!html.includes('<audio'), 'no player when bytes are absent');
    assert.match(html, /LOGGING_LOG_AUDIO_BODIES/);
    assert.match(html, /59\.6 KB/);
});

test('renderAudioBody notes when audio was too large to store', () => {
    const helpers = loadConversationHelpers();
    const html = helpers.renderAudioBody({
        __audio__: true,
        content_type: 'audio/wav',
        bytes: 99999999,
        stored: false,
        too_large: true
    });
    assert.ok(!html.includes('<audio'));
    assert.match(html, /too large/i);
});

test('renderAudioBody renders a player and attached upload metadata', () => {
    const helpers = loadConversationHelpers();
    const html = helpers.renderAudioBody({
        __audio__: true,
        content_type: 'audio/mpeg',
        bytes: 20,
        encoding: 'base64',
        data: 'QUJD',
        stored: true,
        meta: { model: 'gpt-4o-transcribe', language: 'en', file_bytes: 20 }
    });
    assert.match(html, /<audio[^>]+src="data:audio\/mpeg;base64,QUJD"/);
    assert.match(html, /audit-audio-metadata/);
    assert.match(html, /gpt-4o-transcribe/);
    assert.match(html, /language/);
});

test('renderAudioBody renders metadata even when audio is not stored', () => {
    const helpers = loadConversationHelpers();
    const html = helpers.renderAudioBody({
        __audio__: true,
        content_type: 'audio/wav',
        bytes: 99999999,
        stored: false,
        too_large: true,
        meta: { model: 'whisper-1' }
    });
    assert.ok(!html.includes('<audio'), 'no player when too large');
    assert.match(html, /too large/i);
    assert.match(html, /whisper-1/);
});

test('auditPaneState renders audio bodies through the audio helper', () => {
    const helpers = loadConversationHelpers();
    const module = createAuditListModule({
        window: { DashboardConversationHelpers: helpers }
    });
    const paneState = module.auditPaneState({
        entry: { id: 'audit-1' },
        showBody: true,
        body: {
            __audio__: true,
            content_type: 'audio/mpeg',
            bytes: 1024,
            encoding: 'base64',
            data: 'QUJD',
            stored: true
        }
    });
    assert.match(paneState.renderedBody, /<audio[^>]+src="data:audio\/mpeg;base64,QUJD"/);
});

test('auditPaneState copies the formatted body and resets success feedback', async () => {
    let resetCallback = null;
    const writes = [];
    const module = createAuditListModule({
        setTimeout(callback) {
            resetCallback = callback;
            return 1;
        },
        clearTimeout() {},
        window: {
            navigator: {
                clipboard: {
                    writeText(value) {
                        writes.push(value);
                        return Promise.resolve();
                    }
                }
            }
        }
    });

    const paneState = module.auditPaneState({
        copyBody: { model: 'gpt-5', stream: false }
    });

    await paneState.copyBody();

    assert.deepEqual(writes, ['{\n  "model": "gpt-5",\n  "stream": false\n}']);
    assert.equal(paneState.copyBodyState.copied, true);
    assert.equal(paneState.copyBodyState.error, false);

    assert.equal(typeof resetCallback, 'function');
    resetCallback();

    assert.equal(paneState.copyBodyState.copied, false);
    assert.equal(paneState.copyBodyState.error, false);
});

test('auditPaneState copies the formatted headers independently from body feedback', async () => {
    const writes = [];
    const module = createAuditListModule({
        setTimeout() {
            return 1;
        },
        clearTimeout() {},
        window: {
            navigator: {
                clipboard: {
                    writeText(value) {
                        writes.push(value);
                        return Promise.resolve();
                    }
                }
            }
        }
    });

    const paneState = module.auditPaneState({
        copyHeaders: { 'x-request-id': 'req-123' },
        copyBody: { id: 'body-1' }
    });

    await paneState.copyHeaders();

    assert.deepEqual(writes, ['{\n  "x-request-id": "req-123"\n}']);
    assert.equal(paneState.copyHeadersState.copied, true);
    assert.equal(paneState.copyHeadersState.error, false);
    assert.equal(paneState.copyBodyState.copied, false);
    assert.equal(paneState.copyBodyState.error, false);
});

test('auditPaneState marks copy failures and clears the error after reset', async () => {
    let resetCallback = null;
    const module = createAuditListModule({
        console: {
            error() {}
        },
        setTimeout(callback) {
            resetCallback = callback;
            return 1;
        },
        clearTimeout() {},
        window: {
            navigator: {
                clipboard: {
                    writeText() {
                        return Promise.reject(new Error('denied'));
                    }
                }
            }
        }
    });

    const paneState = module.auditPaneState({
        copyBody: { id: 'resp_123' }
    });

    await paneState.copyBody();

    assert.equal(paneState.copyBodyState.copied, false);
    assert.equal(paneState.copyBodyState.error, true);

    assert.equal(typeof resetCallback, 'function');
    resetCallback();

    assert.equal(paneState.copyBodyState.copied, false);
    assert.equal(paneState.copyBodyState.error, false);
});

test('auditTabKeydown roves the tablist with arrow and home/end keys', () => {
    const module = createAuditListModule();
    const entry = {
        data: {
            request_body: { model: 'gpt-5' },
            response_body: { ok: true }
        }
    };

    // A plain entry (no failed attempts) yields exactly the request + response tabs.
    assert.equal(module.auditPanes(entry).map((p) => p.id).join(','), 'request,response');

    const press = (key, currentId) => {
        let prevented = false;
        const event = {
            key,
            preventDefault() {
                prevented = true;
            },
            currentTarget: { closest: () => null }
        };
        const result = module.auditTabKeydown(event, entry, currentId);
        return { result, prevented };
    };

    // Next/previous, wrapping at the ends.
    assert.deepEqual(press('ArrowRight', 'request'), { result: 'response', prevented: true });
    assert.deepEqual(press('ArrowDown', 'request'), { result: 'response', prevented: true });
    assert.deepEqual(press('ArrowRight', 'response'), { result: 'request', prevented: true });
    assert.deepEqual(press('ArrowLeft', 'request'), { result: 'response', prevented: true });
    assert.deepEqual(press('ArrowUp', 'request'), { result: 'response', prevented: true });

    // Home/End jump to the ends.
    assert.deepEqual(press('Home', 'response'), { result: 'request', prevented: true });
    assert.deepEqual(press('End', 'request'), { result: 'response', prevented: true });

    // Unhandled keys leave the selection untouched and do not swallow the event.
    assert.deepEqual(press('Tab', 'request'), { result: null, prevented: false });
    assert.deepEqual(press('a', 'request'), { result: null, prevented: false });
});

test('auditPanes inserts request revision tabs between request and response', () => {
    const module = createAuditListModule();
    const revision = {
        seq: 1,
        rewriter: 'pro-token-compression',
        bytes_before: 1572,
        bytes_after: 1249,
        body: { model: 'gpt-5', compressed: true },
        detail: { tokens_saved_estimate: 89, blocks_replaced: 1 }
    };
    const entry = {
        data: {
            request_body: { model: 'gpt-5' },
            response_body: { ok: true },
            request_revisions: [revision]
        }
    };

    assert.equal(module.auditPanes(entry).map((p) => p.id).join(','), 'request,revision-1,response');

    const pane = module.auditRequestRevisionPane(entry, revision);
    assert.equal(pane.title, 'Rewritten');
    assert.equal(pane.kind, 'pro-token-compression');
    assert.equal(pane.seq, 0); // a single revision hides the #seq chip
    assert.equal(pane.headersTitle, 'What changed');
    assert.equal(pane.headers.bytes, '1572 → 1249');
    assert.equal(pane.headers.detail.tokens_saved_estimate, 89);
    assert.equal(pane.savingsLabel, '-21%');
    assert.equal(pane.showBody, true);
    assert.equal(pane.showTooLarge, false);

    // Without a captured body the pane explains why instead of showing JSON.
    const bare = module.auditRequestRevisionPane(entry, { seq: 2, rewriter: 'x', bytes_before: 10, bytes_after: 8 });
    assert.equal(bare.showBody, false);
    assert.equal(bare.showTooLarge, true);

    // Multiple revisions keep their sequence chips and stable tab ids.
    entry.data.request_revisions = [revision, { ...revision, seq: 2 }];
    assert.equal(module.auditPanes(entry).map((p) => p.id).join(','), 'request,revision-1,revision-2,response');
    assert.equal(module.auditRequestRevisionPane(entry, revision).seq, 1);
});

test('auditRevisionPercentLabel reports the share of the body removed', () => {
    const module = createAuditListModule();
    assert.equal(module.auditRevisionPercentLabel({ bytes_before: 1572, bytes_after: 1249 }), '-21%');
    assert.equal(module.auditRevisionPercentLabel({ bytes_before: 1000, bytes_after: 560 }), '-44%');
    assert.equal(module.auditRevisionPercentLabel({ bytes_before: 1000, bytes_after: 954 }), '-4.6%');
    // Missing sizes or a revision that grew the body renders no percent.
    assert.equal(module.auditRevisionPercentLabel({ bytes_before: 0, bytes_after: 10 }), '');
    assert.equal(module.auditRevisionPercentLabel({ bytes_before: 100, bytes_after: 120 }), '');
    assert.equal(module.auditRevisionPercentLabel({}), '');
    assert.equal(module.auditRevisionPercentLabel(null), '');
});

test('entries without revisions render no revision tabs', () => {
    const module = createAuditListModule();
    const entry = { data: { request_body: { model: 'gpt-5' }, response_body: { ok: true } } };
    assert.equal(module.auditPanes(entry).map((p) => p.id).join(','), 'request,response');
    assert.equal(module.auditRequestRevisions(entry).length, 0);
});
