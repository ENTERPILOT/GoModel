import { test } from "node:test";
import assert from "node:assert/strict";

import {
  buildAccessRows,
  buildAccessSavePlan,
  buildGroupPayload,
  buildUserPayload,
  buildUserTree,
  filterTreeRows,
  groupMemberCounts,
  groupsForPath,
  keyCountsByPath,
  modelSelector,
  pathAncestors,
  subjectAccessStatus,
} from "../src/pages/users/usersLogic.js";

const users = [
  { id: "1", user_path: "/team", name: "Team", groups: ["premium"] },
  { id: "2", user_path: "/team/alpha/svc", name: "Alpha Svc", groups: ["beta-testers"] },
  { id: "3", user_path: "/sales", name: "Sales", groups: [] },
];

test("pathAncestors walks deepest to root", () => {
  assert.deepEqual(pathAncestors("/a/b"), ["/a/b", "/a", "/"]);
  assert.deepEqual(pathAncestors("/"), ["/"]);
});

test("buildUserTree inserts missing intermediate segments and orders children under parents", () => {
  const rows = buildUserTree(users);
  assert.deepEqual(
    rows.map((row) => row.path),
    ["/sales", "/team", "/team/alpha", "/team/alpha/svc"],
  );
  const alpha = rows.find((row) => row.path === "/team/alpha");
  assert.equal(alpha.user, null);
  assert.equal(alpha.depth, 1);
  assert.equal(rows.find((row) => row.path === "/team/alpha/svc").depth, 2);
  assert.equal(rows.find((row) => row.path === "/team").user.name, "Team");
});

test("filterTreeRows keeps ancestors of matches", () => {
  const rows = buildUserTree(users);
  const filtered = filterTreeRows(rows, "beta-testers");
  assert.deepEqual(
    filtered.map((row) => row.path),
    ["/team", "/team/alpha", "/team/alpha/svc"],
  );
  assert.equal(filterTreeRows(rows, ""), rows);
});

test("keyCountsByPath counts only active keys with a path", () => {
  const counts = keyCountsByPath([
    { user_path: "/team", active: true },
    { user_path: "/team", active: true },
    { user_path: "/team", active: false },
    { user_path: "", active: true },
  ]);
  assert.equal(counts.get("/team"), 2);
  assert.equal(counts.size, 1);
});

test("groupMemberCounts and groupsForPath resolve memberships", () => {
  assert.equal(groupMemberCounts(users).get("premium"), 1);
  assert.deepEqual(groupsForPath(users, "/team/alpha/svc/deep"), ["beta-testers", "premium"]);
  assert.deepEqual(groupsForPath(users, "/sales"), []);
});

test("modelSelector prefers provider_name over provider_type", () => {
  assert.equal(modelSelector({ model: { id: "gpt-4o" }, provider_name: "openai_main", provider_type: "openai" }), "openai_main/gpt-4o");
  assert.equal(modelSelector({ model: { id: "gpt-4o" }, provider_type: "openai" }), "openai/gpt-4o");
  assert.equal(modelSelector({ model: { id: "" } }), "");
});

test("subjectAccessStatus resolves effective access", () => {
  const userSubject = { kind: "user", path: "/team/alpha/svc" };
  assert.equal(subjectAccessStatus(null, userSubject, []), "open");
  assert.equal(subjectAccessStatus({ enabled: false }, userSubject, []), "disabled");
  assert.equal(subjectAccessStatus({ enabled: true, user_paths: [], groups: [] }, userSubject, []), "open");
  assert.equal(
    subjectAccessStatus({ enabled: true, user_paths: ["/team/alpha/svc"] }, userSubject, []),
    "granted",
  );
  assert.equal(
    subjectAccessStatus({ enabled: true, user_paths: ["/team"] }, userSubject, []),
    "inherited",
  );
  assert.equal(
    subjectAccessStatus({ enabled: true, groups: ["beta-testers"] }, userSubject, ["beta-testers"]),
    "inherited",
  );
  assert.equal(
    subjectAccessStatus({ enabled: true, user_paths: ["/sales"] }, userSubject, []),
    "blocked",
  );
  const groupSubject = { kind: "group", name: "beta-testers" };
  assert.equal(
    subjectAccessStatus({ enabled: true, groups: ["beta-testers"] }, groupSubject, []),
    "granted",
  );
  assert.equal(
    subjectAccessStatus({ enabled: true, user_paths: ["/x"] }, groupSubject, []),
    "blocked",
  );
});

test("buildAccessRows marks explicit grants checked and managed rows locked", () => {
  const rows = buildAccessRows({
    models: [
      { model: { id: "gpt-4o" }, provider_name: "openai" },
      { model: { id: "claude" }, provider_name: "anthropic" },
    ],
    views: [
      { kind: "policy", source: "openai/gpt-4o", enabled: true, user_paths: ["/team"], managed: true },
      { kind: "redirect", source: "fast", targets: [{ model: "gpt-4o" }] },
    ],
    subject: { kind: "user", path: "/team" },
    users,
  });
  assert.deepEqual(rows.map((row) => row.selector), ["anthropic/claude", "openai/gpt-4o"]);
  const gpt = rows[1];
  assert.equal(gpt.checked, true);
  assert.equal(gpt.managed, true);
  assert.equal(rows[0].status, "open");
});

test("buildAccessSavePlan adds, removes, and deletes policies", () => {
  const subject = { kind: "user", path: "/team" };
  const plan = buildAccessSavePlan(
    [
      // newly granted, no policy yet -> restricting PUT
      { selector: "openai/gpt-4o", policy: null, checked: true, initialChecked: false, managed: false },
      // granted on an existing policy -> merged PUT
      {
        selector: "anthropic/claude",
        policy: { source: "anthropic/claude", enabled: true, user_paths: ["/sales"], groups: ["g"], description: "d" },
        checked: true,
        initialChecked: false,
        managed: false,
      },
      // last subject removed -> DELETE
      {
        selector: "openai/o3",
        policy: { source: "openai/o3", enabled: true, user_paths: ["/team"], groups: [] },
        checked: false,
        initialChecked: true,
        managed: false,
      },
      // unchanged and managed rows are skipped
      { selector: "x/y", policy: null, checked: false, initialChecked: false, managed: false },
      { selector: "m/n", policy: null, checked: true, initialChecked: false, managed: true },
    ],
    subject,
  );
  assert.equal(plan.length, 3);
  assert.deepEqual(plan[0], {
    method: "PUT",
    payload: { source: "openai/gpt-4o", user_paths: ["/team"], groups: [], description: "", enabled: true },
    restricts: true,
  });
  assert.deepEqual(plan[1].payload.user_paths, ["/sales", "/team"]);
  assert.deepEqual(plan[1].payload.groups, ["g"]);
  assert.equal(plan[1].restricts, false);
  assert.deepEqual(plan[2], { method: "DELETE", payload: { source: "openai/o3" }, reopens: true });
});

test("buildAccessSavePlan toggles group subjects on the groups list", () => {
  const subject = { kind: "group", name: "beta-testers" };
  const plan = buildAccessSavePlan(
    [
      {
        selector: "openai/gpt-4o",
        policy: { source: "openai/gpt-4o", enabled: false, user_paths: ["/x"], groups: [] },
        checked: true,
        initialChecked: false,
        managed: false,
      },
    ],
    subject,
  );
  assert.deepEqual(plan[0].payload.groups, ["beta-testers"]);
  assert.deepEqual(plan[0].payload.user_paths, ["/x"]);
  assert.equal(plan[0].payload.enabled, false);
});

test("payload builders validate input", () => {
  assert.equal(buildUserPayload({ user_path: " " }).error, "user_path_required");
  const built = buildUserPayload({ id: "1", user_path: " /a ", name: " n ", description: "", groups: ["g"] });
  assert.deepEqual(built.payload, { id: "1", user_path: "/a", name: "n", description: "", groups: ["g"] });

  assert.equal(buildGroupPayload({ name: "" }).error, "name_required");
  assert.equal(buildGroupPayload({ name: "a/b" }).error, "name_invalid");
  assert.deepEqual(buildGroupPayload({ name: " g ", description: " d " }).payload, { name: "g", description: "d" });
});
