import test from "node:test";
import assert from "node:assert/strict";

import {
  authKeyErrorMessage,
  authKeyUserPathValidationError,
  buildCreateAuthKeyPayload,
  defaultAuthKeyForm,
  labelChipStyle,
  labelColor,
  normalizeAuthKeyUserPath,
  parseAuthKeyLabels,
} from "../src/pages/auth-keys/authKeysLogic.js";

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

test("authKeyErrorMessage extracts the admin error payload message", () => {
  assert.equal(
    authKeyErrorMessage({ error: { message: "key vanished" } }, "fallback"),
    "key vanished",
  );
  assert.equal(authKeyErrorMessage({ error: "plain" }, "fallback"), "fallback");
  assert.equal(authKeyErrorMessage(null, "fallback"), "fallback");
  assert.equal(authKeyErrorMessage({}, "fallback"), "fallback");
});

test("labelColor is deterministic and feeds the chip style custom property", () => {
  assert.equal(labelColor("team-a"), labelColor("team-a"));
  assert.match(labelColor("team-a"), /^#[0-9a-f]{6}$/);
  assert.equal(labelChipStyle("x"), "--label-color: " + labelColor("x"));
});
