const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function createLocalStorage(seed = {}) {
    const data = new Map(Object.entries(seed));
    return {
        getItem(key) {
            return data.has(key) ? data.get(key) : null;
        },
        setItem(key, value) {
            data.set(key, String(value));
        },
        removeItem(key) {
            data.delete(key);
        }
    };
}

function loadTimezoneModuleFactory(overrides = {}) {
    const source = fs.readFileSync(path.join(__dirname, 'timezone.js'), 'utf8');
    const window = {
        localStorage: createLocalStorage(),
        ...(overrides.window || {})
    };
    const context = {
        console,
        Intl,
        Date,
        ...overrides,
        window
    };
    vm.createContext(context);
    vm.runInContext(source, context);
    return context.window.dashboardTimezoneModule;
}

function createTimezoneModule(overrides) {
    const factory = loadTimezoneModuleFactory(overrides);
    return factory();
}

test('dateKeyInTimeZone uses the configured IANA timezone boundary', () => {
    const module = createTimezoneModule();

    assert.equal(
        module.dateKeyInTimeZone(new Date('2026-01-15T23:30:00Z'), 'Europe/Warsaw'),
        '2026-01-16'
    );
});

test('loadTimezonePreference prefers the saved override over the detected browser timezone', () => {
    const module = createTimezoneModule({
        window: {
            localStorage: createLocalStorage({
                gomodel_timezone_override: 'America/New_York'
            })
        }
    });

    module.detectedTimezone = 'Europe/Warsaw';
    module.loadTimezonePreference();

    assert.equal(module.timezoneOverride, 'America/New_York');
    assert.equal(module.effectiveTimezone(), 'America/New_York');
});

test('timeZoneOptionLabel includes the IANA name and UTC offset', () => {
    const module = createTimezoneModule();

    assert.equal(
        module.timeZoneOptionLabel('Europe/Warsaw', new Date('2026-01-15T12:00:00Z')),
        'Europe/Warsaw (UTC+01:00)'
    );
});
