// Ported pure-logic cases from the legacy guardrails.test.cjs. DOM/Alpine
// harness cases (select re-sync, focus management, fetch wiring, stale-auth
// generations) are covered by the Svelte runtime / shared API client and are
// intentionally not ported.
import test from "node:test";
import assert from "node:assert/strict";

import {
  buildGuardrailPayload,
  defaultGuardrailForm,
  filterGuardrails,
  guardrailArrayFieldSelected,
  guardrailFieldValue,
  normalizeGuardrailConfig,
  setGuardrailFieldValue,
  toggleGuardrailArrayValue,
} from "../src/pages/guardrails/guardrails-logic.js";

test("defaultGuardrailForm uses the first available type defaults", () => {
  const types = [
    {
      type: "system_prompt",
      defaults: { mode: "inject", content: "" },
      fields: [],
    },
  ];

  const form = defaultGuardrailForm(types);

  assert.equal(form.type, "system_prompt");
  assert.equal(form.user_path, "");
  assert.deepEqual(form.config, { mode: "inject", content: "" });
});

test("defaultGuardrailForm includes the built-in llm_based_altering prompt", () => {
  const types = [
    {
      type: "llm_based_altering",
      defaults: {
        model: "",
        prompt: "built-in prompt",
        roles: ["user"],
        max_tokens: 4096,
      },
      fields: [],
    },
  ];

  const form = defaultGuardrailForm(types);

  assert.equal(form.type, "llm_based_altering");
  assert.deepEqual(form.config, {
    model: "",
    prompt: "built-in prompt",
    roles: ["user"],
    max_tokens: 4096,
  });
});

test("defaultGuardrailForm falls back to system_prompt without types", () => {
  const form = defaultGuardrailForm([]);

  assert.equal(form.type, "system_prompt");
  assert.deepEqual(form.config, {});
});

test("normalizeGuardrailConfig merges stored config over type defaults", () => {
  const types = [
    {
      type: "system_prompt",
      defaults: { mode: "inject", content: "" },
      fields: [],
    },
  ];

  const config = normalizeGuardrailConfig(
    types,
    { content: "be careful" },
    "system_prompt",
  );

  assert.deepEqual(config, { mode: "inject", content: "be careful" });
});

test("normalizeGuardrailConfig fills the built-in llm_based_altering prompt for existing instances", () => {
  const types = [
    {
      type: "llm_based_altering",
      defaults: {
        model: "",
        prompt: "built-in prompt",
        roles: ["user"],
        max_tokens: 4096,
      },
      fields: [],
    },
  ];

  const config = normalizeGuardrailConfig(
    types,
    { model: "openai/gpt-4o-mini" },
    "llm_based_altering",
  );

  assert.deepEqual(config, {
    model: "openai/gpt-4o-mini",
    prompt: "built-in prompt",
    roles: ["user"],
    max_tokens: 4096,
  });
});

test("normalizeGuardrailConfig returns the input config for unknown types", () => {
  const types = [
    {
      type: "system_prompt",
      defaults: { mode: "inject", content: "" },
      fields: [],
    },
  ];

  const config = normalizeGuardrailConfig(
    types,
    { content: "test" },
    "unknown_type",
  );

  assert.deepEqual(config, { content: "test" });
});

test("filterGuardrails matches user_path values", () => {
  const guardrails = [
    {
      name: "policy",
      type: "system_prompt",
      user_path: "/team/alpha",
      summary: "be careful",
    },
  ];

  const filtered = filterGuardrails(guardrails, "alpha");

  assert.equal(filtered.length, 1);
  assert.equal(filtered[0].name, "policy");
});

test("filterGuardrails returns everything for an empty filter", () => {
  const guardrails = [{ name: "a" }, { name: "b" }];

  assert.equal(filterGuardrails(guardrails, "").length, 2);
  assert.equal(filterGuardrails(guardrails, "zzz").length, 0);
});

test("checkbox guardrail fields normalize and toggle array values", () => {
  const field = { key: "roles", input: "checkboxes" };
  let config = { roles: ["user"] };

  assert.deepEqual(guardrailFieldValue(config, field), ["user"]);
  assert.equal(guardrailArrayFieldSelected(config, field, "user"), true);
  assert.equal(guardrailArrayFieldSelected(config, field, "tool"), false);

  config = toggleGuardrailArrayValue(config, field, "tool", true);
  assert.deepEqual(config.roles, ["user", "tool"]);

  config = toggleGuardrailArrayValue(config, field, "user", false);
  assert.deepEqual(config.roles, ["tool"]);
});

test("setGuardrailFieldValue parses numbers and clears empty number inputs", () => {
  const field = { key: "max_tokens", input: "number" };

  let config = setGuardrailFieldValue({}, field, "4096");
  assert.deepEqual(config, { max_tokens: 4096 });

  config = setGuardrailFieldValue(config, field, "");
  assert.deepEqual(config, {});

  config = setGuardrailFieldValue(config, field, "not-a-number");
  assert.deepEqual(config, { max_tokens: "not-a-number" });
});

test("buildGuardrailPayload sends the guardrail name and omits empty optional fields", () => {
  const payload = buildGuardrailPayload({
    name: "privacy/redactor",
    type: "llm_based_altering",
    description: "",
    user_path: "",
    config: { model: "openai/gpt-4o-mini", roles: ["user"] },
  });

  // Same JSON body shape as the legacy PUT /admin/guardrails request:
  // description/user_path are undefined so JSON.stringify drops them.
  assert.deepEqual(JSON.parse(JSON.stringify(payload)), {
    name: "privacy/redactor",
    type: "llm_based_altering",
    config: {
      model: "openai/gpt-4o-mini",
      roles: ["user"],
    },
  });
});

test("buildGuardrailPayload keeps trimmed optional fields", () => {
  const payload = buildGuardrailPayload({
    name: "  privacy  ",
    type: " system_prompt ",
    description: " what it does ",
    user_path: " /team/alpha ",
    config: { content: "x" },
  });

  assert.deepEqual(JSON.parse(JSON.stringify(payload)), {
    name: "privacy",
    type: "system_prompt",
    description: "what it does",
    user_path: "/team/alpha",
    config: { content: "x" },
  });
});

// ─── Plugin-era additions: field defaults, scopes, phases, secrets, advanced ───

import {
  guardrailEditForm,
  guardrailPhases,
  guardrailTypeFields,
  guardrailTypePhases,
  parseGuardrailTimeoutMs,
} from "../src/pages/guardrails/guardrails-logic.js";

const PLUGIN_TYPES = [
  {
    type: "classifier",
    label: "Classifier",
    defaults: { threshold: 0.5, endpoint: "" },
    phases: ["prompt", "response"],
    fields: [
      { key: "api_key", input: "secret" },
      { key: "endpoint", input: "text", default: "https://classifier.local" },
      { key: "threshold", input: "number", default: 0.8 },
      { key: "model", input: "model" },
      { key: "budget_ms", input: "number", scope: "route", default: 250 },
    ],
  },
];

test("guardrailTypeFields renders only instance-scoped fields", () => {
  assert.deepEqual(
    guardrailTypeFields(PLUGIN_TYPES, "classifier").map((field) => field.key),
    ["api_key", "endpoint", "threshold", "model"],
  );
});

test("defaultGuardrailForm applies per-field defaults over the defaults object", () => {
  const form = defaultGuardrailForm(PLUGIN_TYPES, "classifier");
  // endpoint/threshold come from Field.default; nothing from a route field.
  assert.deepEqual(form.config, {
    threshold: 0.8,
    endpoint: "https://classifier.local",
  });
  assert.equal(form.fail_mode, "");
  assert.equal(form.timeout_ms, "");
});

test("guardrail phases come from the view, then the type, then prompt-only", () => {
  assert.deepEqual(guardrailTypePhases(PLUGIN_TYPES, "classifier"), ["prompt", "response"]);
  assert.deepEqual(guardrailTypePhases(PLUGIN_TYPES, "system_prompt"), ["prompt"]);
  assert.deepEqual(
    guardrailPhases(PLUGIN_TYPES, { type: "classifier", phases: ["stream"] }),
    ["stream"],
  );
  assert.deepEqual(guardrailPhases(PLUGIN_TYPES, { type: "classifier" }), [
    "prompt",
    "response",
  ]);
  assert.deepEqual(guardrailPhases(PLUGIN_TYPES, { type: "legacy" }), ["prompt"]);
});

test("editing keeps the stored secret placeholder and sends it back unchanged", () => {
  const form = guardrailEditForm(PLUGIN_TYPES, {
    name: "pii",
    type: "classifier",
    config: { api_key: "********", endpoint: "https://x", threshold: 0.9 },
    fail_mode: "open",
    timeout_ms: 1500,
  });
  assert.equal(form.config.api_key, "********");
  assert.equal(form.fail_mode, "open");
  assert.equal(form.timeout_ms, "1500");

  const untouched = JSON.parse(JSON.stringify(buildGuardrailPayload(form)));
  assert.equal(untouched.config.api_key, "********");
  assert.equal(untouched.fail_mode, "open");
  assert.equal(untouched.timeout_ms, 1500);

  const edited = buildGuardrailPayload({
    ...form,
    config: setGuardrailFieldValue(form.config, { key: "api_key", input: "secret" }, "sk-new"),
  });
  assert.equal(edited.config.api_key, "sk-new");
});

test("buildGuardrailPayload omits default fail_mode and timeout", () => {
  const payload = JSON.parse(
    JSON.stringify(
      buildGuardrailPayload({
        name: "x",
        type: "classifier",
        config: {},
        fail_mode: "",
        timeout_ms: "",
      }),
    ),
  );
  assert.equal("fail_mode" in payload, false);
  assert.equal("timeout_ms" in payload, false);

  const zero = JSON.parse(
    JSON.stringify(
      buildGuardrailPayload({ name: "x", type: "classifier", timeout_ms: "0", fail_mode: "bogus" }),
    ),
  );
  assert.equal("timeout_ms" in zero, false);
  assert.equal("fail_mode" in zero, false);
});

test("parseGuardrailTimeoutMs accepts empty/whole numbers and rejects the rest", () => {
  assert.equal(parseGuardrailTimeoutMs(""), 0);
  assert.equal(parseGuardrailTimeoutMs(" 250 "), 250);
  assert.ok(Number.isNaN(parseGuardrailTimeoutMs("1.5")));
  assert.ok(Number.isNaN(parseGuardrailTimeoutMs("-1")));
  assert.ok(Number.isNaN(parseGuardrailTimeoutMs("abc")));
});

test("guardrailEditForm falls back to the default type for unknown types", () => {
  const form = guardrailEditForm(PLUGIN_TYPES, { name: "old", type: "gone", timeout_ms: 0 });
  assert.equal(form.type, "classifier");
  assert.equal(form.timeout_ms, "");
});
