// Contract tests for virtual-model target reordering.
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
  vmFormPopulatedTargetCount,
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

test("target helpers tolerate a null or malformed form", () => {
  assert.equal(vmFormTargetCount(null), 0);
  assert.equal(vmFormTargetCount(undefined), 0);
  assert.deepEqual(flattenFormTargets(null), []);
  assert.deepEqual(flattenFormTargets(undefined), []);
  // Non-array targets still surfaces the primary row; moveFormTarget refuses
  // because it cannot build a contiguous list.
  assert.deepEqual(flattenFormTargets({ target_model: "a", targets: "bad" }), [
    { provider: "", model: "a", weight: 1 },
  ]);
  assert.equal(moveFormTarget(null, 0, 1), false);
  assert.equal(
    moveFormTarget(
      { ...defaultVirtualModelForm(), target_model: "a", targets: "bad" },
      0,
      1,
    ),
    false,
  );
});

test("flattened indices the editor passes to moveFormTarget align with the list", () => {
  // The editor renders the primary row with index 0 and extras from 1 on —
  // but only when the primary holds a model: an empty primary row is not in
  // the flattened list, so extras must start at 0. This pins that contract;
  // a mismatch made drops on unsaved/new rows land out of bounds.
  const withPrimary = formWithTargets(["a", "b", "c"]);
  assert.deepEqual(modelsOf(withPrimary), ["a", "b", "c"]);
  // UI: primary index 0, extras 1 and 2 — all in bounds.
  assert.equal(moveFormTarget(withPrimary, 2, 0), true);
  assert.deepEqual(modelsOf(withPrimary), ["c", "a", "b"]);

  // UI: primary row holds no model (empty slot in a fresh form): the row is
  // not in the flattened list, so extras must be indexed from 0.
  const noPrimary = {
    ...defaultVirtualModelForm(),
    source: "new-vm",
    target_model: "",
    target_weight: 1,
    targets: [
      { provider: "", model: "b", weight: 1 },
      { provider: "", model: "c", weight: 1 },
    ],
  };
  assert.equal(vmFormTargetCount(noPrimary), 2);
  assert.deepEqual(modelsOf(noPrimary), ["b", "c"]);
  // UI: extras at indices 0 and 1 — the move the component now sends.
  assert.equal(moveFormTarget(noPrimary, 1, 0), true);
  assert.deepEqual(modelsOf(noPrimary), ["c", "b"]);
});

test("non-positive weights normalize to 1, matching the backend contract", () => {
  // The backend treats a non-positive or unset weight as 1
  // (internal/virtualmodels/types.go), so the frontend mirrors that instead
  // of preserving a value the gateway would ignore.
  const form = {
    ...defaultVirtualModelForm(),
    source: "w",
    target_model: "a",
    target_weight: 0,
    targets: [{ provider: "", model: "b", weight: 0 }],
  };
  assert.equal(moveFormTarget(form, 1, 0), true);
  assert.equal(form.target_weight, 1);
  assert.equal(form.targets[0].weight, 1);

  // Negative weights follow the same rule: the explicit `weight > 0`
  // normalization maps them to 1.
  const negative = {
    ...defaultVirtualModelForm(),
    source: "neg",
    target_model: "a",
    target_weight: -2,
    targets: [{ provider: "", model: "b", weight: -1 }],
  };
  assert.equal(moveFormTarget(negative, 1, 0), true);
  assert.equal(negative.target_weight, 1);
  assert.equal(negative.targets[0].weight, 1);
});

test("vmFormPopulatedTargetCount ignores blank placeholder rows", () => {
  const oneFilled = {
    ...defaultVirtualModelForm(),
    target_model: "a",
    targets: [{ provider: "", model: "", weight: 1 }],
  };
  assert.equal(vmFormPopulatedTargetCount(oneFilled), 1);

  const twoFilled = {
    ...defaultVirtualModelForm(),
    target_model: "a",
    targets: [
      { provider: "", model: "b", weight: 1 },
      { provider: "", model: "", weight: 1 },
    ],
  };
  assert.equal(vmFormPopulatedTargetCount(twoFilled), 2);
});
