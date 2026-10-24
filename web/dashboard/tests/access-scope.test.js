import test from "node:test";
import assert from "node:assert/strict";

import {
  GLOBAL_ONLY_PAGES,
  normalizeScopePath,
  pageVisibleForScope,
  parseAccessScope,
  scopeAllows,
  scopedSubjectAllowed,
} from "../src/lib/stores/accessScope.js";

test("parseAccessScope treats anything but a scoped payload as global", () => {
  const global = { scope: "global", userPath: "" };
  assert.deepEqual(parseAccessScope(null), global);
  assert.deepEqual(parseAccessScope("nope"), global);
  assert.deepEqual(parseAccessScope([]), global);
  assert.deepEqual(parseAccessScope({}), global);
  assert.deepEqual(parseAccessScope({ scope: "global" }), global);
  assert.deepEqual(parseAccessScope({ scope: "user_path" }), global);
  assert.deepEqual(parseAccessScope({ scope: "user_path", user_path: "/" }), global);
  assert.deepEqual(parseAccessScope({ scope: "user_path", user_path: "/a/../b" }), global);
});

test("parseAccessScope canonicalizes a scoped payload", () => {
  assert.deepEqual(parseAccessScope({ scope: "user_path", user_path: "team/alpha/" }), {
    scope: "user_path",
    userPath: "/team/alpha",
  });
});

test("normalizeScopePath canonicalizes and rejects unsafe segments", () => {
  assert.equal(normalizeScopePath(""), "");
  assert.equal(normalizeScopePath("/"), "/");
  assert.equal(normalizeScopePath(" team / alpha "), "/team/alpha");
  assert.equal(normalizeScopePath("/team//alpha/"), "/team/alpha");
  assert.equal(normalizeScopePath("/team/../alpha"), "");
  assert.equal(normalizeScopePath("/team/a:b"), "");
});

test("scopeAllows mirrors the backend subtree rule", () => {
  assert.equal(scopeAllows("", "/anything"), true);
  assert.equal(scopeAllows("/", "/anything"), true);
  assert.equal(scopeAllows("/team/alpha", "/team/alpha"), true);
  assert.equal(scopeAllows("/team/alpha", "team/alpha/service"), true);
  assert.equal(scopeAllows("/team/alpha", "/team/alpha-2"), false);
  assert.equal(scopeAllows("/team/alpha", "/team"), false);
  assert.equal(scopeAllows("/team/alpha", "/team/beta"), false);
  assert.equal(scopeAllows("/team/alpha", ""), false);
  assert.equal(scopeAllows("/team/alpha", "/"), false);
});

test("pageVisibleForScope hides gateway-wide pages from scoped admins only", () => {
  for (const page of GLOBAL_ONLY_PAGES) {
    assert.equal(pageVisibleForScope(page, true), false, page);
    assert.equal(pageVisibleForScope(page, false), true, page);
  }
  for (const page of ["overview", "usage", "budgets", "rate-limits", "auth-keys", "users", "audit-logs", "settings", "playground"]) {
    assert.equal(pageVisibleForScope(page, true), true, page);
  }
});

test("scopedSubjectAllowed gates budget and rate-limit forms by scope", () => {
  assert.equal(scopedSubjectAllowed(false, "", "label", "team"), true);
  assert.equal(scopedSubjectAllowed(false, "", "provider", "openai"), true);
  assert.equal(scopedSubjectAllowed(true, "/team/alpha", "user_path", "/team/alpha"), true);
  assert.equal(scopedSubjectAllowed(true, "/team/alpha", "user_path", "/team/alpha/svc"), true);
  assert.equal(scopedSubjectAllowed(true, "/team/alpha", "user_path", "/team/beta"), false);
  assert.equal(scopedSubjectAllowed(true, "/team/alpha", "user_path", "/team/alpha-2"), false);
  assert.equal(scopedSubjectAllowed(true, "/team/alpha", "label", "/team/alpha"), false);
  assert.equal(scopedSubjectAllowed(true, "/team/alpha", "provider", "openai"), false);
});
