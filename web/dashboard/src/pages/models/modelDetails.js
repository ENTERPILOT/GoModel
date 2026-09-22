// Model details: the property sections listed under an expanded model row.
// A row's model carries what the provider listed (id, owner, created) and
// its merged metadata — the catalog entry, the provider's own report and
// config overrides layered together. Only pricing records which source set
// each field, so that is the only section that can name one.
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

function listingItems(model) {
  return [
    item(m.models_details_model_id(), text(model.id), { mono: true }),
    item(m.models_details_owned_by(), text(model.owned_by), { mono: true }),
    item(m.models_details_created(), createdDate(model.created), { mono: true }),
  ].filter(present);
}

function propertyItems(metadata) {
  return [
    item(m.models_details_display_name(), text(metadata.display_name)),
    item(m.models_details_family(), text(metadata.family), { mono: true }),
    item(m.models_details_description(), text(metadata.description)),
    item(m.models_details_modes(), list(metadata.modes).join(", "), { mono: true }),
    item(m.models_details_categories(), list(metadata.categories).join(", "), { mono: true }),
    item(m.models_details_tags(), list(metadata.tags).join(", "), { mono: true }),
    item(m.models_details_context_window(), tokens(metadata.context_window), { mono: true }),
    item(m.models_details_max_output_tokens(), tokens(metadata.max_output_tokens), { mono: true }),
  ].filter(present);
}

function capabilityChips(capabilities) {
  if (!capabilities || typeof capabilities !== "object") return [];
  return Object.keys(capabilities)
    .sort()
    .map((name) => ({ label: name, enabled: Boolean(capabilities[name]) }));
}

function rankingItems(rankings) {
  if (!rankings || typeof rankings !== "object") return [];
  return Object.keys(rankings)
    .sort()
    .map((name) => {
      const ranking = rankings[name] || {};
      const parts = [];
      if (ranking.elo != null) parts.push(m.models_details_elo({ value: formatNumber(ranking.elo) }));
      if (ranking.rank != null) parts.push(m.models_details_rank({ value: formatNumber(ranking.rank) }));
      if (text(ranking.as_of)) parts.push(m.models_details_as_of({ date: text(ranking.as_of) }));
      return item(name, parts.join(" · "), { mono: true });
    })
    .filter(present);
}

function priceFormatter(field) {
  return field.endsWith("_per_mtok") ? formatPrice : formatPriceFine;
}

// pricing is the row's effective pricing (overrides applied) and sources the
// matching per-field source labels, both from pricingOverridesLogic.
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
// order. has_metadata is false when neither the catalog nor the provider
// reported any metadata, so the panel can say why it is nearly empty.
export function buildModelDetails(row, pricing, pricingSources) {
  const model = (row && row.model) || {};
  const metadata = model.metadata && typeof model.metadata === "object" ? model.metadata : null;
  const sections = [];
  pushSection(sections, "listing", m.models_details_section_listing(), listingItems(model));
  if (metadata) {
    pushSection(sections, "properties", m.models_details_section_properties(), propertyItems(metadata));
    const chips = capabilityChips(metadata.capabilities);
    if (chips.length > 0) {
      sections.push({ key: "capabilities", title: m.models_details_section_capabilities(), chips });
    }
    pushSection(sections, "rankings", m.models_details_section_rankings(), rankingItems(metadata.rankings));
  }
  pushSection(sections, "pricing", m.models_details_section_pricing(), pricingItems(pricing, pricingSources));
  return { sections, has_metadata: Boolean(metadata) };
}
