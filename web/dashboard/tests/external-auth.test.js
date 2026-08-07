import assert from "node:assert/strict";
import test from "node:test";

import {
  authenticationResponseMetadata,
  safeAuthenticationPath,
} from "../src/lib/stores/external-auth.js";

test("safeAuthenticationPath accepts only app-local paths", () => {
  assert.equal(safeAuthenticationPath("/g/sso/login"), "/g/sso/login");
  assert.equal(safeAuthenticationPath("https://evil.example/login"), "");
  assert.equal(safeAuthenticationPath("//evil.example/login"), "");
  assert.equal(safeAuthenticationPath("/g\\evil"), "");
});

test("authenticationResponseMetadata reads extension auth headers", () => {
  const response = {
    headers: new Headers({
      "X-GoModel-Auth-Login": "/g/sso/login",
      "X-GoModel-Auth-Logout": "/g/sso/logout",
      "X-GoModel-Auth-User": "/users/person@example.com",
    }),
  };
  assert.deepEqual(authenticationResponseMetadata(response), {
    loginURL: "/g/sso/login",
    logoutURL: "/g/sso/logout",
    user: "/users/person@example.com",
  });
});
