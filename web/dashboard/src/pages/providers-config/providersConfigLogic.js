// Pure logic for the Providers (provider credentials) page, kept free of DOM
// and Svelte-runtime dependencies so it can be unit-tested directly.

// defaultProviderCredentialForm returns a blank editor form.
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

// providerCredentialTypeOptions ensures the currently selected type always
// renders as an option, even if it isn't (yet, or anymore) in the fetched
// /types list — e.g. while types are still loading.
export function providerCredentialTypeOptions(types, currentType) {
  const list = Array.isArray(types) ? types.slice() : [];
  const current = String(currentType || "").trim();
  if (current && !list.includes(current)) {
    list.push(current);
  }
  return list;
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

import { splitCommaList } from "../../lib/utils/format.js";

export { splitCommaList };

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

// validateProviderCredentialForm returns an error message, or "" when the
// form can be submitted.
export function validateProviderCredentialForm(form, mode, existingRows) {
  const name = String((form && form.name) || "").trim();
  const type = String((form && form.type) || "").trim();
  if (!name) {
    return "Name is required.";
  }
  if (!type) {
    return "Type is required.";
  }
  if (
    mode === "create" &&
    (Array.isArray(existingRows) ? existingRows : []).some(
      (row) => String((row && row.name) || "").trim() === name,
    )
  ) {
    return 'Provider "' + name + '" already exists.';
  }
  const apiKeys = providerCredentialKeyRowsToArray(form && form.api_keys);
  if (apiKeys.some((value) => !value.trim())) {
    return "API key rows cannot be empty. Remove the row instead of leaving it blank.";
  }
  return "";
}

// buildProviderCredentialPayload normalizes the editor form into the PUT
// /admin/provider-credentials body. Most fields are trimmed; api_keys and
// service_account_json are sent verbatim (mask preservation / raw JSON).
export function buildProviderCredentialPayload(form) {
  return {
    name: String((form && form.name) || "").trim(),
    type: String((form && form.type) || "").trim(),
    api_keys: providerCredentialKeyRowsToArray(form && form.api_keys),
    base_url: String((form && form.base_url) || "").trim(),
    api_version: String((form && form.api_version) || "").trim(),
    backend: String((form && form.backend) || "").trim(),
    auth_type: String((form && form.auth_type) || "").trim(),
    api_mode: String((form && form.api_mode) || "").trim(),
    vertex_project: String((form && form.vertex_project) || "").trim(),
    vertex_location: String((form && form.vertex_location) || "").trim(),
    service_account_file: String(
      (form && form.service_account_file) || "",
    ).trim(),
    service_account_json: (form && form.service_account_json) || "",
    service_account_json_base64: String(
      (form && form.service_account_json_base64) || "",
    ).trim(),
    gcp_scope: String((form && form.gcp_scope) || "").trim(),
    models: splitCommaList(form && form.models),
    enabled: Boolean(form && form.enabled),
  };
}
