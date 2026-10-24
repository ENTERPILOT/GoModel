import test from "node:test";
import assert from "node:assert/strict";

import {
  buildModelDetails,
  metadataForView,
  metadataSourceLabel,
  metadataViewOptions,
  resolveMetadataView,
  rowHasModelDetails,
} from "../src/pages/models/modelDetails.js";
import { formatNumber } from "../src/lib/utils/format.js";
import * as m from "../src/lib/paraglide/messages.js";

function modelRow(overrides = {}) {
  return {
    key: "model:openai/gpt-4o",
    display_name: "openai/gpt-4o",
    is_alias: false,
    model: {
      id: "gpt-4o",
      object: "model",
      owned_by: "openai",
      created: 1715558400,
      metadata: {
        display_name: "GPT-4o",
        family: "gpt-4o",
        description: "Flagship multimodal model.",
        modes: ["chat", "responses"],
        categories: ["text_generation"],
        tags: ["vision"],
        context_window: 128000,
        max_output_tokens: 16384,
        capabilities: { vision: true, tools: true, audio_input: false },
        rankings: { lmarena: { elo: 1280.5, rank: 7, as_of: "2025-01-15" } },
        pricing: { input_per_mtok: 2.5, output_per_mtok: 10, per_image: 0.04 },
        pricing_sources: { input_per_mtok: "config_yaml" },
      },
    },
    ...overrides,
  };
}

function sectionKeys(details) {
  return details.sections.map((section) => section.key);
}

function section(details, key) {
  return details.sections.find((candidate) => candidate.key === key);
}

function itemValue(details, key, label) {
  const entry = section(details, key).items.find((candidate) => candidate.label === label);
  return entry ? entry.value : undefined;
}

test("rowHasModelDetails: real models expand, virtual models do not", () => {
  assert.equal(rowHasModelDetails(modelRow()), true);
  assert.equal(rowHasModelDetails(modelRow({ model: { id: "bare" } })), true);
  assert.equal(rowHasModelDetails({ is_alias: true, model: { id: "alias", object: "model" } }), false);
  assert.equal(
    rowHasModelDetails({ is_alias: true, model: { id: "gpt-4o", owned_by: "openai", metadata: {} } }),
    false,
  );
  assert.equal(rowHasModelDetails({ is_alias: false, model: null }), false);
  assert.equal(rowHasModelDetails(null), false);
});

test("buildModelDetails lists every section in display order", () => {
  const details = buildModelDetails(modelRow(), { input_per_mtok: 2.5, output_per_mtok: 10, per_image: 0.04 }, {
    input_per_mtok: "config.yaml",
  });
  assert.equal(details.has_metadata, true);
  assert.deepEqual(sectionKeys(details), ["listing", "properties", "capabilities", "rankings", "pricing"]);
});

test("provider listing shows the id, owner and created date", () => {
  const details = buildModelDetails(modelRow(), {}, {});
  assert.equal(itemValue(details, "listing", m.models_details_model_id()), "gpt-4o");
  assert.equal(itemValue(details, "listing", m.models_details_owned_by()), "openai");
  assert.equal(itemValue(details, "listing", m.models_details_created()), "2024-05-13");
});

test("created accepts milliseconds and drops zero", () => {
  const millis = buildModelDetails(modelRow({ model: { id: "x", created: 1715558400000 } }), {}, {});
  assert.equal(itemValue(millis, "listing", m.models_details_created()), "2024-05-13");
  const zero = buildModelDetails(modelRow({ model: { id: "x", created: 0 } }), {}, {});
  assert.equal(itemValue(zero, "listing", m.models_details_created()), undefined);
});

test("properties format lists and token counts and skip empty fields", () => {
  const details = buildModelDetails(modelRow(), {}, {});
  assert.equal(itemValue(details, "properties", m.models_details_modes()), "chat, responses");
  assert.equal(
    itemValue(details, "properties", m.models_details_context_window()),
    m.models_details_tokens({ value: formatNumber(128000) }),
  );
  assert.equal(
    itemValue(details, "properties", m.models_details_max_output_tokens()),
    m.models_details_tokens({ value: formatNumber(16384) }),
  );

  const sparse = buildModelDetails(
    modelRow({ model: { id: "x", metadata: { family: "  ", tags: [], context_window: "n/a" } } }),
    {},
    {},
  );
  assert.equal(section(sparse, "properties"), undefined);
});

test("capabilities are sorted chips carrying their enabled flag", () => {
  const details = buildModelDetails(modelRow(), {}, {});
  assert.deepEqual(section(details, "capabilities").chips, [
    { label: "audio_input", enabled: false, source: "" },
    { label: "tools", enabled: true, source: "" },
    { label: "vision", enabled: true, source: "" },
  ]);
});

test("rankings join elo, rank and date", () => {
  const details = buildModelDetails(modelRow(), {}, {});
  const value = itemValue(details, "rankings", "lmarena");
  assert.equal(
    value,
    [
      m.models_details_elo({ value: formatNumber(1280.5) }),
      m.models_details_rank({ value: formatNumber(7) }),
      m.models_details_as_of({ date: "2025-01-15" }),
    ].join(" · "),
  );
});

test("pricing uses the effective prices and names each source", () => {
  const details = buildModelDetails(
    modelRow(),
    { input_per_mtok: 3, output_per_mtok: 10, per_image: 0.04 },
    { input_per_mtok: "config.yaml", output_per_mtok: "Model registry" },
  );
  const items = section(details, "pricing").items;
  assert.deepEqual(
    items.map((entry) => [entry.value, entry.source]),
    [
      ["$3.00", "config.yaml"],
      ["$10.00", "Model registry"],
      ["$0.0400", ""],
    ],
  );
  assert.ok(items.every((entry) => entry.mono));
});

test("pricing hints at time-of-day windows for a field", () => {
  const pricing = {
    input_per_mtok: 1,
    time_windows: [
      {
        label: "off_peak",
        pricing: { input_per_mtok: 0.5 },
        utc_ranges: [{ start: "16:30", end: "00:30" }],
      },
    ],
  };
  const details = buildModelDetails(modelRow(), pricing, {});
  const [input] = section(details, "pricing").items;
  assert.match(input.hint, /off peak/);
  assert.match(input.hint, /\$0\.50/);
});

test("a model without metadata only lists what the provider reported", () => {
  const details = buildModelDetails(modelRow({ model: { id: "local-llm", owned_by: "library" } }), {}, {});
  assert.equal(details.has_metadata, false);
  assert.deepEqual(sectionKeys(details), ["listing"]);
});

// ---- Metadata layers and views ----

function layersFixture(overrides = {}) {
  return {
    selector: "openai/gpt-4o",
    effective: modelRow().model.metadata,
    provider: { context_window: 4096, capabilities: { tools: false } },
    catalog: { display_name: "GPT-4o", modes: ["chat"], context_window: 128000, pricing: { input_per_mtok: 2.5 } },
    config: null,
    sources: {
      display_name: "catalog",
      context_window: "provider",
      modes: "catalog",
      categories: "inferred",
      "capabilities.tools": "provider",
      "capabilities.vision": "catalog",
      "rankings.lmarena": "catalog",
    },
    ...overrides,
  };
}

test("metadataViewOptions offers config only when an override exists", () => {
  assert.deepEqual(
    metadataViewOptions(layersFixture()).map((option) => option.value),
    ["effective", "provider", "catalog"],
  );
  assert.deepEqual(
    metadataViewOptions(layersFixture({ config: { display_name: "x" } })).map((option) => option.value),
    ["effective", "provider", "catalog", "config"],
  );
  assert.deepEqual(metadataViewOptions(null), []);
});

test("resolveMetadataView falls back to effective for views a row lacks", () => {
  assert.equal(resolveMetadataView("catalog", layersFixture()), "catalog");
  assert.equal(resolveMetadataView("config", layersFixture()), "effective");
  assert.equal(resolveMetadataView("provider", null), "effective");
});

test("metadataForView reads the inventory row for the effective view", () => {
  const row = modelRow();
  assert.equal(metadataForView(row, null, "effective"), row.model.metadata);
  assert.equal(metadataForView(row, layersFixture(), "effective"), row.model.metadata);
  assert.deepEqual(metadataForView(row, layersFixture(), "provider"), layersFixture().provider);
  assert.equal(metadataForView(row, layersFixture(), "config"), null);
});

test("metadataSourceLabel names every layer and ignores unknown ones", () => {
  assert.equal(metadataSourceLabel("provider"), m.models_details_source_provider());
  assert.equal(metadataSourceLabel("catalog"), m.models_details_source_catalog());
  assert.equal(metadataSourceLabel("config"), m.models_details_source_config());
  assert.equal(metadataSourceLabel("inferred"), m.models_details_source_inferred());
  assert.equal(metadataSourceLabel(""), "");
  assert.equal(metadataSourceLabel("bogus"), "");
});

test("the effective view labels each field with its source layer", () => {
  const details = buildModelDetails(modelRow(), {}, {}, { view: "effective", layers: layersFixture() });
  const properties = section(details, "properties").items;
  const byLabel = Object.fromEntries(properties.map((entry) => [entry.label, entry.source]));
  assert.equal(byLabel[m.models_details_display_name()], m.models_details_source_catalog());
  assert.equal(byLabel[m.models_details_context_window()], m.models_details_source_provider());
  assert.equal(byLabel[m.models_details_categories()], m.models_details_source_inferred());
  assert.equal(byLabel[m.models_details_tags()], "");
  assert.deepEqual(
    section(details, "capabilities").chips.map((chip) => chip.source),
    ["", m.models_details_source_provider(), m.models_details_source_catalog()],
  );
  assert.equal(section(details, "rankings").items[0].source, m.models_details_source_catalog());
});

test("the effective view carries no source labels before the layers arrive", () => {
  const details = buildModelDetails(modelRow(), {}, {});
  assert.ok(section(details, "properties").items.every((entry) => !entry.source));
});

test("layer views render only that layer, with its own pricing", () => {
  const provider = buildModelDetails(modelRow(), { input_per_mtok: 3 }, { input_per_mtok: "config.yaml" }, {
    view: "provider",
    layers: layersFixture(),
  });
  assert.equal(provider.view, "provider");
  assert.equal(provider.has_metadata, true);
  assert.deepEqual(sectionKeys(provider), ["listing", "properties", "capabilities"]);
  assert.equal(
    itemValue(provider, "properties", m.models_details_context_window()),
    m.models_details_tokens({ value: formatNumber(4096) }),
  );
  assert.ok(section(provider, "properties").items.every((entry) => !entry.source));

  const catalog = buildModelDetails(modelRow(), { input_per_mtok: 3 }, { input_per_mtok: "config.yaml" }, {
    view: "catalog",
    layers: layersFixture(),
  });
  assert.deepEqual(sectionKeys(catalog), ["properties", "pricing"]);
  assert.deepEqual(
    section(catalog, "pricing").items.map((entry) => [entry.value, entry.source]),
    [["$2.50", ""]],
  );
});

test("an empty layer view reports it has no metadata", () => {
  const details = buildModelDetails(modelRow(), {}, {}, { view: "config", layers: layersFixture() });
  assert.equal(details.has_metadata, false);
  assert.deepEqual(sectionKeys(details), []);
});
