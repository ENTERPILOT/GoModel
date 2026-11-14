import test from "node:test";
import assert from "node:assert/strict";

import { authKeyLabels, authKeysNamed } from "../src/lib/utils/authKeyLabels.js";

test("authKeyLabels shows a unique name alone", () => {
  assert.deepEqual(
    authKeyLabels([
      { id: "k1", name: "ci-deploy", redacted_value: "sk_gom_...abcd" },
      { id: "k2", name: "batch", redacted_value: "sk_gom_...wxyz" },
    ]),
    { k1: "ci-deploy", k2: "batch" },
  );
});

test("authKeyLabels appends the redacted token to same-name keys", () => {
  assert.deepEqual(
    authKeyLabels([
      { id: "k1", name: "ci-deploy", redacted_value: "sk_gom_...abcd", active: false },
      { id: "k2", name: " ci-deploy ", redacted_value: "sk_gom_...wxyz", active: true },
      { id: "k3", name: "CI-deploy", redacted_value: "sk_gom_...q1q1" },
    ]),
    {
      k1: "ci-deploy (sk_gom_...abcd)",
      k2: "ci-deploy (sk_gom_...wxyz)",
      // Case is not folded: "CI-deploy" is a different name.
      k3: "CI-deploy",
    },
  );
});

test("authKeyLabels falls back to a short id without a usable redacted value", () => {
  assert.deepEqual(
    authKeyLabels([
      { id: "0f8b2c1e-5d4a-4b7e-9c3f-8a6d2e1b7c90", name: "imported" },
      { id: "7c1d9e2a-1b2c-4d5e-8f90-123456789abc", name: "imported", redacted_value: "sk-..." },
    ]),
    {
      "0f8b2c1e-5d4a-4b7e-9c3f-8a6d2e1b7c90": "imported (0f8b2c1e)",
      "7c1d9e2a-1b2c-4d5e-8f90-123456789abc": "imported (7c1d9e2a)",
    },
  );
});

test("authKeyLabels falls back to the id for a blank name and skips entries without an id", () => {
  assert.deepEqual(
    authKeyLabels([{ id: "k1", name: " " }, { id: "k2", name: "" }, { name: "no id" }, null]),
    { k1: "k1", k2: "k2" },
  );
  assert.deepEqual(authKeyLabels(null), {});
});

test("authKeysNamed matches trimmed names exactly and can leave one key out", () => {
  const keys = [
    { id: "k1", name: "ci-deploy" },
    { id: "k2", name: "CI-deploy" },
    { id: "k3", name: "ci-deploy" },
  ];
  assert.deepEqual(authKeysNamed(keys, "  ci-deploy ").map((key) => key.id), ["k1", "k3"]);
  assert.deepEqual(authKeysNamed(keys, "ci-deploy", "k1").map((key) => key.id), ["k3"]);
  assert.deepEqual(authKeysNamed(keys, "  "), []);
  assert.deepEqual(authKeysNamed(null, "ci-deploy"), []);
});
