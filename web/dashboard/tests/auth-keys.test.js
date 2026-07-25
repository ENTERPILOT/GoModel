import test from "node:test";
import assert from "node:assert/strict";

import {
  authKeyActive,
  authKeyDeactivated,
  authKeyExpired,
  authKeyUserPathValidationError,
  buildCreateAuthKeyPayload,
  countInactiveAuthKeys,
  defaultAuthKeyForm,
  filterAuthKeys,
  labelChipStyle,
  labelColor,
  normalizeAuthKeyUserPath,
  parseAuthKeyLabels,
  sortAuthKeys,
} from "../src/pages/auth-keys/authKeysLogic.js";

const NOW = Date.parse("2026-07-25T12:00:00Z");

function authKey(overrides) {
  return {
    id: "k1",
    name: "ci-deploy",
    description: "",
    user_path: "",
    labels: [],
    redacted_value: "sk_gom_...abcd",
    enabled: true,
    active: true,
    ...overrides,
  };
}

test("buildCreateAuthKeyPayload serializes date-only expirations to the end of the selected UTC day", () => {
  const { payload, error } = buildCreateAuthKeyPayload({
    ...defaultAuthKeyForm(),
    name: "ci-deploy",
    expires_at: "2026-04-01",
  });
  assert.equal(error, undefined);
  assert.equal(payload.expires_at, "2026-04-01T23:59:59Z");
});

test("buildCreateAuthKeyPayload omits expires_at when no date is selected", () => {
  const { payload } = buildCreateAuthKeyPayload({
    ...defaultAuthKeyForm(),
    name: "ci-deploy",
  });
  assert.equal("expires_at" in payload, false);
});

test("buildCreateAuthKeyPayload sends dashboard_access only when granted", () => {
  const granted = buildCreateAuthKeyPayload({
    ...defaultAuthKeyForm(),
    name: "ops",
    dashboard_access: true,
  });
  assert.equal(granted.payload.dashboard_access, true);

  const denied = buildCreateAuthKeyPayload({
    ...defaultAuthKeyForm(),
    name: "ci-deploy",
  });
  assert.equal(denied.payload.dashboard_access, undefined);
});

test("buildCreateAuthKeyPayload normalizes user paths before sending them", () => {
  const { payload, error } = buildCreateAuthKeyPayload({
    ...defaultAuthKeyForm(),
    name: "ci-deploy",
    user_path: " team//alpha/service/ ",
  });
  assert.equal(error, undefined);
  assert.equal(payload.user_path, "/team/alpha/service");
});

test("buildCreateAuthKeyPayload parses comma-separated labels and omits them when empty", () => {
  const withLabels = buildCreateAuthKeyPayload({
    ...defaultAuthKeyForm(),
    name: "ci-deploy",
    labels: " team-a , batch,, team-a ",
  });
  assert.deepEqual(withLabels.payload.labels, ["team-a", "batch"]);

  const noLabels = buildCreateAuthKeyPayload({
    ...defaultAuthKeyForm(),
    name: "ci-deploy",
    labels: " , ",
  });
  assert.equal(noLabels.payload.labels, undefined);
});

test("buildCreateAuthKeyPayload trims the description and omits empty optional fields", () => {
  const { payload } = buildCreateAuthKeyPayload({
    ...defaultAuthKeyForm(),
    name: " ci-deploy ",
    description: "  deploy key  ",
  });
  assert.equal(payload.name, "ci-deploy");
  assert.equal(payload.description, "deploy key");
  assert.equal(payload.user_path, undefined);
  assert.equal(payload.labels, undefined);
});

test("buildCreateAuthKeyPayload rejects a missing name", () => {
  const { payload, error } = buildCreateAuthKeyPayload({
    ...defaultAuthKeyForm(),
    name: "   ",
  });
  assert.equal(payload, undefined);
  assert.equal(error, "Name is required.");
});

test("buildCreateAuthKeyPayload rejects invalid user paths before sending the request", () => {
  const { payload, error } = buildCreateAuthKeyPayload({
    ...defaultAuthKeyForm(),
    name: "ci-deploy",
    user_path: "/team/../alpha",
  });
  assert.equal(payload, undefined);
  assert.equal(error, 'User path cannot contain "." or ".." segments.');
});

test("authKeyUserPathValidationError flags dot and colon segments only", () => {
  assert.equal(authKeyUserPathValidationError(""), "");
  assert.equal(authKeyUserPathValidationError("/team/alpha"), "");
  assert.equal(
    authKeyUserPathValidationError("/team/./alpha"),
    'User path cannot contain "." or ".." segments.',
  );
  assert.equal(
    authKeyUserPathValidationError("team/a:b"),
    'User path cannot contain ":" segments.',
  );
});

test("normalizeAuthKeyUserPath canonicalizes paths and drops invalid ones", () => {
  assert.equal(normalizeAuthKeyUserPath(""), "");
  assert.equal(normalizeAuthKeyUserPath("team-a"), "/team-a");
  assert.equal(normalizeAuthKeyUserPath(" /team / alpha "), "/team/alpha");
  assert.equal(normalizeAuthKeyUserPath(" team//alpha/service/ "), "/team/alpha/service");
  assert.equal(normalizeAuthKeyUserPath("/team/../alpha"), "");
});

test("parseAuthKeyLabels trims, de-duplicates, and preserves order", () => {
  assert.deepEqual(parseAuthKeyLabels(" team-a , batch,, team-a "), ["team-a", "batch"]);
  assert.deepEqual(parseAuthKeyLabels(" , "), []);
  assert.deepEqual(parseAuthKeyLabels(""), []);
  assert.deepEqual(parseAuthKeyLabels(null), []);
});


test("authKeyExpired only fires for a parseable expiration in the past", () => {
  assert.equal(authKeyExpired(authKey(), NOW), false);
  assert.equal(authKeyExpired(authKey({ expires_at: "2026-07-24T23:59:59Z" }), NOW), true);
  assert.equal(authKeyExpired(authKey({ expires_at: "2026-07-26T23:59:59Z" }), NOW), false);
  assert.equal(authKeyExpired(authKey({ expires_at: "not-a-date" }), NOW), false);
});

test("authKeyDeactivated distinguishes deactivation from expiration", () => {
  assert.equal(authKeyDeactivated(authKey({ expires_at: "2020-01-01T00:00:00Z", active: false })), false);
  assert.equal(authKeyDeactivated(authKey({ deactivated_at: "2026-07-01T00:00:00Z", active: false })), true);
  assert.equal(authKeyDeactivated(authKey({ enabled: false })), true);
});

test("authKeyActive re-checks expiration locally on top of the backend flag", () => {
  assert.equal(authKeyActive(authKey(), NOW), true);
  assert.equal(authKeyActive(authKey({ active: false }), NOW), false);
  // Expired since the list was fetched, so the backend still reports active.
  assert.equal(authKeyActive(authKey({ active: true, expires_at: "2026-07-25T11:00:00Z" }), NOW), false);
});

test("filterAuthKeys hides inactive keys unless they are requested", () => {
  const keys = [
    authKey({ id: "live", name: "live" }),
    authKey({ id: "dead", name: "dead", active: false, deactivated_at: "2026-07-01T00:00:00Z" }),
    authKey({ id: "old", name: "old", active: false, expires_at: "2026-07-01T00:00:00Z" }),
  ];
  assert.deepEqual(
    filterAuthKeys(keys, { now: NOW }).map((k) => k.id),
    ["live"],
  );
  assert.deepEqual(
    filterAuthKeys(keys, { showInactive: true, now: NOW }).map((k) => k.id),
    ["live", "dead", "old"],
  );
  assert.equal(countInactiveAuthKeys(keys, NOW), 2);
});

test("filterAuthKeys matches the query case-insensitively across searchable fields", () => {
  const keys = [
    authKey({ id: "a", name: "ci-deploy", labels: ["team-a"] }),
    authKey({ id: "b", name: "ops", description: "On-call rotation" }),
    authKey({ id: "c", name: "batch", user_path: "/team/alpha" }),
    authKey({ id: "d", name: "tokened", redacted_value: "sk_gom_...wxyz" }),
  ];
  const ids = (query) => filterAuthKeys(keys, { query, now: NOW }).map((k) => k.id);
  assert.deepEqual(ids("TEAM-A"), ["a"]);
  assert.deepEqual(ids("on-call"), ["b"]);
  assert.deepEqual(ids("/team/alpha"), ["c"]);
  assert.deepEqual(ids("wxyz"), ["d"]);
  assert.deepEqual(ids("  "), ["a", "b", "c", "d"]);
  assert.deepEqual(ids("nothing"), []);
});

test("sortAuthKeys puts live keys first by remaining lifetime, deactivated last", () => {
  const keys = [
    authKey({ id: "1", name: "expires-soon", expires_at: "2026-07-26T00:00:00Z" }),
    authKey({ id: "2", name: "deactivated-old", active: false, deactivated_at: "2026-01-01T00:00:00Z" }),
    authKey({ id: "3", name: "never-expires" }),
    authKey({ id: "4", name: "expired", active: false, expires_at: "2026-07-01T00:00:00Z" }),
    authKey({ id: "5", name: "expires-later", expires_at: "2027-01-01T00:00:00Z" }),
    authKey({ id: "6", name: "deactivated-recent", active: false, deactivated_at: "2026-07-01T00:00:00Z" }),
    authKey({ id: "7", name: "expired-earlier", active: false, expires_at: "2026-02-01T00:00:00Z" }),
  ];
  assert.deepEqual(
    sortAuthKeys(keys, NOW).map((k) => k.name),
    [
      "never-expires",
      "expires-later",
      "expires-soon",
      "expired",
      "expired-earlier",
      "deactivated-recent",
      "deactivated-old",
    ],
  );
});

test("sortAuthKeys breaks ties by name and leaves the input untouched", () => {
  const keys = [authKey({ id: "1", name: "zeta" }), authKey({ id: "2", name: "alpha" })];
  assert.deepEqual(sortAuthKeys(keys, NOW).map((k) => k.name), ["alpha", "zeta"]);
  assert.deepEqual(keys.map((k) => k.name), ["zeta", "alpha"]);
  assert.deepEqual(sortAuthKeys(undefined, NOW), []);
});

test("filterAuthKeys tolerates a missing list", () => {
  assert.deepEqual(filterAuthKeys(undefined, { now: NOW }), []);
  assert.equal(countInactiveAuthKeys(null, NOW), 0);
});

test("labelColor is deterministic and feeds the chip style custom property", () => {
  assert.equal(labelColor("team-a"), labelColor("team-a"));
  assert.match(labelColor("team-a"), /^#[0-9a-f]{6}$/);
  assert.equal(labelChipStyle("x"), "--label-color: " + labelColor("x"));
});
