const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadLiveLogsModuleFactory() {
    const source = fs.readFileSync(path.join(__dirname, 'live-logs.js'), 'utf8');
    const context = {
        console,
        setTimeout,
        clearTimeout,
        TextDecoder,
        ReadableStream,
        window: {}
    };
    vm.createContext(context);
    vm.runInContext(source, context);
    return context.window.dashboardLiveLogsModule;
}

function createLiveLogsApp() {
    const factory = loadLiveLogsModuleFactory();
    return {
        auditLog: { entries: [], total: 0, limit: 25, offset: 0 },
        usageLog: { entries: [], total: 0, limit: 50, offset: 0 },
        auditSearch: '',
        auditMethod: '',
        auditStatusCode: '',
        auditStream: '',
        usageLogSearch: '',
        usageLogModel: '',
        usageLogProvider: '',
        usageLogUserPath: '',
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

test('live reset asks normal REST endpoints to resync source of truth', () => {
    const app = createLiveLogsApp();

    app.applyLiveLogEvent({ seq: 8, type: 'reset' });

    assert.equal(app.liveLogsLastSeq, 8);
    assert.equal(app.fetchUsageCalls, 1);
    assert.equal(app.fetchAuditCalls, 1);
});
