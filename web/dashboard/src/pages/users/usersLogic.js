// Pure page logic for the Users page: group-tree building, group chain
// resolution, model-access status computation against virtual model access
// policies, and the PUT/DELETE plan for the grant-access editor. No Svelte
// imports so tests/users.test.js can run under node.

// ---- user path helpers ----

// pathSegments splits a normalized user path into its segments.
export function pathSegments(path) {
  return String(path || "")
    .split("/")
    .filter((segment) => segment !== "");
}

// pathAncestors returns deepest-to-root fallback candidates, mirroring the
// backend's core.UserPathAncestors.
export function pathAncestors(path) {
  const segments = pathSegments(path);
  const result = [];
  for (let i = segments.length; i >= 1; i--) {
    result.push("/" + segments.slice(0, i).join("/"));
  }
  result.push("/");
  return result;
}

// ---- group tree ----

// groupPathByName maps group names to their derived hierarchy paths. The API
// sends `path` on each group; when absent (older payloads, tests) the path is
// derived from the parent chain.
export function groupPathByName(groups) {
  const list = Array.isArray(groups) ? groups : [];
  const byName = new Map(list.map((group) => [group.name, group]));
  const paths = new Map();
  const resolve = (name, trail) => {
    if (paths.has(name)) {
      return paths.get(name);
    }
    const group = byName.get(name);
    if (!group) {
      return "";
    }
    if (group.path) {
      paths.set(name, group.path);
      return group.path;
    }
    let path = "/" + name;
    if (group.parent && !trail.has(group.parent)) {
      trail.add(name);
      const parentPath = resolve(group.parent, trail);
      if (parentPath) {
        path = parentPath + "/" + name;
      }
    }
    paths.set(name, path);
    return path;
  };
  for (const group of list) {
    resolve(group.name, new Set([group.name]));
  }
  return paths;
}

// buildRegistryTree lays the group tree out as ordered rows, with each
// group's member users directly beneath it (root users first, at depth 0).
// Rows: { kind: "group"|"user", path, depth, group | user }.
export function buildRegistryTree(groups, users) {
  const groupList = Array.isArray(groups) ? groups : [];
  const paths = groupPathByName(groupList);
  const childGroups = new Map();
  for (const group of groupList) {
    const parent = group.parent || "";
    if (!childGroups.has(parent)) {
      childGroups.set(parent, []);
    }
    childGroups.get(parent).push(group);
  }
  const usersByGroup = new Map();
  for (const user of Array.isArray(users) ? users : []) {
    const owner = user.group || "";
    if (!usersByGroup.has(owner)) {
      usersByGroup.set(owner, []);
    }
    usersByGroup.get(owner).push(user);
  }
  const byName = (a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0);
  for (const list of childGroups.values()) {
    list.sort(byName);
  }
  for (const list of usersByGroup.values()) {
    list.sort(byName);
  }

  const rows = [];
  const visited = new Set();
  const visit = (parentName, depth) => {
    for (const user of usersByGroup.get(parentName) || []) {
      rows.push({ kind: "user", path: user.user_path, depth, user });
    }
    for (const group of childGroups.get(parentName) || []) {
      if (visited.has(group.name)) {
        continue;
      }
      visited.add(group.name);
      rows.push({ kind: "group", path: paths.get(group.name) || "/" + group.name, depth, group });
      visit(group.name, depth + 1);
    }
  };
  visit("", 0);
  // Groups whose parent chain is broken (out-of-band drift) still show up.
  for (const group of groupList) {
    if (!visited.has(group.name)) {
      visited.add(group.name);
      rows.push({ kind: "group", path: paths.get(group.name) || "/" + group.name, depth: 0, group });
      visit(group.name, 1);
    }
  }
  return rows;
}

// filterTreeRows narrows tree rows by a text query on path, names, and
// descriptions, keeping ancestors of matches so the tree stays connected.
export function filterTreeRows(rows, query) {
  const q = String(query || "").trim().toLowerCase();
  if (!q) {
    return rows;
  }
  const keep = new Set();
  for (const row of rows) {
    const source = row.user || row.group;
    const haystack = [row.path, source && source.name, source && source.description]
      .filter(Boolean)
      .join(" ")
      .toLowerCase();
    if (haystack.includes(q)) {
      for (const ancestor of pathAncestors(row.path)) {
        keep.add(ancestor);
      }
    }
  }
  return rows.filter((row) => keep.has(row.path));
}

// keyCountsByPath counts active managed API keys bound to each exact user
// path. User-bound keys report the user's live path, so they count here too.
export function keyCountsByPath(keys) {
  const counts = new Map();
  for (const key of Array.isArray(keys) ? keys : []) {
    if (!key || key.active === false) {
      continue;
    }
    const path = String(key.user_path || "").trim();
    if (!path) {
      continue;
    }
    counts.set(path, (counts.get(path) || 0) + 1);
  }
  return counts;
}

// groupMemberCounts counts direct member users per group name.
export function groupMemberCounts(users) {
  const counts = new Map();
  for (const user of Array.isArray(users) ? users : []) {
    if (user.group) {
      counts.set(user.group, (counts.get(user.group) || 0) + 1);
    }
  }
  return counts;
}

// groupsForPath resolves the group chain a path carries: every group whose
// derived path is the path itself or an ancestor, mirroring the backend's
// users.Service.GroupsForPath.
export function groupsForPath(groups, path) {
  const paths = groupPathByName(groups);
  const nameByPath = new Map();
  for (const [name, groupPath] of paths) {
    nameByPath.set(groupPath, name);
  }
  const merged = new Set();
  for (const ancestor of pathAncestors(path)) {
    if (nameByPath.has(ancestor)) {
      merged.add(nameByPath.get(ancestor));
    }
  }
  return Array.from(merged).sort();
}

// parentGroupOptions lists the groups that can be another group's parent:
// everything except the group itself and its descendants (which would form a
// cycle).
export function parentGroupOptions(groups, name) {
  const list = Array.isArray(groups) ? groups : [];
  if (!name) {
    return list;
  }
  const paths = groupPathByName(list);
  const selfPath = paths.get(name) || "/" + name;
  return list.filter((group) => {
    const path = paths.get(group.name) || "";
    return group.name !== name && path !== selfPath && !path.startsWith(selfPath + "/");
  });
}

// ---- access policies ----

// accessPoliciesBySource indexes access-policy views (kind === "policy") by
// their source selector.
export function accessPoliciesBySource(views) {
  const map = new Map();
  for (const view of Array.isArray(views) ? views : []) {
    if (view && view.kind === "policy") {
      map.set(view.source, view);
    }
  }
  return map;
}

// modelSelector derives the policy selector for one /admin/models item.
export function modelSelector(model) {
  if (!model) {
    return "";
  }
  const id = String((model.model && model.model.id) || "").trim();
  if (!id) {
    return "";
  }
  const providerName = String(model.provider_name || "").trim();
  if (providerName) {
    return providerName + "/" + id;
  }
  const providerType = String(model.provider_type || "").trim();
  if (!providerType || id.includes("/")) {
    return id;
  }
  return providerType + "/" + id;
}

// subjectExplicitlyGranted reports whether the policy lists the subject
// verbatim (the state the grant editor toggles).
export function subjectExplicitlyGranted(policy, subject) {
  if (!policy || !subject) {
    return false;
  }
  if (subject.kind === "group") {
    return (policy.groups || []).includes(subject.name);
  }
  return (policy.user_paths || []).includes(subject.path);
}

// subjectAccessStatus resolves one model's effective availability for the
// subject:
//   "open"      — no restriction on this selector (no policy, or one with no
//                 paths/groups)
//   "disabled"  — the policy turns the model off for everyone
//   "granted"   — the subject is listed verbatim on the policy
//   "inherited" — the subject is covered via an ancestor path or a group in
//                 its chain
//   "blocked"   — the policy restricts the model and the subject is not in it
// subjectGroups is the subject's group chain (groupsForPath of its path).
export function subjectAccessStatus(policy, subject, subjectGroups) {
  if (!policy) {
    return "open";
  }
  if (policy.enabled === false) {
    return "disabled";
  }
  const paths = policy.user_paths || [];
  const groups = policy.groups || [];
  if (paths.length === 0 && groups.length === 0) {
    return "open";
  }
  if (subjectExplicitlyGranted(policy, subject)) {
    return "granted";
  }
  for (const ancestor of pathAncestors(subject.path)) {
    if (paths.includes(ancestor)) {
      return "inherited";
    }
  }
  for (const group of subjectGroups || []) {
    if (groups.includes(group)) {
      return "inherited";
    }
  }
  return "blocked";
}

// buildAccessRows assembles the grant-access editor rows for a subject: one
// row per concrete model with its policy, status, and initial checkbox state.
export function buildAccessRows({ models, views, subject, groups }) {
  const policies = accessPoliciesBySource(views);
  const subjectGroups = subject ? groupsForPath(groups, subject.path) : [];
  const rows = [];
  const seen = new Set();
  for (const model of Array.isArray(models) ? models : []) {
    const selector = modelSelector(model);
    if (!selector || seen.has(selector)) {
      continue;
    }
    seen.add(selector);
    const policy = policies.get(selector) || null;
    const status = subjectAccessStatus(policy, subject, subjectGroups);
    rows.push({
      selector,
      policy,
      status,
      managed: Boolean(policy && policy.managed),
      checked: status === "granted",
      initialChecked: status === "granted",
    });
  }
  rows.sort((a, b) => (a.selector < b.selector ? -1 : a.selector > b.selector ? 1 : 0));
  return rows;
}

// buildAccessSavePlan diffs the editor rows into virtual-model mutations:
//   { method: "PUT", payload }    — add/remove the subject on a policy
//   { method: "DELETE", payload } — drop a policy whose last subject was removed
// Removing the last subject deletes the policy, which reopens the model for
// every caller — the editor surfaces that warning before saving.
export function buildAccessSavePlan(rows, subject) {
  const plan = [];
  for (const row of Array.isArray(rows) ? rows : []) {
    if (row.checked === row.initialChecked || row.managed) {
      continue;
    }
    const policy = row.policy;
    let userPaths = (policy && policy.user_paths) || [];
    let groups = (policy && policy.groups) || [];
    if (subject.kind === "group") {
      groups = row.checked
        ? sortedUnique([...groups, subject.name])
        : groups.filter((name) => name !== subject.name);
    } else {
      userPaths = row.checked
        ? sortedUnique([...userPaths, subject.path])
        : userPaths.filter((path) => path !== subject.path);
    }

    if (!row.checked && userPaths.length === 0 && groups.length === 0) {
      plan.push({ method: "DELETE", payload: { source: row.selector }, reopens: true });
      continue;
    }

    const payload = {
      source: row.selector,
      user_paths: userPaths,
      groups,
      description: String((policy && policy.description) || ""),
      enabled: policy ? policy.enabled !== false : true,
    };
    if (policy && policy.slowdown != null) {
      payload.slowdown = policy.slowdown;
    }
    plan.push({
      method: "PUT",
      payload,
      restricts: row.checked && !policy,
    });
  }
  return plan;
}

function sortedUnique(values) {
  return Array.from(new Set(values)).sort();
}

// ---- editor forms ----

export function defaultUserForm() {
  return { id: "", name: "", description: "", group: "" };
}

export function userFormFromUser(user) {
  return {
    id: user.id || "",
    name: user.name || "",
    description: user.description || "",
    group: user.group || "",
  };
}

export function buildUserPayload(form) {
  const name = String(form.name || "").trim();
  if (!name) {
    return { error: "name_required" };
  }
  if (name.includes("/")) {
    return { error: "name_invalid" };
  }
  const payload = {
    name,
    description: String(form.description || "").trim(),
    group: String(form.group || "").trim(),
  };
  if (form.id) {
    payload.id = form.id;
  }
  return { payload };
}

export function defaultGroupForm() {
  return { name: "", original: "", description: "", parent: "" };
}

export function buildGroupPayload(form) {
  const name = String(form.name || "").trim();
  if (!name) {
    return { error: "name_required" };
  }
  if (name.includes("/") || name.includes(",")) {
    return { error: "name_invalid" };
  }
  const parent = String(form.parent || "").trim();
  if (parent && parent === name) {
    return { error: "parent_invalid" };
  }
  return {
    payload: { name, description: String(form.description || "").trim(), parent },
  };
}
