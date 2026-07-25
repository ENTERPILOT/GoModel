// Pure logic for the Providers (provider credentials) page, kept free of DOM
// and Svelte-runtime dependencies so it can be unit-tested directly.
//
// The editor form is driven by the per-type credential schemas served by
// GET /admin/provider-credentials/types: the gateway knows which fields each
// provider type actually reads, so the form offers exactly those and nothing
// else. Field names are shared across the schema, the PUT payload, and the
// `param` of a validation error, which is what lets a server-side rejection
// land on the input that caused it.

import { splitCommaList } from "../../lib/utils/format.js";

export { splitCommaList };

// Field names, mirroring internal/providers/credential_schema.go.
export const FIELD_API_KEYS = "api_keys";
export const FIELD_BASE_URL = "base_url";
export const FIELD_SERVICE_ACCOUNT_JSON = "service_account_json";
export const FIELD_MODELS = "models";

// PROVIDER_CREDENTIAL_FIELDS is the presentation of each credential field:
// what to call it, which control to render, and what to say about it. The
// gateway decides *whether* a field applies to a type; this decides how it
// looks. A field the gateway grows before this map knows about it still
// renders, as a labelled text input (see providerCredentialFieldMeta).
export const PROVIDER_CREDENTIAL_FIELDS = {
  [FIELD_API_KEYS]: {
    label: "API Keys",
    control: "keys",
    hint: "Multiple keys rotate round-robin. Saved values are shown as ***********; leave the asterisks unchanged to keep the stored key.",
  },
  [FIELD_BASE_URL]: {
    label: "Base URL",
    control: "text",
  },
  api_version: {
    label: "API Version",
    control: "text",
    placeholder: "e.g. 2024-10-01-preview",
    hint: "Leave empty for the provider default. Realtime endpoints may need a newer version.",
  },
  backend: {
    label: "Backend",
    control: "select",
    hint: "Which Google surface to call. Vertex authenticates with Google credentials instead of an API key.",
  },
  auth_type: {
    label: "Auth Type",
    control: "select",
    hint: "How to obtain Google credentials. Leave on the default to use Application Default Credentials.",
  },
  api_mode: {
    label: "API Mode",
    control: "select",
    hint: "Which request shape to send upstream.",
  },
  vertex_project: {
    label: "Vertex Project",
    control: "text",
    placeholder: "my-gcp-project",
  },
  vertex_location: {
    label: "Vertex Location",
    control: "text",
    placeholder: "us-central1",
  },
  service_account_file: {
    label: "Service Account File",
    control: "text",
    placeholder: "/path/to/service-account.json",
    hint: "Path readable by the gateway process.",
  },
  [FIELD_SERVICE_ACCOUNT_JSON]: {
    label: "Service Account JSON",
    control: "textarea",
    placeholder: "Paste service account JSON",
    hint: "Saved values are shown as ***********; leave the asterisks unchanged to keep the stored value, or clear it to remove.",
  },
  service_account_json_base64: {
    label: "Service Account JSON (base64)",
    control: "text",
    hint: "Saved values are shown as ***********; leave the asterisks unchanged to keep the stored value.",
  },
  gcp_scope: {
    label: "GCP Scope",
    control: "text",
    placeholder: "https://www.googleapis.com/auth/cloud-platform",
  },
  [FIELD_MODELS]: {
    label: "Models (comma-separated)",
    control: "text",
    placeholder: "gpt-4o, gpt-4o-mini",
    hint: "Leave empty to auto-discover models from the provider's /models endpoint where supported.",
  },
};

// providerCredentialFieldMeta describes one field, falling back to a
// humanized label so an unknown field is still usable rather than invisible.
export function providerCredentialFieldMeta(name) {
  const known = PROVIDER_CREDENTIAL_FIELDS[name];
  if (known) {
    return known;
  }
  return {
    label: String(name || "")
      .split("_")
      .filter(Boolean)
      .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
      .join(" "),
    control: "text",
  };
}

// defaultProviderCredentialForm returns a blank editor form. It carries every
// field the wire format has; which of them the operator sees comes from the
// selected type's schema.
export function defaultProviderCredentialForm() {
  return {
    name: "",
    type: "",
    api_keys: [],
    base_url: "",
    api_version: "",
    backend: "",
    auth_type: "",
    api_mode: "",
    vertex_project: "",
    vertex_location: "",
    service_account_file: "",
    service_account_json: "",
    service_account_json_base64: "",
    gcp_scope: "",
    models: "",
    enabled: true,
  };
}

// filterProviderCredentials matches rows by name, type, or base URL
// (case-insensitive substring).
export function filterProviderCredentials(rows, filter) {
  const list = Array.isArray(rows) ? rows : [];
  if (!filter) {
    return list;
  }
  const needle = String(filter).toLowerCase();
  return list.filter((row) => {
    const fields = [row.name, row.type, row.base_url];
    return fields.some((value) =>
      String(value || "")
        .toLowerCase()
        .includes(needle),
    );
  });
}

// providerCredentialTypeOptions lists the selectable type names, always
// including the current selection even if it isn't (yet, or anymore) in the
// fetched schema list — e.g. while the schemas are still loading.
export function providerCredentialTypeOptions(schemas, currentType) {
  const list = (Array.isArray(schemas) ? schemas : [])
    .map((schema) => String((schema && schema.type) || "").trim())
    .filter(Boolean);
  const current = String(currentType || "").trim();
  if (current && !list.includes(current)) {
    list.push(current);
  }
  return list;
}

// providerCredentialSchema returns the credential schema of one type, or null
// when the schemas haven't loaded (or the type is unknown to this gateway).
export function providerCredentialSchema(schemas, type) {
  const wanted = String(type || "").trim();
  if (!wanted) {
    return null;
  }
  const match = (Array.isArray(schemas) ? schemas : []).find(
    (schema) => String((schema && schema.type) || "").trim() === wanted,
  );
  return match || null;
}

// providerCredentialFormFields resolves a schema into ready-to-render field
// descriptors, split into the fields worth showing up front and the ones
// folded behind "Advanced settings". A missing schema falls back to every
// known field, so a gateway that cannot serve schemas still gets a usable
// (if unfiltered) form.
export function providerCredentialFormFields(schema, defaultBaseURL) {
  const entries =
    schema && Array.isArray(schema.fields) && schema.fields.length > 0
      ? schema.fields
      : Object.keys(PROVIDER_CREDENTIAL_FIELDS).map((name) => ({
          name,
          advanced: name !== FIELD_API_KEYS,
        }));
  const fallbackBaseURL = defaultBaseURL || (schema && schema.default_base_url) || "";

  const primary = [];
  const advanced = [];
  for (const entry of entries) {
    const name = String((entry && entry.name) || "").trim();
    if (!name) {
      continue;
    }
    // Presentation first, then the schema's facts on top: what a field is
    // called and how it looks is this module's call, but whether it is
    // required and what it accepts is only ever the gateway's.
    const field = {
      ...providerCredentialFieldMeta(name),
      name,
      required: Boolean(entry && entry.required),
      options: Array.isArray(entry && entry.options) ? entry.options : [],
    };
    if (name === FIELD_BASE_URL && fallbackBaseURL) {
      field.placeholder = fallbackBaseURL;
      field.hint = "Defaults to " + fallbackBaseURL;
    }
    if (field.options.length > 0) {
      field.control = "select";
    }
    (entry && entry.advanced ? advanced : primary).push(field);
  }
  return { primary, advanced };
}

// Form values that identify the provider rather than configure its type, so a
// change of type leaves them alone.
const IDENTITY_FIELDS = new Set(["name", "type", "enabled"]);

// resetProviderCredentialFields puts every value the given fields do not
// render back to its default. Changing type changes which fields exist, and
// buildProviderCredentialPayload deliberately echoes back values the form does
// not render — right when editing a stored row, wrong for something typed
// under the type just abandoned, which would reach the gateway with nothing on
// screen to explain it.
export function resetProviderCredentialFields(form, fields) {
  const rendered = new Set(
    [...(fields.primary || []), ...(fields.advanced || [])].map((field) => field.name),
  );
  const blank = defaultProviderCredentialForm();
  const next = { ...form };
  for (const name of Object.keys(blank)) {
    if (!IDENTITY_FIELDS.has(name) && !rendered.has(name)) {
      next[name] = blank[name];
    }
  }
  return next;
}

// providerCredentialAuthLabel infers the auth mode of a row from which
// credential fields are populated.
export function providerCredentialAuthLabel(row) {
  const keyCount = Array.isArray(row && row.api_keys) ? row.api_keys.length : 0;
  if (keyCount > 0) {
    return keyCount + " key" + (keyCount === 1 ? "" : "s");
  }
  if (
    String((row && row.service_account_json) || "").trim() ||
    String((row && row.service_account_json_base64) || "").trim() ||
    String((row && row.service_account_file) || "").trim()
  ) {
    return "service account";
  }
  if (String((row && row.vertex_project) || "").trim()) {
    return "ADC";
  }
  return "keyless";
}

// providerCredentialModelsLabel reports the configured model count, or that
// models are auto-discovered from the provider's /models endpoint.
export function providerCredentialModelsLabel(row) {
  const models = Array.isArray(row && row.models) ? row.models : [];
  if (models.length === 0) {
    return "auto-discovered";
  }
  return models.length + " model" + (models.length === 1 ? "" : "s");
}

// providerCredentialKeysToRows converts a stored api_keys array (usually all
// "***********" masks) into editable {value} rows.
export function providerCredentialKeysToRows(apiKeys) {
  return (Array.isArray(apiKeys) ? apiKeys : []).map((value) => ({
    value: String(value || ""),
  }));
}

// providerCredentialKeyRowsToArray flattens editor rows back into the wire
// array. Values are NOT trimmed: an untouched "***********" mask must be sent
// verbatim so the server preserves the stored key at that position.
export function providerCredentialKeyRowsToArray(rows) {
  return (Array.isArray(rows) ? rows : []).map((row) =>
    String((row && row.value) || ""),
  );
}

// suggestProviderCredentialName proposes a free provider name for the given
// type: the bare type name if no provider (declared or dashboard-managed)
// already uses it, otherwise "{type}-1", "{type}-2", ... picking the first
// unused suffix.
export function suggestProviderCredentialName(rows, type) {
  const base = String(type || "").trim();
  if (!base) {
    return "";
  }
  const taken = new Set(
    (Array.isArray(rows) ? rows : []).map((row) =>
      String((row && row.name) || "").trim(),
    ),
  );
  if (!taken.has(base)) {
    return base;
  }
  let n = 1;
  while (taken.has(base + "-" + n)) {
    n += 1;
  }
  return base + "-" + n;
}

// providerCredentialRowToForm prefills the editor form from a fetched row.
export function providerCredentialRowToForm(row) {
  return {
    name: String((row && row.name) || "").trim(),
    type: String((row && row.type) || "").trim(),
    api_keys: providerCredentialKeysToRows(row && row.api_keys),
    base_url: String((row && row.base_url) || ""),
    api_version: String((row && row.api_version) || ""),
    backend: String((row && row.backend) || ""),
    auth_type: String((row && row.auth_type) || ""),
    api_mode: String((row && row.api_mode) || ""),
    vertex_project: String((row && row.vertex_project) || ""),
    vertex_location: String((row && row.vertex_location) || ""),
    service_account_file: String((row && row.service_account_file) || ""),
    service_account_json: String((row && row.service_account_json) || ""),
    service_account_json_base64: String(
      (row && row.service_account_json_base64) || "",
    ),
    gcp_scope: String((row && row.gcp_scope) || ""),
    models: (Array.isArray(row && row.models) ? row.models : []).join(", "),
    enabled: !row || row.enabled !== false,
  };
}

// isRedactedCredentialValue recognizes the server's secret placeholder, which
// must round-trip untouched rather than be validated as a real value.
function isRedactedCredentialValue(value) {
  const trimmed = String(value || "").trim();
  return trimmed.length >= 3 && /^\*+$/.test(trimmed);
}

// validateProviderCredentialForm returns a {field: message} map, empty when
// the form can be submitted. Only rules the form can decide on its own live
// here — anything needing provider knowledge (which field combinations
// actually authenticate) is left to the gateway, which reports it back
// against the same field names.
export function validateProviderCredentialForm(form, mode, existingRows, schema) {
  const errors = {};
  const name = String((form && form.name) || "").trim();
  const type = String((form && form.type) || "").trim();

  if (!type) {
    errors.type = "Select a provider type.";
  }
  if (!name) {
    errors.name = "Name is required.";
  } else if (name.includes("/")) {
    errors.name = "Name cannot contain '/' — it separates the provider from the model.";
  } else if (
    mode === "create" &&
    (Array.isArray(existingRows) ? existingRows : []).some(
      (row) => String((row && row.name) || "").trim() === name,
    )
  ) {
    errors.name = 'Provider "' + name + '" already exists.';
  }

  const { primary, advanced } = providerCredentialFormFields(schema);
  for (const field of [...primary, ...advanced]) {
    const message = validateProviderCredentialField(form, field);
    if (message) {
      errors[field.name] = message;
    }
  }
  return errors;
}

// validateProviderCredentialField checks one field of the form against its
// schema entry.
function validateProviderCredentialField(form, field) {
  if (field.name === FIELD_API_KEYS) {
    const keys = providerCredentialKeyRowsToArray(form && form.api_keys);
    // "no key at all" first: a required field left as one blank row is a
    // missing key, not a stray row to tidy up.
    if (field.required && !keys.some((value) => value.trim())) {
      return "At least one API key is required for this provider type.";
    }
    if (keys.some((value) => !value.trim())) {
      return "Remove the empty row instead of leaving a key blank.";
    }
    return "";
  }

  const value = String((form && form[field.name]) || "").trim();
  if (field.required && !value) {
    return field.label + " is required for this provider type.";
  }
  if (!value) {
    return "";
  }
  if (field.name === FIELD_BASE_URL && !value.includes("://") && /[./]/.test(value)) {
    return "Include the scheme, e.g. https://" + value;
  }
  if (field.name === FIELD_SERVICE_ACCOUNT_JSON && !isRedactedCredentialValue(value)) {
    try {
      JSON.parse(value);
    } catch {
      return "Paste the service account JSON file's contents — this is not valid JSON.";
    }
  }
  // Enumerated fields are not checked against their options: the schema lists
  // the canonical values a form should offer, while providers accept further
  // spellings of each, and a stored one of those must survive an edit.
  return "";
}

// buildProviderCredentialPayload normalizes the editor form into the PUT
// /admin/provider-credentials body. Most fields are trimmed; api_keys and
// service_account_json are sent verbatim (mask preservation / raw JSON).
//
// PUT replaces the whole row, so every field the form renders is sent, empty
// or not — that is how clearing a value works. A field the type's schema does
// not render is sent back only when the row already had a value there: the
// operator cannot see it to confirm dropping it, and a field missing from a
// schema by mistake is precisely the one whose loss would be silent.
export function buildProviderCredentialPayload(form, schema) {
  const payload = {
    name: String((form && form.name) || "").trim(),
    type: String((form && form.type) || "").trim(),
    enabled: Boolean(form && form.enabled),
  };
  const { primary, advanced } = providerCredentialFormFields(schema);
  const rendered = new Set();
  for (const field of [...primary, ...advanced]) {
    rendered.add(field.name);
    payload[field.name] = providerCredentialPayloadValue(form, field.name);
  }
  for (const name of Object.keys(PROVIDER_CREDENTIAL_FIELDS)) {
    if (rendered.has(name)) {
      continue;
    }
    const value = providerCredentialPayloadValue(form, name);
    if (Array.isArray(value) ? value.length > 0 : String(value).trim() !== "") {
      payload[name] = value;
    }
  }
  return payload;
}

// providerCredentialPayloadValue converts one form field to its wire value.
function providerCredentialPayloadValue(form, name) {
  switch (name) {
    case FIELD_API_KEYS:
      return providerCredentialKeyRowsToArray(form && form.api_keys);
    case FIELD_MODELS:
      return splitCommaList(form && form.models);
    // Raw JSON must survive verbatim: trimming would still parse, but the
    // stored value should be exactly what the operator pasted.
    case FIELD_SERVICE_ACCOUNT_JSON:
      return (form && form.service_account_json) || "";
    default:
      return String((form && form[name]) || "").trim();
  }
}
