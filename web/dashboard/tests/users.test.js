import test from "node:test";
import assert from "node:assert/strict";

import {
  buildUpsertUserPayload,
  defaultUserForm,
  filterUserNodes,
  sortUserNodes,
  userNodeKind,
  userNodeRestricted,
  userPathDepth,
  userPathLeaf,
  userPathValidationError,
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
    ...overrides,
  };
}

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

test("userPathValidationError mirrors the backend rules", () => {
  assert.notEqual(userPathValidationError(""), "");
  assert.notEqual(userPathValidationError("/acme/../eng"), "");
  assert.notEqual(userPathValidationError("/acme/a:b"), "");
  assert.equal(userPathValidationError("acme/eng"), "");
});

test("buildUpsertUserPayload prefixes the slash, parses selectors, and drops an empty description", () => {
  const { payload, error } = buildUpsertUserPayload({
    ...defaultUserForm(),
    user_path: "acme/eng",
    allowed_models: "anthropic/*\nopenai/gpt-4o",
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
