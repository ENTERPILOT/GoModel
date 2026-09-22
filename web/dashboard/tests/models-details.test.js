import test from "node:test";
import assert from "node:assert/strict";

import {
  buildModelDetails,
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

test("rowHasModelDetails: real models expand, alias stubs do not", () => {
  assert.equal(rowHasModelDetails(modelRow()), true);
  assert.equal(rowHasModelDetails(modelRow({ model: { id: "bare" } })), true);
  assert.equal(rowHasModelDetails({ is_alias: true, model: { id: "alias", object: "model" } }), false);
  assert.equal(
    rowHasModelDetails({ is_alias: true, model: { id: "gpt-4o", owned_by: "openai" } }),
    true,
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
    { label: "audio_input", enabled: false },
    { label: "tools", enabled: true },
    { label: "vision", enabled: true },
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
