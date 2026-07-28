import test from "node:test";
import assert from "node:assert/strict";

import { debounced } from "../src/lib/utils/debounce.js";

const tick = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

test("debounced fires once, after the delay, with the last arguments", async () => {
  const calls = [];
  const run = debounced((v) => calls.push(v), 20);
  run("a");
  run("b");
  run("c");
  assert.deepEqual(calls, []);
  await tick(50);
  assert.deepEqual(calls, ["c"]);
});

test("debounced fires again for a later burst", async () => {
  let count = 0;
  const run = debounced(() => count++, 20);
  run();
  await tick(50);
  run();
  await tick(50);
  assert.equal(count, 2);
});

test("cancel drops a pending call", async () => {
  let count = 0;
  const run = debounced(() => count++, 20);
  run();
  run.cancel();
  await tick(50);
  assert.equal(count, 0);
});

test("cancel on an idle debounce is a no-op", async () => {
  let count = 0;
  const run = debounced(() => count++, 20);
  run.cancel();
  run();
  await tick(50);
  assert.equal(count, 1);
});
