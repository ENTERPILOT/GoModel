const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadLiveLogsModuleFactory(overrides = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'live-logs.js'), 'utf8');
    const context = {
        console,
        setTimeout,
        clearTimeout,
        TextDecoder,
        ReadableStream,
        window: {},
        ...overrides
    };
    vm.createContext(context);
    vm.runInContext(source, context);
    return context.window.dashboardLiveLogsModule;
}

function createLiveLogsApp(overrides = {}) {
    const factory = loadLiveLogsModuleFactory(overrides);
    return {
        auditLog: { entries: [], total: 0, limit: 25, offset: 0 },
        usageLog: { entries: [], total: 0, limit: 50, offset: 0 },
        auditSearch: '',
        auditMethod: '',
        auditStatusCode: '',
        auditStream: '',
        usageLogSearch: '',
        usageFilterModel: '',
        usageFilterProvider: '',
        usageFilterLabel: '',
        usageFilterUserPath: '',
        page: 'audit-logs',
        fetchUsageCalls: 0,
        fetchAuditCalls: 0,
        fetchUsage() {
            this.fetchUsageCalls++;
        },
        fetchAuditLog() {
            this.fetchAuditCalls++;
        },
        handleFetchResponse() {
            return true;
        },
        requestOptions() {
            return { headers: {} };
        },
        ...factory()
    };
}

function liveReaderFromChunks(chunks) {
    const values = chunks.map((chunk) => Buffer.from(chunk));
    let index = 0;
    return {
        async read() {
            if (index >= values.length) {
                return { done: true };
            }
            return { done: false, value: values[index++] };
        }
    };
}

test('live audit lifecycle events merge into one dashboard row by request id', () => {
    const app = createLiveLogsApp();

    app.applyLiveLogEvent({
        seq: 1,
        type: 'audit.started',
        data: { id: 'audit-1', request_id: 'req-1', method: 'POST', path: '/v1/chat/completions' }
    });
    app.applyLiveLogEvent({
        seq: 2,
        type: 'audit.updated',
        data: { id: 'audit-1', request_id: 'req-1', requested_model: 'gpt-test', provider: 'openai' }
    });
    app.applyLiveLogEvent({
        seq: 3,
        type: 'audit.completed',
        data: { id: 'audit-1', request_id: 'req-1', status_code: 200, duration_ns: 1000 }
    });
    app.applyLiveLogEvent({
        seq: 4,
        type: 'audit.flushed',
        data: { id: 'audit-1', request_id: 'req-1', status_code: 200, duration_ns: 1000 }
    });

    assert.equal(app.liveLogsLastSeq, 4);
    assert.equal(app.auditLog.entries.length, 1);
    assert.equal(app.auditLog.entries[0].requested_model, 'gpt-test');
    assert.equal(app.auditLog.entries[0].status_code, 200);
    assert.equal(app.auditLog.entries[0]._live_state, 'audit.flushed');
    assert.equal(app.auditLog.entries[0]._live_pending, false);
    assert.equal(app.auditLog.entries[0]._audit_flushed, true);
});

test('audit.stream events merge partial response bodies and keep rows pending', () => {
    const app = createLiveLogsApp();

    app.applyLiveLogEvent({
        seq: 1,
        type: 'audit.started',
        data: { id: 'audit-1', request_id: 'req-1', method: 'POST', path: '/v1/chat/completions' }
    });
    app.applyLiveLogEvent({
        seq: 2,
        type: 'audit.stream',
        data: {
            id: 'audit-1',
            request_id: 'req-1',
            status_code: 200,
            stream: true,
            data: {
                response_body: { choices: [{ index: 0, message: { role: 'assistant', content: 'partial' } }] },
                response_body_partial: true
            }
        }
    });

    const streaming = app.auditLog.entries[0];
    assert.equal(streaming._response_partial, true);
    assert.equal(streaming._live_pending, true);
    assert.equal(streaming._live_state, 'audit.stream');
    assert.equal(streaming.data.response_body.choices[0].message.content, 'partial');

    app.applyLiveLogEvent({
        seq: 3,
        type: 'audit.completed',
        data: {
            id: 'audit-1',
            request_id: 'req-1',
            status_code: 200,
            duration_ns: 1000,
            data: {
                response_body: { choices: [{ index: 0, message: { role: 'assistant', content: 'final' } }] }
            }
        }
    });

    const completed = app.auditLog.entries[0];
    assert.equal(completed._response_partial, false);
    assert.equal(completed._live_state, 'audit.completed');
    assert.equal(completed.data.response_body.choices[0].message.content, 'final');
});

test('live audit merges notify an open live conversation drawer', () => {
    const app = createLiveLogsApp();
    const seen = [];
    app.refreshLiveConversation = (entry) => seen.push(entry);

    app.applyLiveLogEvent({
        seq: 1,
        type: 'audit.started',
        data: { id: 'audit-1', request_id: 'req-1' }
    });
    app.applyLiveLogEvent({
        seq: 2,
        type: 'audit.stream',
        data: { id: 'audit-1', request_id: 'req-1', data: { response_body: { choices: [] }, response_body_partial: true } }
    });

    assert.equal(seen.length, 2);
    assert.equal(seen[1]._response_partial, true);
});

test('live audit removed event drops suppressed preview rows', () => {
    const app = createLiveLogsApp();
    app.auditLog.entries = [{ id: 'audit-1', request_id: 'req-1' }];
    app.auditLog.total = 1;

    app.applyLiveLogEvent({
        seq: 4,
        type: 'audit.removed',
        data: { id: 'audit-1', request_id: 'req-1' }
    });

    assert.deepEqual(app.auditLog.entries, []);
    assert.equal(app.auditLog.total, 0);
});

test('live audit removed event decrements total by removed row count', () => {
    const app = createLiveLogsApp();
    app.auditLog.entries = [
        { id: 'audit-1', request_id: 'req-1' },
        { id: 'audit-2', request_id: 'req-1' },
        { id: 'audit-3', request_id: 'req-3' }
    ];
    app.auditLog.total = 3;

    app.applyLiveLogEvent({
        seq: 5,
        type: 'audit.removed',
        data: { request_id: 'req-1' }
    });

    assert.deepEqual(app.auditLog.entries, [{ id: 'audit-3', request_id: 'req-3' }]);
    assert.equal(app.auditLog.total, 1);
});

test('live audit inserts are blocked while a custom audit date range is active', () => {
    const app = createLiveLogsApp();

    app.customStartDate = new Date();
    app.applyLiveLogEvent({
        seq: 1,
        type: 'audit.started',
        data: { id: 'audit-start', request_id: 'req-start' }
    });

    app.customStartDate = null;
    app.customEndDate = new Date();
    app.applyLiveLogEvent({
        seq: 2,
        type: 'audit.started',
        data: { id: 'audit-end', request_id: 'req-end' }
    });

    assert.equal(app.auditLog.entries.length, 0);
    assert.equal(app.auditLog.total, 0);
});

test('live logs parser handles CRLF-separated SSE frames', async () => {
    const app = createLiveLogsApp();

    await app.consumeLiveLogsBody(liveReaderFromChunks([
        'data: {"seq":1,"type":"heartbeat"}\r\n\r\n',
        'data: {"seq":2,"type":"audit.started","data":{"id":"audit-1","request_id":"req-1"}}\r\n\r\n'
    ]));

    assert.equal(app.liveLogsLastSeq, 2);
    assert.equal(app.auditLog.entries.length, 1);
    assert.equal(app.auditLog.entries[0].id, 'audit-1');
});

test('live stream fetch uses dashboard base path helper', async () => {
    const urls = [];
    const factory = loadLiveLogsModuleFactory({
        window: {
            gomodelPath(pathValue) {
                return '/base' + pathValue;
            }
        },
        fetch: async (url) => {
            urls.push(url);
            return { body: null };
        }
    });
    const app = {
        ...factory(),
        liveLogsLastSeq: 42,
        requestOptions() {
            return { headers: {} };
        },
        handleFetchResponse() {
            return true;
        },
        scheduleLiveLogsReconnect() {}
    };

    await app.readLiveLogsStream(null);

    assert.deepEqual(urls, ['/base/admin/live/logs?types=audit,usage&cursor=42']);
});

test('audit detail fetch uses dashboard base path helper', async () => {
    const urls = [];
    const factory = loadLiveLogsModuleFactory({
        window: {
            gomodelPath(pathValue) {
                return '/base' + pathValue;
            }
        },
        fetch: async (url) => {
            urls.push(url);
            return {
                async json() {
                    return {
                        id: 'audit-1',
                        request_id: 'req-1',
                        data: {
                            request_headers: { 'content-type': 'application/json' }
                        }
                    };
                }
            };
        }
    });
    const entry = { id: 'audit-1', request_id: 'req-1' };
    const app = {
        ...factory(),
        auditLog: { entries: [entry], total: 1, limit: 25, offset: 0 },
        auditSearch: '',
        auditMethod: '',
        auditStatusCode: '',
        auditStream: '',
        requestOptions() {
            return { headers: {} };
        },
        handleFetchResponse() {
            return true;
        }
    };

    await app.fetchAuditEntryDetail(entry);

    assert.deepEqual(urls, ['/base/admin/audit/detail?log_id=audit-1']);
    assert.equal(app.auditLog.entries[0]._detail_loaded, true);
    assert.equal(app.auditLog.entries[0].data.request_headers['content-type'], 'application/json');
});

test('live usage event updates usage log and enriches matching audit row', () => {
    const app = createLiveLogsApp();
    app.auditLog.entries = [{ id: 'audit-1', request_id: 'req-1' }];

    app.applyLiveLogEvent({
        seq: 5,
        type: 'usage.completed',
        data: {
            id: 'usage-1',
            request_id: 'req-1',
            model: 'gpt-test',
            provider: 'openai',
            input_tokens: 10,
            output_tokens: 4,
            total_tokens: 14
        }
    });

    assert.equal(app.usageLog.entries.length, 1);
    assert.equal(app.usageLog.entries[0].id, 'usage-1');
    assert.equal(app.auditLog.entries[0].usage.total_tokens, 14);
    assert.equal(app.auditLog.entries[0]._usage_live_pending, true);

    app.applyLiveLogEvent({
        seq: 6,
        type: 'usage.flushed',
        data: {
            id: 'usage-1',
            request_id: 'req-1',
            model: 'gpt-test',
            provider: 'openai',
            input_tokens: 10,
            output_tokens: 4,
            total_tokens: 14
        }
    });

    assert.equal(app.usageLog.entries[0]._live_pending, false);
    assert.equal(app.auditLog.entries[0]._usage_flushed, true);
});

test('cached live usage events stay visible in default usage preview', () => {
    const app = createLiveLogsApp();
    app.auditLog.entries = [{
        id: 'audit-cache',
        request_id: 'req-cache',
        cache_type: 'exact',
        _live: true
    }];

    app.applyLiveLogEvent({
        seq: 7,
        type: 'usage.completed',
        data: {
            id: 'usage-cache',
            request_id: 'req-cache',
            cache_type: 'exact',
            model: 'gpt-test',
            provider: 'openai',
            input_tokens: 10,
            output_tokens: 4,
            total_tokens: 14,
            total_cost: 0
        }
    });

    assert.equal(app.liveLogsLastSeq, 7);
    assert.equal(app.usageLog.entries.length, 1);
    assert.equal(app.usageLog.total, 1);
    assert.equal(app.usageLog.entries[0].id, 'usage-cache');
    assert.equal(app.usageLog.entries[0]._live_pending, true);
    assert.equal(app.auditLog.entries[0].usage.total_tokens, 14);
    assert.equal(app.auditLog.entries[0]._usage_live_pending, true);
    assert.equal(app.auditLog.entries[0]._usage_live_state, 'usage.completed');
});

test('cached live usage flushed events keep and settle existing previews', () => {
    const app = createLiveLogsApp();
    app.auditLog.entries = [{
        id: 'audit-cache',
        request_id: 'req-cache',
        cache_type: 'exact',
        usage: { entries: 1, input_tokens: 10, uncached_input_tokens: 10, output_tokens: 4, total_tokens: 14 },
        _usage_live_state: 'usage.completed',
        _usage_live_pending: true
    }];
    app.usageLog.entries = [{
        id: 'usage-cache',
        request_id: 'req-cache',
        cache_type: 'exact',
        input_tokens: 10,
        output_tokens: 4,
        total_tokens: 14,
        _live: true,
        _live_pending: true
    }];
    app.usageLog.total = 1;

    app.applyLiveLogEvent({
        seq: 8,
        type: 'usage.flushed',
        data: {
            id: 'usage-cache',
            request_id: 'req-cache',
            cache_type: 'exact'
        }
    });

    assert.equal(app.usageLog.entries.length, 1);
    assert.equal(app.usageLog.total, 1);
    assert.equal(app.usageLog.entries[0]._live_state, 'usage.flushed');
    assert.equal(app.usageLog.entries[0]._live_pending, false);
    assert.equal(app.usageLog.entries[0]._usage_flushed, true);
    assert.equal(app.auditLog.entries[0]._usage_flushed, true);
    assert.equal(app.auditLog.entries[0]._usage_live_pending, false);
    assert.equal(app.auditLog.entries[0].usage.total_tokens, 14);
});

test('cached live usage attaches when the audit preview arrives later', () => {
    const app = createLiveLogsApp();

    app.applyLiveLogEvent({
        seq: 9,
        type: 'usage.completed',
        data: {
            id: 'usage-cache',
            request_id: 'req-cache',
            cache_type: 'exact',
            model: 'gpt-test',
            provider: 'openai',
            input_tokens: 10,
            output_tokens: 4,
            total_tokens: 14
        }
    });
    app.applyLiveLogEvent({
        seq: 10,
        type: 'audit.completed',
        data: {
            id: 'audit-cache',
            request_id: 'req-cache',
            cache_type: 'exact',
            status_code: 200,
            data: {
                workflow_features: {
                    cache: true,
                    audit: true,
                    usage: true
                }
            }
        }
    });

    assert.equal(app.auditLog.entries.length, 1);
    assert.equal(app.auditLog.entries[0].usage.total_tokens, 14);
    assert.equal(app.auditLog.entries[0]._usage_live_state, 'usage.completed');
    assert.equal(app.auditLog.entries[0]._usage_live_pending, true);
});

test('hidden cached live usage attaches to later audit previews', () => {
    const app = createLiveLogsApp();
    app.usageLogHideCached = true;

    app.applyLiveLogEvent({
        seq: 11,
        type: 'usage.completed',
        data: {
            id: 'usage-cache',
            request_id: 'req-cache',
            cache_type: 'exact',
            model: 'gpt-test',
            provider: 'openai',
            input_tokens: 10,
            output_tokens: 4,
            total_tokens: 14
        }
    });

    assert.equal(app.usageLog.entries.length, 0);

    app.applyLiveLogEvent({
        seq: 12,
        type: 'audit.completed',
        data: {
            id: 'audit-cache',
            request_id: 'req-cache',
            cache_type: 'exact',
            status_code: 200
        }
    });

    assert.equal(app.auditLog.entries[0].usage.total_tokens, 14);
    assert.equal(app.auditLog.entries[0]._usage_live_state, 'usage.completed');
    assert.equal(app.auditLog.entries[0]._usage_live_pending, true);
    assert.equal(app.skippedLiveUsageByRequestId['req-cache'], undefined);

    app.applyLiveLogEvent({
        seq: 13,
        type: 'usage.flushed',
        data: {
            id: 'usage-cache',
            request_id: 'req-cache',
            cache_type: 'exact'
        }
    });

    assert.equal(app.usageLog.entries.length, 0);
    assert.equal(app.auditLog.entries[0].usage.total_tokens, 14);
    assert.equal(app.auditLog.entries[0]._usage_flushed, true);
    assert.equal(app.auditLog.entries[0]._usage_live_pending, false);

    app.usageLogHideCached = false;
    app.applyLiveLogEvent({
        seq: 14,
        type: 'usage.flushed',
        data: {
            id: 'usage-cache',
            request_id: 'req-cache',
            cache_type: 'exact'
        }
    });

    assert.equal(app.usageLog.entries.length, 1);
    assert.equal(app.usageLog.entries[0].total_tokens, 14);
    assert.equal(app.skippedLiveUsageByRequestId['req-cache'], undefined);
});

test('cached updates to visible live usage rows move to skipped when cached rows are hidden', () => {
    const app = createLiveLogsApp();
    app.auditLog.entries = [{ id: 'audit-cache', request_id: 'req-cache' }];

    app.applyLiveLogEvent({
        seq: 15,
        type: 'usage.completed',
        data: {
            id: 'usage-cache',
            request_id: 'req-cache',
            model: 'gpt-test',
            provider: 'openai',
            input_tokens: 10,
            output_tokens: 4,
            total_tokens: 14
        }
    });

    assert.equal(app.usageLog.entries.length, 1);
    assert.equal(app.usageLog.total, 1);

    app.usageLogHideCached = true;
    app.applyLiveLogEvent({
        seq: 16,
        type: 'usage.flushed',
        data: {
            id: 'usage-cache',
            request_id: 'req-cache',
            cache_type: 'exact'
        }
    });

    assert.equal(app.usageLog.entries.length, 0);
    assert.equal(app.usageLog.total, 0);
    assert.equal(app.auditLog.entries[0]._usage_flushed, true);
    assert.equal(app.auditLog.entries[0].usage.total_tokens, 14);
    assert.equal(app.skippedLiveUsageByRequestId['req-cache'].total_tokens, 14);
});

test('live usage audit summary uses normalized split prompt-cache tokens', () => {
    const app = createLiveLogsApp();
    app.auditLog.entries = [{ id: 'audit-cache', request_id: 'req-cache' }];

    app.applyLiveLogEvent({
        seq: 17,
        type: 'usage.completed',
        data: {
            id: 'usage-cache',
            request_id: 'req-cache',
            input_tokens: 100,
            uncached_input_tokens: 100,
            cached_input_tokens: 50,
            cache_write_input_tokens: 25,
            output_tokens: 20,
            total_tokens: 120
        }
    });

    const summary = app.auditLog.entries[0].usage;
    assert.equal(summary.input_tokens, 175);
    assert.equal(summary.uncached_input_tokens, 100);
    assert.equal(summary.cached_input_tokens, 50);
    assert.equal(summary.cache_write_input_tokens, 25);
    assert.equal(summary.output_tokens, 20);
    assert.equal(summary.total_tokens, 195);
    assert.equal(summary.cached_input_ratio, 50 / 175);
    assert.equal(summary.estimated_cached_characters, 200);
});

test('audit detail merge preserves existing live lifecycle state', () => {
    const app = createLiveLogsApp();
    app.auditLog.entries = [{
        id: 'audit-1',
        request_id: 'req-1',
        _live: true,
        _live_state: 'audit.completed',
        _live_pending: true,
        _audit_flushed: false,
        data: {
            workflow_features: { cache: true }
        }
    }];

    const merged = app.mergeLiveAuditEntry({
        id: 'audit-1',
        request_id: 'req-1',
        data: {
            request_headers: { authorization: 'Bearer redacted' }
        }
    }, 'audit.detail');

    assert.equal(merged._live, true);
    assert.equal(merged._live_state, 'audit.completed');
    assert.equal(merged._live_pending, true);
    assert.equal(merged._audit_flushed, false);
    assert.equal(merged._detail_loaded, true);
    assert.deepEqual(merged.data.workflow_features, { cache: true });
    assert.deepEqual(merged.data.request_headers, { authorization: 'Bearer redacted' });
});

test('live audit lifecycle patches merge captured detail data', () => {
    const app = createLiveLogsApp();
    app.auditLog.entries = [{
        id: 'audit-1',
        request_id: 'req-1',
        _detail_loaded: true,
        data: {
            request_headers: { authorization: 'Bearer redacted' },
            response_body: { id: 'chatcmpl_123' }
        }
    }];

    app.applyLiveLogEvent({
        seq: 9,
        type: 'audit.flushed',
        data: {
            id: 'audit-1',
            request_id: 'req-1',
            status_code: 200,
            data: {
                response_headers: { 'x-request-id': 'req-1' }
            }
        }
    });

    assert.equal(app.auditLog.entries[0].status_code, 200);
    assert.equal(app.auditLog.entries[0]._live_state, 'audit.flushed');
    assert.equal(app.auditLog.entries[0]._live_pending, false);
    assert.deepEqual(app.auditLog.entries[0].data.request_headers, { authorization: 'Bearer redacted' });
    assert.deepEqual(app.auditLog.entries[0].data.response_headers, { 'x-request-id': 'req-1' });
    assert.deepEqual(app.auditLog.entries[0].data.response_body, { id: 'chatcmpl_123' });
});

test('audit detail fetch runs for compact live workflow data and clears stored row loading state', async () => {
    const requests = [];
    const app = createLiveLogsApp({
        fetch(url) {
            requests.push(url);
            return Promise.resolve({
                json: async () => ({
                    id: 'audit-1',
                    request_id: 'req-1',
                    data: {
                        request_headers: { authorization: 'Bearer redacted' }
                    }
                })
            });
        }
    });
    app.auditLog.entries = [{
        id: 'audit-1',
        request_id: 'req-1',
        data: {
            workflow_features: { cache: true }
        }
    }];

    await app.fetchAuditEntryDetail(app.auditLog.entries[0]);

    assert.equal(requests.length, 1);
    assert.match(requests[0], /log_id=audit-1/);
    assert.deepEqual(app.auditLog.entries[0].data.request_headers, { authorization: 'Bearer redacted' });
    assert.equal(app.auditLog.entries[0]._detail_loading, false);
    assert.equal(app.auditLog.entries[0]._detail_loaded, true);
});

test('audit detail fetch skips rows that already have captured detail data', async () => {
    let requests = 0;
    const app = createLiveLogsApp({
        fetch() {
            requests++;
            return Promise.reject(new Error('fetch should not run'));
        }
    });
    app.auditLog.entries = [{
        id: 'audit-1',
        request_id: 'req-1',
        data: {
            request_headers: { authorization: 'Bearer redacted' }
        }
    }];

    await app.fetchAuditEntryDetail(app.auditLog.entries[0]);

    assert.equal(requests, 0);
});

test('audit detail fetch waits for live audit rows to flush before loading persisted detail', async () => {
    let requests = 0;
    const app = createLiveLogsApp({
        fetch() {
            requests++;
            return Promise.reject(new Error('fetch should not run before flush'));
        }
    });
    const entry = {
        id: 'audit-1',
        request_id: 'req-1',
        _live: true,
        _live_state: 'audit.updated',
        _live_pending: true,
        _audit_flushed: false,
        data: {
            request_headers: { authorization: 'Bearer redacted' },
            request_body: { model: 'gpt-test' }
        }
    };

    await app.fetchAuditEntryDetail(entry);

    assert.equal(requests, 0);
});

test('flushed live audit rows fetch persisted detail even with preview request data', async () => {
    const requests = [];
    const app = createLiveLogsApp({
        fetch(url) {
            requests.push(url);
            return Promise.resolve({
                json: async () => ({
                    id: 'audit-1',
                    request_id: 'req-1',
                    data: {
                        response_headers: { 'x-request-id': 'req-1' },
                        response_body: { id: 'chatcmpl-test' }
                    }
                })
            });
        }
    });
    app.auditLog.entries = [{
        id: 'audit-1',
        request_id: 'req-1',
        _live: true,
        _live_state: 'audit.flushed',
        _live_pending: false,
        _audit_flushed: true,
        data: {
            request_headers: { authorization: 'Bearer redacted' },
            request_body: { model: 'gpt-test' }
        }
    }];

    await app.fetchAuditEntryDetail(app.auditLog.entries[0]);

    assert.equal(requests.length, 1);
    assert.match(requests[0], /log_id=audit-1/);
    assert.equal(app.auditLog.entries[0]._detail_loaded, true);
    assert.deepEqual(app.auditLog.entries[0].data.request_headers, { authorization: 'Bearer redacted' });
    assert.deepEqual(app.auditLog.entries[0].data.request_body, { model: 'gpt-test' });
    assert.deepEqual(app.auditLog.entries[0].data.response_headers, { 'x-request-id': 'req-1' });
    assert.deepEqual(app.auditLog.entries[0].data.response_body, { id: 'chatcmpl-test' });
});

test('late queued events do not regress flushed live rows to pending', () => {
    const app = createLiveLogsApp();

    app.applyLiveLogEvent({
        seq: 1,
        type: 'audit.flushed',
        data: { id: 'audit-1', request_id: 'req-1', status_code: 200, duration_ns: 1000 }
    });
    app.applyLiveLogEvent({
        seq: 2,
        type: 'audit.completed',
        data: { id: 'audit-1', request_id: 'req-1', status_code: 200, duration_ns: 1000 }
    });

    assert.equal(app.auditLog.entries[0]._live_state, 'audit.flushed');
    assert.equal(app.auditLog.entries[0]._live_pending, false);
    assert.equal(app.auditLog.entries[0]._audit_flushed, true);

    app.auditLog.entries[0].usage = { entries: 1 };
    app.applyLiveLogEvent({
        seq: 3,
        type: 'usage.flushed',
        data: { id: 'usage-1', request_id: 'req-1', total_tokens: 14 }
    });
    app.applyLiveLogEvent({
        seq: 4,
        type: 'usage.completed',
        data: { id: 'usage-1', request_id: 'req-1', total_tokens: 14 }
    });

    assert.equal(app.usageLog.entries[0]._live_state, 'usage.flushed');
    assert.equal(app.usageLog.entries[0]._live_pending, false);
    assert.equal(app.usageLog.entries[0]._usage_flushed, true);
    assert.equal(app.auditLog.entries[0]._usage_live_state, 'usage.flushed');
    assert.equal(app.auditLog.entries[0]._usage_live_pending, false);
    assert.equal(app.auditLog.entries[0]._usage_flushed, true);
});

test('flushed expanded live audit rows retry detail fetch', () => {
    const app = createLiveLogsApp();
    let detailFetches = 0;
    app.auditLog.entries = [{
        id: 'audit-1',
        request_id: 'req-1',
        _live: true,
        _live_state: 'audit.completed',
        _live_pending: true,
        _audit_flushed: false
    }];
    app.isAuditEntryExpanded = (entry) => String(entry && entry.id || '') === 'audit-1';
    app.fetchAuditEntryDetail = (entry) => {
        detailFetches++;
        assert.equal(entry.id, 'audit-1');
        assert.equal(entry._live_state, 'audit.flushed');
    };

    app.applyLiveLogEvent({
        seq: 5,
        type: 'audit.flushed',
        data: { id: 'audit-1', request_id: 'req-1', status_code: 200 }
    });

    assert.equal(detailFetches, 1);
});

test('failed live events clear pending state', () => {
    const app = createLiveLogsApp();

    app.applyLiveLogEvent({
        seq: 1,
        type: 'audit.completed',
        data: { id: 'audit-1', request_id: 'req-1' }
    });
    app.applyLiveLogEvent({
        seq: 2,
        type: 'audit.failed',
        data: { id: 'audit-1', request_id: 'req-1' }
    });
    app.applyLiveLogEvent({
        seq: 3,
        type: 'usage.completed',
        data: { id: 'usage-1', request_id: 'req-1', total_tokens: 14 }
    });
    app.applyLiveLogEvent({
        seq: 4,
        type: 'usage.failed',
        data: { id: 'usage-1', request_id: 'req-1', total_tokens: 14 }
    });

    assert.equal(app.auditLog.entries[0]._live_state, 'audit.failed');
    assert.equal(app.auditLog.entries[0]._live_pending, false);
    assert.equal(app.usageLog.entries[0]._live_state, 'usage.failed');
    assert.equal(app.usageLog.entries[0]._live_pending, false);
    assert.equal(app.auditLog.entries[0]._usage_live_state, 'usage.failed');
    assert.equal(app.auditLog.entries[0]._usage_live_pending, false);
});

test('cached live usage events are suppressed when hide-cached toggle is on', () => {
    const app = createLiveLogsApp();
    app.usageLogHideCached = true;

    app.applyLiveLogEvent({
        seq: 10,
        type: 'usage.completed',
        data: {
            id: 'usage-cache',
            request_id: 'req-cache',
            cache_type: 'exact',
            model: 'gpt-test',
            provider: 'openai',
            input_tokens: 10,
            output_tokens: 4,
            total_tokens: 14
        }
    });

    assert.equal(app.usageLog.entries.length, 0);
    assert.equal(app.usageLog.total, 0);

    app.applyLiveLogEvent({
        seq: 11,
        type: 'usage.completed',
        data: {
            id: 'usage-fresh',
            request_id: 'req-fresh',
            model: 'gpt-test',
            provider: 'openai',
            input_tokens: 10,
            output_tokens: 4,
            total_tokens: 14
        }
    });

    assert.equal(app.usageLog.entries.length, 1);
    assert.equal(app.usageLog.entries[0].id, 'usage-fresh');
});

test('live reset asks normal REST endpoints to resync source of truth', () => {
    const app = createLiveLogsApp();

    app.applyLiveLogEvent({ seq: 8, type: 'reset' });

    assert.equal(app.liveLogsLastSeq, 8);
    assert.equal(app.fetchUsageCalls, 1);
    assert.equal(app.fetchAuditCalls, 1);
});
