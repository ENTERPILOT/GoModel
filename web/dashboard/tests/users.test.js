import test from "node:test";
import assert from "node:assert/strict";

import {
  buildUpsertUserPayload,
  countInactiveUserNodes,
  defaultUserForm,
  filterUserNodes,
  parentEffectiveModels,
  previewEffectiveModels,
  selectorMatchesModel,
  sortUserNodes,
  userNodeInactive,
  userNodeKind,
  userNodeRestricted,
  userPathDepth,
  userPathLeaf,
  userPathValidationError,
  userSelectorOptions,
} from "../src/pages/users/usersLogic.js";
import {
  displayModelSelector,
  formatModelSelectors,
  parseModelSelectors,
} from "../src/lib/utils/modelSelectors.js";

function node(overrides) {
  return {
    user_path: "/acme",
    allowed_models: [],
    inherited_from: [],
    configured: false,
    key_count: 0,
    active_key_count: 0,
    ...overrides,
  };
}

test("parseModelSelectors accepts an array of entries", () => {
  assert.deepEqual(parseModelSelectors(["anthropic/*", " gpt-4o, anthropic/*"]), ["anthropic/*", "gpt-4o"]);
});

test("parseModelSelectors splits on commas and newlines, trims, and de-duplicates", () => {
  assert.deepEqual(
    parseModelSelectors(" anthropic/* ,openai/gpt-4o\n\n gpt-4o, anthropic/*"),
    ["anthropic/*", "openai/gpt-4o", "gpt-4o"],
  );
  assert.deepEqual(parseModelSelectors(""), []);
  assert.deepEqual(parseModelSelectors(null), []);
});

test("displayModelSelector renders canonical wildcards the way operators type them", () => {
  assert.equal(displayModelSelector("/"), "*");
  assert.equal(displayModelSelector("anthropic/"), "anthropic/*");
  assert.equal(displayModelSelector("openai/gpt-4o"), "openai/gpt-4o");
  assert.equal(formatModelSelectors(["anthropic/", "gpt-4o"]), "anthropic/*\ngpt-4o");
  assert.equal(formatModelSelectors(undefined), "");
});

test("userPathDepth and userPathLeaf describe the tree position", () => {
  assert.equal(userPathDepth("/"), 0);
  assert.equal(userPathDepth("/acme"), 1);
  assert.equal(userPathDepth("/acme/eng/alice"), 3);
  assert.equal(userPathLeaf("/"), "/");
  assert.equal(userPathLeaf("/acme/eng/alice"), "alice");
});

test("userNodeKind treats nodes with keys as users and containers as groups", () => {
  const nodes = [
    node({ user_path: "/acme" }),
    node({ user_path: "/acme/eng" }),
    node({ user_path: "/acme/eng/alice", key_count: 2 }),
    node({ user_path: "/acme/ops", configured: true }),
  ];
  assert.equal(userNodeKind(nodes[0], nodes), "group");
  assert.equal(userNodeKind(nodes[1], nodes), "group");
  assert.equal(userNodeKind(nodes[2], nodes), "user");
  // A configured leaf without keys or children reads as a user.
  assert.equal(userNodeKind(nodes[3], nodes), "user");
});

test("userNodeRestricted is true for own or inherited allowlists only", () => {
  assert.equal(userNodeRestricted(node()), false);
  assert.equal(userNodeRestricted(node({ allowed_models: ["anthropic/"] })), true);
  assert.equal(userNodeRestricted(node({ inherited_from: ["/acme"] })), true);
});

test("filterUserNodes matches path, description, and selectors; sortUserNodes orders by path", () => {
  const nodes = [
    node({ user_path: "/zeta" }),
    node({ user_path: "/acme/eng", allowed_models: ["anthropic/"], description: "Engineering" }),
    node({ user_path: "/acme" }),
  ];
  assert.deepEqual(
    sortUserNodes(nodes).map((n) => n.user_path),
    ["/acme", "/acme/eng", "/zeta"],
  );
  assert.equal(filterUserNodes(nodes, "engineer").length, 1);
  assert.equal(filterUserNodes(nodes, "anthropic").length, 1);
  assert.equal(filterUserNodes(nodes, "acme").length, 2);
  assert.equal(filterUserNodes(nodes, "").length, 3);
});

test("userNodeInactive is true only when a node has keys and none are active", () => {
  assert.equal(userNodeInactive(node({ key_count: 2, active_key_count: 1 })), false);
  assert.equal(userNodeInactive(node({ key_count: 2, active_key_count: 0 })), true);
  assert.equal(userNodeInactive(node({ key_count: 1 })), true);
  // Zero-key nodes are groups or configured policies and never hide.
  assert.equal(userNodeInactive(node({ key_count: 0 })), false);
  assert.equal(userNodeInactive(null), false);
});

test("filterUserNodes hides all-inactive-key users unless showInactive; groups stay visible", () => {
  const nodes = [
    node({ user_path: "/friends", key_count: 0 }),
    node({ user_path: "/friends/tom", key_count: 1, active_key_count: 0 }),
    node({ user_path: "/friends/anna", key_count: 1, active_key_count: 1 }),
    node({ user_path: "/policies", configured: true }),
  ];
  assert.deepEqual(
    filterUserNodes(nodes, "").map((n) => n.user_path),
    ["/friends", "/friends/anna", "/policies"],
  );
  assert.deepEqual(
    filterUserNodes(nodes, "", { showInactive: true }).map((n) => n.user_path),
    ["/friends", "/friends/tom", "/friends/anna", "/policies"],
  );
  assert.deepEqual(
    filterUserNodes(nodes, "tom", { showInactive: true }).map((n) => n.user_path),
    ["/friends/tom"],
  );
});

test("countInactiveUserNodes counts the rows the default view hides", () => {
  const nodes = [
    node({ user_path: "/friends" }),
    node({ user_path: "/friends/tom", key_count: 1, active_key_count: 0 }),
    node({ user_path: "/friends/anna", key_count: 2, active_key_count: 0 }),
    node({ user_path: "/friends/bob", key_count: 1, active_key_count: 1 }),
  ];
  assert.equal(countInactiveUserNodes(nodes), 2);
  assert.equal(countInactiveUserNodes([]), 0);
  assert.equal(countInactiveUserNodes(undefined), 0);
});

test("userPathValidationError mirrors the backend rules", () => {
  assert.notEqual(userPathValidationError(""), "");
  assert.notEqual(userPathValidationError("/acme/../eng"), "");
  assert.notEqual(userPathValidationError("/acme/a:b"), "");
  assert.equal(userPathValidationError("acme/eng"), "");
});

test("buildUpsertUserPayload prefixes the slash, parses selectors, and drops an empty description", () => {
  assert.deepEqual(defaultUserForm().allowed_models, []);
  const { payload, error } = buildUpsertUserPayload({
    ...defaultUserForm(),
    user_path: "acme/eng",
    allowed_models: ["anthropic/*", "openai/gpt-4o, anthropic/*"],
    description: "  ",
  });
  assert.equal(error, undefined);
  assert.deepEqual(payload, {
    user_path: "/acme/eng",
    allowed_models: ["anthropic/*", "openai/gpt-4o"],
    description: undefined,
  });
  assert.ok(buildUpsertUserPayload({ ...defaultUserForm(), user_path: "" }).error);
});

test("userSelectorOptions lists provider wildcards first, then concrete selectors, de-duplicated", () => {
  const options = userSelectorOptions([
    { provider_name: "openai", access: { selector: "openai/gpt-4o" } },
    { provider_name: "anthropic", access: { selector: "anthropic/claude-sonnet-4-6" } },
    { provider_name: "openai", access: { selector: "openai/gpt-4o" } },
    { provider_name: "", selector: "" },
  ]);
  assert.deepEqual(
    options.map((option) => option.value),
    ["anthropic/*", "openai/*", "anthropic/claude-sonnet-4-6", "openai/gpt-4o"],
  );
  assert.ok(options[0].description.includes("anthropic"));
  assert.equal(options[2].description, "");
  assert.deepEqual(userSelectorOptions(undefined), []);
});

test("selectorMatchesModel mirrors the backend matcher", () => {
  assert.equal(selectorMatchesModel("*", "openai/gpt-4o"), true);
  assert.equal(selectorMatchesModel("openai/*", "openai/gpt-4o"), true);
  assert.equal(selectorMatchesModel("openai/", "openai/gpt-4o"), true);
  assert.equal(selectorMatchesModel("openai/*", "anthropic/claude"), false);
  assert.equal(selectorMatchesModel("openai/gpt-4o", "openai/gpt-4o"), true);
  assert.equal(selectorMatchesModel("openai/gpt-4o", "openai/gpt-4o-mini"), false);
  assert.equal(selectorMatchesModel("gpt-4o", "azure/gpt-4o"), true);
  assert.equal(selectorMatchesModel("gpt-4o", "openai/gpt-4o-mini"), false);
  assert.equal(selectorMatchesModel("", "openai/gpt-4o"), false);
});

test("parentEffectiveModels finds the nearest ancestor with a computed list", () => {
  const nodes = [
    node({ user_path: "/", effective_models: ["openai/gpt-4o", "anthropic/claude"] }),
    node({ user_path: "/sales", effective_models: ["anthropic/claude"] }),
    node({ user_path: "/sales/john", effective_models: [] }),
  ];
  assert.deepEqual(parentEffectiveModels(nodes, "/sales/john"), ["anthropic/claude"]);
  assert.deepEqual(parentEffectiveModels(nodes, "sales/new"), ["anthropic/claude"]);
  assert.deepEqual(parentEffectiveModels(nodes, "/eng/alice"), ["openai/gpt-4o", "anthropic/claude"]);
  assert.equal(parentEffectiveModels([], "/sales/john"), null);
});

test("previewEffectiveModels intersects the parent's models with the typed selectors", () => {
  const parent = ["anthropic/claude", "anthropic/opus"];
  assert.deepEqual(previewEffectiveModels(parent, ["openai/*"]), []);
  assert.deepEqual(previewEffectiveModels(parent, ["anthropic/opus"]), ["anthropic/opus"]);
  assert.deepEqual(previewEffectiveModels(parent, []), parent);
});

test("filterUserNodes keeps inactive ancestor when descendant is visible, and countInactiveUserNodes excludes it", () => {
  // Inactive parent (/acme) with active child → parent stays visible and is NOT counted hidden.
  // Child /acme/eng/deep is inactive, no active descendants → hidden and counted.
  const nodes = [
    node({ user_path: "/acme", key_count: 1, active_key_count: 0 }),
    node({ user_path: "/acme/eng", key_count: 1, active_key_count: 1 }),
    node({ user_path: "/acme/eng/deep", key_count: 1, active_key_count: 0 }),
    node({ user_path: "/policies", key_count: 0 }),
  ];
  const filtered = filterUserNodes(nodes, "");
  assert.deepEqual(
    filtered.map((n) => n.user_path),
    ["/acme", "/acme/eng", "/policies"],
  );
  // /acme is retained (anchors visible /acme/eng); /acme/eng/deep is hidden.
  assert.equal(countInactiveUserNodes(nodes), 1);
});

test("countInactiveUserNodes counts fully inactive subtrees", () => {
  const nodes = [
    node({ user_path: "/teams", key_count: 1, active_key_count: 0 }),
    node({ user_path: "/teams/old", key_count: 1, active_key_count: 0 }),
  ];
  assert.equal(countInactiveUserNodes(nodes), 2);
  assert.deepEqual(
    filterUserNodes(nodes, ""),
    [],
  );
});
