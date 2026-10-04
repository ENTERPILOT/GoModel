// Pure model-metadata-override logic: the editable fields, the editor form
// <-> API payload mapping, and the input-media chips the details panel shows.
// The API (/admin/model-metadata-overrides) accepts categories, the four
// input capabilities below, context_window and max_output_tokens; unset
// fields inherit from config.yaml, provider discovery and the catalog.
// Relative imports keep it loadable by the node:test suite.

import * as m from "../../lib/paraglide/messages.js";

// The categories an override may assign ("all" is the tab, not a category).
export const OVERRIDE_CATEGORIES = [
  "text_generation",
  "embedding",
  "image",
  "audio",
  "video",
  "utility",
];

// Catalog capability keys for the media a model accepts besides text.
export const INPUT_CAPABILITIES = [
  { key: "vision", label: () => m.models_input_images() },
  { key: "audio_input", label: () => m.models_input_audio() },
  { key: "video_input", label: () => m.models_input_video() },
  { key: "pdf_input", label: () => m.models_input_pdf() },
];

export const INPUT_CAPABILITY_KEYS = INPUT_CAPABILITIES.map((entry) => entry.key);

// Capability select values: inherit leaves the key out of the payload.
export const CAPABILITY_INHERIT = "inherit";
export const CAPABILITY_SUPPORTED = "supported";
export const CAPABILITY_UNSUPPORTED = "unsupported";

// metadataOverrideSelector is the exact provider/model selector for a row,
// or "" when the row has no provider (metadata overrides are exact-only).
export function metadataOverrideSelector(row) {
  const provider = String((row && row.provider_name) || "").trim();
  const model = String((row && row.model && row.model.id) || "").trim();
  return provider && model ? provider + "/" + model : "";
}

export function findMetadataOverride(overrides, selector) {
  const wanted = String(selector || "").trim();
  if (!wanted) return null;
  return (Array.isArray(overrides) ? overrides : []).find((item) => item && item.selector === wanted) || null;
}

// formFromOverride builds the editor state for an existing override (or a
// blank form when there is none).
export function formFromOverride(override) {
  const metadata = (override && override.metadata) || {};
  const capabilities = {};
  for (const { key } of INPUT_CAPABILITIES) {
    const value = metadata.capabilities ? metadata.capabilities[key] : undefined;
    capabilities[key] =
      value === true ? CAPABILITY_SUPPORTED : value === false ? CAPABILITY_UNSUPPORTED : CAPABILITY_INHERIT;
  }
  return {
    categories: Array.isArray(metadata.categories) ? [...metadata.categories] : [],
    capabilities,
    context_window: metadata.context_window == null ? "" : String(metadata.context_window),
    max_output_tokens: metadata.max_output_tokens == null ? "" : String(metadata.max_output_tokens),
  };
}

function positiveInteger(raw, label) {
  const text = String(raw ?? "").trim();
  if (text === "") return { value: undefined };
  const value = Number(text);
  if (!Number.isInteger(value) || value <= 0) {
    return { error: m.models_metadata_invalid_tokens({ field: label }) };
  }
  return { value };
}

// buildMetadataOverridePayload validates the form into {metadata}, or
// returns {error} on the first problem.
export function buildMetadataOverridePayload(form) {
  const source = form || {};
  const metadata = {};
  const categories = OVERRIDE_CATEGORIES.filter((category) =>
    Array.isArray(source.categories) ? source.categories.includes(category) : false,
  );
  if (categories.length > 0) metadata.categories = categories;

  const capabilities = {};
  for (const { key } of INPUT_CAPABILITIES) {
    const value = source.capabilities ? source.capabilities[key] : CAPABILITY_INHERIT;
    if (value === CAPABILITY_SUPPORTED) capabilities[key] = true;
    if (value === CAPABILITY_UNSUPPORTED) capabilities[key] = false;
  }
  if (Object.keys(capabilities).length > 0) metadata.capabilities = capabilities;

  const contextWindow = positiveInteger(source.context_window, m.models_details_context_window());
  if (contextWindow.error) return { error: contextWindow.error };
  if (contextWindow.value !== undefined) metadata.context_window = contextWindow.value;

  const maxOutput = positiveInteger(source.max_output_tokens, m.models_details_max_output_tokens());
  if (maxOutput.error) return { error: maxOutput.error };
  if (maxOutput.value !== undefined) metadata.max_output_tokens = maxOutput.value;

  if (Object.keys(metadata).length === 0) {
    return { error: m.models_metadata_empty() };
  }
  return { metadata };
}

// inputCapabilityChips lists the input media a model accepts. With `all`
// set (the effective view) every input type is listed, and one no layer
// reports is marked unknown; otherwise only the keys the metadata sets.
export function inputCapabilityChips(capabilities, sourceLabel, all) {
  const known = capabilities && typeof capabilities === "object" ? capabilities : {};
  return INPUT_CAPABILITIES.filter(({ key }) => all || key in known).map(({ key, label }) => ({
    label: label(),
    enabled: known[key] === true,
    unknown: !(key in known),
    source: sourceLabel("capabilities." + key),
  }));
}
