// Pure-logic tests for the Providers (provider credentials) page, ported
// from internal/admin/dashboard/static/js/modules/providers-config.test.cjs.
// Networking/dialog flows live in the Svelte state module and are not
// re-tested here.
import test from "node:test";
import assert from "node:assert/strict";

import {
  defaultProviderCredentialForm,
  filterProviderCredentials,
  providerCredentialTypeOptions,
  providerCredentialAuthLabel,
  providerCredentialModelsLabel,
  providerCredentialKeysToRows,
  providerCredentialKeyRowsToArray,
  suggestProviderCredentialName,
  normalizeProviderCredentialCommaList,
  providerCredentialRowToForm,
  validateProviderCredentialForm,
  buildProviderCredentialPayload,
  providerCredentialErrorMessage,
} from "../src/pages/providers-config/providersConfigLogic.js";

test("buildProviderCredentialPayload sends a normalized PUT payload on create", () => {
  const form = {
    ...defaultProviderCredentialForm(),
    name: " my-openai ",
    type: "openai",
    api_keys: [{ value: " sk-live-123 " }],
    base_url: " https://api.openai.com/v1 ",
    models: " gpt-4o, gpt-4o-mini ,,",
    enabled: true,
  };

  assert.equal(validateProviderCredentialForm(form, "create", []), "");
  const body = buildProviderCredentialPayload(form);

  assert.equal(body.name, "my-openai");
  assert.equal(body.type, "openai");
  // API key values must be sent verbatim (no trimming): the server preserves
  // stored keys at positions holding an untouched "***********" mask.
  assert.deepEqual(body.api_keys, [" sk-live-123 "]);
  assert.equal(body.base_url, "https://api.openai.com/v1");
  assert.deepEqual(body.models, ["gpt-4o", "gpt-4o-mini"]);
  assert.equal(body.enabled, true);
});

test("payload preserves untouched masked API key positions on edit", () => {
  const existing = {
    name: "my-openai",
    type: "openai",
    api_keys: ["***********", "***********"],
    base_url: "https://api.openai.com/v1",
    models: ["gpt-4o"],
    enabled: true,
    managed: false,
  };

  const form = providerCredentialRowToForm(existing);
  // Untouched: both rows stay masked. Only a newly added third key is real.
  form.api_keys.push({ value: "sk-new-key" });

  const body = buildProviderCredentialPayload(form);
  assert.deepEqual(body.api_keys, ["***********", "***********", "sk-new-key"]);
  assert.equal(body.name, "my-openai");
});

test("service_account_json is sent verbatim while other fields are trimmed", () => {
  const form = {
    ...defaultProviderCredentialForm(),
    name: "vertex",
    type: "vertex",
    service_account_json: '  {"type": "service_account"}\n',
    service_account_json_base64: " abc123 ",
    vertex_project: " my-project ",
  };

  const body = buildProviderCredentialPayload(form);
  assert.equal(body.service_account_json, '  {"type": "service_account"}\n');
  assert.equal(body.service_account_json_base64, "abc123");
  assert.equal(body.vertex_project, "my-project");
});

test("validateProviderCredentialForm rejects missing fields and blank key rows", () => {
  assert.equal(
    validateProviderCredentialForm(defaultProviderCredentialForm(), "create", []),
    "Name is required.",
  );

  assert.equal(
    validateProviderCredentialForm(
      { ...defaultProviderCredentialForm(), name: "my-openai" },
      "create",
      [],
    ),
    "Type is required.",
  );

  assert.equal(
    validateProviderCredentialForm(
      {
        ...defaultProviderCredentialForm(),
        name: "my-openai",
        type: "openai",
        api_keys: [{ value: "   " }],
      },
      "create",
      [],
    ),
    "API key rows cannot be empty. Remove the row instead of leaving it blank.",
  );
});

test("validateProviderCredentialForm rejects duplicate names only on create", () => {
  const rows = [{ name: "my-openai" }];
  const form = {
    ...defaultProviderCredentialForm(),
    name: "my-openai",
    type: "openai",
  };

  assert.equal(
    validateProviderCredentialForm(form, "create", rows),
    'Provider "my-openai" already exists.',
  );
  assert.equal(validateProviderCredentialForm(form, "edit", rows), "");
});

test("filterProviderCredentials matches name, type, and base URL", () => {
  const rows = [
    { name: "my-openai", type: "openai", base_url: "https://api.openai.com/v1" },
    { name: "local-ollama", type: "ollama", base_url: "http://localhost:11434" },
  ];

  assert.deepEqual(
    filterProviderCredentials(rows, "ollama").map((row) => row.name),
    ["local-ollama"],
  );
  assert.deepEqual(
    filterProviderCredentials(rows, "openai.com").map((row) => row.name),
    ["my-openai"],
  );
  assert.equal(filterProviderCredentials(rows, "").length, 2);
});

test("providerCredentialAuthLabel infers auth mode from populated fields", () => {
  assert.equal(providerCredentialAuthLabel({ api_keys: ["***********"] }), "1 key");
  assert.equal(
    providerCredentialAuthLabel({ api_keys: ["***********", "***********"] }),
    "2 keys",
  );
  assert.equal(
    providerCredentialAuthLabel({ service_account_json: "***********" }),
    "service account",
  );
  assert.equal(
    providerCredentialAuthLabel({ service_account_file: "/etc/gcp.json" }),
    "service account",
  );
  assert.equal(providerCredentialAuthLabel({ vertex_project: "my-project" }), "ADC");
  assert.equal(providerCredentialAuthLabel({}), "keyless");
});

test("providerCredentialModelsLabel reports counts or auto-discovery", () => {
  assert.equal(providerCredentialModelsLabel({ models: [] }), "auto-discovered");
  assert.equal(providerCredentialModelsLabel({}), "auto-discovered");
  assert.equal(providerCredentialModelsLabel({ models: ["gpt-4o"] }), "1 model");
  assert.equal(
    providerCredentialModelsLabel({ models: ["gpt-4o", "gpt-4o-mini"] }),
    "2 models",
  );
});

test("providerCredentialRowToForm prefills the form from a view row", () => {
  const form = providerCredentialRowToForm({
    name: "my-openai",
    type: "openai",
    api_keys: ["***********"],
    base_url: "https://api.openai.com/v1",
    models: ["gpt-4o", "gpt-4o-mini"],
    enabled: false,
    managed: false,
  });

  assert.equal(form.name, "my-openai");
  assert.equal(form.type, "openai");
  assert.deepEqual(form.api_keys, [{ value: "***********" }]);
  assert.equal(form.models, "gpt-4o, gpt-4o-mini");
  assert.equal(form.enabled, false);
});

test("providerCredentialKeysToRows and back round-trip without trimming", () => {
  const rows = providerCredentialKeysToRows(["***********", " sk-raw "]);
  assert.deepEqual(rows, [{ value: "***********" }, { value: " sk-raw " }]);
  assert.deepEqual(providerCredentialKeyRowsToArray(rows), [
    "***********",
    " sk-raw ",
  ]);
  assert.deepEqual(providerCredentialKeysToRows(undefined), []);
  assert.deepEqual(providerCredentialKeyRowsToArray(undefined), []);
});

test("suggestProviderCredentialName prefers the bare type name when free", () => {
  assert.equal(
    suggestProviderCredentialName([{ name: "anthropic" }], "openai"),
    "openai",
  );
});

test('suggestProviderCredentialName falls back to "{type}-N" when the bare name is taken', () => {
  // openai and openai-1 are both taken (one declared/config, one dashboard-managed).
  assert.equal(
    suggestProviderCredentialName(
      [{ name: "openai" }, { name: "openai-1" }, { name: "other" }],
      "openai",
    ),
    "openai-2",
  );
});

test("suggestProviderCredentialName returns empty for an empty type", () => {
  assert.equal(suggestProviderCredentialName([{ name: "openai" }], ""), "");
  assert.equal(suggestProviderCredentialName([{ name: "openai" }], "   "), "");
});

test("providerCredentialTypeOptions always includes the current selection", () => {
  assert.deepEqual(
    providerCredentialTypeOptions(["openai", "anthropic"], "ollama"),
    ["openai", "anthropic", "ollama"],
  );
  assert.deepEqual(
    providerCredentialTypeOptions(["openai", "anthropic"], "openai"),
    ["openai", "anthropic"],
  );
  assert.deepEqual(providerCredentialTypeOptions(null, " "), []);
});

test("normalizeProviderCredentialCommaList trims and drops empties", () => {
  assert.deepEqual(
    normalizeProviderCredentialCommaList(" gpt-4o, gpt-4o-mini ,,"),
    ["gpt-4o", "gpt-4o-mini"],
  );
  assert.deepEqual(normalizeProviderCredentialCommaList(""), []);
});

test("providerCredentialErrorMessage extracts error.message or falls back", () => {
  assert.equal(
    providerCredentialErrorMessage(
      { error: { message: "storage unavailable" } },
      "fallback",
    ),
    "storage unavailable",
  );
  assert.equal(providerCredentialErrorMessage({}, "fallback"), "fallback");
  assert.equal(providerCredentialErrorMessage(null, "fallback"), "fallback");
  assert.equal(
    providerCredentialErrorMessage({ error: "plain string" }, "fallback"),
    "fallback",
  );
});
