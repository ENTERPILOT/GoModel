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
    assert.equal(m.calendarLevel(65535, 65535), 10);
    assert.equal(m.calendarLevel(50, 50), 10);
    assert.equal(m.calendarLevel(1, 1), 10);
});

// With the power scale (exponent 0.7) spread across 10 buckets and max = 65535,
// the bucket boundaries land at value = 65535 * (k/10)^(1/0.7) for k = 1..10
// (~2442, 6579, 11737, 17699, 24350, 31593, 39373, 47650, 56378, 65535). We
// probe inside each band and just across a few boundaries, avoiding the exact
// boundary where float rounding is ambiguous.
test('calendarLevel buckets values into power-scaled bands relative to max', () => {
    const m = createCalendarModule();
    const MAX = 65535;
    const cases = [
        { value: 1, level: 1 },
        { value: 100, level: 1 },
        { value: 2000, level: 1 },   // just below the level-1/2 boundary (~2442)
        { value: 3000, level: 2 },   // just above it
        { value: 6000, level: 2 },
        { value: 7000, level: 3 },   // just above the level-2/3 boundary (~6579)
        { value: 11000, level: 3 },  // just below the level-3/4 boundary (~11737)
        { value: 12000, level: 4 },  // just above it
        { value: 25000, level: 6 },
        { value: 35000, level: 7 },
        { value: 45000, level: 8 },
        { value: 55000, level: 9 },
        { value: 60000, level: 10 },
        { value: 65535, level: 10 }, // the max day
    ];
    for (const c of cases) {
        assert.equal(m.calendarLevel(c.value, MAX), c.level, `value ${c.value}`);
    }
});

test('calendarLegendLevels covers level 0 through the max level', () => {
    const m = createCalendarModule();
    const levels = m.calendarLegendLevels();
    assert.equal(levels[0], 0, 'starts at the empty level');
    assert.equal(levels[levels.length - 1], 10, 'ends at the darkest level');
    // Every active level must have a matching swatch so the legend mirrors the grid.
    for (let v = 1; v <= 10; v++) {
        assert.ok(levels.includes(v), `legend includes level ${v}`);
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
        { date: olderKey, total_tokens: 100 },         // far smaller, a tiny fraction of max
    ];

    const days = flattenDays(m.buildCalendarGrid());
    const busiest = days.find((d) => d.dateStr === '2026-06-23');
    const quiet = days.find((d) => d.dateStr === olderKey);
    const empties = days.filter((d) => d.value === 0);

    assert.equal(busiest.level, 10, 'busiest day is darkest');
    assert.equal(quiet.level, 1, 'a far smaller day reads lighter, not identical');
    assert.ok(empties.length > 0, 'the year window contains days with no usage');
    assert.ok(empties.every((d) => d.level === 0), 'no-usage days have level 0');
});

test('buildCalendarGrid lays out weeks Sunday-first to match the day labels', () => {
    const m = withCalendarDates(createCalendarModule(), '2026-06-23');
    m.calendarMode = 'tokens';
    m.calendarData = [];

    const firstWeek = m.buildCalendarGrid()[0];
    // Day labels are [_, Mon, _, Wed, _, Fri, _], so the top row must be Sunday:
    // row index r holds weekday r (Sun=0, Mon=1, ... Sat=6).
    for (let row = 0; row < 7; row++) {
        const cell = firstWeek[row];
        assert.ok(!cell.empty, `first week row ${row} should be a real day`);
        const weekday = new Date(cell.dateStr + 'T00:00:00Z').getUTCDay();
        assert.equal(weekday, row, `row ${row} should be weekday ${row} (got ${cell.dateStr})`);
    }
});

test('buildCalendarGrid applies the same power scaling to the costs view', () => {
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

    assert.equal(busiest.level, 10, 'highest-cost day is darkest');
    assert.equal(quiet.level, 1, 'a tiny-cost day stays lightest even with a huge token count');
});
