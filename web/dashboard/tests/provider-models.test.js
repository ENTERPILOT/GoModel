// Pure-logic tests for the provider editor's custom models list/text control.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

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

// There is no DOM test harness in this suite, so the editor's keyboard and
// view wiring is asserted on the component and store source.
const SRC = fileURLToPath(new URL("../src", import.meta.url));
const control = readFileSync(
  join(SRC, "pages/providers-config/ProviderModelsControl.svelte"),
  "utf8",
);
const store = readFileSync(
  join(SRC, "pages/providers-config/providersConfig.svelte.js"),
  "utf8",
);

test("Enter in a model row adds and focuses the next row instead of submitting", () => {
  const keydown = (control.match(/function onRowKeydown\([^)]*\) \{[\s\S]*?\n  \}/) || [""])[0];
  assert.match(keydown, /event\.key === "Enter" && !event\.isComposing/);
  assert.match(keydown, /event\.preventDefault\(\)/);
  assert.match(keydown, /addRow\(index \+ 1\)/);

  const addRow = (control.match(/async function addRow\([^)]*\) \{[\s\S]*?\n  \}/) || [""])[0];
  assert.match(addRow, /await tick\(\)/);
  assert.match(addRow, /getElementById\(rowId\(index\)\)\?\.focus\(\)/);
});

test("removing a model row keeps focus inside the control", () => {
  const removeRow = (control.match(/async function removeRow\([^)]*\) \{[\s\S]*?\n  \}/) || [""])[0];
  assert.match(removeRow, /providersConfig\.removeModelRow\(index\)/);
  assert.match(removeRow, /await tick\(\)/);
  assert.match(removeRow, /getElementById\(next\)\?\.focus\(\)/);
  assert.match(control, /onclick=\{\(\) => removeRow\(index\)\}/);
  // With no rows left, the Add model button takes the control's id.
  assert.match(control, /id=\{rows\.length === 0 \? id : undefined\}/);
});

test("every model row links to the models error message", () => {
  const input = (control.match(/<input[\s\S]*?\/>/) || [""])[0];
  assert.match(input, /aria-invalid=\{invalid \? "true" : undefined\}/);
  assert.match(input, /aria-describedby=\{describedBy\}/);
});

test("switching to the text view starts from the current rows", () => {
  const setView = (store.match(/\n  setModelsView\([^)]*\) \{[\s\S]*?\n  \}/) || [""])[0];
  assert.match(setView, /this\.modelsText = modelRowsToText\(this\.form\.models\)/);
  assert.match(setView, /this\.modelsView = view/);
  assert.match(control, /onchange=\{\(view\) => providersConfig\.setModelsView\(view\)\}/);
  assert.match(control, /oninput=\{\(event\) => providersConfig\.setModelsText\(event\.currentTarget\.value\)\}/);
});
