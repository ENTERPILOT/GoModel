const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function loadCalendarModuleFactory() {
    const source = fs.readFileSync(path.join(__dirname, 'contribution-calendar.js'), 'utf8');
    const window = {};
    const context = { console, window };
    vm.createContext(context);
    vm.runInContext(source, context);
    return context.window.dashboardContributionCalendarModule;
}

function createCalendarModule() {
    return loadCalendarModuleFactory()();
}

function pad(n) {
    return n < 10 ? '0' + n : '' + n;
}

// Mirror the UTC date helpers from timezone.js so buildCalendarGrid can run
// deterministically without a real clock.
function withCalendarDates(module, todayKey) {
    module.currentDateKey = () => todayKey;
    module.dateKeyToDate = (key) => {
        const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(key);
        return m ? new Date(Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]))) : null;
    };
    // Duck-type the Date check rather than `instanceof Date`: buildCalendarGrid
    // builds dates with the vm sandbox's Date, which is a different realm than
    // this test file's Date, so `instanceof` would never match.
    module.dateToDateKey = (date) =>
        (date && typeof date.getTime === 'function' && !Number.isNaN(date.getTime()))
            ? date.getUTCFullYear() + '-' + pad(date.getUTCMonth() + 1) + '-' + pad(date.getUTCDate())
            : '';
    module.addDaysToDateKey = (key, days) => {
        const d = module.dateKeyToDate(key);
        if (!d) return '';
        d.setUTCDate(d.getUTCDate() + days);
        return module.dateToDateKey(d);
    };
    return module;
}

function flattenDays(weeks) {
    return weeks.flat().filter((d) => !d.empty);
}

test('calendarLevel returns 0 for empty, negative, or maxless input', () => {
    const m = createCalendarModule();
    assert.equal(m.calendarLevel(0, 1000), 0);
    assert.equal(m.calendarLevel(-5, 1000), 0);
    assert.equal(m.calendarLevel(500, 0), 0, 'no busiest day yet -> level 0');
    assert.equal(m.calendarLevel(0, 0), 0);
});

test('calendarLevel puts the busiest day at the darkest level', () => {
    const m = createCalendarModule();
    assert.equal(m.calendarLevel(65535, 65535), 4);
    assert.equal(m.calendarLevel(50, 50), 4);
    assert.equal(m.calendarLevel(1, 1), 4);
});

// With max = 65535 (= 16^4 - 1) the log cutoffs 0.25/0.5/0.75 land on value
// boundaries 15, 255, and 4095. We probe inside each band and just across each
// boundary, but avoid the exact integer at the cutoff (where the ratio equals
// the cutoff and float rounding is ambiguous).
test('calendarLevel buckets values into log-scaled bands relative to max', () => {
    const m = createCalendarModule();
    const MAX = 65535;
    const cases = [
        { value: 5, level: 1 },
        { value: 14, level: 1 },    // just below the 0.25 cutoff (boundary at 15)
        { value: 16, level: 2 },    // just above the 0.25 cutoff
        { value: 100, level: 2 },
        { value: 254, level: 2 },   // just below the 0.5 cutoff (boundary at 255)
        { value: 256, level: 3 },   // just above the 0.5 cutoff
        { value: 1000, level: 3 },
        { value: 4094, level: 3 },  // just below the 0.75 cutoff (boundary at 4095)
        { value: 4096, level: 4 },  // just above the 0.75 cutoff
        { value: 30000, level: 4 },
        { value: 65535, level: 4 }, // the max day
    ];
    for (const c of cases) {
        assert.equal(m.calendarLevel(c.value, MAX), c.level, `value ${c.value}`);
    }
});

test('calendarLevel never decreases as value grows toward max', () => {
    const m = createCalendarModule();
    const max = 1000000;
    let prev = 0;
    for (const v of [0, 1, 10, 100, 1000, 10000, 100000, 1000000]) {
        const level = m.calendarLevel(v, max);
        assert.ok(level >= prev, `level for ${v} (${level}) should be >= ${prev}`);
        prev = level;
    }
});

test('buildCalendarGrid scales every day against the busiest day in the whole period', () => {
    const m = withCalendarDates(createCalendarModule(), '2026-06-23');
    const olderKey = m.addDaysToDateKey('2026-06-23', -100);
    m.calendarMode = 'tokens';
    m.calendarData = [
        { date: '2026-06-23', total_tokens: 1000000 }, // busiest day in the year
        { date: olderKey, total_tokens: 100 },         // far smaller, ~0.33 of max in log space
    ];

    const days = flattenDays(m.buildCalendarGrid());
    const busiest = days.find((d) => d.dateStr === '2026-06-23');
    const quiet = days.find((d) => d.dateStr === olderKey);
    const empties = days.filter((d) => d.value === 0);

    assert.equal(busiest.level, 4, 'busiest day is darkest');
    assert.equal(quiet.level, 2, 'a far smaller day reads lighter, not identical');
    assert.ok(empties.length > 0, 'the year window contains days with no usage');
    assert.ok(empties.every((d) => d.level === 0), 'no-usage days have level 0');
});

test('buildCalendarGrid applies the same log scaling to the costs view', () => {
    const m = withCalendarDates(createCalendarModule(), '2026-06-23');
    const olderKey = m.addDaysToDateKey('2026-06-23', -100);
    m.calendarMode = 'costs';
    m.calendarData = [
        // total_tokens is inverted vs. total_cost to prove costs mode ignores tokens.
        { date: '2026-06-23', total_cost: 50, total_tokens: 1 },
        { date: olderKey, total_cost: 0.5, total_tokens: 9999999 },
    ];

    const days = flattenDays(m.buildCalendarGrid());
    const busiest = days.find((d) => d.dateStr === '2026-06-23');
    const quiet = days.find((d) => d.dateStr === olderKey);

    assert.equal(busiest.level, 4, 'highest-cost day is darkest');
    assert.equal(quiet.level, 1, 'a tiny-cost day stays lightest even with a huge token count');
});
