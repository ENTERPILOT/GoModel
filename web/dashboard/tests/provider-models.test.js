// Pure-logic tests for the provider editor's custom models list/text control.
import test from "node:test";
import assert from "node:assert/strict";

import {
  modelRowsToList,
  modelRowsToText,
  modelsTextToRows,
  modelsToRows,
  parseModelList,
} from "../src/pages/providers-config/providerModels.js";

test("parseModelList splits on commas and newlines, trims, and dedupes", () => {
  const cases = [
    { name: "empty", input: "", want: [] },
    { name: "undefined", input: undefined, want: [] },
    { name: "blanks only", input: " ,\n , \r\n", want: [] },
    { name: "commas", input: " gpt-4o, gpt-4o-mini ,,", want: ["gpt-4o", "gpt-4o-mini"] },
    { name: "lines", input: "gpt-4o\r\n\ngpt-4o-mini\n", want: ["gpt-4o", "gpt-4o-mini"] },
    { name: "mixed", input: "a, b\nc", want: ["a", "b", "c"] },
    { name: "first occurrence wins", input: "b\na\nb, a", want: ["b", "a"] },
  ];
  for (const { name, input, want } of cases) {
    assert.deepEqual(parseModelList(input), want, name);
  }
});

test("modelRowsToList drops blank rows, dedupes, and splits pasted lists", () => {
  const rows = [
    { value: " gpt-4o " },
    { value: "" },
    { value: "gpt-4o-mini, o3" },
    { value: "gpt-4o" },
    null,
  ];
  assert.deepEqual(modelRowsToList(rows), ["gpt-4o", "gpt-4o-mini", "o3"]);
  // No rows is the auto-discover case and must stay an empty array.
  assert.deepEqual(modelRowsToList([]), []);
  assert.deepEqual(modelRowsToList(undefined), []);
});

test("rows and text round-trip through each other", () => {
  const rows = modelsToRows(["gpt-4o", "gpt-4o-mini"]);
  assert.deepEqual(rows, [{ value: "gpt-4o" }, { value: "gpt-4o-mini" }]);

  const text = modelRowsToText([...rows, { value: " " }]);
  assert.equal(text, "gpt-4o\ngpt-4o-mini");
  assert.deepEqual(modelsTextToRows(text), rows);
  assert.deepEqual(modelsTextToRows(""), []);
  assert.deepEqual(modelsToRows(undefined), []);
});
