// Schema-driven form fields shared by the guardrail editor (instance-scoped
// plugin config) and the virtual-model editor (route-scoped plugin config).
// Pure helpers over a plain config object; SchemaFields.svelte renders them.
// Field shape (from GET /admin/guardrails/types and GET /admin/plugins):
//   { key, label, input: text|textarea|number|select|checkboxes|secret|model|bool|list,
//     required, help, placeholder, options: [{value, label}], default, scope }
// `checkboxes` and `list` hold string arrays, `bool` a boolean, `model` a
// provider/model selector, alias, or virtual model; everything else a string.

// SECRET_PLACEHOLDER is what the server returns for a stored secret. Sending
// it back unchanged keeps the stored value; any other value replaces it.
export const SECRET_PLACEHOLDER = "********";

// cloneSchemaConfig deep-clones a config payload, coercing anything that is
// not a plain JSON object into {}.
export function cloneSchemaConfig(value) {
  try {
    const cloned = JSON.parse(JSON.stringify(value || {}));
    return cloned && typeof cloned === "object" && !Array.isArray(cloned)
      ? cloned
      : {};
  } catch {
    return {};
  }
}

// normalizeSchemaArrayValue accepts an array or a comma-separated string and
// returns a trimmed, non-empty string array.
export function normalizeSchemaArrayValue(value) {
  if (Array.isArray(value)) {
    return value.map((item) => String(item || "").trim()).filter((item) => item);
  }
  if (value === null || value === undefined) {
    return [];
  }
  return String(value)
    .split(",")
    .map((item) => item.trim())
    .filter((item) => item);
}

// normalizeSchemaListValue accepts an array or free text split on commas and
// newlines and returns a trimmed, non-empty, de-duplicated string array.
export function normalizeSchemaListValue(value) {
  const items = Array.isArray(value)
    ? value.map((item) => String(item ?? "").trim())
    : String(value ?? "")
        .split(/[,\n]/)
        .map((item) => item.trim());
  return [...new Set(items.filter((item) => item))];
}

// normalizeSchemaBoolValue reads a stored boolean: JSON booleans, 0/1, and
// the words true/false, yes/no, on/off in any case.
export function normalizeSchemaBoolValue(value) {
  if (typeof value === "boolean") return value;
  if (typeof value === "number") return value !== 0;
  switch (String(value ?? "").trim().toLowerCase()) {
    case "true":
    case "yes":
    case "on":
    case "1":
      return true;
    default:
      return false;
  }
}

// instanceSchemaFields keeps the fields an instance editor renders: scope ""
// (or missing). Route-scoped fields belong to the virtual-model editor.
export function instanceSchemaFields(fields) {
  return (Array.isArray(fields) ? fields : []).filter(
    (field) =>
      field && field.key && String(field.scope || "").trim() === "",
  );
}

// routeSchemaFields keeps the fields rendered per virtual model.
export function routeSchemaFields(fields) {
  return (Array.isArray(fields) ? fields : []).filter(
    (field) =>
      field && field.key && String(field.scope || "").trim() === "route",
  );
}

// schemaFieldDefaults collects each field's `default` into a config object.
// Fields without a default are left out so a `defaults` object can fill them.
export function schemaFieldDefaults(fields) {
  const config = {};
  for (const field of Array.isArray(fields) ? fields : []) {
    if (!field || !field.key) continue;
    if (field.default === undefined || field.default === null) continue;
    try {
      config[field.key] = JSON.parse(JSON.stringify(field.default));
    } catch {
      // A non-serializable default is not a usable value.
    }
  }
  return config;
}

// isSecretPlaceholder reports whether a secret field still holds the
// server's redaction marker (nothing typed yet).
export function isSecretPlaceholder(value) {
  return String(value ?? "") === SECRET_PLACEHOLDER;
}

// schemaFieldValue reads a field's current value from a config.
export function schemaFieldValue(config, field) {
  const value = field && config ? config[field.key] : undefined;
  switch (field && field.input) {
    case "checkboxes":
      return normalizeSchemaArrayValue(value);
    case "list":
      return normalizeSchemaListValue(value);
    case "bool":
      return normalizeSchemaBoolValue(value);
    default:
      return value === null || value === undefined ? "" : value;
  }
}

// setSchemaFieldValue returns the next config with a field updated: numbers
// are parsed (empty clears the key, non-numeric input is kept as the trimmed
// string so the server can reject it), checkbox groups and lists normalize
// to string arrays, bools to booleans, everything else (including secrets)
// stores the raw value.
export function setSchemaFieldValue(config, field, value) {
  if (!field) {
    return config;
  }
  const nextConfig = cloneSchemaConfig(config);
  if (field.input === "number") {
    const trimmed = String(value ?? "").trim();
    if (trimmed === "") {
      delete nextConfig[field.key];
    } else {
      const parsed = Number(trimmed);
      nextConfig[field.key] = Number.isFinite(parsed) ? parsed : trimmed;
    }
  } else if (field.input === "checkboxes") {
    nextConfig[field.key] = normalizeSchemaArrayValue(value);
  } else if (field.input === "list") {
    nextConfig[field.key] = normalizeSchemaListValue(value);
  } else if (field.input === "bool") {
    nextConfig[field.key] = normalizeSchemaBoolValue(value);
  } else {
    nextConfig[field.key] = value;
  }
  return nextConfig;
}

// schemaArrayFieldSelected reports whether a checkbox option is selected.
export function schemaArrayFieldSelected(config, field, optionValue) {
  return schemaFieldValue(config, field).includes(
    String(optionValue || "").trim(),
  );
}

// toggleSchemaArrayValue returns the next config with a checkbox option added
// or removed (deduplicated, order-preserving).
export function toggleSchemaArrayValue(config, field, optionValue, checked) {
  const selected = normalizeSchemaArrayValue(schemaFieldValue(config, field));
  const normalizedValue = String(optionValue || "").trim();
  if (!normalizedValue) {
    return config;
  }
  const next = checked
    ? Array.from(new Set([...selected, normalizedValue]))
    : selected.filter((item) => item !== normalizedValue);
  return setSchemaFieldValue(config, field, next);
}
