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
  schemaFieldRangeBounds,
  schemaFieldRangeValue,
  schemaFieldValue,
  setSchemaFieldValue,
} from "../src/lib/utils/schemaFields.js";
import { modelPickerOptions } from "../src/lib/utils/modelSelectors.js";

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

test("list fields read arrays or free text and store a de-duplicated array", () => {
  const field = { key: "entities", input: "list" };
  assert.deepEqual(schemaFieldValue({}, field), []);
  assert.deepEqual(schemaFieldValue({ entities: ["PERSON", " EMAIL "] }, field), ["PERSON", "EMAIL"]);
  assert.deepEqual(schemaFieldValue({ entities: "PERSON, EMAIL\nPHONE" }, field), ["PERSON", "EMAIL", "PHONE"]);
  assert.deepEqual(setSchemaFieldValue({}, field, "PERSON\n\nEMAIL,PERSON"), {
    entities: ["PERSON", "EMAIL"],
  });
  assert.deepEqual(setSchemaFieldValue({ entities: ["x"] }, field, ""), { entities: [] });
});

test("bool fields read stored booleans and words and store a boolean", () => {
  const field = { key: "reversible", input: "bool" };
  assert.equal(schemaFieldValue({}, field), false);
  assert.equal(schemaFieldValue({ reversible: true }, field), true);
  assert.equal(schemaFieldValue({ reversible: "true" }, field), true);
  assert.equal(schemaFieldValue({ reversible: "yes" }, field), true);
  assert.equal(schemaFieldValue({ reversible: 1 }, field), true);
  assert.equal(schemaFieldValue({ reversible: "false" }, field), false);
  assert.equal(schemaFieldValue({ reversible: "" }, field), false);
  assert.deepEqual(setSchemaFieldValue({}, field, true), { reversible: true });
  assert.deepEqual(setSchemaFieldValue({ reversible: true }, field, false), { reversible: false });
});

test("modelPickerOptions lists enabled selectors sorted with their provider", () => {
  const models = [
    { selector: "openai/gpt-4o-mini", provider_name: "openai" },
    { selector: "anthropic/claude-haiku-4-5", provider_name: "anthropic" },
    { selector: "openai/gpt-4o-mini", provider_name: "openai" },
    { selector: "openai/o1", provider_name: "openai", access: { effective_enabled: false } },
    { model: { id: "gemini/flash" }, provider_name: "gemini" },
    { selector: "", provider_name: "empty" },
  ];
  assert.deepEqual(modelPickerOptions(models), [
    { value: "anthropic/claude-haiku-4-5", label: "anthropic/claude-haiku-4-5", description: "anthropic" },
    { value: "gemini/flash", label: "gemini/flash", description: "gemini" },
    { value: "openai/gpt-4o-mini", label: "openai/gpt-4o-mini", description: "openai" },
  ]);
  assert.deepEqual(modelPickerOptions(undefined), []);
});

test("range fields read a bounded number and store a number", () => {
  const field = { key: "weight", input: "range", min: 0, max: 1, step: 0.05, default: 0.35 };

  assert.equal(schemaFieldValue({}, field), 0.35);
  assert.equal(schemaFieldValue({ weight: 0.7 }, field), 0.7);
  assert.equal(schemaFieldValue({ weight: "0.7" }, field), 0.7);
  assert.equal(schemaFieldValue({}, { key: "cut", input: "range", min: 0.2 }), 0.2);
  assert.deepEqual(setSchemaFieldValue({}, field, "0.5"), { weight: 0.5 });
  assert.deepEqual(setSchemaFieldValue({ weight: 0.5 }, field, ""), {});
});

test("a range without a default seeds the value the slider shows", () => {
  const fields = [{ key: "cut", input: "range", min: 0.2 }];
  assert.deepEqual(schemaFieldDefaults(fields), { cut: 0.2 });
  assert.equal(schemaFieldValue(schemaFieldDefaults(fields), fields[0]), 0.2);
});

test("a range clamps the displayed value and keeps the stored one", () => {
  const field = { key: "weight", input: "range", min: 0, max: 1, step: 0.05 };
  const stored = { weight: 2 };

  assert.equal(schemaFieldRangeValue(stored, field), 1, "the slider and its output show the upper bound");
  assert.equal(stored.weight, 2, "the config keeps the out-of-range value until the operator moves the slider");
  assert.equal(schemaFieldRangeValue({ weight: -1 }, field), 0);
  assert.equal(schemaFieldRangeValue({}, field), 0, "an empty range displays its lower bound");

  assert.deepEqual(schemaFieldRangeBounds({ key: "w", input: "range" }), { min: 0, max: 1, step: 0.01 });
  assert.deepEqual(
    schemaFieldRangeBounds({ key: "w", input: "range", min: 0.2, max: 0.9, step: 0.05 }),
    { min: 0.2, max: 0.9, step: 0.05 },
  );
});
