const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadGuardrailsModuleFactory(overrides = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'guardrails.js'), 'utf8');
    const window = {
        ...(overrides.window || {})
    };
    const context = {
        window,
        console,
        ...overrides
    };
    vm.createContext(context);
    vm.runInContext(source, context);
    return context.window.dashboardGuardrailsModule;
}

function createGuardrailsModule(overrides) {
    const factory = loadGuardrailsModuleFactory(overrides);
    return factory();
}

function createFakeSelect(values) {
    const select = {
        options: values.map((value) => ({ value: value })),
        _value: '',
        set value(nextValue) {
            this._value = this.options.some((option) => option.value === nextValue) ? nextValue : '';
        },
        get value() {
            return this._value;
        }
    };

    return select;
}

test('defaultGuardrailForm uses the first available type defaults', () => {
    const module = createGuardrailsModule();
    module.guardrailTypes = [
        {
            type: 'system_prompt',
            defaults: { mode: 'inject', content: '' },
            fields: []
        }
    ];

    const form = module.defaultGuardrailForm();

    assert.equal(form.type, 'system_prompt');
    assert.equal(form.user_path, '');
    assert.equal(JSON.stringify(form.config), JSON.stringify({ mode: 'inject', content: '' }));
});

test('defaultGuardrailForm includes the built-in llm_based_altering prompt', () => {
    const module = createGuardrailsModule();
    module.guardrailTypes = [
        {
            type: 'llm_based_altering',
            defaults: {
                model: '',
                prompt: 'built-in prompt',
                roles: ['user'],
                max_tokens: 4096
            },
            fields: []
        }
    ];

    const form = module.defaultGuardrailForm();

    assert.equal(form.type, 'llm_based_altering');
    assert.equal(
        JSON.stringify(form.config),
        JSON.stringify({ model: '', prompt: 'built-in prompt', roles: ['user'], max_tokens: 4096 })
    );
});

test('normalizeGuardrailConfig merges stored config over type defaults', () => {
    const module = createGuardrailsModule();
    module.guardrailTypes = [
        {
            type: 'system_prompt',
            defaults: { mode: 'inject', content: '' },
            fields: []
        }
    ];

    const config = module.normalizeGuardrailConfig({ content: 'be careful' }, 'system_prompt');

    assert.equal(JSON.stringify(config), JSON.stringify({ mode: 'inject', content: 'be careful' }));
});

test('normalizeGuardrailConfig fills the built-in llm_based_altering prompt for existing instances', () => {
    const module = createGuardrailsModule();
    module.guardrailTypes = [
        {
            type: 'llm_based_altering',
            defaults: {
                model: '',
                prompt: 'built-in prompt',
                roles: ['user'],
                max_tokens: 4096
            },
            fields: []
        }
    ];

    const config = module.normalizeGuardrailConfig({ model: 'openai/gpt-4o-mini' }, 'llm_based_altering');

    assert.equal(
        JSON.stringify(config),
        JSON.stringify({
            model: 'openai/gpt-4o-mini',
            prompt: 'built-in prompt',
            roles: ['user'],
            max_tokens: 4096
        })
    );
});

test('normalizeGuardrailConfig returns the input config for unknown types', () => {
    const module = createGuardrailsModule();
    module.guardrailTypes = [
        {
            type: 'system_prompt',
            defaults: { mode: 'inject', content: '' },
            fields: []
        }
    ];

    const config = module.normalizeGuardrailConfig({ content: 'test' }, 'unknown_type');

    assert.equal(JSON.stringify(config), JSON.stringify({ content: 'test' }));
});

test('filteredGuardrails matches user_path values', () => {
    const module = createGuardrailsModule();
    module.guardrails = [
        { name: 'policy', type: 'system_prompt', user_path: '/team/alpha', summary: 'be careful' }
    ];
    module.guardrailFilter = 'alpha';

    assert.equal(module.filteredGuardrails.length, 1);
    assert.equal(module.filteredGuardrails[0].name, 'policy');
});

test('checkbox guardrail fields normalize and toggle array values', () => {
    const module = createGuardrailsModule();
    module.guardrailForm = {
        name: 'privacy',
        type: 'llm_based_altering',
        description: '',
        user_path: '',
        config: {
            roles: ['user']
        }
    };

    const field = { key: 'roles', input: 'checkboxes' };

    assert.equal(JSON.stringify(module.guardrailFieldValue(field)), JSON.stringify(['user']));
    assert.equal(module.guardrailArrayFieldSelected(field, 'user'), true);
    assert.equal(module.guardrailArrayFieldSelected(field, 'tool'), false);

    module.toggleGuardrailArrayFieldValue(field, 'tool', true);
    assert.equal(JSON.stringify(module.guardrailForm.config.roles), JSON.stringify(['user', 'tool']));

    module.toggleGuardrailArrayFieldValue(field, 'user', false);
    assert.equal(JSON.stringify(module.guardrailForm.config.roles), JSON.stringify(['tool']));
});

test('syncGuardrailTypeSelectValue reapplies the current type after options render', () => {
    const module = createGuardrailsModule();
    const select = createFakeSelect(['']);

    module.$refs = { guardrailTypeSelect: select };
    module.guardrailForm = {
        name: '',
        type: 'llm_based_altering',
        description: '',
        user_path: '',
        config: {}
    };

    module.syncGuardrailTypeSelectValue();
    assert.equal(select.value, '');

    module.guardrailTypes = [
        {
            type: 'llm_based_altering',
            defaults: { model: '', roles: ['user'], max_tokens: 4096 },
            fields: []
        }
    ];
    select.options.push({ value: 'llm_based_altering' });
    module.syncGuardrailTypeSelectValue();

    assert.equal(select.value, 'llm_based_altering');
});

test('submitGuardrailForm logs non-auth HTTP failures before surfacing the UI error', async () => {
    const errors = [];
    const module = createGuardrailsModule({
        console: {
            error(...args) {
                errors.push(args.join(' '));
            }
        },
        fetch: async () => ({
            status: 400,
            statusText: 'Bad Request',
            async json() {
                return {
                    error: {
                        message: 'system_prompt content is required'
                    }
                };
            }
        })
    });

    module.headers = () => ({ 'Content-Type': 'application/json' });
    module.guardrailForm = {
        name: 'privacy',
        type: 'system_prompt',
        description: '',
        user_path: '',
        config: {}
    };

    await module.submitGuardrailForm();

    assert.equal(module.guardrailError, 'system_prompt content is required');
    assert.equal(errors.length, 1);
    assert.match(errors[0], /Failed to save guardrail: 400 Bad Request system_prompt content is required/);
});
