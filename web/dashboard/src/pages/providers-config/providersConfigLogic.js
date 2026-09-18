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

// Resolve translated metadata on access so locale changes cannot leave stale
// labels, hints, or placeholders. The metadata describes each credential field:
// what to call it, which control to render, and what to say about it. The
// gateway decides *whether* a field applies to a type; this decides how it
// looks. A field the gateway grows before this map knows about it still
// renders, as a labelled text input (see providerCredentialFieldMeta).
const PROVIDER_CREDENTIAL_FIELD_NAMES = [
  FIELD_API_KEYS,
  FIELD_SESSION_STICKY_KEYS,
  FIELD_BASE_URL,
  "api_version",
  "backend",
  "auth_type",
  "api_mode",
  "vertex_project",
  "vertex_location",
  "service_account_file",
  FIELD_SERVICE_ACCOUNT_JSON,
  "service_account_json_base64",
  "gcp_scope",
  FIELD_MODELS,
];

function providerCredentialFields() {
  return {
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
}

// providerCredentialFieldMeta describes one field, falling back to a
// humanized label so an unknown field is still usable rather than invisible.
export function providerCredentialFieldMeta(name) {
  const known = providerCredentialFields()[name];
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
    // Quota breaker trip rules as editable rows; ttl stays the Go duration
    // string the operator typed and converts at the payload boundary.
    trip_on: [],
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
      : PROVIDER_CREDENTIAL_FIELD_NAMES.map((name) => ({
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
// change of type leaves them alone. trip_on joins them: quota-breaker rules
// apply to the provider as a whole, whatever type authenticates it.
const IDENTITY_FIELDS = new Set(["name", "type", "enabled", "trip_on"]);

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
    return m.providers_keys_count({ count: keyCount });
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
  return m.providers_models_count({ count: models.length });
}

// providerCredentialKeysToRows converts a stored api_keys array (usually all
// "***********" masks) into editable {value} rows.
export function providerCredentialKeysToRows(apiKeys) {
  return (Array.isArray(apiKeys) ? apiKeys : []).map((value) => ({
    value: String(value || ""),
  }));
}

// --- Quota breaker trip rules ---
//
// The wire format carries trip rules as {match, ttl} with ttl in integer
// nanoseconds (Go's time.Duration JSON encoding). The editor keeps ttl as
// the Go duration string the operator typed ("15m", "1h30m", "90s") and
// converts at the payload boundary.

const GO_DURATION_UNITS = {
  ns: 1,
  us: 1000,
  "µs": 1000,
  "μs": 1000,
  ms: 1e6,
  s: 1e9,
  m: 60e9,
  h: 3600e9,
};

// parseGoDuration converts a Go duration string to integer nanoseconds,
// mirroring time.ParseDuration ("15m", "1h30m", "90s", "-500ms", "0").
// Returns null when the text is not a valid duration.
export function parseGoDuration(text) {
  let rest = String(text ?? "").trim();
  if (!rest) return null;
  let neg = false;
  const sign = rest.charAt(0);
  if (sign === "-" || sign === "+") {
    neg = sign === "-";
    rest = rest.slice(1);
  }
  // A lone "0" is Go's zero-duration special case.
  if (rest === "0") return 0;
  if (!rest) return null;
  let total = 0;
  while (rest) {
    // A fresh sticky regex anchors at the slice's start each iteration.
    const match = /(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/y.exec(rest);
    if (!match || match.index !== 0) return null;
    const value = Number.parseFloat(match[1]);
    if (!Number.isFinite(value)) return null;
    total += value * GO_DURATION_UNITS[match[2]];
    rest = rest.slice(match[0].length);
  }
  if (!Number.isFinite(total)) return null;
  // Fractions below a nanosecond truncate, as in Go.
  const ns = Math.floor(total);
  return neg ? -ns : ns;
}

// formatGoDurationNs renders integer nanoseconds the way Go's
// time.Duration.String() does ("15m0s", "1h30m0s", "90s", "1.5ms", "0s").
// Returns "" for values it cannot represent.
export function formatGoDurationNs(ns) {
  const value = Number(ns);
  if (!Number.isFinite(value)) return "";
  const neg = value < 0;
  const u = Math.abs(Math.trunc(value));
  let out;
  if (u === 0) {
    out = "0s";
  } else if (u < 1e3) {
    out = u + "ns";
  } else if (u < 1e6) {
    out = u / 1e3 + "µs";
  } else if (u < 1e9) {
    out = u / 1e6 + "ms";
  } else {
    const frac = u % 1e9;
    const totalSeconds = (u - frac) / 1e9;
    const seconds = totalSeconds % 60;
    let rest = (totalSeconds - seconds) / 60;
    const fraction =
      frac === 0
        ? ""
        : "." + String(frac).padStart(9, "0").replace(/0+$/, "");
    out = seconds + fraction + "s";
    if (rest > 0) {
      const minutes = rest % 60;
      rest = (rest - minutes) / 60;
      out = minutes + "m" + out;
      if (rest > 0) {
        out = rest + "h" + out;
      }
    }
  }
  return (neg ? "-" : "") + out;
}

// tripRulesToRows converts a view row's trip_on array ({match, ttl} with ttl
// in nanoseconds) into editable rows whose ttl is a Go duration string.
export function tripRulesToRows(tripOn) {
  return (Array.isArray(tripOn) ? tripOn : []).map((rule) => ({
    match: String((rule && rule.match) || ""),
    // ttl 0 or absent means "use breaker timeout"; show a blank field, not "0s".
    ttl: rule && rule.ttl ? formatGoDurationNs(rule.ttl) : "",
  }));
}

// tripRuleRowsToWire converts editor rows into the wire trip_on array
// ({match, ttl} with ttl in nanoseconds), trimming the match and dropping
// rows the operator left completely empty. Returns null when a filled row's
// ttl is not a valid Go duration. An empty list is valid: no rules.
export function tripRuleRowsToWire(rows) {
  const wire = [];
  for (const row of Array.isArray(rows) ? rows : []) {
    const match = String((row && row.match) || "").trim();
    const ttlText = String((row && row.ttl) || "").trim();
    if (!match && !ttlText) {
      continue;
    }
    // TTL is optional: empty string means use breaker timeout (encode as 0).
    const ttl = ttlText ? parseGoDuration(ttlText) : 0;
    if (ttl === null) {
      return null;
    }
    wire.push({ match, ttl });
  }
  return wire;
}

// validateTripRuleRows returns the editor message for the first trip-rule
// problem, or "" when the rules can be submitted.
function validateTripRuleRows(rows) {
  for (const row of Array.isArray(rows) ? rows : []) {
    const match = String((row && row.match) || "").trim();
    const ttlText = String((row && row.ttl) || "").trim();
    if (!match && !ttlText) {
      return m.providers_trip_on_row_blank();
    }
    if (!match) {
      return m.providers_trip_on_match_required();
    }
    // TTL is optional; when omitted the backend uses the breaker's open-state
    // timeout.  Validate only when the field has content.
    if (ttlText && parseGoDuration(ttlText) === null) {
      return m.providers_trip_on_ttl_invalid();
    }
  }
  return "";
}

// providerCredentialTripRulesLabel summarizes a row's trip rules for the
// list ("insufficient_quota (15m0s), rate limit (1m0s)"); empty when the row
// declares none. Read-only for every row: rules are declarative
// configuration for config-declared providers and editable in the editor for
// dashboard-managed ones.
export function providerCredentialTripRulesLabel(row) {
  return (Array.isArray(row && row.trip_on) ? row.trip_on : [])
    .map((rule) => {
      // ttl 0 or absent means "use breaker timeout" (operator left it blank);
      // omit the suffix instead of showing "0s" or "()".
      if (!rule || !rule.ttl) {
        return String((rule && rule.match) || "");
      }
      return String((rule && rule.match) || "") + " (" + formatGoDurationNs(rule.ttl) + ")";
    })
    .filter(Boolean)
    .join(", ");
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
    trip_on: tripRulesToRows(row && row.trip_on),
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

  const tripError = validateTripRuleRows(form && form.trip_on);
  if (tripError) {
    errors.trip_on = tripError;
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
  // PUT replaces the whole row, so trip_on is always sent — an empty array
  // is how clearing every rule works. (Validation rejects invalid ttls
  // before the payload is built; a null here means "no valid rules".)
  const tripOn = tripRuleRowsToWire(form && form.trip_on);
  payload.trip_on = Array.isArray(tripOn) ? tripOn : [];
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
  for (const name of PROVIDER_CREDENTIAL_FIELD_NAMES) {
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

// providerRowsHaveActions reports whether any listed provider can be edited
// or deleted. Managed rows (config.yaml or env) never can, so a deployment
// with only managed providers has no use for an actions column.
export function providerRowsHaveActions(rows) {
  return (Array.isArray(rows) ? rows : []).some((row) => row && !row.managed);
}

// providerRowsHaveTripRules reports whether any listed provider declares
// quota-breaker trip rules, which is when the list shows the read-only
// rules column at all.
export function providerRowsHaveTripRules(rows) {
  return (Array.isArray(rows) ? rows : []).some(
    (row) => Array.isArray(row && row.trip_on) && row.trip_on.length > 0,
  );
}
