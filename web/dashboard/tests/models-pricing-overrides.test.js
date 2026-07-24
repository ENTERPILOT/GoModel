// Ported pure-logic cases from the legacy
// internal/admin/dashboard/static/js/modules/model-pricing-overrides.test.cjs,
// exercising web/dashboard/src/pages/models/pricingOverridesLogic.js.

import test from "node:test";
import assert from "node:assert/strict";

import {
  buildPricingOverridePayload,
  effectivePreviewRows,
  modelRowPricingState,
} from "../src/pages/models/pricingOverridesLogic.js";

test("modelRowPricing applies exact override over inherited/base pricing", () => {
  const row = {
    provider_name: "openai-main",
    model: {
      id: "gpt-4o",
      metadata: {
        pricing: { input_per_mtok: 1, output_per_mtok: 2 },
      },
    },
  };
  const views = [
    { selector: "/", pricing: { input_per_mtok: 10 } },
    { selector: "openai-main/", pricing: { input_per_mtok: 20 } },
    { selector: "gpt-4o", pricing: { input_per_mtok: 30 } },
    { selector: "openai-main/gpt-4o", pricing: { input_per_mtok: 40 } },
  ];

  const { pricing } = modelRowPricingState(row, views);

  assert.equal(pricing.input_per_mtok, 40);
  assert.equal(pricing.output_per_mtok, 2);
});

test("effective preview reports per-field pricing sources", () => {
  const row = {
    provider_name: "openai-main",
    model: {
      id: "gpt-4o",
      metadata: {
        pricing: { input_per_mtok: 1, output_per_mtok: 2, cached_input_per_mtok: 0.5 },
        pricing_sources: {
          input_per_mtok: "config_yaml",
          output_per_mtok: "model_registry",
        },
      },
    },
  };
  const views = [{ selector: "openai-main/", pricing: { output_per_mtok: 20 } }];

  const state = modelRowPricingState(row, views, "openai-main/gpt-4o");
  assert.equal(state.pricing.output_per_mtok, 20);
  assert.equal(state.sources.input_per_mtok, "config.yaml");
  assert.equal(state.sources.output_per_mtok, "Dashboard/API override (openai-main/)");
  assert.equal(state.sources.cached_input_per_mtok, "Model registry");

  const draftRows = [{ id: "draft", field: "input_per_mtok", value: "10" }];
  const draft = buildPricingOverridePayload(draftRows, []).pricing;
  const rows = effectivePreviewRows(state.pricing, state.sources, draft);
  assert.equal(rows.find((item) => item.field === "input_per_mtok").source, "Form/API value");
  assert.equal(
    rows.find((item) => item.field === "output_per_mtok").source,
    "Dashboard/API override (openai-main/)",
  );
  assert.equal(rows.find((item) => item.field === "cached_input_per_mtok").source, "Model registry");
});

test("payload validates duplicate and negative pricing rows", () => {
  assert.match(
    buildPricingOverridePayload(
      [
        { id: "1", field: "input_per_mtok", value: "1" },
        { id: "2", field: "input_per_mtok", value: "2" },
      ],
      [],
    ).error,
    /only be used once/,
  );

  assert.match(
    buildPricingOverridePayload([{ id: "1", field: "input_per_mtok", value: "-1" }], []).error,
    /greater than or equal to 0/,
  );
});

test("payload requires a value for every chosen field and at least one field", () => {
  assert.match(
    buildPricingOverridePayload([{ id: "1", field: "input_per_mtok", value: "" }], []).error,
    /Enter a value for Input \$\/MTok/,
  );
  assert.match(buildPricingOverridePayload([], []).error, /Add at least one pricing field/);
});

test("payload preserves tiered pricing when scalar rows are absent", () => {
  assert.deepEqual(
    buildPricingOverridePayload([], [{ up_to_mtok: 1, input_per_mtok: 0.5 }]),
    { pricing: { tiers: [{ up_to_mtok: 1, input_per_mtok: 0.5 }] } },
  );
});

test("payload parses valid rows into a selector-ready pricing patch", () => {
  assert.deepEqual(buildPricingOverridePayload([{ id: "1", field: "input_per_mtok", value: "1.25" }], []), {
    pricing: { input_per_mtok: 1.25 },
  });
});
