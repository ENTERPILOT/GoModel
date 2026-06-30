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
