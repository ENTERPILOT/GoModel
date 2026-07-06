const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Loads the drawer together with the real live-logs module so live-state
// helpers (auditEntryLiveDetailPending, liveAuditStateSettled, …) behave
// exactly as in production instead of via drifting stubs.
function loadDrawerWindow() {
    const context = {
        console,
        setTimeout,
        clearTimeout,
        window: {},
        HTMLElement: class HTMLElement {},
        requestAnimationFrame: () => {},
        document: {
            activeElement: null,
            body: { classList: { add() {}, remove() {} } },
            contains() { return false; }
        }
    };
    vm.createContext(context);
    for (const file of ['conversation-helpers.js', 'live-logs.js', 'conversation-drawer.js']) {
        vm.runInContext(fs.readFileSync(path.join(__dirname, file), 'utf8'), context);
    }
    return context.window;
}

function createDrawerApp() {
    const win = loadDrawerWindow();
    return {
        ...win.dashboardLiveLogsModule(),
        ...win.dashboardConversationDrawerModule(),
        conversationOpen: false,
        conversationLoading: false,
        conversationError: '',
        conversationAnchorID: '',
        conversationEntries: [],
        conversationMessages: [],
        conversationRequestToken: 0,
        conversationReturnFocusEl: null,
        conversationLiveEntryId: '',
        bodyPointerStart: null,
        fetchCalls: [],
        fetchConversation(logID, token) {
            this.fetchCalls.push({ logID, token });
        }
    };
}

function liveEntry(overrides = {}) {
    return {
        id: 'audit-1',
        _live: true,
        _live_pending: true,
        _live_state: 'audit.updated',
        path: '/v1/chat/completions',
        timestamp: '2026-07-06T12:00:00Z',
        data: {
            request_body: { messages: [{ role: 'user', content: 'Hi' }] }
        },
        ...overrides
    };
}

test('openConversation renders live entries locally without a persisted fetch', async () => {
    const app = createDrawerApp();
    const entry = liveEntry();

    await app.openConversation(entry, null, false, null);

    assert.equal(app.conversationOpen, true);
    assert.equal(app.conversationLiveEntryId, 'audit-1');
    assert.equal(app.conversationLoading, false);
    assert.equal(app.fetchCalls.length, 0);
    assert.equal(app.conversationMessages.length, 1);
    assert.equal(app.conversationMessages[0].text, 'Hi');
    assert.equal(app.conversationLiveWaiting(), true);
    assert.equal(app.conversationLiveStatusText(), 'Model is responding…');
});

test('openConversation fetches persisted threads for non-live entries', async () => {
    const app = createDrawerApp();

    await app.openConversation({ id: 'audit-2', path: '/v1/chat/completions' }, null, false, null);

    assert.equal(app.conversationLiveEntryId, '');
    assert.equal(app.conversationLoading, true);
    assert.equal(app.fetchCalls.length, 1);
    assert.equal(app.fetchCalls[0].logID, 'audit-2');
});

test('refreshLiveConversation re-renders streaming chunks for the open entry', async () => {
    const app = createDrawerApp();
    await app.openConversation(liveEntry(), null, false, null);

    const streamed = liveEntry({
        _live_state: 'audit.stream',
        _response_partial: true,
        data: {
            request_body: { messages: [{ role: 'user', content: 'Hi' }] },
            response_body: { choices: [{ index: 0, message: { role: 'assistant', content: 'Par' } }] }
        }
    });
    app.refreshLiveConversation(streamed);

    assert.equal(app.conversationMessages.length, 2);
    assert.equal(app.conversationMessages[1].text, 'Par');
    assert.equal(app.conversationLiveWaiting(), true);

    // Events for other entries or a closed drawer are ignored.
    app.refreshLiveConversation(liveEntry({ id: 'audit-9' }));
    assert.equal(app.conversationMessages.length, 2);
});

test('refreshLiveConversation hydrates the persisted thread once the entry flushes', async () => {
    const app = createDrawerApp();
    await app.openConversation(liveEntry(), null, false, null);

    app.refreshLiveConversation(liveEntry({
        _live_state: 'audit.completed',
        _live_pending: true
    }));
    assert.equal(app.conversationLiveWaiting(), false, 'spinner stops once the response completed');
    assert.equal(app.fetchCalls.length, 0);

    app.refreshLiveConversation(liveEntry({
        _live_state: 'audit.flushed',
        _live_pending: false,
        _audit_flushed: true
    }));
    assert.equal(app.conversationLiveEntryId, '');
    assert.equal(app.fetchCalls.length, 1);
    assert.equal(app.fetchCalls[0].logID, 'audit-1');
});

test('closeConversation clears the live conversation binding', async () => {
    const app = createDrawerApp();
    await app.openConversation(liveEntry(), null, false, null);

    app.closeConversation();

    assert.equal(app.conversationOpen, false);
    assert.equal(app.conversationLiveEntryId, '');
    assert.equal(app.conversationLiveWaiting(), false);
});
