// Pure page logic for the Users page: user-path tree building, group
// membership resolution, model-access status computation against virtual
// model access policies, and the PUT/DELETE plan for the grant-access
// editor. No Svelte imports so tests/users.test.js can run under node.

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

// ---- user tree ----

// buildUserTree lays registered users out as a tree ordered by path. Missing
// intermediate path nodes appear as plain segments so /team/alpha/svc nests
// under /team even when /team has no user record. Rows:
//   { path, depth, segment, user | null }
export function buildUserTree(users) {
  const byPath = new Map();
  for (const user of Array.isArray(users) ? users : []) {
    byPath.set(user.user_path, user);
  }

  // Every path that must appear: registered paths plus their ancestors that
  // are prefixes of at least one registered path.
  const paths = new Set();
  for (const path of byPath.keys()) {
    const segments = pathSegments(path);
    for (let i = 1; i <= segments.length; i++) {
      paths.add("/" + segments.slice(0, i).join("/"));
    }
    if (segments.length === 0) {
      paths.add("/");
    }
  }

  const sorted = Array.from(paths).sort(comparePaths);
  return sorted.map((path) => {
    const segments = pathSegments(path);
    return {
      path,
      depth: Math.max(segments.length - 1, 0),
      segment: segments.length ? segments[segments.length - 1] : "/",
      user: byPath.get(path) || null,
    };
  });
}

// comparePaths orders paths so children always follow their parent
// (lexicographic on segment lists).
function comparePaths(a, b) {
  const left = pathSegments(a);
  const right = pathSegments(b);
  const len = Math.min(left.length, right.length);
  for (let i = 0; i < len; i++) {
    if (left[i] !== right[i]) {
      return left[i] < right[i] ? -1 : 1;
    }
  }
  return left.length - right.length;
}

// filterTreeRows narrows tree rows by a text query on path, name, and groups,
// keeping ancestors of matches so the tree stays connected.
export function filterTreeRows(rows, query) {
  const q = String(query || "").trim().toLowerCase();
  if (!q) {
    return rows;
  }
  const keep = new Set();
  for (const row of rows) {
    const haystack = [
      row.path,
      row.user && row.user.name,
      ...((row.user && row.user.groups) || []),
    ]
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

// keyCountsByPath counts active managed API keys bound to each exact user path.
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

// groupMemberCounts counts direct members per group name.
export function groupMemberCounts(users) {
  const counts = new Map();
  for (const user of Array.isArray(users) ? users : []) {
    for (const group of user.groups || []) {
      counts.set(group, (counts.get(group) || 0) + 1);
    }
  }
  return counts;
}

// groupsForPath unions the group memberships along the path's ancestor
// chain, mirroring the backend's users.Service.GroupsForPath.
export function groupsForPath(users, path) {
  const byPath = new Map();
  for (const user of Array.isArray(users) ? users : []) {
    byPath.set(user.user_path, user);
  }
  const merged = new Set();
  for (const ancestor of pathAncestors(path)) {
    const user = byPath.get(ancestor);
    for (const group of (user && user.groups) || []) {
      merged.add(group);
    }
  }
  return Array.from(merged).sort();
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
//   "inherited" — a user subject is covered via an ancestor path or a group
//   "blocked"   — the policy restricts the model and the subject is not in it
// userGroups only applies to user subjects: the groups their path carries.
export function subjectAccessStatus(policy, subject, userGroups) {
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
  if (subject.kind === "user") {
    for (const ancestor of pathAncestors(subject.path)) {
      if (paths.includes(ancestor)) {
        return "inherited";
      }
    }
    for (const group of userGroups || []) {
      if (groups.includes(group)) {
        return "inherited";
      }
    }
  }
  return "blocked";
}

// buildAccessRows assembles the grant-access editor rows for a subject: one
// row per concrete model with its policy, status, and initial checkbox state.
export function buildAccessRows({ models, views, subject, users }) {
  const policies = accessPoliciesBySource(views);
  const userGroups =
    subject && subject.kind === "user" ? groupsForPath(users, subject.path) : [];
  const rows = [];
  const seen = new Set();
  for (const model of Array.isArray(models) ? models : []) {
    const selector = modelSelector(model);
    if (!selector || seen.has(selector)) {
      continue;
    }
    seen.add(selector);
    const policy = policies.get(selector) || null;
    const status = subjectAccessStatus(policy, subject, userGroups);
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
  return { id: "", user_path: "", name: "", description: "", groups: [] };
}

export function userFormFromUser(user) {
  return {
    id: user.id || "",
    user_path: user.user_path || "",
    name: user.name || "",
    description: user.description || "",
    groups: [...(user.groups || [])],
  };
}

export function buildUserPayload(form) {
  const userPath = String(form.user_path || "").trim();
  if (!userPath) {
    return { error: "user_path_required" };
  }
  const payload = {
    user_path: userPath,
    name: String(form.name || "").trim(),
    description: String(form.description || "").trim(),
    groups: [...(form.groups || [])],
  };
  if (form.id) {
    payload.id = form.id;
  }
  return { payload };
}

export function defaultGroupForm() {
  return { name: "", original: "", description: "" };
}

export function buildGroupPayload(form) {
  const name = String(form.name || "").trim();
  if (!name) {
    return { error: "name_required" };
  }
  if (name.includes("/") || name.includes(",")) {
    return { error: "name_invalid" };
  }
  return { payload: { name, description: String(form.description || "").trim() } };
}
