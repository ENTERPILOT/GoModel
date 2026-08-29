import { test } from "node:test";
import assert from "node:assert/strict";
import {
  categoryColumns,
  categoryColspan,
} from "../src/pages/models/categoryColumns.js";

test("categoryColspan matches each category's rendered columns", () => {
  assert.equal(categoryColspan("all"), 3);
  assert.equal(categoryColspan("text_generation"), 4);
  assert.equal(categoryColspan("embedding"), 3);
  assert.equal(categoryColspan("image"), 3);
  assert.equal(categoryColspan("audio"), 4);
  assert.equal(categoryColspan("video"), 4);
  assert.equal(categoryColspan("utility"), 4);
});

test("unknown categories fall back to the 'all' columns", () => {
  assert.deepEqual(categoryColumns("bogus"), categoryColumns("all"));
});

test("price columns carry the col-price class", () => {
  const [inputOutput] = categoryColumns("all");
  assert.equal(inputOutput.class, "col-price");
  for (const col of categoryColumns("utility")) {
    assert.equal(col.class, "col-price");
  }
});

test("input/output column formats both prices from the pricing arg", () => {
  const [inputOutput] = categoryColumns("text_generation");
  const text = inputOutput.value({}, { input_per_mtok: 1, output_per_mtok: 2 });
  assert.match(text, /\//);
  assert.notEqual(text, "— / —");
  assert.equal(inputOutput.value({}, undefined), "— / —");
});
