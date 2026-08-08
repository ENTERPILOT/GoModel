import assert from "node:assert/strict";
import test from "node:test";

import {
  authenticationLoginURL,
  authenticationResponseMetadata,
  safeAuthenticationPath,
} from "../src/lib/stores/external-auth.js";

test("authenticationLoginURL preserves the current dashboard deep link", () => {
  assert.equal(
    authenticationLoginURL("/g/sso/login", {
      pathname: "/g/admin/dashboard/usage",
      search: "?window=7d",
    }),
    "/g/sso/login?return_to=%2Fg%2Fadmin%2Fdashboard%2Fusage%3Fwindow%3D7d",
  );
  assert.equal(
    authenticationLoginURL("/g/sso/login?prompt=login", {
      pathname: "/g/admin/dashboard",
      search: "",
    }),
    "/g/sso/login?prompt=login&return_to=%2Fg%2Fadmin%2Fdashboard",
  );
  assert.equal(
    authenticationLoginURL("/g/sso/login#provider", {
      pathname: "/g/admin/dashboard/usage",
      search: "?window=7d",
    }),
    "/g/sso/login?return_to=%2Fg%2Fadmin%2Fdashboard%2Fusage%3Fwindow%3D7d#provider",
  );
});

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

test("authenticationResponseMetadata clears inactive extension auth state", () => {
  const response = {
    headers: new Headers({
      "X-GoModel-Auth-Login": "/g/sso/login",
      "X-GoModel-Auth-Logout": "",
      "X-GoModel-Auth-User": "",
    }),
  };
  assert.deepEqual(authenticationResponseMetadata(response), {
    loginURL: "/g/sso/login",
    logoutURL: "",
    user: "",
  });
});
