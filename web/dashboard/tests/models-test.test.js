import test from "node:test";
import assert from "node:assert/strict";

import {
  MODEL_TEST_MAX_TOKENS,
  MODEL_TEST_PROMPT,
  MODEL_TEST_SNIPPET_LENGTH,
  buildModelTestRequest,
  modelTestSelector,
  modelTestUserPath,
  replySnippet,
  rowCanTestModel,
  summarizeModelTest,
} from "../src/pages/models/modelTest.js";

function modelRow(overrides = {}) {
  return {
    key: "model:openai/gpt-4o",
    display_name: "openai/gpt-4o",
    selector: "",
    is_alias: false,
    alias: null,
    access: { effective_enabled: true },
    model: { id: "gpt-4o", metadata: { modes: ["chat"], categories: ["text_generation"] } },
    ...overrides,
  };
}

function aliasRow(overrides = {}) {
  return {
    key: "alias:smart",
    display_name: "smart",
    is_alias: true,
    alias: { name: "smart", enabled: true },
    access: null,
    model: { id: "smart", object: "model" },
    ...overrides,
  };
}

test("modelTestSelector sends the row's own selector", () => {
  const cases = [
    { name: "qualified display name", row: modelRow(), want: "openai/gpt-4o" },
    { name: "explicit selector wins", row: modelRow({ selector: "azure-east/gpt-4o" }), want: "azure-east/gpt-4o" },
    { name: "virtual model name", row: aliasRow({ display_name: "ignored" }), want: "smart" },
    { name: "missing row", row: null, want: "" },
  ];
  for (const { name, row, want } of cases) {
    assert.equal(modelTestSelector(row), want, name);
  }
});

test("rowCanTestModel offers the test only where a chat prompt makes sense", () => {
  const meta = (metadata) => ({ id: "x", metadata });
  const cases = [
    { name: "text generation", row: modelRow(), want: true },
    { name: "multi-category including text", row: modelRow({ model: meta({ categories: ["image", "text_generation"] }) }), want: true },
    { name: "embedding only", row: modelRow({ model: meta({ categories: ["embedding"], modes: ["embedding"] }) }), want: false },
    { name: "image only", row: modelRow({ model: meta({ categories: ["image"] }) }), want: false },
    { name: "audio only", row: modelRow({ model: meta({ categories: ["audio"] }) }), want: false },
    { name: "modes without categories", row: modelRow({ model: meta({ modes: ["responses"] }) }), want: true },
    { name: "non-chat modes without categories", row: modelRow({ model: meta({ modes: ["audio_speech"] }) }), want: false },
    { name: "no metadata", row: modelRow({ model: { id: "llama3" } }), want: true },
    { name: "disabled model", row: modelRow({ access: { effective_enabled: false } }), want: false },
    { name: "virtual model over unknown target", row: aliasRow(), want: true },
    { name: "virtual model over embedding target", row: aliasRow({ model: meta({ categories: ["embedding"] }) }), want: false },
    { name: "disabled virtual model", row: aliasRow({ alias: { name: "smart", enabled: false } }), want: false },
    { name: "missing row", row: null, want: false },
  ];
  for (const { name, row, want } of cases) {
    assert.equal(rowCanTestModel(row), want, name);
  }
});

test("modelTestUserPath picks the first allowed user path", () => {
  assert.equal(modelTestUserPath(modelRow()), "");
  assert.equal(modelTestUserPath(modelRow({ access: { user_paths: ["", " /team/a ", "/team/b"] } })), "/team/a");
  assert.equal(modelTestUserPath(aliasRow()), "");
});

test("buildModelTestRequest sends the default prompt with a small token budget", () => {
  assert.deepEqual(buildModelTestRequest("openai/gpt-4o"), {
    model: "openai/gpt-4o",
    messages: [{ role: "user", content: MODEL_TEST_PROMPT }],
    max_tokens: MODEL_TEST_MAX_TOKENS,
  });
});

test("replySnippet flattens whitespace and truncates long replies", () => {
  assert.equal(replySnippet("  Hello,\n\n  world!  "), "Hello, world!");
  assert.equal(replySnippet(null), "");
  const long = replySnippet("a".repeat(MODEL_TEST_SNIPPET_LENGTH + 10));
  assert.equal(long.length, MODEL_TEST_SNIPPET_LENGTH);
  assert.ok(long.endsWith("…"));
  assert.equal(replySnippet("abcdef", 4), "abc…");
});

test("summarizeModelTest reports success with latency and the reply", () => {
  const cases = [
    {
      name: "text reply",
      outcome: {
        ok: true,
        status: 200,
        durationMs: 812.4,
        data: { choices: [{ message: { role: "assistant", content: "Hello there!" }, finish_reason: "stop" }] },
      },
      want: { state: "ok", durationMs: 812, reply: "Hello there!", finishReason: "stop" },
    },
    {
      name: "empty reply keeps the finish reason",
      outcome: {
        ok: true,
        status: 200,
        durationMs: 1500,
        data: { choices: [{ message: { role: "assistant", content: "" }, finish_reason: "length" }] },
      },
      want: { state: "ok", durationMs: 1500, reply: "", finishReason: "length" },
    },
    {
      name: "unexpected body",
      outcome: { ok: true, status: 200, durationMs: -5, data: null },
      want: { state: "ok", durationMs: 0, reply: "", finishReason: "" },
    },
  ];
  for (const { name, outcome, want } of cases) {
    assert.deepEqual(summarizeModelTest(outcome, "fallback"), want, name);
  }
});

test("summarizeModelTest reports the gateway error message or the fallback", () => {
  assert.deepEqual(
    summarizeModelTest(
      { ok: false, status: 404, durationMs: 40, data: { error: { message: " model not found " } } },
      "fallback",
    ),
    { state: "error", durationMs: 40, status: 404, message: "model not found" },
  );
  assert.deepEqual(
    summarizeModelTest({ ok: false, durationMs: 60000 }, "timed out"),
    { state: "error", durationMs: 60000, status: 0, message: "timed out" },
  );
});
