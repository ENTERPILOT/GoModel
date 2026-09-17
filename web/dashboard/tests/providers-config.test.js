// Pure-logic tests for the Providers (provider credentials) page.
// Networking/dialog flows live in the Svelte state module and are not
// re-tested here.
import test from "node:test";
import assert from "node:assert/strict";
import { overwriteGetLocale } from "../src/lib/paraglide/runtime.js";

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
  resetProviderCredentialFields,
  validateProviderCredentialForm,
  buildProviderCredentialPayload,
  providerRowsHaveActions,
  providerRowsHaveTripRules,
  providerCredentialTripRulesLabel,
  parseGoDuration,
  formatGoDurationNs,
  tripRulesToRows,
  tripRuleRowsToWire,
} from "../src/pages/providers-config/providersConfigLogic.js";

// Schemas shaped like GET /admin/provider-credentials/types serves them.
const OPENAI_SCHEMA = {
  type: "openai",
  default_base_url: "https://api.openai.com/v1",
  fields: [
    { name: "api_keys", required: true, advanced: false },
    { name: "base_url", required: false, advanced: true },
    { name: "session_sticky_keys", required: false, advanced: true },
    { name: "models", required: false, advanced: true },
  ],
};

const AZURE_SCHEMA = {
  type: "azure",
  fields: [
    { name: "api_keys", required: true, advanced: false },
    { name: "base_url", required: true, advanced: false },
    { name: "api_version", required: false, advanced: false },
    { name: "session_sticky_keys", required: false, advanced: true },
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
  assert.equal(body.session_sticky_keys, true);
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
    "session_sticky_keys",
    "trip_on",
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

// Switching type in the create form must not leave the abandoned type's
// values behind: the payload echoes back unrendered values, so they would
// reach the gateway with nothing on screen to explain them.
test("changing type drops the values the new type does not render", () => {
  const typedForGemini = {
    ...defaultProviderCredentialForm(),
    name: "my-openai",
    type: "openai",
    api_keys: [{ value: "sk-live" }],
    vertex_project: "left-over",
    service_account_json: '{"type":"service_account"}',
    models: "gpt-4o",
  };

  const form = resetProviderCredentialFields(
    typedForGemini,
    providerCredentialFormFields(OPENAI_SCHEMA),
  );

  assert.equal(form.vertex_project, "");
  assert.equal(form.service_account_json, "");
  // Identity and the fields OpenAI does render survive.
  assert.equal(form.name, "my-openai");
  assert.equal(form.type, "openai");
  assert.equal(form.models, "gpt-4o");
  assert.deepEqual(form.api_keys, [{ value: "sk-live" }]);
  assert.equal("vertex_project" in buildProviderCredentialPayload(form, OPENAI_SCHEMA), false);
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
  assert.equal("session_sticky_keys" in body, false);
});

test("an empty schema does not advertise session-sticky keys", () => {
  const form = {
    ...defaultProviderCredentialForm(),
    name: "my-openai",
    type: "openai",
  };

  for (const schema of [{ type: "openai", fields: [] }, { type: "openai" }]) {
    const body = buildProviderCredentialPayload(form, schema);
    assert.equal("session_sticky_keys" in body, false);
  }
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
  assert.equal("session_sticky_keys" in body, false);
});

test("providerCredentialFormFields splits a schema into primary and advanced", () => {
  const { primary, advanced } = providerCredentialFormFields(OPENAI_SCHEMA);

  assert.deepEqual(primary.map((field) => field.name), ["api_keys"]);
  assert.deepEqual(advanced.map((field) => field.name), [
    "base_url",
    "session_sticky_keys",
    "models",
  ]);
  assert.equal(primary[0].required, true);
  assert.equal(primary[0].label, "API Keys");
  assert.equal(primary[0].control, "keys");
  assert.equal(advanced[1].control, "checkbox");
});

test("session stickiness defaults on and can be disabled", () => {
  assert.equal(defaultProviderCredentialForm().session_sticky_keys, true);
  assert.equal(providerCredentialRowToForm({ session_sticky_keys: false }).session_sticky_keys, false);

  const form = {
    ...defaultProviderCredentialForm(),
    name: "my-openai",
    type: "openai",
    api_keys: [{ value: "sk-live" }],
    session_sticky_keys: false,
  };
  assert.equal(buildProviderCredentialPayload(form, OPENAI_SCHEMA).session_sticky_keys, false);
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

test("providerCredentialFieldMeta resolves translations on access", () => {
  overwriteGetLocale(() => "pl");
  try {
    assert.equal(providerCredentialFieldMeta("base_url").label, "Bazowy URL");
  } finally {
    overwriteGetLocale(() => "en");
  }
  assert.equal(providerCredentialFieldMeta("base_url").label, "Base URL");
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

test("providerCredentialTypeOptions lists every server-supplied provider type", () => {
  const recentProviderSchemas = ["chutes", "cohere", "llmd", "sglang"].map((type) => ({
    type,
    fields: [],
  }));

  assert.deepEqual(providerCredentialTypeOptions(recentProviderSchemas, ""), [
    "chutes",
    "cohere",
    "llmd",
    "sglang",
  ]);
});

test("splitCommaList trims and drops empties", () => {
  assert.deepEqual(
    splitCommaList(" gpt-4o, gpt-4o-mini ,,"),
    ["gpt-4o", "gpt-4o-mini"],
  );
  assert.deepEqual(splitCommaList(""), []);
});

test("providerRowsHaveActions is false when every provider is managed", () => {
  assert.equal(providerRowsHaveActions([]), false);
  assert.equal(providerRowsHaveActions([{ name: "openai", managed: true }]), false);
  assert.equal(
    providerRowsHaveActions([{ name: "openai", managed: true }, { name: "mine", managed: false }]),
    true,
  );
  assert.equal(providerRowsHaveActions(undefined), false);
});

// --- Quota breaker trip rules ---

test("parseGoDuration mirrors time.ParseDuration for the strings operators type", () => {
  const NANO = 1;
  assert.equal(parseGoDuration("15m"), 15 * 60 * 1e9);
  assert.equal(parseGoDuration("1h"), 3600 * 1e9);
  assert.equal(parseGoDuration("1h30m"), 5400 * 1e9);
  assert.equal(parseGoDuration("90s"), 90 * 1e9);
  assert.equal(parseGoDuration("500ms"), 5e8);
  assert.equal(parseGoDuration("2h45m"), 9900 * 1e9);
  assert.equal(parseGoDuration("0"), 0);
  assert.equal(parseGoDuration("-500ms"), -5e8);
  assert.equal(parseGoDuration("1.5h"), 5400 * 1e9);
  assert.equal(parseGoDuration(" 15m "), 900 * 1e9);

  // Go's exact strings round-trip through both directions.
  assert.equal(parseGoDuration(formatGoDurationNs(900 * 1e9)), 900 * 1e9);

  // Invalid: missing unit, empty, unknown suffix, trailing junk.
  assert.equal(parseGoDuration(""), null);
  assert.equal(parseGoDuration("15"), null);
  assert.equal(parseGoDuration("abc"), null);
  assert.equal(parseGoDuration("15x"), null);
  assert.equal(parseGoDuration("15m30"), null);
  assert.equal(parseGoDuration(null), null);
  assert.equal(parseGoDuration(undefined), null);
});

test("formatGoDurationNs renders durations the way Go's Duration.String does", () => {
  assert.equal(formatGoDurationNs(0), "0s");
  assert.equal(formatGoDurationNs(900 * 1e9), "15m0s");
  assert.equal(formatGoDurationNs(3600 * 1e9), "1h0m0s");
  assert.equal(formatGoDurationNs(5400 * 1e9), "1h30m0s");
  assert.equal(formatGoDurationNs(90 * 1e9), "1m30s");
  assert.equal(formatGoDurationNs(45 * 1e9), "45s");
  assert.equal(formatGoDurationNs(5e8), "500ms");
  assert.equal(formatGoDurationNs(15e5), "1.5ms");
  assert.equal(formatGoDurationNs(15e2), "1.5µs");
  assert.equal(formatGoDurationNs(42), "42ns");
  assert.equal(formatGoDurationNs(-5e8), "-500ms");
  assert.equal(formatGoDurationNs("not a number"), "");
  // A missing ttl decodes to 0 and displays as Go's zero duration.
  assert.equal(formatGoDurationNs(null), "0s");
  assert.equal(formatGoDurationNs(undefined), "");
});

test("tripRulesToRows converts the view's nanosecond ttl into duration strings", () => {
  assert.deepEqual(
    tripRulesToRows([
      { match: "insufficient_quota", ttl: 900 * 1e9 },
      { match: "rate limit", ttl: 60000000000 },
    ]),
    [
      { match: "insufficient_quota", ttl: "15m0s" },
      { match: "rate limit", ttl: "1m0s" },
    ],
  );
  assert.deepEqual(tripRulesToRows(undefined), []);
  assert.deepEqual(tripRulesToRows("junk"), []);
});

test("tripRuleRowsToWire builds the {match, ttl} payload with nanosecond ttls", () => {
  const wire = tripRuleRowsToWire([
    { match: " insufficient_quota ", ttl: " 15m " },
    { match: "", ttl: "" },
    { match: "rate limit", ttl: "1h30m" },
  ]);

  assert.deepEqual(wire, [
    { match: "insufficient_quota", ttl: 900 * 1e9 },
    { match: "rate limit", ttl: 5400 * 1e9 },
  ]);

  // An empty list is fine: rules are optional.
  assert.deepEqual(tripRuleRowsToWire([]), []);
  assert.deepEqual(tripRuleRowsToWire(undefined), []);
  assert.deepEqual(tripRuleRowsToWire([{ match: "", ttl: "" }]), []);

  // A filled row with an unparsable ttl is reported, not guessed at.
  assert.equal(tripRuleRowsToWire([{ match: "quota", ttl: "soon" }]), null);
});

test("validation rejects blank trip-rule rows and half-filled or invalid rules", () => {
  const form = (trip_on) => ({
    ...defaultProviderCredentialForm(),
    name: "my-openai",
    type: "openai",
    api_keys: [{ value: "sk-live" }],
    trip_on,
  });

  assert.equal(validateProviderCredentialForm(form([]), "create", [], OPENAI_SCHEMA).trip_on, undefined);
  assert.equal(
    validateProviderCredentialForm(form([{ match: "", ttl: "" }]), "create", [], OPENAI_SCHEMA).trip_on,
    "Remove the empty row instead of leaving a rule blank.",
  );
  assert.equal(
    validateProviderCredentialForm(form([{ match: "", ttl: "15m" }]), "create", [], OPENAI_SCHEMA).trip_on,
    "Match is required when a TTL is set.",
  );
  assert.equal(
    validateProviderCredentialForm(form([{ match: "quota", ttl: "" }]), "create", [], OPENAI_SCHEMA).trip_on,
    undefined,
  );
  assert.match(
    validateProviderCredentialForm(form([{ match: "quota", ttl: "soon" }]), "create", [], OPENAI_SCHEMA).trip_on,
    /duration like 15m/,
  );
});

test("trip_on round-trips from a stored row through the form into the PUT payload", () => {
  const row = {
    name: "my-openai",
    type: "openai",
    api_keys: ["***********"],
    trip_on: [
      { match: "insufficient_quota", ttl: 900 * 1e9 },
      { match: "rate limit", ttl: 60000000000 },
    ],
    managed: false,
    enabled: true,
  };

  const form = providerCredentialRowToForm(row);
  assert.deepEqual(form.trip_on, [
    { match: "insufficient_quota", ttl: "15m0s" },
    { match: "rate limit", ttl: "1m0s" },
  ]);

  const body = buildProviderCredentialPayload(form, OPENAI_SCHEMA);
  assert.deepEqual(body.trip_on, [
    { match: "insufficient_quota", ttl: 900 * 1e9 },
    { match: "rate limit", ttl: 60000000000 },
  ]);
});

test("an empty trip_on list is always in the payload so clearing rules works", () => {
  const body = buildProviderCredentialPayload(defaultProviderCredentialForm(), OPENAI_SCHEMA);
  assert.deepEqual(body.trip_on, []);
});

test("match-only rows are valid and produce ttl: 0 in the wire payload", () => {
  const form = (trip_on) => ({
    ...defaultProviderCredentialForm(),
    name: "my-openai",
    type: "openai",
    api_keys: [{ value: "sk-live" }],
    trip_on,
  });

  // Match-only row passes validation.
  assert.equal(
    validateProviderCredentialForm(form([{ match: "quota" }]), "create", [], OPENAI_SCHEMA).trip_on,
    undefined,
  );

  // Wire payload carries ttl: 0 for a match-only row.
  const wire = tripRuleRowsToWire([{ match: "quota", ttl: "" }]);
  assert.deepEqual(wire, [{ match: "quota", ttl: 0 }]);
});

test("changing provider type keeps trip rules, like the other identity values", () => {
  const typed = {
    ...defaultProviderCredentialForm(),
    name: "my-openai",
    type: "openai",
    api_keys: [{ value: "sk-live" }],
    trip_on: [{ match: "insufficient_quota", ttl: "15m" }],
    vertex_project: "left-over",
  };

  const form = resetProviderCredentialFields(
    typed,
    providerCredentialFormFields(OPENAI_SCHEMA),
  );

  assert.equal(form.vertex_project, "");
  assert.deepEqual(form.trip_on, [{ match: "insufficient_quota", ttl: "15m" }]);
});

test("trip-rule list helpers summarize rows and gate the read-only column", () => {
  const quota = 900 * 1e9;
  const row = {
    name: "dash-openai",
    managed: true,
    trip_on: [
      { match: "insufficient_quota", ttl: quota },
      { match: "rate limit", ttl: 60 * 1e9 },
    ],
  };

  // Read-only rendering: every row's rules are shown in the list, declared
  // or managed.
  assert.equal(
    providerCredentialTripRulesLabel(row),
    "insufficient_quota (15m0s), rate limit (1m0s)",
  );
  assert.equal(providerCredentialTripRulesLabel({ trip_on: [] }), "");
  assert.equal(providerCredentialTripRulesLabel({}), "");
  assert.equal(providerCredentialTripRulesLabel(null), "");

  // ttl 0 means "use breaker timeout"; the label shows match only, no (0s).
  assert.equal(
    providerCredentialTripRulesLabel({
      trip_on: [{ match: "insufficient_quota", ttl: 0 }],
    }),
    "insufficient_quota",
  );

  assert.equal(providerRowsHaveTripRules([row, { trip_on: [] }]), true);
  assert.equal(providerRowsHaveTripRules([{ trip_on: [] }, {}]), false);
  assert.equal(providerRowsHaveTripRules([]), false);
  assert.equal(providerRowsHaveTripRules(undefined), false);
});
