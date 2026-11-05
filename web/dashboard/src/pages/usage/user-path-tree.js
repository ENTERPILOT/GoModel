// User path tree for the usage filter: known paths grouped by their common
// prefix segments, so /team/alice/laptop and /team/alice/ci sit under
// /team -> /team/alice. Selecting a node filters its whole subtree, the same
// way the API treats every user_path value. Pure functions for node:test.

import { fuzzyMatch, normalizeQuery, substringIndex } from "../../lib/utils/textMatch.js";

// normalizeUserPath mirrors the gateway's canonical form: leading slash, no
// empty or trailing segments. Returns "" for blank input.
export function normalizeUserPath(raw) {
  const segments = String(raw || "")
    .split("/")
    .map((segment) => segment.trim())
    .filter(Boolean);
  if (segments.length === 0) return String(raw || "").trim() ? "/" : "";
  return "/" + segments.join("/");
}

// buildUserPathTree turns a flat path list into nested nodes
// { path, name, children }, sorted by name. Intermediate prefixes become
// nodes even when no request used them directly. With a scope (a scoped
// key's subtree root) the tree starts at that root and paths outside it are
// dropped, so no ancestor the key may not filter by is offered.
export function buildUserPathTree(paths, scope = "/") {
  const rootPath = normalizeUserPath(scope) || "/";
  const root = { path: rootPath, name: rootPath, children: [] };
  const byPath = new Map([[rootPath, root]]);
  const base = rootPath === "/" ? "" : rootPath;
  for (const raw of paths || []) {
    const path = normalizeUserPath(raw);
    if (!path || path === rootPath || !userPathWithin(path, rootPath)) continue;
    let parent = root;
    let prefix = base;
    for (const segment of path.slice(base.length + 1).split("/")) {
      prefix += "/" + segment;
      let node = byPath.get(prefix);
      if (!node) {
        node = { path: prefix, name: segment, children: [] };
        byPath.set(prefix, node);
        parent.children.push(node);
      }
      parent = node;
    }
  }
  sortTree(root);
  return rootPath === "/" ? root.children : [root];
}

// userPathWithin reports whether path is scope or lies below it.
export function userPathWithin(path, scope) {
  return scope === "/" || path === scope || path.startsWith(scope + "/");
}

function sortTree(node) {
  node.children.sort((a, b) => a.name.localeCompare(b.name));
  node.children.forEach(sortTree);
}

// visibleUserPathRows flattens the tree into the rows the picker shows:
// { path, name, depth, hasChildren, expanded }. Without a query only
// expanded nodes open; with one, matching paths and their ancestors show,
// fully expanded. The query falls back to the fuzzy level only when no path
// matches by substring, the same rule the flat filters follow.
export function visibleUserPathRows(tree, expanded, query) {
  const needle = normalizeQuery(query);
  const collect = (matchesPath) => {
    const rows = [];
    const keeps = (node) => matchesPath(node.path) || node.children.some(keeps);
    const walk = (nodes, depth) => {
      for (const node of nodes) {
        if (!keeps(node)) continue;
        const hasChildren = node.children.length > 0;
        const open = hasChildren && (needle ? true : expanded.has(node.path));
        rows.push({ path: node.path, name: node.name, depth, hasChildren, expanded: open });
        if (open) walk(node.children, depth + 1);
      }
    };
    walk(tree || [], 0);
    return rows;
  };
  if (!needle) return collect(() => true);
  const bySubstring = collect((path) => substringIndex(path, needle) >= 0);
  if (bySubstring.length) return bySubstring;
  return collect((path) => fuzzyMatch(path, needle) !== null);
}

// userPathCovered reports whether path is already inside a selected
// subtree, so the picker can show it as included without a separate pick.
export function userPathCovered(path, selected) {
  return (selected || []).some(
    (value) => value !== path && (value === "/" || path.startsWith(value + "/")),
  );
}

// customUserPath returns the typed query as a path to offer when it is not
// already a known node or selection and lies inside scope, or "" when there
// is nothing to offer.
export function customUserPath(query, tree, selected, scope = "/") {
  const path = normalizeUserPath(query);
  if (!path || (selected || []).includes(path) || !userPathWithin(path, normalizeUserPath(scope) || "/")) return "";
  const known = (nodes) => nodes.some((node) => node.path === path || known(node.children));
  return known(tree || []) ? "" : path;
}
