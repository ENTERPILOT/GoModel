import { test } from "node:test";
import assert from "node:assert/strict";

import {
  buildAccessRows,
  buildAccessSavePlan,
  buildGroupPayload,
  buildRegistryTree,
  buildUserPayload,
  filterTreeRows,
  groupMemberCounts,
  groupPathByName,
  groupsForPath,
  keyCountsByPath,
  modelSelector,
  parentGroupOptions,
  pathAncestors,
  subjectAccessStatus,
} from "../src/pages/users/usersLogic.js";

const groups = [
  { name: "engineering", parent: "", path: "/engineering" },
  { name: "platform", parent: "engineering", path: "/engineering/platform" },
  { name: "sales", parent: "" },
];

const users = [
  { id: "1", user_path: "/engineering/platform/anna", name: "anna", group: "platform" },
  { id: "2", user_path: "/engineering/bob", name: "bob", group: "engineering" },
  { id: "3", user_path: "/admin", name: "admin", group: "" },
];

test("pathAncestors walks deepest to root", () => {
  assert.deepEqual(pathAncestors("/a/b"), ["/a/b", "/a", "/"]);
  assert.deepEqual(pathAncestors("/"), ["/"]);
});

test("groupPathByName uses the API path and derives when absent", () => {
  const paths = groupPathByName(groups);
  assert.equal(paths.get("platform"), "/engineering/platform");
  assert.equal(paths.get("sales"), "/sales");
});

test("buildRegistryTree nests users under their group in tree order", () => {
  const rows = buildRegistryTree(groups, users);
  assert.deepEqual(
    rows.map((row) => row.kind + ":" + row.path),
    [
      "user:/admin",
      "group:/engineering",
      "user:/engineering/bob",
      "group:/engineering/platform",
      "user:/engineering/platform/anna",
      "group:/sales",
    ],
  );
  assert.equal(rows.find((row) => row.path === "/engineering/platform").depth, 1);
  assert.equal(rows.find((row) => row.path === "/engineering/platform/anna").depth, 2);
  assert.equal(rows.find((row) => row.path === "/admin").depth, 0);
});

test("buildRegistryTree keeps groups with a broken parent chain visible", () => {
  const rows = buildRegistryTree([{ name: "orphan", parent: "gone" }], []);
  assert.deepEqual(rows.map((row) => row.path), ["/orphan"]);
});

test("filterTreeRows keeps ancestors of matches", () => {
  const rows = buildRegistryTree(groups, users);
  const filtered = filterTreeRows(rows, "anna");
  assert.deepEqual(
    filtered.map((row) => row.path),
    ["/engineering", "/engineering/platform", "/engineering/platform/anna"],
  );
  assert.equal(filterTreeRows(rows, ""), rows);
});

test("keyCountsByPath counts only active keys with a path", () => {
  const counts = keyCountsByPath([
    { user_path: "/engineering/bob", active: true },
    { user_path: "/engineering/bob", active: true },
    { user_path: "/engineering/bob", active: false },
    { user_path: "", active: true },
  ]);
  assert.equal(counts.get("/engineering/bob"), 2);
  assert.equal(counts.size, 1);
});

test("groupMemberCounts counts direct members", () => {
  const counts = groupMemberCounts(users);
  assert.equal(counts.get("platform"), 1);
  assert.equal(counts.get("engineering"), 1);
  assert.equal(counts.has(""), false);
});

test("groupsForPath matches the group chain by path prefix", () => {
  assert.deepEqual(groupsForPath(groups, "/engineering/platform/anna/service"), [
    "engineering",
    "platform",
  ]);
  assert.deepEqual(groupsForPath(groups, "/engineering"), ["engineering"]);
  assert.deepEqual(groupsForPath(groups, "/other"), []);
});

test("parentGroupOptions excludes self and descendants", () => {
  const options = parentGroupOptions(groups, "engineering").map((group) => group.name);
  assert.deepEqual(options, ["sales"]);
  assert.equal(parentGroupOptions(groups, "").length, 3);
});

test("modelSelector prefers provider_name and falls back to provider_type", () => {
  assert.equal(modelSelector({ provider_name: "openai_a", model: { id: "gpt-4o" } }), "openai_a/gpt-4o");
  assert.equal(modelSelector({ provider_type: "openai", model: { id: "gpt-4o" } }), "openai/gpt-4o");
  assert.equal(modelSelector({ provider_type: "openai", model: { id: "x/y" } }), "x/y");
  assert.equal(modelSelector({ model: { id: "" } }), "");
});

test("subjectAccessStatus resolves effective availability", () => {
  const anna = { kind: "user", path: "/engineering/platform/anna" };
  const annaGroups = groupsForPath(groups, anna.path);
  assert.equal(subjectAccessStatus(null, anna, annaGroups), "open");
  assert.equal(subjectAccessStatus({ enabled: false }, anna, annaGroups), "disabled");
  assert.equal(subjectAccessStatus({ user_paths: [], groups: [] }, anna, annaGroups), "open");
  assert.equal(
    subjectAccessStatus({ user_paths: ["/engineering/platform/anna"] }, anna, annaGroups),
    "granted",
  );
  assert.equal(subjectAccessStatus({ user_paths: ["/engineering"] }, anna, annaGroups), "inherited");
  assert.equal(subjectAccessStatus({ groups: ["engineering"] }, anna, annaGroups), "inherited");
  assert.equal(subjectAccessStatus({ user_paths: ["/sales"] }, anna, annaGroups), "blocked");

  // A subgroup inherits a grant made to its ancestor group.
  const platform = { kind: "group", name: "platform", path: "/engineering/platform" };
  const platformGroups = groupsForPath(groups, platform.path);
  assert.equal(subjectAccessStatus({ groups: ["platform"] }, platform, platformGroups), "granted");
  assert.equal(subjectAccessStatus({ groups: ["engineering"] }, platform, platformGroups), "inherited");
  assert.equal(subjectAccessStatus({ user_paths: ["/engineering"] }, platform, platformGroups), "inherited");
});

test("buildAccessRows derives checkbox state from explicit grants", () => {
  const rows = buildAccessRows({
    models: [
      { provider_name: "openai_a", model: { id: "gpt-4o" } },
      { provider_name: "openai_a", model: { id: "gpt-4o" } },
      { provider_name: "groq_a", model: { id: "llama" } },
    ],
    views: [
      { kind: "policy", source: "openai_a/gpt-4o", user_paths: ["/engineering/bob"], groups: [] },
      { kind: "redirect", source: "groq_a/llama" },
    ],
    subject: { kind: "user", path: "/engineering/bob" },
    groups,
  });
  assert.deepEqual(
    rows.map((row) => [row.selector, row.status, row.checked]),
    [
      ["groq_a/llama", "open", false],
      ["openai_a/gpt-4o", "granted", true],
    ],
  );
});

test("buildAccessSavePlan diffs rows into PUT and DELETE steps", () => {
  const subject = { kind: "user", path: "/engineering/bob" };
  const plan = buildAccessSavePlan(
    [
      // newly granted, no policy yet -> restricting PUT
      { selector: "a/m1", policy: null, checked: true, initialChecked: false, managed: false },
      // unchanged -> skipped
      { selector: "a/m2", policy: null, checked: false, initialChecked: false, managed: false },
      // removed last subject -> DELETE (reopens)
      {
        selector: "a/m3",
        policy: { user_paths: ["/engineering/bob"], groups: [] },
        checked: false,
        initialChecked: true,
        managed: false,
      },
      // removed but group remains -> PUT
      {
        selector: "a/m4",
        policy: { user_paths: ["/engineering/bob"], groups: ["premium"], enabled: true },
        checked: false,
        initialChecked: true,
        managed: false,
      },
      // managed rows never change
      { selector: "a/m5", policy: null, checked: true, initialChecked: false, managed: true },
    ],
    subject,
  );
  assert.deepEqual(
    plan.map((step) => [step.method, step.payload.source]),
    [
      ["PUT", "a/m1"],
      ["DELETE", "a/m3"],
      ["PUT", "a/m4"],
    ],
  );
  assert.equal(plan[0].restricts, true);
  assert.deepEqual(plan[0].payload.user_paths, ["/engineering/bob"]);
  assert.equal(plan[1].reopens, true);
  assert.deepEqual(plan[2].payload.user_paths, []);
  assert.deepEqual(plan[2].payload.groups, ["premium"]);
});

test("buildAccessSavePlan toggles group membership for group subjects", () => {
  const subject = { kind: "group", name: "platform", path: "/engineering/platform" };
  const plan = buildAccessSavePlan(
    [
      {
        selector: "a/m1",
        policy: { user_paths: ["/x"], groups: [], enabled: true },
        checked: true,
        initialChecked: false,
        managed: false,
      },
    ],
    subject,
  );
  assert.deepEqual(plan[0].payload.groups, ["platform"]);
  assert.deepEqual(plan[0].payload.user_paths, ["/x"]);
});

test("buildUserPayload validates the name", () => {
  assert.deepEqual(buildUserPayload({ name: " anna ", group: "platform", description: " d " }), {
    payload: { name: "anna", description: "d", group: "platform" },
  });
  assert.equal(buildUserPayload({ name: "" }).error, "name_required");
  assert.equal(buildUserPayload({ name: "a/b" }).error, "name_invalid");
  assert.equal(buildUserPayload({ id: "u1", name: "x" }).payload.id, "u1");
});

test("buildGroupPayload validates name and parent", () => {
  assert.deepEqual(buildGroupPayload({ name: " eng ", description: "", parent: " " }), {
    payload: { name: "eng", description: "", parent: "" },
  });
  assert.equal(buildGroupPayload({ name: "" }).error, "name_required");
  assert.equal(buildGroupPayload({ name: "a/b" }).error, "name_invalid");
  assert.equal(buildGroupPayload({ name: "a,b" }).error, "name_invalid");
  assert.equal(buildGroupPayload({ name: "eng", parent: "eng" }).error, "parent_invalid");
});
