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

function loadDashboardApp() {
    const timezoneSource = fs.readFileSync(path.join(__dirname, 'timezone.js'), 'utf8');
    const dashboardSource = fs.readFileSync(path.join(__dirname, '../dashboard.js'), 'utf8');

    const window = {
        localStorage: createLocalStorage(),
        location: { pathname: '/admin/dashboard/overview' },
        matchMedia() {
            return { addEventListener() {} };
        },
        addEventListener() {}
    };

    const context = {
        console,
        Date,
        Intl,
        setTimeout,
        clearTimeout,
        requestAnimationFrame(callback) {
            callback();
        },
        history: { pushState() {} },
        document: {
            documentElement: {
                removeAttribute() {},
                setAttribute() {}
            },
            getElementById() {
                return null;
            }
        },
        getComputedStyle() {
            return {
                getPropertyValue() {
                    return '';
                }
            };
        },
        window
    };

    vm.createContext(context);
    vm.runInContext(timezoneSource, context);
    vm.runInContext(dashboardSource, context);
    return context.dashboard();
}

function formatInZone(ts, zone) {
    const parts = new Intl.DateTimeFormat('en-CA', {
        timeZone: zone,
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
        hourCycle: 'h23'
    }).formatToParts(new Date(ts));

    const byType = {};
    parts.forEach((part) => {
        byType[part.type] = part.value;
    });

    return byType.year + '-' + byType.month + '-' + byType.day + ' ' +
        byType.hour + ':' + byType.minute + ':' + byType.second;
}

test('formatTimestamp uses the effective timezone override for dashboard rows', () => {
    const app = loadDashboardApp();
    app.detectedTimezone = 'Europe/Warsaw';
    app.timezoneOverride = 'America/New_York';

    const timestamp = '2026-01-15T23:30:00Z';

    assert.equal(
        app.formatTimestamp(timestamp),
        formatInZone(timestamp, 'America/New_York')
    );
});

test('formatDateUTC keeps expiration dates on their stored UTC calendar day', () => {
    const app = loadDashboardApp();
    app.detectedTimezone = 'Europe/Warsaw';
    app.timezoneOverride = 'America/Los_Angeles';

    assert.equal(
        app.formatDateUTC('2026-04-01T23:59:59Z'),
        '2026-04-01'
    );
});
