// Contract tests for virtual-model target reordering (PR #77).
// These pin the behavior a reviewer sees in the editor so a future refactor
// cannot silently change it:
//   1. Drag-and-move, not drag-and-replace: dropping a row onto another row
//      inserts the dragged row at the drop row's position and shifts the
//      others down — the drop target is not swapped away.
//   2. The save payload the editor sends after a reorder lists the targets
//      in exactly the new display order (failover primary first), so the
//      stored configuration and the rendered chain agree.
//   3. Reopening a reordered virtual model restores the saved order.

import test from "node:test";
import assert from "node:assert/strict";

import {
  aliasFormTargets,
  buildVirtualModelSavePayload,
  defaultVirtualModelForm,
  flattenFormTargets,
  moveFormTarget,
  vmFormTargetCount,
} from "../src/pages/models/vmForm.js";

function formWithTargets(models) {
  const [primary, ...extras] = models;
  return {
    ...defaultVirtualModelForm(),
    source: "coding",
    target_provider: "",
    target_model: primary,
    target_weight: 1,
    targets: extras.map((model) => ({ provider: "", model, weight: 1 })),
  };
}

function modelsOf(form) {
  return flattenFormTargets(form).map((t) => t.model);
}

test("drag onto a row inserts the dragged row at that position (move, not swap)", () => {
  // The scenario from the PR screenshots: three rows, drag the last onto the
  // first. The dragged row lands first and the previous rows shift down — the
  // drop row is not replaced.
  const form = formWithTargets(["a", "b", "c"]);
  assert.equal(moveFormTarget(form, 2, 0), true);
  assert.deepEqual(modelsOf(form), ["c", "a", "b"]);

  // Drag the first row onto the last: it lands last, the middle rows shift up.
  assert.equal(moveFormTarget(form, 0, 2), true);
  assert.deepEqual(modelsOf(form), ["a", "b", "c"]);

  // Adjacent move down: insert-between equals "swap with the next row".
  assert.equal(moveFormTarget(form, 0, 1), true);
  assert.deepEqual(modelsOf(form), ["b", "a", "c"]);
});

test("save payload after a reorder lists targets in the new display order", () => {
  const form = formWithTargets(["a", "b", "c"]);
  form.strategy = "failover";
  moveFormTarget(form, 2, 0);

  const { payload, isRedirect } = buildVirtualModelSavePayload(form, "", "edit");
  assert.equal(isRedirect, true);
  assert.deepEqual(
    payload.targets.map((t) => t.model),
    ["c", "a", "b"],
  );
  assert.equal(payload.strategy, "failover");
});

test("reopening a reordered virtual model restores the saved order", () => {
  // Stored shape after the reorder save above: primary first, extras after.
  const stored = {
    name: "coding",
    targets: [{ model: "c" }, { model: "a" }, { model: "b" }],
    strategy: "failover",
  };
  const { primaryModel, extraTargets } = aliasFormTargets(stored);
  assert.equal(primaryModel, "c");
  assert.deepEqual(
    extraTargets.map((t) => t.model),
    ["a", "b"],
  );

  // Flattened back to one list, the display order matches what was saved.
  const reopened = formWithTargets(["c", "a", "b"]);
  assert.equal(vmFormTargetCount(reopened), 3);
  assert.deepEqual(modelsOf(reopened), ["c", "a", "b"]);
});

test("weights and explicit provider pins survive a reorder", () => {
  const form = {
    ...defaultVirtualModelForm(),
    source: "pool",
    strategy: "round_robin",
    target_provider: "",
    target_model: "a",
    target_weight: 1,
    targets: [
      { provider: "team", model: "b", weight: 3 },
      { provider: "", model: "c", weight: 1 },
    ],
  };
  moveFormTarget(form, 1, 0);
  assert.equal(form.target_model, "b");
  assert.equal(form.target_provider, "team");
  assert.equal(form.target_weight, 3);
  assert.deepEqual(
    form.targets.map((t) => `${t.model}:${t.weight}`),
    ["a:1", "c:1"],
  );
});
