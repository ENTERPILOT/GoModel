// Pure guardrail logic. Everything here is side-effect free so it can be
// unit-tested with node:test; the Svelte state module (guardrails.svelte.js)
// owns fetches and reactive state.

// cloneGuardrailJSON deep-clones a config payload, coercing anything that is
// not a plain JSON object into {}.
function cloneGuardrailJSON(value) {
  try {
    const cloned = JSON.parse(JSON.stringify(value || {}));
    return cloned && typeof cloned === "object" && !Array.isArray(cloned)
      ? cloned
      : {};
  } catch {
    return {};
  }
}

// normalizeGuardrailArrayValue accepts an array or a comma-separated string
// and returns a trimmed, non-empty string array.
function normalizeGuardrailArrayValue(value) {
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

// guardrailTypeDefinition finds the schema definition for a type name.
function guardrailTypeDefinition(types, type) {
  const normalized = String(type || "").trim();
  return (
    (types || []).find(
      (item) => String((item && item.type) || "").trim() === normalized,
    ) || null
  );
}

// defaultGuardrailType picks the first advertised type, falling back to the
// built-in system_prompt type.
export function defaultGuardrailType(types) {
  if (Array.isArray(types) && types.length > 0) {
    return String(types[0].type || "").trim() || "system_prompt";
  }
  return "system_prompt";
}

// resolvedGuardrailType returns the given type when it is a known definition,
// otherwise the default type.
export function resolvedGuardrailType(types, type) {
  const normalized = String(type || "").trim();
  if (normalized && guardrailTypeDefinition(types, normalized)) {
    return normalized;
  }
  return defaultGuardrailType(types);
}

// defaultGuardrailConfig clones the schema defaults for a type.
export function defaultGuardrailConfig(types, type) {
  const definition = guardrailTypeDefinition(types, type);
  if (!definition || !definition.defaults) {
    return {};
  }
  return cloneGuardrailJSON(definition.defaults);
}

// normalizeGuardrailConfig merges a stored config over the type defaults so
// newly added schema fields get their built-in values.
export function normalizeGuardrailConfig(types, config, type) {
  return {
    ...defaultGuardrailConfig(types, type),
    ...cloneGuardrailJSON(config),
  };
}

// defaultGuardrailForm builds a blank editor form for a type.
export function defaultGuardrailForm(types, type) {
  const resolvedType = resolvedGuardrailType(types, type);
  return {
    name: "",
    type: resolvedType,
    description: "",
    user_path: "",
    config: defaultGuardrailConfig(types, resolvedType),
  };
}

// filterGuardrails applies the free-text list filter across name, type,
// user_path, description, and summary.
export function filterGuardrails(guardrails, filter) {
  if (!filter) {
    return guardrails || [];
  }
  const needle = String(filter).toLowerCase();
  return (guardrails || []).filter((guardrail) => {
    const fields = [
      guardrail.name,
      guardrail.type,
      guardrail.user_path,
      guardrail.description,
      guardrail.summary,
    ];
    return fields.some((value) =>
      String(value || "")
        .toLowerCase()
        .includes(needle),
    );
  });
}

// guardrailTypeLabel returns the human label for a type.
export function guardrailTypeLabel(types, type) {
  const definition = guardrailTypeDefinition(types, type);
  return definition && definition.label ? definition.label : type || "Unknown";
}

// guardrailTypeFields returns the schema-driven form fields for a type.
export function guardrailTypeFields(types, type) {
  const definition = guardrailTypeDefinition(types, type);
  return Array.isArray(definition && definition.fields)
    ? definition.fields
    : [];
}

// guardrailFieldValue reads a schema field's current value from a config.
export function guardrailFieldValue(config, field) {
  if (!field || !config) {
    return field && field.input === "checkboxes" ? [] : "";
  }
  const value = config[field.key];
  if (value === null || value === undefined) {
    return field.input === "checkboxes" ? [] : "";
  }
  if (field.input === "checkboxes") {
    return normalizeGuardrailArrayValue(value);
  }
  return value;
}

// setGuardrailFieldValue returns the next config with a schema field updated:
// numbers are parsed (empty clears the key, non-numeric input is kept as the
// trimmed string so the server can reject it), checkbox groups normalize to
// string arrays, everything else stores the raw value.
export function setGuardrailFieldValue(config, field, value) {
  if (!field) {
    return config;
  }
  const nextConfig = cloneGuardrailJSON(config);
  if (field.input === "number") {
    const trimmed = String(value || "").trim();
    if (trimmed === "") {
      delete nextConfig[field.key];
    } else {
      const parsed = Number(trimmed);
      nextConfig[field.key] = Number.isFinite(parsed) ? parsed : trimmed;
    }
  } else if (field.input === "checkboxes") {
    nextConfig[field.key] = normalizeGuardrailArrayValue(value);
  } else {
    nextConfig[field.key] = value;
  }
  return nextConfig;
}

// guardrailArrayFieldSelected reports whether a checkbox option is selected.
export function guardrailArrayFieldSelected(config, field, optionValue) {
  return guardrailFieldValue(config, field).includes(
    String(optionValue || "").trim(),
  );
}

// toggleGuardrailArrayValue returns the next config with a checkbox option
// added or removed (deduplicated, order-preserving).
export function toggleGuardrailArrayValue(config, field, optionValue, checked) {
  const selected = normalizeGuardrailArrayValue(
    guardrailFieldValue(config, field),
  );
  const normalizedValue = String(optionValue || "").trim();
  if (!normalizedValue) {
    return config;
  }
  const next = checked
    ? Array.from(new Set([...selected, normalizedValue]))
    : selected.filter((item) => item !== normalizedValue);
  return setGuardrailFieldValue(config, field, next);
}

// buildGuardrailPayload builds the PUT /admin/guardrails body. Empty
// description/user_path become undefined so JSON.stringify omits them.
export function buildGuardrailPayload(form) {
  return {
    name: String((form && form.name) || "").trim(),
    type: String((form && form.type) || "").trim(),
    description: String((form && form.description) || "").trim() || undefined,
    user_path: String((form && form.user_path) || "").trim() || undefined,
    config: cloneGuardrailJSON(form && form.config),
  };
}
