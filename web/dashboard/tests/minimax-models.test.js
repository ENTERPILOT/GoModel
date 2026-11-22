import assert from "node:assert/strict";
import test from "node:test";
import { playgroundModelOptions, buildPlaygroundRequest } from "../src/pages/playground/playgroundLogic.js";
import { modelPickerOptions, modelSelectorOptions } from "../src/lib/utils/modelSelectors.js";

function entry(id, provider = "minimax-cn", type = "minimax", modes = ["chat"]) {
  return { selector: `${provider}/${id}`, provider_name: provider, provider_type: type,
    model: { id, metadata: { modes } } };
}

const inventory = [
  entry("MiniMax-M2.7"), entry("MiniMax-M3"), entry("MiniMax-M3.1-Flash"),
  entry("speech-2.6-hd", "minimax-cn", "minimax", ["speech"]),
  entry("image-01", "minimax-cn", "minimax", ["image_generation"]),
  entry("custom-deployment"), entry("other-model", "another", "other"),
  entry("minimax/minimax-m2.7", "gateway", "openrouter"),
  entry("minimax/minimax-m3", "gateway", "openrouter"),
  entry("minimax/minimax-m3.1-flash:free", "gateway", "openrouter"),
];

test("ordinary suggestions offer Flash before M3 and preserve routing inventory", () => {
  const before = structuredClone(inventory);
  for (const picker of [playgroundModelOptions, modelPickerOptions]) {
    const ids = picker(inventory).map((item) => item.id || item.value);
    assert(!ids.some((id) => id.includes("m2.7") || id.includes("M2.7")));
    for (const [flash, m3] of [
      ["minimax-cn/MiniMax-M3.1-Flash", "minimax-cn/MiniMax-M3"],
      ["gateway/minimax/minimax-m3.1-flash:free", "gateway/minimax/minimax-m3"],
    ]) {
      assert(ids.includes(flash));
      assert(ids.indexOf(flash) < ids.indexOf(m3));
    }
    assert(ids.includes("minimax-cn/custom-deployment"));
    assert(ids.includes("another/other-model"));
  }
  const plugin = modelPickerOptions(inventory).map((item) => item.value);
  assert(plugin.includes("minimax-cn/speech-2.6-hd"));
  assert(plugin.includes("minimax-cn/image-01"));
  assert.deepEqual(inventory, before);
  assert(modelSelectorOptions(inventory).some((item) => item.value === "minimax-cn/MiniMax-M2.7"));
});

test("the first MiniMax choice becomes the request model without resetting saved/custom IDs", () => {
  const options = playgroundModelOptions(inventory.filter((item) => item.provider_type === "minimax" && item.model.id.startsWith("MiniMax-")));
  assert.equal(options[0].id, "minimax-cn/MiniMax-M3.1-Flash");
  for (const endpoint of ["chat", "responses", "messages"]) {
    for (const model of [options[0].id, "minimax-cn/MiniMax-M2.7", "minimax-cn/my-custom-model"]) {
      const request = buildPlaygroundRequest(endpoint, { model, messages: [{ role: "user", content: "Hello" }] });
      assert.equal(JSON.parse(JSON.stringify(request)).model, model);
    }
  }
});

test("disabled Flash is never selected and unknown provider routes are unchanged", () => {
  const flash = entry("MiniMax-M3.1-Flash");
  flash.access = { effective_enabled: false };
  assert.deepEqual(playgroundModelOptions([flash, entry("MiniMax-M3")]).map((item) => item.id), ["minimax-cn/MiniMax-M3"]);
  const custom = entry("MiniMax-M2.7", "custom", "openai");
  assert.equal(playgroundModelOptions([custom])[0].id, custom.selector);
});
