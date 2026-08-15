// Pure model-pricing-override logic.

import * as m from "../../lib/paraglide/messages.js";

export const PRICE_FIELDS = [
  { value: "input_per_mtok", label: m.models_price_input(), group: m.models_price_group_tokens() },
  { value: "output_per_mtok", label: m.models_price_output(), group: m.models_price_group_tokens() },
  { value: "cached_input_per_mtok", label: m.models_price_cached_input(), group: m.models_price_group_tokens() },
  { value: "cache_write_per_mtok", label: m.models_price_cache_write(), group: m.models_price_group_tokens() },
  { value: "reasoning_output_per_mtok", label: m.models_price_reasoning_output(), group: m.models_price_group_tokens() },
  { value: "batch_input_per_mtok", label: m.models_price_batch_input(), group: m.models_price_group_batch() },
  { value: "batch_output_per_mtok", label: m.models_price_batch_output(), group: m.models_price_group_batch() },
  { value: "audio_input_per_mtok", label: m.models_price_audio_input(), group: m.models_price_group_audio() },
  { value: "audio_output_per_mtok", label: m.models_price_audio_output(), group: m.models_price_group_audio() },
  { value: "per_image", label: m.models_price_per_image(), group: m.models_price_group_image() },
  { value: "input_per_image", label: m.models_price_input_image(), group: m.models_price_group_image() },
  { value: "per_second_input", label: m.models_price_input_second(), group: m.models_price_group_audio_video() },
  { value: "per_second_output", label: m.models_price_output_second(), group: m.models_price_group_video() },
  { value: "per_character_input", label: m.models_price_per_character(), group: m.models_price_group_audio() },
  { value: "per_page", label: m.models_price_per_page(), group: m.models_price_group_utility() },
  { value: "per_request", label: m.models_price_per_request(), group: m.models_price_group_utility() },
];

export const GLOBAL_PRICING_SELECTOR = "/";

export function pricingFieldLabel(field) {
  const option = PRICE_FIELDS.find((item) => item.value === field);
  return option ? option.label : String(field || "").replace(/_/g, " ");
}

export function clonePricing(pricing) {
  return pricing && typeof pricing === "object" ? JSON.parse(JSON.stringify(pricing)) : {};
}

function mergePricing(base, override) {
  const out = clonePricing(base);
  const patch = override && override.pricing ? override.pricing : override;
  if (!patch || typeof patch !== "object") {
    return out;
  }
  for (const option of PRICE_FIELDS) {
    if (patch[option.value] !== null && patch[option.value] !== undefined) {
      out[option.value] = Number(patch[option.value]);
    }
  }
  if (Array.isArray(patch.tiers) && patch.tiers.length > 0) {
    out.tiers = clonePricing(patch.tiers);
  }
  return out;
}

function modelPricingSourceLabel(source) {
  switch (String(source || "").trim()) {
    case "config_yaml":
      return "config.yaml";
    case "model_registry":
      return m.models_price_registry();
    default:
      return source ? String(source) : m.models_price_unknown();
  }
}

function pricingSourcesFromMetadata(metadata) {
  const pricing = metadata && metadata.pricing ? metadata.pricing : {};
  const rawSources =
    metadata && metadata.pricing_sources && typeof metadata.pricing_sources === "object"
      ? metadata.pricing_sources
      : {};
  const sources = {};
  for (const option of PRICE_FIELDS) {
    if (pricing[option.value] !== null && pricing[option.value] !== undefined) {
      sources[option.value] = modelPricingSourceLabel(rawSources[option.value] || "model_registry");
    }
  }
  return sources;
}

function modelPricingOverrideSourceLabel(override) {
  const selector = String((override && override.selector) || "").trim();
  return selector
    ? m.models_price_override_source_selector({ selector })
    : m.models_price_override_source();
}

// ---- Selectors ----

export function providerPricingOverrideSelector(providerName) {
  const name = String(providerName || "").trim();
  return name ? name + "/" : "";
}

function modelPricingModelID(row) {
  return String((row && row.model && row.model.id) || "").trim();
}

export function modelPricingExactSelector(row) {
  const providerName = String((row && row.provider_name) || "").trim();
  const modelID = modelPricingModelID(row);
  if (providerName && modelID) {
    return providerName + "/" + modelID;
  }
  return modelID;
}

export function modelPricingModelWideSelector(row) {
  return modelPricingModelID(row);
}

function pricingOverrideMap(views) {
  const out = new Map();
  for (const override of Array.isArray(views) ? views : []) {
    const selector = String((override && override.selector) || "").trim();
    if (selector) {
      out.set(selector, override);
    }
  }
  return out;
}

export function findModelPricingOverrideView(views, selector) {
  const normalized = String(selector || "").trim();
  if (!normalized) {
    return null;
  }
  return pricingOverrideMap(views).get(normalized) || null;
}

// matchingModelPricingOverride walks exact -> model-wide -> provider-wide ->
// global selectors and returns the first override, skipping ignoredSelector.
function matchingModelPricingOverride(views, row, ignoredSelector) {
  const overrides = pricingOverrideMap(views);
  const exact = modelPricingExactSelector(row);
  const modelWide = modelPricingModelWideSelector(row);
  const providerWide = providerPricingOverrideSelector(row && row.provider_name);
  const ignored = String(ignoredSelector || "").trim();
  for (const selector of [exact, modelWide, providerWide, GLOBAL_PRICING_SELECTOR]) {
    if (!selector || selector === ignored) {
      continue;
    }
    const override = overrides.get(selector);
    if (override) {
      return override;
    }
  }
  return null;
}

// modelRowPricingState resolves the effective pricing for a table row along
// with a per-field source label.
export function modelRowPricingState(row, views, ignoredSelector) {
  const metadata = row && row.model && row.model.metadata ? row.model.metadata : null;
  const pricing = clonePricing(metadata && metadata.pricing);
  const sources = pricingSourcesFromMetadata(metadata);
  const override = matchingModelPricingOverride(views, row, ignoredSelector);
  const patch = override && override.pricing ? override.pricing : null;
  if (patch) {
    const overrideSource = modelPricingOverrideSourceLabel(override);
    for (const option of PRICE_FIELDS) {
      if (patch[option.value] !== null && patch[option.value] !== undefined) {
        pricing[option.value] = Number(patch[option.value]);
        sources[option.value] = overrideSource;
      }
    }
    if (Array.isArray(patch.tiers) && patch.tiers.length > 0) {
      pricing.tiers = clonePricing(patch.tiers);
      sources.tiers = overrideSource;
    }
  }
  return { pricing, sources };
}

// ---- Editor rows / payload ----

export function pricingRowsFromOverride(override, nextID) {
  const pricing = override && override.pricing ? override.pricing : {};
  const rows = [];
  for (const option of PRICE_FIELDS) {
    if (pricing[option.value] !== null && pricing[option.value] !== undefined) {
      rows.push({
        id: nextID(),
        field: option.value,
        value: String(pricing[option.value]),
      });
    }
  }
  return rows;
}

export function selectedPricingFields(rows, exceptID) {
  const fields = new Set();
  for (const row of Array.isArray(rows) ? rows : []) {
    if (exceptID && row.id === exceptID) {
      continue;
    }
    const field = String(row.field || "").trim();
    if (field) {
      fields.add(field);
    }
  }
  return fields;
}

export function availablePricingFieldOptions(rows, row) {
  const selected = selectedPricingFields(rows, row && row.id);
  return PRICE_FIELDS.filter(
    (option) => option.value === (row && row.field) || !selected.has(option.value),
  );
}

// buildPricingOverridePayload validates the editor rows into a {pricing}
// payload, or returns {error} on the first problem.
export function buildPricingOverridePayload(rows, preservedTiers) {
  const pricing = {};
  const seen = new Set();
  for (const row of Array.isArray(rows) ? rows : []) {
    const field = String(row.field || "").trim();
    if (!field) {
      return { error: m.models_price_choose_type() };
    }
    if (seen.has(field)) {
      return { error: m.models_price_unique_type() };
    }
    seen.add(field);
    const raw = String(row.value || "").trim();
    if (raw === "") {
      return { error: m.models_price_enter_value({ field: pricingFieldLabel(field) }) };
    }
    const value = Number(raw);
    if (!Number.isFinite(value) || value < 0) {
      return { error: m.models_price_invalid_value() };
    }
    pricing[field] = value;
  }
  const tiers = Array.isArray(preservedTiers) ? preservedTiers : [];
  if (tiers.length > 0) {
    pricing.tiers = clonePricing(tiers);
  }
  if (Object.keys(pricing).length === 0) {
    return { error: m.models_price_add_field() };
  }
  return { pricing };
}

// effectivePreviewRows merges the base pricing with the in-progress draft and
// labels where each field's effective value comes from.
export function effectivePreviewRows(basePricing, baseSources, draftPricing) {
  const base = basePricing || {};
  const sources = baseSources || {};
  const draft = draftPricing || {};
  const effective = mergePricing(base, draft);
  return PRICE_FIELDS.map((option) => {
    const hasDraft = draft[option.value] !== null && draft[option.value] !== undefined;
    const hasBase = base[option.value] !== null && base[option.value] !== undefined;
    return {
      field: option.value,
      label: option.label,
      value: effective[option.value],
      source: hasDraft
        ? m.models_price_form_value()
        : hasBase
          ? sources[option.value] || m.models_price_registry()
          : m.models_price_unset(),
    };
  }).filter((row) => row.source !== m.models_price_unset() || row.value !== undefined);
}
