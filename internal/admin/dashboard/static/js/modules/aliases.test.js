const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadAliasesModuleFactory() {
    const source = fs.readFileSync(path.join(__dirname, 'aliases.js'), 'utf8');
    const context = {
        window: {},
        console
    };
    vm.createContext(context);
    vm.runInContext(source, context);
    return context.window.dashboardAliasesModule;
}

function createAliasesModule() {
    const factory = loadAliasesModuleFactory();
    return factory();
}

test('filteredDisplayModels returns stable rows when filter is empty', () => {
    const module = createAliasesModule();
    module.models = [
        {
            provider_type: 'openai',
            model: {
                id: 'davinci-002',
                object: 'model',
                owned_by: 'openai',
                metadata: {
                    modes: ['chat'],
                    categories: ['text_generation']
                }
            }
        }
    ];
    module.aliases = [];
    module.aliasesAvailable = true;
    module.modelFilter = '';
    module.syncDisplayModels();

    const first = module.filteredDisplayModels;
    const second = module.filteredDisplayModels;

    assert.equal(first.length, 1);
    assert.strictEqual(second, first);
    assert.strictEqual(second[0], first[0]);
    assert.equal(first[0].key, 'model:openai/davinci-002');
});

test('qualifiedModelName prefers selector when available', () => {
    const module = createAliasesModule();
    const model = {
        selector: 'openrouter/openai/gpt-3.5-turbo',
        provider_name: 'openrouter',
        provider_type: 'openrouter',
        model: {
            id: 'openai/gpt-3.5-turbo',
            object: 'model',
            owned_by: 'openai'
        }
    };

    assert.equal(module.qualifiedModelName(model), 'openrouter/openai/gpt-3.5-turbo');
});
