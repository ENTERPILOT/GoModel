// The "auto" load-balancing strategy routes by level: every target names one
// of light < normal < smart < smartest. These pin what the editor does with
// levels — defaults, the save and toggle payloads, loading — and how an auto
// route reads on the models list.

import test from "node:test";
import assert from "node:assert/strict";

import {
  aliasRouting,
  aliasSearchText,
  aliasTargetLabel,
  DEFAULT_ROUTE_LEVEL,
  mapRedirectView,
  maskingRoutingKind,
  maskingRoutingLabel,
  ROUTE_LEVELS,
  routeLevel,
  strategyLabel,
  strategyOptions,
  strategyUsesWeights,
} from "../src/pages/models/routing.js";
import {
  aliasFormTargets,
  applyDefaultLevels,
  buildAliasTogglePayload,
  buildVirtualModelSavePayload,
  defaultVirtualModelForm,
  flattenFormTargets,
  moveFormTarget,
  removePrimaryTarget,
  vmFormMissingTopLevel,
  vmFormShowBalancingOptions,
  vmFormShowLevels,
  vmFormShowWeights,
  vmRoutingSummary,
} from "../src/pages/models/vmForm.js";

// autoForm builds an auto form from [model, level] pairs, primary first.
function autoForm(pairs, overrides = {}) {
  const [[primary, primaryLevel], ...extras] = pairs;
  return {
    ...defaultVirtualModelForm(),
    source: "coding",
    target_model: primary,
    target_level: primaryLevel,
    targets: extras.map(([model, level]) => ({ provider: "", model, weight: 1, reasoning_effort: "", level })),
    strategy: "auto",
    ...overrides,
  };
}

function autoView(targets, overrides = {}) {
  return mapRedirectView({
    source: "coding",
    kind: "redirect",
    targets,
    strategy: "auto",
    enabled: true,
    valid: true,
    ...overrides,
  });
}

test("the levels are ordered lowest first and the default is the highest", () => {
  assert.deepEqual(ROUTE_LEVELS, ["light", "normal", "smart", "smartest"]);
  assert.equal(DEFAULT_ROUTE_LEVEL, ROUTE_LEVELS[ROUTE_LEVELS.length - 1]);
  assert.equal(routeLevel(" Smart "), "smart");
  assert.equal(routeLevel("genius"), "");
  assert.equal(routeLevel(undefined), "");
});

test("the strategy picker offers auto with a label, after the server's order", () => {
  const options = strategyOptions(["round_robin", "cost", "failover", "auto"], "round_robin");
  assert.deepEqual(options.map((option) => option.value), ["round_robin", "cost", "failover", "auto"]);
  assert.equal(
    options[3].label,
    "Auto (route by level: the lowest level that covers what a request needs)",
  );
  assert.equal(strategyLabel("auto"), "Auto");
  assert.equal(strategyLabel("AUTO"), "Auto");
});

test("the level column shows only under auto with several targets; weights and balancing options stay", () => {
  const form = autoForm([["openai/gpt-5", "smartest"], ["groq/llama", "light"]]);
  assert.equal(vmFormShowLevels(form), true);
  // Weights apply inside a level, and auto pins sessions and fails over.
  assert.equal(strategyUsesWeights("auto"), true);
  assert.equal(vmFormShowWeights(form), true);
  assert.equal(vmFormShowBalancingOptions(form), true);

  for (const strategy of ["round_robin", "cost", "failover", "adaptive", "plugin"]) {
    assert.equal(vmFormShowLevels({ ...form, strategy }), false, strategy);
  }
  // One target: the strategy does not apply yet, so there is no level to set.
  assert.equal(vmFormShowLevels(autoForm([["openai/gpt-5", "smartest"]])), false);
});

test("switching to auto gives every row without a level the default, keeping chosen ones", () => {
  const form = {
    ...defaultVirtualModelForm(),
    source: "coding",
    target_model: "openai/gpt-5",
    targets: [
      { provider: "", model: "groq/llama", weight: 1, reasoning_effort: "", level: "light" },
      { provider: "", model: "", weight: 1, reasoning_effort: "" },
      { provider: "", model: "x/y", weight: 1, reasoning_effort: "", level: "genius" },
    ],
  };

  // Under another strategy nothing is filled in.
  applyDefaultLevels(form);
  assert.equal(form.target_level, "");
  assert.equal(form.targets[1].level, undefined);

  form.strategy = "auto";
  applyDefaultLevels(form);
  assert.equal(form.target_level, "smartest");
  assert.deepEqual(form.targets.map((target) => target.level), ["light", "smartest", "smartest"]);

  // Switching away and back keeps what the operator chose.
  form.targets[1].level = "normal";
  form.strategy = "cost";
  applyDefaultLevels(form);
  form.strategy = "auto";
  applyDefaultLevels(form);
  assert.deepEqual(form.targets.map((target) => target.level), ["light", "normal", "smartest"]);
});

test("save payload sends every target's level under auto, keeping weights and order", () => {
  const form = autoForm([["openai/gpt-5", "smartest"], ["anthropic/sonnet", "smart"], ["groq/llama", "light"]]);
  form.targets[0].weight = 3;
  form.targets[1].reasoning_effort = "low";
  const { payload } = buildVirtualModelSavePayload(form, "", "create");

  assert.equal(payload.strategy, "auto");
  assert.deepEqual(payload.targets, [
    { model: "openai/gpt-5", weight: 1, level: "smartest" },
    { model: "anthropic/sonnet", weight: 3, level: "smart" },
    { model: "groq/llama", weight: 1, reasoning_effort: "low", level: "light" },
  ]);
});

test("save payload sends no level under any other strategy, or for a single target", () => {
  const leveled = autoForm([["openai/gpt-5", "smartest"], ["groq/llama", "light"]]);
  for (const strategy of ["round_robin", "cost", "failover", "adaptive"]) {
    const { payload } = buildVirtualModelSavePayload({ ...leveled, strategy }, "", "create");
    assert.equal(payload.strategy, strategy);
    assert.equal(payload.targets.length, 2, strategy);
    for (const target of payload.targets) {
      assert.equal("level" in target, false, strategy);
    }
  }

  // A single target is a plain alias: no strategy applies, so no level goes.
  const single = buildVirtualModelSavePayload(autoForm([["openai/gpt-5", "smart"]]), "", "create").payload;
  assert.equal(single.target_model, "openai/gpt-5");
  assert.equal("targets" in single, false);
  assert.equal("strategy" in single, false);

  // ...also when that target needs the targets list for its pinned provider.
  const pinned = buildVirtualModelSavePayload(
    autoForm([["openai/gpt-5", "smart"]], { target_provider: "openai" }),
    "",
    "create",
  ).payload;
  assert.deepEqual(pinned.targets, [{ provider: "openai", model: "gpt-5" }]);
});

test("save payload leaves a missing level out rather than inventing one", () => {
  // The editor always fills levels (applyDefaultLevels); if a row still has
  // none, the backend's "needs a level" error must surface, not a guess.
  const form = autoForm([["openai/gpt-5", "smartest"], ["groq/llama", ""]]);
  const { payload } = buildVirtualModelSavePayload(form, "", "create");
  assert.deepEqual(payload.targets, [
    { model: "openai/gpt-5", weight: 1, level: "smartest" },
    { model: "groq/llama", weight: 1 },
  ]);
});

test("an auto route loads back into the editor with its levels and saves unchanged", () => {
  const alias = autoView([
    { provider: "openai", model: "gpt-5", level: "smartest" },
    { provider: "groq", model: "llama", weight: 2, level: "light" },
  ]);
  assert.deepEqual(alias.targets, [
    { provider: "openai", model: "gpt-5", level: "smartest" },
    { provider: "groq", model: "llama", weight: 2, level: "light" },
  ]);

  const loaded = aliasFormTargets(alias);
  assert.equal(loaded.primaryLevel, "smartest");
  assert.deepEqual(loaded.extraTargets, [
    { provider: "groq", model: "groq/llama", weight: 2, reasoning_effort: "", level: "light" },
  ]);

  const form = {
    ...defaultVirtualModelForm(),
    source: alias.name,
    target_provider: loaded.primaryProvider,
    target_model: loaded.primaryModel,
    target_weight: loaded.primaryWeight,
    target_level: loaded.primaryLevel,
    targets: loaded.extraTargets,
    strategy: alias.strategy,
  };
  const { payload } = buildVirtualModelSavePayload(form, alias.name, "edit");
  assert.deepEqual(payload.targets, [
    { provider: "openai", model: "gpt-5", weight: 1, level: "smartest" },
    { provider: "groq", model: "llama", weight: 2, level: "light" },
  ]);

  // A route under another strategy loads with no levels.
  const plain = aliasFormTargets(
    mapRedirectView({
      source: "pool",
      kind: "redirect",
      targets: [{ provider: "openai", model: "gpt-5" }, { provider: "groq", model: "llama" }],
      strategy: "round_robin",
    }),
  );
  assert.equal(plain.primaryLevel, "");
  assert.equal(plain.extraTargets[0].level, "");
});

test("levels travel with their target when rows are reordered or the first is removed", () => {
  const form = autoForm([["a", "smartest"], ["b", "smart"], ["c", "light"]]);
  assert.equal(moveFormTarget(form, 2, 0), true);
  assert.deepEqual(
    flattenFormTargets(form).map((target) => [target.model, target.level]),
    [["c", "light"], ["a", "smartest"], ["b", "smart"]],
  );

  removePrimaryTarget(form);
  assert.deepEqual(
    flattenFormTargets(form).map((target) => [target.model, target.level]),
    [["a", "smartest"], ["b", "smart"]],
  );
});

test("toggling an auto route round-trips every level, so the backend accepts it", () => {
  const payload = buildAliasTogglePayload(
    autoView([
      { provider: "openai", model: "gpt-5", level: "smartest" },
      { provider: "groq", model: "llama", weight: 2, level: "light" },
    ]),
  );
  assert.equal(payload.enabled, false);
  assert.equal(payload.strategy, "auto");
  assert.deepEqual(payload.targets, [
    { provider: "openai", model: "gpt-5", level: "smartest" },
    { provider: "groq", model: "llama", weight: 2, level: "light" },
  ]);

  // A stale level on a route under another strategy is not sent back.
  const other = buildAliasTogglePayload(
    autoView(
      [
        { provider: "openai", model: "gpt-5", level: "smartest" },
        { provider: "groq", model: "llama", level: "light" },
      ],
      { strategy: "round_robin" },
    ),
  );
  for (const target of other.targets) {
    assert.equal("level" in target, false);
  }
});

test("the editor warns when an auto route has no smartest target", () => {
  const cases = [
    {
      name: "one target is smartest",
      form: autoForm([["openai/gpt-5", "smartest"], ["groq/llama", "light"]]),
      want: false,
    },
    {
      name: "no target is smartest",
      form: autoForm([["anthropic/sonnet", "smart"], ["groq/llama", "light"]]),
      want: true,
    },
    {
      name: "the only smartest row has no model, so it is not a target",
      form: autoForm([["anthropic/sonnet", "smart"], ["", "smartest"]]),
      want: true,
    },
    {
      name: "the smartest target is an extra row",
      form: autoForm([["groq/llama", "light"], ["openai/gpt-5", "smartest"]]),
      want: false,
    },
    {
      name: "no row has a model yet",
      form: autoForm([["", "smart"], ["", "smart"]]),
      want: false,
    },
    {
      name: "one target: the strategy does not apply",
      form: autoForm([["anthropic/sonnet", "smart"]]),
      want: false,
    },
    {
      name: "another strategy with leftover levels",
      form: autoForm([["anthropic/sonnet", "smart"], ["groq/llama", "light"]], { strategy: "round_robin" }),
      want: false,
    },
  ];
  for (const { name, form, want } of cases) {
    assert.equal(vmFormMissingTopLevel(form), want, name);
  }
});

test("the editor summary says an auto route picks by level, naming each target's", () => {
  const form = autoForm([["openai/gpt-5", "smartest"], ["groq/llama", "light"]]);
  assert.deepEqual(vmRoutingSummary(form, false), {
    text: "Requests for coding are routed by level across openai/gpt-5 (smartest), groq/llama (light).",
    replaces: false,
  });

  // Over a real model that is one of the targets: nothing is shadowed.
  const self = { ...form, source: "openai/gpt-5" };
  assert.deepEqual(vmRoutingSummary(self, true), {
    text: "Requests for openai/gpt-5 are routed by level across openai/gpt-5 (smartest), groq/llama (light).",
    replaces: false,
  });

  // Over a real model that is not a target: it is replaced, and says so.
  const shadowed = vmRoutingSummary({ ...form, source: "openai/gpt-4o" }, true);
  assert.equal(shadowed.replaces, true);
  assert.equal(
    shadowed.text,
    "Requests are routed by level across openai/gpt-5 (smartest), groq/llama (light). Add openai/gpt-4o as a target if you don't want to shadow the source model.",
  );
});

test("the models list names an auto route's strategy and each target's level", () => {
  const alias = autoView([
    { provider: "openai", model: "gpt-5", level: "smartest" },
    { provider: "anthropic", model: "sonnet", level: "smart" },
    { provider: "groq", model: "llama", level: "light" },
  ]);
  assert.deepEqual(aliasRouting(alias), {
    kind: "auto",
    label: "Auto",
    targets: "openai/gpt-5 (smartest), anthropic/sonnet (smart) +1",
    all: "openai/gpt-5 (smartest), anthropic/sonnet (smart), groq/llama (light)",
    // What the row draws as badges: the targets it names, and how many more.
    levels: [
      { name: "openai/gpt-5", level: "smartest" },
      { name: "anthropic/sonnet", level: "smart" },
    ],
    more: 1,
  });
  assert.equal(aliasTargetLabel(alias), "openai/gpt-5 (smartest), anthropic/sonnet (smart) +1 · Auto");
  // The list filter finds the route by a level, also one the row cuts off.
  assert.match(aliasSearchText(alias), /groq\/llama \(light\)/);
});

test("a real model under an auto route lists every target with its level", () => {
  const self = { provider: "openai", model: "gpt-5", level: "smartest" };
  const light = { provider: "groq", model: "llama", level: "light" };
  const over = (targets) => autoView(targets, { source: "openai/gpt-5" });

  // The model is a target: its own level decides what it serves, so it is listed.
  const routed = over([self, light]);
  assert.equal(maskingRoutingKind(routed), "balanced");
  assert.equal(
    maskingRoutingLabel(routed, "balanced"),
    "openai/gpt-5 (smartest), groq/llama (light) · Auto",
  );

  // The model is not a target: requests go elsewhere, by level.
  const elsewhere = over([{ provider: "azure", model: "gpt-5", level: "smart" }, light]);
  assert.equal(maskingRoutingKind(elsewhere), "redirect");
  assert.equal(
    maskingRoutingLabel(elsewhere, "redirect"),
    "azure/gpt-5 (smart), groq/llama (light) · Auto",
  );
});
