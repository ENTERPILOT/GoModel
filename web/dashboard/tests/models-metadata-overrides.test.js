import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import {
  CAPABILITY_INHERIT,
  CAPABILITY_SUPPORTED,
  CAPABILITY_UNSUPPORTED,
  buildMetadataOverridePayload,
  findMetadataOverride,
  formFromOverride,
  inheritedTokenLimits,
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
  for (const bad of ["0", "-1", "1.5", "abc", "9007199254740993"]) {
    const form = formFromOverride(null);
    form.context_window = bad;
    assert.deepEqual(buildMetadataOverridePayload(form), {
      error: m.models_metadata_invalid_tokens({ field: m.models_details_context_window() }),
    });
  }
});

test("inheritedTokenLimits leaves out the limits the override sets itself", () => {
  const effective = { context_window: 32000, max_output_tokens: 4096, capabilities: { vision: true } };
  assert.deepEqual(inheritedTokenLimits(effective, null), { context_window: 32000, max_output_tokens: 4096 });
  assert.deepEqual(inheritedTokenLimits(effective, { metadata: { context_window: 32000 } }), {
    max_output_tokens: 4096,
  });
  assert.deepEqual(inheritedTokenLimits(null, null), {});
});

// Regression: the editor opened on a list that was still loading, or had
// failed to load, started blank, and saving it replaced the stored override.
test("editing waits for the stored overrides and ignores retired list responses", () => {
  const SRC = fileURLToPath(new URL("../src", import.meta.url));
  const store = readFileSync(join(SRC, "pages/models/metadataOverrides.svelte.js"), "utf8");

  assert.ok(store.includes("loaded = $state(false);"), "editing must start disabled");
  const canEdit = (store.match(/canEdit\(row\) \{[\s\S]*?\n  \}/) || [""])[0];
  assert.ok(canEdit.includes("this.loaded &&"), "canEdit must require a loaded list");

  const fetchOverrides = (store.match(/async fetchOverrides\(\) \{[\s\S]*?\n  \}/) || [""])[0];
  assert.ok(fetchOverrides, "fetchOverrides missing");
  // Each expression must exist: indexOf's -1 would pass an order check.
  const assertBefore = (first, second, message) => {
    const firstIndex = fetchOverrides.indexOf(first);
    const secondIndex = fetchOverrides.indexOf(second);
    assert.ok(firstIndex >= 0 && secondIndex >= 0 && firstIndex < secondIndex, message);
  };
  assertBefore("const seq = ++this.#fetchSeq;", "await ", "the sequence must be taken before the request");
  assertBefore("seq !== this.#fetchSeq", "this.overrides =", "a retired response must be dropped before it is applied");
  assert.ok(fetchOverrides.includes('this.loaded = outcome.status === "ok";'), "only a successful load enables editing");
});

// Regression: closing the editor during a save and opening another model let
// the first save's completion close the second editor.
test("the metadata editor cannot be closed while a save is in flight", () => {
  const SRC = fileURLToPath(new URL("../src", import.meta.url));
  const editor = readFileSync(join(SRC, "pages/models/MetadataOverrideEditor.svelte"), "utf8");
  assert.ok(editor.includes("canClose={() => !mo.submitting}"));
});
