import test from "node:test";
import assert from "node:assert/strict";

import {
  gettingStartedSteps,
  gettingStartedProgress,
  gettingStartedVisible,
  loadGettingStartedDismissed,
  saveGettingStartedDismissed,
} from "../src/pages/overview/gettingStartedLogic.js";

function provider(runtime) {
  return { name: "openai", type: "openai", runtime };
}

function doneByStep(input) {
  return Object.fromEntries(
    gettingStartedSteps(input).map((step) => [step.id, step.done]),
  );
}

test("gettingStartedSteps ticks each step from the overview data", () => {
  const cases = [
    {
      name: "empty gateway",
      input: { providers: [], dailyUsage: [], usageTracked: true },
      want: { provider: false, models: false, request: false },
    },
    {
      name: "provider still starting",
      input: {
        providers: [provider({ discovered_model_count: 0 })],
        dailyUsage: [],
        usageTracked: true,
      },
      want: { provider: true, models: false, request: false },
    },
    {
      name: "provider without a runtime snapshot",
      input: {
        providers: [{ name: "openai" }],
        dailyUsage: [],
        usageTracked: true,
      },
      want: { provider: true, models: false, request: false },
    },
    {
      name: "stale inventory counts as no models, like the provider card",
      input: {
        providers: [
          provider({ discovered_model_count: 12, inventory_stale: true }),
        ],
        dailyUsage: [],
        usageTracked: true,
      },
      want: { provider: true, models: false, request: false },
    },
    {
      name: "one provider with models is enough",
      input: {
        providers: [
          provider({ discovered_model_count: 0 }),
          provider({ discovered_model_count: 3 }),
        ],
        dailyUsage: [{ date: "2026-11-19", requests: 0 }],
        usageTracked: true,
      },
      want: { provider: true, models: true, request: false },
    },
    {
      name: "a recorded request completes the list",
      input: {
        providers: [provider({ discovered_model_count: 3 })],
        dailyUsage: [
          { date: "2026-11-18", requests: 0 },
          { date: "2026-11-19", requests: 1 },
        ],
        usageTracked: true,
      },
      want: { provider: true, models: true, request: true },
    },
    {
      name: "malformed payloads read as nothing done",
      input: { providers: null, dailyUsage: "nope", usageTracked: true },
      want: { provider: false, models: false, request: false },
    },
  ];
  for (const { name, input, want } of cases) {
    assert.deepEqual(doneByStep(input), want, name);
  }
});

test("gettingStartedSteps drops the request step when usage is not tracked", () => {
  const steps = gettingStartedSteps({
    providers: [provider({ discovered_model_count: 3 })],
    dailyUsage: [],
    usageTracked: false,
  });
  assert.deepEqual(
    steps.map((step) => step.id),
    ["provider", "models"],
  );
  assert.deepEqual(gettingStartedProgress(steps), { done: 2, total: 2 });
});

test("gettingStartedSteps links each step to the page that completes it", () => {
  const steps = gettingStartedSteps({
    providers: [],
    dailyUsage: [],
    usageTracked: true,
  });
  assert.deepEqual(
    steps.map((step) => [step.id, step.page]),
    [
      ["provider", "providers-config"],
      ["models", "models"],
      ["request", "playground"],
    ],
  );
});

test("gettingStartedProgress counts finished steps", () => {
  assert.deepEqual(gettingStartedProgress(null), { done: 0, total: 0 });
  assert.deepEqual(
    gettingStartedProgress([{ done: true }, { done: false }, { done: true }]),
    { done: 2, total: 3 },
  );
});

test("gettingStartedVisible shows the checklist only on an unfinished, loaded gateway", () => {
  const open = [{ done: true }, { done: false }];
  const finished = [{ done: true }, { done: true }];
  const base = { loaded: true, dismissed: false, demo: false, steps: open };
  const cases = [
    { name: "open step", input: base, want: true },
    { name: "every step done", input: { ...base, steps: finished }, want: false },
    { name: "data not loaded", input: { ...base, loaded: false }, want: false },
    { name: "dismissed", input: { ...base, dismissed: true }, want: false },
    { name: "demo mode", input: { ...base, demo: true }, want: false },
    { name: "no steps", input: { ...base, steps: [] }, want: false },
  ];
  for (const { name, input, want } of cases) {
    assert.equal(gettingStartedVisible(input), want, name);
  }
});

function createStorageStub() {
  return {
    values: new Map(),
    getItem(key) {
      return this.values.has(key) ? this.values.get(key) : null;
    },
    setItem(key, value) {
      this.values.set(key, String(value));
    },
  };
}

test("the dismissed flag round-trips through storage", () => {
  const storage = createStorageStub();
  assert.equal(loadGettingStartedDismissed(storage), false);
  saveGettingStartedDismissed(storage);
  assert.equal(loadGettingStartedDismissed(storage), true);
  assert.deepEqual(
    [...storage.values.entries()],
    [["gomodel_getting_started_dismissed", "true"]],
  );
});

test("the dismissed flag tolerates missing or failing storage", () => {
  const failing = {
    getItem() {
      throw new Error("blocked");
    },
    setItem() {
      throw new Error("blocked");
    },
  };
  assert.equal(loadGettingStartedDismissed(null), false);
  assert.equal(loadGettingStartedDismissed(failing), false);
  assert.doesNotThrow(() => saveGettingStartedDismissed(null));
  assert.doesNotThrow(() => saveGettingStartedDismissed(failing));
});
