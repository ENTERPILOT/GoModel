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
    const window = overrides.window || {
        localStorage: createLocalStorage()
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

test('timestampTitle keeps the UTC timestamp without a duplicate prefix label', () => {
    const module = createTimezoneModule();
    module.formatTimestampUTC = (value) => value === '2026-01-15T23:30:00Z'
        ? '2026-01-15 23:30:00 UTC'
        : '';

    assert.equal(
        module.timestampTitle('2026-01-15T23:30:00Z'),
        '2026-01-15 23:30:00 UTC'
    );
});

test('formatTimestampInTimeZone accepts a zero epoch timestamp', () => {
    const module = createTimezoneModule();

    assert.equal(
        module.formatTimestampInTimeZone(0, 'UTC'),
        '1970-01-01 00:00:00'
    );
});

test('loadTimezonePreference ignores localStorage accessor failures', () => {
    const window = {};
    Object.defineProperty(window, 'localStorage', {
        get() {
            throw new Error('storage denied');
        }
    });

    const module = createTimezoneModule({ window });
    module.detectedTimezone = 'Europe/Warsaw';

    assert.doesNotThrow(() => module.loadTimezonePreference());
    assert.equal(module.timezoneOverride, '');
});

test('saveTimezoneOverride and clearTimezoneOverride ignore localStorage method failures', () => {
    const module = createTimezoneModule({
        window: {
            localStorage: {
                getItem() {
                    return null;
                },
                setItem() {
                    throw new Error('write denied');
                },
                removeItem() {
                    throw new Error('remove denied');
                }
            }
        }
    });
    module.refreshTimeZoneData = () => {};

    module.timezoneOverride = 'Pacific/Chatham';
    assert.doesNotThrow(() => module.saveTimezoneOverride());
    assert.equal(module.timezoneOverride, 'Pacific/Chatham');

    assert.doesNotThrow(() => module.clearTimezoneOverride());
    assert.equal(module.timezoneOverride, '');
});

test('syncTimezoneOverrideSelectValue reapplies the saved override after options render', () => {
    const module = createTimezoneModule();
    const select = createFakeSelect(['']);

    module.$refs = { timezoneOverrideSelect: select };
    module.timezoneOverride = 'America/New_York';

    module.syncTimezoneOverrideSelectValue();
    assert.equal(select.value, '');

    select.options.push({ value: 'America/New_York' });
    module.syncTimezoneOverrideSelectValue();

    assert.equal(select.value, 'America/New_York');
});

test('dateKeyInTimeZone reuses timezone support checks and formatters for repeated calls', () => {
    let formatterConstructions = 0;

    function FakeDateTimeFormat(_locale, options = {}) {
        formatterConstructions++;
        this.options = options;
    }

    FakeDateTimeFormat.prototype.format = function() {
        return 'ok';
    };

    FakeDateTimeFormat.prototype.formatToParts = function() {
        if (this.options.timeZoneName === 'longOffset') {
            return [{ type: 'timeZoneName', value: 'GMT+01:00' }];
        }

        return [
            { type: 'year', value: '2026' },
            { type: 'literal', value: '-' },
            { type: 'month', value: '01' },
            { type: 'literal', value: '-' },
            { type: 'day', value: '16' }
        ];
    };

    FakeDateTimeFormat.prototype.resolvedOptions = function() {
        return { timeZone: 'Europe/Warsaw' };
    };

    const module = createTimezoneModule({
        Intl: {
            DateTimeFormat: FakeDateTimeFormat
        }
    });

    for (let i = 0; i < 5; i++) {
        assert.equal(
            module.dateKeyInTimeZone(new Date('2026-01-15T23:30:00Z'), 'Europe/Warsaw'),
            '2026-01-16'
        );
    }

    assert.equal(formatterConstructions, 2);
});
