import test from "node:test";
import assert from "node:assert/strict";

import {
  CAPABILITY_INHERIT,
  CAPABILITY_SUPPORTED,
  CAPABILITY_UNSUPPORTED,
  buildMetadataOverridePayload,
  findMetadataOverride,
  formFromOverride,
  metadataOverrideSelector,
} from "../src/pages/models/metadataOverridesLogic.js";
import * as m from "../src/lib/paraglide/messages.js";

test("metadataOverrideSelector is exact provider/model only", () => {
  assert.equal(
    metadataOverrideSelector({ provider_name: "openrouter", model: { id: "meta-llama/llama-3.2-vision" } }),
    "openrouter/meta-llama/llama-3.2-vision",
  );
  assert.equal(metadataOverrideSelector({ model: { id: "flux" } }), "");
  assert.equal(metadataOverrideSelector(null), "");
});

test("findMetadataOverride matches by selector", () => {
  const overrides = [{ selector: "pollinations/flux" }, { selector: "pollinations/openai" }];
  assert.equal(findMetadataOverride(overrides, "pollinations/openai"), overrides[1]);
  assert.equal(findMetadataOverride(overrides, "other/flux"), null);
  assert.equal(findMetadataOverride(null, "pollinations/flux"), null);
});

test("formFromOverride maps stored fields and defaults the rest to inherit", () => {
  const form = formFromOverride({
    metadata: { categories: ["image"], capabilities: { vision: true, audio_input: false }, context_window: 4096 },
  });
  assert.deepEqual(form, {
    categories: ["image"],
    capabilities: {
      vision: CAPABILITY_SUPPORTED,
      audio_input: CAPABILITY_UNSUPPORTED,
      video_input: CAPABILITY_INHERIT,
      pdf_input: CAPABILITY_INHERIT,
    },
    context_window: "4096",
    max_output_tokens: "",
  });
  assert.equal(formFromOverride(null).capabilities.vision, CAPABILITY_INHERIT);
});

test("buildMetadataOverridePayload round-trips the form and drops inherited fields", () => {
  const form = formFromOverride(null);
  form.categories = ["image", "text_generation"];
  form.capabilities.pdf_input = CAPABILITY_UNSUPPORTED;
  form.max_output_tokens = 8192;
  assert.deepEqual(buildMetadataOverridePayload(form), {
    metadata: {
      categories: ["text_generation", "image"],
      capabilities: { pdf_input: false },
      max_output_tokens: 8192,
    },
  });
});

test("buildMetadataOverridePayload rejects empty and invalid forms", () => {
  assert.deepEqual(buildMetadataOverridePayload(formFromOverride(null)), { error: m.models_metadata_empty() });
  for (const bad of ["0", "-1", "1.5", "abc"]) {
    const form = formFromOverride(null);
    form.context_window = bad;
    assert.deepEqual(buildMetadataOverridePayload(form), {
      error: m.models_metadata_invalid_tokens({ field: m.models_details_context_window() }),
    });
  }
});
