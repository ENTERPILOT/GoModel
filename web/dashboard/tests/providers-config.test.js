// Pure-logic tests for the Providers (provider credentials) page.
// Networking/dialog flows live in the Svelte state module and are not
// re-tested here.
import test from "node:test";
import assert from "node:assert/strict";

import {
  defaultProviderCredentialForm,
  filterProviderCredentials,
  providerCredentialTypeOptions,
  providerCredentialSchema,
  providerCredentialFormFields,
  providerCredentialFieldMeta,
  providerCredentialAuthLabel,
  providerCredentialModelsLabel,
  providerCredentialKeysToRows,
  providerCredentialKeyRowsToArray,
  suggestProviderCredentialName,
  splitCommaList,
  providerCredentialRowToForm,
  validateProviderCredentialForm,
  buildProviderCredentialPayload,
} from "../src/pages/providers-config/providersConfigLogic.js";

// Schemas shaped like GET /admin/provider-credentials/types serves them.
const OPENAI_SCHEMA = {
  type: "openai",
  default_base_url: "https://api.openai.com/v1",
  fields: [
    { name: "api_keys", required: true, advanced: false },
    { name: "base_url", required: false, advanced: true },
    { name: "models", required: false, advanced: true },
  ],
};

const AZURE_SCHEMA = {
  type: "azure",
  fields: [
    { name: "api_keys", required: true, advanced: false },
    { name: "base_url", required: true, advanced: false },
    { name: "api_version", required: false, advanced: false },
    { name: "models", required: false, advanced: true },
  ],
};

const VERTEX_SCHEMA = {
  type: "vertex",
  fields: [
    { name: "auth_type", required: false, advanced: false, options: ["gcp_adc", "gcp_service_account"] },
    { name: "vertex_project", required: false, advanced: false },
    { name: "vertex_location", required: false, advanced: false },
    { name: "service_account_json", required: false, advanced: false },
    { name: "base_url", required: false, advanced: true },
    { name: "models", required: false, advanced: true },
  ],
};

const SCHEMAS = [OPENAI_SCHEMA, AZURE_SCHEMA, VERTEX_SCHEMA];

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

  assert.deepEqual(validateProviderCredentialForm(form, "create", [], OPENAI_SCHEMA), {});
  const body = buildProviderCredentialPayload(form, OPENAI_SCHEMA);

  assert.equal(body.name, "my-openai");
  assert.equal(body.type, "openai");
  // API key values must be sent verbatim (no trimming): the server preserves
  // stored keys at positions holding an untouched "***********" mask.
  assert.deepEqual(body.api_keys, [" sk-live-123 "]);
  assert.equal(body.base_url, "https://api.openai.com/v1");
  assert.deepEqual(body.models, ["gpt-4o", "gpt-4o-mini"]);
  assert.equal(body.enabled, true);
});

test("the payload carries the fields the provider type accepts", () => {
  const form = {
    ...defaultProviderCredentialForm(),
    name: "my-openai",
    type: "openai",
    api_keys: [{ value: "sk-live-123" }],
  };

  assert.deepEqual(Object.keys(buildProviderCredentialPayload(form, OPENAI_SCHEMA)).sort(), [
    "api_keys",
    "base_url",
    "enabled",
    "models",
    "name",
    "type",
  ]);
});

// PUT replaces the whole row, so a value the form never showed would be
// dropped — and a field missing from a schema by mistake is exactly the one
// whose loss would go unnoticed.
test("a stored value the form does not render is sent back, not dropped", () => {
  const form = {
    ...providerCredentialRowToForm({
      name: "my-openai",
      type: "openai",
      api_keys: ["***********"],
      api_version: "2024-10-01-preview",
    }),
  };

  const body = buildProviderCredentialPayload(form, OPENAI_SCHEMA);
  assert.equal(body.api_version, "2024-10-01-preview");
  // Empty non-schema fields stay out of the payload.
  assert.equal("vertex_project" in body, false);
});

test("an unavailable schema falls back to sending every field", () => {
  const form = {
    ...defaultProviderCredentialForm(),
    name: "my-openai",
    type: "openai",
    vertex_project: "my-project",
  };

  const body = buildProviderCredentialPayload(form, null);
  assert.equal(body.vertex_project, "my-project");
  assert.equal(body.api_version, "");
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

  const body = buildProviderCredentialPayload(form, OPENAI_SCHEMA);
  assert.deepEqual(body.api_keys, ["***********", "***********", "sk-new-key"]);
  assert.equal(body.name, "my-openai");
});

test("service_account_json is sent verbatim while other fields are trimmed", () => {
  const form = {
    ...defaultProviderCredentialForm(),
    name: "vertex",
    type: "vertex",
    service_account_json: '  {"type": "service_account"}\n',
    vertex_project: " my-project ",
  };

  const body = buildProviderCredentialPayload(form, VERTEX_SCHEMA);
  assert.equal(body.service_account_json, '  {"type": "service_account"}\n');
  assert.equal(body.vertex_project, "my-project");
});

test("providerCredentialFormFields splits a schema into primary and advanced", () => {
  const { primary, advanced } = providerCredentialFormFields(OPENAI_SCHEMA);

  assert.deepEqual(primary.map((field) => field.name), ["api_keys"]);
  assert.deepEqual(advanced.map((field) => field.name), ["base_url", "models"]);
  assert.equal(primary[0].required, true);
  assert.equal(primary[0].label, "API Keys");
  assert.equal(primary[0].control, "keys");
});

test("a field with options renders as a select", () => {
  const { primary } = providerCredentialFormFields(VERTEX_SCHEMA);
  const authType = primary.find((field) => field.name === "auth_type");

  assert.equal(authType.control, "select");
  assert.deepEqual(authType.options, ["gcp_adc", "gcp_service_account"]);
  // Vertex authenticates with Google credentials: no API key field at all.
  assert.equal(primary.some((field) => field.name === "api_keys"), false);
});

test("the base URL placeholder shows the provider type's default", () => {
  const { advanced } = providerCredentialFormFields(OPENAI_SCHEMA, OPENAI_SCHEMA.default_base_url);
  const baseURL = advanced.find((field) => field.name === "base_url");

  assert.equal(baseURL.placeholder, "https://api.openai.com/v1");
  assert.equal(baseURL.hint, "Defaults to https://api.openai.com/v1");
});

test("a missing schema falls back to every known field", () => {
  const { primary, advanced } = providerCredentialFormFields(null);
  const names = [...primary, ...advanced].map((field) => field.name);

  assert.equal(names.includes("api_keys"), true);
  assert.equal(names.includes("vertex_project"), true);
  assert.deepEqual(primary.map((field) => field.name), ["api_keys"]);
});

test("providerCredentialFieldMeta humanizes a field the dashboard has no copy for", () => {
  assert.equal(providerCredentialFieldMeta("some_new_field").label, "Some New Field");
  assert.equal(providerCredentialFieldMeta("some_new_field").control, "text");
});

test("providerCredentialSchema finds the selected type", () => {
  assert.equal(providerCredentialSchema(SCHEMAS, "azure"), AZURE_SCHEMA);
  assert.equal(providerCredentialSchema(SCHEMAS, "nope"), null);
  assert.equal(providerCredentialSchema(SCHEMAS, ""), null);
  assert.equal(providerCredentialSchema(null, "azure"), null);
});

test("validation reports each problem against its own field", () => {
  const errors = validateProviderCredentialForm(
    defaultProviderCredentialForm(),
    "create",
    [],
    OPENAI_SCHEMA,
  );

  assert.equal(errors.name, "Name is required.");
  assert.equal(errors.type, "Select a provider type.");
  assert.equal(errors.api_keys, "At least one API key is required for this provider type.");
});

test("a field the type does not require is not demanded", () => {
  // Vertex needs no API key, and its own field rules (project/location vs
  // base URL) are the gateway's call, so a bare form is submittable.
  const form = { ...defaultProviderCredentialForm(), name: "vertex", type: "vertex" };
  assert.deepEqual(validateProviderCredentialForm(form, "create", [], VERTEX_SCHEMA), {});
});

test("a required field of the selected type is demanded", () => {
  const form = {
    ...defaultProviderCredentialForm(),
    name: "my-azure",
    type: "azure",
    api_keys: [{ value: "sk-azure" }],
  };
  const errors = validateProviderCredentialForm(form, "create", [], AZURE_SCHEMA);

  assert.equal(errors.base_url, "Base URL is required for this provider type.");
});

test("validation rejects blank key rows, bad names, and duplicates", () => {
  const strayRow = validateProviderCredentialForm(
    {
      ...defaultProviderCredentialForm(),
      name: "my-openai",
      type: "openai",
      api_keys: [{ value: "sk-live" }, { value: "   " }],
    },
    "create",
    [],
    OPENAI_SCHEMA,
  );
  assert.equal(strayRow.api_keys, "Remove the empty row instead of leaving a key blank.");

  // A required field left as a single blank row is a missing key, not a
  // stray row: the editor opens one empty row for a type that needs a key.
  const onlyBlank = validateProviderCredentialForm(
    {
      ...defaultProviderCredentialForm(),
      name: "my-openai",
      type: "openai",
      api_keys: [{ value: "   " }],
    },
    "create",
    [],
    OPENAI_SCHEMA,
  );
  assert.equal(onlyBlank.api_keys, "At least one API key is required for this provider type.");

  const slashed = validateProviderCredentialForm(
    { ...defaultProviderCredentialForm(), name: "my/openai", type: "openai" },
    "create",
    [],
    OPENAI_SCHEMA,
  );
  assert.match(slashed.name, /cannot contain/);

  const rows = [{ name: "my-openai" }];
  const duplicate = {
    ...defaultProviderCredentialForm(),
    name: "my-openai",
    type: "openai",
    api_keys: [{ value: "sk-live" }],
  };
  assert.equal(
    validateProviderCredentialForm(duplicate, "create", rows, OPENAI_SCHEMA).name,
    'Provider "my-openai" already exists.',
  );
  // The same name is expected on edit — that row is the one being edited.
  assert.deepEqual(validateProviderCredentialForm(duplicate, "edit", rows, OPENAI_SCHEMA), {});
});

test("a scheme-less host is rejected but a region is accepted", () => {
  const form = {
    ...defaultProviderCredentialForm(),
    name: "my-openai",
    type: "openai",
    api_keys: [{ value: "sk-live" }],
    base_url: "api.openai.com/v1",
  };
  assert.equal(
    validateProviderCredentialForm(form, "create", [], OPENAI_SCHEMA).base_url,
    "Include the scheme, e.g. https://api.openai.com/v1",
  );

  form.base_url = "us-east-1";
  assert.equal(validateProviderCredentialForm(form, "create", [], OPENAI_SCHEMA).base_url, undefined);
});

test("service account JSON must parse, and an untouched mask is left alone", () => {
  const form = {
    ...defaultProviderCredentialForm(),
    name: "vertex",
    type: "vertex",
    service_account_json: "not json",
  };
  assert.match(
    validateProviderCredentialForm(form, "create", [], VERTEX_SCHEMA).service_account_json,
    /not valid JSON/,
  );

  form.service_account_json = "***********";
  assert.equal(
    validateProviderCredentialForm(form, "create", [], VERTEX_SCHEMA).service_account_json,
    undefined,
  );
});

// The schema's options are what the form offers, not a whitelist: providers
// accept other spellings of the same value, so a stored one must not be
// flagged as invalid on the next edit. Only the gateway judges these.
test("an enumerated field accepts a value outside its options", () => {
  const form = {
    ...defaultProviderCredentialForm(),
    name: "vertex",
    type: "vertex",
    auth_type: "service_account",
  };
  assert.deepEqual(validateProviderCredentialForm(form, "create", [], VERTEX_SCHEMA), {});
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
  assert.deepEqual(providerCredentialTypeOptions(SCHEMAS, "ollama"), [
    "openai",
    "azure",
    "vertex",
    "ollama",
  ]);
  assert.deepEqual(providerCredentialTypeOptions(SCHEMAS, "openai"), [
    "openai",
    "azure",
    "vertex",
  ]);
  assert.deepEqual(providerCredentialTypeOptions(null, " "), []);
});

test("splitCommaList trims and drops empties", () => {
  assert.deepEqual(
    splitCommaList(" gpt-4o, gpt-4o-mini ,,"),
    ["gpt-4o", "gpt-4o-mini"],
  );
  assert.deepEqual(splitCommaList(""), []);
});
