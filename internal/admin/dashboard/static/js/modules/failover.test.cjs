const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadFailoverModuleFactory(overrides = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'failover.js'), 'utf8');
    const window = {
        ...(overrides.window || {})
    };
    const context = {
        console,
        ...overrides.context,
        window
    };
    vm.createContext(context);
    vm.runInContext(source, context);
    return context.window.dashboardFailoverModule;
}

function createFailoverModule(overrides) {
    const factory = loadFailoverModuleFactory(overrides);
    const module = factory();
    module.qualifiedModelName = (row) => row.selector || row.provider_name + '/' + row.model.id;
    return module;
}

test('failover action button marks rows with enabled fallback mappings active', () => {
    const module = createFailoverModule();
    const row = {
        selector: 'openai/gpt-4o',
        display_name: 'gpt-4o',
        model: { id: 'gpt-4o' },
        provider_name: 'openai'
    };
    module.failoverRules = [
        { primary_model: 'openai/gpt-4o', fallback_models: ['anthropic/claude-3-5-sonnet'], enabled: true },
        { primary_model: 'openai/gpt-4o-mini', fallback_models: ['anthropic/claude-3-haiku'], enabled: false },
        { primary_model: 'openai/gpt-4.1', fallback_models: [], enabled: true }
    ];

    assert.equal(module.hasActiveFailoverMapping(row), true);
    assert.equal(module.failoverButtonClass(row), 'table-action-btn-failover-active');
    assert.equal(module.failoverButtonLabel(row), 'Edit failover for gpt-4o (active)');
    assert.equal(module.hasActiveFailoverMapping({ ...row, selector: 'openai/gpt-4o-mini' }), false);
    assert.equal(module.hasActiveFailoverMapping({ ...row, selector: 'openai/gpt-4.1' }), false);
    assert.equal(module.hasActiveFailoverMapping({ ...row, is_alias: true }), false);
});

test('fetchFailoverRules skips admin endpoint when failover is globally disabled', async() => {
    const module = createFailoverModule();
    module.failoverRules = [{ primary_model: 'openai/gpt-4o', fallback_models: ['anthropic/claude-3-5-sonnet'] }];
    module.failoverGeneratedRules = [{ primary_model: 'openai/gpt-4.1', fallback_models: ['anthropic/claude-3-haiku'] }];
    module.failoverError = 'stale error';
    module.failoverLoading = true;
    module.workflowRuntimeBooleanFlag = () => false;
    module.adminRequestOptions = () => {
        throw new Error('admin endpoint should not be called');
    };

    await module.fetchFailoverRules();

    assert.equal(module.failoverAvailable, false);
    assert.equal(module.failoverLoading, false);
    assert.equal(module.failoverError, '');
    assert.equal(Array.isArray(module.failoverRules), true);
    assert.equal(module.failoverRules.length, 0);
    assert.equal(Array.isArray(module.failoverGeneratedRules), true);
    assert.equal(module.failoverGeneratedRules.length, 0);
});

test('generateFailoverForForm requests suggestions for the modal primary model', async() => {
    const requests = [];
    const module = createFailoverModule({
        context: {
            fetch: async(url, request) => {
                requests.push({ url, request });
                return {
                    ok: true,
                    status: 200,
                    json: async() => [{
                        primary_model: 'openai/gpt-4o',
                        fallback_models: ['anthropic/claude-3-5-sonnet', 'gemini/gemini-2.5-pro'],
                        enabled: true
                    }]
                };
            }
        }
    });
    module.failoverForm.source = 'openai/gpt-4o';
    module.adminRequestOptions = (options) => ({ ...(options || {}), headers: {} });
    module.handleFetchResponse = () => true;
    module.focusFailoverEditor = () => {};

    await module.generateFailoverForForm();

    assert.equal(requests.length, 1);
    assert.equal(requests[0].url, '/admin/failover/generate');
    assert.equal(requests[0].request.method, 'POST');
    assert.deepEqual(JSON.parse(requests[0].request.body), { primary_model: 'openai/gpt-4o' });
    assert.equal(module.failoverForm.target_model, 'anthropic/claude-3-5-sonnet');
    assert.equal(module.failoverForm.targets.length, 1);
    assert.equal(module.failoverForm.targets[0].model, 'gemini/gemini-2.5-pro');
    assert.equal(module.failoverGenerating, false);
});

test('generateFailoverRules opens a preselected draft modal', async() => {
    const requests = [];
    const module = createFailoverModule({
        context: {
            fetch: async(url, request) => {
                requests.push({ url, request });
                return {
                    ok: true,
                    status: 200,
                    json: async() => [
                        {
                            primary_model: 'openai/gpt-4o',
                            fallback_models: ['anthropic/claude-3-5-sonnet'],
                            enabled: true
                        },
                        {
                            primary_model: 'openai/gpt-4.1',
                            fallback_models: ['gemini/gemini-2.5-pro'],
                            enabled: true
                        }
                    ]
                };
            }
        }
    });
    module.adminRequestOptions = (options) => ({ ...(options || {}), headers: {} });
    module.handleFetchResponse = () => true;
    module.renderIconsAfterUpdate = () => {};

    await module.generateFailoverRules();

    assert.equal(requests.length, 1);
    assert.equal(requests[0].url, '/admin/failover/generate');
    assert.equal(requests[0].request.method, 'POST');
    assert.equal(module.failoverDraftsOpen, true);
    assert.equal(module.failoverGeneratedRules.length, 2);
    assert.equal(module.failoverDraftSelected(module.failoverGeneratedRules[0]), true);
    assert.equal(module.failoverDraftSelected(module.failoverGeneratedRules[1]), true);
    assert.equal(module.failoverGenerating, false);
});

test('saveSelectedFailoverDrafts saves selected drafts only', async() => {
    const requests = [];
    let refreshed = false;
    const module = createFailoverModule({
        context: {
            fetch: async(url, request) => {
                requests.push({ url, request });
                return {
                    ok: true,
                    status: 200,
                    json: async() => ({})
                };
            }
        }
    });
    module.adminRequestOptions = (options) => ({ ...(options || {}), headers: {} });
    module.handleFetchResponse = () => true;
    module.fetchFailoverRules = async() => {
        refreshed = true;
    };
    module.failoverDraftsOpen = true;
    module.failoverGeneratedRules = [
        {
            primary_model: 'openai/gpt-4o',
            fallback_models: ['anthropic/claude-3-5-sonnet'],
            enabled: true
        },
        {
            primary_model: 'openai/gpt-4.1',
            fallback_models: ['gemini/gemini-2.5-pro'],
            enabled: true
        }
    ];
    module.selectAllFailoverDrafts(module.failoverGeneratedRules);
    module.setFailoverDraftSelected(module.failoverGeneratedRules[1], false);

    await module.saveSelectedFailoverDrafts();

    assert.equal(requests.length, 1);
    assert.equal(requests[0].url, '/admin/failover');
    assert.equal(requests[0].request.method, 'PUT');
    assert.deepEqual(JSON.parse(requests[0].request.body), {
        primary_model: 'openai/gpt-4o',
        fallback_models: ['anthropic/claude-3-5-sonnet'],
        enabled: true
    });
    assert.equal(refreshed, true);
    assert.equal(module.failoverDraftsOpen, false);
    assert.equal(module.failoverGeneratedRules.length, 0);
    assert.equal(Object.keys(module.failoverDraftSelections).length, 0);
});

test('failover draft filter counter and bulk toggle reflect generated drafts', () => {
    const module = createFailoverModule();
    module.failoverGeneratedRules = [
        {
            primary_model: 'openai/gpt-4o',
            fallback_models: ['anthropic/claude-3-5-sonnet'],
            enabled: true
        },
        {
            primary_model: 'openai/gpt-4.1',
            fallback_models: ['gemini/gemini-2.5-pro'],
            enabled: true
        },
        {
            primary_model: 'groq/llama-3.3-70b',
            fallback_models: ['openrouter/meta-llama/llama-3.3-70b-instruct'],
            enabled: true
        }
    ];
    module.selectAllFailoverDrafts(module.failoverGeneratedRules);

    assert.equal(module.failoverDraftCountLabel(), '3 / 3 selected');
    assert.equal(module.allFailoverDraftsSelected(), true);

    module.failoverDraftFilter = 'gemini';
    assert.equal(module.filteredFailoverDrafts().length, 1);
    assert.equal(module.failoverPrimaryModel(module.filteredFailoverDrafts()[0]), 'openai/gpt-4.1');

    module.toggleAllFailoverDrafts();
    assert.equal(module.selectedFailoverDraftCount(), 0);
    assert.equal(module.failoverDraftCountLabel(), '0 / 3 selected');
    assert.equal(module.filteredFailoverDrafts().length, 1);

    module.toggleAllFailoverDrafts();
    assert.equal(module.selectedFailoverDraftCount(), 3);
    assert.equal(module.allFailoverDraftsSelected(), true);
});
