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
import * as m from "../../lib/paraglide/messages.js";

export { splitCommaList };

// Field names, mirroring internal/providers/credential_schema.go.
export const FIELD_API_KEYS = "api_keys";
export const FIELD_SESSION_STICKY_KEYS = "session_sticky_keys";
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
    label: m.providers_api_keys(),
    control: "keys",
    hint: m.providers_api_keys_hint(),
  },
  [FIELD_SESSION_STICKY_KEYS]: {
    label: m.providers_session_sticky_keys(),
    control: "checkbox",
    hint: m.providers_session_sticky_hint(),
  },
  [FIELD_BASE_URL]: {
    label: m.providers_base_url(),
    control: "text",
  },
  api_version: {
    label: m.providers_api_version(),
    control: "text",
    placeholder: "e.g. 2024-10-01-preview",
    hint: m.providers_api_version_hint(),
  },
  backend: {
    label: m.providers_backend(),
    control: "select",
    hint: m.providers_backend_hint(),
  },
  auth_type: {
    label: m.providers_auth_type(),
    control: "select",
    hint: m.providers_auth_type_hint(),
  },
  api_mode: {
    label: m.providers_api_mode(),
    control: "select",
    hint: m.providers_api_mode_hint(),
  },
  vertex_project: {
    label: m.providers_vertex_project(),
    control: "text",
    placeholder: "my-gcp-project",
  },
  vertex_location: {
    label: m.providers_vertex_location(),
    control: "text",
    placeholder: "us-central1",
  },
  service_account_file: {
    label: m.providers_service_account_file(),
    control: "text",
    placeholder: "/path/to/service-account.json",
    hint: m.providers_service_account_file_hint(),
  },
  [FIELD_SERVICE_ACCOUNT_JSON]: {
    label: m.providers_service_account_json(),
    control: "textarea",
    placeholder: m.providers_paste_service_account(),
    hint: m.providers_service_account_json_hint(),
  },
  service_account_json_base64: {
    label: m.providers_service_account_json_base64(),
    control: "text",
    hint: m.providers_service_account_json_base64_hint(),
  },
  gcp_scope: {
    label: m.providers_gcp_scope(),
    control: "text",
    placeholder: "https://www.googleapis.com/auth/cloud-platform",
  },
  [FIELD_MODELS]: {
    label: m.providers_models_field(),
    control: "text",
    placeholder: "gpt-4o, gpt-4o-mini",
    hint: m.providers_models_hint(),
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
    session_sticky_keys: true,
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
      field.hint = m.providers_default_value({ value: fallbackBaseURL });
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
    return keyCount === 1
      ? m.providers_keys_count({ count: keyCount })
      : m.providers_keys_count_plural({ count: keyCount });
  }
  if (
    String((row && row.service_account_json) || "").trim() ||
    String((row && row.service_account_json_base64) || "").trim() ||
    String((row && row.service_account_file) || "").trim()
  ) {
    return m.providers_service_account();
  }
  if (String((row && row.vertex_project) || "").trim()) {
    return "ADC";
  }
  return m.providers_keyless();
}

// providerCredentialModelsLabel reports the configured model count, or that
// models are auto-discovered from the provider's /models endpoint.
export function providerCredentialModelsLabel(row) {
  const models = Array.isArray(row && row.models) ? row.models : [];
  if (models.length === 0) {
    return m.providers_auto_discovered();
  }
  return models.length === 1
    ? m.providers_models_count({ count: models.length })
    : m.providers_models_count_plural({ count: models.length });
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
    session_sticky_keys: !row || row.session_sticky_keys !== false,
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
    errors.type = m.providers_type_required();
  }
  if (!name) {
    errors.name = m.providers_name_required();
  } else if (name.includes("/")) {
    errors.name = m.providers_name_slash();
  } else if (
    mode === "create" &&
    (Array.isArray(existingRows) ? existingRows : []).some(
      (row) => String((row && row.name) || "").trim() === name,
    )
  ) {
    errors.name = m.providers_already_exists({ name });
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
      return m.providers_key_required();
    }
    if (keys.some((value) => !value.trim())) {
      return m.providers_key_blank();
    }
    return "";
  }

  const value = String((form && form[field.name]) || "").trim();
  if (field.required && !value) {
    return m.providers_field_required({ field: field.label });
  }
  if (!value) {
    return "";
  }
  if (field.name === FIELD_BASE_URL && !value.includes("://") && /[./]/.test(value)) {
    return m.providers_url_scheme({ value });
  }
  if (field.name === FIELD_SERVICE_ACCOUNT_JSON && !isRedactedCredentialValue(value)) {
    try {
      JSON.parse(value);
    } catch {
      return m.providers_json_invalid();
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
  const hasAdvertisedFields = Boolean(
    schema && Array.isArray(schema.fields) && schema.fields.length > 0,
  );
  const rendered = new Set();
  for (const field of [...primary, ...advanced]) {
    // A missing schema is the compatibility fallback for gateways that predate
    // this capability. Do not send the new toggle unless the gateway explicitly
    // advertised it.
    if (!hasAdvertisedFields && field.name === FIELD_SESSION_STICKY_KEYS) {
      continue;
    }
    rendered.add(field.name);
    payload[field.name] = providerCredentialPayloadValue(form, field.name);
  }
  for (const name of Object.keys(PROVIDER_CREDENTIAL_FIELDS)) {
    if (rendered.has(name)) {
      continue;
    }
    // This is a provider capability toggle rather than free-form stored data.
    // Do not send it to keyless schemas (or older gateways whose schema does
    // not advertise it), where its default true value would be spurious.
    if (name === FIELD_SESSION_STICKY_KEYS) {
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
    case FIELD_SESSION_STICKY_KEYS:
      return Boolean(form && form.session_sticky_keys);
    // Raw JSON must survive verbatim: trimming would still parse, but the
    // stored value should be exactly what the operator pasted.
    case FIELD_SERVICE_ACCOUNT_JSON:
      return (form && form.service_account_json) || "";
    default:
      return String((form && form[name]) || "").trim();
  }
}
