// The editor's "Suggest fallbacks" action: which models it offers for a real
// model's failover chain, and how they land in the form.

import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import {
  applyFallbackSuggestions,
  comparablePrice,
  fallbackSuggestionsNote,
  suggestFallbackTargets,
  vmFormOffersFallbackSuggestions,
  SIMILAR_PRICE_LIMIT,
} from "../src/pages/models/fallbackSuggestions.js";
import {
  buildVirtualModelSavePayload,
  defaultVirtualModelForm,
} from "../src/pages/models/vmForm.js";

// model builds one GET /admin/models row. price is [input, output] per Mtok.
function model(provider, id, { price, categories = ["text_generation"], enabled = true, created = 0 } = {}) {
  const metadata = { categories };
  if (price) {
    metadata.pricing = { currency: "USD", input_per_mtok: price[0], output_per_mtok: price[1] };
  }
  return {
    provider_name: provider,
    provider_type: provider,
    selector: provider + "/" + id,
    model: { id, object: "model", created, metadata },
    access: { effective_enabled: enabled },
  };
}

// failoverForm is the editor as it opens from a model row: the model pinned
// as the first target under the failover strategy.
function failoverForm(source, extra = []) {
  return {
    ...defaultVirtualModelForm(),
    source,
    target_model: source,
    strategy: "failover",
    targets: extra.map((name) => ({ provider: "", model: name, weight: 1, reasoning_effort: "" })),
  };
}

function targets(suggestions) {
  return suggestions.map((suggestion) => suggestion.target);
}

const SOURCE = "openai/gpt-4o";
const GPT_4O = model("openai", "gpt-4o", { price: [2.5, 10] });

test("the same model on other providers comes first, then one similarly priced model per provider, cheapest first", () => {
  const models = [
    model("anthropic", "claude-haiku", { price: [1, 5] }), // below the tier
    model("anthropic", "claude-opus", { price: [5, 25] }), // above the tier
    model("anthropic", "claude-sonnet", { price: [3, 15] }),
    model("azure", "gpt-4o", { price: [2.5, 10] }),
    model("azure", "gpt-4.1", { price: [2, 8] }), // azure already serves the model itself
    model("gemini", "gemini-2.5-pro", { price: [1.25, 10] }),
    model("gemini", "gemini-3-pro", { price: [2, 12] }),
    GPT_4O,
    model("openai", "gpt-4.1", { price: [2, 8] }), // same provider is no fallback
    model("openrouter", "openai/gpt-4o", { price: [2.5, 10] }),
  ];
  assert.deepEqual(suggestFallbackTargets(failoverForm(SOURCE), models, []), [
    { target: "azure/gpt-4o", reason: "same_model" },
    { target: "openrouter/openai/gpt-4o", reason: "same_model" },
    { target: "gemini/gemini-2.5-pro", reason: "similar_price" },
    { target: "anthropic/claude-sonnet", reason: "similar_price" },
  ]);
});

test("suggestions skip the source, listed targets, disabled models and other modes", () => {
  const models = [
    GPT_4O,
    model("azure", "gpt-4o", { price: [2.5, 10], enabled: false }),
    model("vertex", "gpt-4o", { price: [2.5, 10], categories: ["embedding"] }),
    model("bedrock", "gpt-4o"), // unpriced, but the same model
    model("groq", "gpt-4o", { price: [2.5, 10] }),
    model("gemini", "gemini-pro", { price: [2, 10], enabled: false }),
    model("cohere", "embed-large", { price: [3, 9], categories: ["embedding"] }),
    model("mistral", "mistral-large", { price: [2, 6] }),
  ];
  assert.deepEqual(
    targets(suggestFallbackTargets(failoverForm(SOURCE, ["groq/gpt-4o"]), models, [])),
    ["bedrock/gpt-4o", "mistral/mistral-large"],
  );
});

test("a provider prefers its exact copy of the model and is not suggested twice", () => {
  const models = [
    GPT_4O,
    model("openrouter", "openai/gpt-4o", { price: [2.5, 10] }),
    model("openrouter", "gpt-4o", { price: [2.5, 10] }),
    model("openrouter", "other/model", { price: [2, 10] }),
  ];
  assert.deepEqual(targets(suggestFallbackTargets(failoverForm(SOURCE), models, [])), [
    "openrouter/gpt-4o",
  ]);
  // The provider's copy is already listed: nothing more to add from it.
  assert.deepEqual(
    suggestFallbackTargets(failoverForm(SOURCE, ["openrouter/openai/gpt-4o"]), models, []),
    [],
  );
});

test("price-tier suggestions are capped, need a priced text source, and break price ties by newest", () => {
  const peers = ["a", "b", "c", "d", "e"].map((provider, index) =>
    model(provider, "chat-" + provider, { price: [2 + index, 8] }),
  );
  assert.deepEqual(
    targets(suggestFallbackTargets(failoverForm(SOURCE), [GPT_4O, ...peers], [])),
    ["a/chat-a", "b/chat-b", "c/chat-c"],
  );
  assert.equal(SIMILAR_PRICE_LIMIT, 3);

  // A model several providers resell takes one slot, not all of them.
  const resold = [
    GPT_4O,
    model("a", "qwen-plus", { price: [2, 6] }),
    model("b", "vendor/qwen-plus", { price: [2, 6] }),
    model("c", "kimi", { price: [3, 9] }),
  ];
  assert.deepEqual(targets(suggestFallbackTargets(failoverForm(SOURCE), resold, [])), [
    "a/qwen-plus",
    "c/kimi",
  ]);

  const tied = [
    GPT_4O,
    model("anthropic", "claude-old", { price: [3, 15], created: 100 }),
    model("anthropic", "claude-new", { price: [3, 15], created: 200 }),
  ];
  assert.deepEqual(targets(suggestFallbackTargets(failoverForm(SOURCE), tied, [])), [
    "anthropic/claude-new",
  ]);

  // An unpriced, free or non-text source has no price tier to match.
  for (const source of [
    model("openai", "gpt-4o"),
    model("openai", "gpt-4o", { price: [0, 0] }),
    model("openai", "gpt-4o", { price: [2.5, 10], categories: ["image"] }),
  ]) {
    assert.deepEqual(suggestFallbackTargets(failoverForm(SOURCE), [source, ...peers], []), []);
  }
});

test("a model replaced by a redirect is skipped; one only protected by its own chain is not", () => {
  const models = [
    GPT_4O,
    model("azure", "gpt-4o", { price: [2.5, 10] }),
    model("groq", "gpt-4o", { price: [2.5, 10] }),
  ];
  const aliases = [
    // azure/gpt-4o falls back to the source: mutual protection, still a model.
    {
      name: "azure/gpt-4o",
      strategy: "failover",
      targets: [{ provider: "", model: "azure/gpt-4o" }, { provider: "openai", model: "gpt-4o" }],
    },
    // groq/gpt-4o is redirected elsewhere: naming it would reach the redirect.
    { name: "groq/gpt-4o", target_provider: "", target_model: "smart", targets: [] },
    { name: "smart", targets: [{ provider: "", model: "openai/gpt-4o" }] },
  ];
  assert.deepEqual(targets(suggestFallbackTargets(failoverForm(SOURCE), models, aliases)), [
    "azure/gpt-4o",
  ]);
});

test("suggestions are offered only for a catalog model's own failover chain", () => {
  const models = [GPT_4O, model("azure", "gpt-4o", { price: [2.5, 10] })];
  assert.equal(vmFormOffersFallbackSuggestions(failoverForm(SOURCE), models), true);

  const cases = {
    "a new virtual model name": failoverForm("smart"),
    "another strategy": { ...failoverForm(SOURCE), strategy: "round_robin" },
    "the model replaced as first target": { ...failoverForm(SOURCE), target_model: "azure/gpt-4o" },
    "an empty form": defaultVirtualModelForm(),
  };
  for (const [name, form] of Object.entries(cases)) {
    assert.equal(vmFormOffersFallbackSuggestions(form, models), false, name);
    assert.deepEqual(suggestFallbackTargets(form, models, []), [], name);
  }
  assert.equal(vmFormOffersFallbackSuggestions(null, null), false);
});

test("applied suggestions become ordinary failover targets and save as such", () => {
  const form = failoverForm(SOURCE, ["", "groq/llama"]);
  applyFallbackSuggestions(form, [
    { target: "azure/gpt-4o", reason: "same_model" },
    { target: "gemini/gemini-2.5-pro", reason: "similar_price" },
  ]);
  assert.deepEqual(
    form.targets.map((row) => row.model),
    ["groq/llama", "azure/gpt-4o", "gemini/gemini-2.5-pro"],
    "blank rows are dropped, suggestions are appended",
  );
  const { payload } = buildVirtualModelSavePayload(form, SOURCE, "edit");
  assert.equal(payload.strategy, "failover");
  assert.deepEqual(payload.targets, [
    { model: "openai/gpt-4o" },
    { model: "groq/llama" },
    { model: "azure/gpt-4o" },
    { model: "gemini/gemini-2.5-pro" },
  ]);
});

test("the note names what was added per reason", () => {
  assert.equal(
    fallbackSuggestionsNote([
      { target: "azure/gpt-4o", reason: "same_model" },
      { target: "gemini/gemini-2.5-pro", reason: "similar_price" },
      { target: "anthropic/claude-sonnet", reason: "similar_price" },
    ]),
    "Added the same model on another provider: azure/gpt-4o. " +
      "Added similarly priced models: gemini/gemini-2.5-pro, anthropic/claude-sonnet. " +
      "Review the list, then save to apply it.",
  );
  assert.equal(fallbackSuggestionsNote([]), "");
});

test("comparablePrice adds input and output and is null without a token price", () => {
  assert.equal(comparablePrice(model("openai", "gpt-4o", { price: [2.5, 10] })), 12.5);
  assert.equal(
    comparablePrice({ model: { metadata: { pricing: { output_per_mtok: 4 } } } }),
    4,
  );
  assert.equal(comparablePrice({ model: { metadata: { pricing: { per_image: 0.04 } } } }), null);
  assert.equal(comparablePrice(model("openai", "gpt-4o")), null);
  assert.equal(comparablePrice(null), null);
});

test("the editor offers the action for a failover chain and reports added rows as a change", () => {
  // No DOM harness in this suite, so the wiring is asserted on the source:
  // rows filled by a click fire no input event, and without a bubbling change
  // the dialog would discard them on close without asking.
  const editor = readFileSync(
    new URL("../src/pages/models/VirtualModelEditor.svelte", import.meta.url),
    "utf8",
  );
  assert.match(editor, /\{#if vm\.vmOffersFallbackSuggestions\(\)\}/);
  assert.match(
    editor,
    /if \(vm\.suggestVmFallbacks\(\)\) \{\s*event\.currentTarget\.dispatchEvent\(new Event\("change", \{ bubbles: true \}\)\);/,
  );
});
