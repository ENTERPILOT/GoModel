// Shared schema-driven field helpers ($lib/utils/schemaFields.js) used by the
// guardrail editor and the virtual-model editor's plugin route fields.

import test from "node:test";
import assert from "node:assert/strict";

import {
  SECRET_PLACEHOLDER,
  instanceSchemaFields,
  isSecretPlaceholder,
  routeSchemaFields,
  schemaFieldDefaults,
  schemaFieldValue,
  setSchemaFieldValue,
} from "../src/lib/utils/schemaFields.js";

const FIELDS = [
  { key: "api_key", input: "secret" },
  { key: "endpoint", input: "text", default: "https://classifier.local" },
  { key: "threshold", input: "number", scope: "", default: 0.8 },
  { key: "p95_window", input: "text", scope: "route", default: "5m" },
  { key: "roles", input: "checkboxes", default: ["user"] },
];

test("instanceSchemaFields keeps scope \"\" or missing; routeSchemaFields keeps scope route", () => {
  assert.deepEqual(
    instanceSchemaFields(FIELDS).map((field) => field.key),
    ["api_key", "endpoint", "threshold", "roles"],
  );
  assert.deepEqual(
    routeSchemaFields(FIELDS).map((field) => field.key),
    ["p95_window"],
  );
  assert.deepEqual(instanceSchemaFields(undefined), []);
});

test("schemaFieldDefaults collects defined defaults as an independent copy", () => {
  const defaults = schemaFieldDefaults(instanceSchemaFields(FIELDS));
  assert.deepEqual(defaults, {
    endpoint: "https://classifier.local",
    threshold: 0.8,
    roles: ["user"],
  });
  defaults.roles.push("tool");
  assert.deepEqual(FIELDS[4].default, ["user"]);
});

test("a stored secret round-trips as the placeholder until the user types", () => {
  const field = { key: "api_key", input: "secret" };
  const stored = { api_key: SECRET_PLACEHOLDER };

  assert.equal(schemaFieldValue(stored, field), "********");
  assert.equal(isSecretPlaceholder(schemaFieldValue(stored, field)), true);

  // Untouched: the same literal goes back so the server keeps the value.
  assert.equal(setSchemaFieldValue(stored, field, "********").api_key, "********");
  // Edited: the new value replaces it; cleared: an empty string is sent.
  assert.equal(setSchemaFieldValue(stored, field, "sk-new").api_key, "sk-new");
  assert.equal(setSchemaFieldValue(stored, field, "").api_key, "");
  assert.equal(isSecretPlaceholder("sk-new"), false);
});

test("model fields store the raw selector text", () => {
  const field = { key: "model", input: "model" };
  assert.deepEqual(setSchemaFieldValue({}, field, "openai/gpt-4o-mini"), {
    model: "openai/gpt-4o-mini",
  });
});
