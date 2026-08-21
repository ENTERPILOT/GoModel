import test from "node:test";
import assert from "node:assert/strict";

import { moveItem, sortableShift, sortableTargetIndex } from "../src/lib/utils/sortable.js";

test("moveItem relocates one element and leaves bad input alone", () => {
  const items = ["a", "b", "c", "d"];
  assert.deepEqual(moveItem(items, 0, 2), ["b", "c", "a", "d"]);
  assert.deepEqual(moveItem(items, 3, 0), ["d", "a", "b", "c"]);
  assert.deepEqual(moveItem(items, 1, 1), items);
  assert.deepEqual(moveItem(items, -1, 1), items);
  assert.deepEqual(moveItem(items, 0, 4), items);
  assert.deepEqual(moveItem(items, 0, 2), ["b", "c", "a", "d"]);
  assert.deepEqual(items, ["a", "b", "c", "d"], "input is not mutated");
  assert.deepEqual(moveItem(null, 0, 1), []);
});

// Four 100px rows stacked from y=0: midpoints at 50, 150, 250, 350.
const midpoints = [50, 150, 250, 350];

test("sortableTargetIndex moves past siblings once the pointer crosses their centre", () => {
  assert.equal(sortableTargetIndex(0, 50, midpoints), 0);
  assert.equal(sortableTargetIndex(0, 149, midpoints), 0);
  assert.equal(sortableTargetIndex(0, 151, midpoints), 1);
  assert.equal(sortableTargetIndex(0, 260, midpoints), 2);
  assert.equal(sortableTargetIndex(0, 900, midpoints), 3);
  assert.equal(sortableTargetIndex(3, 249, midpoints), 2);
  assert.equal(sortableTargetIndex(3, 10, midpoints), 0);
  assert.equal(sortableTargetIndex(2, 250, midpoints), 2);
  assert.equal(sortableTargetIndex(0, 0, []), -1);
});

test("sortableShift slides only the rows between the old and new slot", () => {
  // Dragging row 0 down to slot 2: rows 1 and 2 move up by one row.
  assert.deepEqual([0, 1, 2, 3].map((i) => sortableShift(i, 0, 2, 100)), [0, -100, -100, 0]);
  // Dragging row 3 up to slot 1: rows 1 and 2 move down.
  assert.deepEqual([0, 1, 2, 3].map((i) => sortableShift(i, 3, 1, 100)), [0, 100, 100, 0]);
  assert.deepEqual([0, 1, 2, 3].map((i) => sortableShift(i, 1, 1, 100)), [0, 0, 0, 0]);
});
