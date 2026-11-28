import assert from "node:assert/strict";
import test from "node:test";

import {
  passwordLoginEnabled,
  sessionUsername,
  signInFailure,
} from "../src/lib/stores/password-login.js";

test("passwordLoginEnabled reads the injected runtime global", () => {
  assert.equal(passwordLoginEnabled({ GOMODEL_PASSWORD_LOGIN: true }), true);
  assert.equal(passwordLoginEnabled({ GOMODEL_PASSWORD_LOGIN: false }), false);
  // Only the boolean the Go handler writes turns the login on.
  assert.equal(passwordLoginEnabled({ GOMODEL_PASSWORD_LOGIN: "true" }), false);
  assert.equal(passwordLoginEnabled({}), false);
  assert.equal(passwordLoginEnabled(undefined), false);
});

test("signInFailure tells wrong credentials from a lockout", () => {
  assert.equal(signInFailure(401), "invalid");
  assert.equal(signInFailure(429), "throttled");
  for (const status of [400, 403, 404, 500, 503]) {
    assert.equal(signInFailure(status), "failed");
  }
});

test("sessionUsername returns the username of an active session only", () => {
  assert.equal(
    sessionUsername({ authenticated: true, username: "admin" }),
    "admin",
  );
  assert.equal(sessionUsername({ authenticated: true, username: " ops " }), "ops");
  assert.equal(sessionUsername({ authenticated: false, username: "admin" }), "");
  assert.equal(sessionUsername({ authenticated: "true", username: "admin" }), "");
  assert.equal(sessionUsername({ authenticated: true }), "");
  assert.equal(sessionUsername(null), "");
});
