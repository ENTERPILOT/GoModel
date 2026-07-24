// Ported pure-logic cases from the legacy
// internal/admin/dashboard/static/js/modules/virtual-models.test.cjs,
// exercising web/dashboard/src/pages/models/virtualModelsLogic.js.

import test from "node:test";
import assert from "node:assert/strict";

import {
  aliasFormTargets,
  aliasRowCanRemove,
  aliasTargetLabel,
  buildAliasTogglePayload,
  buildDisplayModels,
  buildModelTogglePayload,
  buildVirtualModelSavePayload,
  computeRenderStep,
  defaultVirtualModelForm,
  displayRowClass,
  filterDisplayModels,
  groupDisplayModels,
  initialRenderStep,
  mapRedirectView,
  qualifiedModelName,
  removePrimaryTarget,
  rowIsManaged,
  rowRedirectCanRemove,
  splitVirtualModelViews,
  vmFormHasPrimaryTarget,
  vmFormIsRedirect,
  vmFormShowStrategy,
  vmFormShowWeights,
} from "../src/pages/models/virtualModelsLogic.js";

function display(models, aliases, { available = true, activeCategory = "" } = {}) {
  return buildDisplayModels({
    models,
    aliases,
    virtualModelsAvailable: available,
    activeCategory,
  });
}

test("filterDisplayModels returns stable rows when the filter is empty", () => {
  const rows = display(
    [
      {
        provider_type: "openai",
        model: {
          id: "davinci-002",
          object: "model",
          owned_by: "openai",
          metadata: { modes: ["chat"], categories: ["text_generation"] },
        },
      },
    ],
    [],
  );

  const first = filterDisplayModels(rows, "");
  const second = filterDisplayModels(rows, "");

  assert.equal(first.length, 1);
  assert.strictEqual(second, first);
  assert.strictEqual(second[0], first[0]);
  assert.equal(first[0].key, "model:openai/davinci-002");
});

test("qualifiedModelName prefers selector when available", () => {
  assert.equal(
    qualifiedModelName({
      selector: "openrouter/openai/gpt-3.5-turbo",
      provider_name: "openrouter",
      provider_type: "openrouter",
      model: { id: "openai/gpt-3.5-turbo", object: "model", owned_by: "openai" },
    }),
    "openrouter/openai/gpt-3.5-turbo",
  );
});

test("splitVirtualModelViews parses redirect and policy Views", () => {
  const { aliases, policies } = splitVirtualModelViews([
    {
      source: "smart",
      kind: "redirect",
      targets: [{ provider: "openai", model: "gpt-4o" }],
      enabled: true,
      valid: true,
      user_paths: ["/team/alpha"],
    },
    {
      source: "openai/gpt-4o",
      kind: "policy",
      enabled: false,
      user_paths: ["/team/alpha"],
      managed: true,
    },
    null,
  ]);

  assert.equal(aliases.length, 1);
  assert.equal(aliases[0].name, "smart");
  assert.equal(aliases[0].target_provider, "openai");
  assert.equal(aliases[0].target_model, "gpt-4o");
  assert.deepEqual(aliases[0].user_paths, ["/team/alpha"]);
  assert.equal(policies.length, 1);
  assert.equal(policies[0].selector, "openai/gpt-4o");
  assert.equal(policies[0].enabled, false);
  assert.equal(policies[0].managed, true);
});

test("mapRedirectView keeps load-balanced targets, strategy, and labels them", () => {
  const alias = mapRedirectView({
    source: "smart",
    kind: "redirect",
    targets: [
      { provider: "openai", model: "gpt-4o" },
      { provider: "groq", model: "llama", weight: 2 },
    ],
    strategy: "round_robin",
    enabled: true,
    valid: true,
  });

  assert.equal(alias.targets.length, 2);
  assert.equal(alias.targets[1].weight, 2);
  assert.equal(alias.strategy, "round_robin");
  assert.equal(aliasTargetLabel(alias), "2 targets · round robin");
});

test("buildDisplayModels combines source-backed redirects with the concrete model row", () => {
  const models = [
    {
      provider_name: "openai",
      provider_type: "openai",
      selector: "openai/gpt-4o",
      model: { id: "gpt-4o", object: "model" },
    },
    {
      provider_name: "openrouter",
      provider_type: "openrouter",
      selector: "openrouter/anthropic/claude-fable-5",
      model: { id: "anthropic/claude-fable-5", object: "model" },
    },
  ];
  const aliases = [
    { name: "smart", target_provider: "openai", target_model: "gpt-4o", enabled: true, valid: true },
    { name: "gpt-4o", target_provider: "openai", target_model: "gpt-4o", enabled: true, valid: true },
    {
      name: "anthropic/claude-fable-5",
      target_provider: "openrouter",
      target_model: "anthropic/claude-fable-5",
      resolved_model: "openrouter/anthropic/claude-fable-5",
      enabled: true,
      valid: true,
    },
  ];
  const rows = display(models, aliases);

  const aliasOnly = rows.find((row) => row.key === "alias:smart");
  const modelBackedAliasRow = rows.find((row) => row.key === "alias:gpt-4o");
  const modelBacked = rows.find((row) => row.key === "model:openai/gpt-4o");
  const nestedTargetAlias = rows.find((row) => row.key === "alias:anthropic/claude-fable-5");
  const nestedTargetModel = rows.find(
    (row) => row.key === "model:openrouter/anthropic/claude-fable-5",
  );

  assert.equal(aliasOnly.source_model_exists, false);
  assert.equal(aliasRowCanRemove(aliasOnly), true);
  assert.equal(modelBackedAliasRow, undefined);
  assert.equal(modelBacked.masking_alias.name, "gpt-4o");
  assert.equal(rowRedirectCanRemove(modelBacked), true);
  assert.equal(nestedTargetAlias, undefined);
  assert.equal(nestedTargetModel.masking_alias.name, "anthropic/claude-fable-5");
  assert.equal(rowRedirectCanRemove(nestedTargetModel), true);
});

test("buildDisplayModels flags model rows with a policy override as carrying a virtual model", () => {
  const rows = display(
    [
      {
        provider_name: "openai",
        provider_type: "openai",
        access: {
          selector: "openai/gpt-4o",
          default_enabled: true,
          effective_enabled: true,
          override: { selector: "openai/gpt-4o" },
        },
        model: { id: "gpt-4o", object: "model" },
      },
      {
        provider_name: "openai",
        provider_type: "openai",
        access: { selector: "openai/gpt-4o-mini", default_enabled: true, effective_enabled: true },
        model: { id: "gpt-4o-mini", object: "model" },
      },
    ],
    [],
  );

  const withOverride = rows.find((row) => row.display_name === "openai/gpt-4o");
  const without = rows.find((row) => row.display_name === "openai/gpt-4o-mini");

  assert.equal(withOverride.has_virtual_model, true);
  assert.equal(without.has_virtual_model, false);
});

test("displayRowClass renders real models carrying a virtual model as alias-like", () => {
  const overrideRow = { is_alias: false, has_virtual_model: true, access: { effective_enabled: true } };
  assert.equal(displayRowClass(overrideRow), "alias-row is-valid");

  const redirectRow = {
    is_alias: false,
    has_virtual_model: true,
    masking_alias: { name: "openai/gpt-4o" },
    access: { effective_enabled: true },
  };
  assert.equal(displayRowClass(redirectRow), "alias-row is-valid masked-model-row");
  assert.equal(rowRedirectCanRemove(redirectRow), true);

  const plainRow = { is_alias: false, has_virtual_model: false, access: { effective_enabled: true } };
  assert.equal(displayRowClass(plainRow), "");

  const aliasRow = { is_alias: true, alias: { enabled: true, valid: true } };
  assert.equal(displayRowClass(aliasRow), "alias-row is-valid");
  assert.equal(
    aliasRowCanRemove({ is_alias: true, source_model_exists: true, alias: { name: "openai/gpt-4o" } }),
    true,
  );
});

test("groupDisplayModels groups rows by provider_name and applies provider-wide overrides", () => {
  const models = [
    {
      provider_name: "openai-backup",
      provider_type: "openai",
      access: { selector: "openai-backup/gpt-4.1-mini", default_enabled: true, effective_enabled: true },
      model: { id: "gpt-4.1-mini", object: "model", owned_by: "openai" },
    },
    {
      provider_name: "openai-primary",
      provider_type: "openai",
      access: { selector: "openai-primary/gpt-4.1", default_enabled: false, effective_enabled: true },
      model: { id: "gpt-4.1", object: "model", owned_by: "openai" },
    },
  ];
  const overrides = [
    { selector: "openai-backup/", provider_name: "openai-backup", user_paths: ["/non-existing"] },
    { selector: "openai-primary/", provider_name: "openai-primary", user_paths: ["/team/alpha"] },
  ];
  const groups = groupDisplayModels(display(models, []), models, overrides);
  const primary = groups.find((group) => group.provider_name === "openai-primary");
  const backup = groups.find((group) => group.provider_name === "openai-backup");

  assert.equal(groups.length, 2);
  assert.equal(primary.type_label, "openai");
  assert.equal(primary.access.selector, "openai-primary/");
  assert.equal(primary.access.default_enabled, false);
  assert.equal(primary.access.effective_enabled, true);
  assert.deepEqual(Array.from(primary.access.user_paths), ["/team/alpha"]);
  assert.equal(primary.item_count_label, "1 model");
  assert.equal(backup.access.selector, "openai-backup/");
  assert.equal(backup.access.effective_enabled, true);
  assert.deepEqual(Array.from(backup.access.user_paths), ["/non-existing"]);
});

test("groupDisplayModels keeps alias-only virtual models in a first group", () => {
  const models = [
    { provider_name: "alpha", provider_type: "test", selector: "alpha/model-a", model: { id: "model-a", object: "model" } },
    { provider_name: "zulu", provider_type: "test", selector: "zulu/model-z", model: { id: "model-z", object: "model" } },
  ];
  const aliases = [
    { name: "smart", target_provider: "zulu", target_model: "model-z", enabled: true, valid: true },
    { name: "model-a", target_provider: "zulu", target_model: "model-z", enabled: true, valid: true },
  ];
  const rows = display(models, aliases, { activeCategory: "all" });
  const groups = groupDisplayModels(rows, models, []);

  assert.equal(groups[0].key, "virtual-model-group");
  assert.equal(groups[0].display_name, "Virtual models");
  assert.deepEqual(Array.from(groups[0].rows, (row) => row.display_name), ["smart"]);
  assert.equal(groups[0].provider_name, "");
  assert.equal(groups[0].access.selector, "");
  assert.deepEqual(Array.from(groups, (group) => group.display_name), ["Virtual models", "alpha", "zulu"]);

  const overriddenModel = groups[1].rows.find((row) => row.display_name === "alpha/model-a");
  assert.equal(overriddenModel.masking_alias.name, "model-a");
});

test("groupDisplayModels lets provider-wide overrides replace global paths", () => {
  const models = [
    {
      provider_name: "anthropic-primary",
      provider_type: "anthropic",
      access: {
        selector: "anthropic-primary/claude-3-7-sonnet",
        default_enabled: false,
        effective_enabled: false,
      },
      model: { id: "claude-3-7-sonnet", object: "model", owned_by: "anthropic" },
    },
    {
      provider_name: "openai-primary",
      provider_type: "openai",
      access: { selector: "openai-primary/gpt-4.1", default_enabled: false, effective_enabled: false },
      model: { id: "gpt-4.1", object: "model", owned_by: "openai" },
    },
  ];
  const overrides = [
    { selector: "/", user_paths: ["/team/alpha"] },
    { selector: "openai-primary/", provider_name: "openai-primary", user_paths: ["/team/openai"] },
  ];
  const groups = groupDisplayModels(display(models, []), models, overrides);
  const anthropic = groups.find((group) => group.provider_name === "anthropic-primary");
  const openai = groups.find((group) => group.provider_name === "openai-primary");

  assert.equal(anthropic.access.effective_enabled, true);
  assert.deepEqual(Array.from(anthropic.access.user_paths), ["/team/alpha"]);
  assert.equal(openai.access.effective_enabled, true);
  assert.deepEqual(Array.from(openai.access.user_paths), ["/team/openai"]);
});

test("buildModelTogglePayload disabling a real model sends a policy PUT with enabled false", () => {
  const { method, payload } = buildModelTogglePayload(
    "openai/gpt-4o",
    null,
    { selector: "openai/gpt-4o", default_enabled: true, effective_enabled: true },
  );
  assert.equal(method, "PUT");
  assert.deepEqual(payload, { source: "openai/gpt-4o", enabled: false, user_paths: [] });
});

test("buildModelTogglePayload enabling a model with an existing path-less policy sends DELETE", () => {
  const { method, payload } = buildModelTogglePayload(
    "openai/gpt-4o",
    { selector: "openai/gpt-4o", user_paths: [], enabled: false },
    { selector: "openai/gpt-4o", default_enabled: true, effective_enabled: false },
  );
  assert.equal(method, "DELETE");
  assert.deepEqual(payload, { source: "openai/gpt-4o" });
});

test("buildModelTogglePayload enabling a model with restricted paths sends PUT enabled true", () => {
  const { method, payload } = buildModelTogglePayload(
    "openai/gpt-4o",
    { selector: "openai/gpt-4o", user_paths: ["/team/alpha"], enabled: false },
    { selector: "openai/gpt-4o", default_enabled: true, effective_enabled: false },
  );
  assert.equal(method, "PUT");
  assert.deepEqual(payload, { source: "openai/gpt-4o", enabled: true, user_paths: ["/team/alpha"] });
});

test("buildAliasTogglePayload flips an alias enabled flag preserving the target", () => {
  const payload = buildAliasTogglePayload({
    name: "smart",
    target_provider: "openai",
    target_model: "gpt-4o",
    description: "chat",
    enabled: true,
    user_paths: ["/team/alpha"],
  });
  assert.deepEqual(payload, {
    source: "smart",
    target_model: "openai/gpt-4o",
    description: "chat",
    user_paths: ["/team/alpha"],
    enabled: false,
  });
});

test("buildAliasTogglePayload round-trips every target and strategy for a cost alias", () => {
  const payload = buildAliasTogglePayload({
    name: "smart",
    targets: [
      { provider: "openai", model: "gpt-4o" },
      { provider: "groq", model: "llama", weight: 2 },
    ],
    strategy: "cost",
    enabled: true,
    user_paths: [],
  });
  // Cost balancers persist weight-less targets, matching the save path, even
  // though a stored target happened to carry a weight.
  assert.deepEqual(payload.targets, [{ model: "openai/gpt-4o" }, { model: "groq/llama" }]);
  assert.equal(payload.strategy, "cost");
  assert.equal(payload.enabled, false);
});

test("buildAliasTogglePayload keeps per-target weights for a round-robin alias", () => {
  const payload = buildAliasTogglePayload({
    name: "smart",
    targets: [
      { provider: "openai", model: "gpt-4o" },
      { provider: "groq", model: "llama", weight: 2 },
    ],
    strategy: "round_robin",
    enabled: true,
    user_paths: [],
  });
  assert.deepEqual(payload.targets, [
    { model: "openai/gpt-4o" },
    { model: "groq/llama", weight: 2 },
  ]);
  assert.equal(payload.strategy, "round_robin");
});

test("save payload sends a redirect body when target_model is filled", () => {
  const { payload, isRedirect } = buildVirtualModelSavePayload(
    {
      source: "smart",
      target_model: "openai/gpt-4o",
      target_weight: 1,
      targets: [],
      strategy: "round_robin",
      user_paths: "/\n/team/alpha",
      description: " chat ",
      enabled: true,
    },
    "",
    "create",
  );
  assert.equal(isRedirect, true);
  assert.deepEqual(payload, {
    source: "smart",
    user_paths: ["/", "/team/alpha"],
    description: "chat",
    enabled: true,
    target_model: "openai/gpt-4o",
  });
});

test("save payload sends a policy body when target_model is empty", () => {
  const { payload, isRedirect } = buildVirtualModelSavePayload(
    {
      source: "openai/gpt-4o",
      target_model: "",
      target_weight: "",
      targets: [],
      strategy: "round_robin",
      user_paths: "/team/alpha",
      description: "",
      enabled: false,
    },
    "openai/gpt-4o",
    "edit",
  );
  assert.equal(isRedirect, false);
  assert.deepEqual(payload, {
    source: "openai/gpt-4o",
    user_paths: ["/team/alpha"],
    description: "",
    enabled: false,
  });
});

test("save payload carries old_source only when an edit renames the Source", () => {
  const renamed = buildVirtualModelSavePayload(
    { source: "smarter", target_model: "openai/gpt-4o", targets: [], enabled: true },
    "smart",
    "edit",
  );
  assert.equal(renamed.isRename, true);
  assert.equal(renamed.payload.old_source, "smart");

  const kept = buildVirtualModelSavePayload(
    { source: "smart", target_model: "openai/gpt-4o", targets: [], enabled: true },
    "smart",
    "edit",
  );
  assert.equal(kept.isRename, false);
  assert.equal(Object.prototype.hasOwnProperty.call(kept.payload, "old_source"), false);

  // Case-only renames are renames too: sources are case-sensitive backend keys.
  const caseOnly = buildVirtualModelSavePayload(
    { source: "smart", target_model: "openai/gpt-4o", targets: [], enabled: true },
    "Smart",
    "edit",
  );
  assert.equal(caseOnly.isRename, true);
  assert.equal(caseOnly.payload.old_source, "Smart");
});

test("save payload sends targets and strategy for load balancing", () => {
  const { payload } = buildVirtualModelSavePayload(
    {
      source: "smart",
      target_model: "openai/gpt-4o",
      targets: [{ model: "groq/llama", weight: 2 }],
      strategy: "round_robin",
      user_paths: "",
      description: "",
      enabled: true,
    },
    "",
    "create",
  );
  assert.equal(Object.prototype.hasOwnProperty.call(payload, "target_model"), false);
  assert.deepEqual(payload.targets, [{ model: "openai/gpt-4o" }, { model: "groq/llama", weight: 2 }]);
  assert.equal(payload.strategy, "round_robin");
});

test("save payload drops per-target weights for the cost strategy", () => {
  const { payload } = buildVirtualModelSavePayload(
    {
      source: "smart",
      target_model: "openai/gpt-4o",
      target_weight: 1,
      targets: [{ model: "groq/llama", weight: 2 }],
      strategy: "cost",
      user_paths: "",
      description: "",
      enabled: true,
    },
    "",
    "create",
  );
  // Cost routes to the cheapest target, so weights carry no meaning and are
  // stripped rather than persisted as dead data.
  assert.deepEqual(payload.targets, [{ model: "openai/gpt-4o" }, { model: "groq/llama" }]);
  assert.equal(payload.strategy, "cost");
});

test("editing a weighted redirect preserves the primary target weight", () => {
  const { primaryModel, primaryWeight, extraTargets } = aliasFormTargets({
    name: "smart",
    strategy: "round_robin",
    enabled: true,
    targets: [
      { provider: "openai", model: "gpt-4o", weight: 3 },
      { provider: "groq", model: "llama", weight: 1 },
    ],
  });
  assert.equal(primaryModel, "openai/gpt-4o");
  assert.equal(primaryWeight, 3);

  const { payload } = buildVirtualModelSavePayload(
    {
      source: "smart",
      target_model: primaryModel,
      target_weight: primaryWeight,
      targets: extraTargets,
      strategy: "round_robin",
      enabled: true,
    },
    "smart",
    "edit",
  );
  assert.deepEqual(payload.targets, [
    { model: "openai/gpt-4o", weight: 3 },
    { model: "groq/llama", weight: 1 },
  ]);
  assert.equal(payload.strategy, "round_robin");
});

test("editing a redirect preserves a provider prefix on a multi-slash model name", () => {
  // The provider is "groq" and the model name itself contains slashes. The
  // editor must rejoin them, not drop the provider because the model has a "/".
  const { primaryModel, extraTargets } = aliasFormTargets({
    name: "oss",
    strategy: "round_robin",
    enabled: true,
    targets: [
      { provider: "groq", model: "openai/gpt-oss-120b" },
      { provider: "openrouter", model: "openai/gpt-4o" },
    ],
  });
  assert.equal(primaryModel, "groq/openai/gpt-oss-120b");
  // Stored targets without an explicit weight surface the neutral default of 1.
  assert.deepEqual(extraTargets, [{ model: "openrouter/openai/gpt-4o", weight: 1 }]);

  const { payload } = buildVirtualModelSavePayload(
    {
      source: "oss",
      target_model: primaryModel,
      target_weight: 1,
      targets: extraTargets,
      strategy: "round_robin",
      enabled: true,
    },
    "oss",
    "edit",
  );
  assert.deepEqual(payload.targets, [
    { model: "groq/openai/gpt-oss-120b", weight: 1 },
    { model: "openrouter/openai/gpt-4o", weight: 1 },
  ]);
});

test("vmFormHasPrimaryTarget hides the primary remove button until a model is set", () => {
  const form = defaultVirtualModelForm();
  form.target_model = "";
  assert.equal(vmFormHasPrimaryTarget(form), false);
  form.target_model = "   ";
  assert.equal(vmFormHasPrimaryTarget(form), false);
  form.target_model = "openai/gpt-4o";
  assert.equal(vmFormHasPrimaryTarget(form), true);
});

test("removePrimaryTarget promotes the next target into the primary slot", () => {
  const form = defaultVirtualModelForm();
  form.target_model = "openai/gpt-4o";
  form.target_weight = 3;
  form.targets = [
    { model: "groq/llama", weight: 2 },
    { model: "anthropic/claude-fable-5", weight: 1 },
  ];

  removePrimaryTarget(form);

  // The first additional target moves up; the rest shift down by one.
  assert.equal(form.target_model, "groq/llama");
  assert.equal(form.target_weight, 2);
  assert.deepEqual(form.targets, [{ model: "anthropic/claude-fable-5", weight: 1 }]);
});

test("removePrimaryTarget clears the primary when it is the only target", () => {
  const form = defaultVirtualModelForm();
  form.target_model = "openai/gpt-4o";
  form.target_weight = 5;
  form.targets = [];

  removePrimaryTarget(form);

  // No targets left: the redirect collapses toward an access policy.
  assert.equal(form.target_model, "");
  assert.equal(form.target_weight, 1);
  assert.equal(vmFormIsRedirect(form), false);
});

test("vmFormShowWeights hides weight inputs unless round-robin balances 2+ targets", () => {
  // Single target: no balancing, no weights.
  let form = { target_model: "openai/gpt-4o", targets: [], strategy: "round_robin" };
  assert.equal(vmFormShowStrategy(form), false);
  assert.equal(vmFormShowWeights(form), false);

  // Two targets under round-robin: weights are meaningful.
  form = {
    target_model: "openai/gpt-4o",
    targets: [{ model: "groq/llama", weight: "" }],
    strategy: "round_robin",
  };
  assert.equal(vmFormShowStrategy(form), true);
  assert.equal(vmFormShowWeights(form), true);

  // Same targets under cost: the strategy selector stays, weights disappear.
  form.strategy = "cost";
  assert.equal(vmFormShowStrategy(form), true);
  assert.equal(vmFormShowWeights(form), false);

  // A second target row appears in the strategy check even when still blank.
  const blank = defaultVirtualModelForm();
  blank.targets = [{ model: "", weight: 1 }];
  assert.equal(vmFormShowStrategy(blank), true);
});

test("managed rows are read-only: rowIsManaged and can-remove predicates", () => {
  assert.equal(rowIsManaged({ is_alias: true, alias: { name: "smart", managed: true } }), true);
  assert.equal(aliasRowCanRemove({ is_alias: true, alias: { name: "smart", managed: true } }), false);
  assert.equal(
    rowIsManaged({ is_alias: false, masking_alias: { name: "gpt-4o", managed: true } }),
    true,
  );
  assert.equal(
    rowRedirectCanRemove({ is_alias: false, masking_alias: { name: "gpt-4o", managed: true } }),
    false,
  );
  assert.equal(
    rowIsManaged({
      is_alias: false,
      access: { override: { selector: "openai/gpt-4o", managed: true } },
    }),
    true,
  );
  assert.equal(rowIsManaged({ is_alias: false, access: { override: { managed: false } } }), false);
});

test("render batching advances in paint-separated steps (50 / 100 / 130)", () => {
  const total = 130;
  let step = initialRenderStep(50, total);
  assert.deepEqual(step, { limit: 50, rendering: true });

  step = computeRenderStep(step.limit, 50, total);
  assert.deepEqual(step, { limit: 100, rendering: true });

  step = computeRenderStep(step.limit, 50, total);
  assert.deepEqual(step, { limit: 130, rendering: false });

  // A catalog smaller than one batch renders fully with no follow-up batch.
  assert.deepEqual(initialRenderStep(75, 10), { limit: 10, rendering: false });
});
