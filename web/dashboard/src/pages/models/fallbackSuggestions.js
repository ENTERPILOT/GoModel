// Fallback suggestions for a real model's failover chain: what the editor's
// "Suggest fallbacks" action appends, picked from the model inventory the
// dashboard already holds (GET /admin/models).
// Pure logic (no Svelte) so the node:test suite can exercise it directly;
// relative imports keep it loadable outside Vite.

import * as m from "../../lib/paraglide/messages.js";
import { qualifiedModelName } from "./modelIdentity.js";
import { qualifyTarget } from "./routing.js";
import { flattenFormTargets } from "./vmForm.js";

const TEXT_GENERATION = "text_generation";

// A model is in the source's price tier when its price is within this factor
// of the source's, either way.
const PRICE_TIER_FACTOR = 2;

// SIMILAR_PRICE_LIMIT caps the price-tier suggestions; the same model on
// another provider is always suggested, however many providers serve it.
export const SIMILAR_PRICE_LIMIT = 3;

export const REASON_SAME_MODEL = "same_model";
export const REASON_SIMILAR_PRICE = "similar_price";

function metadataOf(model) {
  return (model && model.model && model.model.metadata) || {};
}

function categoriesOf(model) {
  const categories = metadataOf(model).categories;
  return Array.isArray(categories) ? categories : [];
}

function providerOf(model) {
  return String((model && model.provider_name) || "").trim();
}

function modelID(model) {
  return String((model && model.model && model.model.id) || "")
    .trim()
    .toLowerCase();
}

// baseModelID drops a vendor prefix, so an aggregator's "openai/gpt-4o" is
// recognized as the "gpt-4o" another provider serves.
function baseModelID(model) {
  const id = modelID(model);
  return id.slice(id.lastIndexOf("/") + 1);
}

function finitePrice(value) {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

// comparablePrice is the per-token price the cost strategy compares targets
// by (input plus output per million tokens, see targetCost in
// internal/virtualmodels/balancer.go); null for a model with no token price.
export function comparablePrice(model) {
  const pricing = metadataOf(model).pricing;
  if (!pricing) return null;
  const input = finitePrice(pricing.input_per_mtok);
  const output = finitePrice(pricing.output_per_mtok);
  if (input === null && output === null) return null;
  return (input || 0) + (output || 0);
}

// sameMode keeps a suggestion in the source's line of work: two models whose
// categories are both known must share one. A model the registry does not
// describe is not ruled out, since the matching model ID already vouches for it.
function sameMode(source, candidate) {
  const left = categoriesOf(source);
  const right = categoriesOf(candidate);
  if (left.length === 0 || right.length === 0) return true;
  return left.some((category) => right.includes(category));
}

function redirectTargets(alias) {
  const targets = Array.isArray(alias && alias.targets)
    ? alias.targets.map(qualifyTarget).filter(Boolean)
    : [];
  if (targets.length > 0) return targets;
  const single = qualifyTarget({
    provider: alias && alias.target_provider,
    model: alias && alias.target_model,
  });
  return single ? [single] : [];
}

// replacedByRedirect reports whether naming this model as a fallback would
// reach another virtual model instead of the model: a redirect stored under
// the model's own name that does not list the model among its targets. A
// model that is only protected by its redirect (it lists itself) is still
// reached directly from another protected model — the same rule the gateway
// applies (see chained in internal/virtualmodels/chain.go), which is what
// lets two models fall back to each other.
function replacedByRedirect(selector, redirects) {
  const alias = redirects.get(selector);
  return Boolean(alias) && !redirectTargets(alias).includes(selector);
}

function catalogBySelector(models) {
  const catalog = new Map();
  for (const model of Array.isArray(models) ? models : []) {
    const selector = qualifiedModelName(model);
    if (selector && !catalog.has(selector)) {
      catalog.set(selector, model);
    }
  }
  return catalog;
}

// vmFormOffersFallbackSuggestions: suggestions are fallbacks for a catalog
// model, so the action shows only while the form is that model's failover
// chain — the model pinned as the first target under the failover strategy,
// which is how the editor opens from a model row.
export function vmFormOffersFallbackSuggestions(form, models) {
  const source = String((form && form.source) || "").trim();
  if (!source) return false;
  if (String((form && form.target_model) || "").trim() !== source) return false;
  if (String((form && form.strategy) || "").toLowerCase() !== "failover") {
    return false;
  }
  return (Array.isArray(models) ? models : []).some(
    (model) => qualifiedModelName(model) === source,
  );
}

// suggestFallbackTargets returns the fallbacks worth adding to the form's
// failover chain, in the order they should be tried:
//   1. the same model under every other provider, in catalog order;
//   2. text models in the source's price tier from providers the chain does
//      not use yet, cheapest first, one per provider and per model, at most
//      SIMILAR_PRICE_LIMIT.
// Each entry is { target, reason }. Skipped: the source itself, targets
// already listed, disabled models, and models another virtual model replaces.
export function suggestFallbackTargets(form, models, aliases) {
  if (!vmFormOffersFallbackSuggestions(form, models)) return [];

  const catalog = catalogBySelector(models);
  const source = String(form.source).trim();
  const sourceModel = catalog.get(source);
  const redirects = new Map();
  for (const alias of Array.isArray(aliases) ? aliases : []) {
    const name = String((alias && alias.name) || "").trim();
    if (name) redirects.set(name, alias);
  }

  const listed = new Set([source]);
  for (const target of flattenFormTargets(form)) {
    const selector = String(target.model || "").trim();
    if (selector) listed.add(selector);
  }
  const usedProviders = new Set();
  for (const selector of listed) {
    if (catalog.has(selector)) usedProviders.add(providerOf(catalog.get(selector)));
  }

  const eligible = (selector, model) =>
    !listed.has(selector) &&
    !(model.access && model.access.effective_enabled === false) &&
    !replacedByRedirect(selector, redirects);

  // One per provider: an exact ID match beats a vendor-prefixed one, and a
  // provider whose copy of the model is already listed is done.
  const sourceBase = baseModelID(sourceModel);
  const covered = new Set();
  for (const selector of listed) {
    const model = catalog.get(selector);
    if (model && baseModelID(model) === sourceBase) covered.add(providerOf(model));
  }
  const sameModel = new Map();
  for (const [selector, model] of catalog) {
    const provider = providerOf(model);
    if (covered.has(provider) || !sourceBase) continue;
    if (baseModelID(model) !== sourceBase) continue;
    if (!sameMode(sourceModel, model) || !eligible(selector, model)) continue;
    const exact = modelID(model) === modelID(sourceModel);
    const current = sameModel.get(provider);
    if (!current || (exact && !current.exact)) {
      sameModel.set(provider, { selector, exact });
    }
  }
  const suggestions = [];
  for (const [provider, match] of sameModel) {
    usedProviders.add(provider);
    listed.add(match.selector);
    suggestions.push({ target: match.selector, reason: REASON_SAME_MODEL });
  }

  const sourcePrice = comparablePrice(sourceModel);
  if (
    !categoriesOf(sourceModel).includes(TEXT_GENERATION) ||
    sourcePrice === null ||
    sourcePrice <= 0
  ) {
    return suggestions;
  }
  const peers = [];
  for (const [selector, model] of catalog) {
    if (!categoriesOf(model).includes(TEXT_GENERATION)) continue;
    const price = comparablePrice(model);
    if (
      price === null ||
      price < sourcePrice / PRICE_TIER_FACTOR ||
      price > sourcePrice * PRICE_TIER_FACTOR
    ) {
      continue;
    }
    if (!eligible(selector, model)) continue;
    const created = Number(model.model && model.model.created) || 0;
    peers.push({
      selector,
      provider: providerOf(model),
      base: baseModelID(model),
      price,
      created,
    });
  }
  // Cheapest first; at the same price the newer model, then a stable order.
  peers.sort(
    (a, b) =>
      a.price - b.price ||
      b.created - a.created ||
      (a.selector < b.selector ? -1 : a.selector > b.selector ? 1 : 0),
  );
  // One per provider and one per model, so the picks spread out instead of
  // repeating a model several providers resell.
  const pickedModels = new Set();
  for (const peer of peers) {
    if (pickedModels.size >= SIMILAR_PRICE_LIMIT) break;
    if (usedProviders.has(peer.provider) || pickedModels.has(peer.base)) continue;
    usedProviders.add(peer.provider);
    pickedModels.add(peer.base);
    suggestions.push({ target: peer.selector, reason: REASON_SIMILAR_PRICE });
  }
  return suggestions;
}

// applyFallbackSuggestions appends the suggestions as target rows, after
// dropping blank rows so the chain stays contiguous. Nothing is saved: the
// rows are ordinary form targets until the editor is submitted.
export function applyFallbackSuggestions(form, suggestions) {
  const rows = (Array.isArray(form.targets) ? form.targets : []).filter(
    (row) => String((row && row.model) || "").trim() !== "",
  );
  for (const suggestion of Array.isArray(suggestions) ? suggestions : []) {
    rows.push({
      provider: "",
      model: suggestion.target,
      weight: 1,
      reasoning_effort: "",
    });
  }
  form.targets = rows;
}

// fallbackSuggestionsNote tells the operator what was just added and why,
// one clause per reason, so the picks can be reviewed before saving.
export function fallbackSuggestionsNote(suggestions) {
  const list = Array.isArray(suggestions) ? suggestions : [];
  const targetsFor = (reason) =>
    list
      .filter((suggestion) => suggestion.reason === reason)
      .map((suggestion) => suggestion.target)
      .join(", ");
  const parts = [];
  const sameModel = targetsFor(REASON_SAME_MODEL);
  if (sameModel) {
    parts.push(m.models_suggest_fallbacks_same_model({ targets: sameModel }));
  }
  const similarPrice = targetsFor(REASON_SIMILAR_PRICE);
  if (similarPrice) {
    parts.push(m.models_suggest_fallbacks_similar_price({ targets: similarPrice }));
  }
  if (parts.length === 0) return "";
  parts.push(m.models_suggest_fallbacks_review());
  return parts.join(" ");
}
