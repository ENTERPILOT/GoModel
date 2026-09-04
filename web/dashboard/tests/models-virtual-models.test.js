// Ported pure-logic cases from the legacy
// internal/admin/dashboard/static/js/modules/virtual-models.test.cjs,
// exercising the pure modules under web/dashboard/src/pages/models/.

import test from "node:test";
import assert from "node:assert/strict";

import {
  aliasRowCanRemove,
  buildDisplayModels,
  displayRowClass,
  filterDisplayModels,
  groupDisplayModels,
  rowIsManaged,
  rowRedirectCanRemove,
  rowAnchorID,
} from "../src/pages/models/displayRows.js";
import {
  qualifiedModelName,
} from "../src/pages/models/modelIdentity.js";
import {
  computeRenderStep,
  initialRenderStep,
} from "../src/pages/models/renderBatching.js";
import {
  aliasTargetLabel,
  mapRedirectView,
  maskingFailsOver,
  maskingRoutingKind,
  maskingRoutingLabel,
  splitVirtualModelViews,
  strategyOptions,
} from "../src/pages/models/routing.js";
import {
  aliasFormTargets,
  buildAliasTogglePayload,
  buildModelTogglePayload,
  buildVirtualModelSavePayload,
  defaultVirtualModelForm,
  removePrimaryTarget,
  virtualModelTargetOptions,
  vmFormHasPrimaryTarget,
  vmFormIsRedirect,
  vmFormSelfOnly,
  vmFormShowBalancingOptions,
  vmFormShowWeights,
  vmFormStrategyPending,
  vmFormSupportsSlowdown,
  vmRoutingSummary,
} from "../src/pages/models/vmForm.js";

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
      slowdown: 0.4,
    },
    {
      source: "openai/gpt-4o",
      kind: "policy",
      enabled: false,
      user_paths: ["/team/alpha"],
      managed: true,
      slowdown: 0.2,
    },
    null,
  ]);

  assert.equal(aliases.length, 1);
  assert.equal(aliases[0].name, "smart");
  assert.equal(aliases[0].target_provider, "openai");
  assert.equal(aliases[0].target_model, "gpt-4o");
  assert.deepEqual(aliases[0].user_paths, ["/team/alpha"]);
  assert.equal(aliases[0].slowdown, 0.4);
  assert.equal(policies.length, 1);
  assert.equal(policies[0].selector, "openai/gpt-4o");
  assert.equal(policies[0].enabled, false);
  assert.equal(policies[0].managed, true);
  assert.equal(policies[0].slowdown, 0.2);
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
  assert.equal(aliasTargetLabel(alias), "openai/gpt-4o, groq/llama · Round-robin");
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
  assert.deepEqual(displayRowClass(overrideRow), ["alias-row", "is-valid"]);

  const redirectRow = {
    is_alias: false,
    has_virtual_model: true,
    masking_alias: { name: "openai/gpt-4o" },
    access: { effective_enabled: true },
  };
  assert.deepEqual(displayRowClass(redirectRow), ["alias-row", "is-valid", "masked-model-row"]);
  assert.equal(rowRedirectCanRemove(redirectRow), true);

  const plainRow = { is_alias: false, has_virtual_model: false, access: { effective_enabled: true } };
  assert.deepEqual(displayRowClass(plainRow), []);

  const aliasRow = { is_alias: true, alias: { enabled: true, valid: true } };
  assert.deepEqual(displayRowClass(aliasRow), ["alias-row", "is-valid"]);
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

test("buildModelTogglePayload preserves a configured slowdown instead of deleting the policy", () => {
  const { method, payload } = buildModelTogglePayload(
    "openai/gpt-4o",
    { selector: "openai/gpt-4o", user_paths: [], enabled: false, slowdown: 0.5 },
    { selector: "openai/gpt-4o", default_enabled: true, effective_enabled: false },
  );
  assert.equal(method, "PUT");
  assert.equal(payload.enabled, true);
  assert.equal(payload.slowdown, 0.5);
});

test("buildModelTogglePayload preserves an explicit zero slowdown override", () => {
  const { method, payload } = buildModelTogglePayload(
    "openai/gpt-4o",
    { selector: "openai/gpt-4o", user_paths: [], enabled: false, slowdown: 0 },
    { selector: "openai/gpt-4o", default_enabled: true, effective_enabled: false },
  );
  assert.equal(method, "PUT");
  assert.equal(payload.slowdown, 0);
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

test("buildAliasTogglePayload preserves an alias slowdown", () => {
  const payload = buildAliasTogglePayload({
    name: "slow",
    target_model: "openai/gpt-4o",
    slowdown: 1.5,
    enabled: true,
  });
  assert.equal(payload.slowdown, 1.5);
});

test("buildAliasTogglePayload preserves an explicit zero slowdown", () => {
  const payload = buildAliasTogglePayload({
    name: "fast",
    target_model: "openai/gpt-4o",
    slowdown: 0,
    enabled: true,
  });
  assert.equal(payload.slowdown, 0);
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
  // though a stored target happened to carry a weight. An explicit provider
  // stays explicit, so the toggle never turns a pinned target into a name.
  assert.deepEqual(payload.targets, [
    { provider: "openai", model: "gpt-4o" },
    { provider: "groq", model: "llama" },
  ]);
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
    { provider: "openai", model: "gpt-4o" },
    { provider: "groq", model: "llama", weight: 2 },
  ]);
  assert.equal(payload.strategy, "round_robin");
});

test("buildAliasTogglePayload keeps a by-name target by name", () => {
  const payload = buildAliasTogglePayload({
    name: "outer",
    targets: [{ model: "team/cheap" }, { model: "openai/gpt-4o", weight: 2 }],
    strategy: "round_robin",
    enabled: true,
    user_paths: [],
  });
  assert.deepEqual(payload.targets, [
    { model: "team/cheap" },
    { model: "openai/gpt-4o", weight: 2 },
  ]);
});

test("buildAliasTogglePayload sends a single pinned target through the targets list", () => {
  const payload = buildAliasTogglePayload({
    name: "pinned",
    targets: [{ provider: "openai", model: "gpt-4o" }],
    enabled: true,
    user_paths: [],
  });
  assert.equal(payload.target_model, undefined);
  assert.deepEqual(payload.targets, [{ provider: "openai", model: "gpt-4o" }]);
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

test("save payload distinguishes configured slowdown, explicit zero, and inherited slowdown", () => {
  const configured = buildVirtualModelSavePayload(
    { source: "slow", target_model: "openai/gpt-4o", targets: [], slowdown: 2.5, enabled: true },
    "",
    "create",
  );
  assert.equal(configured.payload.slowdown, 2.5);

  const disabled = buildVirtualModelSavePayload(
    { source: "fast", target_model: "openai/gpt-4o", targets: [], slowdown: "", enabled: true },
    "",
    "create",
  );
  assert.equal(Object.prototype.hasOwnProperty.call(disabled.payload, "slowdown"), false);

  const explicitlyDisabled = buildVirtualModelSavePayload(
    { source: "fast", target_model: "openai/gpt-4o", targets: [], slowdown: 0, enabled: true },
    "",
    "create",
  );
  assert.equal(explicitlyDisabled.payload.slowdown, 0);

  const invalid = buildVirtualModelSavePayload(
    { source: "invalid", target_model: "openai/gpt-4o", targets: [], slowdown: 10.1, enabled: true },
    "",
    "create",
  );
  assert.equal(invalid.payload.slowdown, 10.1);
});

test("slowdown is available for redirects and model policies only", () => {
  assert.equal(vmFormSupportsSlowdown({ source: "slow", target_model: "openai/gpt-4o" }), true);
  assert.equal(vmFormSupportsSlowdown({ source: "openai/gpt-4o", target_model: "" }), true);
  assert.equal(vmFormSupportsSlowdown({ source: "openai/", target_model: "" }), false);
  assert.equal(vmFormSupportsSlowdown({ source: "/", target_model: "" }), false);
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

test("save payload keeps declared order and drops weights for the failover strategy", () => {
  const { payload } = buildVirtualModelSavePayload(
    {
      source: "resilient",
      target_model: "openai/gpt-4o",
      target_weight: 3,
      targets: [{ model: "groq/llama", weight: 1 }],
      strategy: "failover",
      user_paths: "",
      description: "",
      enabled: true,
    },
    "",
    "create",
  );
  assert.deepEqual(payload.targets, [{ model: "openai/gpt-4o" }, { model: "groq/llama" }]);
  assert.equal(payload.strategy, "failover");
});

test("editing a weighted redirect preserves the primary target weight", () => {
  const { primaryProvider, primaryModel, primaryWeight, extraTargets } =
    aliasFormTargets({
      name: "smart",
      strategy: "round_robin",
      enabled: true,
      targets: [
        { provider: "openai", model: "gpt-4o", weight: 3 },
        { provider: "groq", model: "llama", weight: 1 },
      ],
    });
  assert.equal(primaryProvider, "openai");
  assert.equal(primaryModel, "openai/gpt-4o");
  assert.equal(primaryWeight, 3);

  const { payload } = buildVirtualModelSavePayload(
    {
      source: "smart",
      target_provider: primaryProvider,
      target_model: primaryModel,
      target_weight: primaryWeight,
      targets: extraTargets,
      strategy: "round_robin",
      enabled: true,
    },
    "smart",
    "edit",
  );
  // The explicit providers the row was stored with survive the round trip.
  assert.deepEqual(payload.targets, [
    { provider: "openai", model: "gpt-4o", weight: 3 },
    { provider: "groq", model: "llama", weight: 1 },
  ]);
  assert.equal(payload.strategy, "round_robin");
});

test("picking another target in the editor drops the stored provider pin", () => {
  // VmTargetRow clears `provider` on selection; the payload then sends the
  // chosen name as written, so it may reach a virtual model of that name.
  const { payload } = buildVirtualModelSavePayload(
    {
      source: "smart",
      target_provider: "",
      target_model: "team/cheap",
      target_weight: 1,
      targets: [{ provider: "groq", model: "groq/llama", weight: 1 }],
      strategy: "round_robin",
      enabled: true,
    },
    "smart",
    "edit",
  );
  assert.deepEqual(payload.targets, [
    { model: "team/cheap", weight: 1 },
    { provider: "groq", model: "llama", weight: 1 },
  ]);
});

test("a single pinned target is saved through the targets list", () => {
  const { payload } = buildVirtualModelSavePayload(
    {
      source: "pinned",
      target_provider: "openai",
      target_model: "openai/gpt-4o",
      target_weight: 1,
      targets: [],
      enabled: true,
    },
    "pinned",
    "edit",
  );
  assert.equal(payload.target_model, undefined);
  assert.deepEqual(payload.targets, [{ provider: "openai", model: "gpt-4o" }]);
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
  assert.deepEqual(extraTargets, [
    { provider: "openrouter", model: "openrouter/openai/gpt-4o", weight: 1 },
  ]);

  const { payload } = buildVirtualModelSavePayload(
    {
      source: "oss",
      target_provider: "groq",
      target_model: primaryModel,
      target_weight: 1,
      targets: extraTargets,
      strategy: "round_robin",
      enabled: true,
    },
    "oss",
    "edit",
  );
  // Only the provider's own prefix is split back off; the model's slashes stay.
  assert.deepEqual(payload.targets, [
    { provider: "groq", model: "openai/gpt-oss-120b", weight: 1 },
    { provider: "openrouter", model: "openai/gpt-4o", weight: 1 },
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

test("strategy is always shown; weights and balancing options follow targets and strategy", () => {
  // Single target: nothing balances yet, so the strategy carries a hint.
  let form = { target_model: "openai/gpt-4o", targets: [], strategy: "round_robin" };
  assert.equal(vmFormStrategyPending(form), true);
  assert.equal(vmFormShowWeights(form), false);

  // Two targets under round-robin: weights are meaningful.
  form = {
    target_model: "openai/gpt-4o",
    targets: [{ model: "groq/llama", weight: "" }],
    strategy: "round_robin",
  };
  assert.equal(vmFormStrategyPending(form), false);
  assert.equal(vmFormShowWeights(form), true);
  assert.equal(vmFormShowBalancingOptions(form), true);

  // Cost: weights disappear, the balancing options stay.
  form.strategy = "cost";
  assert.equal(vmFormShowWeights(form), false);
  assert.equal(vmFormShowBalancingOptions(form), true);

  // Failover is a priority list: no weights, no session keeping (the primary
  // is always retried first) and no failover opt-out (it always fails over).
  form.strategy = "failover";
  assert.equal(vmFormShowWeights(form), false);
  assert.equal(vmFormShowBalancingOptions(form), false);

  // A blank second row already counts as a second target for the hint.
  const blank = defaultVirtualModelForm();
  blank.targets = [{ model: "", weight: 1 }];
  assert.equal(vmFormStrategyPending(blank), false);
});

test("vmRoutingSummary spells out the effect on the source, warning when a real model is replaced", () => {
  const form = (source, target_model, targets, strategy) => ({
    ...defaultVirtualModelForm(),
    source,
    target_model,
    targets: targets.map((model) => ({ model, weight: 1 })),
    strategy,
  });
  const me = "openai/gpt-4o";

  // Real model kept as its own first target: a safety net, never a warning.
  let s = vmRoutingSummary(form(me, me, ["azure/gpt-4o", "gemini/g"], "failover"), true);
  assert.equal(s.text, "Requests for openai/gpt-4o are served by openai/gpt-4o; if it fails, by azure/gpt-4o → gemini/g.");
  assert.equal(s.replaces, false);
  s = vmRoutingSummary(form(me, me, ["azure/gpt-4o"], "round_robin"), true);
  assert.equal(s.text, "Requests for openai/gpt-4o are balanced across openai/gpt-4o and azure/gpt-4o (Round-robin).");
  assert.equal(s.replaces, false);
  // Only itself: a plain policy, nothing to say.
  assert.equal(vmRoutingSummary(form(me, me, [], "failover"), true).text, "");

  // Real model missing from its own targets: replaced, and flagged.
  s = vmRoutingSummary(form(me, "azure/gpt-4o", [], "failover"), true);
  assert.equal(s.text, "Requests are routed to azure/gpt-4o. Add openai/gpt-4o as a target if you don't want to shadow the source model.");
  assert.equal(s.replaces, true);
  s = vmRoutingSummary(form(me, "azure/gpt-4o", ["gemini/g"], "failover"), true);
  assert.equal(s.text, "Requests are routed to azure/gpt-4o; if it fails, to gemini/g. Add openai/gpt-4o as a target if you don't want to shadow the source model.");
  assert.equal(s.replaces, true);
  s = vmRoutingSummary(form(me, "azure/gpt-4o", ["gemini/g"], "cost"), true);
  assert.equal(s.replaces, true);

  // Named virtual models: plain wording, never a warning.
  s = vmRoutingSummary(form("smart", "openai/gpt-4o", ["groq/llama"], "failover"), false);
  assert.equal(s.text, "Requests for smart go to openai/gpt-4o; if it fails, to groq/llama.");
  assert.equal(s.replaces, false);
  assert.equal(vmRoutingSummary(form("smart", "openai/gpt-4o", [], "round_robin"), false).text, "Requests for smart go to openai/gpt-4o.");
  assert.equal(vmRoutingSummary(form("smart", "", [], "round_robin"), false).text, "");
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

test("strategyOptions serves the deployment's strategies with labels", () => {
  const options = strategyOptions(["round_robin", "cost", "adaptive"], "round_robin");
  assert.deepEqual(
    options.map((option) => option.value),
    ["round_robin", "cost", "adaptive"],
  );
  for (const option of options) {
    assert.notEqual(option.label, option.value, `expected a human label for ${option.value}`);
  }
});

test("strategyOptions keeps the edited value selectable when unsupported", () => {
  const options = strategyOptions(["round_robin", "cost"], "adaptive");
  assert.deepEqual(
    options.map((option) => option.value),
    ["round_robin", "cost", "adaptive"],
  );
});

test("strategyOptions labels unknown strategies with their raw value", () => {
  const options = strategyOptions(["round_robin"], "experimental");
  assert.deepEqual(options[1], { value: "experimental", label: "experimental" });
});

test("failover is on by default and only the opt-out reaches the server", () => {
  const form = defaultVirtualModelForm();
  form.source = "smart";
  form.target_model = "openai/gpt-4o";
  form.targets = [{ model: "groq/llama", weight: 1 }];
  assert.equal(form.failover, true);

  let { payload } = buildVirtualModelSavePayload(form, "", "create");
  assert.equal("failover" in payload, false);

  form.failover = false;
  ({ payload } = buildVirtualModelSavePayload(form, "", "create"));
  assert.equal(payload.failover, false);

  // Toggling an alias keeps the opt-out so it never silently re-enables.
  const toggle = buildAliasTogglePayload({
    name: "smart",
    targets: [
      { provider: "openai", model: "gpt-4o" },
      { provider: "groq", model: "llama" },
    ],
    strategy: "round_robin",
    failover: false,
    enabled: true,
    user_paths: [],
  });
  assert.equal(toggle.failover, false);
});

test("virtualModelTargetOptions lists catalog models and other virtual models", () => {
  const models = [
    { selector: "openai/gpt-4o", provider_name: "openai", model: { id: "gpt-4o" } },
    { selector: "groq/llama", provider_name: "groq", model: { id: "llama" } },
    { selector: "openai/gpt-4o", provider_name: "openai", model: { id: "gpt-4o" } },
  ];
  const aliases = [{ name: "cheap" }, { name: "smart" }, { name: "" }];
  // Editing a real model lists it first as "this model".
  assert.deepEqual(
    virtualModelTargetOptions(models, aliases, "groq/llama").slice(0, 2).map((o) => [o.value, o.description]),
    [
      ["groq/llama", "This model"],
      ["openai/gpt-4o", "openai"],
    ],
  );
  const options = virtualModelTargetOptions(models, aliases, "smart");
  assert.deepEqual(
    options.map((option) => [option.value, option.description]),
    [
      ["openai/gpt-4o", "openai"],
      ["groq/llama", "groq"],
      ["cheap", "Virtual model"],
    ],
    "duplicates collapse, the edited redirect is excluded, blanks are dropped",
  );
  assert.deepEqual(virtualModelTargetOptions(null, null, ""), []);
});

test("a real model pinned as its own only target saves as a policy, with a fallback as a failover redirect", () => {
  const form = defaultVirtualModelForm();
  form.source = "openai/gpt-4o";
  form.target_model = "openai/gpt-4o";
  form.strategy = "failover";
  assert.equal(vmFormSelfOnly(form), true);
  assert.equal(vmFormIsRedirect(form), false);
  let { payload } = buildVirtualModelSavePayload(form, "openai/gpt-4o", "edit");
  assert.equal("targets" in payload, false);
  assert.equal("target_model" in payload, false);

  form.targets = [{ model: "azure/gpt-4o", weight: 1 }];
  assert.equal(vmFormSelfOnly(form), false);
  assert.equal(vmFormIsRedirect(form), true);
  ({ payload } = buildVirtualModelSavePayload(form, "openai/gpt-4o", "edit"));
  assert.deepEqual(payload.targets, [{ model: "openai/gpt-4o" }, { model: "azure/gpt-4o" }]);
  assert.equal(payload.strategy, "failover");
});

test("maskingRoutingKind tells failover, balancing, and true redirects apart", () => {
  const self = { provider: "openai", model: "gpt-4o" };
  const azure = { provider: "azure", model: "gpt-4o" };
  const gemini = { provider: "gemini", model: "gemini-2.5-pro" };
  const over = (targets, extra = {}) => ({ name: "openai/gpt-4o", targets, ...extra });

  // Failover strategy with the model first: a safety net, listed in order.
  const failover = over([self, azure, gemini], { strategy: "failover" });
  assert.equal(maskingRoutingKind(failover), "failover");
  assert.equal(maskingRoutingLabel(failover, "failover"), "azure/gpt-4o → gemini/gemini-2.5-pro");
  assert.equal(maskingFailsOver(failover), true);
  // Failover switched off globally: it only ever serves itself.
  assert.equal(maskingRoutingKind(failover, false), "balanced");
  assert.equal(maskingFailsOver(failover, false), false);
  // The model listed after another one is replaced as the primary.
  assert.equal(maskingRoutingKind(over([azure, self], { strategy: "failover" })), "redirect");

  // Round-robin including the model: balanced; the flag decides retries.
  const balanced = over([self, azure], { strategy: "round_robin" });
  assert.equal(maskingRoutingKind(balanced), "balanced");
  assert.equal(maskingRoutingLabel(balanced, "balanced"), "azure/gpt-4o · Round-robin");
  assert.equal(maskingFailsOver(balanced), true);
  assert.equal(maskingFailsOver(over([self, azure], { strategy: "round_robin", failover: false })), false);

  // Not a target at all: a real redirect, single or balanced.
  assert.equal(maskingRoutingKind(over([azure])), "redirect");
  assert.equal(maskingRoutingLabel(over([azure]), "redirect"), "azure/gpt-4o");
  assert.equal(maskingRoutingKind(over([azure, gemini], { strategy: "cost" })), "redirect");
  // Plain single-target alias fields are honoured too.
  assert.equal(maskingRoutingKind({ name: "openai/gpt-4o", target_provider: "azure", target_model: "gpt-4o" }), "redirect");
});

test("rowAnchorID keeps distinct alias names on distinct DOM ids", () => {
  const id = (name) => rowAnchorID({ is_alias: true, alias: { name } });
  assert.notEqual(id("foo/bar"), id("foo-bar"));
  assert.match(id("team/cheap"), /^alias-row-[A-Za-z0-9%._~-]+$/);
  assert.equal(rowAnchorID({ is_alias: false, alias: { name: "x" } }), "");
});

// ─── Plugin routing strategies ───

import {
  parseStrategyDropdownValue,
  strategyDropdownValue,
  strategyLabel,
} from "../src/pages/models/routing.js";

test("strategyOptions lists plugin:<name> entries labelled by plugin name, keeping case", () => {
  const options = strategyOptions(["round_robin", "plugin:latency-Aware"], "plugin:latency-Aware");
  assert.deepEqual(
    options.map((option) => option.value),
    ["round_robin", "plugin:latency-Aware"],
  );
  assert.equal(options[1].label, "latency-Aware (routing plugin)");

  // An existing plugin VM whose plugin is no longer loaded stays selectable.
  const stale = strategyOptions(["round_robin"], "plugin:gone");
  assert.deepEqual(stale[1], { value: "plugin:gone", label: "gone (routing plugin)" });
});

test("strategy dropdown values map to and from strategy/strategy_plugin", () => {
  assert.equal(strategyDropdownValue("plugin", "latency-aware"), "plugin:latency-aware");
  assert.equal(strategyDropdownValue("cost", ""), "cost");
  assert.deepEqual(parseStrategyDropdownValue("plugin:latency-aware"), {
    strategy: "plugin",
    strategy_plugin: "latency-aware",
  });
  assert.deepEqual(parseStrategyDropdownValue("round_robin"), {
    strategy: "round_robin",
    strategy_plugin: "",
  });
  assert.equal(strategyLabel("plugin", "latency-aware"), "latency-aware");
  assert.equal(strategyLabel("plugin"), "Plugin");
});

test("save payload carries strategy_plugin and strategy_config for a plugin strategy", () => {
  const form = {
    ...defaultVirtualModelForm(),
    source: "smart",
    target_model: "openai/gpt-4o",
    targets: [{ model: "groq/llama", weight: 2 }],
    strategy: "plugin",
    strategy_plugin: "latency-aware",
    strategy_config: { p95_window: "5m" },
  };
  const { payload } = buildVirtualModelSavePayload(form, "", "create");
  assert.equal(payload.strategy, "plugin");
  assert.equal(payload.strategy_plugin, "latency-aware");
  assert.deepEqual(payload.strategy_config, { p95_window: "5m" });
  // Plugins decide what weights mean, so they are kept.
  assert.deepEqual(payload.targets, [
    { model: "openai/gpt-4o", weight: 1 },
    { model: "groq/llama", weight: 2 },
  ]);

  // An empty config is omitted; a non-plugin strategy sends no plugin fields.
  const bare = buildVirtualModelSavePayload(
    { ...form, strategy_config: {} },
    "",
    "create",
  ).payload;
  assert.equal("strategy_config" in bare, false);
  const cost = buildVirtualModelSavePayload({ ...form, strategy: "cost" }, "", "create").payload;
  assert.equal("strategy_plugin" in cost, false);
});

test("redirect views and toggle payloads round-trip plugin strategy fields", () => {
  const alias = mapRedirectView({
    source: "smart",
    kind: "redirect",
    targets: [
      { provider: "openai", model: "gpt-4o" },
      { provider: "groq", model: "llama" },
    ],
    strategy: "plugin",
    strategy_plugin: "latency-aware",
    strategy_config: { p95_window: "5m" },
    enabled: true,
    valid: true,
  });
  assert.equal(alias.strategy_plugin, "latency-aware");
  assert.deepEqual(alias.strategy_config, { p95_window: "5m" });
  assert.equal(aliasTargetLabel(alias), "openai/gpt-4o, groq/llama · latency-aware");

  const payload = buildAliasTogglePayload(alias);
  assert.equal(payload.strategy, "plugin");
  assert.equal(payload.strategy_plugin, "latency-aware");
  assert.deepEqual(payload.strategy_config, { p95_window: "5m" });

  const plain = mapRedirectView({ source: "x", kind: "redirect", targets: [], strategy: "cost" });
  assert.equal(plain.strategy_plugin, "");
  assert.deepEqual(plain.strategy_config, {});
});
