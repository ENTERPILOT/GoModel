// Model details: the property sections listed under an expanded model row.
// A row's model carries what the provider listed (id, owner, created) and
// its effective metadata — the catalog entry, the provider's own report and
// config overrides merged together. /admin/models/metadata returns those
// layers separately plus the source of each effective field, which is what
// the view switch and the source labels render.
// Pure logic (no Svelte) so the node:test suite can exercise it directly;
// relative imports keep it loadable outside Vite.

import {
  formatDateUTC,
  formatNumber,
  formatPrice,
  formatPriceFine,
} from "../../lib/utils/format.js";
import * as m from "../../lib/paraglide/messages.js";
import { timeWindowHint } from "./categoryColumns.js";
import { PRICE_FIELDS } from "./pricingOverridesLogic.js";

export const METADATA_VIEWS = ["effective", "provider", "catalog", "config"];

function text(value) {
  return String(value ?? "").trim();
}

function list(values) {
  return Array.isArray(values) ? values.map(text).filter(Boolean) : [];
}

function tokens(value) {
  if (value == null || value === "") return "";
  const count = Number(value);
  if (!Number.isFinite(count)) return "";
  return m.models_details_tokens({ value: formatNumber(count) });
}

// Providers report `created` as Unix seconds; tolerate milliseconds too.
function createdDate(value) {
  const stamp = Number(value);
  if (!Number.isFinite(stamp) || stamp <= 0) return "";
  const date = formatDateUTC(stamp > 1e12 ? stamp : stamp * 1000);
  return date === "-" ? "" : date;
}

function item(label, value, options = {}) {
  return { label, value, ...options };
}

function present(entry) {
  return Boolean(entry.value);
}

function pushSection(sections, key, title, items) {
  if (items.length > 0) {
    sections.push({ key, title, items });
  }
}

// rowHasModelDetails reports whether the accordion has anything to show. A
// virtual model whose target is not in the inventory only carries a stub
// model ({ id, object }), so it gets no toggle.
export function rowHasModelDetails(row) {
  const model = row && row.model;
  if (!model) return false;
  if (!row.is_alias) return Boolean(text(model.id));
  return Boolean(model.metadata || text(model.owned_by) || createdDate(model.created));
}

// ---- Layers and views ----

export function metadataViewLabel(view) {
  switch (view) {
    case "provider":
      return m.models_details_view_provider();
    case "catalog":
      return m.models_details_view_catalog();
    case "config":
      return m.models_details_view_config();
    default:
      return m.models_details_view_effective();
  }
}

// metadataSourceLabel renders a layer name from the `sources` map.
export function metadataSourceLabel(source) {
  switch (text(source)) {
    case "provider":
      return m.models_details_source_provider();
    case "catalog":
      return m.models_details_source_catalog();
    case "config":
      return m.models_details_source_config();
    case "inferred":
      return m.models_details_source_inferred();
    default:
      return "";
  }
}

// metadataViewOptions lists the views the fetched layers support: config only
// appears when an override exists, so the switch never leads to an empty
// panel by default.
export function metadataViewOptions(layers) {
  if (!layers || typeof layers !== "object") return [];
  return METADATA_VIEWS.filter((view) => view !== "config" || layers.config).map((view) => ({
    value: view,
    label: metadataViewLabel(view),
  }));
}

// resolveMetadataView keeps a shared view choice valid for one row: a view
// the row's layers do not offer falls back to the effective one.
export function resolveMetadataView(view, layers) {
  return metadataViewOptions(layers).some((option) => option.value === view) ? view : "effective";
}

// metadataForView picks the metadata object a view renders. The effective
// view reads the inventory row itself, so it never waits for the fetch.
export function metadataForView(row, layers, view) {
  const model = (row && row.model) || {};
  if (view === "effective" || !layers) {
    return model.metadata && typeof model.metadata === "object" ? model.metadata : null;
  }
  const layer = layers[view];
  return layer && typeof layer === "object" ? layer : null;
}

export function metadataViewEmptyMessage(view) {
  switch (view) {
    case "provider":
      return m.models_details_layer_empty_provider();
    case "catalog":
      return m.models_details_layer_empty_catalog();
    case "config":
      return m.models_details_layer_empty_config();
    default:
      return m.models_details_no_metadata();
  }
}

// ---- Sections ----

function listingItems(model) {
  return [
    item(m.models_details_model_id(), text(model.id), { mono: true }),
    item(m.models_details_owned_by(), text(model.owned_by), { mono: true }),
    item(m.models_details_created(), createdDate(model.created), { mono: true }),
  ].filter(present);
}

function propertyItems(metadata, sources) {
  const source = (field) => metadataSourceLabel(sources[field]);
  return [
    item(m.models_details_display_name(), text(metadata.display_name), { source: source("display_name") }),
    item(m.models_details_family(), text(metadata.family), { mono: true, source: source("family") }),
    item(m.models_details_description(), text(metadata.description), { source: source("description") }),
    item(m.models_details_modes(), list(metadata.modes).join(", "), { mono: true, source: source("modes") }),
    item(m.models_details_categories(), list(metadata.categories).join(", "), { mono: true, source: source("categories") }),
    item(m.models_details_tags(), list(metadata.tags).join(", "), { mono: true, source: source("tags") }),
    item(m.models_details_context_window(), tokens(metadata.context_window), { mono: true, source: source("context_window") }),
    item(m.models_details_max_output_tokens(), tokens(metadata.max_output_tokens), { mono: true, source: source("max_output_tokens") }),
  ].filter(present);
}

function capabilityChips(capabilities, sources) {
  if (!capabilities || typeof capabilities !== "object") return [];
  return Object.keys(capabilities)
    .sort()
    .map((name) => ({
      label: name,
      enabled: Boolean(capabilities[name]),
      source: metadataSourceLabel(sources["capabilities." + name]),
    }));
}

function rankingItems(rankings, sources) {
  if (!rankings || typeof rankings !== "object") return [];
  return Object.keys(rankings)
    .sort()
    .map((name) => {
      const ranking = rankings[name] || {};
      const parts = [];
      if (ranking.elo != null) parts.push(m.models_details_elo({ value: formatNumber(ranking.elo) }));
      if (ranking.rank != null) parts.push(m.models_details_rank({ value: formatNumber(ranking.rank) }));
      if (text(ranking.as_of)) parts.push(m.models_details_as_of({ date: text(ranking.as_of) }));
      return item(name, parts.join(" · "), {
        mono: true,
        source: metadataSourceLabel(sources["rankings." + name]),
      });
    })
    .filter(present);
}

function priceFormatter(field) {
  return field.endsWith("_per_mtok") ? formatPrice : formatPriceFine;
}

// pricing is the row's effective pricing (overrides applied) and sources the
// matching per-field source labels, both from pricingOverridesLogic. Layer
// views pass the layer's own pricing and no sources.
function pricingItems(pricing, sources) {
  const rates = pricing && typeof pricing === "object" ? pricing : {};
  const labels = sources && typeof sources === "object" ? sources : {};
  const items = [];
  for (const option of PRICE_FIELDS) {
    const value = rates[option.value];
    if (value == null) continue;
    const format = priceFormatter(option.value);
    items.push(
      item(option.label, format(Number(value)), {
        mono: true,
        source: text(labels[option.value]),
        hint: timeWindowHint(rates, [option.value], format),
      }),
    );
  }
  return items;
}

// buildModelDetails returns the sections to render for a row, in display
// order. options.view picks the layer ("effective" by default) and
// options.layers is the /admin/models/metadata payload, whose `sources`
// label the effective fields. has_metadata is false when the chosen layer
// knows nothing about the model, so the panel can say why it is empty.
export function buildModelDetails(row, pricing, pricingSources, options = {}) {
  const view = options.view || "effective";
  const layers = options.layers && typeof options.layers === "object" ? options.layers : null;
  const model = (row && row.model) || {};
  const metadata = metadataForView(row, layers, view);
  const sources =
    view === "effective" && layers && layers.sources && typeof layers.sources === "object"
      ? layers.sources
      : {};
  const sections = [];
  // The listing fields are the provider's own, so they belong to the
  // effective and provider views only.
  if (view === "effective" || view === "provider") {
    pushSection(sections, "listing", m.models_details_section_listing(), listingItems(model));
  }
  if (metadata) {
    pushSection(sections, "properties", m.models_details_section_properties(), propertyItems(metadata, sources));
    const chips = capabilityChips(metadata.capabilities, sources);
    if (chips.length > 0) {
      sections.push({ key: "capabilities", title: m.models_details_section_capabilities(), chips });
    }
    pushSection(sections, "rankings", m.models_details_section_rankings(), rankingItems(metadata.rankings, sources));
  }
  if (view === "effective") {
    pushSection(sections, "pricing", m.models_details_section_pricing(), pricingItems(pricing, pricingSources));
  } else if (metadata) {
    pushSection(sections, "pricing", m.models_details_section_pricing(), pricingItems(metadata.pricing, {}));
  }
  return { sections, has_metadata: Boolean(metadata), view };
}
