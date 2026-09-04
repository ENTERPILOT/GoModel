// Pure guardrail logic. Everything here is side-effect free so it can be
// unit-tested with node:test; the Svelte state module (guardrails.svelte.js)
// owns fetches and reactive state. Field value helpers are shared with the
// virtual-model editor through $lib/utils/schemaFields.js.

import * as m from "../../lib/paraglide/messages.js";
import {
  cloneSchemaConfig,
  instanceSchemaFields,
  schemaArrayFieldSelected,
  schemaFieldDefaults,
  schemaFieldValue,
  setSchemaFieldValue,
  toggleSchemaArrayValue,
} from "../../lib/utils/schemaFields.js";
import { normalizePhases } from "../../lib/utils/pluginPhases.js";

export {
  schemaFieldValue as guardrailFieldValue,
  setSchemaFieldValue as setGuardrailFieldValue,
  schemaArrayFieldSelected as guardrailArrayFieldSelected,
  toggleSchemaArrayValue as toggleGuardrailArrayValue,
};

export const GUARDRAIL_FAIL_MODES = ["closed", "open"];

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

// guardrailTypeFields returns the schema-driven form fields for a type:
// instance-scoped only (scope "" or missing); route-scoped fields belong to
// the virtual-model editor.
export function guardrailTypeFields(types, type) {
  const definition = guardrailTypeDefinition(types, type);
  return instanceSchemaFields(definition && definition.fields);
}

// defaultGuardrailConfig builds a new instance's config: each field's own
// `default` wins, the type's `defaults` object fills the rest.
export function defaultGuardrailConfig(types, type) {
  const definition = guardrailTypeDefinition(types, type);
  if (!definition) {
    return {};
  }
  return {
    ...cloneSchemaConfig(definition.defaults),
    ...schemaFieldDefaults(guardrailTypeFields(types, type)),
  };
}

// normalizeGuardrailConfig merges a stored config over the type defaults so
// newly added schema fields get their built-in values. A stored secret comes
// back as the "********" marker and is kept as-is: sending it back unchanged
// tells the server to keep the stored value.
export function normalizeGuardrailConfig(types, config, type) {
  return {
    ...defaultGuardrailConfig(types, type),
    ...cloneSchemaConfig(config),
  };
}

// guardrailTypePhases lists the phases a type's plugin implements; a type
// without `phases` (older gateway, legacy built-ins) is prompt-only.
export function guardrailTypePhases(types, type) {
  const definition = guardrailTypeDefinition(types, type);
  return normalizePhases(definition && definition.phases);
}

// guardrailPhases lists an instance's phases: the view's own `phases` when
// the server sends them, else its type's.
export function guardrailPhases(types, guardrail) {
  if (guardrail && Array.isArray(guardrail.phases) && guardrail.phases.length > 0) {
    return normalizePhases(guardrail.phases);
  }
  return guardrailTypePhases(types, guardrail && guardrail.type);
}

// normalizeGuardrailFailMode returns "closed" | "open" | "" (plugin default).
export function normalizeGuardrailFailMode(value) {
  const mode = String(value || "").trim().toLowerCase();
  return GUARDRAIL_FAIL_MODES.includes(mode) ? mode : "";
}

// parseGuardrailTimeoutMs returns a positive integer, 0 for empty/zero
// (default), or NaN for invalid input so the form can reject it.
export function parseGuardrailTimeoutMs(value) {
  const trimmed = String(value ?? "").trim();
  if (trimmed === "") {
    return 0;
  }
  const parsed = Number(trimmed);
  if (!Number.isInteger(parsed) || parsed < 0) {
    return Number.NaN;
  }
  return parsed;
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
    fail_mode: "",
    timeout_ms: "",
  };
}

// guardrailEditForm hydrates the editor from a stored definition view.
export function guardrailEditForm(types, guardrail) {
  const resolvedType = resolvedGuardrailType(types, guardrail && guardrail.type);
  const timeout = Number(guardrail && guardrail.timeout_ms);
  return {
    name: String((guardrail && guardrail.name) || "").trim(),
    type: resolvedType,
    description: String((guardrail && guardrail.description) || "").trim(),
    user_path: String((guardrail && guardrail.user_path) || "").trim(),
    config: normalizeGuardrailConfig(
      types,
      guardrail && guardrail.config,
      resolvedType,
    ),
    fail_mode: normalizeGuardrailFailMode(guardrail && guardrail.fail_mode),
    timeout_ms: Number.isFinite(timeout) && timeout > 0 ? String(timeout) : "",
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
  return definition && definition.label ? definition.label : type || m.guardrails_unknown();
}

// buildGuardrailPayload builds the PUT /admin/guardrails body. Empty
// description/user_path/fail_mode/timeout_ms become undefined so
// JSON.stringify omits them (the server applies its defaults).
export function buildGuardrailPayload(form) {
  const timeout = parseGuardrailTimeoutMs(form && form.timeout_ms);
  return {
    name: String((form && form.name) || "").trim(),
    type: String((form && form.type) || "").trim(),
    description: String((form && form.description) || "").trim() || undefined,
    user_path: String((form && form.user_path) || "").trim() || undefined,
    config: cloneSchemaConfig(form && form.config),
    fail_mode: normalizeGuardrailFailMode(form && form.fail_mode) || undefined,
    timeout_ms: Number.isInteger(timeout) && timeout > 0 ? timeout : undefined,
  };
}
